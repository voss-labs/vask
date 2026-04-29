package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voss-labs/ask/internal/policy"
	"github.com/voss-labs/ask/internal/ratelimit"
	"github.com/voss-labs/ask/internal/store"
)

const (
	maxTitleChars = 120
	maxBodyChars  = 2000

	stepChannel = 0
	stepTitle   = 1
	stepBody    = 2
)

type composeModel struct {
	st      *store.Store
	user    *store.User
	limiter *ratelimit.PostLimiter

	channels      []store.Channel
	chanIdx       int
	channelLocked bool // when entering compose from a channel feed, the channel is pre-selected
	preset        string

	title textinput.Model
	body  textarea.Model
	step  int

	width  int
	height int

	status     string
	statusKind string

	flagsAck bool
	sending  bool
}

type composePostedMsg struct {
	id      int64
	channel string
	err     error
}

type composeChannelsMsg struct {
	channels []store.Channel
	err      error
}

func newCompose(st *store.Store, user *store.User, presetChannel string) composeModel {
	title := textinput.New()
	title.Placeholder = "one-line title — what's the question or rant?"
	title.Width = ContentWidth - 4
	title.CharLimit = maxTitleChars
	title.Prompt = ""
	title.Cursor.Style = lipgloss.NewStyle().Foreground(colorBrand)

	body := textarea.New()
	body.Placeholder = "details. context. what you've tried."
	body.SetWidth(ContentWidth - 4)
	body.SetHeight(8)
	body.CharLimit = maxBodyChars
	body.Prompt = ""
	body.ShowLineNumbers = false
	body.FocusedStyle.CursorLine = lipgloss.NewStyle()
	body.FocusedStyle.Base = lipgloss.NewStyle()
	body.BlurredStyle.Base = lipgloss.NewStyle()
	body.Cursor.Style = lipgloss.NewStyle().Foreground(colorBrand)

	c := composeModel{
		st:            st,
		user:          user,
		limiter:       ratelimit.NewPostLimiter(st, 5),
		title:         title,
		body:          body,
		channelLocked: presetChannel != "",
		preset:        presetChannel,
		step:          stepChannel,
	}
	if c.channelLocked {
		c.step = stepTitle
	}
	return c
}

func (m composeModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, loadComposeChannels(m.st))
}

func loadComposeChannels(st *store.Store) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ch, err := st.ListChannels(ctx)
		return composeChannelsMsg{channels: ch, err: err}
	}
}

