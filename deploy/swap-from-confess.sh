#!/usr/bin/env bash
# swap-from-confess.sh — replace voss-confess with voss-ask on the SAME Oracle VM.
#
# What this does, in order:
#   1. Preflight: ssh works, turso credentials present, ask binary builds.
#   2. Builds bin/ask.linux-amd64 locally.
#   3. Stops confess.service (frees port 22) — does NOT disable yet.
#   4. Bootstraps the `ask` runtime user + /opt/ask/ tree.
#   5. Pushes binary + ask.service + turso.env to the VM.
#   6. Starts ask.service (binds port 22 with CAP_NET_BIND_SERVICE).
#   7. End-to-end verify (port 22 reachable from this Mac).
#   8. On success: disables confess so it doesn't auto-start on reboot,
#      but leaves /opt/confess/ intact for rollback.
#      On failure: rolls back — stops ask, restarts confess.
#
# After this completes you still need to (manually):
#   - DNS: add A record `ask.vosslabs.org` → <VM_IP>, DNS-only on Cloudflare
#   - DNS: optionally remove confess.vosslabs.org (or leave it pointing to the
#     same IP — old `ssh confess.vosslabs.org` will land on ask)
#
# Prereqs (one-time, manual):
#   - Turso DB created at app.turso.tech (e.g. voss-ask-harshalmore31)
#   - Turso token issued: turso db tokens create voss-ask-harshalmore31 --expiration none
#   - export TURSO_DATABASE_URL and TURSO_AUTH_TOKEN before running this script
#   - Go installed locally
#
# Usage:
#   ./deploy/swap-from-confess.sh
#   ./deploy/swap-from-confess.sh --purge-confess   # also delete /opt/confess on success

set -euo pipefail

VM_IP="${VM_IP:-129.153.206.68}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/oracle-confess.key}"
SSH_USER="${SSH_USER:-ubuntu}"
SSH_PORT="${SSH_PORT:-22000}"
SSH_TARGET="$SSH_USER@$VM_IP"
SSH_OPTS=(-i "$SSH_KEY" -p "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)
SCP_OPTS=(-i "$SSH_KEY" -P "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PURGE=0
for arg in "$@"; do
    case "$arg" in
        --purge-confess) PURGE=1 ;;
        --help|-h) sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "unknown flag: $arg" >&2; exit 2 ;;
    esac
done

c_brand=$'\033[38;5;208m'
c_dim=$'\033[38;5;240m'
c_ok=$'\033[38;5;72m'
c_err=$'\033[38;5;167m'
c_warn=$'\033[38;5;220m'
c_reset=$'\033[0m'

step() { printf "\n%s▶%s %s\n" "$c_brand" "$c_reset" "$*"; }
ok()   { printf "  %s✓%s %s\n" "$c_ok" "$c_reset" "$*"; }
note() { printf "       %s%s%s\n" "$c_dim" "$*" "$c_reset"; }
warn() { printf "  %s!%s %s\n" "$c_warn" "$c_reset" "$*"; }
fail() { printf "  %s✗%s %s\n" "$c_err" "$c_reset" "$*" >&2; }
die()  { fail "$*"; exit 1; }

# ===== preflight =======================================================

step "preflight"
[[ -f "$SSH_KEY" ]] || die "SSH key not found: $SSH_KEY"
ok "ssh key: $SSH_KEY"

command -v go >/dev/null || die "go not installed"
ok "go: $(go version | awk '{print $3}')"

[[ -n "${TURSO_DATABASE_URL:-}" ]] || die "TURSO_DATABASE_URL must be set (export it first)"
[[ -n "${TURSO_AUTH_TOKEN:-}"   ]] || die "TURSO_AUTH_TOKEN must be set (export it first)"
case "$TURSO_DATABASE_URL" in
    *voss-ask*) ok "turso URL points to an ask DB: $TURSO_DATABASE_URL" ;;
    *) warn "TURSO_DATABASE_URL doesn't contain 'voss-ask' — be sure this isn't the confess DB!"
       printf "  continue anyway? [y/N] "; read -r ans
       [[ "$ans" == "y" || "$ans" == "Y" ]] || die "aborted"
       ;;
esac

if ! ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'echo ok' >/dev/null 2>&1; then
    die "cannot ssh to $SSH_TARGET on port $SSH_PORT"
fi
ok "ssh to $VM_IP works"

# ===== build ===========================================================

step "1/7  build bin/ask.linux-amd64"
( cd "$REPO_ROOT" && make build-amd64 >/dev/null )
ok "$(ls -lh "$REPO_ROOT/bin/ask.linux-amd64" | awk '{print $5}')"

# ===== stop confess ====================================================

step "2/7  stop confess.service (frees port 22)"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE'
set -euo pipefail
if systemctl list-unit-files | grep -q '^confess\.service'; then
    sudo systemctl stop confess 2>/dev/null || true
    echo "  · confess.service stopped"
else
    echo "  · confess.service not present — fresh swap"
