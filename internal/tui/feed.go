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

// feedModel is the post-list view for one channel (or all-channels).
type feedModel struct {
	st      *store.Store
	user    *store.User
	channel string // "" = all
	sort    store.SortMode

	posts            []store.Post
	cursor           int
	width            int
	height           int
	err              error
	pendingDeleteIdx int
	flash            string
}

type postsLoadedMsg struct {
	posts []store.Post
	err   error
}

type voteAppliedMsg struct {
	postID   int64
	newScore int
	newVote  int
	err      error
}

type postDeletedMsg struct {
	postID int64
	err    error
}

func newFeed(st *store.Store, user *store.User, channel string) feedModel {
	return feedModel{st: st, user: user, channel: channel, sort: store.SortHot, pendingDeleteIdx: -1}
}

func (m feedModel) Init() tea.Cmd { return loadPosts(m.st, m.channel, m.sort, m.user.ID) }

func loadPosts(st *store.Store, channel string, sort store.SortMode, userID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		posts, err := st.ListPosts(ctx, channel, sort, 50, userID)
		return postsLoadedMsg{posts: posts, err: err}
	}
}

func setPostVoteCmd(st *store.Store, userID, postID int64, value int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		score, err := st.SetPostVote(ctx, userID, postID, value)
		return voteAppliedMsg{postID: postID, newScore: score, newVote: value, err: err}
	}
}

func deleteOwnPostCmd(st *store.Store, userID, postID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := st.DeleteOwnPost(ctx, userID, postID)
		return postDeletedMsg{postID: postID, err: err}
	}
}

func (m feedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case postsLoadedMsg:
		m.posts = msg.posts
		m.err = msg.err
		if m.cursor >= len(m.posts) {
			m.cursor = 0
		}
	case voteAppliedMsg:
		if msg.err != nil {
			m.flash = "couldn't vote: " + msg.err.Error()
			return m, nil
		}
		for i := range m.posts {
			if m.posts[i].ID == msg.postID {
				m.posts[i].Score = msg.newScore
				m.posts[i].MyVote = msg.newVote
				break
			}
		}
	case postDeletedMsg:
		if msg.err != nil {
			m.flash = "couldn't delete: " + msg.err.Error()
			m.pendingDeleteIdx = -1
			return m, nil
		}
		m.pendingDeleteIdx = -1
		m.flash = "post deleted."
		return m, loadPosts(m.st, m.channel, m.sort, m.user.ID)
	case tea.KeyMsg:
		key := msg.String()
		// any non-D key while pending clears the pending state (esc, arrow, etc still handled below)
		if m.pendingDeleteIdx >= 0 && key != "d" && key != "D" {
			m.pendingDeleteIdx = -1
			m.flash = ""
		}
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "c", "esc":
			return m, func() tea.Msg { return backToChannelsMsg{} }
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.posts)-1 {
				m.cursor++
			}
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = len(m.posts) - 1
			if m.cursor < 0 {
				m.cursor = 0
			}
		case "r":
			m.flash = ""
			return m, loadPosts(m.st, m.channel, m.sort, m.user.ID)
		case "n":
			return m, func() tea.Msg { return openComposeMsg{channel: m.channel} }
		case "f":
			m.sort = (m.sort + 1) % 3
			return m, loadPosts(m.st, m.channel, m.sort, m.user.ID)
		case "enter":
			if len(m.posts) == 0 {
				return m, nil
			}
			id := m.posts[m.cursor].ID
			return m, func() tea.Msg { return openDetailMsg{postID: id} }
		case "v":
			return m.applyVote(+1)
		case "V":
			if m.pendingDeleteIdx >= 0 && m.pendingDeleteIdx == m.cursor {
				return m.confirmDelete()
			}
			return m.applyVote(-1)
		case "d":
			if len(m.posts) == 0 {
				return m, nil
			}
			p := m.posts[m.cursor]
			if p.UserID != m.user.ID {
				m.flash = "you can only delete your own posts."
				m.pendingDeleteIdx = -1
				return m, nil
			}
			m.pendingDeleteIdx = m.cursor
			m.flash = ""
			return m, nil
		case "D":
			return m.confirmDelete()
		}
	}
	return m, nil
}

