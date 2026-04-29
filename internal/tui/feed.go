package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voss-labs/vask/internal/store"
)

// feedModel is the unified post-list view (all posts, mixed). Channels are
// gone from the UX — tags categorise posts now.
//
// On top of the basic list, this model owns three orthogonal concerns:
//
//  1. Filtering — tag, free-text query, mine-only — composed via
//     store.ListPostsParams; reloading is just rebuilding the params.
//  2. Overlays — help (?), search (/), and tag-picker (#). Mutually
//     exclusive; opening one closes any other. Esc closes the active one.
//  3. Texture — body preview, fresh dot, vote-velocity arrow, identicon
//     glyph, "new since last visit" divider. All optional, all driven from
//     data already on store.Post.
type feedModel struct {
	st   *store.Store
	user *store.User

	// filters / sort
	sort        store.SortMode
	tagFilter   string
	searchQuery string
	mineOnly    bool

	// overlay sub-state
	overlay         feedOverlay
	searchInput     textinput.Model
	popularTags     []string
	activityPosts   []store.Post            // your last N authored posts
	activityComments []store.ActivityComment // your last N authored comments (with parent titles)

	// data
	posts            []store.Post
	total            int // total posts matching current filters (drives header + pagination)
	page             int // 0-indexed page; bumped via `]`/`[`
	cursor           int
	width, height    int
	err              error
	pendingDeleteIdx int
	flash            string

	// UX prefs
	compactMode bool // hide body preview when true; toggled by space

	// unread divider
	lastFeedAt        time.Time // snapshot from GetAndBumpLastFeedAt at first load
	lastFeedAtSnapped bool      // ensures we only snap once per feed-mount
}

type feedOverlay int

const (
	overlayNone feedOverlay = iota
	overlayHelp
	overlaySearch
	overlayTagPicker
	overlayInfo
	overlayActivity
)

// pageSize is the post-fetch limit per page. 100 covers nearly every
// session for a campus-scale forum without paying for fetching more than
// the user will scroll. `]` / `[` step through pages of this size.
const pageSize = 100

// === messages ============================================================

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

type lastFeedAtMsg struct {
	at  time.Time
	err error
}

type popularTagsMsg struct {
	tags []string
}

type postsCountedMsg struct {
	total int
}

type activityLoadedMsg struct {
	posts    []store.Post
	comments []store.ActivityComment
}

// === ctor ================================================================

func newFeed(st *store.Store, user *store.User) feedModel {
	si := textinput.New()
	si.Placeholder = "type to search title + body, enter to apply, esc to cancel"
	si.Width = ContentWidth - 4
	si.Prompt = ""
	si.CharLimit = 80
	si.Cursor.Style = lipgloss.NewStyle().Foreground(colorBrand)

	return feedModel{
		st:               st,
		user:             user,
		sort:             store.SortHot,
		pendingDeleteIdx: -1,
		searchInput:      si,
	}
}

func (m feedModel) Init() tea.Cmd {
	return tea.Batch(
		loadPosts(m.st, m.user.ID, m.searchQuery, m.params()),
		countPosts(m.st, m.user.ID, m.params()),
		snapLastFeedAt(m.st, m.user.ID),
		loadPopularTags(m.st),
	)
}

func (m feedModel) params() store.ListPostsParams {
	return store.ListPostsParams{
		Tag:      m.tagFilter,
		Query:    m.searchQuery,
		MineOnly: m.mineOnly,
		Sort:     m.sort,
		Limit:    pageSize,
		Offset:   m.page * pageSize,
	}
}

// === commands ============================================================

// loadPosts is the unified feed-fetch command. When `query` is
// non-empty, results are routed through SearchPostsSemantic (cosine
// similarity over post embeddings, with silent LIKE fallback). When
// empty, it's the normal hot/new/top listing.
//
// Timeout is bumped from 5s to 8s for the search path because the
// semantic flow includes a Cloudflare round-trip to embed the query —
// usually 100–250ms but worth budgeting for.
func loadPosts(st *store.Store, userID int64, query string, params store.ListPostsParams) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var (
			posts []store.Post
			err   error
		)
		if query != "" {
			posts, err = st.SearchPostsSemantic(ctx, userID, query, params)
		} else {
			posts, err = st.ListPosts(ctx, userID, params)
		}
		return postsLoadedMsg{posts: posts, err: err}
	}
}

