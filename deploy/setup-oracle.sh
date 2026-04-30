#!/usr/bin/env bash
# setup-oracle.sh — provision and deploy vosslabs/vask to an Oracle Cloud VM.
#
# Secrets come from a .env file, never from shell args/history. Default
# lookup order: ./.env, ./.env.local, ../.env (relative to repo root).
# Override with --env-file PATH.
#
# Usage:
#   ./deploy/setup-oracle.sh                            full deploy
#   ./deploy/setup-oracle.sh --update                   rebuild + restart only
#   ./deploy/setup-oracle.sh --rotate-token             re-push env, restart
#   ./deploy/setup-oracle.sh --env-file path/to/.env    custom env file
#   ./deploy/setup-oracle.sh --help                     this message
#
# Override deploy targets via env (VM_IP, SSH_KEY, SSH_USER, SSH_PORT).
#
# Required keys in .env:
#   TURSO_DATABASE_URL   libsql://… for the production DB
#   TURSO_AUTH_TOKEN     long-lived token for that DB
#
# Optional keys in .env (enables embeddings):
#   CF_ACCOUNT_ID        Cloudflare account ID (Workers & Pages → sidebar)
#   CF_AI_TOKEN          Workers AI scoped token
#
# Secret-handling rules (do not violate):
#   - No secret value is ever printed to stdout / stderr / journal.
#   - Secrets are sent over SSH via heredoc on stdin, never as command
#     arguments — they don't appear in `ps` on the VM.
#   - The remote env file is mode 0600, owned by vask:vask.
#
# Prerequisites (one-time, manual):
#   1. Oracle VCN security list — allow inbound TCP 2200 from 0.0.0.0/0
#   2. Turso DB created at app.turso.tech
#   3. Turso token issued: turso db tokens create vosslabs/vask-… --expiration none
#   4. Go installed locally (brew install go)
#   5. cp .env.example .env  &&  fill in values  &&  chmod 600 .env

set -euo pipefail

# ===== config ==========================================================

VM_IP="${VM_IP:?VM_IP must be set — your Oracle VM's public IP}"
SSH_USER="${SSH_USER:-ubuntu}"
SSH_PORT="${SSH_PORT:-22000}"   # management ssh — moved off 22 to free that port for ask
SSH_KEY="${SSH_KEY:-$HOME/.ssh/id_ed25519}"
SSH_TARGET="$SSH_USER@$VM_IP"
# ssh and scp disagree on the port flag (-p lowercase vs -P uppercase). Keep separate.
SSH_OPTS=(-i "$SSH_KEY" -p "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new)
SCP_OPTS=(-i "$SSH_KEY" -P "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="full"
ENV_FILE=""

# ===== arg parse =======================================================

while [[ $# -gt 0 ]]; do
    case "$1" in
        --update)        MODE="update"; shift ;;
        --rotate-token)  MODE="rotate"; shift ;;
        --env-file=*)    ENV_FILE="${1#*=}"; shift ;;
        --env-file)      shift; ENV_FILE="${1:-}"; shift ;;
        --help|-h)
            sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            echo "unknown flag: $1 (try --help)" >&2
            exit 2
            ;;
    esac
done

# Default env-file lookup: in-repo .env, then .env.local, then parent .env.
if [[ -z "$ENV_FILE" ]]; then
    if   [[ -f "$REPO_ROOT/.env"        ]]; then ENV_FILE="$REPO_ROOT/.env"
    elif [[ -f "$REPO_ROOT/.env.local"  ]]; then ENV_FILE="$REPO_ROOT/.env.local"
    elif [[ -f "$REPO_ROOT/../.env"     ]]; then ENV_FILE="$REPO_ROOT/../.env"
    fi
fi

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
    [[ -n "$ENV_FILE" && -f "$ENV_FILE" ]] || fail "no .env file found. expected one of: $REPO_ROOT/.env, $REPO_ROOT/.env.local, $REPO_ROOT/../.env, or pass --env-file PATH"

    # Refuse to load a world-readable env file. Common rookie leak.
    env_perms=$(stat -f '%Lp' "$ENV_FILE" 2>/dev/null || stat -c '%a' "$ENV_FILE" 2>/dev/null || echo "")
    case "$env_perms" in
        600|400|640|440) ;;  # acceptable
        "") note "couldn't stat env-file permissions; proceeding" ;;
        *)  note "env-file is mode $env_perms — recommend chmod 600 \"$ENV_FILE\"" ;;
    esac

    # shellcheck disable=SC1090
    set -a; source "$ENV_FILE"; set +a

    [[ -n "${TURSO_DATABASE_URL:-}" ]] || fail "TURSO_DATABASE_URL not set in $ENV_FILE"
    [[ -n "${TURSO_AUTH_TOKEN:-}" ]]   || fail "TURSO_AUTH_TOKEN not set in $ENV_FILE"
    ok "env file: $ENV_FILE"
    ok "turso: TURSO_DATABASE_URL=<set> TURSO_AUTH_TOKEN=<set>"
    if [[ -n "${CF_ACCOUNT_ID:-}" && -n "${CF_AI_TOKEN:-}" ]]; then
        ok "cloudflare: CF_ACCOUNT_ID=<set> CF_AI_TOKEN=<set> (embeddings will be enabled)"
    else
        note "CF_ACCOUNT_ID / CF_AI_TOKEN not set — embeddings disabled on the deployed instance"
    fi
