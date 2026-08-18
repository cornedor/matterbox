package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// modalMaxWidth caps the outer width of the sheet-style modals (keys sheet,
// edit history, the pickers and text popups): wide enough for two columns of
// text, capped so they stay readable on a big terminal.
const modalMaxWidth = 96

// modalDims returns the sheet modal's outer width and content (inner) height:
// min(80% of the terminal, modalMaxWidth) clamped to a floor, and the terminal
// height less a few rows of margin and the chrome (border, title, rule). Every
// sheet modal shares these numbers so they all open as the same frame.
func (m *Model) modalDims() (outerW, innerH int) {
	outerW = m.width * 4 / 5
	if outerW > modalMaxWidth {
		outerW = modalMaxWidth
	}
	if outerW < 30 {
		outerW = 30
	}
	if outerW > m.width-2 {
		outerW = m.width - 2
	}
	if outerW < 1 {
		outerW = 1
	}
	bodyH := m.height - 4
	if bodyH < 6 {
		bodyH = 6
	}
	innerH = bodyH - 4 // border (2) + title (1) + rule (1)
	if innerH < 3 {
		innerH = 3
	}
	return outerW, innerH
}

// modalInnerWidth is the text width inside the modal: the outer width less the
// border (2) and the one-cell padding on each side.
func (m *Model) modalInnerWidth() int {
	outerW, _ := m.modalDims()
	if inner := outerW - 4; inner > 1 {
		return inner
	}
	return 1
}

// renderModal draws the sheet modal frame around body: a title with a dim hint
// beside it, a rule, then the body. body is expected to already fit
// modalDims() (a sized viewport, or a list windowed to innerH rows); a line
// wider than the frame is truncated, never wrapped, so the frame's height is
// the caller's line count.
//
// The box is drawn by hand rather than through lipgloss's Border+Width style:
// the output is byte-identical (TestRenderModalMatchesLipgloss), but the
// style's wrap-and-pad pass was ~70% of a sheet's per-frame allocations,
// which the keys sheet, edit history and every list sheet paid on each
// keystroke while open.
func (m *Model) renderModal(title, hint, body string) string {
	outerW, _ := m.modalDims()
	inner := m.modalInnerWidth()
	dim := lipgloss.NewStyle().Foreground(dimColor)
	head := titleStyle.Render(title)
	if hint != "" {
		head += "  " + dim.Render(hint)
	}
	rule := dim.Render(strings.Repeat("─", inner))

	edge := lipgloss.NewStyle().Foreground(focusedColor)
	left := edge.Render(border.Left) + " "
	right := " " + edge.Render(border.Right)
	var b strings.Builder
	b.Grow((outerW + 16) * (strings.Count(body, "\n") + 5))
	b.WriteString(edge.Render(border.TopLeft + strings.Repeat(border.Top, outerW-2) + border.TopRight))
	line := func(s string) {
		b.WriteByte('\n')
		b.WriteString(left)
		s = truncate(s, inner)
		b.WriteString(s)
		if pad := inner - lipgloss.Width(s); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		b.WriteString(right)
	}
	line(head)
	line(rule)
	for { // every "\n"-separated segment is a row, including empty ones and a trailing one
		i := strings.IndexByte(body, '\n')
		if i < 0 {
			line(body)
			break
		}
		line(body[:i])
		body = body[i+1:]
	}
	b.WriteByte('\n')
	b.WriteString(edge.Render(border.BottomLeft + strings.Repeat(border.Bottom, outerW-2) + border.BottomRight))
	return b.String()
}

// renderListModal draws a selectable list inside the sheet modal. n is the
// row count, row(i) renders one item and idx is the selection. The list is
// windowed to the modal's inner height, keeping the selection in view — only
// the rows in the window are rendered, so a long list costs its window, not
// its length, per frame — and padded to that height so the frame doesn't
// jump as the list changes. When n is 0, body (a loading / empty / error
// line) is shown instead.
func (m *Model) renderListModal(title, hint, body string, n, idx int, row func(i int) string) string {
	inner := m.modalInnerWidth()
	_, innerH := m.modalDims()
	lines := make([]string, 0, innerH)
	if n == 0 {
		lines = append(lines, strings.Split(body, "\n")...)
	} else {
		start, end := listWindow(n, idx, innerH)
		for i := start; i < end; i++ {
			line := truncate(row(i), inner)
			if i == idx {
				line = selectedRow.Render(line)
			}
			lines = append(lines, line)
		}
	}
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	if len(lines) > innerH {
		lines = lines[:innerH]
	}
	return m.renderModal(title, hint, strings.Join(lines, "\n"))
}

// listNav applies the shared list-sheet navigation to *idx over n rows: ↑/k
// and ↓/j (plus the composer's ctrl+p / ctrl+n aliases) move one row, clamped.
// Reports whether msg was a navigation key.
func (m *Model) listNav(msg tea.KeyPressMsg, idx *int, n int) bool {
	switch {
	case key.Matches(msg, m.keys.Up), key.Matches(msg, m.keys.InputUp):
		if *idx > 0 {
			*idx--
		}
	case key.Matches(msg, m.keys.Down), key.Matches(msg, m.keys.InputDown):
		if *idx < n-1 {
			*idx++
		}
	default:
		return false
	}
	if *idx < 0 {
		*idx = 0
	}
	return true
}

// listWindow returns the [start, end) slice of an n-row list that fits in
// height rows while keeping idx visible — centred on it once the list is
// taller than the window, pinned to the ends otherwise.
func listWindow(n, idx, height int) (start, end int) {
	if height <= 0 || n <= height {
		return 0, n
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	start = idx - height/2
	if start < 0 {
		start = 0
	}
	if start+height > n {
		start = n - height
	}
	return start, start + height
}