func snapLastFeedAt(st *store.Store, userID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		at, err := st.GetAndBumpLastFeedAt(ctx, userID)
		return lastFeedAtMsg{at: at, err: err}
	}
}

func countPosts(st *store.Store, userID int64, params store.ListPostsParams) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		n, _ := st.CountPosts(ctx, userID, params)
		return postsCountedMsg{total: n}
	}
}

// loadActivity fetches the user's last 5 posts and last 5 comments
// concurrently and returns them in one msg. Used by the `Y` overlay to
// give users a one-keystroke jump to anything they've recently been
// part of — replaces the "inbox" we deliberately don't have.
func loadActivity(st *store.Store, userID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		posts, _ := st.ListPosts(ctx, userID, store.ListPostsParams{
			MineOnly: true,
			Sort:     store.SortNew,
			Limit:    5,
		})
		comments, _ := st.MyRecentComments(ctx, userID, 5)
		return activityLoadedMsg{posts: posts, comments: comments}
	}
}

func loadPopularTags(st *store.Store) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		tags, _ := st.ListPopularTags(ctx, 9)
		return popularTagsMsg{tags: tags}
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

// === update ==============================================================

func (m feedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.searchInput.Width = ContentWidth - 4

	case postsLoadedMsg:
		m.posts = msg.posts
		m.err = msg.err
		if m.cursor >= len(m.posts) {
			m.cursor = 0
		}

	case lastFeedAtMsg:
		if !m.lastFeedAtSnapped && msg.err == nil {
			m.lastFeedAt = msg.at
			m.lastFeedAtSnapped = true
		}

	case popularTagsMsg:
		m.popularTags = msg.tags

	case postsCountedMsg:
		m.total = msg.total

	case activityLoadedMsg:
		m.activityPosts = msg.posts
		m.activityComments = msg.comments

	case feedRefreshMsg:
		// Fired by App.restoreFeedOrFresh when this model is restored
		// from a stash. Pull fresh data with the user's existing filters
		// so they see the latest state without losing their cursor.
		return m, tea.Batch(
			loadPosts(m.st, m.user.ID, m.searchQuery, m.params()),
			countPosts(m.st, m.user.ID, m.params()),
		)

	case voteAppliedMsg:
		if errors.Is(msg.err, store.ErrSelfVote) {
			m.flash = "you can't vote on your own post."
			return m, nil
		}
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
		return m, m.reloadAndCount()

	case tea.KeyMsg:
		// route keys to whichever overlay is active
		switch m.overlay {
		case overlaySearch:
			return m.handleSearchKey(msg)
		case overlayTagPicker:
			return m.handleTagPickerKey(msg)
		case overlayHelp:
			return m.handleHelpKey(msg)
		case overlayInfo:
			return m.handleInfoKey(msg)
		case overlayActivity:
			return m.handleActivityKey(msg)
		}
		return m.handleMainKey(msg)
	}
	return m, nil
}

