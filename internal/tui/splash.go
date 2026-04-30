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
		Render("──────────────────────────────────────")

	privacy := textMute.Render(
		"anonymous · no email, no real name, no ip stored",
	)

	// Each pitch line is its own string so JoinVertical(Center, ...)
	// centers them individually — keeps the whole splash visually
	// symmetric, no bulleted block looking left-shifted next to a
	// centered heading.
	pitch := []string{
		textBody.Render("for the questions you can't ask on linkedin or insta —"),
		textBody.Render("internships, placements, ml vs ds, courses you're"),
		textBody.Render("behind in, group-project drama, anything weighing on you."),
		"",
		textBody.Render("post once. people reply. you check back tomorrow."),
		textBody.Render("nobody knows it's you."),
	}

	rulesTitle := textDim.Render("ground rules")
	rules := []string{
		textBody.Render("no real names · no phone numbers · no socials"),
		textBody.Render("no doxxing · no targeted harassment"),
	}

	source := textMute.Render("audit the code: ") +
		hyperlink("https://github.com/voss-labs/vask",
			textMute.Render("github.com/voss-labs/vask"))
	cta := textDim.Render("press ") + keyChip.Render("any key") + textDim.Render(" to continue")

	parts := []string{mark, "", title, tagline, rule, "", privacy, ""}
	parts = append(parts, pitch...)
	parts = append(parts, "", rulesTitle)
	parts = append(parts, rules...)
	parts = append(parts, "", source, "", cta)

	return frameStyle.Render(lipgloss.JoinVertical(lipgloss.Center, parts...))
}
