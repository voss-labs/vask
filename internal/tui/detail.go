package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voss-labs/vask/internal/ratelimit"
	"github.com/voss-labs/vask/internal/store"
)

const maxReplyChars = 800

// treeNode pairs a comment with its visual indent depth.
type treeNode struct {
	Comment store.Comment
	Depth   int
}

// detailModel renders a single post + its threaded comments. Owns:
//   - the comment tree (DFS order with depth)
//   - cursor (0 = post; 1..N = comments[cursor-1] in tree order)
//   - reply modal sub-state
//   - vote / delete actions on the focused thing (post or comment)
//   - subtree-collapse state and a help overlay
type detailModel struct {
	st      *store.Store
	user    *store.User
	postID  int64
	limiter *ratelimit.CommentLimiter

	post             *store.Post
	comments         []store.Comment
	fullTree         []treeNode     // every node, regardless of collapse
	tree             []treeNode     // visible — collapsed subtrees stripped out
	hidden           map[int64]int  // commentID -> descendants hidden under it (for [+N] stub)
	collapse         map[int64]bool // commentIDs whose subtree is hidden right now
	autoFoldApplied  bool           // ensures we only auto-fold once per detail mount
	bodyExpanded     bool           // user toggled space on cursor=0 to see full body
	cursor           int
	width            int
	height           int
	err              error

	// reply modal
	replying  bool
	replyTo   *int64 // nil = reply to post
	replyArea textarea.Model
	sending   bool

	// delete confirmation
	pendingDeleteCursor int // -1 = none

	overlay detailOverlay

	flash string
}

type detailOverlay int

const (
	detailOverlayNone detailOverlay = iota
	detailOverlayHelp
	detailOverlayInfo
)

// === messages ============================================================

type detailLoadedMsg struct {
	post     *store.Post
	comments []store.Comment
	err      error
}

type detailVoteMsg struct {
	isComment bool
	targetID  int64
	newScore  int
	newVote   int
	err       error
}

type detailReplyPostedMsg struct {
	id  int64
	err error
}

type detailDeleteMsg struct {
	isComment bool
	targetID  int64
	err       error
}

// === ctor ================================================================

func newDetail(st *store.Store, user *store.User, postID int64) detailModel {
	ta := textarea.New()
	ta.Placeholder = "write your reply"
	ta.SetWidth(ContentWidth - 4)
	ta.SetHeight(5)
	ta.CharLimit = maxReplyChars
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Base = lipgloss.NewStyle()
	ta.BlurredStyle.Base = lipgloss.NewStyle()
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(colorBrand)

	return detailModel{
		st:                  st,
		user:                user,
		postID:              postID,
		limiter:             ratelimit.NewCommentLimiter(st, 30),
		replyArea:           ta,
		pendingDeleteCursor: -1,
		collapse:            map[int64]bool{},
	}
}

func (m detailModel) Init() tea.Cmd {
	return tea.Batch(
		loadDetail(m.st, m.postID, m.user.ID),
		markSeen(m.st, m.user.ID, m.postID),
	)
}

// === commands ============================================================

// loadDetail fetches the post + its comments in parallel. The two queries
// don't depend on each other, so running them serially adds 1 round-trip
// of latency to every detail open for no reason. We could use errgroup
// but a plain sync.WaitGroup is fewer imports for the same result, and
// we don't need cancellation-on-first-error semantics — both queries
// always run to completion within the same context deadline.
func loadDetail(st *store.Store, postID, userID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var (
			post     *store.Post
			comments []store.Comment
			postErr  error
			commErr  error
			wg       sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			post, postErr = st.GetPost(ctx, postID, userID)
		}()
		go func() {
			defer wg.Done()
			comments, commErr = st.ListComments(ctx, postID, userID)
		}()
		wg.Wait()

		// Surface the post error first — if the post is gone, comments don't
		// matter; if comments failed but the post loaded, we still want the
		// detail view to render the post and surface the comment error.
		err := postErr
		if err == nil {
			err = commErr
		}
		return detailLoadedMsg{post: post, comments: comments, err: err}
	}
}

func markSeen(st *store.Store, userID, postID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = st.MarkPostSeen(ctx, userID, postID)
		return nil
	}
}

