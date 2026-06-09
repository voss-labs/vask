package tui

import (
	"errors"
	"net"
	"testing"

	"github.com/voss-labs/vask/internal/store"
)

func TestFriendlyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "",
		},
		{
			name:     "network error",
			err:      &net.DNSError{Err: "no such host", Name: "db.example.com"},
			expected: "couldn't reach the database — check your connection",
		},
		{
			name:     "timeout error",
			err:      errors.New("context deadline exceeded"),
			expected: "request timed out — the database is taking too long to respond",
		},
		{
			name:     "database locked",
			err:      errors.New("database is locked"),
			expected: "the database is busy — please try again in a moment",
		},
		{
			name:     "schema error",
			err:      errors.New("no such table: users"),
			expected: "internal schema error — please report this",
		},
		{
			name:     "unauthorized",
			err:      errors.New("unauthorized access"),
			expected: "session expired or unauthorized — try reconnecting",
		},
		{
			name:     "self vote",
			err:      store.ErrSelfVote,
			expected: "you can't vote on your own contribution",
		},
		{
			name:     "generic error",
			err:      errors.New("something weird happened"),
			expected: "something went wrong: something weird happened",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := friendlyError(tt.err)
			if got != tt.expected {
				t.Errorf("friendlyError() = %q, want %q", got, tt.expected)
			}
		})
	}
}