func (m feedModel) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.pendingDeleteIdx >= 0 && key != "d" && key != "D" {
		m.pendingDeleteIdx = -1
		m.flash = ""
	}

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "?":
		m.overlay = overlayHelp
		return m, nil

	case "i":
		m.overlay = overlayInfo
		return m, nil

	case "Y":
		// "Your activity" overlay — recent authored posts + comments,
		// numbered 1–N for one-keystroke jump-to-thread navigation.
		m.overlay = overlayActivity
		return m, loadActivity(m.st, m.user.ID)

	case "/":
		m.overlay = overlaySearch
		m.searchInput.SetValue(m.searchQuery)
		m.searchInput.CursorEnd()
		return m, m.searchInput.Focus()

	case "#":
		m.overlay = overlayTagPicker
		return m, nil

	case "m":
		// toggle "only my posts" filter
		m.mineOnly = !m.mineOnly
		m.cursor = 0
		m.page = 0
		return m, m.reloadAndCount()

	case "esc", "b":
		// peel back one filter layer at a time in priority order:
		// search → tag → mine → page. Press repeatedly to keep stripping.
		// No-op when nothing is active so it can't accidentally do
		// anything disruptive on the unfiltered first-page feed.
		switch {
		case m.searchQuery != "":
			m.searchQuery = ""
			m.flash = "search cleared."
		case m.tagFilter != "":
			m.tagFilter = ""
			m.flash = "tag filter cleared."
		case m.mineOnly:
			m.mineOnly = false
			m.flash = "mine-only off."
		case m.page > 0:
			m.page = 0
			m.flash = "back to page 1."
		default:
			return m, nil
		}
		m.cursor = 0
		return m, m.reloadAndCount()

	case "x":
		// quick-clear all filters at once (vs esc which peels one at a time)
		if m.searchQuery == "" && m.tagFilter == "" && !m.mineOnly && m.page == 0 {
			return m, nil
		}
		m.tagFilter = ""
		m.searchQuery = ""
		m.mineOnly = false
		m.page = 0
		m.cursor = 0
		m.flash = "all filters cleared."
		return m, m.reloadAndCount()

	case " ":
		// toggle compact mode (hide/show body preview)
		m.compactMode = !m.compactMode

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

	case "]":
		// next page — only if there are more results to show
		if (m.page+1)*pageSize < m.total {
			m.page++
			m.cursor = 0
			return m, loadPosts(m.st, m.user.ID, m.searchQuery, m.params())
		}
		m.flash = "you're on the last page."
		return m, nil
	case "[":
		// previous page
		if m.page == 0 {
			m.flash = "you're on the first page."
			return m, nil
		}
		m.page--
		m.cursor = 0
		return m, loadPosts(m.st, m.user.ID, m.searchQuery, m.params())
	case "r":
		m.flash = ""
		return m, m.reloadAndCount()
	case "n":
		return m, func() tea.Msg { return openComposeMsg{} }
	case "f":
		m.sort = (m.sort + 1) % 3
		m.page = 0
		m.cursor = 0
		return m, m.reloadAndCount()
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
	return m, nil
}

func (m feedModel) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.overlay = overlayNone
		m.searchInput.Blur()
		return m, nil
	case "enter":
		newQuery := strings.TrimSpace(m.searchInput.Value())
		// Skip the round-trip when there's nothing to search and nothing
		// previously searched — esc-equivalent close without burning a
		// useless refetch.
		if newQuery == "" && m.searchQuery == "" {
			m.overlay = overlayNone
			m.searchInput.Blur()
			return m, nil
		}
		m.searchQuery = newQuery
		m.overlay = overlayNone
		m.searchInput.Blur()
		m.cursor = 0
		m.page = 0
		return m, m.reloadAndCount()
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	return m, cmd
}

func (m feedModel) handleTagPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "#":
		m.overlay = overlayNone
		return m, nil
	case "0":
		// 0 clears the active tag filter
		if m.tagFilter == "" {
			return m, nil
		}
		m.tagFilter = ""
		m.overlay = overlayNone
		m.cursor = 0
		m.page = 0
		return m, m.reloadAndCount()
	}
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		idx := int(key[0] - '1')
		if idx < len(m.popularTags) {
			m.tagFilter = m.popularTags[idx]
			m.overlay = overlayNone
			m.cursor = 0
			m.page = 0
			return m, m.reloadAndCount()
		}
	}
	return m, nil
}

func (m feedModel) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "?", "esc", "q":
		m.overlay = overlayNone
		return m, nil
	}
	return m, nil
}

func (m feedModel) handleInfoKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "i", "esc", "q":
		m.overlay = overlayNone
		return m, nil
	}
	return m, nil
}

func (m feedModel) handleActivityKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c":
		return m, tea.Quit
	case "Y", "esc", "q":
		m.overlay = overlayNone
		return m, nil
	}
	// Digits map to flat ordinal: posts first, then comments. Pressing the
	// digit jumps to the underlying post (for a comment, that's its parent).
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		idx := int(key[0] - '1')
		if idx < len(m.activityPosts) {
			id := m.activityPosts[idx].ID
			m.overlay = overlayNone
			return m, func() tea.Msg { return openDetailMsg{postID: id} }
		}
		idx -= len(m.activityPosts)
		if idx < len(m.activityComments) {
			id := m.activityComments[idx].Comment.PostID
			m.overlay = overlayNone
			return m, func() tea.Msg { return openDetailMsg{postID: id} }
		}
	}
	return m, nil
}

