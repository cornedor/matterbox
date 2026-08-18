package editor

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/textwidth"
)

// Property sweeps over the whole editor: rather than pin one scenario each,
// these walk a corpus of awkward buffers at several widths and assert the
// invariants the rest of the package is built on — that the caret can always
// reach every visual row, that a click lands where the caret is drawn, that a
// rendered row is exactly as wide as it claims, and that an edit on a value
// copy of the Model can't reach back into the original.
//
// The corpus is where the interesting cases live: lines that end exactly on the
// wrap width, words longer than the width, wide runes, and grapheme clusters.

// wrapCorpus holds buffers chosen to stress the wrap geometry.
var wrapCorpus = []string{
	"- bar\ncommandscommandscommandscommandscommandscommandscommands\n- baz",
	"foo foo foo foo foo foo\n- bar\n- baz",
	"aaaaaaaaaaaaaaaaaaaa", // exactly 20 cells
	"aaaaaaaaaaaaaaaaaaaa\nbbb",
	"hello world this is a longer sentence that wraps a few times over",
	"日本語のテキストがここにあります、とても長い行です",
	"a\n\nb\n\n\nc",
	"trailing spaces   \nnext",
	"x" + strings.Repeat("y", 100),
	"emoji 😀😀😀 test 😀😀😀😀😀😀😀😀😀",
	"one two three\n",
	"\n\n",
}

// unicodeCorpus holds text whose runes and grapheme clusters disagree.
var unicodeCorpus = []string{
	"日本語テキスト",
	"café",           // precomposed
	"café",           // e + combining acute
	"👨‍👩‍👧‍👦 family", // ZWJ sequence
	"🇳🇱 flag",        // regional indicators
	"zero​width",     // zero-width space
}

func fullCorpus() []string {
	return append(append([]string{}, wrapCorpus...), unicodeCorpus...)
}

// visualRow is the index of the caret's row in the layout View renders.
func visualRow(m *Model) int {
	i, _ := m.cursorVis(m.layout(true))
	return i
}

// TestUpArrowEscapesWrappedWord is the reported bug: with the caret at the end
// of a long word wrapped across full-width rows, up-arrow re-targeted the
// soft-wrap seam, which resolves to the start of the row the caret was already
// on — so the caret snapped to the row's first column and stayed there forever.
func TestUpArrowEscapesWrappedWord(t *testing.T) {
	m := newTestModel(20)
	m.SetValue("- bar\n" + strings.Repeat("commands", 8)[:60] + "\n")
	m.SetCursorOffset(len("- bar\n") + 60)

	start := visualRow(&m)
	for i := range start {
		want := start - i - 1
		m.cursorUp()
		if got := visualRow(&m); got != want {
			t.Fatalf("up #%d: caret on visual row %d, want %d", i+1, got, want)
		}
	}
}

// TestVerticalMovementReachesEveryEdge sweeps the corpus: from any starting
// offset, repeated up-arrows must reach the first visual row and repeated
// down-arrows the last. A caret that stops making progress is stuck.
func TestVerticalMovementReachesEveryEdge(t *testing.T) {
	for _, w := range []int{5, 8, 20} {
		for _, txt := range fullCorpus() {
			for off := range len([]rune(txt)) + 1 {
				m := newTestModel(w)
				m.SetValue(txt)
				if off > len([]rune(m.Value())) {
					continue
				}
				m.SetCursorOffset(off)
				budget := len(m.layout(true)) + 2

				// A step that leaves the caret exactly where it was is a fixpoint:
				// the row index alone can't say, because the caret-reservation row
				// appears and disappears under it as the caret changes line.
				up := m
				for range budget {
					if visualRow(&up) == 0 {
						break
					}
					r, c := up.row, up.col
					up.cursorUp()
					if up.row == r && up.col == c {
						t.Fatalf("up stuck on visual row %d (w=%d off=%d text %q)", visualRow(&up), w, off, txt)
					}
				}
				if r := visualRow(&up); r != 0 {
					t.Fatalf("up ended on row %d, want 0 (w=%d off=%d text %q)", r, w, off, txt)
				}

				down := m
				for range budget {
					if visualRow(&down) == len(down.layout(true))-1 {
						break
					}
					r, c := down.row, down.col
					down.cursorDown()
					if down.row == r && down.col == c {
						t.Fatalf("down stuck on visual row %d (w=%d off=%d text %q)", visualRow(&down), w, off, txt)
					}
				}
				if r, last := visualRow(&down), len(down.layout(true))-1; r != last {
					t.Fatalf("down ended on row %d, want %d (w=%d off=%d text %q)", r, last, w, off, txt)
				}
			}
		}
	}
}

