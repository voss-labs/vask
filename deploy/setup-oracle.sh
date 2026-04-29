#!/usr/bin/env bash
# setup-oracle.sh — provision and deploy voss-ask to an Oracle Cloud VM.
#
# Usage:
#   ./deploy/setup-oracle.sh                  full deploy (bootstrap + push + start)
#   ./deploy/setup-oracle.sh --update         skip bootstrap, just rebuild + restart
#   ./deploy/setup-oracle.sh --rotate-token   re-push turso.env, restart
#   ./deploy/setup-oracle.sh --help           this message
#
# Required env (override defaults by exporting):
#   VM_IP               default: 129.153.206.68
#   SSH_KEY             default: ~/.ssh/oracle-ask.key
#   SSH_USER            default: ubuntu
#   TURSO_DATABASE_URL  required for first deploy and --rotate-token
#   TURSO_AUTH_TOKEN    required for first deploy and --rotate-token
#
# Prerequisites (do these once, manually):
#   1. Oracle VCN security list — allow inbound TCP 2200 from 0.0.0.0/0
#   2. Turso DB created at app.turso.tech
#   3. Turso token issued: turso db tokens create voss-ask-harshalmore31 --expiration none
#   4. Go installed locally (brew install go)

set -euo pipefail

# ===== config ==========================================================

VM_IP="${VM_IP:-129.153.206.68}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/oracle-ask.key}"
SSH_USER="${SSH_USER:-ubuntu}"
SSH_PORT="${SSH_PORT:-22000}"   # management ssh — moved off 22 to free that port for ask
SSH_TARGET="$SSH_USER@$VM_IP"
# ssh and scp disagree on the port flag (-p lowercase vs -P uppercase). Keep separate.
SSH_OPTS=(-i "$SSH_KEY" -p "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new)
SCP_OPTS=(-i "$SSH_KEY" -P "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="full"

# ===== arg parse =======================================================

for arg in "$@"; do
    case "$arg" in
        --update) MODE="update" ;;
        --rotate-token) MODE="rotate" ;;
        --help|-h)
            sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            echo "unknown flag: $arg (try --help)" >&2
            exit 2
            ;;
    esac
done

# ===== logging =========================================================

c_brand=$'\033[38;5;208m'
c_dim=$'\033[38;5;240m'
c_ok=$'\033[38;5;72m'
c_err=$'\033[38;5;167m'
c_reset=$'\033[0m'

step() { printf "\n%s▶%s %s\n" "$c_brand" "$c_reset" "$*"; }
ok()   { printf "  %s✓%s %s\n" "$c_ok" "$c_reset" "$*"; }
note() { printf "  %s·%s %s\n" "$c_dim" "$c_reset" "$*"; }
fail() { printf "  %s✗%s %s\n" "$c_err" "$c_reset" "$*" >&2; exit 1; }

# ===== prereqs =========================================================

step "checking prerequisites"

[[ -f "$SSH_KEY" ]] || fail "SSH key not found at $SSH_KEY (set SSH_KEY=...)"
ok "ssh key: $SSH_KEY"

command -v go >/dev/null || fail "go not installed. brew install go"
ok "go: $(go version | awk '{print $3}')"

command -v scp >/dev/null || fail "scp not found"

note "testing ssh to $SSH_TARGET (will accept host key on first connect)"
if ! ssh "${SSH_OPTS[@]}" -o ConnectTimeout=10 "$SSH_TARGET" 'echo ok' >/dev/null 2>&1; then
    fail "cannot ssh to $SSH_TARGET. is the VM running, and is port 22 open in the VCN security list?"
fi
ok "ssh works"

if [[ "$MODE" != "update" ]]; then
    [[ -n "${TURSO_DATABASE_URL:-}" ]] || fail "TURSO_DATABASE_URL must be set (export it before running, or use --update)"
    [[ -n "${TURSO_AUTH_TOKEN:-}" ]]   || fail "TURSO_AUTH_TOKEN must be set (export it before running, or use --update)"
    ok "turso credentials in env"
fi

# ===== build ===========================================================

step "building bin/ask.linux-amd64"
( cd "$REPO_ROOT" && make build-amd64 >/dev/null )
ok "$(ls -lh "$REPO_ROOT/bin/ask.linux-amd64" | awk '{print $5, $9}')"

# ===== bootstrap (idempotent) ==========================================

