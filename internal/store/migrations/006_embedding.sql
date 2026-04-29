-- 006_embedding.sql — give every post a 1024-dim semantic embedding for
-- vector similarity search.
--
-- We use libsql's native vector type (`F32_BLOB(N)`) so the same column
-- works as a queryable vector under Turso AND as a plain BLOB under
-- modernc.org/sqlite for local dev. Vector functions (vector_distance_cos,
-- libsql_vector_idx) only resolve under libsql at query time, so dev
-- can write embeddings but can't run semantic queries — that's fine,
-- semantic search is a Turso-only path for now.
--
-- 1024 dims matches @cf/baai/bge-m3 — the multilingual model we picked
-- for Cloudflare Workers AI. Storage cost: ~4 KB per post.

ALTER TABLE posts ADD COLUMN embedding F32_BLOB(1024);

INSERT OR IGNORE INTO _schema_version(version, applied_at) VALUES (6, strftime('%s','now'));
