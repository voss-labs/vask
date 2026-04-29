// Package policy is the auto-flag layer: cheap regex checks that fire BEFORE
// a post is inserted and surface a "are you sure?" confirm in the TUI.
//
// False positives are fine — the dialog is the deterrent. The goal is to
// make casually pasting a phone number or full name a deliberate choice,
// not a reflex.
package policy

import (
	"regexp"
	"strings"
)

var (
	// phone numbers (Indian formats: 10 digits, optionally with +91 / 91 /
	// spaces / dashes)
	rePhone = regexp.MustCompile(`(?:\+?91[\s-]?)?[6-9]\d{2}[\s-]?\d{3}[\s-]?\d{4}`)

	// social handle patterns
	reInsta   = regexp.MustCompile(`(?i)(?:instagram\.com/|insta\.com/|@)[a-z0-9._]{3,30}`)
	reTelegram = regexp.MustCompile(`(?i)t\.me/[a-z0-9_]{4,}`)
	reEmail    = regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}`)

	// crude full-name heuristic: 2+ consecutive Capitalized words.
	// false-positive prone, paired with confirm dialog.
	reFullName = regexp.MustCompile(`\b[A-Z][a-z]{2,}\s+[A-Z][a-z]{2,}\b`)

	// class schedule specifics: "CS-A 3rd year", "BE Mech", "B-section"
	reClassSpec = regexp.MustCompile(`(?i)\b(?:cs|it|extc|mech|civil|aiml|ai-?ds|biotech)[\s-][a-z](\b|\s+\d)`)
)

// Flag is a single matched concern.
type Flag struct {
	Kind    string
	Snippet string
}

// Inspect returns flags found in body. Empty slice = nothing tripped.
func Inspect(body string) []Flag {
	var flags []Flag
	check := func(kind string, re *regexp.Regexp) {
		if m := re.FindString(body); m != "" {
			flags = append(flags, Flag{Kind: kind, Snippet: trim(m)})
		}
	}
	check("phone number", rePhone)
	check("instagram handle", reInsta)
	check("telegram handle", reTelegram)
	check("email", reEmail)
	check("class section", reClassSpec)
	check("full name", reFullName)
	return flags
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 32 {
		return s[:29] + "..."
	}
	return s
}