// reloadAndCount fires both a fresh page-load and a count refresh — used
// after any filter change so the "[1–N of TOTAL]" header matches the
// rows actually in view.
func (m feedModel) reloadAndCount() tea.Cmd {
	return tea.Batch(
		loadPosts(m.st, m.user.ID, m.searchQuery, m.params()),
		countPosts(m.st, m.user.ID, m.params()),
	)
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

// === view ===============================================================

func (m feedModel) View() string {
	switch m.overlay {
	case overlayHelp:
		return frameStyle.Render(renderFeedHelp())
	case overlayInfo:
		return frameStyle.Render(renderFeedInfo())
	case overlaySearch:
		return frameStyle.Render(m.viewSearchOverlay())
	case overlayTagPicker:
		return frameStyle.Render(m.viewTagPickerOverlay())
	case overlayActivity:
		return frameStyle.Render(m.viewActivityOverlay())
	}

	header := renderFeedHeader(m.user, m.sort, len(m.posts), m.total, m.page,
		m.tagFilter, m.searchQuery, m.mineOnly, m.compactMode)

	var body string
	switch {
	case m.err != nil:
		body = textErr.Render("error loading feed: " + m.err.Error())
	case len(m.posts) == 0:
		body = m.renderEmptyFeed()
	default:
		visible := visiblePostsForHeight(m.height)
		if !m.compactMode {
			// each card is taller when body preview is on, so we can show fewer
			visible = max(2, visible*2/3)
		}
		start, end := windowAround(m.cursor, len(m.posts), visible)

		var rows []string
		if start > 0 {
			rows = append(rows, textMute.Render(fmt.Sprintf("  ↑ %d more above", start)))
		}
		dividerInserted := false
		for i := start; i < end; i++ {
			// "── new since last visit ──" divider, dropped before the first
			// already-seen post when sort=new (the only sort where chrono
			// boundaries make sense). Suppressed for first-time visitors.
			if !dividerInserted && m.shouldShowUnreadDivider() &&
				m.posts[i].CreatedAt.Before(m.lastFeedAt) && i > 0 {
				rows = append(rows, renderUnreadDivider())
				dividerInserted = true
			}
			rows = append(rows, renderPostRow(
				i, m.posts[i],
				i == m.cursor,
				m.posts[i].UserID == m.user.ID,
				i == m.pendingDeleteIdx,
				m.compactMode,
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

func (m feedModel) shouldShowUnreadDivider() bool {
	if m.lastFeedAt.IsZero() {
		return false
	}
	if m.sort != store.SortNew {
		// chrono-ordered sort only — divider is meaningless when posts are
		// reordered by score
		return false
	}
	return true
}

func renderUnreadDivider() string {
	bar := lipgloss.NewStyle().Foreground(colorTextMute)
	label := lipgloss.NewStyle().Foreground(colorTextDim).Italic(true)
	left := bar.Render(strings.Repeat("─", 4))
	right := bar.Render(strings.Repeat("─", ContentWidth-4-2-len(" new since last visit ")-4))
	if right == "" {
		right = bar.Render("─")
	}
	return lipgloss.NewStyle().PaddingTop(1).PaddingBottom(0).PaddingLeft(2).Render(
		left + " " + label.Render("new since last visit") + " " + right,
	)
}

// === header ==============================================================

func renderFeedHeader(user *store.User, sort store.SortMode, visible, total, page int, tag, search string, mineOnly, compact bool) string {
	left := brandText.Render("voss") + textMute.Render(" / ") + brandText.Render("vask")

	// User chip — just the handle in brand orange. Top-right of the
	// header is unambiguously the viewer's identity slot; explicit "you"
	// label was redundant noise next to a brand-orange username.
	youChip := ""
	if user.Username != "" {
		youChip = lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Render(user.Username)
	}

	gap := ContentWidth - lipgloss.Width(left) - lipgloss.Width(youChip)
	if gap < 1 {
		gap = 1
	}
	headLine := left + strings.Repeat(" ", gap) + youChip

	// Sub-line composition depends on whether a search is active.
	//
	// Default (no search): show pagination indicator
	//   "N posts"             when total fits on one page
	//   "[1–100 of 423]"      otherwise
	//
	// Search active: skip the count chip entirely. The "search:foo" chip
	// further down already says "you're looking at search results", and
	// the count we'd otherwise show is the LIKE-match total — which
	// disagrees with the semantically-ranked rows when semantic search
	// is doing the actual work. Less confusing to omit it.
	parts := []string{}
	if search == "" {
		var countChip string
		if total > pageSize {
			from := page*pageSize + 1
			to := page*pageSize + visible
			countChip = fmt.Sprintf("[%d–%d of %d]", from, to, total)
		} else {
			countChip = fmt.Sprintf("%d posts", total)
		}
		parts = append(parts, countChip)
	}
	parts = append(parts, sortLabel(sort))
	if tag != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorBrand).Render("#"+tag))
	}
	if search != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorBrand).Render("search:"+truncateRunes(search, 20)))
	}
	if mineOnly {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorBrand).Render("mine"))
	}
	if compact {
		parts = append(parts, textMute.Render("compact"))
	}
	subLine := textMute.Render(strings.Join(parts, " · "))

	return headerStyle.Render(headLine + "\n" + subLine)
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

