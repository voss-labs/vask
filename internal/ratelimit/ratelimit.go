// Package ratelimit enforces per-user posting limits backed by the store.
//
// We deliberately avoid in-memory token buckets for posts because a
// process restart should not let a user double their daily quota.
// Vote/reply rate-limits can stay in-memory because the consequence of
// drift is tiny.
package ratelimit

import (
	"context"
	"time"

	"github.com/voss-labs/vask/internal/store"
)

// PostLimiter checks whether a user is allowed to create another post.
type PostLimiter struct {
	st        *store.Store
	maxPerDay int
}

func NewPostLimiter(st *store.Store, maxPerDay int) *PostLimiter {
	if maxPerDay <= 0 {
		maxPerDay = 3
	}
	return &PostLimiter{st: st, maxPerDay: maxPerDay}
}

// Allow returns (allowed, remaining, resetIn).
func (l *PostLimiter) Allow(ctx context.Context, userID int64) (bool, int, time.Duration, error) {
	since := time.Now().Add(-24 * time.Hour)
	n, err := l.st.CountUserPostsSince(ctx, userID, since)
	if err != nil {
		return false, 0, 0, err
	}
	remaining := l.maxPerDay - n
	if remaining < 0 {
		remaining = 0
	}
	return n < l.maxPerDay, remaining, 24 * time.Hour, nil
}

// CommentLimiter checks whether a user is allowed to post another comment.
// Comments are an order of magnitude cheaper than posts, so the cap is
// proportionally higher (default 30/day vs PostLimiter's 5/day) — enough
// to support active threading while still capping a runaway script.
type CommentLimiter struct {
	st        *store.Store
	maxPerDay int
}

func NewCommentLimiter(st *store.Store, maxPerDay int) *CommentLimiter {
	if maxPerDay <= 0 {
		maxPerDay = 30
	}
	return &CommentLimiter{st: st, maxPerDay: maxPerDay}
}

func (l *CommentLimiter) Allow(ctx context.Context, userID int64) (bool, int, time.Duration, error) {
	since := time.Now().Add(-24 * time.Hour)
	n, err := l.st.CountUserCommentsSince(ctx, userID, since)
	if err != nil {
		return false, 0, 0, err
	}
	remaining := l.maxPerDay - n
	if remaining < 0 {
		remaining = 0
	}
	return n < l.maxPerDay, remaining, 24 * time.Hour, nil
}