func voteCmd(st *store.Store, userID int64, isComment bool, targetID int64, value int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var score int
		var err error
		if isComment {
			score, err = st.SetCommentVote(ctx, userID, targetID, value)
		} else {
			score, err = st.SetPostVote(ctx, userID, targetID, value)
		}
		return detailVoteMsg{isComment: isComment, targetID: targetID, newScore: score, newVote: value, err: err}
	}
}

func postReplyCmd(st *store.Store, userID, postID int64, parentCommentID *int64, body string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		id, err := st.CreateComment(ctx, userID, postID, parentCommentID, body)
		return detailReplyPostedMsg{id: id, err: err}
	}
}

func deleteCmd(st *store.Store, userID int64, isComment bool, targetID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		if isComment {
			err = st.DeleteOwnComment(ctx, userID, targetID)
		} else {
			err = st.DeleteOwnPost(ctx, userID, targetID)
		}
		return detailDeleteMsg{isComment: isComment, targetID: targetID, err: err}
	}
}

// === update ==============================================================

func (m detailModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.replyArea.SetWidth(ContentWidth - 4)

	case detailLoadedMsg:
		m.post = msg.post
		m.comments = msg.comments
		m.fullTree = buildCommentTree(m.comments)
		// Auto-fold deep replies on first mount only — subsequent reloads
		// (after a reply / vote / delete) preserve the user's expand state.
		if !m.autoFoldApplied {
			m.applyAutoFold()
			m.autoFoldApplied = true
		}
		m.recomputeVisibleTree()
		m.err = msg.err
		if m.cursor > len(m.tree) {
			m.cursor = 0
		}

	case detailVoteMsg:
		if errors.Is(msg.err, store.ErrSelfVote) {
			label := "post"
			if msg.isComment {
				label = "comment"
			}
			m.flash = "you can't vote on your own " + label + "."
			return m, nil
		}
		if msg.err != nil {
			m.flash = "couldn't vote: " + msg.err.Error()
			return m, nil
		}
		if msg.isComment {
			for i := range m.comments {
				if m.comments[i].ID == msg.targetID {
					m.comments[i].Score = msg.newScore
					m.comments[i].MyVote = msg.newVote
				}
			}
			for i := range m.fullTree {
				if m.fullTree[i].Comment.ID == msg.targetID {
					m.fullTree[i].Comment.Score = msg.newScore
					m.fullTree[i].Comment.MyVote = msg.newVote
				}
			}
			for i := range m.tree {
				if m.tree[i].Comment.ID == msg.targetID {
					m.tree[i].Comment.Score = msg.newScore
					m.tree[i].Comment.MyVote = msg.newVote
				}
			}
		} else if m.post != nil {
			m.post.Score = msg.newScore
			m.post.MyVote = msg.newVote
		}

	case detailReplyPostedMsg:
		m.sending = false
		if msg.err != nil {
			m.flash = "couldn't reply: " + msg.err.Error()
			return m, nil
		}
		m.replying = false
		m.replyTo = nil
		m.replyArea.Reset()
		m.flash = "reply posted."
		return m, loadDetail(m.st, m.postID, m.user.ID)

	case detailDeleteMsg:
		m.pendingDeleteCursor = -1
		if msg.err != nil {
			m.flash = "couldn't delete: " + msg.err.Error()
			return m, nil
		}
		if !msg.isComment {
			// post is gone; back to feed
			return m, func() tea.Msg { return closeDetailMsg{} }
		}
		m.flash = "comment deleted."
		return m, loadDetail(m.st, m.postID, m.user.ID)

	case tea.KeyMsg:
		if m.replying {
			return m.handleReplyKey(msg)
		}
		return m.handleMainKey(msg)
	}

	if m.replying {
		var cmd tea.Cmd
		m.replyArea, cmd = m.replyArea.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m detailModel) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// any open overlay swallows main keys until closed
	if m.overlay == detailOverlayHelp {
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "?", "esc", "q":
			m.overlay = detailOverlayNone
		}
		return m, nil
	}
	if m.overlay == detailOverlayInfo {
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "i", "esc", "q":
			m.overlay = detailOverlayNone
		}
		return m, nil
	}

	// any non-D key while pending delete clears the pending state
	if m.pendingDeleteCursor >= 0 && key != "d" && key != "D" {
		m.pendingDeleteCursor = -1
		m.flash = ""
	}

	maxIdx := len(m.tree) // cursor 0 = post, 1..len(tree) = comments

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.overlay = detailOverlayHelp
		return m, nil
	case "i":
		m.overlay = detailOverlayInfo
		return m, nil
	case " ":
		// Toggle full-body view when the user is focused on the post.
		// Long bodies (10+ lines after wrap) collapse to ~10 lines with a
		// "↓ N more lines" stub — this expands them inline. No-op when
		// cursor is on a comment.
		if m.cursor == 0 {
			m.bodyExpanded = !m.bodyExpanded
		}
		return m, nil
	case "c":
		// toggle collapse on the focused comment's subtree (no-op on post)
		if m.cursor == 0 || m.cursor > len(m.tree) {
			return m, nil
		}
		id := m.tree[m.cursor-1].Comment.ID
		if m.collapse[id] {
			delete(m.collapse, id)
		} else {
			m.collapse[id] = true
		}
		// keep the collapsed parent under the cursor — its index doesn't
		// change, but its siblings/ancestors might shift, so we re-find it.
		m.recomputeVisibleTree()
		for i, n := range m.tree {
			if n.Comment.ID == id {
				m.cursor = i + 1
				break
			}
		}
		if m.cursor > len(m.tree) {
			m.cursor = len(m.tree)
		}
		return m, nil
	case "b", "esc":
		return m, func() tea.Msg { return closeDetailMsg{} }
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < maxIdx {
			m.cursor++
		}
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = maxIdx
	case "r":
		// open reply modal scoped to focused thing
		if m.post == nil {
			return m, nil
		}
		var target *int64
		if m.cursor > 0 && m.cursor <= len(m.tree) {
			id := m.tree[m.cursor-1].Comment.ID
			target = &id
		}
		m.replying = true
		m.replyTo = target
		m.replyArea.Reset()
		return m, m.replyArea.Focus()
	case "v":
		return m.applyVote(+1)
	case "V":
		return m.applyVote(-1)
	case "d":
		// arm delete on focused thing IF it's mine
		isPost, _, _, authorID := m.focusedTarget()
		if authorID != m.user.ID {
			label := "comment"
			if isPost {
				label = "post"
			}
			m.flash = "you can only delete your own " + label + "."
			m.pendingDeleteCursor = -1
			return m, nil
		}
		m.pendingDeleteCursor = m.cursor
		m.flash = ""
		return m, nil
	case "D":
		if m.pendingDeleteCursor < 0 || m.pendingDeleteCursor != m.cursor {
			return m, nil
		}
		isPost, postID, commentID, authorID := m.focusedTarget()
		if authorID != m.user.ID {
			m.pendingDeleteCursor = -1
			return m, nil
		}
		if isPost {
			return m, deleteCmd(m.st, m.user.ID, false, postID)
		}
		return m, deleteCmd(m.st, m.user.ID, true, commentID)
	}
	return m, nil
}