func (m feedModel) applyVote(delta int) (tea.Model, tea.Cmd) {
	if len(m.posts) == 0 {
		return m, nil
	}
	p := m.posts[m.cursor]
	target := delta
	if p.MyVote == delta {
		target = 0 // pressing same direction toggles off
	}
	return m, setPostVoteCmd(m.st, m.user.ID, p.ID, target)
}

func (m feedModel) confirmDelete() (tea.Model, tea.Cmd) {
	if m.pendingDeleteIdx < 0 || m.pendingDeleteIdx != m.cursor || len(m.posts) == 0 {
		return m, nil
	}
	p := m.posts[m.cursor]
	if p.UserID != m.user.ID {
		m.pendingDeleteIdx = -1
		return m, nil
	}
	return m, deleteOwnPostCmd(m.st, m.user.ID, p.ID)
}

func (m feedModel) View() string {
	header := renderFeedHeader(m.channel, m.sort, len(m.posts))

	var body string
	switch {
	case m.err != nil:
		body = textErr.Render("error loading feed: " + m.err.Error())
	case len(m.posts) == 0:
		body = renderEmptyFeed()
	default:
		visible := visiblePostsForHeight(m.height)
		start, end := windowAround(m.cursor, len(m.posts), visible)

		var rows []string
		if start > 0 {
			rows = append(rows, textMute.Render(fmt.Sprintf("  ↑ %d more above", start)))
		}
		for i := start; i < end; i++ {
			rows = append(rows, renderPostRow(
				i, m.posts[i],
				i == m.cursor,
				m.posts[i].UserID == m.user.ID,
				i == m.pendingDeleteIdx,
				m.channel == "",
			))
		}
		if end < len(m.posts) {
			rows = append(rows, textMute.Render(fmt.Sprintf("  ↓ %d more below", len(m.posts)-end)))
		}
		body = strings.Join(rows, "\n")
	}

	flashLine := ""
	if m.flash != "" {
		flashLine = lipgloss.NewStyle().Foreground(colorBrand).Width(ContentWidth).MarginTop(1).Render("· " + m.flash)
	}

	footer := renderFeedFooter(m.pendingDeleteIdx >= 0, m.cursor)

	return frameStyle.Render(
		lipgloss.JoinVertical(lipgloss.Left, header, body, flashLine, footer),
	)
}

func renderFeedHeader(channel string, sort store.SortMode, count int) string {
	loc := "#" + channel
	if channel == "" {
		loc = "all"
	}
	left := brandText.Render("voss") + textMute.Render(" / ") +
		brandText.Render("ask") + textMute.Render(" / ") +
		catBadgeOn.Render(loc)
	right := textMute.Render(fmt.Sprintf("%d posts · %s", count, sortLabel(sort)))
	gap := ContentWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return headerStyle.Render(left + strings.Repeat(" ", gap) + right)
}

func sortLabel(s store.SortMode) string {
	switch s {
	case store.SortNew:
		return "new"
	case store.SortTop:
		return "top"
	case store.SortHot:
		fallthrough
	default:
		return "hot"
	}
}

func renderEmptyFeed() string {
	mark := chainMark()
	title := textBody.Render("no posts yet.")
	cta := textDim.Render("press ") + keyChip.Render("n") + textDim.Render(" to write the first one.")
	block := lipgloss.JoinVertical(lipgloss.Center, mark, "", title, cta)
	return lipgloss.NewStyle().
		Width(ContentWidth).
		Align(lipgloss.Center).
		PaddingTop(2).
		PaddingBottom(2).
		Render(block)
}

// anonID is the public-facing pseudonym for a user. Stable per user_id.
func anonID(userID int64) string {
	return fmt.Sprintf("anony-%04d", userID)
}