fi
REMOTE
ok "port 22 freed"

# rollback function (called on any later failure)
rollback() {
    fail "rolling back: stopping ask, restoring confess"
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE' || true
sudo systemctl stop ask 2>/dev/null
sudo systemctl disable ask 2>/dev/null
sudo systemctl start confess 2>/dev/null
echo "  · rollback complete (confess restarted, ask stopped)"
REMOTE
    exit 1
}
trap 'rollback' ERR

# ===== bootstrap ask ===================================================

step "3/7  bootstrap /opt/ask/ + ask user (idempotent)"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE'
set -euo pipefail
if ! id ask >/dev/null 2>&1; then
    sudo useradd --system --home /opt/ask --shell /usr/sbin/nologin ask
    echo "  · created ask user"
fi
sudo mkdir -p /opt/ask/data
sudo chown -R ask:ask /opt/ask
echo "  · /opt/ask/ ready"
REMOTE
ok "ask runtime ready"

# ===== upload artifacts ================================================

step "4/7  upload binary + service unit"
scp "${SCP_OPTS[@]}" "$REPO_ROOT/bin/ask.linux-amd64" "$SSH_TARGET:/tmp/ask" >/dev/null
scp "${SCP_OPTS[@]}" "$REPO_ROOT/deploy/ask.service"  "$SSH_TARGET:/tmp/" >/dev/null
ok "uploaded"

# ===== write turso.env =================================================

step "5/7  write /opt/ask/data/turso.env"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "sudo tee /opt/ask/data/turso.env >/dev/null && sudo chmod 600 /opt/ask/data/turso.env && sudo chown ask:ask /opt/ask/data/turso.env" <<EOF
TURSO_DATABASE_URL=$TURSO_DATABASE_URL
TURSO_AUTH_TOKEN=$TURSO_AUTH_TOKEN
EOF
ok "turso.env in place (chmod 600, owned by ask)"

# ===== install + start =================================================

step "6/7  install ask binary, install unit, start service"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE'
set -euo pipefail
sudo install -o ask -g ask -m 0755 /tmp/ask /opt/ask/ask
sudo mv /tmp/ask.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable ask >/dev/null 2>&1 || true
sudo systemctl restart ask
sleep 1
echo "----"
sudo systemctl status ask --no-pager --lines=8
echo "----"
sudo journalctl -u ask --no-pager -n 6 --output=cat
REMOTE
ok "ask service started"

# ===== verify ==========================================================

step "7/7  verify port 22 reachable from this machine"
sleep 2
if nc -z -G 5 -w 5 "$VM_IP" 22 2>/dev/null; then
    ok "tcp/22 reachable — ask is live"
elif (exec 3<>"/dev/tcp/$VM_IP/22") 2>/dev/null; then
    exec 3<&-
    ok "tcp/22 reachable — ask is live"
else
    fail "tcp/22 not reachable; see logs above"
    rollback
fi

# ===== success: disable confess permanently ============================

trap - ERR

step "post-success: disable confess.service so it never auto-starts"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE'
set -euo pipefail
if systemctl list-unit-files | grep -q '^confess\.service'; then
    sudo systemctl disable confess 2>/dev/null || true
    echo "  · confess.service disabled (binary + data still at /opt/confess for rollback)"
fi
REMOTE
ok "confess will not auto-start on reboot"

# ===== optional purge ==================================================

if [[ $PURGE -eq 1 ]]; then
    step "purging /opt/confess (--purge-confess flag was passed)"
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE'
set -euo pipefail
sudo rm -rf /opt/confess
sudo rm -f /etc/systemd/system/confess.service
sudo systemctl daemon-reload
if id confess >/dev/null 2>&1; then
    sudo userdel confess 2>/dev/null || true
fi
echo "  · /opt/confess removed, confess.service unit removed, confess user deleted"
REMOTE
    ok "confess fully purged from VM"
fi

# ===== summary =========================================================

step "done"
cat <<EOM

  ${c_brand}share-anywhere command:${c_reset}

    ${c_brand}ssh ask.vosslabs.org${c_reset}     ${c_dim}(after DNS update)${c_reset}
    ${c_brand}ssh $VM_IP${c_reset}     ${c_dim}(works now via raw IP)${c_reset}

  ${c_dim}next manual steps:${c_reset}
    1. ${c_brand}DNS${c_reset}: add ${c_brand}A record  ask.vosslabs.org → $VM_IP${c_reset}
       (DNS-only / grey cloud on Cloudflare — same as confess was)
    2. ${c_dim}optionally remove confess.vosslabs.org A record (or leave it
       pointing here — old links land on ask without breaking)${c_reset}
    3. ${c_dim}rollback (if you ever need it):${c_reset}
       ${c_dim}ssh -p $SSH_PORT $SSH_TARGET 'sudo systemctl stop ask && sudo systemctl start confess'${c_reset}

  ${c_dim}redeploy after code changes:${c_reset}
    ${c_dim}./deploy/setup-oracle.sh --update${c_reset}

EOM