func (m detailModel) handleReplyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sending {
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.replying = false
		m.replyTo = nil
		m.replyArea.Reset()
		return m, nil
	case "ctrl+s":
		body := strings.TrimSpace(m.replyArea.Value())
		if body == "" {
			m.flash = "reply is empty."
			return m, nil
		}
		if len(body) > maxReplyChars {
			m.flash = fmt.Sprintf("reply too long (%d / %d).", len(body), maxReplyChars)
			return m, nil
		}
		// Comment-rate check. Sync because the user is paused on the
		// reply screen anyway and the count query is cheap.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		allowed, remaining, _, err := m.limiter.Allow(ctx, m.user.ID)
		cancel()
		if err != nil {
			m.flash = "rate-limit check failed: " + err.Error()
			return m, nil
		}
		if !allowed {
			m.flash = fmt.Sprintf("daily comment limit reached. remaining: %d", remaining)
			return m, nil
		}
		m.sending = true
		m.flash = "sending…"
		return m, postReplyCmd(m.st, m.user.ID, m.postID, m.replyTo, body)
	}
	var cmd tea.Cmd
	m.replyArea, cmd = m.replyArea.Update(msg)
	return m, cmd
}

func (m detailModel) applyVote(delta int) (tea.Model, tea.Cmd) {
	isPost, postID, commentID, _ := m.focusedTarget()
	if isPost {
		if m.post == nil {
			return m, nil
		}
		target := delta
		if m.post.MyVote == delta {
			target = 0
		}
		return m, voteCmd(m.st, m.user.ID, false, postID, target)
	}
	c := m.tree[m.cursor-1].Comment
	target := delta
	if c.MyVote == delta {
		target = 0
	}
	return m, voteCmd(m.st, m.user.ID, true, commentID, target)
}