// TestVerticalRoundTripReturnsToStart: up then down (from a row with a row on
// either side) must come back to the row it left. Drifting means the moves are
// reading a different wrap geometry than the one on screen.
func TestVerticalRoundTripReturnsToStart(t *testing.T) {
	for _, w := range []int{6, 20} {
		for _, txt := range wrapCorpus {
			for off := range len([]rune(txt)) + 1 {
				m := newTestModel(w)
				m.SetValue(txt)
				m.SetCursorOffset(off)
				start, total := visualRow(&m), len(m.layout(true))
				if start == 0 || start >= total-1 {
					continue
				}
				m.cursorUp()
				m.cursorDown()
				if got := visualRow(&m); got != start {
					t.Fatalf("up+down: row %d -> %d (w=%d off=%d text %q)", start, got, w, off, txt)
				}
			}
		}
	}
}

// caretCell locates the drawn reverse-video caret in a rendered View.
func caretCell(m Model) (row, col int, ok bool) {
	m.Styles.Cursor = lipgloss.NewStyle().Reverse(true)
	for r, line := range strings.Split(m.View(), "\n") {
		i := strings.Index(line, "\x1b[7m")
		if i < 0 {
			continue
		}
		return r, textwidth.Width(ansi.Strip(line[:i])), true
	}
	return 0, 0, false
}

// TestDrawnCaretMatchesCursorViewPos: the cell View paints the caret into and
// the cell CursorViewPos hands the owner for the real terminal cursor must be
// the same one, or the NativeCursor composer draws its caret somewhere else
// than the editor thinks the text is.
func TestDrawnCaretMatchesCursorViewPos(t *testing.T) {
	for _, w := range []int{6, 9, 20} {
		for _, txt := range wrapCorpus {
			for off := range len([]rune(txt)) + 1 {
				m := newTestModel(w)
				m.SetValue(txt)
				m.SetCursorOffset(off)
				col, row, ok := m.CursorViewPos()
				dr, dc, dok := caretCell(m)
				if !ok || !dok {
					continue
				}
				if dr != row || dc != col {
					t.Fatalf("caret drawn at (r%d,c%d) but CursorViewPos says (r%d,c%d) (w=%d off=%d text %q)",
						dr, dc, row, col, w, off, txt)
				}
			}
		}
	}
}

// TestClickOnCaretDoesNotMoveIt: clicking the cell the caret is drawn in must
// be a no-op. It closes the loop between the three coordinate spaces — rune
// offset, visual row/column, and screen cell.
func TestClickOnCaretDoesNotMoveIt(t *testing.T) {
	for _, w := range []int{5, 8, 20} {
		for _, txt := range wrapCorpus {
			for off := range len([]rune(txt)) + 1 {
				m := newTestModel(w)
				m.SetValue(txt)
				m.SetCursorOffset(off)
				col, row, ok := m.CursorViewPos()
				if !ok {
					continue
				}
				before := m.CursorOffset()
				m.MoveCursorToVisual(row+m.yOffset, col-m.promptWidth)
				if got := m.CursorOffset(); got != before {
					t.Fatalf("click on the caret at (r%d,c%d) moved it %d -> %d (w=%d text %q)",
						row, col, before, got, w, txt)
				}
			}
		}
	}
}

