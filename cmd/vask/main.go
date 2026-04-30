// Command vask is the SSH server.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	bm "github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
	"github.com/muesli/termenv"

	"github.com/voss-labs/vask/internal/auth"
	"github.com/voss-labs/vask/internal/embed"
	"github.com/voss-labs/vask/internal/store"
	"github.com/voss-labs/vask/internal/tui"
)

// Force truecolor escape codes regardless of the host TERM.
// systemd starts vask with no TERM set, which would otherwise make
// lipgloss/termenv emit plain text. Every SSH client we care about
// renders 24-bit color, so this is safe and fixes the "no colors over
// SSH" issue universally.
func init() {
	lipgloss.SetColorProfile(termenv.TrueColor)
}

const connectBanner = `
welcome to vask — anonymous campus q&a, terminal-native.

if you see "permission denied (publickey)" below, you don't have an
ssh key on this device yet. one-time setup:

    ssh-keygen -t ed25519 -N "" -f ~/.ssh/id_ed25519

then retry: ssh vask.vosslabs.org
your key never leaves your machine — vask only reads sha256(pubkey).

`

func main() {
	host := flag.String("host", "0.0.0.0", "interface to bind")
	port := flag.String("port", "2300", "ssh port (local dev default; production uses 22 via systemd)")
	dbPath := flag.String("db", "ask.db", "fallback local sqlite file (used only when TURSO_DATABASE_URL is unset)")
	hostKey := flag.String("host-key", "host_ed25519", "ssh host key (auto-generated if missing)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	dbURL := os.Getenv("TURSO_DATABASE_URL")
	dbToken := os.Getenv("TURSO_AUTH_TOKEN")
	dbTarget := dbURL
	mode := "turso"
	if dbTarget == "" {
		dbTarget = *dbPath
		mode = "local-sqlite"
	}

	st, err := store.Open(dbTarget, dbToken)
	if err != nil {
		slog.Error("open store", "err", err, "mode", mode)
		os.Exit(1)
	}
	defer st.Close()
	slog.Info("store ready", "mode", mode, "target", redact(dbTarget))

	// Optional Cloudflare-backed embedding client. FromEnv returns nil if
	// CF_ACCOUNT_ID / CF_AI_TOKEN aren't set, in which case CreatePost
	// will skip the embedding side-effect and posts are stored without
	// vectors — fine for local dev and any deploy that hasn't enabled it.
	if ec := embed.FromEnv(); ec != nil {
		st.UseEmbedClient(ec)
		slog.Info("embeddings enabled", "model", "@cf/baai/bge-m3", "dims", embed.VectorDim)
	} else {
		slog.Info("embeddings disabled", "reason", "CF_ACCOUNT_ID/CF_AI_TOKEN not set")
	}

	srv, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(*host, *port)),
		wish.WithHostKeyPath(*hostKey),
		// Pre-auth banner: shown to every connecting client before auth
		// completes. Helps users on a fresh machine who don't yet have an
		// ssh key, since their auth will fail with "permission denied
		// (publickey)" and they'd otherwise have no idea what to do.
		wish.WithBanner(connectBanner),
		wish.WithPublicKeyAuth(func(_ ssh.Context, _ ssh.PublicKey) bool {
			// accept any pubkey; identity is the fingerprint hash, not a whitelist.
			return true
		}),
		wish.WithMiddleware(
			bm.Middleware(teaHandler(st)),
			activeterm.Middleware(), // require pty (block automated probes)
			logging.Middleware(),
		),
	)
	if err != nil {
		slog.Error("create wish server", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			slog.Error("listen", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// redact strips any ?authToken=... query off a connection string before logging.
func redact(s string) string {
	if i := strings.Index(s, "authToken="); i >= 0 {
		return s[:i] + "authToken=<redacted>"
	}
	return s
}

// teaHandler resolves the SSH session into a per-connection bubbletea program.
func teaHandler(st *store.Store) func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	return func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
		pty, _, active := s.Pty()
		if !active {
			wish.Fatalln(s, "interactive terminal required (allocate a pty)")
			return nil, nil
		}

		fp := auth.Fingerprint(s.PublicKey())
		if fp == "" {
			wish.Fatalln(s, "no public key on session")
			return nil, nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		user, err := st.UpsertUser(ctx, fp)
		if err != nil {
			slog.Error("upsert user", "err", err)
			wish.Fatalln(s, "internal error")
			return nil, nil
		}
		if user.Banned {
			wish.Fatalln(s, "this key has been suspended.\nemail mods if you think this is in error.")
			return nil, nil
		}

		slog.Info("session start",
			"user_id", user.ID,
			"first_time", user.TOSAcceptedAt == nil,
			"term", pty.Term,
		)

		app := tui.NewApp(st, user)
		return app, []tea.ProgramOption{
			tea.WithAltScreen(),
			tea.WithMouseCellMotion(),
		}
	}
}
