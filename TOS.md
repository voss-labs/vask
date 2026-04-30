# vask — terms

Shown to every user on their first SSH connection. Plain English, no boilerplate.

## What we do

- Read your SSH public key (your client sends it during the handshake).
- Compute `sha256(marshalled_pubkey)` and store **only that hash** as your account.
- Persist posts, comments, and votes you submit, keyed to your hash.

## What we don't do

- Store your raw public key.
- Log your IP address.
- Capture your terminal type, OS, or SSH client name.
- Connect your hash to any external identity.

## Rules for what you post

- **No real names.** Initials or descriptions are fine.
  ✓ "the guy with the orange hoodie at canteen 2"
  ✗ "Riya Sharma from CS-A 3rd year"
- **No phone numbers, social handles, schedules, or addresses.**
- **No targeted harassment, doxxing, or revenge posts.**
- **No personal-info-seeking** ("DM me at @...", "what's their number").

Posts that violate these rules are hidden by the operating instance's moderators. Repeat violators are fingerprint-banned — that key can no longer post.

## Moderation

- Each instance publishes its own moderator team (see the deployment's `MODERATION.md` once that doc lands, or the operator's announcement).
- Every moderator action lands in `moderation_actions` with the moderator's user ID and a reason. Audit trail is part of the schema.
- Reporters are visible to moderators only, never to post authors.

## Leaving — deleting your account

Open the activity overlay (press `Y` on the feed), then `D` to delete. You'll be asked to type `delete <your-handle>` to confirm. On success:

- Your username is cleared and your fingerprint hash is replaced with a tombstone — the same SSH key can never re-claim that account.
- Your posts and comments **stay** (they fall back to anonymous `anony-NNNN` labels), so threads other people are still in don't get gutted.
- Reconnecting with the same key creates a brand-new, unrelated account.

Want the content gone too? Use the feed's `m` filter (mine-only) and `d D` on each post/comment to delete them individually first, then delete your account.

## Audit

The repo is open source. Any privacy claim above can be verified by reading the code. The two files that carry the load:

- `internal/auth/fingerprint.go` — proves we hash, never store, the raw key.
- `internal/store/store.go` — proves only the hash and post content are persisted.

If you find a discrepancy between this document and the code, file an issue. We treat that as a critical bug.

## License

MIT.