// TestRenderedRowsFillTheWidth: every rendered row is exactly `width` cells and
// the view is exactly `height` rows — the composer's frame is drawn around this
// block, so a short row tears its right edge open. Includes text whose grapheme
// clusters are narrower than the sum of their runes.
func TestRenderedRowsFillTheWidth(t *testing.T) {
	for _, w := range []int{4, 9, 20} {
		for _, txt := range fullCorpus() {
			for _, h := range []int{1, 3, 7} {
				m := newTestModel(w)
				m.SetHeight(h)
				m.SetValue(txt)
				lines := strings.Split(m.View(), "\n")
				if len(lines) != h {
					t.Fatalf("rendered %d rows, want %d (w=%d text %q)", len(lines), h, w, txt)
				}
				for i, ln := range lines {
					if got := textwidth.Width(ansi.Strip(ln)); got != w {
						t.Fatalf("row %d is %d cells, want %d: %q (text %q)", i, got, w, ansi.Strip(ln), txt)
					}
				}
			}
		}
	}
}

// TestWrapPreservesEveryRune: the layout maps a rune offset to a visual
// position by assuming the sub-lines concatenate back to the source exactly.
func TestWrapPreservesEveryRune(t *testing.T) {
	for _, w := range []int{1, 2, 3, 5, 20} {
		for _, txt := range fullCorpus() {
			for _, line := range strings.Split(txt, "\n") {
				for _, reserve := range []int{0, 1} {
					var b strings.Builder
					for _, sub := range wrapLine([]rune(line), w, reserve) {
						b.WriteString(string(sub))
					}
					if b.String() != line {
						t.Fatalf("wrap(w=%d reserve=%d) turned %q into %q", w, reserve, line, b.String())
					}
				}
			}
		}
	}
}

// TestWrapStaysWithinWidth: no sub-line may overflow the wrap width. Widths
// below 2 are excluded — a 2-cell rune cannot fit and is emitted anyway.
func TestWrapStaysWithinWidth(t *testing.T) {
	for _, w := range []int{2, 3, 5, 20} {
		for _, txt := range fullCorpus() {
			for _, line := range strings.Split(txt, "\n") {
				for i, sub := range wrapLine([]rune(line), w, 0) {
					// Trailing spaces are allowed to spill: they carry no glyph.
					if got := textwidth.Width(strings.TrimRight(string(sub), " ")); got > w {
						t.Fatalf("wrap(w=%d) sub-line %d is %d cells: %q (line %q)", w, i, got, string(sub), line)
					}
				}
			}
		}
	}
}

// TestCaretStaysInScrollWindow: after any cursor placement the caret's row is
// inside [yOffset, yOffset+height) — otherwise the owner is told to place a
// terminal cursor on a row it never rendered.
func TestCaretStaysInScrollWindow(t *testing.T) {
	for _, w := range []int{6, 20} {
		for _, txt := range wrapCorpus {
			for off := range len([]rune(txt)) + 1 {
				m := newTestModel(w)
				m.SetHeight(3)
				m.SetValue(txt)
				m.SetCursorOffset(off)
				if r := visualRow(&m); r < m.yOffset || r >= m.yOffset+m.height {
					t.Fatalf("caret row %d outside window [%d,%d) (w=%d off=%d text %q)",
						r, m.yOffset, m.yOffset+m.height, w, off, txt)
				}
			}
		}
	}
}

// TestScrollOffsetStaysInBounds: yOffset never goes negative or past the last
// screenful of content.
func TestScrollOffsetStaysInBounds(t *testing.T) {
	for _, txt := range wrapCorpus {
		for _, h := range []int{1, 2, 5} {
			m := newTestModel(8)
			m.SetHeight(h)
			m.SetValue(txt)
			m.CursorEnd()
			if maxOff := max(len(m.layout(true))-h, 0); m.yOffset < 0 || m.yOffset > maxOff {
				t.Fatalf("yOffset %d outside [0,%d] (h=%d text %q)", m.yOffset, maxOff, h, txt)
			}
		}
	}
}

