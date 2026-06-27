package editor

// This file holds the rune-offset cursor coordinate space used by callers
// (grammar fixes, undo/redo, mention/emoji completion). An "offset" is the rune
// index into Value(), counting each '\n' between logical lines as one rune.

// CursorOffset returns the cursor position as a rune offset into Value().
func (m *Model) CursorOffset() int {
	off := 0
	for i := 0; i < m.row && i < len(m.lines); i++ {
		off += len(m.lines[i]) + 1 // +1 for the newline
	}
	return off + m.col
}

// SetCursorOffset moves the cursor to the given rune offset into Value(),
// clamping out-of-range values to the end of the buffer.
func (m *Model) SetCursorOffset(off int) {
	if off < 0 {
		off = 0
	}
	for i, ln := range m.lines {
		if off <= len(ln) {
			m.row, m.col = i, off
			m.refreshDesired()
			m.clampScroll()
			return
		}
		off -= len(ln) + 1
	}
	m.CursorEnd()
}

// CursorRowCol returns the cursor's logical (row, column). column is a rune
// index within that row — the coordinates mention/emoji completion work in.
func (m *Model) CursorRowCol() (row, col int) { return m.row, m.col }

// CursorVisualRow returns the cursor's row index among the buffer's wrapped
// visual rows (0 is the very first visual row). Callers use it to tell whether
// the caret sits on the top row of a multi-line draft.
func (m *Model) CursorVisualRow() int {
	rows := m.layout(true)
	i, _ := m.cursorVis(rows)
	return i
}

// CursorStart moves the cursor to the start of the current logical line.
func (m *Model) CursorStart() {
	m.col = 0
	m.refreshDesired()
	m.clampScroll()
}

// CursorEnd moves the cursor to the end of the buffer.
func (m *Model) CursorEnd() {
	m.row = max(len(m.lines)-1, 0)
	m.col = len(m.lines[m.row])
	m.refreshDesired()
	m.clampScroll()
}

// MoveToBegin moves the cursor to the very start of the buffer.
func (m *Model) MoveToBegin() {
	m.row, m.col = 0, 0
	m.refreshDesired()
	m.clampScroll()
}

// refreshDesired records the cursor's current visual column so the next
// vertical move tries to preserve it.
func (m *Model) refreshDesired() {
	rows := m.layout(false)
	_, vc, _ := m.cursorVisRaw(rows)
	m.desiredVCol = vc
}