bootstrap_vm() {
    step "bootstrapping VM (idempotent — safe to re-run)"
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE'
set -euo pipefail

# 1. runtime user + dirs
if ! id ask >/dev/null 2>&1; then
    sudo useradd --system --home /opt/ask --shell /usr/sbin/nologin ask
    echo "  · created ask user"
fi
sudo mkdir -p /opt/ask/data
sudo chown -R ask:ask /opt/ask

# 2. 1 GB swap (this VM has only 1 GB RAM)
if ! swapon --show 2>/dev/null | grep -q '/swapfile'; then
    if [[ ! -f /swapfile ]]; then
        sudo fallocate -l 1G /swapfile
        sudo chmod 600 /swapfile
        sudo mkswap /swapfile >/dev/null
    fi
    sudo swapon /swapfile
    grep -q '/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab >/dev/null
    echo "  · swap enabled (1 GB)"
fi

# 3. host firewall — Oracle Ubuntu ships with iptables; ufw may be inactive
if command -v ufw >/dev/null && sudo ufw status 2>/dev/null | grep -q "Status: active"; then
    sudo ufw allow 2200/tcp >/dev/null
    echo "  · ufw allows tcp/2200"
else
    if ! sudo iptables -C INPUT -p tcp --dport 2200 -j ACCEPT 2>/dev/null; then
        sudo iptables -I INPUT -p tcp --dport 2200 -j ACCEPT
        # try to persist (best effort)
        if command -v netfilter-persistent >/dev/null; then
            sudo netfilter-persistent save >/dev/null 2>&1 || true
        elif [[ -d /etc/iptables ]]; then
            sudo iptables-save | sudo tee /etc/iptables/rules.v4 >/dev/null
        else
            sudo DEBIAN_FRONTEND=noninteractive apt-get install -y iptables-persistent >/dev/null 2>&1 || true
            sudo iptables-save | sudo tee /etc/iptables/rules.v4 >/dev/null
        fi
        echo "  · iptables allows tcp/2200"
    fi
fi

echo "bootstrap done"
REMOTE
    ok "VM ready"
}

# ===== push binary + unit ==============================================

push_artifacts() {
    step "uploading binary + systemd unit"
    scp "${SCP_OPTS[@]}" "$REPO_ROOT/bin/ask.linux-amd64" "$SSH_TARGET:/tmp/ask" >/dev/null
    scp "${SCP_OPTS[@]}" "$REPO_ROOT/deploy/ask.service" "$SSH_TARGET:/tmp/" >/dev/null
    ok "uploaded to /tmp/"
}

# ===== push turso credentials ==========================================

push_turso_env() {
    step "writing /opt/ask/data/turso.env"
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "sudo tee /opt/ask/data/turso.env >/dev/null && sudo chmod 600 /opt/ask/data/turso.env && sudo chown ask:ask /opt/ask/data/turso.env" <<EOF
TURSO_DATABASE_URL=$TURSO_DATABASE_URL
TURSO_AUTH_TOKEN=$TURSO_AUTH_TOKEN
EOF
    ok "turso.env in place (chmod 600, owned by ask)"
}

# ===== install + start =================================================

install_and_start() {
    step "installing and (re)starting ask.service"
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
echo "recent logs:"
sudo journalctl -u ask --no-pager -n 6 --output=cat
REMOTE
    ok "service running"
}

# ===== orchestrate =====================================================

case "$MODE" in
    full)
        bootstrap_vm
        push_artifacts
        push_turso_env
        install_and_start
        ;;
    update)
        push_artifacts
        install_and_start
        ;;
    rotate)
        push_turso_env
        install_and_start
        ;;
esac

step "done"

# infer the public ask port from the deployed unit so the printed share
# command always matches reality (port 22 after the migration, 2200 before).
ask_port=$(grep -oE -- '--port [0-9]+' "$REPO_ROOT/deploy/ask.service" 2>/dev/null | awk '{print $2}' | head -1)
ask_port="${ask_port:-22}"
if [[ "$ask_port" == "22" ]]; then
    share_cmd="ssh ask.vosslabs.org"
    raw_cmd="ssh $VM_IP"
else
    share_cmd="ssh -p $ask_port ask.vosslabs.org"
    raw_cmd="ssh -p $ask_port $VM_IP"
fi

cat <<EOM

  share-anywhere command:

    ${c_brand}$share_cmd${c_reset}

  raw IP test (no DNS needed):  ${c_dim}$raw_cmd${c_reset}

  tail server logs:
    ${c_dim}ssh -p $SSH_PORT $SSH_TARGET sudo journalctl -u ask -f${c_reset}

  redeploy after code changes:
    ${c_dim}./deploy/setup-oracle.sh --update${c_reset}

EOM
