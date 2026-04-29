-- 005_last_feed_at.sql — track each user's last feed-load timestamp so we
-- can render a "── new since last visit ──" divider between unread posts
-- and ones the user has already scrolled past.
--
-- Stored as unix seconds (NULL for never-visited). Single column, single
-- non-idempotent ALTER — gated by _schema_version in the Go migrate loop.

ALTER TABLE users ADD COLUMN last_feed_at INTEGER;

INSERT OR IGNORE INTO _schema_version(version, applied_at) VALUES (5, strftime('%s','now'));
