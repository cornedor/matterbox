package editor

import "unicode"

// InsertRune inserts a single rune at the cursor.
func (m *Model) InsertRune(r rune) { m.insert([]rune{r}) }

// InsertString inserts text at the cursor, splitting on newlines.
func (m *Model) InsertString(s string) { m.insert([]rune(s)) }

// insert places runes at the cursor, honouring the char limit and creating new
// logical lines for any embedded newlines. Input is sanitised first.
func (m *Model) insert(rs []rune) {
	rs = sanitize(rs)
	if len(rs) == 0 {
		return
	}
	// Typing (or pasting) over a selection replaces it: drop the range first,
	// then insert at the collapsed caret. No-op when nothing is selected.
	m.DeleteSelection()
	if m.CharLimit > 0 {
		avail := m.CharLimit - m.length()
		if avail <= 0 {
			return
		}
		if avail < len(rs) {
			rs = rs[:avail]
		}
	}

	line := m.lines[m.row]
	head := line[:m.col]
	tail := append([]rune(nil), line[m.col:]...)

	parts := splitRunes(rs)
	if len(parts) == 1 {
		merged := make([]rune, 0, len(head)+len(parts[0])+len(tail))
		merged = append(merged, head...)
		merged = append(merged, parts[0]...)
		merged = append(merged, tail...)
		m.lines[m.row] = merged
		m.col += len(parts[0])
		m.afterEdit()
		return
	}

	first := make([]rune, 0, len(head)+len(parts[0]))
	first = append(first, head...)
	first = append(first, parts[0]...)
	last := make([]rune, 0, len(parts[len(parts)-1])+len(tail))
	last = append(last, parts[len(parts)-1]...)
	last = append(last, tail...)

	mid := parts[1 : len(parts)-1]
	newLines := make([][]rune, 0, len(m.lines)+len(parts)-1)
	newLines = append(newLines, m.lines[:m.row]...)
	newLines = append(newLines, first)
	for _, p := range mid {
		newLines = append(newLines, append([]rune(nil), p...))
	}
	newLines = append(newLines, last)
	newLines = append(newLines, m.lines[m.row+1:]...)
	m.lines = newLines
	m.row += len(parts) - 1
	m.col = len(parts[len(parts)-1])
	m.afterEdit()
}

// InsertNewline inserts a hard line break at the cursor.
func (m *Model) InsertNewline() { m.insert([]rune{'\n'}) }

// deleteBackward removes the rune before the cursor, joining lines at a line
// start.
func (m *Model) deleteBackward() {
	if m.col > 0 {
		line := m.lines[m.row]
		m.lines[m.row] = append(line[:m.col-1], line[m.col:]...)
		m.col--
	} else if m.row > 0 {
		prev := m.lines[m.row-1]
		joinAt := len(prev)
		merged := append(append([]rune(nil), prev...), m.lines[m.row]...)
		m.lines = append(m.lines[:m.row-1], append([][]rune{merged}, m.lines[m.row+1:]...)...)
		m.row--
		m.col = joinAt
	}
	m.afterEdit()
}

// deleteForward removes the rune at the cursor, joining the next line at a line
// end.
func (m *Model) deleteForward() {
	line := m.lines[m.row]
	if m.col < len(line) {
		m.lines[m.row] = append(line[:m.col], line[m.col+1:]...)
	} else if m.row < len(m.lines)-1 {
		merged := append(append([]rune(nil), line...), m.lines[m.row+1]...)
		m.lines = append(m.lines[:m.row], append([][]rune{merged}, m.lines[m.row+2:]...)...)
	}
	m.afterEdit()
}

// deleteWordBackward removes the word (and preceding spaces) left of the cursor,
// within the current logical line.
func (m *Model) deleteWordBackward() {
	if m.col == 0 {
		m.deleteBackward()
		return
	}
	line := m.lines[m.row]
	end := m.col
	c := m.col
	for c > 0 && unicode.IsSpace(line[c-1]) {
		c--
	}
	for c > 0 && !unicode.IsSpace(line[c-1]) {
		c--
	}
	m.lines[m.row] = append(line[:c], line[end:]...)
	m.col = c
	m.afterEdit()
}

// deleteWordForward removes the word (and following spaces) right of the cursor,
// within the current logical line.
func (m *Model) deleteWordForward() {
	line := m.lines[m.row]
	if m.col >= len(line) {
		m.deleteForward()
		return
	}
	start := m.col
	c := m.col
	for c < len(line) && unicode.IsSpace(line[c]) {
		c++
	}
	for c < len(line) && !unicode.IsSpace(line[c]) {
		c++
	}
	m.lines[m.row] = append(line[:start], line[c:]...)
	m.afterEdit()
}

// deleteAfterCursor truncates the current line at the cursor (ctrl+k).
func (m *Model) deleteAfterCursor() {
	m.lines[m.row] = m.lines[m.row][:m.col]
	m.afterEdit()
}

// deleteBeforeCursor drops everything before the cursor on the current line
// (ctrl+u).
func (m *Model) deleteBeforeCursor() {
	m.lines[m.row] = append([]rune(nil), m.lines[m.row][m.col:]...)
	m.col = 0
	m.afterEdit()
}

// afterEdit re-establishes invariants after a mutation.
func (m *Model) afterEdit() {
	m.clampCursor()
	m.refreshDesired()
	m.recalc()
}

// splitRunes splits a rune slice on '\n' into one slice per line (never empty).
func splitRunes(rs []rune) [][]rune {
	out := [][]rune{{}}
	for _, r := range rs {
		if r == '\n' {
			out = append(out, []rune{})
			continue
		}
		out[len(out)-1] = append(out[len(out)-1], r)
	}
	return out
}
