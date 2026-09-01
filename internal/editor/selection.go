package editor

import "unicode"

// Text selection. A selection is an anchor plus the moving caret, both rune
// offsets into Value() (the CursorOffset coordinate space, '\n' counted as one
// rune). The mouse-facing API speaks the same absolute visual coordinates the
// View renders in — a visual row index (every wrapped row counted from 0) and a
// display column within the content area — so the ui layer only has to translate
// a screen cell to (row, col); the wrap/scroll mapping stays here. See mouse.go.
//
// A selection also carries a granularity (selGran): a plain click selects by
// character, a double-click by word, a triple-click by whole line. After a
// double/triple-click the drag grows a unit at a time — the word/line the click
// landed on stays wholly covered while the moving end snaps outward.

// selGran is the unit a selection (and the drag extending it) snaps to.
type selGran int

const (
	granChar selGran = iota
	granWord
	granLine
)

// runeClass buckets a rune for word selection: whitespace, word (letters,
// digits, underscore) and "other" (punctuation/symbols) each form their own
// contiguous run, so a double-click on any of the three selects that run.
func runeClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 1
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 2
	default:
		return 3
	}
}

// PromptWidth is the column width reserved for the prompt gutter. The ui layer
// needs it to turn a screen column into a content column when mapping clicks.
func (m *Model) PromptWidth() int { return m.promptWidth }

// HasSelection reports whether a non-empty selection is active.
func (m *Model) HasSelection() bool {
	if !m.selActive {
		return false
	}
	s, e := m.selBounds()
	return e > s
}

// SelectionRange returns the ordered selection bounds (rune offsets into Value)
// and whether a non-empty selection is active.
func (m *Model) SelectionRange() (start, end int, ok bool) {
	if !m.HasSelection() {
		return 0, 0, false
	}
	s, e := m.selBounds()
	return s, e, true
}

// SelectedText returns the substring of Value covered by the selection, or "".
func (m *Model) SelectedText() string {
	s, e, ok := m.SelectionRange()
	if !ok {
		return ""
	}
	return string([]rune(m.Value())[s:e])
}

// ClearSelection drops any selection (the caret stays put).
func (m *Model) ClearSelection() {
	m.selActive, m.selAnchor = false, 0
	m.selGran, m.selAnchorLo, m.selAnchorHi = granChar, 0, 0
}

// selBounds orders the anchor and the caret into [start, end).
func (m *Model) selBounds() (start, end int) {
	head := m.CursorOffset()
	if m.selAnchor <= head {
		return m.selAnchor, head
	}
	return head, m.selAnchor
}

// SetSelection anchors at `anchor` and moves the caret (the moving end) to
// `head`, both rune offsets into Value (clamped). The selection is active when
// the two ends differ. Character granularity.
func (m *Model) SetSelection(anchor, head int) {
	a := clamp(anchor, 0, m.length())
	m.selGran = granChar
	m.selAnchor, m.selAnchorLo, m.selAnchorHi = a, a, a
	m.SetCursorOffset(head)
	m.selActive = m.selAnchor != m.CursorOffset()
}

// MoveCursorToVisual places the caret at the given absolute visual position and
// clears any selection — the click-to-place-cursor primitive.
func (m *Model) MoveCursorToVisual(vrow, vcol int) {
	m.ClearSelection()
	m.caretToVisual(vrow, vcol)
	m.refreshDesired()
}

// BeginSelection places the caret at the visual position and drops the anchor
// there, arming a character-granular drag. The selection stays empty until
// the caret moves off the anchor.
func (m *Model) BeginSelection(vrow, vcol int) {
	m.caretToVisual(vrow, vcol)
	m.refreshDesired()
	off := m.CursorOffset()
	m.selGran = granChar
	m.selAnchor, m.selAnchorLo, m.selAnchorHi = off, off, off
	m.selActive = false
}

// SelectWordAtVisual selects the word under the visual position (double-click)
// and arms a word-granular drag.
func (m *Model) SelectWordAtVisual(vrow, vcol int) {
	m.caretToVisual(vrow, vcol)
	lo, hi := m.wordBoundsAt(m.CursorOffset())
	m.setUnitSelection(granWord, lo, hi)
}

// SelectLineAtVisual selects the whole logical line under the visual position
// (triple-click) and arms a line-granular drag.
func (m *Model) SelectLineAtVisual(vrow, vcol int) {
	m.caretToVisual(vrow, vcol)
	lo, hi := m.lineBoundsAt(m.CursorOffset())
	m.setUnitSelection(granLine, lo, hi)
}

// setUnitSelection installs a selection covering the anchor unit [lo, hi) at the
// given granularity, parking the caret at hi (the moving end of a forward drag).
func (m *Model) setUnitSelection(g selGran, lo, hi int) {
	m.selGran = g
	m.selAnchorLo, m.selAnchorHi = lo, hi
	m.selAnchor = lo
	m.SetCursorOffset(hi)
	m.selActive = hi > lo
	m.refreshDesired()
}

// selectMove runs a caret movement as a selection extend: with no selection
// live it first drops the anchor at the caret, so the first shift+arrow starts
// one. Keyboard extension is always character-granular — a word/line selection
// from a double/triple-click keeps its ends but stops snapping once the
// keyboard takes over.
func (m *Model) selectMove(move func()) {
	if !m.selActive {
		m.selAnchor = m.CursorOffset()
	}
	m.selGran = granChar
	m.selAnchorLo, m.selAnchorHi = m.selAnchor, m.selAnchor
	move()
	m.selActive = m.CursorOffset() != m.selAnchor
}

