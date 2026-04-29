# Deploy

Target: **Oracle Cloud Always Free, Ampere ARM (`VM.Standard.A1.Flex`),
Mumbai region** or any other Always Free zone. Free forever.

## One-time VM setup

1. Sign up at https://www.oracle.com/cloud/free/. Pick **Mumbai** as home region.
2. Compute → Instances → Create.
   - Image: Canonical Ubuntu 22.04
   - Shape: `VM.Standard.A1.Flex`, **1 OCPU + 6 GB RAM**
   - Boot volume: 50 GB
   - Add your SSH key.
3. Networking → security list: allow inbound TCP **22** (public) and **2201** (mod, optional).
4. DNS at the registrar: A-record `ask.vosslabs.org` → VM public IP.

## Bootstrap the VM

SSH in once with your management key:

```sh
ssh ubuntu@<vm-ip>
```

Then on the VM:

```sh
# create the runtime user + dirs
sudo useradd --system --home /opt/ask --shell /usr/sbin/nologin ask
sudo mkdir -p /opt/ask/data
sudo chown -R ask:ask /opt/ask

# firewall
sudo ufw allow 22/tcp
sudo ufw allow 2201/tcp
sudo ufw enable
```

## Database — Turso

The compute layer is decoupled from storage: the SQLite-compatible database
lives at Turso (libsql), not on this VM. That means we can move servers
without touching data, and Turso handles backups / point-in-time restore.

1. **Create a database** at <https://app.turso.tech>:
   `voss-ask-harshalmore31` (region close to your audience).
2. **Issue a database token** for the running server. From the Turso CLI on
   your laptop:
   ```sh
   turso db tokens create voss-ask-harshalmore31 --expiration none
   ```
   Copy the token — you won't see it again.
3. **Drop credentials on the VM** in `/opt/ask/data/turso.env`:
   ```sh
   sudo tee /opt/ask/data/turso.env > /dev/null <<'EOF'
   TURSO_DATABASE_URL=libsql://voss-ask-harshalmore31.aws-us-east-1.turso.io
   TURSO_AUTH_TOKEN=<paste-token-here>
   EOF
   sudo chmod 600 /opt/ask/data/turso.env
   sudo chown ask:ask /opt/ask/data/turso.env
   ```
   The systemd unit reads this file via `EnvironmentFile=` — only the
   `ask` user (and root) can read it.

   If `TURSO_DATABASE_URL` is unset (or you point it at a `file:` path),
   the binary falls back to a local SQLite file on the VM. Useful for
   smoke-tests; not for production.

## Build and deploy from your laptop

```sh
# in the repo:
make build-arm64
scp bin/ask.linux-arm64 ubuntu@<vm-ip>:/tmp/ask
scp deploy/ask.service ubuntu@<vm-ip>:/tmp/

# on the VM:
sudo mv /tmp/ask /opt/ask/ask
sudo chmod +x /opt/ask/ask
sudo chown ask:ask /opt/ask/ask

sudo mv /tmp/ask.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ask
sudo systemctl status ask
```

## Updates

```sh
make build-arm64
scp bin/ask.linux-arm64 ubuntu@<vm-ip>:/tmp/ask
ssh ubuntu@<vm-ip> 'sudo install -o ask -g ask -m 0755 /tmp/ask /opt/ask/ask && sudo systemctl restart ask'
```

## Backups (nightly)

On the VM, add to root's crontab (`sudo crontab -e`):

```cron
0 3 * * * sqlite3 /opt/ask/data/ask.db ".backup '/opt/ask/data/backup-$(date +\%F).db'" && find /opt/ask/data/ -name 'backup-*.db' -mtime +14 -delete
```

That keeps the last 14 daily snapshots locally. Push them off-box (Backblaze
B2, S3, etc.) once the user base justifies it.

## Logs

```sh
sudo journalctl -u ask -f          # live tail
sudo journalctl -u ask --since '1h ago'
```

The application code never logs IPs or post bodies. The systemd unit logs to
journald at info level (session start, errors, shutdown).

## Privacy hardening checklist

Before the public launch — verify all of these on the VM:

- [ ] No web server / nginx / caddy on the box. SSH talks directly to ask.
- [ ] No third-party metrics agent (Datadog, New Relic, Posthog, etc.).
- [ ] Cloud provider metric agents disabled if possible.
- [ ] `journald` retention set to a short window (e.g. 7 days), not forever.
- [ ] DNS provider is one we trust (Cloudflare DNS-only, no proxy, is fine —
      they only see DNS lookups, not SSH content).

## Disaster recovery

The state of the world is one SQLite file (`ask.db`) plus the host key
(`host_ed25519`). To restore on a fresh VM: `scp` both files into
`/opt/ask/data/`, fix permissions, `systemctl start ask`.
