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

	"github.com/voss-labs/vask/internal/embed"
	"github.com/voss-labs/vask/internal/policy"
	"github.com/voss-labs/vask/internal/ratelimit"
	"github.com/voss-labs/vask/internal/store"
)

const (
	maxTitleChars = 120
	maxBodyChars  = 2000
	maxTagsChars  = 100
	maxTagsPerPost = 5

	stepTitle = 0
	stepBody  = 1
	stepTags  = 2

	// drafter sub-states — when non-zero, the drafter owns the keyboard
	// and the compose view is replaced with the draft picker UI.
	drafterOff     = 0
	drafterPrompt  = 1
	drafterLoading = 2
	drafterPick    = 3
)

// Seed suggestions when the DB has fewer than 6 distinct tags. Once enough
// real tags exist, suggestions come from data.
var seedTagSuggestions = []string{
	"complaints", "electives", "hostel", "mess", "canteen",
	"exams", "placement", "internship", "lost-found", "study-group", "general", "meta",
}

type composeModel struct {
	st      *store.Store
	user    *store.User
	limiter *ratelimit.PostLimiter

	title textinput.Model
	body  textarea.Model
	tags  textinput.Model
	step  int

	suggestions []string
	// similarTags is the union of tags used by the 3 most semantically
	// similar existing posts to the current draft (title+body). Surfaced
	// at the tags step so writers can pick tags that match how *other
	// people* tagged related discussion. Empty when embeddings aren't
	// configured, when the user has typed too little to embed, or when
	// no similar posts exist yet.
	similarTags []string

	width  int
	height int

	status     string
	statusKind string

	flagsAck bool
	sending  bool

	// Esc on a non-empty draft arms this; the next `y` confirms discard,
	// anything else cancels. Saves users from one-keystroke draft loss.
	pendingDiscard bool

	// drafter is the optional ai-assist sub-flow. ctrl+d on the title
	// step (when AI is configured) enters drafterPrompt; on pick the
	// chosen variant fills title/body/tags and the user lands back in
	// stepTitle to edit before sending.
	drafterStep     int
	drafterInput    textinput.Model
	drafterVariants []embed.DraftVariant
}

type composePostedMsg struct {
	id  int64
	err error
}

type composeSuggestionsMsg struct {
	tags []string
}

type composeSimilarTagsMsg struct {
	tags []string
}

type composeDrafterVariantsMsg struct {
	variants []embed.DraftVariant
	err      error
}

func newCompose(st *store.Store, user *store.User) composeModel {
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

	tags := textinput.New()
	tags.Placeholder = "comma-separated, e.g. electives, cs, study-group"
	tags.Width = ContentWidth - 4
	tags.CharLimit = maxTagsChars
	tags.Prompt = ""
	tags.Cursor.Style = lipgloss.NewStyle().Foreground(colorBrand)

	drafter := textinput.New()
	drafter.Placeholder = "one line — what's the rant or question?"
	drafter.Width = ContentWidth - 4
	drafter.CharLimit = fpMaxDilemma
	drafter.Prompt = ""
	drafter.Cursor.Style = lipgloss.NewStyle().Foreground(colorBrand)

	c := composeModel{
		st:           st,
		user:         user,
		limiter:      ratelimit.NewPostLimiter(st, 5),
		title:        title,
		body:         body,
		tags:         tags,
		step:         stepTitle,
		drafterInput: drafter,
	}
	// FOCUS BUG FIX: focus the initial step's input so the very first keystroke
	// types into it. Previously the focus was never set, so users had to press
	// Enter once before typing would land.
	return c.applyStepFocus()
}

func (m composeModel) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink, loadTagSuggestions(m.st)}
	// kick the cursor blink for the title input as well
	cmds = append(cmds, m.title.Cursor.BlinkCmd())
	return tea.Batch(cmds...)
}