// focusedTarget returns details of whatever the cursor is on right now.
// authorID is 0 if nothing is loaded yet.
func (m detailModel) focusedTarget() (isPost bool, postID, commentID, authorID int64) {
	if m.post == nil {
		return true, 0, 0, 0
	}
	if m.cursor == 0 || m.cursor > len(m.tree) {
		return true, m.post.ID, 0, m.post.UserID
	}
	c := m.tree[m.cursor-1].Comment
	return false, c.PostID, c.ID, c.UserID
}

// === view ===============================================================

func (m detailModel) View() string {
	if m.replying {
		return m.viewReply()
	}
	if m.overlay == detailOverlayHelp {
		return frameStyle.Render(renderDetailHelp())
	}
	if m.overlay == detailOverlayInfo {
		return frameStyle.Render(renderFeedInfo())
	}
	return m.viewMain()
}

func (m detailModel) viewMain() string {
	header := renderDetailHeader(m.post)

	var body string
	switch {
	case m.err != nil:
		body = textErr.Render("error loading post: " + m.err.Error())
	case m.post == nil:
		body = textDim.Render("loading…")
	default:
		body = m.renderPostAndComments()
	}

	flashLine := ""
	if m.flash != "" {
		flashLine = lipgloss.NewStyle().Foreground(colorBrand).Width(ContentWidth).MarginTop(1).Render("· " + m.flash)
	}

	footer := renderDetailFooter(m.pendingDeleteCursor >= 0)

	return frameStyle.Render(
		lipgloss.JoinVertical(lipgloss.Left, header, body, flashLine, footer),
	)
}

func (m detailModel) viewReply() string {
	header := renderDetailHeader(m.post)

	// context preview — what we're replying to
	var ctxLabel, ctxPreview string
	if m.replyTo == nil {
		ctxLabel = textDim.Render("replying to post by " + authorChip(m.post.Username, m.post.UserID, m.user.ID))
		ctxPreview = postHint.Render("> " + truncateRunes(m.post.Title, ContentWidth-8))
	} else {
		var tc store.Comment
		for _, c := range m.comments {
			if c.ID == *m.replyTo {
				tc = c
				break
			}
		}
		ctxLabel = textDim.Render("replying to " + authorChip(tc.Username, tc.UserID, m.user.ID))
		first := strings.SplitN(tc.Body, "\n", 2)[0]
		ctxPreview = postHint.Render("> " + truncateRunes(first, ContentWidth-8))
	}

	box := formBoxFocused.Render(m.replyArea.View())
	counter := lipgloss.NewStyle().
		Width(ContentWidth).Align(lipgloss.Right).Foreground(colorTextMute).
		Render(fmt.Sprintf("%d / %d", len(m.replyArea.Value()), maxReplyChars))

	flashLine := ""
	if m.flash != "" {
		col := colorBrand
		if m.flash != "sending…" {
			col = colorBrandDeep
		}
		flashLine = lipgloss.NewStyle().Foreground(col).Width(ContentWidth).MarginTop(1).Render("· " + m.flash)
	}

	footer := footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(
			renderKey("ctrl+s", "send")+renderKeySep()+renderKey("esc", "cancel"),
		),
	)

	return frameStyle.Render(
		lipgloss.JoinVertical(lipgloss.Left,
			header,
			"",
			ctxLabel,
			ctxPreview,
			"",
			box,
			counter,
			flashLine,
			footer,
		),
	)
}