// TestDynamicHeightAlwaysFitsTheCaret: with DynamicHeight the window is sized
// from the content, and it must always leave the caret's own row visible.
func TestDynamicHeightAlwaysFitsTheCaret(t *testing.T) {
	for _, txt := range wrapCorpus {
		for off := range len([]rune(txt)) + 1 {
			m := New()
			m.SetWidth(10)
			m.Focus()
			m.DynamicHeight = true
			m.MinHeight, m.MaxHeight = 1, 5
			m.SetValue(txt)
			m.SetCursorOffset(off)
			if r := visualRow(&m); r < m.yOffset || r >= m.yOffset+m.height {
				t.Fatalf("caret row %d outside window [%d,%d) at height %d (off=%d text %q)",
					r, m.yOffset, m.yOffset+m.height, m.height, off, txt)
			}
			if _, _, ok := m.CursorViewPos(); !ok {
				t.Fatalf("CursorViewPos hidden at height %d (off=%d text %q)", m.height, off, txt)
			}
		}
	}
}

// TestResizeKeepsTheCursorPut: the composer is re-widthed on every terminal
// resize, and that must not move the caret in the text or scroll it away.
func TestResizeKeepsTheCursorPut(t *testing.T) {
	for _, txt := range wrapCorpus {
		n := len([]rune(txt))
		for _, off := range []int{0, n / 3, n / 2, n} {
			m := newTestModel(20)
			m.SetHeight(4)
			m.SetValue(txt)
			m.SetCursorOffset(off)
			want := m.CursorOffset()
			for _, w := range []int{7, 40, 3, 20} {
				m.SetWidth(w)
				if got := m.CursorOffset(); got != want {
					t.Fatalf("resize to %d moved the cursor %d -> %d (text %q)", w, want, got, txt)
				}
				if r := visualRow(&m); r < m.yOffset || r >= m.yOffset+m.height {
					t.Fatalf("resize to %d scrolled the caret away (row %d, window [%d,%d), text %q)",
						w, r, m.yOffset, m.yOffset+m.height, txt)
				}
			}
		}
	}
}

// TestHorizontalWalkVisitsEveryOffset: left and right must step through every
// rune offset in order, in both directions, across line breaks.
func TestHorizontalWalkVisitsEveryOffset(t *testing.T) {
	for _, txt := range fullCorpus() {
		m := newTestModel(7)
		m.SetValue(txt)
		n := len([]rune(m.Value()))

		m.MoveToBegin()
		for want := range n + 1 {
			if got := m.CursorOffset(); got != want {
				t.Fatalf("right walk of %q: step %d is at offset %d", txt, want, got)
			}
			m.characterRight()
		}
		m.CursorEnd()
		for want := n; want >= 0; want-- {
			if got := m.CursorOffset(); got != want {
				t.Fatalf("left walk of %q: expected offset %d, got %d", txt, want, got)
			}
			m.characterLeft()
		}
	}
}

// TestWordWalkTerminatesAtBothEnds: word motion must always make progress and
// stop exactly at the buffer ends.
func TestWordWalkTerminatesAtBothEnds(t *testing.T) {
	for _, txt := range fullCorpus() {
		m := newTestModel(7)
		m.SetValue(txt)
		n := len([]rune(m.Value()))

		m.MoveToBegin()
		prev := -1
		for range n + 10 {
			m.wordRight()
			off := m.CursorOffset()
			if off < prev {
				t.Fatalf("wordRight moved backwards in %q: %d -> %d", txt, prev, off)
			}
			if off == prev {
				break
			}
			prev = off
		}
		if prev != n {
			t.Fatalf("wordRight in %q stopped at %d, want %d", txt, prev, n)
		}

		m.CursorEnd()
		prev = n + 1
		for range n + 10 {
			m.wordLeft()
			off := m.CursorOffset()
			if off > prev {
				t.Fatalf("wordLeft moved forwards in %q: %d -> %d", txt, prev, off)
			}
			if off == prev {
				break
			}
			prev = off
		}
		if prev != 0 {
			t.Fatalf("wordLeft in %q stopped at %d, want 0", txt, prev)
		}
	}
}

