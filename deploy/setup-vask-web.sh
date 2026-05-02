#!/usr/bin/env bash
# setup-vask-web.sh — deploy vask-web (HTTP read-only mirror) and Caddy
# alongside the existing vask SSH server on the same Oracle VM.
#
# Run setup-oracle.sh at least once first — it provisions the vask user,
# /opt/vask, swap, and the SSH service. This script just adds the web tier.
#
# Idempotent — safe to re-run for binary or config updates.
#
# Usage:
#   ./deploy/setup-vask-web.sh                          full deploy (binary + caddy + env)
#   ./deploy/setup-vask-web.sh --update                 rebuild binary + restart only
#   ./deploy/setup-vask-web.sh --rotate-env             re-push vask-web.env, restart
#   ./deploy/setup-vask-web.sh --env-file PATH          custom .env
#
# Required additional .env keys (on top of TURSO_*):
#   VASK_BASE_URL        e.g. https://vask.vosslabs.org
# Optional:
#   VASK_OG_IMAGE        e.g. https://vosslabs.org/brand/social/og-default.png
#   CADDY_EMAIL          email for Let's Encrypt expiry warnings
#
# Override deploy targets via env (VM_IP, SSH_KEY, SSH_USER, SSH_PORT) —
# same as setup-oracle.sh.
#
# Prerequisites (one-time, manual):
#   1. Oracle VCN security list — allow inbound TCP 80 and 443
#   2. setup-oracle.sh has been run at least once on the same VM
#   3. Cloudflare DNS — vask.vosslabs.org → <VM_IP>, gray-cloud (DNS only)
#      Orange-cloud will route :22 through CF and break SSH.
#   4. (Optional but recommended) Generate a read-only Turso token for
#      the web tier:
#          turso db tokens create vask-<you> --read-only --expiration none
#      Put it in TURSO_AUTH_TOKEN_RO and reference from vask-web.env so
#      the web binary physically cannot mutate the DB.

set -euo pipefail

# ===== config ==========================================================

VM_IP="${VM_IP:?VM_IP must be set — your Oracle VM's public IP}"
SSH_USER="${SSH_USER:-ubuntu}"
SSH_PORT="${SSH_PORT:-22000}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/id_ed25519}"
SSH_TARGET="$SSH_USER@$VM_IP"
SSH_OPTS=(-i "$SSH_KEY" -p "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new)
SCP_OPTS=(-i "$SSH_KEY" -P "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new)

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="full"
ENV_FILE=""

# ===== arg parse =======================================================

while [[ $# -gt 0 ]]; do
    case "$1" in
        --update)        MODE="update"; shift ;;
        --rotate-env)    MODE="rotate"; shift ;;
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

[[ -f "$SSH_KEY" ]] || fail "SSH key not found at $SSH_KEY"
ok "ssh key: $SSH_KEY"

command -v go >/dev/null || fail "go not installed. brew install go"
ok "go: $(go version | awk '{print $3}')"

if ! ssh "${SSH_OPTS[@]}" -o ConnectTimeout=10 "$SSH_TARGET" 'echo ok' >/dev/null 2>&1; then
    fail "cannot ssh to $SSH_TARGET. is the VM running, and is mgmt port $SSH_PORT open?"
fi
ok "ssh works"

# Confirm setup-oracle.sh ran at least once.
if ! ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'id vask >/dev/null 2>&1 && [[ -d /opt/vask ]]'; then
    fail "vask user / /opt/vask not found. run ./deploy/setup-oracle.sh first."
fi
ok "vask user + /opt/vask present (setup-oracle.sh has run)"

if [[ "$MODE" != "update" ]]; then
    [[ -n "$ENV_FILE" && -f "$ENV_FILE" ]] || fail "no .env file found"

    env_perms=$(stat -f '%Lp' "$ENV_FILE" 2>/dev/null || stat -c '%a' "$ENV_FILE" 2>/dev/null || echo "")
    case "$env_perms" in
        600|400|640|440) ;;
        "") note "couldn't stat env-file permissions; proceeding" ;;
        *)  note "env-file is mode $env_perms — recommend chmod 600 \"$ENV_FILE\"" ;;
    esac

    # shellcheck disable=SC1090
    set -a; source "$ENV_FILE"; set +a

    [[ -n "${VASK_BASE_URL:-}" ]] || fail "VASK_BASE_URL not set in $ENV_FILE (e.g. https://vask.vosslabs.org)"
    ok "env file: $ENV_FILE"
    ok "VASK_BASE_URL=$VASK_BASE_URL"
    if [[ -n "${VASK_OG_IMAGE:-}" ]]; then
        ok "VASK_OG_IMAGE=$VASK_OG_IMAGE"
    else
        note "VASK_OG_IMAGE unset — social cards will be text-only summary"
    fi
fi

# ===== build ===========================================================

step "building bin/vask-web.linux-amd64"
( cd "$REPO_ROOT" && make build-amd64 >/dev/null )
ok "$(ls -lh "$REPO_ROOT/bin/vask-web.linux-amd64" | awk '{print $5, $9}')"

# ===== bootstrap web tier (idempotent) =================================

