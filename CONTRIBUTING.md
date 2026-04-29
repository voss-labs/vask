# Contributing to voss-ask

VOSS rules apply: no applications, no interviews, no forms. Pick an issue,
ship a PR, you're a contributor.

## Setup

```sh
git clone https://github.com/voss-labs/ask
cd ask
go mod tidy
make run
# in another shell:
ssh -p 2300 localhost
```

Requires Go `>= 1.22`. No CGO needed (we use the pure-Go SQLite driver).

## Where to start

The v0.1 scaffold boots and shows a read-only feed. The v0.2 milestones in
this README's roadmap are the obvious good-first-issues:

- [ ] Compose new letters (`internal/tui/compose.go`)
- [ ] ♥ vote toggle on highlighted post (wire `store.ToggleHeart`)
- [ ] Threaded replies (`internal/tui/thread.go`)
- [ ] "maybe me" reaction
- [ ] Auto-flag confirm dialog (already implemented in `internal/policy`,
      needs UI in compose)
- [ ] Search by keyword
- [ ] Trending sort (orders by hearts in last 24h)

Bigger pieces:

- [ ] `ask-mod` binary (`cmd/ask-mod/main.go`) — separate port, mod
      allowlist, hide/unhide/ban actions.
- [ ] Live updates — pubsub channel pushes new posts to all connected
      sessions in real time.
- [ ] Auto-archive job: posts older than 90 days move out of the active feed.

## Conventions

- `go fmt` everything. `make fmt` runs `gofmt -s -w .`.
- `make vet` and `make test` must pass before opening a PR.
- Keep packages single-purpose — `auth`, `store`, `tui`, `ratelimit`, `policy`
  do exactly what their names say.
- Never log SSH IPs. Never log raw public keys. Never log post content.
- Never add a dependency without justifying it in the PR description. We
  prefer the standard library and the existing four direct deps over
  pulling in something new.

## Code review

PRs are reviewed within 48 hours. The bar is the standard VOSS bar:

- Does it ship something?
- Is it the simplest thing that ships it?
- Does it preserve the privacy guarantees in the README?

If yes to all three, it merges.

## Running the schema migration manually

The store package embeds the migration and applies it on `Open`. If you
want to inspect the schema directly:

```sh
sqlite3 ask.db < internal/store/migrations/001_init.sql
sqlite3 ask.db '.schema'
```

## License

MIT. By contributing, you agree your work is released under the same terms.
