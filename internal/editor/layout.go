package editor

import "matterbox/internal/textwidth"

// visRow is one rendered visual line: the logical line it came from and the
// half-open rune range [a, b) of that logical line it shows. first marks the
// sub-line that carries the prompt (the rest are continuation rows). A row may
// be an empty trailing sub-line (a == b) when the caret reservation pushed the
// caret past the end of a full line — that empty row hosts the end-of-line
// caret (see wrapLine's reserve argument).
type visRow struct {
	line  int
	a, b  int
	first bool
}

// layout wraps the whole buffer into visual rows. When withCursor is set (and
// the field is focused) the cursor's logical line is wrapped reserving a cell
// for the end-of-line caret, so a caret sitting just past a perfectly full
// sub-line lands on a fresh visual row (and a trailing word that would overflow
// once the caret is accounted for wraps down as a unit).
func (m *Model) layout(withCursor bool) []visRow {
	cw := m.contentWidth()
	cursorAtLineEnd := withCursor && m.focus && m.col == len(m.lines[m.row])
	var rows []visRow
	for li, line := range m.lines {
		reserve := 0
		if cursorAtLineEnd && li == m.row {
			reserve = 1
		}
		subs := wrapLine(line, cw, reserve)
		off := 0
		for si, sub := range subs {
			rows = append(rows, visRow{
				line:  li,
				a:     off,
				b:     off + len(sub),
				first: si == 0,
			})
			off += len(sub)
		}
	}
	return rows
}

// visualRowCount is the number of visual rows the content occupies (no caret
// reservation — the bare wrapped content).
func (m *Model) visualRowCount() int {
	cw := m.contentWidth()
	n := 0
	for _, line := range m.lines {
		n += len(wrapLine(line, cw, 0))
	}
	return n
}

// cursorVisRaw locates the cursor within rows: it returns the visual-row index,
// the visual column, and whether the cursor sits at the end of that row's range
// (col == b). At a soft-wrap seam the cursor belongs to the start of the
// following row, not the end of the current one.
func (m *Model) cursorVisRaw(rows []visRow) (idx, vcol int, atEnd bool) {
	for i, vr := range rows {
		if vr.line != m.row {
			continue
		}
		if m.col >= vr.a && m.col < vr.b {
			return i, m.colWidth(vr.line, vr.a, m.col), false
		}
		if m.col == vr.b {
			// End of this segment. If a continuation row for the same logical
			// line follows, the caret belongs at its start instead.
			if i+1 < len(rows) && rows[i+1].line == m.row {
				continue
			}
			return i, m.colWidth(vr.line, vr.a, m.col), true
		}
	}
	// Fallback: empty buffer or unreachable — first row.
	return 0, 0, true
}

// cursorVis returns the cursor's visual-row index and column within the rows
// produced by layout(true).
func (m *Model) cursorVis(rows []visRow) (idx, vcol int) {
	i, vc, _ := m.cursorVisRaw(rows)
	return i, vc
}

// colWidth is the display width of lines[line][a:col].
func (m *Model) colWidth(line, a, col int) int {
	if line < 0 || line >= len(m.lines) {
		return 0
	}
	rs := m.lines[line]
	if a < 0 {
		a = 0
	}
	if col > len(rs) {
		col = len(rs)
	}
	if a >= col {
		return 0
	}
	return textwidth.Width(string(rs[a:col]))
}

// clampScroll keeps the cursor's visual row inside the [yOffset, yOffset+height)
// window and bounds yOffset to the content.
func (m *Model) clampScroll() {
	rows := m.layout(true)
	total := len(rows)
	h := max(m.height, 1)
	ci, _ := m.cursorVis(rows)
	if ci < m.yOffset {
		m.yOffset = ci
	}
	if ci >= m.yOffset+h {
		m.yOffset = ci - h + 1
	}
	maxOff := max(total-h, 0)
	if m.yOffset > maxOff {
		m.yOffset = maxOff
	}
	if m.yOffset < 0 {
		m.yOffset = 0
	}
}

// clampCursor keeps (row, col) within the buffer.
func (m *Model) clampCursor() {
	if len(m.lines) == 0 {
		m.lines = [][]rune{{}}
	}
	if m.row < 0 {
		m.row = 0
	}
	if m.row >= len(m.lines) {
		m.row = len(m.lines) - 1
	}
	if m.col < 0 {
		m.col = 0
	}
	if m.col > len(m.lines[m.row]) {
		m.col = len(m.lines[m.row])
	}
}