bootstrap_web() {
    step "bootstrapping web tier (Caddy + ports 80/443)"
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE'
set -euo pipefail

# 1. Install Caddy if absent
if ! command -v caddy >/dev/null; then
    sudo apt-get update -qq
    sudo DEBIAN_FRONTEND=noninteractive apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl >/dev/null
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
    sudo apt-get update -qq
    sudo DEBIAN_FRONTEND=noninteractive apt-get install -y caddy >/dev/null
    echo "  · caddy installed"
fi

# 2. Caddy log dir owned by caddy user
sudo mkdir -p /var/log/caddy
sudo chown caddy:caddy /var/log/caddy

# 3. Open ports 80, 443 in iptables (Oracle VCN security list is your job)
for port in 80 443; do
    if ! sudo iptables -C INPUT -p tcp --dport "$port" -j ACCEPT 2>/dev/null; then
        sudo iptables -I INPUT -p tcp --dport "$port" -j ACCEPT
        if   command -v netfilter-persistent >/dev/null; then sudo netfilter-persistent save >/dev/null 2>&1 || true
        elif [[ -d /etc/iptables ]]; then sudo iptables-save | sudo tee /etc/iptables/rules.v4 >/dev/null
        fi
        echo "  · iptables allows tcp/$port"
    fi
done

echo "web bootstrap done"
REMOTE
    ok "VM web tier ready"
    note "REMINDER: open TCP 80 and 443 in your Oracle VCN security list (manual step)"
}

# ===== push artifacts ==================================================

push_artifacts() {
    step "uploading vask-web binary, systemd unit, Caddyfile"
    scp "${SCP_OPTS[@]}" "$REPO_ROOT/bin/vask-web.linux-amd64" "$SSH_TARGET:/tmp/vask-web" >/dev/null
    scp "${SCP_OPTS[@]}" "$REPO_ROOT/deploy/vask-web.service"  "$SSH_TARGET:/tmp/" >/dev/null
    scp "${SCP_OPTS[@]}" "$REPO_ROOT/deploy/Caddyfile"         "$SSH_TARGET:/tmp/Caddyfile" >/dev/null
    ok "uploaded to /tmp/"
}

# ===== push vask-web env ===============================================

push_web_env() {
    step "writing /opt/vask/data/vask-web.env"
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" \
        'sudo install -o vask -g vask -m 0600 /dev/null /opt/vask/data/vask-web.env && sudo tee /opt/vask/data/vask-web.env >/dev/null' \
        <<EOF
# managed by deploy/setup-vask-web.sh — DO NOT edit by hand
VASK_BASE_URL=$VASK_BASE_URL
VASK_OG_IMAGE=${VASK_OG_IMAGE:-}
EOF
    ok "vask-web.env in place (mode 0600, owned by vask)"
}

# ===== install + start =================================================

install_and_start() {
    step "installing binary, systemd unit, Caddyfile, restarting services"
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "CADDY_EMAIL='${CADDY_EMAIL:-}' bash -s" <<'REMOTE'
set -euo pipefail
sudo install -o vask -g vask -m 0755 /tmp/vask-web /opt/vask/vask-web
sudo mv /tmp/vask-web.service /etc/systemd/system/
# Substitute CADDY_EMAIL into the Caddyfile if provided; otherwise the
# default in the file is used.
if [[ -n "$CADDY_EMAIL" ]]; then
    sudo sed -i "s|{\$CADDY_EMAIL:[^}]*}|$CADDY_EMAIL|g" /tmp/Caddyfile
fi
sudo install -o root -g root -m 0644 /tmp/Caddyfile /etc/caddy/Caddyfile
rm -f /tmp/Caddyfile

sudo systemctl daemon-reload
sudo systemctl enable vask-web >/dev/null 2>&1 || true
sudo systemctl restart vask-web
sudo systemctl reload caddy 2>/dev/null || sudo systemctl restart caddy
sleep 2

echo "---- vask-web status ----"
sudo systemctl status vask-web --no-pager --lines=6
echo "---- caddy status ----"
sudo systemctl status caddy --no-pager --lines=6
REMOTE
    ok "services running"
}

# ===== orchestrate =====================================================

case "$MODE" in
    full)
        bootstrap_web
        push_artifacts
        push_web_env
        install_and_start
        ;;
    update)
        push_artifacts
        install_and_start
        ;;
    rotate)
        push_web_env
        ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'sudo systemctl restart vask-web' >/dev/null
        ok "vask-web restarted"
        ;;
esac

step "done"

cat <<EOM

  share-anywhere URL:

    ${c_brand}${VASK_BASE_URL:-https://vask.vosslabs.org}${c_reset}

  smoke test (run from your laptop after DNS + cert finalize):
    ${c_dim}curl -I ${VASK_BASE_URL:-https://vask.vosslabs.org}/${c_reset}
    ${c_dim}curl -sI ${VASK_BASE_URL:-https://vask.vosslabs.org}/sitemap.xml${c_reset}

  tail logs:
    ${c_dim}ssh -p $SSH_PORT $SSH_TARGET sudo journalctl -u vask-web -f${c_reset}
    ${c_dim}ssh -p $SSH_PORT $SSH_TARGET sudo journalctl -u caddy -f${c_reset}

  redeploy after code changes:
    ${c_dim}./deploy/setup-vask-web.sh --update${c_reset}

EOM
