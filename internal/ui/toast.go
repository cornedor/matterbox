package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The toast overlay: a small box stamped over the top-right of the body, on its
// own timer.
//
// The footer's right-hand slot looks like a toast surface and isn't one. It is a
// mode line: it carries the indexer's progress, the hovered link's target, the
// grammar hint and my own username, and each of those takes the slot back the
// moment it applies — the username the instant nothing else holds it, which is
// most of the time. A confirmation of something the user just did survives that
// (they are already looking at the result); a piece of news they did not ask for
// does not, and the update notice was measured at a fraction of a second on
// screen before the badge came back.
//
// So news gets its own surface. It overlays the body instead of sharing a line,
// nothing else can claim it, and it goes away on its own time or when clicked.
// Only the update notice uses it today, which is why it exists — see
// updatecheck.go.

// toastDwell is how long the box stays up. Generous next to the footer's four
// seconds, because it is out of the way and dismissible: the cost of it lingering
// is a corner of the sidebar, not the line the UI talks through.
const toastDwell = 20 * time.Second

// toastTop / toastInset place the box in the body's own coordinates: down from
// the top of the body, and in from its right-hand edge. The inset clears the
// pane's border column and its scrollbar, so the frame still reads as a frame
// around the box.
const (
	toastTop   = 1
	toastInset = 2
)

var (
	toastBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(focusedColor).
			Padding(0, 1)
	toastTitleStyle = lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
	toastBodyStyle  = lipgloss.NewStyle().Foreground(dimColor)
)

// toastState is the box on screen: a headline and one line under it. gen rises
// with every toast so an expiring timer can only ever clear the toast that
// scheduled it.
type toastState struct {
	title string
	body  string
	gen   int
}

func (t toastState) active() bool { return t.title != "" }

// toastExpireMsg is a toast's own timer coming back.
type toastExpireMsg struct{ gen int }

// showToast puts a box up for toastDwell. A second toast replaces the first
// rather than queueing: there is one of these on screen at a time, and the newer
// fact is the one worth the corner.
func (m *Model) showToast(title, body string) tea.Cmd {
	m.toast.gen++
	m.toast.title, m.toast.body = title, body
	gen := m.toast.gen
	return tea.Tick(toastDwell, func(time.Time) tea.Msg { return toastExpireMsg{gen: gen} })
}

// expireToast takes the box down when its own timer comes back, and does nothing
// when a newer toast (or a dismissal) already owns the corner.
func (m *Model) expireToast(gen int) {
	if m.toast.gen == gen {
		m.toast = toastState{gen: gen}
	}
}

// dismissToast takes the box down now. Bumping gen is what makes the timer still
// in flight a no-op when it lands.
func (m *Model) dismissToast() {
	m.toast = toastState{gen: m.toast.gen + 1}
}

// renderToast draws the box, or "" when nothing is up or the body is too small
// to hold it without covering the pane it floats over.
func (m *Model) renderToast(bodyH int) string {
	if !m.toast.active() {
		return ""
	}
	// Border + padding on both sides, plus the inset repeated on the left so a
	// wide notice in a narrow terminal is truncated rather than run to the edge.
	avail := m.width - 2*toastInset - 4
	lines := []string{toastTitleStyle.Render(truncate(m.toast.title, avail))}
	if m.toast.body != "" {
		lines = append(lines, toastBodyStyle.Render(truncate(m.toast.body, avail)))
	}
	if avail < 16 || bodyH < toastTop+len(lines)+2 {
		return ""
	}
	return toastBoxStyle.Render(strings.Join(lines, "\n"))
}

// toastCol is the box's left edge: right-anchored, so the length of the notice
// moves the box instead of moving the text it covers. Clamped at 0 for a
// terminal too narrow to hold the inset, which renderToast has already declined
// to draw in.
func (m *Model) toastCol(box string) int {
	if col := m.width - toastInset - lipgloss.Width(box); col > 0 {
		return col
	}
	return 0
}

// armToastZone records the box's screen rectangle for the mouse layer, or
// disarms it when no box is drawn. Written at render time like the jump pill's
// zone, and for the same reason: the geometry depends on what this frame turned
// out to be, not on the model's resting layout.
func (m *Model) armToastZone(box string) {
	if m.vcache == nil {
		return
	}
	if box == "" {
		m.vcache.toastZone = boxZone{}
		return
	}
	x0 := m.toastCol(box)
	m.vcache.toastZone = boxZone{
		x0:     x0,
		x1:     x0 + lipgloss.Width(box),
		y0:     tabsHeight + toastTop,
		y1:     tabsHeight + toastTop + lipgloss.Height(box),
		active: true,
	}
}

// boxZone is a clickable rectangle: columns [x0,x1) on rows [y0,y1). rectZone's
// multi-row sibling — the toast is the only thing on screen that covers more
// than one row without being a pane.
type boxZone struct {
	x0, x1, y0, y1 int
	active         bool
}

// contains reports whether the screen cell (x,y) falls on the box.
func (z boxZone) contains(x, y int) bool {
	return z.active && x >= z.x0 && x < z.x1 && y >= z.y0 && y < z.y1
}

// stampBlock replaces the cells block covers, row by row, at (row,col) in
// view's own coordinates. Only the rows it touches are rebuilt — the rest are
// handed back byte-for-byte — and each touched row keeps its display width, so
// pane borders and the scrollbar stay in column. Same trick as overlayJumpPill,
// one row taller: the leading reset closes whatever style the covered text left
// open, and TruncateLeft re-opens it for the text resuming to the right.
func stampBlock(view, block string, row, col int) string {
	if block == "" || view == "" {
		return view
	}
	lines := strings.Split(view, "\n")
	for i, bl := range strings.Split(block, "\n") {
		y := row + i
		if y < 0 || y >= len(lines) {
			continue
		}
		left := ansi.Truncate(lines[y], col, "")
		if w := lipgloss.Width(left); w < col {
			left += strings.Repeat(" ", col-w)
		}
		right := ansi.TruncateLeft(lines[y], col+lipgloss.Width(bl), "")
		lines[y] = left + ansi.ResetStyle + bl + right
	}
	return strings.Join(lines, "\n")
}