// TestEditOnACopyLeavesTheOriginalAlone: the Model is passed and returned by
// value all over the ui layer, so a mutation must never write through a shared
// backing array into a copy someone else is still holding.
func TestEditOnACopyLeavesTheOriginalAlone(t *testing.T) {
	ops := map[string]func(*Model){
		"deleteBackward":     (*Model).deleteBackward,
		"deleteForward":      (*Model).deleteForward,
		"deleteWordBackward": (*Model).deleteWordBackward,
		"deleteWordForward":  (*Model).deleteWordForward,
		"deleteAfterCursor":  (*Model).deleteAfterCursor,
		"deleteBeforeCursor": (*Model).deleteBeforeCursor,
		"InsertNewline":      (*Model).InsertNewline,
		"InsertString":       func(m *Model) { m.InsertString("XY") },
		"paste":              func(m *Model) { m.InsertString("one\ntwo") },
	}
	for name, op := range ops {
		orig := newTestModel(40)
		orig.SetValue("hello world\nsecond line\nthird")
		orig.SetCursorOffset(6)
		want := orig.Value()

		cp := orig // the `m.field, _ = m.field.Update(msg)` copy
		op(&cp)

		if got := orig.Value(); got != want {
			t.Errorf("%s on a copy changed the original to %q, want %q", name, got, want)
		}
	}
}

// TestDeleteBackwardRemovesExactlyOneRune sweeps every offset, including the
// line joins.
func TestDeleteBackwardRemovesExactlyOneRune(t *testing.T) {
	txt := "ab\ncd\n\nef"
	rs := []rune(txt)
	for off := 1; off <= len(rs); off++ {
		m := newTestModel(20)
		m.SetValue(txt)
		m.SetCursorOffset(off)
		m.deleteBackward()
		if want := string(rs[:off-1]) + string(rs[off:]); m.Value() != want {
			t.Errorf("backspace at %d: %q, want %q", off, m.Value(), want)
		}
		if m.CursorOffset() != off-1 {
			t.Errorf("backspace at %d left the cursor at %d", off, m.CursorOffset())
		}
	}
}

// TestDeleteForwardRemovesExactlyOneRune is the mirror of the above.
func TestDeleteForwardRemovesExactlyOneRune(t *testing.T) {
	txt := "ab\ncd\n\nef"
	rs := []rune(txt)
	for off := range len(rs) {
		m := newTestModel(20)
		m.SetValue(txt)
		m.SetCursorOffset(off)
		m.deleteForward()
		if want := string(rs[:off]) + string(rs[off+1:]); m.Value() != want {
			t.Errorf("delete at %d: %q, want %q", off, m.Value(), want)
		}
		if m.CursorOffset() != off {
			t.Errorf("delete at %d moved the cursor to %d", off, m.CursorOffset())
		}
	}
}

// TestSetValueSanitizes: a draft restored from disk or a message pulled off the
// server can carry a tab or a stray control rune. A real tab measures as one
// cell and draws as eight, which tears the composer's frame open, so SetValue
// runs the same sanitiser a keystroke does.
func TestSetValueSanitizes(t *testing.T) {
	m := newTestModel(20)
	m.SetValue("a\tb\x07c\rd\r\ne")
	if v := m.Value(); strings.ContainsAny(v, "\t\x07\r") {
		t.Fatalf("SetValue kept a control rune: %q", v)
	}
	if v := m.Value(); !strings.HasPrefix(v, "a b") {
		t.Fatalf("SetValue should turn a tab into a space, got %q", m.Value())
	}
	if n := len(strings.Split(m.Value(), "\n")); n != 3 {
		t.Fatalf("CR and CRLF should each open one line, got %d from %q", n, m.Value())
	}
	// And the rendered rows still fill the width.
	for i, ln := range strings.Split(m.View(), "\n") {
		if got := textwidth.Width(ansi.Strip(ln)); got != 20 {
			t.Fatalf("row %d is %d cells after a tab, want 20: %q", i, got, ansi.Strip(ln))
		}
	}
}

// TestContentHeightCapAppliesToPastes: the cap exists to blunt a pathological
// paste, which arrives through insert rather than SetValue.
func TestContentHeightCapAppliesToPastes(t *testing.T) {
	m := newTestModel(20)
	m.MaxContentHeight = 3
	m.InsertString("a\nb\nc\nd\ne")
	if n := len(strings.Split(m.Value(), "\n")); n > 3 {
		t.Fatalf("paste left %d lines, want at most 3: %q", n, m.Value())
	}
	if m.row >= len(m.lines) || m.col > len(m.lines[m.row]) {
		t.Fatalf("cap left the cursor out of range: row %d col %d of %d lines", m.row, m.col, len(m.lines))
	}
}

