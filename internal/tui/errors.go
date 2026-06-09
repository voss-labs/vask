package tui

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/voss-labs/vask/internal/store"
)

// friendlyError returns a user-facing string for common internal/database errors.
// It helps keep the UI clean and informative without exposing raw SQL/network traces.
func friendlyError(err error) string {
	if err == nil {
		return ""
	}

	// Database/Network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		return "couldn't reach the database — check your connection"
	}

	s := err.Error()

	// common sqlite/libsql errors
	switch {
	case strings.Contains(s, "context deadline exceeded"):
		return "request timed out — the database is taking too long to respond"
	case strings.Contains(s, "database is locked"), strings.Contains(s, "busy"):
		return "the database is busy — please try again in a moment"
	case strings.Contains(s, "no such table"), strings.Contains(s, "no such column"):
		return "internal schema error — please report this"
	case strings.Contains(s, "unauthorized"), strings.Contains(s, "forbidden"):
		return "session expired or unauthorized — try reconnecting"
	}

	// Store-specific errors
	if errors.Is(err, store.ErrSelfVote) {
		return "you can't vote on your own contribution"
	}

	// Fallback to a cleaner version of the raw error
	return fmt.Sprintf("something went wrong: %v", err)
}