func loadTagSuggestions(st *store.Store) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tags, err := st.ListPopularTags(ctx, 8)
		if err != nil {
			return composeSuggestionsMsg{tags: nil}
		}
		return composeSuggestionsMsg{tags: tags}
	}
}

// loadSimilarTags embeds the current draft (title + body) and surfaces
// the tag union from the 3 most similar existing posts. No-op when no
// embedding client is configured or the draft is too short to be
// meaningful — we want suggestions to feel relevant, not random.
func loadSimilarTags(st *store.Store, title, body string) tea.Cmd {
	return func() tea.Msg {
		client := st.EmbedClient()
		if client == nil {
			return composeSimilarTagsMsg{}
		}
		text := strings.TrimSpace(title + "\n\n" + body)
		if len(text) < 10 {
			return composeSimilarTagsMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		v, err := client.Embed(ctx, text)
		if err != nil {
			return composeSimilarTagsMsg{}
		}
		posts, err := st.NearestPostsByVector(ctx, v, 3)
		if err != nil {
			return composeSimilarTagsMsg{}
		}
		seen := map[string]bool{}
		var tags []string
		for _, p := range posts {
			for _, t := range p.Tags {
				if seen[t] {
					continue
				}
				seen[t] = true
				tags = append(tags, t)
			}
		}
		return composeSimilarTagsMsg{tags: tags}
	}
}

func (m composeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case composeSuggestionsMsg:
		m.suggestions = msg.tags
		// fall back to seed list if DB is sparse
		if len(m.suggestions) < 6 {
			seen := map[string]bool{}
			for _, t := range m.suggestions {
				seen[t] = true
			}
			for _, t := range seedTagSuggestions {
				if !seen[t] {
					m.suggestions = append(m.suggestions, t)
				}
				if len(m.suggestions) >= 8 {
					break
				}
			}
		}

	case composeSimilarTagsMsg:
		m.similarTags = msg.tags
	case composeDrafterVariantsMsg:
		if msg.err != nil || len(msg.variants) == 0 {
			m.drafterStep = drafterPrompt
			m.status = "couldn't draft anything just now — try rewording, or esc to cancel"
			m.statusKind = "err"
			m.drafterInput.Focus()
			return m, m.drafterInput.Cursor.BlinkCmd()
		}
		m.drafterVariants = msg.variants
		m.drafterStep = drafterPick
		m.clearStatus()
		return m, nil
	case composePostedMsg:
		m.sending = false
		if msg.err != nil {
			m.status = "couldn't post: " + msg.err.Error()
			m.statusKind = "err"
			return m, nil
		}
		return m, func() tea.Msg { return composeSubmittedMsg{postID: msg.id} }
	case tea.KeyMsg:
		if m.sending {
			return m, nil
		}
		// Drafter sub-flow owns the keyboard while it's open.
		if m.drafterStep != drafterOff {
			return m.updateDrafter(msg)
		}
		// While a discard is armed, intercept `y` to confirm and any other
		// key (including esc) to cancel the prompt. ctrl+c still quits.
		if m.pendingDiscard {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "y", "Y":
				return m, func() tea.Msg { return composeCancelledMsg{} }
			default:
				m.pendingDiscard = false
				m.status = ""
				return m, nil
			}
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.step == stepTitle {
				if m.hasDraftContent() {
					m.pendingDiscard = true
					m.status = "discard draft? press y to throw it away · any other key keeps editing."
					m.statusKind = "warn"
					return m, nil
				}
				return m, func() tea.Msg { return composeCancelledMsg{} }
			}
			m.step--
			m.clearStatus()
			return m.advanceStep()
		case "shift+tab":
			if m.step > stepTitle {
				m.step--
				m.clearStatus()
				return m.advanceStep()
			}
		case "tab":
			if m.step < stepTags {
				m.step++
				m.clearStatus()
				return m.advanceStep()
			}
		case "enter":
			// Enter advances on title and tags steps; in body it inserts a newline.
			if m.step == stepTitle || m.step == stepTags {
				if m.step < stepTags {
					m.step++
					m.clearStatus()
					return m.advanceStep()
				}
				// on tags step, enter = submit
				return m.submit()
			}
			// step == stepBody: fall through to textarea
		case "ctrl+s":
			if m.step != stepTags {
				m.step = stepTags
				m.clearStatus()
				return m.advanceStep()
			}
			return m.submit()
		case "ctrl+d":
			// AI assist: only on the title step, only when configured.
			if m.step != stepTitle || m.st.EmbedClient() == nil {
				return m, nil
			}
			m.drafterStep = drafterPrompt
			m.drafterInput.SetValue("")
			m.drafterInput.Focus()
			m.title.Blur()
			m.clearStatus()
			return m, m.drafterInput.Cursor.BlinkCmd()
		}
	}

	var cmd tea.Cmd
	switch m.step {
	case stepTitle:
		m.title, cmd = m.title.Update(msg)
	case stepBody:
		m.body, cmd = m.body.Update(msg)
	case stepTags:
		m.tags, cmd = m.tags.Update(msg)
	}
	return m, cmd
}

