package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/voss-labs/vask/internal/store"
)

func runStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	since := fs.String("since", "24h", "rolling window for new-* counts (e.g. 1h, 24h, 7d, 30d)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	window, err := parseWindow(*since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad --since: %v\n", err)
		os.Exit(2)
	}
	st := openStore()
	defer st.Close()

	ctx, cancel := ctxTimeout(15 * time.Second)
	defer cancel()
	stats, err := st.Stats(ctx, time.Now().Add(-window))
	if err != nil {
		fmt.Fprintf(os.Stderr, "stats: %v\n", err)
		os.Exit(1)
	}
	printStats(stats, window)
}

// printStats lays out the dashboard. Two sections: cumulative totals on
// top, rolling-window deltas at the bottom. Tabular but no terminal
// dependency — wide enough for a 100-col terminal, narrow enough to
// paste into Slack.
func printStats(s *store.Stats, window time.Duration) {
	fmt.Println()
	fmt.Println("vask-mod stats — " + time.Now().Format(time.RFC3339))
	fmt.Println(strings.Repeat("─", 56))
	fmt.Println()
	row := func(label string, n int) {
		fmt.Printf("  %-26s %6d\n", label, n)
	}
	fmt.Println("real")
	row("users (alive)", s.RealUsers)
	row("posts (visible)", s.RealPosts)
	row("comments (visible)", s.RealComments)
	row("post votes", s.PostVotes)
	row("comment votes", s.CommentVotes)
	fmt.Println()
	fmt.Println("seed / hidden / deleted")
	row("seed users", s.SeedUsers)
	row("seed posts", s.SeedPosts)
	row("hidden posts", s.HiddenPosts)
	row("hidden comments", s.HiddenComments)
	row("deleted (tombstoned) users", s.DeletedUsers)
	fmt.Println()
	fmt.Printf("last %s\n", humanWindow(window))
	row("new users", s.NewUsersWindow)
	row("new posts", s.NewRealPostsWindow)
	row("new comments", s.NewRealCommentsWindow)
	row("unique posters", s.UniquePostersWindow)
	row("unique commenters", s.UniqueCommentersWindow)
	fmt.Println()
}
