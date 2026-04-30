package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voss-labs/vask/internal/embed"
	"github.com/voss-labs/vask/internal/store"
)

// firstpostModel runs once after onboarding when the deployment has AI
// configured. UX: ask for a one-line dilemma, send it to gemma-4, render
// two anonymous variants the user can post with one keystroke. Skipping
// drops straight to the feed — this is a nudge, never a gate.
//
// State machine:
//
//	dilemma ─(enter)─▶ loading ─(variants)─▶ pick ─(1|2)─▶ posting ─(done)─▶ feed
//	   │                                       │
//	   └─(s | empty enter)──────────────────────┴─▶ feed (skip)
type firstpostModel struct {
	st   *store.Store
	user *store.User

	step int

	input textinput.Model

	variants []embed.DraftVariant
	chosen   int

	status     string
	statusKind string
}

const (
	fpStepDilemma = 0
	fpStepLoading = 1
	fpStepPick    = 2
	fpStepPosting = 3

	fpMaxDilemma = 240
)

type firstpostDoneMsg struct{ postID int64 }
type firstpostSkipMsg struct{}

type firstpostVariantsMsg struct {
	variants []embed.DraftVariant
	err      error
}

type firstpostPostedMsg struct {
	id  int64
	err error
}

func newFirstPost(st *store.Store, user *store.User) firstpostModel {
	in := textinput.New()
	in.Placeholder = "anything bugging you, anything you want to know — keep it to one line"
	in.Width = ContentWidth - 4
	in.CharLimit = fpMaxDilemma
	in.Prompt = ""
	in.Cursor.Style = lipgloss.NewStyle().Foreground(colorBrand)
	in.Focus()

	return firstpostModel{
		st:    st,
		user:  user,
		step:  fpStepDilemma,
		input: in,
	}
}

func (m firstpostModel) Init() tea.Cmd { return m.input.Cursor.BlinkCmd() }

func (m firstpostModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case firstpostVariantsMsg:
		if msg.err != nil || len(msg.variants) == 0 {
			m.step = fpStepDilemma
			m.status = "couldn't draft anything just now — try rewording, or press s to skip"
			m.statusKind = "err"
			m.input.Focus()
			return m, m.input.Cursor.BlinkCmd()
		}
		m.variants = msg.variants
		m.step = fpStepPick
		m.status = ""
		m.statusKind = ""
		return m, nil

	case firstpostPostedMsg:
		if msg.err != nil {
			m.step = fpStepPick
			m.status = "couldn't post: " + msg.err.Error()
			m.statusKind = "err"
			return m, nil
		}
		return m, func() tea.Msg { return firstpostDoneMsg{postID: msg.id} }

	case tea.KeyMsg:
		switch m.step {
		case fpStepDilemma:
			return m.updateDilemma(msg)
		case fpStepPick:
			return m.updatePick(msg)
		case fpStepLoading, fpStepPosting:
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			return m, nil
		}
	}

	if m.step == fpStepDilemma {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m firstpostModel) updateDilemma(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		return m, func() tea.Msg { return firstpostSkipMsg{} }
	case "enter":
		dilemma := strings.TrimSpace(m.input.Value())
		if dilemma == "" {
			return m, func() tea.Msg { return firstpostSkipMsg{} }
		}
		m.step = fpStepLoading
		m.status = ""
		return m, m.draftCmd(dilemma)
	}
	// `s` only skips when the input is empty — otherwise the user is
	// trying to type the letter into their dilemma.
	if msg.String() == "s" && strings.TrimSpace(m.input.Value()) == "" {
		return m, func() tea.Msg { return firstpostSkipMsg{} }
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m firstpostModel) updatePick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "s", "S":
		return m, func() tea.Msg { return firstpostSkipMsg{} }
	case "r", "R":
		dilemma := strings.TrimSpace(m.input.Value())
		if dilemma == "" {
			return m, func() tea.Msg { return firstpostSkipMsg{} }
		}
		m.variants = nil
		m.step = fpStepLoading
		return m, m.draftCmd(dilemma)
	case "1", "2":
		idx := int(msg.String()[0] - '1')
		if idx < 0 || idx >= len(m.variants) {
			return m, nil
		}
		m.chosen = idx
		m.step = fpStepPosting
		return m, m.postCmd(m.variants[idx])
	}
	return m, nil
}

func (m firstpostModel) draftCmd(dilemma string) tea.Cmd {
	st := m.st
	return func() tea.Msg {
		client := st.EmbedClient()
		if client == nil {
			return firstpostVariantsMsg{err: embed.ErrNotConfigured}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		v, err := client.Draft(ctx, dilemma)
		return firstpostVariantsMsg{variants: v, err: err}
	}
}

func (m firstpostModel) postCmd(v embed.DraftVariant) tea.Cmd {
	st := m.st
	uid := m.user.ID
	title := strings.TrimSpace(v.Title)
	body := strings.TrimSpace(v.Body)
	tags := normalizeDraftTags(v.Tags)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		id, err := st.CreatePost(ctx, uid, title, body, tags)
		return firstpostPostedMsg{id: id, err: err}
	}
}

// normalizeDraftTags runs raw model output through store.NormalizeTag,
// dedupes, and caps at maxTagsPerPost. The model is asked for 2-4 lowercase
// tags; this is a belt-and-braces clean-up so a malformed response can't
// break CreatePost.
func normalizeDraftTags(raw []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		n := store.NormalizeTag(t)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		if len(out) >= maxTagsPerPost {
			break
		}
	}
	if len(out) == 0 {
		out = []string{"general"}
	}
	return out
}

