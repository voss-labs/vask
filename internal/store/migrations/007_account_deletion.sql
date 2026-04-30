-- 007_account_deletion.sql — soft-delete for user accounts.
-- Posts and comments survive (their author's username falls back to
-- "anony-NNNN"); the user row sticks around with a tombstoned fingerprint
-- so the same SSH key can never re-claim that identity.

ALTER TABLE users ADD COLUMN deleted_at INTEGER;

INSERT OR IGNORE INTO _schema_version(version, applied_at) VALUES (7, strftime('%s','now'));
