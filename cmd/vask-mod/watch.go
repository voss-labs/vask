// Live event tail. Polls posts + comments every few seconds and prints
// each new row as a single line. Excludes votes (high-frequency,
// low-signal) — `vask-mod stats` covers vote totals separately.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/voss-labs/vask/internal/store"
)

func runWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	interval := fs.Duration("interval", 3*time.Second, "polling interval (default 3s)")
	includeSeeds := fs.Bool("include-seeds", false, "include events from seed users")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *interval < time.Second {
		// Sub-second polling is wasteful — Turso round-trip is ~200ms
		// from a far region and the operator can't read events that fast.
		*interval = time.Second
	}
	st := openStore()
	defer st.Close()

	// Skip historical rows: capture the current max ids so the first
	// tick only reports what lands AFTER the watch starts.
	seedCtx, seedCancel := ctxTimeout(8 * time.Second)
	maxPostID, maxCommentID, err := st.MaxEventIDs(seedCtx)
	seedCancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init watch: %v\n", err)
		os.Exit(1)
	}

	tag := "real only"
	if *includeSeeds {
		tag = "real + seed"
	}
	fmt.Printf("watching new posts + comments (%s · poll %s)\n", tag, *interval)
	fmt.Println(strings.Repeat("─", 72))
	fmt.Println("(ctrl+c to exit)")
	fmt.Println()

	// Catch SIGINT so the loop exits cleanly without a stack trace.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-sigCh:
			fmt.Println("\n(stopped)")
			return
		case <-ticker.C:
			ctx, cancel := ctxTimeout(*interval + 4*time.Second)
			events, err := st.EventsSince(ctx, maxPostID, maxCommentID, 50)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "poll: %v\n", err)
				continue
			}
			for _, e := range events {
				if e.IsSeed && !*includeSeeds {
					if e.Kind == "post" && e.ID > maxPostID {
						maxPostID = e.ID
					}
					if e.Kind == "comment" && e.ID > maxCommentID {
						maxCommentID = e.ID
					}
					continue
				}
				printEvent(e)
				if e.Kind == "post" && e.ID > maxPostID {
					maxPostID = e.ID
				}
				if e.Kind == "comment" && e.ID > maxCommentID {
					maxCommentID = e.ID
				}
			}
		}
	}
}

// printEvent renders a single line per row. Posts and comments share
// the same shape so the operator's eye can scan a column.
func printEvent(e store.FeedEvent) {
	author := e.Username
	if author == "" {
		author = fmt.Sprintf("anony-%04d", e.UserID)
	}
	stamp := e.CreatedAt.Format("15:04:05")
	switch e.Kind {
	case "post":
		fmt.Printf("%s  POST     #%-4d  %-16s  %s\n",
			stamp, e.ID, truncRunes(author, 16), truncRunes(e.Title, 60))
	case "comment":
		fmt.Printf("%s  COMMENT  #%-4d  %-16s  → post #%d  %s\n",
			stamp, e.ID, truncRunes(author, 16), e.PostID,
			truncRunes(strings.ReplaceAll(e.Snippet, "\n", " "), 50))
	}
}
