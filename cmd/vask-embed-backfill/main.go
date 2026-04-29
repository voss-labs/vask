// Command ask-embed-backfill walks every post that has no embedding yet
// and embeds it via Cloudflare Workers AI (bge-m3). Run once after
// adding the embedding migration to populate historical posts; run again
// any time embedding generation has been failing in the background and
// you want to catch up.
//
// Usage:
//
//	TURSO_DATABASE_URL=libsql://… \
//	TURSO_AUTH_TOKEN=… \
//	CF_ACCOUNT_ID=… CF_AI_TOKEN=… \
//	go run ./cmd/vask-embed-backfill
//
// Idempotent: only touches rows WHERE embedding IS NULL. Safe to re-run.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/voss-labs/vask/internal/embed"
	"github.com/voss-labs/vask/internal/store"
)

func main() {
	dbPath := flag.String("db", "ask.db", "fallback local sqlite file (used only when TURSO_DATABASE_URL is unset)")
	batch := flag.Int("batch", 50, "posts per batch (avoids long single transactions on Turso)")
	pause := flag.Duration("pause", 100*time.Millisecond, "delay between embed calls so we don't hammer Cloudflare")
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

	ec := embed.FromEnv()
	if ec == nil {
		slog.Error("CF_ACCOUNT_ID and CF_AI_TOKEN must be set to backfill")
		os.Exit(1)
	}
	st.UseEmbedClient(ec)

	slog.Info("backfill starting", "mode", mode, "batch", *batch, "pause", *pause)

	total := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		posts, err := st.PostsMissingEmbedding(ctx, *batch)
		cancel()
		if err != nil {
			slog.Error("list posts", "err", err)
			os.Exit(1)
		}
		if len(posts) == 0 {
			break
		}
		for _, p := range posts {
			st.EmbedAndSavePost(p.ID, p.Title, p.Body)
			total++
			fmt.Printf("  ✓ post #%d  %s\n", p.ID, truncate(p.Title, 64))
			time.Sleep(*pause)
		}
		slog.Info("batch done", "batch_size", len(posts), "total_so_far", total)
	}

	slog.Info("backfill complete", "total_embedded", total)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
