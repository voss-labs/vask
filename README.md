# voss-ask

> The campus q&a forum that doesn't want your email.
> Open source. Anonymous by math. Terminal-native.

```sh
ssh ask.vosslabs.org
```

A VOSS (Vidyalankar Open Source Software Labs) project. Three channels at
launch — `#complaints`, `#electives`, `#general` — for everything from
hostel mess rants to elective-picking advice.

## How it works

- You connect with `ssh ask.vosslabs.org`. Your SSH client does the handshake.
- The server reads your **public-key fingerprint** and computes `sha256(fingerprint)`.
- That hash is your only identity. No email, no real name, no IP logged.
- You browse channels, post questions, vote on others — all in your terminal.
- All code is in this repo. Every privacy claim above is auditable.

## Status

**v0.1 — bootable forum, posts + voting. Threaded comments deferred to v0.2.**

What works in v0.1:

- SSH server on port `2300` (dev) / `22` (prod) accepting any pubkey.
- First-connection TOS screen, persisted per fingerprint.
- Channel picker (entry view) + per-channel feed + unified "all" feed.
- Compose new post (3-step wizard: channel → title → body). 5 posts/day rate-limit.
- Up/down voting on posts. Hot / new / top sort modes.
- Anonymous IDs (`anony-0042`) with `(you)` highlight on your own posts.
- Delete-own-post (transactional cascade across votes + comments).
- Brand-matched TUI, responsive viewport, full color over SSH.

What's deferred to v0.2:

- Threaded comments + reply modal + comment voting.
- `ask-mod` CLI (schema is ready, only the CLI wiring is pending).
- Search across posts.
- "Top of today" trending sort.

## Quick start (local dev)

Requires Go `>= 1.22`.

```sh
git clone https://github.com/voss-labs/ask
cd ask
go mod tidy
make run            # SSH server on port 2300

# in another terminal:
ssh -p 2300 -i ~/.ssh/id_ed25519 -o IdentitiesOnly=yes localhost
```

First connect generates a `host_ed25519` host key in the working directory
and an `ask.db` SQLite file. Both are gitignored.

## Project layout

```
voss-ask/
├── cmd/
│   ├── ask/                   # public SSH server
│   └── ask-mod/               # moderator CLI (v0.2 — currently stubbed)
├── internal/
│   ├── auth/                  # ssh pubkey → sha256 fingerprint
│   ├── store/                 # libsql/sqlite layer + embedded migration
│   │   └── migrations/
│   │       └── 001_init.sql
│   ├── tui/                   # bubbletea models
│   │   ├── app.go             # state machine
│   │   ├── style.go           # colors, frame, footer rules
│   │   ├── splash.go          # first-connect TOS
│   │   ├── channel.go         # channel picker
│   │   ├── feed.go            # post list
│   │   ├── compose.go         # 3-step post wizard
│   │   └── detail.go          # full post + comments stub
│   ├── ratelimit/             # per-user post quota
│   └── policy/                # auto-flag regex
├── deploy/                    # systemd + Oracle Cloud scripts
├── Makefile
├── go.mod
├── README.md
├── CONTRIBUTING.md
├── TOS.md                     # shown on first connect
└── LICENSE
```

## Stack

| Layer | Choice |
| --- | --- |
| Language | Go 1.22+ |
| SSH server | `github.com/charmbracelet/wish` |
| TUI runtime | `github.com/charmbracelet/bubbletea` |
| Styling | `github.com/charmbracelet/lipgloss` |
| Storage | Turso (libsql) — pure-Go, decoupled from compute |
| Local dev fallback | `modernc.org/sqlite` (no CGO) |
| Process manager | `systemd` (production) |
| Hosting | Oracle Cloud Always Free, separate VM from confess |

## Privacy guarantees

What we **never** store:

- The raw public key. Only `sha256(marshalled_pubkey)`.
- IP addresses.
- SSH client name, terminal type, geographic info.
- Email, phone, real name, college details.

What we **do** store:

- Your fingerprint hash (irreversible).
- Posts, comments, and votes you submit.

Code is open source — read `internal/auth/fingerprint.go` and
`internal/store/store.go` to verify the above is structurally true.

## Deploying

See [`deploy/README.md`](./deploy/README.md). Target environment is a *second*
Oracle Cloud Always Free instance (separate from confess) so `ask.vosslabs.org`
gets its own port-22 binding without conflicting with `confess.vosslabs.org`.

## Contributing

See [`CONTRIBUTING.md`](./CONTRIBUTING.md). Pick something from the v0.2
backlog (comments, mod tool, search), fork, build, ship a PR.

## License

MIT — see [`LICENSE`](./LICENSE).