// TestCharLimitHoldsAcrossInserts.
func TestCharLimitHoldsAcrossInserts(t *testing.T) {
	m := newTestModel(20)
	m.CharLimit = 10
	m.InsertString(strings.Repeat("a", 50))
	m.InsertString("bbbb")
	if n := len([]rune(m.Value())); n != 10 {
		t.Fatalf("buffer holds %d runes, want 10", n)
	}
}

// TestSelectedTextMatchesTheRange sweeps anchor/head pairs, including backwards
// selections and ones spanning line breaks and wide runes.
func TestSelectedTextMatchesTheRange(t *testing.T) {
	txt := "hello world\nsecond 日本 line\nthird"
	rs := []rune(txt)
	m := newTestModel(20)
	m.SetValue(txt)
	for a := 0; a <= len(rs); a += 3 {
		for b := 0; b <= len(rs); b += 3 {
			m.SetSelection(a, b)
			lo, hi := min(a, b), max(a, b)
			if got, want := m.SelectedText(), string(rs[lo:hi]); got != want {
				t.Fatalf("SelectedText(%d,%d) = %q, want %q", a, b, got, want)
			}
			if lo == hi {
				continue
			}
			if s, e, ok := m.SelectionRange(); !ok || s != lo || e != hi {
				t.Fatalf("SelectionRange(%d,%d) = (%d,%d,%v)", a, b, s, e, ok)
			}
		}
	}
}

// TestDeleteSelectionRemovesExactlyTheRange.
func TestDeleteSelectionRemovesExactlyTheRange(t *testing.T) {
	txt := "hello world\nsecond line\nthird"
	rs := []rune(txt)
	for a := 0; a <= len(rs); a += 2 {
		for b := a; b <= len(rs); b += 2 {
			m := newTestModel(20)
			m.SetValue(txt)
			m.SetSelection(a, b)
			m.DeleteSelection()
			if want := string(rs[:a]) + string(rs[b:]); m.Value() != want {
				t.Fatalf("DeleteSelection(%d,%d) = %q, want %q", a, b, m.Value(), want)
			}
			if m.HasSelection() {
				t.Fatalf("DeleteSelection(%d,%d) left a selection behind", a, b)
			}
		}
	}
}

// TestDragOffThePaneStaysInBounds: a drag can be reported well past the last
// row or column, and the resulting range must still be a valid one.
func TestDragOffThePaneStaysInBounds(t *testing.T) {
	txt := "hello world this is a long wrapped sentence\nsecond\nthird"
	m := newTestModel(9)
	m.SetValue(txt)
	n := len([]rune(txt))
	rows := len(m.layout(true))
	for r := -2; r < rows+2; r++ {
		for c := -2; c < 14; c++ {
			m.BeginSelection(0, 0)
			m.ExtendSelectionToVisual(r, c)
			if s, e, ok := m.SelectionRange(); ok && (s < 0 || e > n || s > e) {
				t.Fatalf("drag to (r%d,c%d) gave range (%d,%d) over %d runes", r, c, s, e, n)
			}
		}
	}
}

// TestWordSelectionNeverCrossesALine — a double-click selects within one
// logical line.
func TestWordSelectionNeverCrossesALine(t *testing.T) {
	txt := "alpha beta\ngamma delta\n\nomega"
	rs := []rune(txt)
	m := newTestModel(20)
	m.SetValue(txt)
	for off := range len(rs) + 1 {
		lo, hi := m.wordBoundsAt(off)
		if lo < 0 || hi > len(rs) || lo > hi {
			t.Fatalf("wordBoundsAt(%d) = (%d,%d)", off, lo, hi)
		}
		if strings.Contains(string(rs[lo:hi]), "\n") {
			t.Fatalf("wordBoundsAt(%d) crossed a newline: %q", off, string(rs[lo:hi]))
		}
	}
}

