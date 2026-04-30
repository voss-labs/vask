// Package store wraps the libsql / SQLite database with typed methods.
// All identity columns hold sha256(pubkey); never the raw key.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"

	"github.com/voss-labs/vask/internal/embed"
)

//go:embed migrations/001_init.sql
var schema001 string

//go:embed migrations/002_unread.sql
var schema002 string

//go:embed migrations/003_tags.sql
var schema003 string

//go:embed migrations/004_username.sql
var schema004 string

//go:embed migrations/005_last_feed_at.sql
var schema005 string

//go:embed migrations/006_embedding.sql
var schema006 string

//go:embed migrations/007_account_deletion.sql
var schema007 string

type Store struct {
	db    *sql.DB
	embed *embed.Client
}

func (s *Store) UseEmbedClient(c *embed.Client) { s.embed = c }
func (s *Store) EmbedClient() *embed.Client     { return s.embed }
func (s *Store) Close() error                   { return s.db.Close() }

func Open(url, authToken string) (*Store, error) {
	driver, dsn := resolveDriver(url, authToken)
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping %s: %w", driver, err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func resolveDriver(url, authToken string) (driver, dsn string) {
	switch {
	case strings.HasPrefix(url, "libsql://"),
		strings.HasPrefix(url, "https://"),
		strings.HasPrefix(url, "wss://"):
		dsn = url
		if authToken != "" {
			sep := "?"
			if strings.Contains(url, "?") {
				sep = "&"
			}
			dsn = url + sep + "authToken=" + authToken
		}
		return "libsql", dsn
	default:
		return "sqlite", fmt.Sprintf(
			"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
			url,
		)
	}
}

// migration 001 always runs (idempotent CREATE TABLE IF NOT EXISTS); the
// rest are version-gated because they include non-idempotent ALTER TABLE.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema001); err != nil {
		return fmt.Errorf("migration 001: %w", err)
	}
	applied, err := s.appliedVersions()
	if err != nil {
		return err
	}
	rest := []struct {
		version int
		sql     string
	}{
		{2, schema002}, {3, schema003}, {4, schema004},
		{5, schema005}, {6, schema006}, {7, schema007},
	}
	for _, m := range rest {
		if applied[m.version] {
			continue
		}
		if _, err := s.db.Exec(m.sql); err != nil {
			return fmt.Errorf("migration %03d: %w", m.version, err)
		}
	}
	return nil
}

func (s *Store) appliedVersions() (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT version FROM _schema_version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// users ===============================================================

type User struct {
	ID              int64
	FingerprintHash string
	CreatedAt       time.Time
	TOSAcceptedAt   *time.Time
	Banned          bool
	BanReason       string
	Username        string
}

func (s *Store) UpsertUser(ctx context.Context, fingerprint string) (*User, error) {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO users(fingerprint_hash, created_at) VALUES(?, ?)`,
		fingerprint, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return s.UserByFingerprint(ctx, fingerprint)
}

func (s *Store) UserByFingerprint(ctx context.Context, fingerprint string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, fingerprint_hash, created_at, tos_accepted_at, banned, COALESCE(ban_reason,''),
		        COALESCE(username,'')
		 FROM users WHERE fingerprint_hash = ?`,
		fingerprint,
	)
	var u User
	var createdAt int64
	var tos sql.NullInt64
	var banned int
	if err := row.Scan(&u.ID, &u.FingerprintHash, &createdAt, &tos, &banned, &u.BanReason, &u.Username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	u.CreatedAt = time.Unix(createdAt, 0)
	if tos.Valid {
		t := time.Unix(tos.Int64, 0)
		u.TOSAcceptedAt = &t
	}
	u.Banned = banned == 1
	return &u, nil
}

// ClaimUsername is race-safe: the WHERE clause embeds both "no username yet"
// and "no other row has this name", so two concurrent claimers can't both win.
func (s *Store) ClaimUsername(ctx context.Context, userID int64, candidate string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET username = ?
		 WHERE id = ? AND username IS NULL
		   AND NOT EXISTS (SELECT 1 FROM users WHERE username = ?)`,
		candidate, userID, candidate,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *Store) AcceptTOS(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET tos_accepted_at = ? WHERE id = ? AND tos_accepted_at IS NULL`,
		time.Now().Unix(), userID,
	)
	return err
}

// DeleteOwnAccount soft-deletes the user: tombstones their fingerprint
// (so the same SSH key can never re-claim this user_id) and nulls out
// their handle. Their posts and comments stay — they fall back to
// "anony-NNNN" via displayName because the JOIN on users now sees a
// NULL username. Idempotent: re-running on an already-deleted row is
// a no-op (the WHERE deleted_at IS NULL guard).
func (s *Store) DeleteOwnAccount(ctx context.Context, userID int64) error {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("delete account: rand: %w", err)
	}
	tomb := "deleted-" + hex.EncodeToString(b[:])
	_, err := s.db.ExecContext(ctx, `
		UPDATE users
		SET deleted_at = ?,
		    username = NULL,
		    fingerprint_hash = ?
		WHERE id = ? AND deleted_at IS NULL`,
		time.Now().Unix(), tomb, userID)
	return err
}

