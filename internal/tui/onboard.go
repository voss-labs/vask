package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voss-labs/vask/internal/store"
	"github.com/voss-labs/vask/internal/username"
)

// onboardModel is the one-time username picker shown after splash for users
// who haven't been assigned a handle yet. UX:
//
//   - we present three candidates side-by-side ("◆ polite-okapi", etc.)
//   - 1/2/3 claims the corresponding one
//   - R rerolls all three locally (no DB write)
//   - on collision we silently replace just that slot with a new draw and
//     keep going, so the user is never stuck staring at an unclaimable name
//
// Once claimed the username is permanent — what every other user sees
// attached to your posts and comments. Showing three at once turns this
// from a slot-machine ("am I going to like the next one?") into a
// curated shortlist.
type onboardModel struct {
	st         *store.Store
	user       *store.User
	candidates [3]string
	collisions int    // consecutive failed claims; drives the suffix fallback
	flash      string // transient status message
	claiming   int    // 0 = not claiming; 1..3 = which slot is being claimed
}

type usernameAcceptedMsg struct{ username string }

type usernameClaimResultMsg struct {
	slot      int
	candidate string
	ok        bool
	err       error
}

func newOnboard(st *store.Store, user *store.User) onboardModel {
	return onboardModel{
		st:         st,
		user:       user,
		candidates: [3]string{username.Random(), username.Random(), username.Random()},
	}
}

func (m onboardModel) Init() tea.Cmd { return nil }

func (m onboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.claiming != 0 {
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "r", "R", "tab", " ":
			m.candidates = [3]string{username.Random(), username.Random(), username.Random()}
			m.collisions = 0
			m.flash = ""
			return m, nil
		case "1", "2", "3":
			slot := int(msg.String()[0] - '0')
			m.claiming = slot
			m.flash = ""
			return m, m.claimCmd(slot, m.candidates[slot-1])
		}

	case usernameClaimResultMsg:
		m.claiming = 0
		if msg.err != nil {
			m.flash = "couldn't reach the database — try again"
			return m, nil
		}
		if msg.ok {
			m.user.Username = msg.candidate
			return m, func() tea.Msg { return usernameAcceptedMsg{username: msg.candidate} }
		}
		// collision: replace just that slot, with a numeric suffix once
		// we've been unlucky a few times in a row
		m.collisions++
		var fresh string
		if m.collisions >= 3 {
			fresh = username.RandomWithSuffix()
		} else {
			fresh = username.Random()
		}
		m.candidates[msg.slot-1] = fresh
		m.flash = "that one was just taken — picked another for that slot"
		return m, nil
	}
	return m, nil
}

func (m onboardModel) claimCmd(slot int, candidate string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ok, err := m.st.ClaimUsername(ctx, m.user.ID, candidate)
		return usernameClaimResultMsg{slot: slot, candidate: candidate, ok: ok, err: err}
	}
}

func (m onboardModel) View() string {
	title := brandText.Render("pick a handle")
	tagline := textDim.Render("auto-generated and anonymous · permanent once chosen")

	rule := lipgloss.NewStyle().
		Foreground(colorBorder).
		Render("──────────────────────────────────────────────")

	intro := textBody.Render(
		"this is the name your posts and comments will appear under.\n" +
			"three options below — pick the one you like, or reroll for new ones.",
	)

	// Each candidate gets a numbered chip + identicon + name in a roomy box.
	rowStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBorder).
		Padding(0, 2).
		Width(ContentWidth - 8)
	claimingStyle := rowStyle.BorderForeground(colorBrand)

	var rows []string
	for i, c := range m.candidates {
		key := keyChip.Render([]string{"1", "2", "3"}[i])
		name := lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Render(c)
		line := key + "  " + name
		st := rowStyle
		if m.claiming == i+1 {
			st = claimingStyle
		}
		rows = append(rows, st.Render(line))
	}
	candidateBlock := lipgloss.JoinVertical(lipgloss.Center, rows...)

	keys := lipgloss.JoinHorizontal(
		lipgloss.Left,
		renderKey("1-3", "pick"),
		renderKeySep(),
		renderKey("r", "reroll all"),
		renderKeySep(),
		renderKey("q", "quit"),
	)

	flashLine := ""
	switch {
	case m.flash != "":
		flashLine = textWarn.Render(m.flash)
	case m.claiming != 0:
		flashLine = textDim.Render("claiming…")
	}

	body := lipgloss.JoinVertical(
		lipgloss.Center,
		title,
		tagline,
		rule,
		"",
		intro,
		"",
		candidateBlock,
		"",
		flashLine,
		"",
		keys,
	)

	return frameStyle.Render(body)
}