// === post row ============================================================

func (m feedModel) renderEmptyFeed() string {
	mark := chainMark()

	var title, cta string
	switch {
	case m.tagFilter != "" || m.searchQuery != "" || m.mineOnly:
		title = textBody.Render("nothing matches your filters.")
		cta = textDim.Render("press ") + keyChip.Render("x") + textDim.Render(" to clear filters · ") +
			keyChip.Render("?") + textDim.Render(" for help")
	default:
		title = textBody.Render("no posts yet.")
		cta = textDim.Render("press ") + keyChip.Render("n") + textDim.Render(" to write the first one.")
	}
	block := lipgloss.JoinVertical(lipgloss.Center, mark, "", title, cta)
	return lipgloss.NewStyle().
		Width(ContentWidth).
		Align(lipgloss.Center).
		PaddingTop(2).
		PaddingBottom(2).
		Render(block)
}

// displayName is the public-facing handle for a user. If the user has
// claimed a real username (e.g. "polite-okapi") we render that; otherwise
// we fall back to the legacy "anony-NNNN" surrogate so old rows still
// look reasonable in the UI.
func displayName(uname string, userID int64) string {
	if uname != "" {
		return uname
	}
	return fmt.Sprintf("anony-%04d", userID)
}

// renderTagChipsInline is the tighter variant used on the right side of the
// feed title row — single space between chips so they fit alongside the score.
func renderTagChipsInline(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	chip := lipgloss.NewStyle().Foreground(colorBrand)
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = chip.Render("#" + t)
	}
	return strings.Join(out, " ")
}

