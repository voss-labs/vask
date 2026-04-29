// Command ask-mod is the moderation CLI for VOSS Ask.
//
// Stubbed in v0.1 — full implementation lands in v0.2 alongside threaded
// comments and per-channel moderation. The schema and store methods are
// already in place (see internal/store/store.go: ListPostsAdmin, HidePost,
// HideComment, BanUser, BannedUsers, LogModAction, ModActions); only the
// CLI wiring is pending.
//
// In the meantime, moderation can be performed directly via Turso shell:
//
//	turso db shell voss-ask
//	UPDATE posts SET hidden = 1 WHERE id = 5;
//	UPDATE users SET banned = 1, ban_reason = '…' WHERE id = 17;
//	SELECT * FROM moderation_actions ORDER BY id DESC LIMIT 50;
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "ask-mod is not implemented in v0.1.")
	fmt.Fprintln(os.Stderr, "lands in v0.2 alongside threaded comments.")
	fmt.Fprintln(os.Stderr, "for now, moderate via: turso db shell voss-ask")
	os.Exit(0)
}
