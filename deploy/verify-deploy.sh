#!/usr/bin/env bash
# verify-deploy.sh — end-to-end diagnostic for vosslabs/vask on Oracle.
#
# Walks every layer in order:
#   1. management ssh (port 22)              — proves VM is reachable at all
#   2. systemd service                       — proves the binary is running
#   3. socket binding                        — proves the binary owns port 2200
#   4. host firewall (iptables/ufw)          — proves the VM accepts tcp/2200
#   5. end-to-end reachability               — proves the world can reach 2200
#
# When something fails, the script prints exactly what to fix and stops
# wasting your time on lower-layer checks.
#
# Usage:
#   ./deploy/verify-deploy.sh
#
# Override defaults by exporting:
#   VM_IP, SSH_KEY, SSH_USER, PORT

set -uo pipefail   # intentionally NOT -e: keep checking after a failure

VM_IP="${VM_IP:-129.153.206.68}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/oracle-ask.key}"
SSH_USER="${SSH_USER:-ubuntu}"
SSH_PORT="${SSH_PORT:-22000}"   # management ssh after the port-22 cutover
SSH_TARGET="$SSH_USER@$VM_IP"
SSH_OPTS=(-i "$SSH_KEY" -p "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)
PORT="${PORT:-22}"              # ask on the canonical SSH port

# ===== logging =========================================================

c_brand=$'\033[38;5;208m'
c_dim=$'\033[38;5;240m'
c_ok=$'\033[38;5;72m'
c_err=$'\033[38;5;167m'
c_reset=$'\033[0m'

step() { printf "\n%s▶%s %s\n" "$c_brand" "$c_reset" "$*"; }
pass() { printf "  %sPASS%s %s\n" "$c_ok" "$c_reset" "$*"; }
fail() { printf "  %sFAIL%s %s\n" "$c_err" "$c_reset" "$*"; }
note() { printf "       %s%s%s\n" "$c_dim" "$*" "$c_reset"; }

failures=0
mark_fail() { failures=$((failures + 1)); }

# ===== 1. management ssh ==============================================

step "1/5  management ssh (port $SSH_PORT)"
if ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'echo ok' >/dev/null 2>&1; then
    pass "ssh $SSH_USER@$VM_IP works"
else
    fail "cannot ssh to $VM_IP on port 22"
    note "→ VM is unreachable. Possible causes:"
    note "    • VM stopped — check Oracle Cloud console"
    note "    • VCN security list missing TCP 22 rule"
    note "    • Wrong SSH_KEY ($SSH_KEY)"
    note ""
    note "stopping further checks (everything below depends on ssh)."
    exit 1
fi

# ===== 2. systemd service =============================================

step "2/5  systemd service"
# is-active returns non-zero for inactive states, so swallow the exit and just keep stdout.
status=$(ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'systemctl is-active vask; true' 2>/dev/null | tr -d '[:space:]')
case "$status" in
    active)
        pass "vask.service is active"
        ;;
    inactive|failed|activating|deactivating)
        fail "vask.service is $status"
        note "→ tail journal: ssh -p $SSH_PORT $SSH_TARGET sudo journalctl -u vask -n 50"
        note "→ try restart:  ssh -p $SSH_PORT $SSH_TARGET sudo systemctl restart vask"
        mark_fail
        ;;
    "")
        fail "could not query vask.service (ssh may have failed)"
        mark_fail
        ;;
    *)
        fail "vask.service unexpected status: '$status'"
        mark_fail
        ;;
esac

# ===== 3. socket binding ==============================================

step "3/5  socket binding (vm should listen on tcp/$PORT)"
listen=$(ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "sudo ss -tlnH 'sport = :$PORT' 2>/dev/null" 2>/dev/null)
if [[ -n "$listen" ]]; then
    pass "service is listening:"
    note "$(echo "$listen" | awk '{print $1, $4, $6}')"
else
    fail "nothing listening on tcp/$PORT inside the VM"
    note "→ the binary may have crashed during startup."
    note "→ tail logs: ssh $SSH_TARGET sudo journalctl -u vask -n 50"
    mark_fail
fi

# ===== 4. host firewall (diagnostic only) ============================
#
# Several patterns can legitimately allow tcp/$PORT — exact-rule, default
# policy, conntrack-style rule, ufw — and listing them all here is fragile.
# Step 5 below is the actual ground truth (end-to-end reachability).
# We try a few common patterns; if none match, we just say "couldn't
# pin down a specific rule" and let step 5 deliver the verdict.

step "4/5  host firewall on VM"
fw_remote=$(cat <<REMOTE
sudo iptables -S INPUT 2>/dev/null | grep -qE -- "--dport $PORT( |\\\$).*-j ACCEPT" && echo dport-rule && exit 0
sudo iptables -L INPUT -n 2>/dev/null | grep -qE "ACCEPT[[:space:]]+all" && echo accept-all && exit 0
sudo iptables -L INPUT -n -v 2>/dev/null | head -3 | grep -qE "policy ACCEPT" && echo policy-accept && exit 0
sudo ufw status 2>/dev/null | grep -qE "$PORT/tcp.*ALLOW" && echo ufw-rule && exit 0
echo none
REMOTE
)
fw=$(ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "$fw_remote" 2>/dev/null | tail -1 | tr -d '[:space:]')
case "$fw" in
    dport-rule)    pass "iptables has explicit ACCEPT for tcp/$PORT" ;;
    accept-all)    pass "iptables INPUT chain allows ALL traffic"      ;;
    policy-accept) pass "iptables INPUT default policy is ACCEPT"      ;;
    ufw-rule)      pass "ufw allows tcp/$PORT"                          ;;
    *)
        note "couldn't introspect a specific allow rule for tcp/$PORT;"
        note "step 5 below is the authoritative check."
        ;;
esac

# ===== 5. end-to-end reachability ====================================

step "5/5  end-to-end reachability (your machine → $VM_IP:$PORT)"
if command -v nc >/dev/null && nc -z -G 5 -w 5 "$VM_IP" "$PORT" 2>/dev/null; then
    pass "tcp/$PORT reachable from this machine"
elif (exec 3<>"/dev/tcp/$VM_IP/$PORT") 2>/dev/null; then
    exec 3<&-
    pass "tcp/$PORT reachable from this machine (via /dev/tcp)"
else
    fail "cannot reach $VM_IP:$PORT from this machine"
    note ""
    note "→ this is almost certainly the ${c_brand}Oracle VCN security list${c_reset}${c_dim}."
    note "  Oracle has a firewall *outside* the VM that's stricter than iptables."
    note "  Until you add an ingress rule, the world cannot see port $PORT."
    note ""
    note "  Fix: in Oracle Cloud Console:"
    note "    Networking → Virtual Cloud Networks → (your VCN)"
    note "    → click your public Subnet"
    note "    → click Default Security List"
    note "    → Add Ingress Rules:"
    note "        Source CIDR:        0.0.0.0/0"
    note "        IP Protocol:        TCP"
    note "        Destination Port:   $PORT"
    note "        Description:        ask"
    note "    → save, then re-run this script."
    mark_fail
fi

# ===== summary =======================================================

step "summary"
if [[ $failures -eq 0 ]]; then
    printf "  %sall checks passed%s\n\n" "$c_ok" "$c_reset"
    printf "  test the live UI:  %sssh -p %s %s%s\n\n" "$c_brand" "$PORT" "$VM_IP" "$c_reset"
    exit 0
else
    printf "  %s%d check(s) failed.%s fix the FAIL items above and re-run.\n\n" "$c_err" "$failures" "$c_reset"
    exit 1
fi
