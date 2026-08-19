package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The Feed tab's mark-all-read button: a clickable label parked at the right
// end of the pane's title row while there is anything to mark. It states the
// key that does the same thing (read from the live keymap, so a rebind shows
// the user's own key) and highlights under the pointer — the same clickable
// vocabulary as the jump-to-bottom pill, which is where the palette comes from.

var (
	feedButtonStyle      = lipgloss.NewStyle().Foreground(jumpPillFg).Background(jumpPillBg)
	feedButtonHoverStyle = lipgloss.NewStyle().Foreground(jumpPillFg).Background(jumpPillHoverBg)
)

// feedTitleMinLeft is how many columns the title row keeps for itself before
// the button is allowed on it — enough for "Unread Feed" and a gap. Below that
// the pane says what it is rather than what you can do to it.
const feedTitleMinLeft = 14

// feedButton is the button as renderFeedPane resolved it for this frame: what
// it says, which content column it starts at, and whether the pointer is on
// it. The zero value means "don't draw one".
type feedButton struct {
	text    string
	col0    int
	active  bool
	hovered bool
}

// feedMarkAllText is the label, with the action's configured key spliced in.
func (m *Model) feedMarkAllText() string {
	if k := m.keys.MarkAllRead.Help().Key; k != "" {
		return " Mark all read (" + k + ") "
	}
	return " Mark all read "
}

// feedMarkAllButton sizes the button for a title row contentW columns wide, or
// returns the zero value when there is nothing to mark (an empty feed, or the
// action unbound) or the pane is too narrow to hold it.
func (m *Model) feedMarkAllButton(contentW int) feedButton {
	if len(m.feed.entries) == 0 || !m.keys.MarkAllRead.Enabled() {
		return feedButton{}
	}
	text := m.feedMarkAllText()
	w := lipgloss.Width(text)
	if contentW < w+feedTitleMinLeft {
		return feedButton{}
	}
	return feedButton{
		text:    text,
		col0:    contentW - w,
		active:  true,
		hovered: m.hover.zone == hitFeedMarkAll,
	}
}

// feedTitleRow lays out the pane's title row: title and metadata on the left,
// the button right-aligned against the pane's inner edge. The left half is
// truncated rather than wrapped — the hints there are the row's least
// load-bearing text, and a wrapped title row would push the viewport down a
// line and put every mouse row one off (feedGeom assumes exactly two rows above
// the bubbles).
func feedTitleRow(left string, contentW int, b feedButton) string {
	if !b.active {
		return ansi.Truncate(left, contentW, "…")
	}
	st := feedButtonStyle
	if b.hovered {
		st = feedButtonHoverStyle
	}
	// Keep a column of air between the metadata and the button.
	left = ansi.Truncate(left, b.col0-1, "…")
	if pad := b.col0 - lipgloss.Width(left); pad > 0 {
		left += strings.Repeat(" ", pad)
	}
	return left + ansi.ResetStyle + st.Render(b.text)
}

// armFeedButtonZone records the button's screen rectangle for the mouse layer,
// or disarms it when there is no button. The Feed pane fills the body width
// behind a single left border, so its content starts at column 1, and its title
// row is the body's first row. renderViewContent clears the zone every frame,
// so another tab can't inherit it.
func (m *Model) armFeedButtonZone(b feedButton) {
	if m.vcache == nil {
		return
	}
	if !b.active {
		m.vcache.feedBtnZone = rectZone{}
		return
	}
	x0 := 1 + b.col0
	m.vcache.feedBtnZone = rectZone{
		x0:     x0,
		x1:     x0 + lipgloss.Width(b.text),
		y:      tabsHeight,
		active: true,
	}
}
