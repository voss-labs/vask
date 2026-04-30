// Moderation actions — hide/unhide posts and comments + the audit log.
// Every action writes a row in moderation_actions identified by
// VASK_MOD_USER. Audit is enforced at the binary, can't be bypassed by
// the operator running the tool (without manual SQL, which leaves a
// different kind of trail anyway).
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// runHide handles `hide-post <id>` and `hide-comment <id>`. The kind
// parameter selects which target — keeps both flows in one function
// since they differ only in the store call and the audit action name.
func runHide(args []string, kind string) {
	id, reason := parseModArgs(args, "hide-"+kind)
	uid := modUser()

	st := openStore()
	defer st.Close()
	ctx, cancel := ctxTimeout(8 * time.Second)
	defer cancel()

	var action string
	var postID, commentID *int64
	switch kind {
	case "post":
		if err := st.HidePost(ctx, id); err != nil {
			fmt.Fprintf(os.Stderr, "hide post %d: %v\n", id, err)
			os.Exit(1)
		}
		postID = &id
		action = "hide_post"
	case "comment":
		if err := st.HideComment(ctx, id); err != nil {
			fmt.Fprintf(os.Stderr, "hide comment %d: %v\n", id, err)
			os.Exit(1)
		}
		commentID = &id
		action = "hide_comment"
	}
	if err := st.LogModAction(ctx, uid, postID, commentID, action, reason); err != nil {
		fmt.Fprintf(os.Stderr, "log action: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("hid %s #%d  (mod=%d, reason=%q)\n", kind, id, uid, reason)
}

func runUnhide(args []string, kind string) {
	id, reason := parseModArgs(args, "unhide-"+kind)
	uid := modUser()

	st := openStore()
	defer st.Close()
	ctx, cancel := ctxTimeout(8 * time.Second)
	defer cancel()

	var action string
	var postID, commentID *int64
	switch kind {
	case "post":
		if err := st.UnhidePost(ctx, id); err != nil {
			fmt.Fprintf(os.Stderr, "unhide post %d: %v\n", id, err)
			os.Exit(1)
		}
		postID = &id
		action = "unhide_post"
	case "comment":
		if err := st.UnhideComment(ctx, id); err != nil {
			fmt.Fprintf(os.Stderr, "unhide comment %d: %v\n", id, err)
			os.Exit(1)
		}
		commentID = &id
		action = "unhide_comment"
	}
	if err := st.LogModAction(ctx, uid, postID, commentID, action, reason); err != nil {
		fmt.Fprintf(os.Stderr, "log action: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("unhid %s #%d  (mod=%d, reason=%q)\n", kind, id, uid, reason)
}

// parseModArgs accepts `<id> --reason "..."` OR `--reason "..." <id>`.
// Go's flag package stops at the first positional, so we sift the args
// manually before handing the flag-shaped ones to the parser. Empty
// reason still records the action — we don't refuse the call because
// operators sometimes need to react fast.
func parseModArgs(args []string, name string) (id int64, reason string) {
	var flagArgs []string
	var positional []string
	skip := false
	for i, a := range args {
		if skip {
			skip = false
			continue
		}
		switch {
		case a == "--":
			positional = append(positional, args[i+1:]...)
			break
		case strings.HasPrefix(a, "--"):
			// `--key=value` is one token; `--key value` is two
			if strings.Contains(a, "=") || i == len(args)-1 {
				flagArgs = append(flagArgs, a)
			} else {
				flagArgs = append(flagArgs, a, args[i+1])
				skip = true
			}
		default:
			positional = append(positional, a)
		}
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	r := fs.String("reason", "", "audit reason (recommended)")
	if err := fs.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	if len(positional) != 1 {
		fmt.Fprintf(os.Stderr, "usage: vask-mod %s <id> [--reason \"...\"]\n", name)
		os.Exit(2)
	}
	parsed, err := strconv.ParseInt(positional[0], 10, 64)
	if err != nil || parsed <= 0 {
		fmt.Fprintf(os.Stderr, "invalid id: %q\n", positional[0])
		os.Exit(2)
	}
	return parsed, strings.TrimSpace(*r)
}

// runLog prints the recent moderation_actions audit trail, newest first.
// No auth required — the audit is meant to be transparent. We render
// post/comment ids as targets so the operator can chase them up.
func runLog(args []string) {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	limit := fs.Int("limit", 50, "max rows to show")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	st := openStore()
	defer st.Close()

	ctx, cancel := ctxTimeout(8 * time.Second)
	defer cancel()
	actions, err := st.ModActions(ctx, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "log: %v\n", err)
		os.Exit(1)
	}
	if len(actions) == 0 {
		fmt.Println("(no moderation actions yet)")
		return
	}
	fmt.Printf("%-20s  %-4s  %-14s  %-8s  %s\n", "when", "mod", "action", "target", "reason")
	fmt.Println(strings.Repeat("─", 80))
	for _, a := range actions {
		target := "-"
		switch {
		case a.TargetPostID != nil:
			target = fmt.Sprintf("post #%d", *a.TargetPostID)
		case a.TargetCommentID != nil:
			target = fmt.Sprintf("cmnt #%d", *a.TargetCommentID)
		}
		reason := truncRunes(a.Reason, 40)
		if reason == "" {
			reason = "(none)"
		}
		fmt.Printf("%-20s  %-4d  %-14s  %-8s  %s\n",
			a.CreatedAt.Format("2006-01-02 15:04:05"),
			a.ModeratorID, a.Action, target, reason)
	}
}
