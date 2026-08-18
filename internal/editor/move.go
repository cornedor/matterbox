package editor

import (
	"unicode"

	"matterbox/internal/textwidth"
)

// characterLeft moves one rune left, crossing to the end of the previous
// logical line at a line start.
func (m *Model) characterLeft() {
	if m.col > 0 {
		m.col--
	} else if m.row > 0 {
		m.row--
		m.col = len(m.lines[m.row])
	}
	m.refreshDesired()
}

// characterRight moves one rune right, crossing to the start of the next
// logical line at a line end.
func (m *Model) characterRight() {
	if m.col < len(m.lines[m.row]) {
		m.col++
	} else if m.row < len(m.lines)-1 {
		m.row++
		m.col = 0
	}
	m.refreshDesired()
}

// wordLeft moves to the start of the previous word.
func (m *Model) wordLeft() {
	for m.col == 0 && m.row > 0 {
		m.row--
		m.col = len(m.lines[m.row])
	}
	line := m.lines[m.row]
	for m.col > 0 && unicode.IsSpace(line[m.col-1]) {
		m.col--
	}
	for m.col > 0 && !unicode.IsSpace(line[m.col-1]) {
		m.col--
	}
	m.refreshDesired()
}

// wordRight moves to the end of the next word.
func (m *Model) wordRight() {
	for m.col >= len(m.lines[m.row]) && m.row < len(m.lines)-1 {
		m.row++
		m.col = 0
	}
	line := m.lines[m.row]
	for m.col < len(line) && unicode.IsSpace(line[m.col]) {
		m.col++
	}
	for m.col < len(line) && !unicode.IsSpace(line[m.col]) {
		m.col++
	}
	m.refreshDesired()
}

// cursorUp / cursorDown move by one visual row, trying to keep desiredVCol.
// They navigate the cursor-aware layout — the geometry View actually drew — so
// the caret steps between the rows the user can see; measuring against the bare
// wrap instead made a caret parked on the reserved end-of-line row skip a row.
func (m *Model) cursorUp() {
	rows := m.layout(true)
	ci, _, _ := m.cursorVisRaw(rows)
	if ci <= 0 {
		m.col = 0
		m.refreshDesired()
		m.clampScroll()
		return
	}
	m.moveToVis(rows, ci-1, m.desiredVCol)
}

func (m *Model) cursorDown() {
	rows := m.layout(true)
	ci, _, _ := m.cursorVisRaw(rows)
	if ci >= len(rows)-1 {
		m.col = len(m.lines[m.row])
		m.refreshDesired()
		m.clampScroll()
		return
	}
	m.moveToVis(rows, ci+1, m.desiredVCol)
}

// moveToVis places the cursor on visual row vi at the rune nearest visual
// column vcol. desiredVCol is intentionally left unchanged so a run of vertical
// moves keeps targeting the same column.
func (m *Model) moveToVis(rows []visRow, vi, vcol int) {
	if vi < 0 || vi >= len(rows) {
		return
	}
	vr := rows[vi]
	m.row = vr.line
	rs := m.lines[vr.line]
	col := vr.a
	w := 0
	for col < vr.b {
		cw := textwidth.Width(string(rs[col]))
		if w+cw > vcol {
			break
		}
		w += cw
		col++
	}
	// The offset at a soft-wrap seam is shared by two rows, and cursorVisRaw
	// resolves it to the start of the *following* one. Landing there would put
	// the caret a row below the one asked for — and for cursorUp that means no
	// visible movement at all, which is how the caret got stuck on a wrapped
	// long word: every up-press re-targeted the seam and bounced straight back.
	// Keep the caret on row vi by stopping one rune short of the seam.
	if col == vr.b && col > vr.a && vi+1 < len(rows) && rows[vi+1].line == vr.line {
		col--
	}
	m.col = col
	m.clampScroll()
}
