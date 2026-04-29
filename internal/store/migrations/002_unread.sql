-- 002_unread.sql — track per-user post visits so the feed can flag
-- "new comments since you last looked here" with a single dot.
--
-- Idempotent: re-runs are safe (IF NOT EXISTS everywhere).

CREATE TABLE IF NOT EXISTS post_views (
    user_id      INTEGER NOT NULL REFERENCES users(id),
    post_id      INTEGER NOT NULL REFERENCES posts(id),
    last_seen_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, post_id)
);

CREATE INDEX IF NOT EXISTS idx_post_views_user
    ON post_views(user_id);

INSERT OR IGNORE INTO _schema_version(version, applied_at) VALUES (2, strftime('%s','now'));