// TestListContinuationCases covers the markers the composer opens the next item
// with, and the ones that must stay plain text.
func TestListContinuationCases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"- item", "- item\n- "},
		{"* item", "* item\n* "},
		{"+ item", "+ item\n+ "},
		{"1. item", "1. item\n2. "},
		{"3) item", "3) item\n4) "},
		{"  - nested", "  - nested\n  - "},
		{"- [ ] task", "- [ ] task\n- [ ] "},
		{"- [x] done", "- [x] done\n- [ ] "},
		{"not a list", "not a list\n"},
		{"1.5 not a list", "1.5 not a list\n"},
		{"*bold*", "*bold*\n"},
		{"--", "--\n"},
	}
	for _, c := range cases {
		m := newTestModel(40)
		m.ContinueLists = true
		m.SetValue(c.in)
		m.InsertNewline()
		if got := m.Value(); got != c.want {
			t.Errorf("newline after %q = %q, want %q", c.in, got, c.want)
		}
	}
	// A second newline on the still-empty item ends the list.
	m := newTestModel(40)
	m.ContinueLists = true
	m.SetValue("- item")
	m.InsertNewline()
	m.InsertNewline()
	if got := m.Value(); got != "- item\n" {
		t.Errorf("a newline on the empty item should end the list, got %q", got)
	}
}

// TestStylingNeverChangesTheText: markdown highlighting, decorations, the
// command shimmer and the ghost hint are all pure styling — the plain text of
// the render must be identical with and without them.
func TestStylingNeverChangesTheText(t *testing.T) {
	texts := []string{
		"**bold** and _italic_ and ~~strike~~ and `code`",
		"```\nfenced\n```",
		"unclosed **bold",
		"`code with **stars**`",
		"hello world this is a wrapped sentence\nsecond line",
	}
	underline := lipgloss.NewStyle().Underline(true)
	for _, txt := range texts {
		base := newTestModel(12)
		base.SetHeight(8)
		base.SetValue(txt)
		want := strings.Join(plainLines(base.View()), "\n")

		md := base
		md.MarkdownHighlight = true
		if got := strings.Join(plainLines(md.View()), "\n"); got != want {
			t.Errorf("markdown highlighting changed the text of %q:\n got %q\nwant %q", txt, got, want)
		}

		for a := 0; a < len([]rune(txt)); a += 5 {
			dec := base
			dec.SetDecorations([]Decoration{{Start: a, End: a + 7, Style: underline}})
			if got := strings.Join(plainLines(dec.View()), "\n"); got != want {
				t.Errorf("decoration [%d,%d) changed the text of %q", a, a+7, txt)
			}
		}

		cmd := base
		cmd.SetCommandSpan(0, 4)
		if got := strings.Join(plainLines(cmd.View()), "\n"); got != want {
			t.Errorf("command span changed the text of %q", txt)
		}
	}
}

// TestOutOfRangeOverlaysAreHarmless: decoration and command-span offsets are
// pushed in from the ui layer and can lag a keystroke behind the buffer.
func TestOutOfRangeOverlaysAreHarmless(t *testing.T) {
	m := newTestModel(12)
	m.SetHeight(4)
	m.SetValue("hello world")
	n := len([]rune(m.Value()))
	m.SetDecorations([]Decoration{{Start: -50, End: 5}, {Start: n + 10, End: n + 40}, {Start: 5, End: 2}})
	m.SetCommandSpan(-5, 500)
	for i, ln := range strings.Split(m.View(), "\n") {
		if got := textwidth.Width(ansi.Strip(ln)); got != 12 {
			t.Fatalf("row %d is %d cells with stale overlays, want 12", i, got)
		}
	}
}

// TestGhostHintNeverWidensARow: the ghost is virtual text drawn past the caret
// and must stay inside the content width and out of the buffer.
func TestGhostHintNeverWidensARow(t *testing.T) {
	for _, ghost := range []string{"[message]", strings.Repeat("x", 80)} {
		m := newTestModel(20)
		m.SetHeight(3)
		m.SetValue("/dm ")
		m.SetGhost(ghost)
		for i, ln := range strings.Split(m.View(), "\n") {
			if got := textwidth.Width(ansi.Strip(ln)); got != 20 {
				t.Fatalf("ghost %q made row %d %d cells, want 20", ghost, i, got)
			}
		}
		if m.Value() != "/dm " {
			t.Fatalf("ghost leaked into the buffer: %q", m.Value())
		}
	}
}

