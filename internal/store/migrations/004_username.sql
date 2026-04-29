-- 004_username.sql — give every user a memorable display name like
-- "polite-okapi" instead of the schematic "anony-0042" surrogate.
--
-- Idempotent (IF NOT EXISTS / NULL-tolerant unique). Existing rows get
-- username = NULL and are prompted to pick on next connect.

ALTER TABLE users ADD COLUMN username TEXT;

-- SQLite treats NULLs as distinct for UNIQUE indexes, so multiple legacy
-- rows with NULL username don't collide; only set names are constrained
-- to be unique.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username
    ON users(username);

INSERT OR IGNORE INTO _schema_version(version, applied_at) VALUES (4, strftime('%s','now'));
