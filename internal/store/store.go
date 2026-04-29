// Package store wraps the SQLite-compatible database with typed read/write
// methods.
//
// In production we run against Turso (libsql) so the data layer is decoupled
// from the compute layer — backend can move providers without touching data.
// For local dev, point at a plain `file:` path and we fall back to local
// SQLite (modernc.org/sqlite). Both speak SQLite; schema is identical;
// the rest of the code doesn't care which one is in use.
//
// All identity columns hold the SHA256 hex hash of an SSH public key — never
// the raw key, never an email, never a name.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/tursodatabase/libsql-client-go/libsql" // libsql:// (Turso)
	_ "modernc.org/sqlite"                                // file:./db (local dev)
)

//go:embed migrations/001_init.sql
var schema001 string

// Store is a thin wrapper around *sql.DB.
type Store struct {
	db *sql.DB
}

// Open connects to a SQLite-compatible database. If url starts with libsql://,
// https://, or wss:// it talks to a Turso instance. Otherwise the value is
// treated as a local SQLite file path. authToken is appended to the libsql URL
// when non-empty.
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

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema001)
	return err
}

// =====================================================================
// users
// =====================================================================

type User struct {
	ID              int64
	FingerprintHash string
	CreatedAt       time.Time
	TOSAcceptedAt   *time.Time
	Banned          bool
	BanReason       string
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
		`SELECT id, fingerprint_hash, created_at, tos_accepted_at, banned, COALESCE(ban_reason,'')
		 FROM users WHERE fingerprint_hash = ?`,
		fingerprint,
	)
	var u User
	var createdAt int64
	var tos sql.NullInt64
	var banned int
	if err := row.Scan(&u.ID, &u.FingerprintHash, &createdAt, &tos, &banned, &u.BanReason); err != nil {
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

func (s *Store) AcceptTOS(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET tos_accepted_at = ? WHERE id = ? AND tos_accepted_at IS NULL`,
		time.Now().Unix(), userID,
	)
	return err
}

// =====================================================================
// channels
// =====================================================================

type Channel struct {
	Slug        string
	Name        string
	Description string
	SortOrder   int
	PostCount   int
}

// ListChannels returns all channels sorted by sort_order, with cached post counts.
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

// =====================================================================
// posts
// =====================================================================

// Post is the public-facing row. Hidden + Reports are zero in the public
// Feed query and only populated by admin queries.
type Post struct {
	ID           int64
	UserID       int64
	Channel      string
	Title        string
	Body         string
	CreatedAt    time.Time
	Score        int   // sum(post_votes.value)
	MyVote       int   // -1, 0, +1 — the calling user's current vote
	CommentCount int   // count of non-hidden comments
	Hidden       bool  // admin only
	Reports      int   // admin only
}

// SortMode controls ListPosts ordering.
type SortMode int

const (
	SortHot SortMode = iota
	SortNew
	SortTop
)

// ListPosts returns posts in the chosen channel (empty string = all channels).
// myUserID is used to populate Post.MyVote — pass 0 for unauthenticated.
func (s *Store) ListPosts(ctx context.Context, channel string, sort SortMode, limit int, myUserID int64) ([]Post, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	orderBy := postOrderBy(sort)

	var args []any
	args = append(args, myUserID) // for my_vote LEFT JOIN
	q := `
		SELECT p.id, p.user_id, p.channel, p.title, p.body, p.created_at,
		       COALESCE((SELECT SUM(value) FROM post_votes WHERE post_id = p.id), 0)        AS score,
		       COALESCE((SELECT value      FROM post_votes WHERE post_id = p.id AND user_id = ?), 0) AS my_vote,
		       (SELECT COUNT(*) FROM comments WHERE post_id = p.id AND hidden = 0)           AS comments
		FROM posts p
		WHERE p.hidden = 0`
	if channel != "" {
		q += ` AND p.channel = ?`
		args = append(args, channel)
	}
	q += ` ORDER BY ` + orderBy + ` LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Post, 0, limit)
	for rows.Next() {
		var p Post
		var createdAt int64
		if err := rows.Scan(&p.ID, &p.UserID, &p.Channel, &p.Title, &p.Body, &createdAt,
			&p.Score, &p.MyVote, &p.CommentCount); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

func postOrderBy(sort SortMode) string {
	switch sort {
	case SortNew:
		return "p.created_at DESC, p.id DESC"
	case SortTop:
		return "score DESC, p.created_at DESC"
	case SortHot:
		fallthrough
	default:
		// HN-style: score / (hours_old + 2)^1.8
		return `(
			COALESCE((SELECT SUM(value) FROM post_votes WHERE post_id = p.id), 0)
			/ POWER(((strftime('%s','now') - p.created_at) / 3600.0) + 2.0, 1.8)
		) DESC, p.created_at DESC`
	}
}

// GetPost returns a single post including the calling user's vote, or nil if
// not found or hidden (when myUserID isn't a moderator). Pass 0 for myUserID
// if anonymous.
func (s *Store) GetPost(ctx context.Context, postID, myUserID int64) (*Post, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT p.id, p.user_id, p.channel, p.title, p.body, p.created_at, p.hidden, p.reports,
		       COALESCE((SELECT SUM(value) FROM post_votes WHERE post_id = p.id), 0),
		       COALESCE((SELECT value FROM post_votes WHERE post_id = p.id AND user_id = ?), 0),
		       (SELECT COUNT(*) FROM comments WHERE post_id = p.id AND hidden = 0)
		FROM posts p WHERE p.id = ?`, myUserID, postID)

	var p Post
	var createdAt int64
	var hidden int
	if err := row.Scan(&p.ID, &p.UserID, &p.Channel, &p.Title, &p.Body, &createdAt, &hidden, &p.Reports,
		&p.Score, &p.MyVote, &p.CommentCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p.CreatedAt = time.Unix(createdAt, 0)
	p.Hidden = hidden == 1
	if p.Hidden {
		// public callers shouldn't see hidden posts
		return nil, nil
	}
	return &p, nil
}

// CreatePost inserts a new post and returns its id.
func (s *Store) CreatePost(ctx context.Context, userID int64, channel, title, body string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO posts(user_id, channel, title, body, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		userID, channel, title, body, time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CountUserPostsSince — for the per-user post-rate limiter.
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

// SetPostVote sets the user's vote to value (-1, 0, or +1). 0 = remove vote.
// Returns the new score.
func (s *Store) SetPostVote(ctx context.Context, userID, postID int64, value int) (int, error) {
	if value != -1 && value != 0 && value != 1 {
		return 0, fmt.Errorf("invalid vote value: %d", value)
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

// DeleteOwnPost deletes a post the calling user authored, plus all dependent
// rows (votes, comments + comment votes) — in a single transaction. Strictly
// enforced at the SQL level.
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

	// delete comment votes for any comment under this post, then comments, then post-related rows
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM comment_votes WHERE comment_id IN (SELECT id FROM comments WHERE post_id = ?)`,
		postID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM comments   WHERE post_id = ?`, postID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM post_votes WHERE post_id = ?`, postID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM posts      WHERE id = ? AND user_id = ?`, postID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// =====================================================================
// comments (thread tree)
// =====================================================================

type Comment struct {
	ID              int64
	PostID          int64
	ParentCommentID *int64 // nil = top-level
	UserID          int64
	Body            string
	CreatedAt       time.Time
	Score           int
	MyVote          int
	Hidden          bool
}

// ListComments returns all comments under a post ordered by created_at,
// flat (caller assembles the tree using ParentCommentID).
func (s *Store) ListComments(ctx context.Context, postID, myUserID int64) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.post_id, c.parent_comment_id, c.user_id, c.body, c.created_at, c.hidden,
		       COALESCE((SELECT SUM(value) FROM comment_votes WHERE comment_id = c.id), 0),
		       COALESCE((SELECT value      FROM comment_votes WHERE comment_id = c.id AND user_id = ?), 0)
		FROM comments c
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
		if err := rows.Scan(&c.ID, &c.PostID, &parent, &c.UserID, &c.Body, &createdAt, &hidden,
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

// CreateComment adds a top-level (parent=nil) or threaded reply.
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

// SetCommentVote — same semantics as SetPostVote.
func (s *Store) SetCommentVote(ctx context.Context, userID, commentID int64, value int) (int, error) {
	if value != -1 && value != 0 && value != 1 {
		return 0, fmt.Errorf("invalid vote value: %d", value)
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

// DeleteOwnComment — strictly own comments. Cascades to comment_votes and
// any nested replies (set their parent to NULL so they orphan up rather than
// disappearing — keeps thread context intact).
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

// =====================================================================
// moderator-only queries (used by cmd/ask-mod)
// =====================================================================

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

// =====================================================================
// helpers
// =====================================================================

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
