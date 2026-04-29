package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// splashModel is the first-connect TOS screen.
type splashModel struct{}

type tosAcceptedMsg struct{}

func newSplash() splashModel { return splashModel{} }

func (m splashModel) Init() tea.Cmd { return nil }

func (m splashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		s := k.String()
		if s == "ctrl+c" || s == "q" {
			return m, tea.Quit
		}
		return m, func() tea.Msg { return tosAcceptedMsg{} }
	}
	return m, nil
}

// chainMark renders the cascading-squares brand mark in orange.
func chainMark() string {
	s := lipgloss.NewStyle().Foreground(colorBrand)
	rows := []string{
		"▰ ▰ ▰ ▰",
		"  ▰ ▰ ▰",
		"    ▰ ▰",
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = s.Render(r)
	}
	return lipgloss.JoinVertical(lipgloss.Center, out...)
}

func (m splashModel) View() string {
	mark := chainMark()

	title := brandText.Render("voss / vask")
	tagline := textDim.Render("campus q&a · open source · terminal-native")

	rule := lipgloss.NewStyle().
		Foreground(colorBorder).
		Render("──────────────────────────────────────────────")

	intro := textBody.Render(
		"your ssh key is hashed (sha256) and used only to give you a\n" +
			"stable identity for rate-limiting and ban management. no\n" +
			"email, no real name, no IP logged. your posts and votes\n" +
			"are never linked to your key in any public view.",
	)

	rulesTitle := textDim.Render("rules")
	rules := textBody.Render(
		"  • no real names. initials or descriptions are fine.\n" +
			"  • no phone numbers, social handles, schedules.\n" +
			"  • no targeted harassment, doxxing, or revenge posts.\n" +
			"  • stay on-topic for the channel you post in.",
	)

	source := textMute.Render("audit the code: github.com/voss-labs/vask")

	cta := textDim.Render("press ") + keyChip.Render("any key") + textDim.Render(" to continue")

	body := lipgloss.JoinVertical(
		lipgloss.Center,
		mark,
		"",
		title,
		tagline,
		rule,
		"",
		intro,
		"",
		rulesTitle,
		rules,
		"",
		source,
		"",
		cta,
	)

	return frameStyle.Render(body)
}