// TestPromptIsAskedForConsecutiveVisualLines: the promptFunc argument is the
// global visual line index, which is what lets the composer draw "> " only on
// the first row of the draft.
func TestPromptIsAskedForConsecutiveVisualLines(t *testing.T) {
	var seen []int
	m := New()
	m.SetWidth(12)
	m.SetHeight(6)
	m.Focus()
	m.SetPromptFunc(2, func(i int, _ bool) string {
		seen = append(seen, i)
		return "> "
	})
	m.SetValue("aaa bbb ccc ddd\nshort")
	_ = m.View()
	for i, got := range seen {
		if got != i {
			t.Fatalf("prompt asked for lines %v, want 0..n", seen)
		}
	}
}

// TestTableEditingSurvivesAnyInput types awkward pipe-table source in a
// character at a time and then backspaces it all out, checking the cursor stays
// in range throughout — the table code rewrites whole line blocks under it.
func TestTableEditingSurvivesAnyInput(t *testing.T) {
	inputs := []string{
		"| a | b |\n|---|---|\n| 1 | 2 |",
		"| a | b |",
		"|",
		"||",
		"|||",
		"| a \\| b | c |",
		"|:-:|:--|--:|",
		"| very long cell content here | x |",
		"|a|b|\n|-|-|\n",
	}
	for _, in := range inputs {
		for _, w := range []int{6, 12, 40} {
			m := newTableModel(w)
			for _, r := range in {
				if r == '\n' {
					m.InsertNewline()
				} else {
					m.InsertRune(r)
				}
				if m.row < 0 || m.row >= len(m.lines) || m.col < 0 || m.col > len(m.lines[m.row]) {
					t.Fatalf("typing %q at w=%d put the cursor at row %d col %d of %d lines",
						in, w, m.row, m.col, len(m.lines))
				}
			}
			_ = m.View()
			for range len([]rune(m.Value())) + 5 {
				m.deleteBackward()
				if m.row < 0 || m.row >= len(m.lines) || m.col < 0 || m.col > len(m.lines[m.row]) {
					t.Fatalf("backspacing %q at w=%d put the cursor at row %d col %d", in, w, m.row, m.col)
				}
			}
			if m.Value() != "" {
				t.Errorf("backspacing %q at w=%d left %q", in, w, m.Value())
			}
		}
	}
}

// TestTableTabStaysInsideTheTable.
func TestTableTabStaysInsideTheTable(t *testing.T) {
	m := newTableModel(40)
	m.SetValue("| a | b | c |\n|---|---|---|\n| 1 | 2 | 3 |")
	m.SetCursorOffset(2)
	seen := map[int]bool{}
	for range 30 {
		if !m.NextTableCell(1) {
			break
		}
		seen[m.CursorOffset()] = true
		if m.row < 0 || m.row >= len(m.lines) || m.col > len(m.lines[m.row]) {
			t.Fatalf("tab left the buffer at row %d col %d", m.row, m.col)
		}
	}
	if len(seen) < 4 {
		t.Errorf("tab visited only %d cells", len(seen))
	}
}

// TestTableRealignKeepsEveryCell — realignment repads the block, and must not
// drop content doing it, at any width.
func TestTableRealignKeepsEveryCell(t *testing.T) {
	for _, w := range []int{10, 24, 60} {
		m := newTableModel(w)
		m.SetValue("|alpha|beta|\n|---|---|\n|gamma|delta|")
		m.SetCursorOffset(3)
		m.InsertRune('X')
		for _, want := range []string{"alXpha", "beta", "gamma", "delta"} {
			if !strings.Contains(m.Value(), want) {
				t.Errorf("w=%d: realign lost %q from:\n%s", w, want, m.Value())
			}
		}
	}
}
