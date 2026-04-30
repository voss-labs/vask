package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voss-labs/vask/internal/store"
)

// minTermWidth / minTermHeight — below these we refuse to render the
// normal frame and show a "resize me" message instead. The frame width
// is 78; anything narrower than ~60 produces visible truncation, and
// fewer than ~18 rows hides the footer or pushes a post off-screen.
const (
	minTermWidth  = 60
	minTermHeight = 18
)

// App is the top-level model. State machine (post-tags pivot, post-username):
//
//	splash ─(any key)─▶ onboard ─(enter)─▶ feed ⇄ compose
//	                      │                  │
//	                      │                  ▼
//	                      │                detail ─(b/esc)─▶ feed
//	                      ▼
//	  (existing users with a username skip straight to feed)
//
// Onboarding is the one-time handle picker — first-connect users go through
// it after accepting the TOS; users with a username already set go straight
// to the feed.
//
// stashedFeed: when the user navigates feed → detail or feed → compose, we
// hold onto the live feed model and restore it on return instead of
// rebuilding from scratch. That preserves cursor position, filter state,
// scroll offset, and the lastFeedAt snapshot — so the unread divider and
// the scroll position both behave correctly across navigation, and the
// returning view feels free (the stale data renders instantly while a
// background refresh runs).
type App struct {
	st   *store.Store
	user *store.User

	current     tea.Model
	stashedFeed tea.Model // saved feed model while we're in detail/compose
	width       int
	height     int
}

func NewApp(st *store.Store, user *store.User) App {
	app := App{st: st, user: user}
	switch {
	case user.TOSAcceptedAt == nil:
		app.current = newSplash()
	case user.Username == "":
		app.current = newOnboard(st, user)
	default:
		app.current = newFeed(st, user)
	}
	return app
}

func (a App) Init() tea.Cmd { return a.current.Init() }

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = m.Width, m.Height
		next, cmd := a.current.Update(msg)
		a.current = next
		return a, cmd

	case tosAcceptedMsg:
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.st.AcceptTOS(ctx, a.user.ID)
		now := time.Now()
		a.user.TOSAcceptedAt = &now
		// New users still need to pick a handle. Existing users with a
		// username already set (somehow accepted TOS but not onboarded)
		// fall through to the feed.
		if a.user.Username == "" {
			ob := newOnboard(a.st, a.user)
			a.current = ob
			return a, tea.Batch(ob.Init(), a.forwardSize())
		}
		feed := newFeed(a.st, a.user)
		a.current = feed
		return a, tea.Batch(feed.Init(), a.forwardSize())

	case usernameAcceptedMsg:
		a.user.Username = m.username
		// New users with AI configured get the optional starter-post
		// nudge; everyone else lands on the feed.
		if a.st.EmbedClient() != nil {
			fp := newFirstPost(a.st, a.user)
			a.current = fp
			return a, tea.Batch(fp.Init(), a.forwardSize())
		}
		feed := newFeed(a.st, a.user)
		a.current = feed
		return a, tea.Batch(feed.Init(), a.forwardSize())

	case firstpostSkipMsg:
		feed := newFeed(a.st, a.user)
		a.current = feed
		return a, tea.Batch(feed.Init(), a.forwardSize())

	case firstpostDoneMsg:
		// Ignore postID for now — the feed refresh will surface the new
		// post at the top under SortHot/New.
		_ = m
		feed := newFeed(a.st, a.user)
		a.current = feed
		return a, tea.Batch(feed.Init(), a.forwardSize())

	case openComposeMsg:
		// Stash whatever's current (the feed) so we can restore on cancel
		// or submit instead of rebuilding it.
		if _, ok := a.current.(feedModel); ok {
			a.stashedFeed = a.current
		}
		c := newCompose(a.st, a.user)
		a.current = c
		return a, tea.Batch(c.Init(), a.forwardSize())

	case composeCancelledMsg:
		return a.restoreFeedOrFresh()

	case composeSubmittedMsg:
		// Ignoring m.postID for now — feed refresh will surface the new
		// post at the top under SortHot/New.
		_ = m
		return a.restoreFeedOrFresh()

	case openDetailMsg:
		if _, ok := a.current.(feedModel); ok {
			a.stashedFeed = a.current
		}
		d := newDetail(a.st, a.user, m.postID)
		a.current = d
		return a, tea.Batch(d.Init(), a.forwardSize())

	case closeDetailMsg:
		return a.restoreFeedOrFresh()
	}

	next, cmd := a.current.Update(msg)
	a.current = next
	return a, cmd
}

// restoreFeedOrFresh returns to the feed view. Prefers the stashed live
// model (preserves cursor, filters, scroll, lastFeedAt snapshot) and
// fires a feedRefreshMsg to pull updated data in the background. Falls
// back to a fresh feed model if nothing was stashed (which only happens
// on first paint after onboard / TOS acceptance).
func (a App) restoreFeedOrFresh() (tea.Model, tea.Cmd) {
	if a.stashedFeed != nil {
		a.current = a.stashedFeed
		a.stashedFeed = nil
		return a, tea.Batch(
			func() tea.Msg { return feedRefreshMsg{} },
			a.forwardSize(),
		)
	}
	feed := newFeed(a.st, a.user)
	a.current = feed
	return a, tea.Batch(feed.Init(), a.forwardSize())
}

func (a App) forwardSize() tea.Cmd {
	if a.width == 0 || a.height == 0 {
		return nil
	}
	w, h := a.width, a.height
	return func() tea.Msg { return tea.WindowSizeMsg{Width: w, Height: h} }
}

func (a App) View() string {
	// Tiny terminals get a friendly nudge instead of a mangled frame.
	// The frame is 78 wide and renders ~14+ rows; anything smaller
	// destroys the layout and produces stuck-looking screens.
	if a.width > 0 && a.height > 0 && (a.width < minTermWidth || a.height < minTermHeight) {
		return lipgloss.Place(a.width, a.height,
			lipgloss.Center, lipgloss.Center,
			renderTooSmall(a.width, a.height),
		)
	}

	content := a.current.View()
	if a.width > 0 && a.height > 0 {
		return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center, content)
	}
	return content
}

func renderTooSmall(w, h int) string {
	title := brandText.Render("voss / vask")
	tag := textDim.Render("terminal too small")
	body := textBody.Render(
		"please resize your terminal to at least\n" +
			"  60 cols × 18 rows  for a good experience.",
	)
	current := textMute.Render(
		"current size: " + itoa(w) + " × " + itoa(h),
	)
	return lipgloss.JoinVertical(lipgloss.Center, title, tag, "", body, "", current)
}

// itoa is the minimal int→string helper used in the resize message;
// avoids pulling in fmt for one stringification.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// === inter-view messages ===============================================

type openComposeMsg struct{}
type composeCancelledMsg struct{}
type composeSubmittedMsg struct {
	postID int64
}
type openDetailMsg struct{ postID int64 }
type closeDetailMsg struct{}

// feedRefreshMsg is dispatched by App when the feed model is restored
// from a stash (after detail or compose). The feed handles it as a
// trigger to rebuild ListPostsParams and refetch — so users see the
// latest state without losing their cursor.
type feedRefreshMsg struct{}