// renderPostRow draws one feed card in a tight 2-line, 2-column layout.
//
//	row 1:  [NN] **title**                          #tag1 #tag2  ▲ 1 ↗
//	row 2:       body snippet truncated…   silly-fox · 4m ago · ↳ 0
//
// The title row pairs the post identity (number + headline) on the left
// with discoverability + signal on the right (tags, vote score, momentum
// arrow). The body+meta row pairs *what the post is about* on the left
// with *who and when* on the right, so a single eye-sweep across the row
// answers "is this for me?" then "should I open it?".
//
// In compact mode (`space` toggles it) we drop the second row entirely —
// trades author/time visibility for max scan density.
func renderPostRow(idx int, p store.Post, selected, isMe, pendingDelete, compact bool) string {
	num := postNum.Render(fmt.Sprintf("[%02d]", idx+1))

	// === row 1: title + tags + score =================================
	tagsInline := renderTagChipsInline(p.Tags)
	score := renderScore(p.Score, p.MyVote)
	if p.RecentScore > 0 && p.MyVote != -1 {
		// "↗" hints at recent positive momentum. Suppressed if the viewer
		// has personally downvoted (visual contradiction otherwise).
		score += " " + lipgloss.NewStyle().Foreground(colorOk).Render("↗")
	}
	rightSide := score
	if tagsInline != "" {
		rightSide = tagsInline + "   " + score
	}

	titleBudget := ContentWidth - lipgloss.Width(num) - 1 - lipgloss.Width(rightSide) - 4
	if titleBudget < 12 {
		titleBudget = 12
	}
	title := lipgloss.NewStyle().Foreground(colorText).Bold(true).
		Render(truncateRunes(p.Title, titleBudget))
	titleLine := lipgloss.JoinHorizontal(lipgloss.Left, num, " ", title)
	gap := ContentWidth - lipgloss.Width(titleLine) - lipgloss.Width(rightSide) - 2
	if gap < 1 {
		gap = 1
	}
	headRow := titleLine + strings.Repeat(" ", gap) + rightSide

	parts := []string{headRow}

	// === row 2: body snippet (left) + author meta (right) ============
	if !compact {
		// Build the meta string first so we know how much horizontal space
		// it claims; the body snippet then takes whatever's left.
		var authorLabel string
		if isMe {
			authorLabel = lipgloss.NewStyle().Foreground(colorBrand).Bold(true).
				Render(displayName(p.Username, p.UserID))
		} else {
			authorLabel = textMute.Render(displayName(p.Username, p.UserID))
		}
		commentChip := textMute.Render(fmt.Sprintf("↳ %d", p.CommentCount))
		if p.HasUnread {
			commentChip += " " + lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Render("●")
		}
		sep := textMute.Render(" · ")
		meta := authorLabel + sep + textMute.Render(relTime(p.CreatedAt)) + sep + commentChip

		// Indent body text to start under the title (after "[NN] ").
		bodyIndent := strings.Repeat(" ", lipgloss.Width(num)+1)
		// Reserve indent + meta + 2-char gap. -2 accounts for the outer
		// PaddingLeft(2) the card gets after this function returns.
		bodyBudget := ContentWidth - 2 - lipgloss.Width(bodyIndent) - lipgloss.Width(meta) - 2
		if bodyBudget < 16 {
			bodyBudget = 16
		}

		flat := strings.ReplaceAll(strings.TrimSpace(p.Body), "\n", " ")
		bodyText := truncateRunes(flat, bodyBudget)
		bodyStyled := lipgloss.NewStyle().Foreground(colorTextDim).Render(bodyText)

		left := bodyIndent + bodyStyled
		gap2 := ContentWidth - 2 - lipgloss.Width(left) - lipgloss.Width(meta)
		if gap2 < 1 {
			gap2 = 1
		}
		bodyMetaRow := left + strings.Repeat(" ", gap2) + meta
		parts = append(parts, bodyMetaRow)
	}

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

// renderScore prints "▲ 12" / "▼ 3" / "· 0".
//
// Colour conveys two facts:
//   - whether *you* voted: brand orange = your upvote, muted red = your
//     downvote
//   - if you haven't voted, the chip stays in the neutral mute palette
//     even when the score itself is negative — we don't shout "this is
//     bad" at the reader, only at the voter
//
// Brand orange is reserved for "your action / fresh / focused" everywhere
// in the UI, so negative score reuses colorNegative (muted red) instead of
// colorBrandDeep to avoid the orange-means-bad conflation.
func renderScore(score, myVote int) string {
	icon := "·"
	col := colorTextMute
	switch myVote {
	case 1:
		icon = "▲"
		col = colorBrand
	case -1:
		icon = "▼"
		col = colorNegative
	default:
		if score > 0 {
			icon = "▲"
		} else if score < 0 {
			icon = "▼"
		}
	}
	return lipgloss.NewStyle().Foreground(col).Render(fmt.Sprintf("%s %d", icon, score))
}

// === footer ==============================================================

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
	// Footer is intentionally trimmed — full keybinds live behind `?`.
	// Voting from the list is supported (`v`/`V`) so it earns a chip; tag
	// filter (`#`) and the various activity/help keys live in `?` to keep
	// this row scannable.
	row := strings.Join([]string{
		renderKey("↑↓", "scroll"),
		renderKey("⏎", "open"),
		renderKey("n", "new"),
		renderKey("v/V", "vote"),
		renderKey("/", "search"),
		renderKey("?", "help"),
		renderKey("q", "quit"),
	}, renderKeySep())
	return footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(row),
	)
}

// === overlays ============================================================