func (m composeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case composeChannelsMsg:
		m.channels = msg.channels
		if msg.err != nil {
			m.status = "couldn't load channels: " + msg.err.Error()
			m.statusKind = "err"
			return m, nil
		}
		// align chanIdx to preset slug if any
		if m.preset != "" {
			for i, c := range m.channels {
				if c.Slug == m.preset {
					m.chanIdx = i
					break
				}
			}
		}
	case composePostedMsg:
		m.sending = false
		if msg.err != nil {
			m.status = "couldn't post: " + msg.err.Error()
			m.statusKind = "err"
			return m, nil
		}
		return m, func() tea.Msg { return composeSubmittedMsg{postID: msg.id, channel: msg.channel} }
	case tea.KeyMsg:
		if m.sending {
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.step == stepChannel || (m.channelLocked && m.step == stepTitle) {
				return m, func() tea.Msg { return composeCancelledMsg{} }
			}
			m.step--
			if m.channelLocked && m.step == stepChannel {
				m.step = stepTitle
			}
			m.clearStatus()
			return m.applyStepFocus(), nil
		case "shift+tab":
			if m.step > stepChannel {
				m.step--
				if m.channelLocked && m.step == stepChannel {
					m.step = stepTitle
				}
				m.clearStatus()
				return m.applyStepFocus(), nil
			}
		case "tab":
			if m.step < stepBody {
				m.step++
				m.clearStatus()
				return m.applyStepFocus(), nil
			}
		case "enter":
			if m.step != stepBody {
				m.step++
				m.clearStatus()
				return m.applyStepFocus(), nil
			}
		case "ctrl+s":
			if m.step != stepBody {
				m.step = stepBody
				m.clearStatus()
				return m.applyStepFocus(), nil
			}
			return m.submit()
		case "left", "h":
			if m.step == stepChannel && m.chanIdx > 0 {
				m.chanIdx--
				return m, nil
			}
		case "right", "l":
			if m.step == stepChannel && m.chanIdx < len(m.channels)-1 {
				m.chanIdx++
				return m, nil
			}
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			if m.step == stepChannel {
				idx := int(msg.String()[0] - '1')
				if idx >= 0 && idx < len(m.channels) {
					m.chanIdx = idx
				}
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	switch m.step {
	case stepTitle:
		m.title, cmd = m.title.Update(msg)
	case stepBody:
		m.body, cmd = m.body.Update(msg)
	}
	return m, cmd
}

func (m composeModel) applyStepFocus() composeModel {
	m.title.Blur()
	m.body.Blur()
	switch m.step {
	case stepTitle:
		m.title.Focus()
	case stepBody:
		m.body.Focus()
	}
	return m
}

func (m *composeModel) clearStatus() {
	m.status = ""
	m.statusKind = ""
	m.flagsAck = false
}

func (m composeModel) submit() (tea.Model, tea.Cmd) {
	title := strings.TrimSpace(m.title.Value())
	body := strings.TrimSpace(m.body.Value())

	if title == "" {
		m.status = "title is empty."
		m.statusKind = "err"
		m.step = stepTitle
		return m.applyStepFocus(), nil
	}
	if body == "" {
		m.status = "body is empty."
		m.statusKind = "err"
		return m, nil
	}
	if len(body) > maxBodyChars {
		m.status = fmt.Sprintf("body too long (%d / %d).", len(body), maxBodyChars)
		m.statusKind = "err"
		return m, nil
	}
	if len(m.channels) == 0 || m.chanIdx < 0 || m.chanIdx >= len(m.channels) {
		m.status = "pick a valid channel."
		m.statusKind = "err"
		return m, nil
	}

	if !m.flagsAck {
		flags := policy.Inspect(title + " " + body)
		if len(flags) > 0 {
			kinds := make([]string, len(flags))
			for i, f := range flags {
				kinds[i] = f.Kind
			}
			m.status = fmt.Sprintf("heads up — looks like %s. press ctrl+s again to send anyway.",
				strings.Join(kinds, ", "))
			m.statusKind = "warn"
			m.flagsAck = true
			return m, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	allowed, remaining, _, err := m.limiter.Allow(ctx, m.user.ID)
	if err != nil {
		m.status = "rate-limit check failed: " + err.Error()
		m.statusKind = "err"
		return m, nil
	}
	if !allowed {
		m.status = fmt.Sprintf("daily post limit reached. remaining: %d", remaining)
		m.statusKind = "err"
		return m, nil
	}

	channel := m.channels[m.chanIdx].Slug
	m.sending = true
	m.status = "sending…"
	m.statusKind = "ok"

	st := m.st
	uid := m.user.ID
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		id, err := st.CreatePost(ctx, uid, channel, title, body)
		return composePostedMsg{id: id, channel: channel, err: err}
	}
}

// === view ============================================================

func (m composeModel) View() string {
	header := renderComposeHeader(m.step, m.totalSteps())
	dots := renderStepDots(m.step, m.totalSteps())

	var stepBlock string
	switch m.step {
	case stepChannel:
		stepBlock = renderChannelStep(m.channels, m.chanIdx)
	case stepTitle:
		stepBlock = renderTitleStep(m)
	case stepBody:
		stepBlock = renderBodyStep(m)
	}

	bread := ""
	if m.step > stepChannel || m.channelLocked {
		bread = renderComposeBreadcrumb(m)
	}

	statusLine := ""
	if m.status != "" {
		statusLine = renderStatus(m.status, m.statusKind)
	}

	footer := renderComposeFooter(m.step, m.channelLocked)

	body := lipgloss.JoinVertical(lipgloss.Left, header, dots, bread, stepBlock, statusLine, footer)
	return frameStyle.Render(body)
}

func (m composeModel) totalSteps() int {
	if m.channelLocked {
		return 2
	}
	return 3
}

func renderComposeHeader(step, total int) string {
	stepLabel := []string{"channel", "title", "body"}[step]
	left := brandText.Render("voss") + textMute.Render(" / ") + brandText.Render("ask")
	stepIdx := step + 1
	if total == 2 && step > 0 {
		stepIdx = step
	}
	right := textDim.Render(fmt.Sprintf("new post · %d/%d · %s", stepIdx, total, stepLabel))
	gap := ContentWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return headerStyle.Render(left + strings.Repeat(" ", gap) + right)
}

func renderStepDots(step, total int) string {
	on := lipgloss.NewStyle().Foreground(colorBrand)
	off := lipgloss.NewStyle().Foreground(colorBorder)
	dots := make([]string, total)
	curIdx := step
	if total == 2 && step > 0 {
		curIdx = step - 1
	}
	for i := range dots {
		switch {
		case i == curIdx:
			dots[i] = on.Bold(true).Render("●")
		case i < curIdx:
			dots[i] = on.Render("●")
		default:
			dots[i] = off.Render("○")
		}
	}
	return lipgloss.NewStyle().
		Width(ContentWidth).Align(lipgloss.Center).PaddingTop(1).PaddingBottom(1).
		Render(strings.Join(dots, "   "))
}

func renderComposeBreadcrumb(m composeModel) string {
	var parts []string
	if m.chanIdx >= 0 && m.chanIdx < len(m.channels) {
		parts = append(parts, catBadgeOn.Render("#"+m.channels[m.chanIdx].Slug))
	}
	if m.step >= stepBody {
		t := strings.TrimSpace(m.title.Value())
		if t == "" {
			t = "(no title yet)"
		}
		parts = append(parts, postHint.PaddingLeft(0).Render(`"`+truncateRunes(t, 40)+`"`))
	}
	row := strings.Join(parts, textMute.Render("  ·  "))
	return lipgloss.NewStyle().Width(ContentWidth).MarginBottom(1).Render(row)
}

func renderChannelStep(channels []store.Channel, sel int) string {
	prompt := textBody.Render("pick a channel for your post")
	rows := []string{prompt, ""}
	for i, c := range channels {
		num := textMute.Render(fmt.Sprintf("%d", i+1))
		name := "#" + c.Slug
		var styled string
		if i == sel {
			styled = num + " " + catFocusBracket.Render("‹ ") + catBadgeOn.Render(name) + catFocusBracket.Render(" ›") +
				textDim.Render("   "+c.Description)
		} else {
			styled = num + " " + catBadgeOff.Render(name) + textDim.Render("   "+c.Description)
		}
		rows = append(rows, "  "+styled)
	}
	rows = append(rows, "")
	rows = append(rows, textMute.Render(fmt.Sprintf("use ←→ or 1-%d · enter to continue", len(channels))))
	return lipgloss.NewStyle().PaddingTop(1).PaddingBottom(1).Render(strings.Join(rows, "\n"))
}

func renderTitleStep(m composeModel) string {
	prompt := textBody.Render("title — keep it scannable")
	box := formBoxFocused.Render(m.title.View())
	counter := lipgloss.NewStyle().
		Width(ContentWidth).Align(lipgloss.Right).Foreground(colorTextMute).
		Render(fmt.Sprintf("%d / %d", len(m.title.Value()), maxTitleChars))
	hint := textMute.Render("enter to continue · shift+tab back · esc back")
	return lipgloss.NewStyle().PaddingTop(1).PaddingBottom(1).Render(
		strings.Join([]string{prompt, "", box, counter, "", hint}, "\n"),
	)
}

func renderBodyStep(m composeModel) string {
	prompt := textBody.Render("body")
	box := formBoxFocused.Render(m.body.View())
	counter := lipgloss.NewStyle().
		Width(ContentWidth).Align(lipgloss.Right).Foreground(colorTextMute).
		Render(fmt.Sprintf("%d / %d", len(m.body.Value()), maxBodyChars))
	return lipgloss.NewStyle().PaddingTop(1).PaddingBottom(0).Render(
		strings.Join([]string{prompt, "", box, counter}, "\n"),
	)
}

func renderStatus(s, kind string) string {
	var styled string
	switch kind {
	case "err":
		styled = textErr.Render("✗ " + s)
	case "warn":
		styled = textWarn.Render("⚠ " + s)
	case "ok":
		styled = textOk.Render("→ " + s)
	default:
		styled = textDim.Render(s)
	}
	return lipgloss.NewStyle().Width(ContentWidth).MarginTop(1).Render(styled)
}

func renderComposeFooter(step int, channelLocked bool) string {
	var keys []string
	switch step {
	case stepChannel:
		keys = []string{
			renderKey("←→", "pick"),
			renderKey("1-9", "jump"),
			renderKey("enter", "continue"),
			renderKey("esc", "cancel"),
		}
	case stepTitle:
		back := "shift+tab"
		if channelLocked {
			back = "esc"
		}
		keys = []string{
			renderKey("enter", "continue"),
			renderKey(back, "back"),
		}
		if !channelLocked {
			keys = append(keys, renderKey("esc", "cancel"))
		}
	case stepBody:
		keys = []string{
			renderKey("ctrl+s", "send"),
			renderKey("shift+tab", "back"),
			renderKey("esc", "back"),
		}
	}
	row := strings.Join(keys, renderKeySep())
	return footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(row),
	)
}