// === view ============================================================

func (m firstpostModel) View() string {
	switch m.step {
	case fpStepDilemma:
		return m.viewDilemma()
	case fpStepLoading:
		return m.viewLoading()
	case fpStepPick:
		return m.viewPick()
	case fpStepPosting:
		return m.viewPosting()
	}
	return ""
}

func (m firstpostModel) viewDilemma() string {
	header := brandText.Render("welcome, " + m.user.Username)
	tagline := textDim.Render("kick things off with a starter post · or skip to lurk")

	rule := lipgloss.NewStyle().Foreground(colorBorder).
		Render(strings.Repeat("─", ContentWidth-4))

	intro := textBody.Render(
		"give us one line — a dilemma, a hot take, a thing you wish someone\n" +
			"would tell you. we'll draft two anonymous starter posts you can\n" +
			"send in one keystroke. nothing about your line gets saved.",
	)

	prompt := textBody.Render("what's on your mind?")

	exampleLabel := textMute.Render("examples ·")
	exampleStyle := textDim.Italic(true)
	examples := lipgloss.JoinVertical(lipgloss.Left,
		exampleLabel,
		exampleStyle.Render(`  "everyone's bagging placement offers and i'm spiraling"`),
		exampleStyle.Render(`  "is the OS prof actually checking attendance or nah"`),
	)

	box := formBoxFocused.Render(m.input.View())
	counter := lipgloss.NewStyle().
		Width(ContentWidth).Align(lipgloss.Right).Foreground(colorTextMute).
		Render(itoa(len(m.input.Value())) + " / " + itoa(fpMaxDilemma))

	statusLine := ""
	if m.status != "" {
		statusLine = renderStatus(m.status, m.statusKind)
	}

	keys := lipgloss.JoinHorizontal(lipgloss.Left,
		renderKey("enter", "draft variants"),
		renderKeySep(),
		renderKey("s", "skip to feed"),
		renderKeySep(),
		renderKey("esc", "skip"),
	)
	footer := footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(keys),
	)

	body := lipgloss.JoinVertical(lipgloss.Left,
		header, tagline, rule, "",
		intro, "",
		prompt, "",
		examples, "",
		box, counter, "",
		statusLine,
		footer,
	)
	return frameStyle.Render(body)
}

func (m firstpostModel) viewLoading() string {
	header := brandText.Render("drafting…")
	tagline := textDim.Render("reasoning model · usually 30-60 seconds")

	rule := lipgloss.NewStyle().Foreground(colorBorder).
		Render(strings.Repeat("─", ContentWidth-4))

	body := textBody.Render(
		"sit tight. gemma-4 is thinking through two short anonymous\n" +
			"variants of your line — different angles on the same situation.\n" +
			"reasoning models trade speed for quality, so this is the slow part.",
	)

	hint := textMute.Render("ctrl+c to quit")
	footer := footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(hint),
	)

	return frameStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		header, tagline, rule, "", body, "", footer,
	))
}

func (m firstpostModel) viewPick() string {
	header := brandText.Render("pick one to post")
	tagline := textDim.Render("two angles on your line · or redraft · or skip")

	rule := lipgloss.NewStyle().Foreground(colorBorder).
		Render(strings.Repeat("─", ContentWidth-4))

	cards := make([]string, 0, len(m.variants))
	for i, v := range m.variants {
		cards = append(cards, renderVariantCard(i+1, v))
	}
	cardsBlock := lipgloss.JoinVertical(lipgloss.Left, cards...)

	statusLine := ""
	if m.status != "" {
		statusLine = renderStatus(m.status, m.statusKind)
	}

	keys := lipgloss.JoinHorizontal(lipgloss.Left,
		renderKey("1-2", "post that one"),
		renderKeySep(),
		renderKey("r", "redraft"),
		renderKeySep(),
		renderKey("s", "skip"),
	)
	footer := footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(keys),
	)

	return frameStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		header, tagline, rule, "", cardsBlock, "", statusLine, footer,
	))
}

func (m firstpostModel) viewPosting() string {
	header := brandText.Render("posting…")
	rule := lipgloss.NewStyle().Foreground(colorBorder).
		Render(strings.Repeat("─", ContentWidth-4))
	statusLine := ""
	if m.status != "" {
		statusLine = renderStatus(m.status, m.statusKind)
	}
	hint := textMute.Render("ctrl+c to quit")
	footer := footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(hint),
	)
	return frameStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		header, rule, "", statusLine, footer,
	))
}

func renderVariantCard(num int, v embed.DraftVariant) string {
	key := keyChip.Render(itoa(num))
	title := lipgloss.NewStyle().Foreground(colorBrand).Bold(true).
		Render(strings.TrimSpace(v.Title))

	body := textBody.Render(strings.TrimSpace(v.Body))

	tagChip := lipgloss.NewStyle().Foreground(colorBrand)
	tags := normalizeDraftTags(v.Tags)
	chips := make([]string, 0, len(tags))
	for _, t := range tags {
		chips = append(chips, tagChip.Render("#"+t))
	}
	tagRow := strings.Join(chips, "  ")

	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBorder).
		Padding(0, 2).
		Width(ContentWidth - 4).
		MarginBottom(1)

	inner := lipgloss.JoinVertical(lipgloss.Left,
		key+"  "+title,
		"",
		body,
		"",
		tagRow,
	)
	return cardStyle.Render(inner)
}
