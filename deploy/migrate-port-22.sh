#!/usr/bin/env bash
# migrate-port-22.sh — one-time cutover from "ssh -p 2200 vask.vosslabs.org"
# to "ssh vask.vosslabs.org".
#
# Moves the VM's sshd to port 22000, then re-deploys ask on port 22.
# Idempotent: re-running after success is a no-op.
#
# Safety design:
#   - Adds Port 22000 *before* removing Port 22, so there's always a way in.
#   - Verifies ssh on 22000 from your Mac before touching port 22.
#   - Pauses for the manual Oracle VCN step (only thing the script can't do).
#   - Each remote command is idempotent.
#
# Usage:
#   ./deploy/migrate-port-22.sh
#
# Override defaults by exporting:
#   VM_IP, SSH_KEY, SSH_USER

set -euo pipefail

VM_IP="${VM_IP:?VM_IP must be set — your Oracle VM's public IP}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/id_ed25519}"
SSH_USER="${SSH_USER:-ubuntu}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ===== logging =========================================================
c_brand=$'\033[38;5;208m'
c_dim=$'\033[38;5;240m'
c_ok=$'\033[38;5;72m'
c_err=$'\033[38;5;167m'
c_warn=$'\033[38;5;220m'
c_reset=$'\033[0m'

step() { printf "\n%s▶%s %s\n" "$c_brand" "$c_reset" "$*"; }
ok()   { printf "  %s✓%s %s\n" "$c_ok" "$c_reset" "$*"; }
note() { printf "       %s%s%s\n" "$c_dim" "$*" "$c_reset"; }
fail() { printf "  %s✗%s %s\n" "$c_err" "$c_reset" "$*" >&2; exit 1; }

ssh_at() {
    local port="$1"; shift
    ssh -i "$SSH_KEY" -p "$port" -o IdentitiesOnly=yes \
        -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8 \
        "$SSH_USER@$VM_IP" "$@"
}

# ===== preflight =======================================================

step "preflight"
[[ -f "$SSH_KEY" ]] || fail "SSH key not found: $SSH_KEY"

mgmt=""
for p in 22000 22; do
    if ssh_at "$p" 'echo ok' >/dev/null 2>&1; then
        mgmt=$p
        break
    fi
done
[[ -n "$mgmt" ]] || fail "cannot ssh to $VM_IP on port 22 or 22000"
ok "management ssh works on port $mgmt"

# ===== 1. add Port 22000 to sshd_config + iptables =====================

step "1/6  ensure sshd listens on port 22000 (idempotent)"
ssh_at "$mgmt" 'bash -s' <<'REMOTE'
set -euo pipefail
changed=0

if ! grep -qE '^Port 22000\b' /etc/ssh/sshd_config; then
    echo 'Port 22000' | sudo tee -a /etc/ssh/sshd_config >/dev/null
    echo "  · added 'Port 22000' to sshd_config"
    changed=1
else
    echo "  · sshd_config already has Port 22000"
fi

if ! sudo iptables -C INPUT -p tcp --dport 22000 -j ACCEPT 2>/dev/null; then
    sudo iptables -I INPUT -p tcp --dport 22000 -j ACCEPT
    if [[ -d /etc/iptables ]]; then
        sudo iptables-save | sudo tee /etc/iptables/rules.v4 >/dev/null
    fi
    echo "  · iptables: tcp/22000 ACCEPT"
    changed=1
else
    echo "  · iptables already allows tcp/22000"
fi

if [[ $changed -eq 1 ]]; then
    sudo systemctl reload ssh
    echo "  · sshd reloaded"
fi
REMOTE
ok "sshd ready on tcp/22000 (port 22 still active too)"

# ===== 2. wait for Oracle VCN ingress on 22000 =========================

step "2/6  Oracle VCN ingress rule for tcp/22000"
if ssh_at 22000 'echo ok' >/dev/null 2>&1; then
    ok "tcp/22000 already reachable from your Mac (VCN rule present)"
else
    note ""
    note "VCN rule needed. In Oracle Cloud Console:"
    note "  Networking → Virtual Cloud Networks → (your VCN)"
    note "    → click your public Subnet"
    note "    → click Default Security List"
    note "    → Add Ingress Rules:"
    note "        Source CIDR:        0.0.0.0/0"
    note "        IP Protocol:        TCP"
    note "        Destination Port:   22000"
    note "        Description:        mgmt-ssh"
    note ""
    read -p "  press ENTER once saved (ctrl+c to abort): " _

    for i in 1 2 3 4 5; do
        if ssh_at 22000 'echo ok' >/dev/null 2>&1; then
            ok "tcp/22000 reachable"
            break
        fi
        if [[ $i -lt 5 ]]; then
            note "still not reachable, retrying in 5s ($i/5)..."
            sleep 5
        else
            fail "tcp/22000 still unreachable. check the VCN rule and re-run."
        fi
    done
fi

# ===== 3. safety net check =============================================

step "3/6  safety net — confirm ssh-on-22000 from this Mac"
ssh_at 22000 'whoami' >/dev/null || fail "lost ssh on 22000 — abort, do not proceed."
ok "ssh on 22000 works (you have a guaranteed door)"

# ===== 4. remove Port 22 from sshd_config =============================

step "4/6  removing 'Port 22' from sshd_config (idempotent)"
ssh_at 22000 'bash -s' <<'REMOTE'
set -euo pipefail

# remove plain 'Port 22' lines (commented-out ones don't matter)
sudo sed -i.bak -E '/^Port 22$/d' /etc/ssh/sshd_config

# verify only Port 22000 remains
ports=$(grep -E '^Port [0-9]+' /etc/ssh/sshd_config | awk '{print $2}' | sort -u | tr '\n' ' ')
ports="${ports% }"
echo "  · active Port directives: '${ports}'"
if [[ "$ports" != "22000" ]]; then
    echo "  · refusing to reload sshd — config is unexpected"
    exit 1
fi

sudo systemctl reload ssh
sleep 1
echo "  · sshd reloaded; only listening on 22000"
REMOTE
ok "sshd is exclusively on port 22000 — port 22 freed for ask"

# ===== 5. redeploy ask to bind port 22 =============================

step "5/6  redeploying ask (now binds tcp/22 with CAP_NET_BIND_SERVICE)"
SSH_PORT=22000 "$REPO_ROOT/deploy/setup-oracle.sh" --update

# ===== 6. end-to-end verify ============================================

step "6/6  end-to-end verify"
SSH_PORT=22000 PORT=22 "$REPO_ROOT/deploy/verify-deploy.sh"

# ===== summary ========================================================

step "done"
cat <<EOM

  ${c_brand}share-anywhere command:${c_reset}

    ${c_brand}ssh vask.vosslabs.org${c_reset}

  ${c_dim}management ssh going forward:${c_reset}

    ${c_dim}ssh -p 22000 -i $SSH_KEY $SSH_USER@$VM_IP${c_reset}

  add this to ~/.ssh/config to skip the flags:
${c_dim}
    Host oracle-ask
      HostName $VM_IP
      Port 22000
      User $SSH_USER
      IdentityFile $SSH_KEY
      IdentitiesOnly yes
${c_reset}

  ${c_dim}future redeploys are unchanged:${c_reset}
    ${c_dim}./deploy/setup-oracle.sh --update${c_reset}

EOM
