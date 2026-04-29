# voss-ask — terms

Shown to every user on their first SSH connection. Plain English, no boilerplate.

## What we do

- Read your SSH public key (your client sends it during the handshake).
- Compute `sha256(marshalled_pubkey)` and store **only that hash** as your account.
- Persist letters and votes you submit, keyed to your hash.

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

Posts that violate these rules are hidden by moderators within 24 hours.
Repeat violators are fingerprint-banned — that key can no longer post.

## Moderation

- 2–3 student moderators on a 1-week rotation review the report queue.
- Moderator fingerprints are stored in a public allowlist file in this repo.
- Every moderator action lands in `moderation_actions` with the
  moderator's user ID and a reason. Audit-trail is part of the schema.

## Data lifecycle

- Letters auto-archive (move out of the active feed) after **90 days**.
- Hard delete after **1 year**, except for legal hold cases (rare).

## Audit

This repo is open source. Any privacy claim above can be verified by
reading the code. The two files that carry the load:

- `internal/auth/fingerprint.go` — proves we hash, never store, the raw key.
- `internal/store/store.go` — proves only the hash and post content are persisted.

If you find a discrepancy between this document and the code, file an issue.
We treat that as a critical bug.

## License

This project is licensed under the MIT License.