func renderDetailHeader(p *store.Post) string {
	left := brandText.Render("voss") + textMute.Render(" / ") + brandText.Render("vask")
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

func (m detailModel) renderPostAndComments() string {
	post := m.renderPostBlock(m.cursor == 0, m.pendingDeleteCursor == 0)
	commentsHeader := lipgloss.NewStyle().
		Foreground(colorTextDim).
		MarginTop(1).
		MarginBottom(1).
		Render(fmt.Sprintf("─── %d comments ───", len(m.comments)))

	var commentRows []string
	if len(m.tree) == 0 {
		commentRows = append(commentRows, lipgloss.NewStyle().
			Foreground(colorTextMute).
			Italic(true).
			PaddingLeft(2).
			Render("no comments yet · press r to leave the first one."),
		)
	} else {
		for i, n := range m.tree {
			cursorIdx := i + 1
			commentRows = append(commentRows, m.renderCommentRow(
				n.Comment, n.Depth,
				cursorIdx == m.cursor,
				cursorIdx == m.pendingDeleteCursor,
			))
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		post, commentsHeader, strings.Join(commentRows, "\n"),
	)
}

// renderPostBlock renders the post itself (focusable as cursor=0).
//
// Selection in detail used to share the same orange left bar as the feed
// list cursor, which made the post block look "selected by default" even
// when the user was focused on a comment. We drop the bar entirely here
// and instead accent the *title* with brand colour when cursor=0 — the
// post is always present, so the discriminator is "is the title hot?",
// not "is there a bar?". Comment rows keep the orange bar; the two cues
// no longer collide.
func (m detailModel) renderPostBlock(selected, pendingDelete bool) string {
	p := m.post
	titleColor := colorText
	if selected {
		titleColor = colorBrand
	}

	// === row 1: title (left) + tags + score (right) ==================
	score := renderScore(p.Score, p.MyVote)
	tagsInline := renderTagChipsInline(p.Tags)
	rightSide := score
	if tagsInline != "" {
		rightSide = tagsInline + "   " + score
	}
	titleBudget := ContentWidth - 4 - lipgloss.Width(rightSide) - 2
	if titleBudget < 16 {
		titleBudget = 16
	}
	title := lipgloss.NewStyle().Foreground(titleColor).Bold(true).
		Render(truncateRunes(p.Title, titleBudget))
	titleGap := ContentWidth - 4 - lipgloss.Width(title) - lipgloss.Width(rightSide)
	if titleGap < 1 {
		titleGap = 1
	}
	titleRow := title + strings.Repeat(" ", titleGap) + rightSide

	// === row 2: byline, right-aligned ================================
	// Mirror the feed's row-2 layout exactly: meta lives on the right
	// edge so the eye lands at the same place across views. Same single
	// " · " separator visual; no leading dots, no inline score (score
	// already lives on row 1).
	sep := textMute.Render(" · ")
	metaInline := authorChip(p.Username, p.UserID, m.user.ID) +
		sep + textMute.Render(relTime(p.CreatedAt)) +
		sep + textMute.Render(fmt.Sprintf("↳ %d", p.CommentCount))
	metaRow := lipgloss.NewStyle().
		Width(ContentWidth - 4).
		Align(lipgloss.Right).
		Render(metaInline)

	rule := lipgloss.NewStyle().Foreground(colorBorder).Render(strings.Repeat("─", ContentWidth-4))

	// Collapse very long bodies so that comments below stay reachable on
	// shorter terminals. Threshold of 10 lines covers ~700 chars of text;
	// bodies up to that render fully, longer ones are truncated with an
	// inline expand affordance. Cursor-on-post + space toggles the state.
	const collapseThreshold = 10
	bodyText := wrap(p.Body, ContentWidth-4)
	bodyLines := strings.Split(bodyText, "\n")
	bodyRendered := bodyText
	if len(bodyLines) > collapseThreshold && !m.bodyExpanded {
		shown := strings.Join(bodyLines[:collapseThreshold], "\n")
		moreLine := lipgloss.NewStyle().Foreground(colorTextDim).Italic(true).
			Render(fmt.Sprintf("↓ %d more lines · press ", len(bodyLines)-collapseThreshold)) +
			keyChip.Render("space") +
			lipgloss.NewStyle().Foreground(colorTextDim).Italic(true).Render(" to expand")
		bodyRendered = shown + "\n\n" + moreLine
	} else if len(bodyLines) > collapseThreshold && m.bodyExpanded {
		bodyRendered = bodyText + "\n\n" +
			lipgloss.NewStyle().Foreground(colorTextDim).Italic(true).
				Render("↑ press ") +
			keyChip.Render("space") +
			lipgloss.NewStyle().Foreground(colorTextDim).Italic(true).Render(" to collapse")
	}
	body := postBody.PaddingLeft(0).Render(bodyRendered)

	parts := []string{titleRow, metaRow, rule, "", body}
	if pendingDelete {
		parts = append(parts, "",
			lipgloss.NewStyle().Foreground(colorBrand).Render(
				"⚠ delete this post? press "+keyChip.Render("D")+" to confirm.",
			),
		)
	}

	block := lipgloss.JoinVertical(lipgloss.Left, parts...)
	block = lipgloss.NewStyle().PaddingTop(0).PaddingBottom(0).Render(block)

	if pendingDelete {
		return lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(colorBrandDeep).
			PaddingLeft(1).
			Render(block)
	}
	return lipgloss.NewStyle().PaddingLeft(2).Render(block)
}

func (m detailModel) renderCommentRow(c store.Comment, depth int, selected, pendingDelete bool) string {
	indent := strings.Repeat("  ", clampInt(depth, 0, 6))
	arrow := lipgloss.NewStyle().Foreground(colorTextMute).Render("↳ ")

	author := authorChip(c.Username, c.UserID, m.user.ID)
	if m.post != nil && c.UserID == m.post.UserID {
		author += " " + lipgloss.NewStyle().Foreground(colorBrand).Render("· OP")
	}
	metaPieces := []string{
		author,
		textMute.Render(relTime(c.CreatedAt)),
		renderScore(c.Score, c.MyVote),
	}
	// "[+N hidden]" badge if this comment's subtree is collapsed; clicking
	// `c` again expands it.
	if hidden := m.hidden[c.ID]; hidden > 0 {
		metaPieces = append(metaPieces,
			lipgloss.NewStyle().Foreground(colorBrand).Render(fmt.Sprintf("[+%d hidden]", hidden)))
	}
	meta := strings.Join(metaPieces, textMute.Render("  ·  "))

	bodyText := wrap(c.Body, ContentWidth-4-lipgloss.Width(indent)-2)
	body := lipgloss.NewStyle().Foreground(colorText).Render(bodyText)

	parts := []string{indent + arrow + meta}
	for _, line := range strings.Split(body, "\n") {
		parts = append(parts, indent+"  "+line)
	}
	if pendingDelete {
		parts = append(parts,
			indent+"  "+lipgloss.NewStyle().Foreground(colorBrand).Render(
				"⚠ delete this comment? press "+keyChip.Render("D")+" to confirm.",
			),
		)
	}

	block := strings.Join(parts, "\n")

	switch {
	case pendingDelete:
		return lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(colorBrandDeep).
			PaddingLeft(1).
			Render(block)
	case selected:
		return postSelectedBar.Render(block)
	default:
		return lipgloss.NewStyle().PaddingLeft(2).Render(block)
	}
}

func renderDetailFooter(pendingDelete bool) string {
	if pendingDelete {
		row := lipgloss.NewStyle().Foreground(colorBrand).Render("delete focused item? ") +
			renderKey("D", "confirm") + renderKeySep() +
			renderKey("esc", "cancel")
		return footerStyle.Render(
			lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(row),
		)
	}
	row := strings.Join([]string{
		renderKey("↑↓", "scroll"),
		renderKey("r", "reply"),
		renderKey("v", "▲"),
		renderKey("V", "▼"),
		renderKey("c", "fold"),
		renderKey("?", "help"),
		renderKey("b/esc", "back"),
	}, renderKeySep())
	return footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(row),
	)
}

// === helpers =============================================================

// authorChip renders an author label for a post or comment row. Own
// content gets brand-orange bold; everyone else gets the mute palette.
// That colour split is enough to spot your own contributions in a
// thread — no need for an identicon glyph or a "(you)" suffix doubling
// the same signal in different forms.
func authorChip(authorUsername string, authorID, currentUserID int64) string {
	name := displayName(authorUsername, authorID)
	if authorID == currentUserID {
		return lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Render(name)
	}
	return textMute.Render(name)
}

// autoFoldDepth — at depths >= autoFoldDepth-1 we pre-fold any node that
// has children, so visual indentation never marches off the right edge of
// the 78-col frame. Users can `c` on a folded parent to expand it.
const autoFoldDepth = 4

// applyAutoFold seeds m.collapse with depth-3 (and deeper) nodes that
// have descendants. Idempotent in the sense that a node already user-
// folded stays folded; nodes the user explicitly expanded earlier in
// the session get re-folded on a fresh mount, which matches what users
// expect when they re-enter a post.
func (m *detailModel) applyAutoFold() {
	if m.collapse == nil {
		m.collapse = map[int64]bool{}
	}
	hasChildren := map[int64]bool{}
	for _, n := range m.fullTree {
		if n.Comment.ParentCommentID != nil {
			hasChildren[*n.Comment.ParentCommentID] = true
		}
	}
	for _, n := range m.fullTree {
		if n.Depth >= autoFoldDepth-1 && hasChildren[n.Comment.ID] {
			m.collapse[n.Comment.ID] = true
		}
	}
}

// recomputeVisibleTree filters m.fullTree by m.collapse, writing the
// visible nodes to m.tree and the per-collapsed-parent hidden counts to
// m.hidden. Algorithm: walk fullTree (already in DFS order with depth);
// when we hit a collapsed node we keep the node itself, then skip every
// subsequent node whose depth is greater (those are descendants).
func (m *detailModel) recomputeVisibleTree() {
	visible := make([]treeNode, 0, len(m.fullTree))
	hidden := map[int64]int{}
	skipDepth := -1
	var skipParent int64
	for _, n := range m.fullTree {
		if skipDepth >= 0 {
			if n.Depth > skipDepth {
				hidden[skipParent]++
				continue
			}
			skipDepth = -1
		}
		visible = append(visible, n)
		if m.collapse[n.Comment.ID] {
			skipDepth = n.Depth
			skipParent = n.Comment.ID
		}
	}
	m.tree = visible
	m.hidden = hidden
}

// buildCommentTree returns comments in DFS order with their visual depth.
// Top-level (parent_comment_id = NULL) at depth 0; replies +1 per level.
func buildCommentTree(comments []store.Comment) []treeNode {
	if len(comments) == 0 {
		return nil
	}
	childrenOf := map[int64][]store.Comment{}
	rootKey := int64(-1)
	for _, c := range comments {
		key := rootKey
		if c.ParentCommentID != nil {
			key = *c.ParentCommentID
		}
		childrenOf[key] = append(childrenOf[key], c)
	}

	var out []treeNode
	var visit func(c store.Comment, depth int)
	visit = func(c store.Comment, depth int) {
		out = append(out, treeNode{Comment: c, Depth: depth})
		for _, child := range childrenOf[c.ID] {
			visit(child, depth+1)
		}
	}
	for _, root := range childrenOf[rootKey] {
		visit(root, 0)
	}
	// any orphans (e.g. parent was deleted but parent_comment_id wasn't NULLed) — render at depth 0
	seen := map[int64]bool{}
	for _, n := range out {
		seen[n.Comment.ID] = true
	}
	for _, c := range comments {
		if !seen[c.ID] {
			visit(c, 0)
		}
	}
	return out
}

func renderDetailHelp() string {
	title := brandText.Render("post · keybinds")
	tagline := textDim.Render("press ? or esc to close")
	rule := lipgloss.NewStyle().Foreground(colorBorder).Render(strings.Repeat("─", ContentWidth-4))

	groups := []struct {
		head string
		keys [][2]string
	}{
		{"navigate", [][2]string{
			{"↑↓ / j k", "move cursor (post + comments)"},
			{"g / G", "jump to top / bottom"},
			{"b / esc", "back to feed"},
		}},
		{"thread", [][2]string{
			{"r", "reply to focused thing"},
			{"c", "collapse / expand subtree"},
		}},
		{"explore", [][2]string{
			{"space", "expand long body (cursor on post)"},
		}},
		{"vote & delete", [][2]string{
			{"v / V", "upvote / downvote"},
			{"d  D", "arm / confirm delete (own)"},
		}},
		{"reply modal", [][2]string{
			{"ctrl+s", "send"},
			{"esc", "cancel"},
		}},
		{"about", [][2]string{
			{"i", "what is voss / vask?"},
		}},
		{"session", [][2]string{
			{"q / ctrl+c", "quit"},
		}},
	}

	keyCol := lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Width(18)
	descCol := lipgloss.NewStyle().Foreground(colorText)
	headStyle := lipgloss.NewStyle().Foreground(colorTextDim).Italic(true).MarginTop(1)

	var blocks []string
	for _, g := range groups {
		blocks = append(blocks, headStyle.Render(g.head))
		for _, k := range g.keys {
			blocks = append(blocks, keyCol.Render(k[0])+descCol.Render(k[1]))
		}
	}

	body := lipgloss.JoinVertical(lipgloss.Left, blocks...)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(title),
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(tagline),
		"",
		rule,
		body,
		"",
		footerStyle.Render(
			lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(
				renderKey("?", "close")+renderKeySep()+renderKey("esc", "close"),
			),
		),
	)
}

func clampInt(n, min, max int) int {
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
