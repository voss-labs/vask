# Contributing to vask

Pick a thing, ship a PR. No interviews, no forms.

## Setup

```sh
git clone https://github.com/voss-labs/vask
cd vask
go mod tidy
make run
# in another shell:
ssh -p 2300 localhost
```

Requires Go `>= 1.22`. No CGO (pure-Go SQLite via `modernc.org/sqlite`).

For semantic search locally, drop Cloudflare Workers AI credentials into `.env` (see `.env.example`). Without them, `/` falls back to LIKE search.

## Where to look

| package | what's in it |
| --- | --- |
| `cmd/vask` | SSH server entrypoint |
| `cmd/vask-embed-backfill` | one-shot to embed historical posts |
| `internal/auth` | ssh pubkey → sha256 fingerprint |
| `internal/store` | libsql / sqlite layer + embedded migrations |
| `internal/tui` | bubbletea models — splash, onboard, feed, detail, compose |
| `internal/embed` | Cloudflare Workers AI client |
| `internal/username` | adjective-animal handle generator |
| `internal/ratelimit` | per-user post + comment quotas |
| `internal/policy` | compose-time pattern checks |

## Conventions

- `make fmt` (`gofmt -s -w`), `make vet`, `make test` must pass before opening a PR.
- Keep packages single-purpose. New abstraction = new package.
- Never log SSH IPs. Never log raw public keys. Never log post content.
- Never add a dependency without justifying it in the PR description.
- Comments only where logic isn't self-evident. One-liners only.
- Aim for files under 400 lines; split when growing.

## Schema changes

Migrations are version-gated and live in `internal/store/migrations/NNN_name.sql`. Each ends with an `INSERT OR IGNORE INTO _schema_version` row. Run order is enforced by the migrate loop.

```sh
sqlite3 vask.db '.schema'
sqlite3 vask.db 'SELECT * FROM _schema_version;'
```

## Code review

The bar:
- Does it ship something?
- Is it the simplest thing that ships it?
- Does it preserve the privacy guarantees in the README?

If yes to all three, it merges.

## License

MIT. By contributing, you agree your work is released under the same terms.
