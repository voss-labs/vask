-- 003_tags.sql — replace fixed channels with user-supplied tags.
--
-- Channels stay in the schema as a legacy column (posts.channel NOT NULL)
-- and silently default to 'general' for new posts; the UI stops surfacing
-- them. Tags are the new categorisation surface — many-to-many, user-named.

CREATE TABLE IF NOT EXISTS post_tags (
    post_id     INTEGER NOT NULL REFERENCES posts(id),
    tag         TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (post_id, tag)
);

CREATE INDEX IF NOT EXISTS idx_post_tags_tag        ON post_tags(tag);
CREATE INDEX IF NOT EXISTS idx_post_tags_post_id    ON post_tags(post_id);

INSERT OR IGNORE INTO _schema_version(version, applied_at) VALUES (3, strftime('%s','now'));