func (m composeModel) applyStepFocus() composeModel {
	m.title.Blur()
	m.body.Blur()
	m.tags.Blur()
	switch m.step {
	case stepTitle:
		m.title.Focus()
	case stepBody:
		m.body.Focus()
	case stepTags:
		m.tags.Focus()
	}
	return m
}

// updateDrafter handles keystrokes while the AI-assist sub-flow is open.
// Esc cancels back to the regular compose view; enter on a non-empty line
// kicks off the model call; 1/2 in the pick state fills the title/body/
// tags fields with the chosen variant and returns control to the title
// step for review.
func (m composeModel) updateDrafter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.drafterStep {
	case drafterPrompt:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.drafterStep = drafterOff
			m.drafterInput.Blur()
			m.clearStatus()
			return m.applyStepFocus(), nil
		case "enter":
			dilemma := strings.TrimSpace(m.drafterInput.Value())
			if dilemma == "" {
				return m, nil
			}
			m.drafterStep = drafterLoading
			m.clearStatus()
			return m, m.drafterCmd(dilemma)
		}
		var cmd tea.Cmd
		m.drafterInput, cmd = m.drafterInput.Update(msg)
		return m, cmd
	case drafterLoading:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	case drafterPick:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.drafterStep = drafterOff
			m.drafterInput.Blur()
			m.clearStatus()
			return m.applyStepFocus(), nil
		case "r", "R":
			dilemma := strings.TrimSpace(m.drafterInput.Value())
			if dilemma == "" {
				return m, nil
			}
			m.drafterStep = drafterLoading
			m.drafterVariants = nil
			return m, m.drafterCmd(dilemma)
		case "1", "2":
			idx := int(msg.String()[0] - '1')
			if idx < 0 || idx >= len(m.drafterVariants) {
				return m, nil
			}
			v := m.drafterVariants[idx]
			m.title.SetValue(strings.TrimSpace(v.Title))
			m.body.SetValue(strings.TrimSpace(v.Body))
			m.tags.SetValue(strings.Join(normalizeDraftTags(v.Tags), ", "))
			m.drafterStep = drafterOff
			m.drafterInput.Blur()
			m.drafterVariants = nil
			m.step = stepTitle
			m.clearStatus()
			m.status = "drafted from your line · edit anything before sending"
			m.statusKind = "ok"
			return m.applyStepFocus(), nil
		}
		return m, nil
	}
	return m, nil
}