func renderFeedHelp() string {
	title := brandText.Render("keybinds")
	tagline := textDim.Render("press ? or esc to close")
	rule := lipgloss.NewStyle().Foreground(colorBorder).Render(strings.Repeat("─", ContentWidth-4))

	groups := []struct {
		head string
		keys [][2]string
	}{
		{"navigate", [][2]string{
			{"↑↓ / j k", "move cursor"},
			{"g / G", "jump to top / bottom"},
			{"[ / ]", "previous / next page"},
			{"⏎", "open post"},
			{"r", "reload feed"},
		}},
		{"filter & find", [][2]string{
			{"/", "search title + body"},
			{"#", "filter by tag"},
			{"m", "toggle: only my posts"},
			{"f", "cycle sort (hot → new → top)"},
			{"esc / b", "clear last filter (search → tag → mine)"},
			{"x", "clear all filters at once"},
		}},
		{"act on a post", [][2]string{
			{"n", "new post"},
			{"v / V", "upvote / downvote"},
			{"d  D", "arm / confirm delete (own)"},
		}},
		{"display", [][2]string{
			{"space", "toggle compact mode (body preview)"},
		}},
		{"about", [][2]string{
			{"i", "what is voss / vask?"},
			{"Y", "your activity (recent posts + comments)"},
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

// renderFeedInfo is the "what is this?" screen behind the `i` key. The
// content is intentionally short and editorial — explains the project's
// shape, identity model, and where to find the source.
func renderFeedInfo() string {
	mark := chainMark()
	title := brandText.Render("voss / vask")
	tagline := textDim.Render("campus q&a · terminal-native · open source")

	rule := lipgloss.NewStyle().Foreground(colorBorder).
		Render(strings.Repeat("─", ContentWidth-4))

	headStyle := lipgloss.NewStyle().Foreground(colorTextDim).Italic(true).MarginTop(1)

	whatHead := headStyle.Render("what is this")
	whatBody := textBody.Render(
		"a no-frills q&a forum that lives entirely inside your terminal.\n" +
			"you ssh in, you read threads, you post questions, you reply.\n" +
			"no website, no app, no notifications, no inbox.",
	)

	idHead := headStyle.Render("how identity works")
	idBody := textBody.Render(
		"your ssh public key is hashed (sha256) and that hash is your\n" +
			"identity. we never store the raw key, your email, your name,\n" +
			"or your ip. on first connect we hand you a random handle like\n" +
			"\"polite-okapi\" — that's what other readers see.",
	)

	shapeHead := headStyle.Render("shape")
	shapeBody := textBody.Render(
		"posts and comments are immutable on purpose. once you send it,\n" +
			"it's there. no edit, no rewrite of history. you can delete\n" +
			"your own contributions, that's it.\n" +
			"\n" +
			"you can't vote on your own post or comment. score reflects\n" +
			"what other people think of your work, not what you think of it.",
	)

	whyHead := headStyle.Render("why")
	whyBody := textBody.Render(
		"campus forums tend to drift into noise or die under moderation\n" +
			"overhead. voss / vask is one frame, three actions: read, write,\n" +
			"vote. designed to stay small and stay fast.",
	)

	source := textMute.Render("source · ") +
		lipgloss.NewStyle().Foreground(colorBrand).Render("github.com/voss-labs/vask")
	brand := textMute.Render("brand  · ") +
		lipgloss.NewStyle().Foreground(colorBrand).Render("vosslabs.org")

	body := lipgloss.JoinVertical(
		lipgloss.Left,
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(mark),
		"",
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(title),
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(tagline),
		"",
		rule,
		whatHead,
		whatBody,
		idHead,
		idBody,
		shapeHead,
		shapeBody,
		whyHead,
		whyBody,
		"",
		source,
		brand,
		"",
		footerStyle.Render(
			lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(
				renderKey("i", "close")+renderKeySep()+renderKey("esc", "close"),
			),
		),
	)
	return body
}

func (m feedModel) viewSearchOverlay() string {
	header := headerStyle.Render(
		brandText.Render("voss") + textMute.Render(" / ") + brandText.Render("vask") +
			"  " + textDim.Render("search"),
	)
	prompt := textBody.Render("search posts")
	hint := textMute.Render("matches in title and body · case-insensitive")
	box := formBoxFocused.Render(m.searchInput.View())
	footer := footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(
			renderKey("⏎", "apply")+renderKeySep()+renderKey("esc", "cancel"),
		),
	)
	body := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		prompt,
		hint,
		"",
		box,
		"",
		footer,
	)
	return body
}

// viewActivityOverlay is the `Y` "your activity" panel. Shows the user's
// last 5 authored posts and last 5 comments, numbered for quick-jump.
// All numbers map onto detail-view of the underlying post (a comment's
// "open" is its parent post). This is the inbox-replacement gesture.
func (m feedModel) viewActivityOverlay() string {
	header := headerStyle.Render(
		brandText.Render("voss") + textMute.Render(" / ") + brandText.Render("vask") +
			"  " + textDim.Render("your activity"),
	)

	headStyle := lipgloss.NewStyle().Foreground(colorTextDim).Italic(true).MarginTop(1)

	var rows []string
	num := 1

	if len(m.activityPosts) > 0 {
		rows = append(rows, headStyle.Render("your last posts"))
		for _, p := range m.activityPosts {
			key := keyChip.Render(fmt.Sprintf("[%d]", num))
			title := lipgloss.NewStyle().Foreground(colorText).Bold(true).
				Render(truncateRunes(p.Title, 50))
			score := renderScore(p.Score, p.MyVote)
			when := textMute.Render(relTime(p.CreatedAt))
			rows = append(rows, "  "+key+"  "+title+
				"   "+textMute.Render("·")+"   "+when+
				"   "+textMute.Render("·")+"   "+score)
			num++
		}
	}

	if len(m.activityComments) > 0 {
		rows = append(rows, "")
		rows = append(rows, headStyle.Render("your last comments"))
		for _, ac := range m.activityComments {
			key := keyChip.Render(fmt.Sprintf("[%d]", num))
			snippet := strings.SplitN(ac.Comment.Body, "\n", 2)[0]
			snippet = truncateRunes(snippet, 36)
			arrow := lipgloss.NewStyle().Foreground(colorTextMute).Render("↳")
			body := lipgloss.NewStyle().Foreground(colorText).Render("\"" + snippet + "\"")
			on := textMute.Render(" on ") +
				lipgloss.NewStyle().Foreground(colorTextDim).Render("\""+truncateRunes(ac.PostTitle, 24)+"\"")
			when := textMute.Render(relTime(ac.Comment.CreatedAt))
			rows = append(rows, "  "+key+"  "+arrow+" "+body+on+
				"   "+textMute.Render("·")+"   "+when)
			num++
		}
	}

	if len(rows) == 0 {
		rows = append(rows,
			textMute.Render("no posts or comments from you yet · press n to write your first one"))
	}

	footer := footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(
			renderKey("1-9", "jump")+renderKeySep()+renderKey("esc", "close"),
		),
	)

	prompt := textBody.Render("things you've recently been part of")
	body := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		prompt,
		"",
		strings.Join(rows, "\n"),
		"",
		footer,
	)
	return body
}

