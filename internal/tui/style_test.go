package tui

import (
	"testing"
)

func TestLinkify(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain url",
			input:    "https://google.com",
			expected: hyperlink("https://google.com", "https://google.com"),
		},
		{
			name:     "url with trailing dot",
			input:    "Visit https://google.com.",
			expected: "Visit " + hyperlink("https://google.com", "https://google.com") + ".",
		},
		{
			name:     "url with trailing exclamation",
			input:    "Look at https://google.com!",
			expected: "Look at " + hyperlink("https://google.com", "https://google.com") + "!",
		},
		{
			name:     "url in parentheses",
			input:    "Check (https://google.com)",
			expected: "Check (" + hyperlink("https://google.com", "https://google.com") + ")",
		},
		{
			name:     "multiple urls",
			input:    "https://a.com and https://b.com",
			expected: hyperlink("https://a.com", "https://a.com") + " and " + hyperlink("https://b.com", "https://b.com"),
		},
		{
			name:     "url with path and query",
			input:    "https://google.com/search?q=foo",
			expected: hyperlink("https://google.com/search?q=foo", "https://google.com/search?q=foo"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := linkify(tt.input)
			if got != tt.expected {
				t.Errorf("linkify() = %q, want %q", got, tt.expected)
			}
		})
	}
}
