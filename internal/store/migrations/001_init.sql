-- 001_init.sql — initial schema for vask
--
-- Identity is the SHA256 of the marshalled SSH public key.
-- The raw key is never stored; the hash is irreversible.
-- Posts live in channels; comments are nested via parent_comment_id.
-- Voting is +1 / -1 per (user, post) and (user, comment).

CREATE TABLE IF NOT EXISTS users (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    fingerprint_hash    TEXT NOT NULL UNIQUE,
    created_at          INTEGER NOT NULL,
    tos_accepted_at     INTEGER,
    banned              INTEGER NOT NULL DEFAULT 0,
    ban_reason          TEXT
);

CREATE TABLE IF NOT EXISTS channels (
    slug         TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL,
    sort_order   INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS posts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    channel      TEXT    NOT NULL REFERENCES channels(slug),
    title        TEXT    NOT NULL,
    body         TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    hidden       INTEGER NOT NULL DEFAULT 0,
    reports      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_posts_channel_created
    ON posts(channel, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_posts_created
    ON posts(created_at DESC);

CREATE INDEX IF NOT EXISTS idx_posts_user
    ON posts(user_id);

CREATE TABLE IF NOT EXISTS comments (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id           INTEGER NOT NULL REFERENCES posts(id),
    parent_comment_id INTEGER REFERENCES comments(id),
    user_id           INTEGER NOT NULL REFERENCES users(id),
    body              TEXT    NOT NULL,
    created_at        INTEGER NOT NULL,
    hidden            INTEGER NOT NULL DEFAULT 0,
    reports           INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_comments_post
    ON comments(post_id, created_at);

CREATE INDEX IF NOT EXISTS idx_comments_parent
    ON comments(parent_comment_id);

CREATE TABLE IF NOT EXISTS post_votes (
    user_id     INTEGER NOT NULL REFERENCES users(id),
    post_id     INTEGER NOT NULL REFERENCES posts(id),
    value       INTEGER NOT NULL CHECK (value IN (-1, 1)),
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (user_id, post_id)
);

CREATE INDEX IF NOT EXISTS idx_post_votes_post
    ON post_votes(post_id);

CREATE TABLE IF NOT EXISTS comment_votes (
    user_id     INTEGER NOT NULL REFERENCES users(id),
    comment_id  INTEGER NOT NULL REFERENCES comments(id),
    value       INTEGER NOT NULL CHECK (value IN (-1, 1)),
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (user_id, comment_id)
);

CREATE TABLE IF NOT EXISTS moderation_actions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    moderator_id        INTEGER NOT NULL REFERENCES users(id),
    target_post_id      INTEGER REFERENCES posts(id),
    target_comment_id   INTEGER REFERENCES comments(id),
    action              TEXT NOT NULL,
    reason              TEXT,
    created_at          INTEGER NOT NULL
);

-- Seed the three v0.1 channels. INSERT OR IGNORE so re-runs are no-ops.
INSERT OR IGNORE INTO channels(slug, name, description, sort_order, created_at) VALUES
    ('complaints', 'complaints', 'hostel · mess · infra · what is broken on campus',     1, strftime('%s','now')),
    ('electives',  'electives',  'course advice · branch picks · prof tips',              2, strftime('%s','now')),
    ('general',    'general',    'lost-and-found · study groups · everything else',       3, strftime('%s','now'));

CREATE TABLE IF NOT EXISTS _schema_version (
    version     INTEGER PRIMARY KEY,
    applied_at  INTEGER NOT NULL
);

INSERT OR IGNORE INTO _schema_version(version, applied_at) VALUES (1, strftime('%s','now'));