func (m feedModel) viewTagPickerOverlay() string {
	header := headerStyle.Render(
		brandText.Render("voss") + textMute.Render(" / ") + brandText.Render("vask") +
			"  " + textDim.Render("filter by tag"),
	)

	var rows []string
	if len(m.popularTags) == 0 {
		rows = append(rows, textMute.Render("no tags yet · post something tagged to seed this list"))
	} else {
		for i, t := range m.popularTags {
			key := keyChip.Render(fmt.Sprintf("%d", i+1))
			tag := lipgloss.NewStyle().Foreground(colorBrand).Render("#" + t)
			marker := ""
			if t == m.tagFilter {
				marker = textOk.Render("  ← active")
			}
			rows = append(rows, "  "+key+"  "+tag+marker)
		}
	}
	if m.tagFilter != "" {
		rows = append(rows, "")
		rows = append(rows, "  "+keyChip.Render("0")+"  "+textDim.Render("clear filter"))
	}

	footer := footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(
			renderKey("1-9", "pick")+renderKeySep()+
				renderKey("0", "clear")+renderKeySep()+
				renderKey("esc", "cancel"),
		),
	)

	prompt := textBody.Render("popular tags")
	body := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		prompt,
		"",
		strings.Join(rows, "\n"),
		"",
		footer,
	)
	return body
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

