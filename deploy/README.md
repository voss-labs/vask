# Deploy

Target: **Oracle Cloud Always Free** (Ampere ARM `VM.Standard.A1.Flex` or AMD `VM.Standard.E2.1.Micro`). Free forever.

Two paths:
- **Bootstrap a fresh VM** — `./deploy/setup-oracle.sh` (one-time manual)
- **Push subsequent updates** — GitHub Actions on `v*.*.*` tag (automated)

## TL;DR — first deploy on a fresh VM

```sh
cp .env.example .env
chmod 600 .env
$EDITOR .env       # fill in TURSO_* (required) and CF_* (optional)

./deploy/setup-oracle.sh
```

The script:
- loads secrets only from `.env` (or `--env-file PATH`)
- pipes them to the VM over SSH stdin (never as command args, so they don't appear in `ps`)
- writes `/opt/vask/data/turso.env` mode `0600` owned by the unprivileged `vask` user
- builds + uploads the binary, installs the systemd unit, starts the service

Then add a Cloudflare DNS A-record `vask.<your-domain> → <VM_IP>` (DNS-only, grey cloud — proxy doesn't work on port 22).

```sh
ssh vask.<your-domain>
```

## One-time VM setup (on Oracle Cloud)

1. Sign up at <https://www.oracle.com/cloud/free/>. Pick a region close to your users.
2. Compute → Instances → Create.
   - Image: Canonical Ubuntu 22.04
   - Shape: `VM.Standard.A1.Flex` (1 OCPU + 6 GB RAM) or `VM.Standard.E2.1.Micro` (1 OCPU + 1 GB RAM)
   - Add your SSH key.
3. Networking → security list: allow inbound TCP **22** (public).
4. Move sshd off port 22 so vask can bind it: see [`migrate-port-22.sh`](./migrate-port-22.sh).
5. Run `./deploy/setup-oracle.sh` from your laptop.

## Database — Turso

Storage is decoupled from compute: SQLite-compatible DB lives at Turso (libsql), not on the VM.

```sh
turso db create vask-<you> --location <region>     # bom = Mumbai, sin = Singapore, iad = US East
turso db show vask-<you> --url                     # → TURSO_DATABASE_URL
turso db tokens create vask-<you> --expiration none # → TURSO_AUTH_TOKEN
```

Paste both into `.env`. The deploy script writes them to `/opt/vask/data/turso.env` mode `0600`, owned by `vask:vask`. Never edit that file by hand — re-run the script.

If `TURSO_DATABASE_URL` is unset, the binary falls back to a local SQLite file. Useful for smoke tests; not for production.

## Embeddings — Cloudflare Workers AI (optional)

Semantic search, the similar-posts rail, and compose tag suggestions all run on `@cf/baai/bge-m3`.

1. **Account ID** — dashboard → Workers & Pages → Overview → right sidebar.
2. **Token** — dashboard → My Profile → API Tokens → Create token → "Workers AI" template.
3. Add to `.env`:
   ```
   CF_ACCOUNT_ID=<account id>
   CF_AI_TOKEN=<workers AI scoped token>
   ```
4. Re-deploy (full setup or `--rotate-token`).
5. Backfill historical posts:
   ```sh
   source .env
   go run ./cmd/vask-embed-backfill
   ```

Leaving `CF_*` blank disables embeddings end-to-end. The app still runs; `/` falls back to LIKE search, the similar-posts rail stays hidden, no compose suggestions.

Cost at typical campus scale: ~1,300 neurons/month vs free tier's 10K *per day* — effectively free.

## Secret-handling rules

- `.env` is in `.gitignore`. Never commit it.
- The deploy scripts pass secrets to the VM over SSH stdin, never as command-line arguments — so they don't appear in `ps` on the VM or in your shell history.
- Remote `/opt/vask/data/turso.env` is created via `install -m 0600` so the file is never world-readable, even mid-write.
- The script refuses to load an env file that's mode `0644` or wider — fix with `chmod 600 .env` if you see the warning.

## Updates after the first deploy

### Manual (any commit)

```sh
./deploy/setup-oracle.sh --update
```

Skips bootstrap, just rebuilds + restarts.

### Automated (tag release)

Push a `v*.*.*` tag and GitHub Actions handles it:

```sh
git tag v0.3.0
git push origin v0.3.0
```

The `release` workflow builds Linux binaries for arm64 + amd64 and attaches them to a GitHub Release. The `deploy` workflow then SSHes to the Oracle VM and rolls the new binary onto the live `vask` service.

For this to work, set the following repo secrets (Settings → Secrets and variables → Actions):

| secret | purpose |
| --- | --- |
| `ORACLE_SSH_HOST` | VM public IP |
| `ORACLE_SSH_PORT` | management SSH port (e.g. `22000`) |
| `ORACLE_SSH_USER` | `ubuntu` |
| `ORACLE_SSH_KEY` | full contents of the private key file |
| `TURSO_DATABASE_URL` | same as `.env` |
| `TURSO_AUTH_TOKEN` | same as `.env` |
| `CF_ACCOUNT_ID` | optional |
| `CF_AI_TOKEN` | optional |

The deploy workflow has `if: github.repository == 'voss-labs/vask'` — it's a no-op on forks even if you push tags. Forkers can still use the `release` workflow to publish their own builds without secrets.

## Backups (nightly)

On the VM, root crontab (`sudo crontab -e`):

```cron
0 3 * * * sqlite3 /opt/vask/data/vask.db ".backup '/opt/vask/data/backup-$(date +\%F).db'" && find /opt/vask/data/ -name 'backup-*.db' -mtime +14 -delete
```

Keeps the last 14 daily snapshots locally. Push them off-box (Backblaze B2, S3, etc.) once user count justifies it. Turso also has built-in point-in-time restore in the dashboard.

## Logs

```sh
sudo journalctl -u vask -f
sudo journalctl -u vask --since '1h ago'
```

The application code never logs IPs or post bodies. The systemd unit logs to journald at info level: session start, errors, shutdown.

## Privacy hardening checklist

Before the public launch — verify all of these on the VM:

- [ ] No web server (nginx, caddy) on the box. SSH talks directly to vask.
- [ ] No third-party metrics agent (Datadog, New Relic, PostHog).
- [ ] Cloud provider metric agents disabled where possible.
- [ ] `journald` retention set to a short window (7 days) — not forever.
- [ ] DNS provider is one you trust (Cloudflare DNS-only is fine — they only see lookups, not SSH content).

## Disaster recovery

State of the world: one libsql/SQLite database (in Turso) plus the host key (`/opt/vask/data/host_ed25519`). To restore on a fresh VM:

1. Run `./deploy/setup-oracle.sh` against the new VM.
2. SCP the saved `host_ed25519` into `/opt/vask/data/`.
3. Update DNS A-record to the new IP.
4. `systemctl restart vask`.