fi

# ===== build ===========================================================

step "building bin/vask.linux-amd64"
( cd "$REPO_ROOT" && make build-amd64 >/dev/null )
ok "$(ls -lh "$REPO_ROOT/bin/vask.linux-amd64" | awk '{print $5, $9}')"

# ===== bootstrap (idempotent) ==========================================

bootstrap_vm() {
    step "bootstrapping VM (idempotent — safe to re-run)"
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE'
set -euo pipefail

# 1. runtime user + dirs
if ! id vask >/dev/null 2>&1; then
    sudo useradd --system --home /opt/vask --shell /usr/sbin/nologin vask
    echo "  · created vask user"
fi
sudo mkdir -p /opt/vask/data
sudo chown -R vask:vask /opt/vask

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
    scp "${SCP_OPTS[@]}" "$REPO_ROOT/bin/vask.linux-amd64" "$SSH_TARGET:/tmp/ask" >/dev/null
    scp "${SCP_OPTS[@]}" "$REPO_ROOT/deploy/vask.service" "$SSH_TARGET:/tmp/" >/dev/null
    ok "uploaded to /tmp/"
}

# ===== push turso credentials ==========================================

push_turso_env() {
    step "writing /opt/vask/data/turso.env"
    # `install` creates the file mode 0600 owned by vask:vask before tee
    # writes anything into it — so even mid-write the file is never
    # world-readable. The values flow over ssh stdin, never as argv.
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" \
        'sudo install -o vask -g vask -m 0600 /dev/null /opt/vask/data/turso.env && sudo tee /opt/vask/data/turso.env >/dev/null' \
        <<EOF
# managed by deploy/setup-oracle.sh — DO NOT edit by hand
TURSO_DATABASE_URL=$TURSO_DATABASE_URL
TURSO_AUTH_TOKEN=$TURSO_AUTH_TOKEN
CF_ACCOUNT_ID=${CF_ACCOUNT_ID:-}
CF_AI_TOKEN=${CF_AI_TOKEN:-}
EOF
    if [[ -n "${CF_ACCOUNT_ID:-}" && -n "${CF_AI_TOKEN:-}" ]]; then
        ok "turso.env in place (mode 0600, owned by ask, embeddings enabled)"
    else
        ok "turso.env in place (mode 0600, owned by ask, embeddings disabled)"
    fi
}

# ===== install + start =================================================

install_and_start() {
    step "installing and (re)starting vask.service"
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE'
set -euo pipefail
sudo install -o vask -g vask -m 0755 /tmp/ask /opt/vask/vask
sudo mv /tmp/vask.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable vask >/dev/null 2>&1 || true
sudo systemctl restart vask
sleep 1
echo "----"
sudo systemctl status vask --no-pager --lines=8
echo "----"
echo "recent logs:"
sudo journalctl -u vask --no-pager -n 6 --output=cat
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
vask_port=$(grep -oE -- '--port [0-9]+' "$REPO_ROOT/deploy/vask.service" 2>/dev/null | awk '{print $2}' | head -1)
vask_port="${ask_port:-22}"
if [[ "$vask_port" == "22" ]]; then
    share_cmd="ssh vask.vosslabs.org"
    raw_cmd="ssh $VM_IP"
else
    share_cmd="ssh -p $vask_port vask.vosslabs.org"
    raw_cmd="ssh -p $vask_port $VM_IP"
fi

cat <<EOM

  share-anywhere command:

    ${c_brand}$share_cmd${c_reset}

  raw IP test (no DNS needed):  ${c_dim}$raw_cmd${c_reset}

  tail server logs:
    ${c_dim}ssh -p $SSH_PORT $SSH_TARGET sudo journalctl -u vask -f${c_reset}

  redeploy after code changes:
    ${c_dim}./deploy/setup-oracle.sh --update${c_reset}

EOM