// ExtendSelectionToVisual moves the selection's moving end to the visual
// position, keeping the anchor fixed and snapping to the active granularity (a
// drag after a double/triple-click grows a whole word/line at a time).
func (m *Model) ExtendSelectionToVisual(vrow, vcol int) {
	m.caretToVisual(vrow, vcol)
	m.selExtendTo(m.CursorOffset())
}

// ExtendSelectionFromCaret extends a selection to the visual position for a
// shift-click: with no live selection it first anchors at the current caret, so
// shift-click selects from the caret to the click. An existing word/line
// selection keeps its granularity, so shift-click then grows it by word/line.
func (m *Model) ExtendSelectionFromCaret(vrow, vcol int) {
	if !m.HasSelection() {
		off := m.CursorOffset()
		m.selGran = granChar
		m.selAnchor, m.selAnchorLo, m.selAnchorHi = off, off, off
	}
	m.caretToVisual(vrow, vcol)
	m.selExtendTo(m.CursorOffset())
}

// selExtendTo moves the moving end to offset off, snapping both ends to the
// active granularity: the anchor unit [selAnchorLo, selAnchorHi) stays covered
// and the dragged end grows to the word/line boundary at off. The caret is
// always left at the moving end so SelectionRange / rendering stay consistent.
func (m *Model) selExtendTo(off int) {
	lo, hi := off, off
	switch m.selGran {
	case granWord:
		lo, hi = m.wordBoundsAt(off)
	case granLine:
		lo, hi = m.lineBoundsAt(off)
	}
	switch {
	case lo >= m.selAnchorHi: // dragged at or past the anchor unit
		m.selAnchor = m.selAnchorLo
		m.SetCursorOffset(hi)
	case hi <= m.selAnchorLo: // dragged at or before the anchor unit
		m.selAnchor = m.selAnchorHi
		m.SetCursorOffset(lo)
	default: // overlapping the anchor unit (same word/line)
		m.selAnchor = min(lo, m.selAnchorLo)
		m.SetCursorOffset(max(hi, m.selAnchorHi))
	}
	m.selActive = m.CursorOffset() != m.selAnchor
	m.refreshDesired()
}

// caretToVisual sets (row, col) from an absolute visual position, mapping
// against the same cursor-aware layout the View drew, clamped into range. It
// leaves desiredVCol alone (callers refresh it) and re-clamps scroll via
// moveToVis.
func (m *Model) caretToVisual(vrow, vcol int) {
	rows := m.layout(true)
	if len(rows) == 0 {
		return
	}
	if vrow < 0 {
		vrow = 0
	}
	if vrow >= len(rows) {
		vrow = len(rows) - 1
	}
	if vcol < 0 {
		vcol = 0
	}
	m.moveToVis(rows, vrow, vcol)
}

// DeleteSelection removes the selected range, parks the caret at its start, and
// clears the selection. Returns false when nothing was selected.
func (m *Model) DeleteSelection() bool {
	s, e, ok := m.SelectionRange()
	if !ok {
		return false
	}
	r0, c0 := m.offsetToRowCol(s)
	r1, c1 := m.offsetToRowCol(e)
	head := append([]rune(nil), m.lines[r0][:c0]...)
	merged := append(head, m.lines[r1][c1:]...)
	newLines := make([][]rune, 0, len(m.lines)-(r1-r0))
	newLines = append(newLines, m.lines[:r0]...)
	newLines = append(newLines, merged)
	newLines = append(newLines, m.lines[r1+1:]...)
	m.lines = newLines
	m.row, m.col = r0, c0
	m.ClearSelection()
	m.afterEdit()
	return true
}

// rowColOffset is the inverse of offsetToRowCol: the rune offset into Value of
// logical position (row, col).
func (m *Model) rowColOffset(row, col int) int {
	off := 0
	for i := 0; i < row && i < len(m.lines); i++ {
		off += len(m.lines[i]) + 1 // +1 for the newline
	}
	return off + col
}

// wordBoundsAt returns the [lo, hi) offset range of the word — the run of one
// rune class (see runeClass) — containing offset off, never crossing the
// logical line. An offset on an empty line, or past the last rune of its line,
// yields an empty range at that point (a double-click there selects nothing).
func (m *Model) wordBoundsAt(off int) (lo, hi int) {
	row, col := m.offsetToRowCol(off)
	line := m.lines[row]
	if len(line) == 0 || col >= len(line) {
		o := m.rowColOffset(row, col)
		return o, o
	}
	cls := runeClass(line[col])
	a, b := col, col+1
	for a > 0 && runeClass(line[a-1]) == cls {
		a--
	}
	for b < len(line) && runeClass(line[b]) == cls {
		b++
	}
	return m.rowColOffset(row, a), m.rowColOffset(row, b)
}

// lineBoundsAt returns the [lo, hi) offset range of the whole logical line
// containing off (its content, excluding the trailing newline).
func (m *Model) lineBoundsAt(off int) (lo, hi int) {
	row, _ := m.offsetToRowCol(off)
	return m.rowColOffset(row, 0), m.rowColOffset(row, len(m.lines[row]))
}

// offsetToRowCol converts a rune offset into Value() to a logical (row, col),
// clamping out-of-range offsets to the buffer ends.
func (m *Model) offsetToRowCol(off int) (row, col int) {
	if off <= 0 {
		return 0, 0
	}
	for i, ln := range m.lines {
		if off <= len(ln) {
			return i, off
		}
		off -= len(ln) + 1
	}
	last := len(m.lines) - 1
	return last, len(m.lines[last])
}