func (m composeModel) drafterCmd(dilemma string) tea.Cmd {
	st := m.st
	return func() tea.Msg {
		client := st.EmbedClient()
		if client == nil {
			return composeDrafterVariantsMsg{err: embed.ErrNotConfigured}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		v, err := client.Draft(ctx, dilemma)
		return composeDrafterVariantsMsg{variants: v, err: err}
	}
}

// advanceStep is the focus-shift wrapper used by every step transition
// (tab / shift+tab / enter / esc-back). It applies the step's focus AND
// fires the similar-tags suggestion query whenever we land on stepTags
// — that's the only step where the suggestion is shown, and we want
// it refreshed each visit so it reflects the latest body content.
func (m composeModel) advanceStep() (tea.Model, tea.Cmd) {
	m = m.applyStepFocus()
	if m.step == stepTags {
		return m, loadSimilarTags(m.st, m.title.Value(), m.body.Value())
	}
	return m, nil
}

func (m *composeModel) clearStatus() {
	m.status = ""
	m.statusKind = ""
	m.flagsAck = false
}

// hasDraftContent reports whether the user has typed anything worth
// confirming a discard for. Pure whitespace doesn't count.
func (m composeModel) hasDraftContent() bool {
	return strings.TrimSpace(m.title.Value()) != "" ||
		strings.TrimSpace(m.body.Value()) != "" ||
		strings.TrimSpace(m.tags.Value()) != ""
}

func (m composeModel) parsedTags() []string {
	raw := strings.Split(m.tags.Value(), ",")
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
	return out
}

func (m composeModel) submit() (tea.Model, tea.Cmd) {
	title := strings.TrimSpace(m.title.Value())
	body := strings.TrimSpace(m.body.Value())
	tags := m.parsedTags()

	if title == "" {
		m.status = "title is empty."
		m.statusKind = "err"
		m.step = stepTitle
		return m.applyStepFocus(), nil
	}
	if body == "" {
		m.status = "body is empty."
		m.statusKind = "err"
		m.step = stepBody
		return m.applyStepFocus(), nil
	}
	if len(body) > maxBodyChars {
		m.status = fmt.Sprintf("body too long (%d / %d).", len(body), maxBodyChars)
		m.statusKind = "err"
		return m, nil
	}
	if len(tags) == 0 {
		m.status = "add at least one tag (so people can find this)."
		m.statusKind = "err"
		m.step = stepTags
		return m.applyStepFocus(), nil
	}

	if !m.flagsAck {
		flags := policy.Inspect(title + " " + body)
		if len(flags) > 0 {
			kinds := make([]string, len(flags))
			for i, f := range flags {
				kinds[i] = f.Kind
			}
			m.status = fmt.Sprintf("heads up — looks like %s. press enter again to send anyway.",
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

	m.sending = true
	m.status = "sending…"
	m.statusKind = "ok"

	st := m.st
	uid := m.user.ID
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		id, err := st.CreatePost(ctx, uid, title, body, tags)
		return composePostedMsg{id: id, err: err}
	}
}

// === view ============================================================

func (m composeModel) View() string {
	if m.drafterStep != drafterOff {
		return m.viewDrafter()
	}
	header := renderComposeHeader(m.step)
	dots := renderStepDots(m.step)

	var stepBlock string
	switch m.step {
	case stepTitle:
		stepBlock = renderTitleStep(m)
	case stepBody:
		stepBlock = renderBodyStep(m)
	case stepTags:
		stepBlock = renderTagsStep(m)
	}

	bread := ""
	if m.step > stepTitle {
		bread = renderComposeBreadcrumb(m)
	}

	statusLine := ""
	if m.status != "" {
		statusLine = renderStatus(m.status, m.statusKind)
	}

	footer := renderComposeFooter(m.step)

	body := lipgloss.JoinVertical(lipgloss.Left, header, dots, bread, stepBlock, statusLine, footer)
	return frameStyle.Render(body)
}

func renderComposeHeader(step int) string {
	stepLabel := []string{"title", "body", "tags"}[step]
	left := brandText.Render("voss") + textMute.Render(" / ") + brandText.Render("vask")
	right := textDim.Render(fmt.Sprintf("new post · %d/3 · %s", step+1, stepLabel))
	gap := ContentWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return headerStyle.Render(left + strings.Repeat(" ", gap) + right)
}

func renderStepDots(step int) string {
	on := lipgloss.NewStyle().Foreground(colorBrand)
	off := lipgloss.NewStyle().Foreground(colorBorder)
	dots := []string{"●", "●", "●"}
	for i := range dots {
		switch {
		case i == step:
			dots[i] = on.Bold(true).Render("●")
		case i < step:
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
	t := strings.TrimSpace(m.title.Value())
	if t == "" {
		t = "(no title yet)"
	}
	parts = append(parts, postHint.PaddingLeft(0).Render(`"`+truncateRunes(t, 50)+`"`))
	if m.step >= stepTags {
		// show first 80 chars of body inline as second crumb
		b := strings.TrimSpace(m.body.Value())
		if b != "" {
			b = strings.ReplaceAll(b, "\n", " ")
			parts = append(parts, textMute.Render(truncateRunes(b, 50)))
		}
	}
	row := strings.Join(parts, textMute.Render("  ·  "))
	return lipgloss.NewStyle().Width(ContentWidth).MarginBottom(1).Render(row)
}

func renderTitleStep(m composeModel) string {
	prompt := textBody.Render("title — keep it scannable")
	box := formBoxFocused.Render(m.title.View())
	counter := lipgloss.NewStyle().
		Width(ContentWidth).Align(lipgloss.Right).Foreground(colorTextMute).
		Render(fmt.Sprintf("%d / %d", len(m.title.Value()), maxTitleChars))
	hint := textMute.Render("enter to continue · esc to cancel")
	return lipgloss.NewStyle().PaddingTop(1).PaddingBottom(1).Render(
		strings.Join([]string{prompt, "", box, counter, "", hint}, "\n"),
	)
}

func renderBodyStep(m composeModel) string {
	prompt := textBody.Render("body — context, what you've tried, links")
	box := formBoxFocused.Render(m.body.View())
	counter := lipgloss.NewStyle().
		Width(ContentWidth).Align(lipgloss.Right).Foreground(colorTextMute).
		Render(fmt.Sprintf("%d / %d", len(m.body.Value()), maxBodyChars))
	hint := textMute.Render("ctrl+s when done · shift+tab back · esc back")
	return lipgloss.NewStyle().PaddingTop(1).PaddingBottom(1).Render(
		strings.Join([]string{prompt, "", box, counter, "", hint}, "\n"),
	)
}

func renderTagsStep(m composeModel) string {
	prompt := textBody.Render("tags — comma-separated, max 5 (lowercase, hyphens for spaces)")

	// suggestions row(s) — popular tags from the whole forum, plus
	// (when embeddings are configured) tags pulled from the 3 most
	// semantically similar existing posts. The semantic row matters more,
	// so it renders second (closer to the input) and uses a slightly
	// stronger color to nudge the eye toward it.
	chip := lipgloss.NewStyle().Foreground(colorBrand)

	var suggestionsBlock []string
	if len(m.suggestions) > 0 {
		picks := make([]string, 0, len(m.suggestions))
		for _, t := range m.suggestions {
			picks = append(picks, chip.Render("#"+t))
		}
		suggestionsBlock = append(suggestionsBlock,
			textDim.Render("popular tags · ")+strings.Join(picks, "  "))
	}
	if len(m.similarTags) > 0 {
		similarChip := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
		picks := make([]string, 0, len(m.similarTags))
		for _, t := range m.similarTags {
			picks = append(picks, similarChip.Render("#"+t))
		}
		suggestionsBlock = append(suggestionsBlock,
			textDim.Render("tags from similar posts · ")+strings.Join(picks, "  "))
	}
	suggestions := strings.Join(suggestionsBlock, "\n")

	box := formBoxFocused.Render(m.tags.View())

	parsed := m.parsedTags()

	counter := lipgloss.NewStyle().
		Width(ContentWidth).Align(lipgloss.Right).Foreground(colorTextMute).
		Render(fmt.Sprintf("%d / %d", len(parsed), maxTagsPerPost))

	// "this is what your post will look like" mini-card. Built from a
	// synthesized store.Post and rendered through the same renderPostRow
	// the feed uses, so what you see here is byte-for-byte what readers
	// will see (modulo selection/delete bars).
	previewLabel := textDim.Render("preview")
	previewRule := lipgloss.NewStyle().Foreground(colorBorder).Render(strings.Repeat("─", ContentWidth-4))
	previewCard := renderPostRow(
		0,
		store.Post{
			Title:     strings.TrimSpace(m.title.Value()),
			Body:      strings.TrimSpace(m.body.Value()),
			Tags:      parsed,
			Username:  m.user.Username,
			UserID:    m.user.ID,
			CreatedAt: time.Now(),
		},
		false, // not selected
		true,  // it's me
		false, // not pending delete
		false, // not compact — show body preview so writer sees what readers see
	)

	hint := textMute.Render("enter to send · shift+tab back · esc back")

	return lipgloss.NewStyle().PaddingTop(1).PaddingBottom(1).Render(
		strings.Join([]string{
			prompt,
			"",
			suggestions,
			"",
			box,
			counter,
			"",
			previewLabel,
			previewRule,
			previewCard,
			previewRule,
			"",
			hint,
		}, "\n"),
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

func (m composeModel) viewDrafter() string {
	header := brandText.Render("ai assist · draft from one line")
	tagline := textDim.Render("type a one-liner · we'll suggest two anonymous angles")
	rule := lipgloss.NewStyle().Foreground(colorBorder).
		Render(strings.Repeat("─", ContentWidth-4))

	var bodyBlock string
	switch m.drafterStep {
	case drafterPrompt:
		intro := textBody.Render("nothing about your line gets saved.")
		box := formBoxFocused.Render(m.drafterInput.View())
		counter := lipgloss.NewStyle().
			Width(ContentWidth).Align(lipgloss.Right).Foreground(colorTextMute).
			Render(fmt.Sprintf("%d / %d", len(m.drafterInput.Value()), fpMaxDilemma))
		bodyBlock = lipgloss.JoinVertical(lipgloss.Left,
			intro, "", box, counter,
		)
	case drafterLoading:
		bodyBlock = textBody.Render(
			"gemma-4 is thinking · reasoning model · 30-60 seconds…",
		)
	case drafterPick:
		cards := make([]string, 0, len(m.drafterVariants))
		for i, v := range m.drafterVariants {
			cards = append(cards, renderVariantCard(i+1, v))
		}
		bodyBlock = lipgloss.JoinVertical(lipgloss.Left, cards...)
	}

	statusLine := ""
	if m.status != "" {
		statusLine = renderStatus(m.status, m.statusKind)
	}

	var keys []string
	switch m.drafterStep {
	case drafterPrompt:
		keys = []string{
			renderKey("enter", "draft"),
			renderKey("esc", "cancel"),
		}
	case drafterLoading:
		keys = []string{renderKey("ctrl+c", "quit")}
	case drafterPick:
		keys = []string{
			renderKey("1-2", "use that one"),
			renderKey("r", "redraft"),
			renderKey("esc", "cancel"),
		}
	}
	row := strings.Join(keys, renderKeySep())
	footer := footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(row),
	)

	return frameStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		header, tagline, rule, "", bodyBlock, "", statusLine, footer,
	))
}

func renderComposeFooter(step int) string {
	var keys []string
	switch step {
	case stepTitle:
		keys = []string{
			renderKey("enter", "continue"),
			renderKey("ctrl+d", "ai draft"),
			renderKey("esc", "cancel"),
		}
	case stepBody:
		keys = []string{
			renderKey("ctrl+s", "next: tags"),
			renderKey("shift+tab", "back"),
			renderKey("esc", "back"),
		}
	case stepTags:
		keys = []string{
			renderKey("enter", "send"),
			renderKey("shift+tab", "back"),
			renderKey("esc", "back"),
		}
	}
	row := strings.Join(keys, renderKeySep())
	return footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(row),
	)
}
