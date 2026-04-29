package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voss-labs/ask/internal/store"
)

// detailModel renders a single post in full + a comments stub.
// Threaded comments + reply modal lands in v0.2.
type detailModel struct {
	st     *store.Store
	user   *store.User
	postID int64

	post   *store.Post
	width  int
	height int
	err    error
	flash  string
}

type detailLoadedMsg struct {
	post *store.Post
	err  error
}

type detailVoteMsg struct {
	newScore int
	newVote  int
	err      error
}

func newDetail(st *store.Store, user *store.User, postID int64) detailModel {
	return detailModel{st: st, user: user, postID: postID}
}

func (m detailModel) Init() tea.Cmd { return loadDetail(m.st, m.postID, m.user.ID) }

func loadDetail(st *store.Store, postID, userID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p, err := st.GetPost(ctx, postID, userID)
		return detailLoadedMsg{post: p, err: err}
	}
}

func detailVoteCmd(st *store.Store, userID, postID int64, value int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		score, err := st.SetPostVote(ctx, userID, postID, value)
		return detailVoteMsg{newScore: score, newVote: value, err: err}
	}
}

func (m detailModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case detailLoadedMsg:
		m.post = msg.post
		m.err = msg.err
	case detailVoteMsg:
		if msg.err != nil {
			m.flash = "couldn't vote: " + msg.err.Error()
			return m, nil
		}
		if m.post != nil {
			m.post.Score = msg.newScore
			m.post.MyVote = msg.newVote
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "b", "esc":
			return m, func() tea.Msg { return closeDetailMsg{} }
		case "r":
			return m, loadDetail(m.st, m.postID, m.user.ID)
		case "v":
			return m.applyVote(+1)
		case "V":
			return m.applyVote(-1)
		}
	}
	return m, nil
}

func (m detailModel) applyVote(delta int) (tea.Model, tea.Cmd) {
	if m.post == nil {
		return m, nil
	}
	target := delta
	if m.post.MyVote == delta {
		target = 0
	}
	return m, detailVoteCmd(m.st, m.user.ID, m.post.ID, target)
}

func (m detailModel) View() string {
	header := renderDetailHeader(m.post)

	var body string
	switch {
	case m.err != nil:
		body = textErr.Render("error loading post: " + m.err.Error())
	case m.post == nil:
		body = textDim.Render("loading…")
	default:
		body = renderPostDetail(m.post, m.user.ID)
	}

	flashLine := ""
	if m.flash != "" {
		flashLine = lipgloss.NewStyle().Foreground(colorBrand).Width(ContentWidth).MarginTop(1).Render("· " + m.flash)
	}

	footer := renderDetailFooter()
	return frameStyle.Render(
		lipgloss.JoinVertical(lipgloss.Left, header, body, flashLine, footer),
	)
}

func renderDetailHeader(p *store.Post) string {
	left := brandText.Render("voss") + textMute.Render(" / ") + brandText.Render("ask")
	if p != nil {
		left += textMute.Render(" / ") + catBadgeOn.Render("#"+p.Channel)
	}
	right := textDim.Render("post")
	if p != nil {
		right = textDim.Render(fmt.Sprintf("post #%d", p.ID))
	}
	gap := ContentWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return headerStyle.Render(left + strings.Repeat(" ", gap) + right)
}

func renderPostDetail(p *store.Post, currentUserID int64) string {
	title := lipgloss.NewStyle().
		Foreground(colorText).
		Bold(true).
		Width(ContentWidth).
		Render(p.Title)

	var meta []string
	if p.UserID == currentUserID {
		meta = append(meta, lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Render(anonID(p.UserID))+textDim.Render(" (you)"))
	} else {
		meta = append(meta, textMute.Render(anonID(p.UserID)))
	}
	meta = append(meta, textMute.Render(relTime(p.CreatedAt)))
	meta = append(meta, renderScore(p.Score, p.MyVote))
	meta = append(meta, textMute.Render(fmt.Sprintf("💬 %d", p.CommentCount)))
	metaLine := strings.Join(meta, textMute.Render("  ·  "))

	rule := lipgloss.NewStyle().Foreground(colorBorder).Render(strings.Repeat("─", ContentWidth))

	body := postBody.PaddingLeft(0).Render(wrap(p.Body, ContentWidth-2))

	commentsHeader := lipgloss.NewStyle().
		Foreground(colorTextDim).
		MarginTop(2).
		Render(fmt.Sprintf("─── %d comments ───", p.CommentCount))

	commentsStub := lipgloss.NewStyle().
		Foreground(colorTextMute).
		Italic(true).
		PaddingLeft(2).
		MarginTop(1).
		Render("threaded comments arrive in v0.2.")

	return lipgloss.JoinVertical(lipgloss.Left,
		title,
		"",
		metaLine,
		"",
		rule,
		"",
		body,
		commentsHeader,
		commentsStub,
	)
}

func renderDetailFooter() string {
	row := strings.Join([]string{
		renderKey("v", "▲"),
		renderKey("V", "▼"),
		renderKey("r", "refresh"),
		renderKey("b", "back"),
		renderKey("q", "quit"),
	}, renderKeySep())
	return footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(row),
	)
}
