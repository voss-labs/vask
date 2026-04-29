# vask

> Campus q&a forum that doesn't want your email.
> Open source. Anonymous by math. Terminal-native.

```sh
ssh vask.vosslabs.org
```

A [VOSS Labs](https://vosslabs.org) project. Open the SSH client you already have, get a campus forum. No web app, no notifications, no inbox.

## How it works

- You connect with `ssh vask.vosslabs.org`. Your SSH client does the handshake.
- The server reads your **public-key fingerprint** and stores `sha256(fingerprint)`.
- That hash is your only identity. No email, no real name, no IP logged.
- You browse, post, vote, comment — all in your terminal.
- Everything in this repo. Every privacy claim is auditable in the source.

## What's in it

- **Feed** — hot / new / top sort, tag filter, semantic search (`/`), per-page navigation (`[`/`]`).
- **Detail** — threaded comments, reply, vote, collapse subtrees, "similar posts" rail.
- **Compose** — 3-step wizard with live preview and tag suggestions inferred from semantically similar posts.
- **Identity** — auto-generated handles like `polite-okapi` (race-safe atomic claim, no real names).
- **Activity** — `Y` opens "your last posts + comments" for one-key thread navigation.
- **Help** — `?` opens the full keybind sheet, `i` opens the about screen.

## Quick start (local dev)

Requires Go `>= 1.24`.

```sh
git clone https://github.com/voss-labs/vask
cd vask
go mod tidy
make run            # SSH server on port 2300

# in another terminal:
ssh -p 2300 -i ~/.ssh/id_ed25519 -o IdentitiesOnly=yes localhost
```

First connect generates `host_ed25519` and `vask.db` in the working directory. Both are gitignored.

To enable semantic search locally, add Cloudflare Workers AI credentials to `.env` (see `.env.example`). Without them, `/` falls back to substring search.

## Project layout

```
vask/
├── cmd/
│   ├── vask/                       # SSH server
│   └── vask-embed-backfill/        # one-shot: embed historical posts
├── internal/
│   ├── auth/                       # ssh pubkey → sha256 fingerprint
│   ├── store/                      # libsql / sqlite layer + embedded migrations
│   │   └── migrations/             # 001 → 006, version-gated
│   ├── tui/                        # bubbletea models (splash, onboard, feed, detail, compose)
│   ├── embed/                      # Cloudflare Workers AI client (bge-m3)
│   ├── username/                   # auto-handle generator (adjective-animal)
│   ├── ratelimit/                  # per-user post + comment quotas
│   └── policy/                     # auto-flag patterns at compose time
├── deploy/                         # systemd unit + Oracle Cloud scripts
├── Makefile
├── go.mod
├── README.md
├── CONTRIBUTING.md
├── TOS.md
└── LICENSE
```

## Stack

| Layer | Choice |
| --- | --- |
| Language | Go 1.24+ |
| SSH server | [`charmbracelet/wish`](https://github.com/charmbracelet/wish) |
| TUI | [`bubbletea`](https://github.com/charmbracelet/bubbletea) + [`lipgloss`](https://github.com/charmbracelet/lipgloss) |
| Storage | Turso ([libsql](https://github.com/tursodatabase/libsql)) |
| Local dev | [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite) (no CGO) |
| Embeddings | Cloudflare Workers AI (`@cf/baai/bge-m3`, 1024 dim) — optional |
| Process | systemd (production) |

## Privacy guarantees

What we **never** store:

- The raw public key. Only `sha256(marshalled_pubkey)`.
- IP addresses.
- SSH client name, terminal type, geographic info.
- Email, phone, real name, college details.

What we **do** store:

- Your fingerprint hash (irreversible).
- Posts, comments, votes you submit.
- Your auto-generated handle once you pick one.

Read [`internal/auth/fingerprint.go`](./internal/auth/) and [`internal/store/store.go`](./internal/store/store.go) to verify.

## Keybinds

| key | feed | detail |
| --- | --- | --- |
| `↑↓` `j/k` | move cursor | move cursor |
| `g` `G` | top / bottom | top / bottom |
| `[` `]` | prev / next page | — |
| `⏎` | open post | — |
| `n` | new post | — |
| `r` | reload | reply to focused thing |
| `v` `V` | upvote / downvote | upvote / downvote |
| `c` | — | collapse / expand subtree |
| `space` | toggle compact mode | expand long body (cursor on post) |
| `1`–`3` | — | jump to similar post |
| `/` | search | — |
| `#` | tag filter | — |
| `m` | toggle "only my posts" | — |
| `f` | cycle sort | — |
| `esc` `b` | peel back filter / page | back to feed |
| `x` | clear all filters | — |
| `Y` | your activity | — |
| `i` | about | about |
| `?` | help | help |
| `d` `D` | arm / confirm delete (own) | arm / confirm delete (own) |
| `q` `ctrl+c` | quit | quit |

## Deploying

See [`deploy/README.md`](./deploy/README.md). Target is Oracle Cloud Always Free.

## Contributing

See [`CONTRIBUTING.md`](./CONTRIBUTING.md).

## License

MIT — see [`LICENSE`](./LICENSE).
