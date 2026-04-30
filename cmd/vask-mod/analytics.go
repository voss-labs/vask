// Read-only analytics views — recent-posts, top-posts, users.
// All filter out seed users by default; pass --include-seeds for the
// raw picture (useful when debugging the seed flow itself).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func runRecentPosts(args []string) {
	fs := flag.NewFlagSet("recent-posts", flag.ExitOnError)
	since := fs.String("since", "30d", "rolling window (e.g. 1h, 24h, 7d, 30d)")
	limit := fs.Int("limit", 20, "max rows")
	includeSeeds := fs.Bool("include-seeds", false, "include posts authored by seed users")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	w, err := parseWindow(*since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad --since: %v\n", err)
		os.Exit(2)
	}
	listPosts("new", w, *limit, *includeSeeds)
}

func runTopPosts(args []string) {
	fs := flag.NewFlagSet("top-posts", flag.ExitOnError)
	by := fs.String("by", "score", "sort key: views | score | comments | new")
	since := fs.String("since", "30d", "rolling window")
	limit := fs.Int("limit", 20, "max rows")
	includeSeeds := fs.Bool("include-seeds", false, "include posts authored by seed users")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	switch *by {
	case "views", "score", "comments", "new":
	default:
		fmt.Fprintf(os.Stderr, "bad --by: %q (want views|score|comments|new)\n", *by)
		os.Exit(2)
	}
	w, err := parseWindow(*since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad --since: %v\n", err)
		os.Exit(2)
	}
	listPosts(*by, w, *limit, *includeSeeds)
}

// listPosts is the shared rendering path for both recent-posts and
// top-posts — same query under the hood, only the sort key differs.
func listPosts(sortBy string, window time.Duration, limit int, includeSeeds bool) {
	st := openStore()
	defer st.Close()
	ctx, cancel := ctxTimeout(15 * time.Second)
	defer cancel()
	posts, err := st.PostsWithStats(ctx, time.Now().Add(-window), limit, sortBy, includeSeeds)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list posts: %v\n", err)
		os.Exit(1)
	}
	if len(posts) == 0 {
		fmt.Println("(nothing in that window)")
		return
	}
	tag := "real only"
	if includeSeeds {
		tag = "real + seed"
	}
	fmt.Printf("\nposts (sort=%s · window=%s · %s)\n", sortBy, humanWindow(window), tag)
	fmt.Println(strings.Repeat("─", 96))
	fmt.Printf("%-4s  %-14s  %-5s  %-5s  %-5s  %-7s  %-8s  %s\n",
		"id", "author", "score", "cmnt", "views", "hidden", "age", "title")
	fmt.Println(strings.Repeat("─", 96))
	for _, p := range posts {
		hidden := "-"
		if p.Hidden {
			hidden = "yes"
		}
		author := p.Username
		if author == "" {
			author = fmt.Sprintf("anony-%04d", p.UserID)
		}
		fmt.Printf("%-4d  %-14s  %-5d  %-5d  %-5d  %-7s  %-8s  %s\n",
			p.ID, truncRunes(author, 14), p.Score, p.CommentCount, p.ViewCount,
			hidden, relTime(p.CreatedAt), truncRunes(p.Title, 60))
	}
	fmt.Println()
}

func runUsers(args []string) {
	fs := flag.NewFlagSet("users", flag.ExitOnError)
	since := fs.String("since", "24h", "activity window (e.g. 1h, 24h, 7d)")
	limit := fs.Int("limit", 50, "max rows")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	w, err := parseWindow(*since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad --since: %v\n", err)
		os.Exit(2)
	}
	st := openStore()
	defer st.Close()
	ctx, cancel := ctxTimeout(15 * time.Second)
	defer cancel()
	users, err := st.ActiveUsers(ctx, time.Now().Add(-w), *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "users: %v\n", err)
		os.Exit(1)
	}
	if len(users) == 0 {
		fmt.Printf("(no active real users in last %s)\n", humanWindow(w))
		return
	}
	fmt.Printf("\nactive users (last %s · real only)\n", humanWindow(w))
	fmt.Println(strings.Repeat("─", 72))
	fmt.Printf("%-4s  %-18s  %-5s  %-5s  %-5s  %s\n",
		"id", "handle", "posts", "cmnt", "votes", "last seen")
	fmt.Println(strings.Repeat("─", 72))
	for _, u := range users {
		handle := u.Username
		if handle == "" {
			handle = fmt.Sprintf("anony-%04d", u.UserID)
		}
		fmt.Printf("%-4d  %-18s  %-5d  %-5d  %-5d  %s\n",
			u.UserID, truncRunes(handle, 18), u.Posts, u.Comments, u.Votes, relTime(u.LastSeen))
	}
	fmt.Println()
}
