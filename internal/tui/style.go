// Package tui contains every bubbletea model the SSH server can render.
//
// style.go is the single source of truth for colors, borders, spacing,
// and component-level styles — matched to vosslabs.org so the terminal
// feels like the same product.
package tui

import "github.com/charmbracelet/lipgloss"

// AppWidth is the maximum content width. The frame is rendered at this
// width and centered in the terminal viewport.
const AppWidth = 78

// ContentWidth is the inner width available after frame padding.
const ContentWidth = AppWidth - 4

// === palette =============================================================

var (
	// brand — same hex tokens as vosslabs.org
	colorBrand     = lipgloss.Color("#FB7A3C")
	colorBrandDeep = lipgloss.Color("#C4421D")

	// neutrals — picked to read on black terminals
	colorText     = lipgloss.Color("#FAFAFA")
	colorTextDim  = lipgloss.Color("#A8A29E")
	colorTextMute = lipgloss.Color("#737373")
	colorBorder   = lipgloss.Color("#3A3A3A")
	colorBorderHi = lipgloss.Color("#525252")
	colorOk       = lipgloss.Color("#5CB88A")
	// colorNegative — used exclusively for downvoted-score chips so brand
	// orange can stay reserved for "your action / fresh / focused" without
	// double-encoding as "this is bad". A muted desaturated red reads as
	// negative without being alarming on a black terminal.
	colorNegative = lipgloss.Color("#9F4848")
)

// === frame ===============================================================

var frameStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colorBorder).
	Padding(1, 2).
	Width(AppWidth)

// === type ================================================================

var (
	brandText = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	textBody  = lipgloss.NewStyle().Foreground(colorText)
	textDim   = lipgloss.NewStyle().Foreground(colorTextDim)
	textMute  = lipgloss.NewStyle().Foreground(colorTextMute)
	textWarn  = lipgloss.NewStyle().Foreground(colorBrand)
	textErr   = lipgloss.NewStyle().Foreground(colorBrandDeep)
	textOk    = lipgloss.NewStyle().Foreground(colorOk)
	heart     = lipgloss.NewStyle().Foreground(colorBrand)
	keyChip   = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
)

// === sections (header / footer rule) =====================================

var (
	headerStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(colorBorder).
			PaddingBottom(1).
			MarginBottom(1).
			Width(ContentWidth)

	footerStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.NormalBorder()).
			BorderTop(true).
			BorderForeground(colorBorder).
			PaddingTop(1).
			MarginTop(1).
			Width(ContentWidth)
)

// === form (compose) ======================================================

var (
	formLabel = lipgloss.NewStyle().
			Foreground(colorTextDim).
			Bold(false).
			MarginTop(1)

	formBoxFocused = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBrand).
			Padding(0, 1).
			Width(ContentWidth - 2)

	formBoxBlurred = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1).
			Width(ContentWidth - 2)
)

// === badges ==============================================================

var (
	catBadgeOn = lipgloss.NewStyle().
			Foreground(colorBrand).
			Bold(true)

	catBadgeOff = lipgloss.NewStyle().
			Foreground(colorTextMute)

	catFocusBracket = lipgloss.NewStyle().Foreground(colorBrand)
)

// === post card ===========================================================

var (
	postNum = lipgloss.NewStyle().Foreground(colorTextMute)

	postBody = lipgloss.NewStyle().
			Foreground(colorText).
			PaddingLeft(2)

	postHint = lipgloss.NewStyle().
			Foreground(colorTextDim).
			Italic(true).
			PaddingLeft(2)

	postSelectedBar = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(colorBrand).
			PaddingLeft(1)
)

// renderKey renders "shortcut · label" pair for footer keybind rows.
func renderKey(k, label string) string {
	return keyChip.Render(k) + " " + textDim.Render(label)
}

// renderKeySep returns "  " — used between key/label groups in the footer.
func renderKeySep() string {
	return textMute.Render("   ")
}

// hyperlink wraps text in an OSC 8 escape so terminals that support it
// (iTerm2, Terminal.app, kitty, alacritty, wezterm, ghostty, modern
// Windows Terminal) render the text as a clickable link to url. Older
// terminals just display the plain text — the escape bytes are
// consumed silently. Width-counting ANSI strippers treat OSC sequences
// the same as SGR, so lipgloss centering still measures the visible
// text correctly.
func hyperlink(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}
