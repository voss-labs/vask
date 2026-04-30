// Command vask-mod is the operator console — moderation actions plus
// observability stats. Sibling binary to cmd/vask and cmd/vask-seed,
// shares the same store layer and env-var configuration.
//
// Auth model: trust-the-operator. Anyone with TURSO_DATABASE_URL +
// TURSO_AUTH_TOKEN exported in their shell can run this binary. For
// any action that writes a moderation_actions row (hide-post,
// unhide-post, hide-comment, unhide-comment) also export
// VASK_MOD_USER=<users.id> so the audit trail names the moderator.
//
// Open source by design — running this tool requires database
// credentials. The code in front of those credentials is auditable
// here so anyone can verify what mods can and can't do.
//
// Usage:
//
//	vask-mod stats               # default window: last 24h
//	vask-mod recent-posts        # latest 20 real posts with stats
//	vask-mod top-posts --by views
//	vask-mod users --since 24h
//	vask-mod hide-post 42 --reason "real name"
//	vask-mod log
//	vask-mod watch
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/voss-labs/vask/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(logger)

	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "stats":
		runStats(args)
	case "recent-posts":
		runRecentPosts(args)
	case "top-posts":
		runTopPosts(args)
	case "users":
		runUsers(args)
	case "hide-post":
		runHide(args, "post")
	case "unhide-post":
		runUnhide(args, "post")
	case "hide-comment":
		runHide(args, "comment")
	case "unhide-comment":
		runUnhide(args, "comment")
	case "log":
		runLog(args)
	case "watch":
		runWatch(args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `vask-mod — operator console for vask

USAGE
  vask-mod <subcommand> [flags]

OBSERVABILITY (read-only)
  stats                   real/seed/hidden counts plus rolling-window deltas
  recent-posts            latest posts with score, comments, views
  top-posts --by KEY      KEY ∈ {views, score, comments, new}; window-bounded
  users --since 24h       active real users in the window
  log                     recent moderation_actions audit trail
  watch                   live tail of new posts and comments

MODERATION (writes audit row, requires VASK_MOD_USER)
  hide-post     <id> --reason "..."
  unhide-post   <id> --reason "..."
  hide-comment  <id> --reason "..."
  unhide-comment <id> --reason "..."

REQUIRED ENV
  TURSO_DATABASE_URL       libsql://...   (production)
  TURSO_AUTH_TOKEN         operator's Turso token
  VASK_MOD_USER            users.id of the moderator (for hide/unhide only)

PER-SUBCOMMAND FLAGS
  vask-mod <subcommand> --help`)
}

// === shared helpers ====================================================

// openStore mirrors how cmd/vask and cmd/vask-seed wire the store. Falls
// back to local sqlite when TURSO_DATABASE_URL is empty so dev runs
// don't need prod creds.
func openStore() *store.Store {
	dbURL := os.Getenv("TURSO_DATABASE_URL")
	dbToken := os.Getenv("TURSO_AUTH_TOKEN")
	target := dbURL
	if target == "" {
		target = "vask.db"
	}
	st, err := store.Open(target, dbToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		os.Exit(1)
	}
	return st
}

// modUser reads VASK_MOD_USER and refuses to proceed if unset or
// malformed. Required by every action that writes a moderation_actions
// row — keeps anonymous moderation impossible by design.
func modUser() int64 {
	raw := strings.TrimSpace(os.Getenv("VASK_MOD_USER"))
	if raw == "" {
		fmt.Fprintln(os.Stderr, "VASK_MOD_USER is required for moderation actions")
		fmt.Fprintln(os.Stderr, "set it to the moderator's users.id, e.g. `export VASK_MOD_USER=1`")
		os.Exit(2)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintf(os.Stderr, "VASK_MOD_USER must be a positive integer (got %q)\n", raw)
		os.Exit(2)
	}
	return id
}

func ctxTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// parseWindow accepts "1h", "24h", "7d", "30d", "30m" and a few common
// shapes. Falls through to time.ParseDuration so "90m" works too. Days
// are added explicitly because Go's stdlib doesn't know about them.
func parseWindow(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid days: %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("window must be positive: %q", s)
	}
	return d, nil
}

func humanWindow(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	if d >= time.Hour && d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return d.String()
}

// truncRunes limits a string to n display runes, appending "…" on
// truncation. ASCII-only inputs work too; emoji-free by design.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// relTime renders a duration since t as "Xm ago" / "Xh ago" / "Xd ago"
// for compact log/event output.
func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