// GetAndBumpLastFeedAt returns the previous timestamp and atomically sets it
// to "now". The previous value is the cutoff for the unread divider.
func (s *Store) GetAndBumpLastFeedAt(ctx context.Context, userID int64) (time.Time, error) {
	var prev sql.NullInt64
	row := s.db.QueryRowContext(ctx, `SELECT last_feed_at FROM users WHERE id = ?`, userID)
	if err := row.Scan(&prev); err != nil {
		return time.Time{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_feed_at = ? WHERE id = ?`,
		time.Now().Unix(), userID); err != nil {
		return time.Time{}, err
	}
	if !prev.Valid {
		return time.Time{}, nil
	}
	return time.Unix(prev.Int64, 0), nil
}

// channels (legacy) ===================================================

type Channel struct {
	Slug        string
	Name        string
	Description string
	SortOrder   int
	PostCount   int
}

func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.slug, c.name, c.description, c.sort_order,
		        (SELECT COUNT(*) FROM posts p WHERE p.channel = c.slug AND p.hidden = 0)
		 FROM channels c
		 ORDER BY c.sort_order ASC, c.slug ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.Slug, &c.Name, &c.Description, &c.SortOrder, &c.PostCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetChannel(ctx context.Context, slug string) (*Channel, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT slug, name, description, sort_order,
		        (SELECT COUNT(*) FROM posts WHERE channel = ? AND hidden = 0)
		 FROM channels WHERE slug = ?`, slug, slug)
	var c Channel
	if err := row.Scan(&c.Slug, &c.Name, &c.Description, &c.SortOrder, &c.PostCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// posts ===============================================================

type Post struct {
	ID           int64
	UserID       int64
	Username     string
	Channel      string
	Title        string
	Body         string
	CreatedAt    time.Time
	Score        int
	MyVote       int
	RecentScore  int
	CommentCount int
	HasUnread    bool
	Tags         []string
	Hidden       bool
	Reports      int
}

type SortMode int

const (
	SortHot SortMode = iota
	SortNew
	SortTop
)

type ListPostsParams struct {
	Tag      string
	Query    string
	MineOnly bool
	Sort     SortMode
	Limit    int
	Offset   int
}

func (s *Store) ListPosts(ctx context.Context, myUserID int64, params ListPostsParams) ([]Post, error) {
	limit := params.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := params.Offset
	if offset < 0 {
		offset = 0
	}

	args := []any{myUserID, myUserID}
	q := postSelectColumns + `
		FROM posts p
		LEFT JOIN users u ON u.id = p.user_id
		WHERE p.hidden = 0`

	if params.Tag != "" {
		q += ` AND EXISTS (SELECT 1 FROM post_tags WHERE post_id = p.id AND tag = ?)`
		args = append(args, params.Tag)
	}
	if params.Query != "" {
		needle := "%" + strings.ToLower(params.Query) + "%"
		q += ` AND (lower(p.title) LIKE ? OR lower(p.body) LIKE ?)`
		args = append(args, needle, needle)
	}
	if params.MineOnly {
		q += ` AND p.user_id = ?`
		args = append(args, myUserID)
	}

	q += ` ORDER BY ` + postOrderBy(params.Sort) + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	return scanPosts(s.db.QueryContext(ctx, q, args...))
}

// SearchPostsSemantic embeds the query and ranks by cosine distance.
// Falls back to LIKE silently on any failure (no embed client, embed call,
// SQL — local SQLite doesn't have vector_distance_cos).
func (s *Store) SearchPostsSemantic(ctx context.Context, myUserID int64, query string, params ListPostsParams) ([]Post, error) {
	if s.embed != nil {
		v, err := s.embed.Embed(ctx, query)
		if err == nil {
			semanticParams := params
			semanticParams.Query = ""
			posts, qerr := s.listPostsByVector(ctx, myUserID, v, semanticParams)
			if qerr == nil {
				return posts, nil
			}
			slog.Warn("semantic search failed; LIKE fallback", "err", qerr)
		} else {
			slog.Warn("embed query failed; LIKE fallback", "err", err)
		}
	}
	likeParams := params
	if likeParams.Query == "" {
		likeParams.Query = query
	}
	return s.ListPosts(ctx, myUserID, likeParams)
}

func (s *Store) listPostsByVector(ctx context.Context, myUserID int64, v []float32, params ListPostsParams) ([]Post, error) {
	limit := params.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := params.Offset
	if offset < 0 {
		offset = 0
	}

	args := []any{embed.Format(v), myUserID, myUserID}
	q := postSelectColumns + `
		FROM posts p
		LEFT JOIN users u ON u.id = p.user_id
		WHERE p.hidden = 0 AND p.embedding IS NOT NULL`

	if params.Tag != "" {
		q += ` AND EXISTS (SELECT 1 FROM post_tags WHERE post_id = p.id AND tag = ?)`
		args = append(args, params.Tag)
	}
	if params.MineOnly {
		q += ` AND p.user_id = ?`
		args = append(args, myUserID)
	}

	q += ` ORDER BY vector_distance_cos(p.embedding, vector(?1)) LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	return scanPosts(s.db.QueryContext(ctx, q, args...))
}

func (s *Store) NearestPostsToPost(ctx context.Context, postID int64, limit int) ([]Post, error) {
	if limit <= 0 || limit > 20 {
		limit = 3
	}
	var srcBlob []byte
	row := s.db.QueryRowContext(ctx, `SELECT embedding FROM posts WHERE id = ?`, postID)
	if err := row.Scan(&srcBlob); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(srcBlob) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.user_id, COALESCE(u.username, ''), p.title, p.created_at,
		       COALESCE((SELECT SUM(value) FROM post_votes WHERE post_id = p.id), 0),
		       COALESCE((SELECT GROUP_CONCAT(tag, ',') FROM post_tags WHERE post_id = p.id), '')
		FROM posts p
		LEFT JOIN users u ON u.id = p.user_id
		WHERE p.id != ? AND p.hidden = 0 AND p.embedding IS NOT NULL
		ORDER BY vector_distance_cos(p.embedding, ?)
		LIMIT ?`, postID, srcBlob, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Post
	for rows.Next() {
		var p Post
		var createdAt int64
		var tagsCSV string
		if err := rows.Scan(&p.ID, &p.UserID, &p.Username, &p.Title, &createdAt, &p.Score, &tagsCSV); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(createdAt, 0)
		p.Tags = splitCSV(tagsCSV)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) NearestPostsByVector(ctx context.Context, v []float32, limit int) ([]Post, error) {
	if limit <= 0 || limit > 20 {
		limit = 3
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.title,
		       COALESCE((SELECT GROUP_CONCAT(tag, ',') FROM post_tags WHERE post_id = p.id), '')
		FROM posts p
		WHERE p.hidden = 0 AND p.embedding IS NOT NULL
		ORDER BY vector_distance_cos(p.embedding, vector(?))
		LIMIT ?`, embed.Format(v), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Post
	for rows.Next() {
		var p Post
		var tagsCSV string
		if err := rows.Scan(&p.ID, &p.Title, &tagsCSV); err != nil {
			return nil, err
		}
		p.Tags = splitCSV(tagsCSV)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) HasEmbeddings(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM posts WHERE embedding IS NOT NULL`).Scan(&n)
	return n > 0, err
}

func (s *Store) CountPosts(ctx context.Context, myUserID int64, params ListPostsParams) (int, error) {
	q := `SELECT COUNT(*) FROM posts p WHERE p.hidden = 0`
	var args []any
	if params.Tag != "" {
		q += ` AND EXISTS (SELECT 1 FROM post_tags WHERE post_id = p.id AND tag = ?)`
		args = append(args, params.Tag)
	}
	if params.Query != "" {
		needle := "%" + strings.ToLower(params.Query) + "%"
		q += ` AND (lower(p.title) LIKE ? OR lower(p.body) LIKE ?)`
		args = append(args, needle, needle)
	}
	if params.MineOnly {
		q += ` AND p.user_id = ?`
		args = append(args, myUserID)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) MarkPostSeen(ctx context.Context, userID, postID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO post_views(user_id, post_id, last_seen_at) VALUES (?, ?, ?)
		 ON CONFLICT(user_id, post_id) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		userID, postID, time.Now().Unix())
	return err
}

func postOrderBy(sort SortMode) string {
	switch sort {
	case SortNew:
		return "p.created_at DESC, p.id DESC"
	case SortTop:
		return "score DESC, p.created_at DESC"
	default:
		// HN ranking: score / (hours_old + 2)^1.8
		return `(
			COALESCE((SELECT SUM(value) FROM post_votes WHERE post_id = p.id), 0)
			/ POWER(((strftime('%s','now') - p.created_at) / 3600.0) + 2.0, 1.8)
		) DESC, p.created_at DESC`
	}
}

func (s *Store) GetPost(ctx context.Context, postID, myUserID int64) (*Post, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT p.id, p.user_id, COALESCE(u.username, ''),
		       p.channel, p.title, p.body, p.created_at, p.hidden, p.reports,
		       COALESCE((SELECT SUM(value) FROM post_votes WHERE post_id = p.id), 0),
		       COALESCE((SELECT value FROM post_votes WHERE post_id = p.id AND user_id = ?), 0),
		       (SELECT COUNT(*) FROM comments WHERE post_id = p.id AND hidden = 0),
		       COALESCE((SELECT GROUP_CONCAT(tag, ',') FROM post_tags WHERE post_id = p.id), '')
		FROM posts p
		LEFT JOIN users u ON u.id = p.user_id
		WHERE p.id = ?`, myUserID, postID)

	var p Post
	var createdAt int64
	var hidden int
	var tagsCSV string
	if err := row.Scan(&p.ID, &p.UserID, &p.Username, &p.Channel, &p.Title, &p.Body, &createdAt, &hidden, &p.Reports,
		&p.Score, &p.MyVote, &p.CommentCount, &tagsCSV); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p.CreatedAt = time.Unix(createdAt, 0)
	p.Hidden = hidden == 1
	p.Tags = splitCSV(tagsCSV)
	if p.Hidden {
		return nil, nil
	}
	return &p, nil
}

func (s *Store) CreatePost(ctx context.Context, userID int64, title, body string, tags []string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO posts(user_id, channel, title, body, created_at)
		 VALUES (?, 'general', ?, ?, ?)`,
		userID, title, body, now,
	)
	if err != nil {
		return 0, err
	}
	postID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, t := range tags {
		t = NormalizeTag(t)
		if t == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO post_tags(post_id, tag, created_at) VALUES (?, ?, ?)`,
			postID, t, now,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// fire-and-forget: post is durable; embedding is best-effort
	if s.embed != nil {
		go s.embedAndSavePost(postID, title, body)
	}
	return postID, nil
}

func (s *Store) embedAndSavePost(postID int64, title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	v, err := s.embed.Embed(ctx, title+"\n\n"+body)
	if err != nil {
		slog.Warn("embed post", "post_id", postID, "err", err)
		return
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE posts SET embedding = ? WHERE id = ?`,
		embed.Pack(v), postID); err != nil {
		slog.Warn("embed update", "post_id", postID, "err", err)
	}
}

// EmbedAndSavePost is the exported variant for cmd/vask-embed-backfill.
func (s *Store) EmbedAndSavePost(postID int64, title, body string) {
	s.embedAndSavePost(postID, title, body)
}

func (s *Store) PostsMissingEmbedding(ctx context.Context, limit int) ([]Post, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, body FROM posts WHERE embedding IS NULL AND hidden = 0 ORDER BY id LIMIT ?`,
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Post
	for rows.Next() {
		var p Post
		if err := rows.Scan(&p.ID, &p.Title, &p.Body); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListPopularTags(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 50 {
		limit = 12
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT tag FROM post_tags GROUP BY tag ORDER BY COUNT(*) DESC, tag ASC LIMIT ?`,
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// NormalizeTag: lowercase, trim, spaces→hyphens, max 24 chars, [a-z0-9-_].
func NormalizeTag(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = strings.ReplaceAll(t, " ", "-")
	var b strings.Builder
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '-' || r == '_':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Store) CountUserPostsSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM posts WHERE user_id = ? AND created_at >= ?`,
		userID, since.Unix())
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ErrSelfVote: enforced at the store layer so score reflects others' opinion,
// not author's. Important in small communities where one self-vote is a
// meaningful percentage of total signal.
var ErrSelfVote = errors.New("can't vote on your own contribution")

func (s *Store) SetPostVote(ctx context.Context, userID, postID int64, value int) (int, error) {
	if value != -1 && value != 0 && value != 1 {
		return 0, fmt.Errorf("invalid vote value: %d", value)
	}
	authorID, err := s.AuthorOfPost(ctx, postID)
	if err != nil {
		return 0, err
	}
	if authorID == userID {
		return 0, ErrSelfVote
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	if value == 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM post_votes WHERE user_id = ? AND post_id = ?`,
			userID, postID); err != nil {
			return 0, err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO post_votes(user_id, post_id, value, created_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(user_id, post_id) DO UPDATE SET value = excluded.value, created_at = excluded.created_at`,
			userID, postID, value, time.Now().Unix()); err != nil {
			return 0, err
		}
	}

	row := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(value), 0) FROM post_votes WHERE post_id = ?`, postID)
	var score int
	if err := row.Scan(&score); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return score, nil
}

func (s *Store) DeleteOwnPost(ctx context.Context, userID, postID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	row := tx.QueryRowContext(ctx, `SELECT 1 FROM posts WHERE id = ? AND user_id = ?`, postID, userID)
	var x int
	if err := row.Scan(&x); err != nil {
		return err
	}
	// Cascade order: every table with a FK to posts(id) must be cleared
	// (or NULLed) before the post row itself goes. Missed any of these
	// and the final DELETE FROM posts trips a FOREIGN KEY constraint.
	stmts := []string{
		`DELETE FROM comment_votes WHERE comment_id IN (SELECT id FROM comments WHERE post_id = ?)`,
		`DELETE FROM comments      WHERE post_id = ?`,
		`DELETE FROM post_votes    WHERE post_id = ?`,
		`DELETE FROM post_tags     WHERE post_id = ?`,
		`DELETE FROM post_views    WHERE post_id = ?`,
		// Preserve moderation audit rows but disconnect them from the
		// vanishing post so the FK doesn't fire. target_post_id is
		// nullable in 001_init.sql precisely for this case.
		`UPDATE moderation_actions SET target_post_id = NULL WHERE target_post_id = ?`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, postID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM posts WHERE id = ? AND user_id = ?`, postID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// comments ============================================================

type Comment struct {
	ID              int64
	PostID          int64
	ParentCommentID *int64
	UserID          int64
	Username        string
	Body            string
	CreatedAt       time.Time
	Score           int
	MyVote          int
	Hidden          bool
}

func (s *Store) ListComments(ctx context.Context, postID, myUserID int64) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.post_id, c.parent_comment_id, c.user_id,
		       COALESCE(u.username, ''),
		       c.body, c.created_at, c.hidden,
		       COALESCE((SELECT SUM(value) FROM comment_votes WHERE comment_id = c.id), 0),
		       COALESCE((SELECT value      FROM comment_votes WHERE comment_id = c.id AND user_id = ?), 0)
		FROM comments c
		LEFT JOIN users u ON u.id = c.user_id
		WHERE c.post_id = ?
		ORDER BY c.created_at ASC, c.id ASC`,
		myUserID, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Comment
	for rows.Next() {
		var c Comment
		var parent sql.NullInt64
		var createdAt int64
		var hidden int
		if err := rows.Scan(&c.ID, &c.PostID, &parent, &c.UserID, &c.Username, &c.Body, &createdAt, &hidden,
			&c.Score, &c.MyVote); err != nil {
			return nil, err
		}
		if parent.Valid {
			v := parent.Int64
			c.ParentCommentID = &v
		}
		c.CreatedAt = time.Unix(createdAt, 0)
		c.Hidden = hidden == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CreateComment(ctx context.Context, userID, postID int64, parentCommentID *int64, body string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO comments(post_id, parent_comment_id, user_id, body, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		postID, nullID(parentCommentID), userID, body, time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SetCommentVote(ctx context.Context, userID, commentID int64, value int) (int, error) {
	if value != -1 && value != 0 && value != 1 {
		return 0, fmt.Errorf("invalid vote value: %d", value)
	}
	authorID, err := s.AuthorOfComment(ctx, commentID)
	if err != nil {
		return 0, err
	}
	if authorID == userID {
		return 0, ErrSelfVote
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	if value == 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM comment_votes WHERE user_id = ? AND comment_id = ?`,
			userID, commentID); err != nil {
			return 0, err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO comment_votes(user_id, comment_id, value, created_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(user_id, comment_id) DO UPDATE SET value = excluded.value, created_at = excluded.created_at`,
			userID, commentID, value, time.Now().Unix()); err != nil {
			return 0, err
		}
	}

	row := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(value), 0) FROM comment_votes WHERE comment_id = ?`, commentID)
	var score int
	if err := row.Scan(&score); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return score, nil
}

// DeleteOwnComment orphans children up to the parent (sets their parent to
// NULL) so thread context survives.
func (s *Store) DeleteOwnComment(ctx context.Context, userID, commentID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	row := tx.QueryRowContext(ctx, `SELECT 1 FROM comments WHERE id = ? AND user_id = ?`, commentID, userID)
	var x int
	if err := row.Scan(&x); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE comments SET parent_comment_id = NULL WHERE parent_comment_id = ?`, commentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM comment_votes WHERE comment_id = ?`, commentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM comments WHERE id = ? AND user_id = ?`, commentID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

type ActivityComment struct {
	Comment   Comment
	PostTitle string
}

func (s *Store) MyRecentComments(ctx context.Context, userID int64, limit int) ([]ActivityComment, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.post_id, c.user_id, c.body, c.created_at,
		       COALESCE(p.title, '')
		FROM comments c
		JOIN posts p ON p.id = c.post_id
		WHERE c.user_id = ? AND c.hidden = 0 AND p.hidden = 0
		ORDER BY c.created_at DESC
		LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ActivityComment
	for rows.Next() {
		var ac ActivityComment
		var createdAt int64
		if err := rows.Scan(&ac.Comment.ID, &ac.Comment.PostID, &ac.Comment.UserID,
			&ac.Comment.Body, &createdAt, &ac.PostTitle); err != nil {
			return nil, err
		}
		ac.Comment.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, ac)
	}
	return out, rows.Err()
}

func (s *Store) CountUserCommentsSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM comments WHERE user_id = ? AND created_at >= ?`,
		userID, since.Unix())
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// moderation ==========================================================

func (s *Store) ListPostsAdmin(ctx context.Context, channel string, includeHidden bool, limit int) ([]Post, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `
		SELECT p.id, p.user_id, p.channel, p.title, p.body, p.created_at, p.hidden, p.reports,
		       COALESCE((SELECT SUM(value) FROM post_votes WHERE post_id = p.id), 0),
		       0,
		       (SELECT COUNT(*) FROM comments WHERE post_id = p.id)
		FROM posts p`
	var args []any
	conds := []string{}
	if !includeHidden {
		conds = append(conds, "p.hidden = 0")
	}
	if channel != "" {
		conds = append(conds, "p.channel = ?")
		args = append(args, channel)
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY p.id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Post
	for rows.Next() {
		var p Post
		var createdAt int64
		var hidden int
		if err := rows.Scan(&p.ID, &p.UserID, &p.Channel, &p.Title, &p.Body, &createdAt, &hidden, &p.Reports,
			&p.Score, &p.MyVote, &p.CommentCount); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(createdAt, 0)
		p.Hidden = hidden == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) HidePost(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE posts SET hidden = 1 WHERE id = ?`, id)
	return err
}

func (s *Store) UnhidePost(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE posts SET hidden = 0 WHERE id = ?`, id)
	return err
}

func (s *Store) HideComment(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE comments SET hidden = 1 WHERE id = ?`, id)
	return err
}

func (s *Store) UnhideComment(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE comments SET hidden = 0 WHERE id = ?`, id)
	return err
}

func (s *Store) AuthorOfPost(ctx context.Context, postID int64) (int64, error) {
	row := s.db.QueryRowContext(ctx, `SELECT user_id FROM posts WHERE id = ?`, postID)
	var uid int64
	if err := row.Scan(&uid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return uid, nil
}

func (s *Store) AuthorOfComment(ctx context.Context, commentID int64) (int64, error) {
	row := s.db.QueryRowContext(ctx, `SELECT user_id FROM comments WHERE id = ?`, commentID)
	var uid int64
	if err := row.Scan(&uid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return uid, nil
}

func (s *Store) BanUser(ctx context.Context, userID int64, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET banned = 1, ban_reason = ? WHERE id = ?`, reason, userID)
	return err
}

func (s *Store) UnbanUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET banned = 0, ban_reason = NULL WHERE id = ?`, userID)
	return err
}

func (s *Store) BannedUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, fingerprint_hash, created_at, banned, COALESCE(ban_reason,'')
		 FROM users WHERE banned = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var createdAt int64
		var banned int
		if err := rows.Scan(&u.ID, &u.FingerprintHash, &createdAt, &banned, &u.BanReason); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(createdAt, 0)
		u.Banned = banned == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

type ModAction struct {
	ID              int64
	ModeratorID     int64
	TargetPostID    *int64
	TargetCommentID *int64
	Action          string
	Reason          string
	CreatedAt       time.Time
}

func (s *Store) LogModAction(ctx context.Context, modID int64, postID, commentID *int64, action, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO moderation_actions(moderator_id, target_post_id, target_comment_id, action, reason, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		modID, nullID(postID), nullID(commentID), action, nullify(reason), time.Now().Unix(),
	)
	return err
}

func (s *Store) ModActions(ctx context.Context, limit int) ([]ModAction, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, moderator_id, target_post_id, target_comment_id, action, COALESCE(reason,''), created_at
		 FROM moderation_actions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModAction
	for rows.Next() {
		var a ModAction
		var post, comment sql.NullInt64
		var createdAt int64
		if err := rows.Scan(&a.ID, &a.ModeratorID, &post, &comment, &a.Action, &a.Reason, &createdAt); err != nil {
			return nil, err
		}
		if post.Valid {
			v := post.Int64
			a.TargetPostID = &v
		}
		if comment.Valid {
			v := comment.Int64
			a.TargetCommentID = &v
		}
		a.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// helpers =============================================================

const postSelectColumns = `
	SELECT p.id, p.user_id, COALESCE(u.username, '') AS username,
	       p.channel, p.title, p.body, p.created_at,
	       COALESCE((SELECT SUM(value) FROM post_votes WHERE post_id = p.id), 0)        AS score,
	       COALESCE((SELECT value      FROM post_votes WHERE post_id = p.id AND user_id = ?), 0) AS my_vote,
	       COALESCE((SELECT SUM(value) FROM post_votes
	                  WHERE post_id = p.id
	                    AND created_at >= strftime('%s','now') - 3600), 0)              AS recent_score,
	       (SELECT COUNT(*) FROM comments WHERE post_id = p.id AND hidden = 0)           AS comments,
	       CASE
	         WHEN (SELECT MAX(created_at) FROM comments WHERE post_id = p.id AND hidden = 0)
	              > COALESCE((SELECT last_seen_at FROM post_views WHERE post_id = p.id AND user_id = ?), 0)
	         THEN 1 ELSE 0
	       END AS has_unread,
	       COALESCE((SELECT GROUP_CONCAT(tag, ',') FROM post_tags WHERE post_id = p.id), '') AS tags`

func scanPosts(rows *sql.Rows, err error) ([]Post, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Post
	for rows.Next() {
		var p Post
		var createdAt int64
		var unread int
		var tagsCSV string
		if err := rows.Scan(&p.ID, &p.UserID, &p.Username, &p.Channel, &p.Title, &p.Body, &createdAt,
			&p.Score, &p.MyVote, &p.RecentScore, &p.CommentCount, &unread, &tagsCSV); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(createdAt, 0)
		p.HasUnread = unread == 1
		p.Tags = splitCSV(tagsCSV)
		out = append(out, p)
	}
	return out, rows.Err()
}

func nullify(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullID(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// === operator stats ====================================================

// Stats is the operator-console snapshot read by `vask-mod stats`. Counts
// are split between "real" and "seed" so the operator can distinguish
// genuine community activity from the deploy seed. Seed users carry the
// `seed-` fingerprint prefix written by cmd/vask-seed.
type Stats struct {
	// Cumulative
	RealUsers      int
	SeedUsers      int
	DeletedUsers   int // tombstoned (deleted_at NOT NULL)
	RealPosts      int
	SeedPosts      int
	HiddenPosts    int
	RealComments   int
	HiddenComments int
	PostVotes      int
	CommentVotes   int

	// Rolling window (rows where created_at >= Since.Unix())
	Since                  time.Time
	NewUsersWindow         int
	NewRealPostsWindow     int
	NewRealCommentsWindow  int
	UniquePostersWindow    int
	UniqueCommentersWindow int
}

// PostStats wraps Post with admin-only counters that aren't shown in
// the regular feed. ViewCount is sourced from post_views (distinct
// users who opened the post).
type PostStats struct {
	Post
	ViewCount int
}

// PostsWithStats returns posts ordered by sortBy ("views", "score",
// "comments", "new") with a window filter and optional seed filter.
// Used by `vask-mod top-posts` and `vask-mod recent-posts`.
func (s *Store) PostsWithStats(ctx context.Context, since time.Time, limit int, sortBy string, includeSeeds bool) ([]PostStats, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	order := "p.created_at DESC"
	switch sortBy {
	case "views":
		order = "view_count DESC, p.created_at DESC"
	case "score":
		order = "score DESC, p.created_at DESC"
	case "comments":
		order = "comment_count DESC, p.created_at DESC"
	case "new":
		order = "p.created_at DESC"
	}
	seedClause := "AND u.fingerprint_hash NOT LIKE 'seed-%'"
	if includeSeeds {
		seedClause = ""
	}
	q := `
SELECT p.id, p.user_id, COALESCE(u.username, ''), COALESCE(p.channel, ''),
       p.title, p.body, p.created_at, p.hidden,
       (SELECT COALESCE(SUM(value),0) FROM post_votes WHERE post_id = p.id)         AS score,
       (SELECT COUNT(*) FROM comments WHERE post_id = p.id AND hidden = 0)          AS comment_count,
       (SELECT COUNT(*) FROM post_views WHERE post_id = p.id)                       AS view_count,
       (SELECT COALESCE(GROUP_CONCAT(tag, ','), '') FROM post_tags WHERE post_id = p.id) AS tags_csv
FROM posts p
LEFT JOIN users u ON u.id = p.user_id
WHERE p.created_at >= ? ` + seedClause + `
ORDER BY ` + order + `
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PostStats
	for rows.Next() {
		var ps PostStats
		var createdAt int64
		var hidden int
		var tagsCSV string
		if err := rows.Scan(&ps.ID, &ps.UserID, &ps.Username, &ps.Channel,
			&ps.Title, &ps.Body, &createdAt, &hidden,
			&ps.Score, &ps.CommentCount, &ps.ViewCount, &tagsCSV); err != nil {
			return nil, err
		}
		ps.CreatedAt = time.Unix(createdAt, 0)
		ps.Hidden = hidden == 1
		ps.Tags = splitCSV(tagsCSV)
		out = append(out, ps)
	}
	return out, rows.Err()
}

// ActiveUser is a row in the `vask-mod users --active` listing — a real
// user who posted, commented, or voted in the window.
type ActiveUser struct {
	UserID    int64
	Username  string
	Posts     int
	Comments  int
	Votes     int
	LastSeen  time.Time
}

// ActiveUsers returns real (non-seed, non-deleted) users who did at
// least one of {post, comment, vote} in the window, with per-user
// activity counters.
func (s *Store) ActiveUsers(ctx context.Context, since time.Time, limit int) ([]ActiveUser, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
SELECT u.id, COALESCE(u.username, ''),
       (SELECT COUNT(*) FROM posts    WHERE user_id = u.id AND created_at >= ?) AS posts,
       (SELECT COUNT(*) FROM comments WHERE user_id = u.id AND created_at >= ?) AS comments,
       (SELECT COUNT(*) FROM post_votes    WHERE user_id = u.id AND created_at >= ?)
       + (SELECT COUNT(*) FROM comment_votes WHERE user_id = u.id AND created_at >= ?) AS votes,
       MAX(COALESCE((SELECT MAX(created_at) FROM posts    WHERE user_id = u.id), 0),
           COALESCE((SELECT MAX(created_at) FROM comments WHERE user_id = u.id), 0),
           COALESCE((SELECT MAX(created_at) FROM post_votes    WHERE user_id = u.id), 0),
           COALESCE((SELECT MAX(created_at) FROM comment_votes WHERE user_id = u.id), 0)) AS last_seen
FROM users u
WHERE u.deleted_at IS NULL
  AND u.fingerprint_hash NOT LIKE 'seed-%'
  AND ((SELECT COUNT(*) FROM posts    WHERE user_id = u.id AND created_at >= ?) > 0
    OR (SELECT COUNT(*) FROM comments WHERE user_id = u.id AND created_at >= ?) > 0
    OR (SELECT COUNT(*) FROM post_votes    WHERE user_id = u.id AND created_at >= ?) > 0
    OR (SELECT COUNT(*) FROM comment_votes WHERE user_id = u.id AND created_at >= ?) > 0)
ORDER BY last_seen DESC
LIMIT ?`
	t := since.Unix()
	rows, err := s.db.QueryContext(ctx, q, t, t, t, t, t, t, t, t, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveUser
	for rows.Next() {
		var u ActiveUser
		var lastSeen int64
		if err := rows.Scan(&u.UserID, &u.Username, &u.Posts, &u.Comments, &u.Votes, &lastSeen); err != nil {
			return nil, err
		}
		u.LastSeen = time.Unix(lastSeen, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

// FeedEvent is a single row in the `vask-mod watch` live tail —
// either a new post or a new comment. Votes are intentionally excluded
// (high-frequency, low-signal). Returned in created_at order.
type FeedEvent struct {
	Kind      string // "post" or "comment"
	ID        int64
	UserID    int64
	Username  string
	PostID    int64  // for comments, the parent post; for posts, equals ID
	Title     string // post title (or parent post title for comments)
	Snippet   string // first ~80 chars of body
	CreatedAt time.Time
	IsSeed    bool
}

// EventsSince returns posts and comments created strictly after the
// given ids. The caller passes max(id) of the last batch and gets
// only newer rows back. Used by `vask-mod watch` polling.
func (s *Store) EventsSince(ctx context.Context, sincePostID, sinceCommentID int64, limit int) ([]FeedEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	const q = `
SELECT 'post' AS kind, p.id, p.user_id, COALESCE(u.username, ''), p.id AS post_id,
       p.title, substr(p.body, 1, 80), p.created_at,
       (CASE WHEN u.fingerprint_hash LIKE 'seed-%' THEN 1 ELSE 0 END) AS is_seed
FROM posts p LEFT JOIN users u ON u.id = p.user_id
WHERE p.id > ?
UNION ALL
SELECT 'comment' AS kind, c.id, c.user_id, COALESCE(u.username, ''), c.post_id,
       (SELECT title FROM posts WHERE id = c.post_id), substr(c.body, 1, 80), c.created_at,
       (CASE WHEN u.fingerprint_hash LIKE 'seed-%' THEN 1 ELSE 0 END) AS is_seed
FROM comments c LEFT JOIN users u ON u.id = c.user_id
WHERE c.id > ?
ORDER BY 8 ASC
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, sincePostID, sinceCommentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedEvent
	for rows.Next() {
		var e FeedEvent
		var createdAt int64
		var isSeed int
		if err := rows.Scan(&e.Kind, &e.ID, &e.UserID, &e.Username, &e.PostID,
			&e.Title, &e.Snippet, &createdAt, &isSeed); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(createdAt, 0)
		e.IsSeed = isSeed == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// MaxEventIDs returns the current maximum post.id and comment.id —
// the watch loop calls this once at startup to skip historical rows.
func (s *Store) MaxEventIDs(ctx context.Context) (postID, commentID int64, err error) {
	row := s.db.QueryRowContext(ctx, `SELECT
        COALESCE((SELECT MAX(id) FROM posts), 0),
        COALESCE((SELECT MAX(id) FROM comments), 0)`)
	err = row.Scan(&postID, &commentID)
	return
}

// Stats returns the operator-console snapshot in a single round-trip
// (one big SELECT with subquery aggregates). Cheap on Turso even from
// a far region because it's one network hop instead of ten.
func (s *Store) Stats(ctx context.Context, since time.Time) (*Stats, error) {
	const q = `
SELECT
  (SELECT COUNT(*) FROM users WHERE fingerprint_hash NOT LIKE 'seed-%' AND deleted_at IS NULL),
  (SELECT COUNT(*) FROM users WHERE fingerprint_hash LIKE 'seed-%'),
  (SELECT COUNT(*) FROM users WHERE deleted_at IS NOT NULL),
  (SELECT COUNT(*) FROM posts p JOIN users u ON u.id=p.user_id
     WHERE p.hidden=0 AND u.fingerprint_hash NOT LIKE 'seed-%'),
  (SELECT COUNT(*) FROM posts p JOIN users u ON u.id=p.user_id
     WHERE u.fingerprint_hash LIKE 'seed-%'),
  (SELECT COUNT(*) FROM posts WHERE hidden=1),
  (SELECT COUNT(*) FROM comments c JOIN users u ON u.id=c.user_id
     WHERE c.hidden=0 AND u.fingerprint_hash NOT LIKE 'seed-%'),
  (SELECT COUNT(*) FROM comments WHERE hidden=1),
  (SELECT COUNT(*) FROM post_votes),
  (SELECT COUNT(*) FROM comment_votes),
  (SELECT COUNT(*) FROM users WHERE created_at >= ? AND fingerprint_hash NOT LIKE 'seed-%'),
  (SELECT COUNT(*) FROM posts p JOIN users u ON u.id=p.user_id
     WHERE p.created_at >= ? AND u.fingerprint_hash NOT LIKE 'seed-%' AND p.hidden=0),
  (SELECT COUNT(*) FROM comments c JOIN users u ON u.id=c.user_id
     WHERE c.created_at >= ? AND u.fingerprint_hash NOT LIKE 'seed-%' AND c.hidden=0),
  (SELECT COUNT(DISTINCT p.user_id) FROM posts p JOIN users u ON u.id=p.user_id
     WHERE p.created_at >= ? AND u.fingerprint_hash NOT LIKE 'seed-%' AND p.hidden=0),
  (SELECT COUNT(DISTINCT c.user_id) FROM comments c JOIN users u ON u.id=c.user_id
     WHERE c.created_at >= ? AND u.fingerprint_hash NOT LIKE 'seed-%' AND c.hidden=0)
`
	sinceUnix := since.Unix()
	row := s.db.QueryRowContext(ctx, q,
		sinceUnix, sinceUnix, sinceUnix, sinceUnix, sinceUnix)
	var st Stats
	st.Since = since
	if err := row.Scan(
		&st.RealUsers, &st.SeedUsers, &st.DeletedUsers,
		&st.RealPosts, &st.SeedPosts, &st.HiddenPosts,
		&st.RealComments, &st.HiddenComments,
		&st.PostVotes, &st.CommentVotes,
		&st.NewUsersWindow, &st.NewRealPostsWindow, &st.NewRealCommentsWindow,
		&st.UniquePostersWindow, &st.UniqueCommentersWindow,
	); err != nil {
		return nil, fmt.Errorf("stats: %w", err)
	}
	return &st, nil
}
