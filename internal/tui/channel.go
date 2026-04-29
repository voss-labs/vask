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

// channelPicker is the entry view after splash. Shows all channels +
// an "all" pseudo-channel that opens a unified feed.
type channelPicker struct {
	st       *store.Store
	channels []store.Channel
	cursor   int
	width    int
	height   int
	err      error
}

type channelsLoadedMsg struct {
	channels []store.Channel
	err      error
}

func newChannelPicker(st *store.Store) channelPicker {
	return channelPicker{st: st}
}

func (m channelPicker) Init() tea.Cmd {
	return loadChannels(m.st)
}

func loadChannels(st *store.Store) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ch, err := st.ListChannels(ctx)
		return channelsLoadedMsg{channels: ch, err: err}
	}
}

// rowCount returns the number of selectable rows: channels + 1 ("all").
func (m channelPicker) rowCount() int { return len(m.channels) + 1 }

func (m channelPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case channelsLoadedMsg:
		m.channels = msg.channels
		m.err = msg.err
		if m.cursor >= m.rowCount() {
			m.cursor = 0
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < m.rowCount()-1 {
				m.cursor++
			}
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = m.rowCount() - 1
		case "a":
			// jump straight to all-channels feed
			return m, func() tea.Msg { return selectChannelMsg{slug: ""} }
		case "enter":
			slug := ""
			if m.cursor < len(m.channels) {
				slug = m.channels[m.cursor].Slug
			}
			return m, func() tea.Msg { return selectChannelMsg{slug: slug} }
		case "r":
			return m, loadChannels(m.st)
		}
	}
	return m, nil
}

func (m channelPicker) View() string {
	header := renderChannelHeader()

	var rows []string
	if m.err != nil {
		rows = append(rows, textErr.Render("error loading channels: "+m.err.Error()))
	} else {
		rows = append(rows, textDim.Render("pick a channel"))
		rows = append(rows, "")
		for i, c := range m.channels {
			rows = append(rows, renderChannelRow(c, i == m.cursor, false))
		}
		// "all" pseudo-row
		rows = append(rows, "")
		rows = append(rows, textMute.Render("  ─────────────"))
		rows = append(rows, "")
		allRow := store.Channel{
			Slug:        "",
			Name:        "all",
			Description: "unified feed across every channel",
			PostCount:   sumPosts(m.channels),
		}
		rows = append(rows, renderChannelRow(allRow, m.cursor == len(m.channels), true))
	}

	footer := renderChannelFooter()

	return frameStyle.Render(
		lipgloss.JoinVertical(lipgloss.Left, header, strings.Join(rows, "\n"), footer),
	)
}

func renderChannelHeader() string {
	left := brandText.Render("voss") + textMute.Render(" / ") + brandText.Render("ask")
	right := textDim.Render("channels")
	gap := ContentWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return headerStyle.Render(left + strings.Repeat(" ", gap) + right)
}

func renderChannelRow(c store.Channel, selected, isAll bool) string {
	name := "#" + c.Name
	if isAll {
		name = c.Name
	}
	nameStyled := catBadgeOn.Render(name)
	if !selected {
		nameStyled = catBadgeOff.Render(name)
	}

	desc := textDim.Render(c.Description)
	count := textMute.Render(fmt.Sprintf("(%d posts)", c.PostCount))

	row := nameStyled + "  " + desc + "  " + count

	if selected {
		return postSelectedBar.Render(row)
	}
	return lipgloss.NewStyle().PaddingLeft(2).Render(row)
}

func renderChannelFooter() string {
	row := strings.Join([]string{
		renderKey("↑↓", "pick"),
		renderKey("enter", "open"),
		renderKey("a", "all feed"),
		renderKey("r", "refresh"),
		renderKey("q", "quit"),
	}, renderKeySep())
	return footerStyle.Render(
		lipgloss.NewStyle().Width(ContentWidth).Align(lipgloss.Center).Render(row),
	)
}

func sumPosts(chans []store.Channel) int {
	total := 0
	for _, c := range chans {
		total += c.PostCount
	}
	return total
}
