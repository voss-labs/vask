package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voss-labs/ask/internal/store"
)

// App is the top-level model. State machine:
//
//	splash ─(any key)─▶ channels ─(enter)─▶ feed ⇄ compose
//	                       ▲                  │
//	                       └──── 'c' ─────────┤
//	                                          ▼
//	                                       detail ─(b)─▶ feed
type App struct {
	st   *store.Store
	user *store.User

	current tea.Model
	width   int
	height  int
	channel string // empty = "all channels" feed
}

func NewApp(st *store.Store, user *store.User) App {
	app := App{st: st, user: user}
	if user.TOSAcceptedAt != nil {
		app.current = newChannelPicker(st)
	} else {
		app.current = newSplash()
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
		picker := newChannelPicker(a.st)
		a.current = picker
		return a, tea.Batch(picker.Init(), a.forwardSize())

	case selectChannelMsg:
		a.channel = m.slug
		feed := newFeed(a.st, a.user, a.channel)
		a.current = feed
		return a, tea.Batch(feed.Init(), a.forwardSize())

	case backToChannelsMsg:
		picker := newChannelPicker(a.st)
		a.current = picker
		return a, tea.Batch(picker.Init(), a.forwardSize())

	case openComposeMsg:
		c := newCompose(a.st, a.user, m.channel)
		a.current = c
		return a, tea.Batch(c.Init(), a.forwardSize())

	case composeCancelledMsg:
		feed := newFeed(a.st, a.user, a.channel)
		a.current = feed
		return a, tea.Batch(feed.Init(), a.forwardSize())

	case composeSubmittedMsg:
		// jump into the channel we just posted to so the user sees their post
		a.channel = m.channel
		feed := newFeed(a.st, a.user, a.channel)
		a.current = feed
		return a, tea.Batch(feed.Init(), a.forwardSize())

	case openDetailMsg:
		d := newDetail(a.st, a.user, m.postID)
		a.current = d
		return a, tea.Batch(d.Init(), a.forwardSize())

	case closeDetailMsg:
		feed := newFeed(a.st, a.user, a.channel)
		a.current = feed
		return a, tea.Batch(feed.Init(), a.forwardSize())
	}

	next, cmd := a.current.Update(msg)
	a.current = next
	return a, cmd
}

func (a App) forwardSize() tea.Cmd {
	if a.width == 0 || a.height == 0 {
		return nil
	}
	w, h := a.width, a.height
	return func() tea.Msg { return tea.WindowSizeMsg{Width: w, Height: h} }
}

func (a App) View() string {
	content := a.current.View()
	if a.width > 0 && a.height > 0 {
		return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center, content)
	}
	return content
}

// === inter-view messages ===============================================

type selectChannelMsg struct{ slug string } // "" = all-channels feed
type backToChannelsMsg struct{}
type openComposeMsg struct{ channel string }
type composeCancelledMsg struct{}
type composeSubmittedMsg struct {
	postID  int64
	channel string
}
type openDetailMsg struct{ postID int64 }
type closeDetailMsg struct{}