func renderPostRow(idx int, p store.Post, selected, isMe, pendingDelete, showChannel bool) string {
	num := postNum.Render(fmt.Sprintf("[%02d]", idx+1))
	title := lipgloss.NewStyle().Foreground(colorText).Bold(true).Render(truncateRunes(p.Title, ContentWidth-30))

	score := renderScore(p.Score, p.MyVote)

	titleLine := lipgloss.JoinHorizontal(lipgloss.Left, num, " ", title)
	gap := ContentWidth - lipgloss.Width(titleLine) - lipgloss.Width(score) - 2
	if gap < 1 {
		gap = 1
	}
	headRow := titleLine + strings.Repeat(" ", gap) + score

	var metaParts []string
	if showChannel {
		metaParts = append(metaParts, catBadgeOff.Render("#"+p.Channel))
	}
	if isMe {
		metaParts = append(metaParts, lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Render(anonID(p.UserID))+textDim.Render(" (you)"))
	} else {
		metaParts = append(metaParts, textMute.Render(anonID(p.UserID)))
	}
	metaParts = append(metaParts, textMute.Render(relTime(p.CreatedAt)))
	metaParts = append(metaParts, textMute.Render(fmt.Sprintf("💬 %d", p.CommentCount)))
	metaLine := postBody.Render(strings.Join(metaParts, textMute.Render(" · ")))

	parts := []string{headRow, metaLine}
	if pendingDelete {
		parts = append(parts, "",
			lipgloss.NewStyle().Foreground(colorBrand).PaddingLeft(2).Render(
				"⚠ delete this post? press "+keyChip.Render("D")+" to confirm · any other key cancels.",
			),
		)
	}

	card := lipgloss.JoinVertical(lipgloss.Left, parts...)
	card = lipgloss.NewStyle().PaddingTop(1).PaddingBottom(0).Render(card)

	switch {
	case pendingDelete:
		card = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(colorBrandDeep).
			PaddingLeft(1).
			Render(card)
	case selected:
		card = postSelectedBar.Render(card)
	default:
		card = lipgloss.NewStyle().PaddingLeft(2).Render(card)
	}
	return card
}

// renderScore prints "▲ 12" / "▼ 3" / "· 0" with colour reflecting the user's own vote.
func renderScore(score, myVote int) string {
	icon := "·"
	col := colorTextMute
	switch myVote {
	case 1:
		icon = "▲"
		col = colorBrand
	case -1:
		icon = "▼"
		col = colorBrandDeep
	default:
		if score > 0 {
			icon = "▲"
		} else if score < 0 {
			icon = "▼"
		}
	}
	return lipgloss.NewStyle().Foreground(col).Render(fmt.Sprintf("%s %d", icon, score))
}

func renderFeedFooter(pendingDelete bool, cursor int) string {
	if pendingDelete {
		row := lipgloss.NewStyle().Foreground(colorBrand).Render(
			fmt.Sprintf("delete post [%02d]? ", cursor+1),
		) +
			renderKey("D", "confirm") + renderKeySep() +
			renderKey("esc", "cancel")
		return footerStyle.Render(
			lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(row),
		)
	}
	row := strings.Join([]string{
		renderKey("↑↓", "scroll"),
		renderKey("⏎", "open"),
		renderKey("n", "new"),
		renderKey("v", "▲"),
		renderKey("V", "▼"),
		renderKey("f", "sort"),
		renderKey("c", "channels"),
		renderKey("d", "del"),
		renderKey("q", "quit"),
	}, renderKeySep())
	return footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(row),
	)
}

// === helpers (shared with detail.go) ==================================

func visiblePostsForHeight(termHeight int) int {
	const perCard = 4
	const reserved = 10
	if termHeight <= 0 {
		return 5
	}
	n := (termHeight - reserved) / perCard
	switch {
	case n < 3:
		return 3
	case n > 14:
		return 14
	default:
		return n
	}
}

func windowAround(cursor, total, size int) (int, int) {
	if size >= total {
		return 0, total
	}
	half := size / 2
	start := cursor - half
	if start < 0 {
		start = 0
	}
	end := start + size
	if end > total {
		end = total
		start = end - size
	}
	return start, end
}

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func wrap(s string, width int) string {
	if width < 8 {
		return s
	}
	var out strings.Builder
	for li, line := range strings.Split(s, "\n") {
		if li > 0 {
			out.WriteByte('\n')
		}
		col := 0
		for wi, word := range strings.Fields(line) {
			wlen := lipgloss.Width(word)
			switch {
			case wi == 0:
				out.WriteString(word)
				col = wlen
			case col+1+wlen > width:
				out.WriteByte('\n')
				out.WriteString(word)
				col = wlen
			default:
				out.WriteByte(' ')
				out.WriteString(word)
				col += 1 + wlen
			}
		}
	}
	return out.String()
}
