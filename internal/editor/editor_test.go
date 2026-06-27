package editor

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
)

// keyPress builds a printable key press carrying the given text.
func keyPress(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: s, Code: []rune(s)[0]}
}

// typeString feeds each rune as a separate key press, the way a terminal does.
func typeString(m Model, s string) Model {
	for _, r := range s {
		m, _ = m.Update(keyPress(string(r)))
	}
	return m
}

func newTestModel(width int) Model {
	m := New()
	m.SetWidth(width)
	m.Focus()
	return m
}

func TestValueRoundTrip(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello\nworld")
	if got := m.Value(); got != "hello\nworld" {
		t.Fatalf("Value = %q, want %q", got, "hello\nworld")
	}
}

func TestTypingAppends(t *testing.T) {
	m := newTestModel(40)
	m = typeString(m, "hi there")
	if got := m.Value(); got != "hi there" {
		t.Fatalf("Value = %q, want %q", got, "hi there")
	}
	if off := m.CursorOffset(); off != len("hi there") {
		t.Fatalf("cursor offset = %d, want %d", off, len("hi there"))
	}
}

func TestCursorOffsetRoundTrip(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello\nbig world\nfoo")
	for off := 0; off <= len([]rune(m.Value())); off++ {
		m.SetCursorOffset(off)
		if got := m.CursorOffset(); got != off {
			t.Fatalf("offset round trip: set %d, got %d", off, got)
		}
	}
}

func TestInsertMidline(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("helo")
	m.SetCursorOffset(3) // between 'l' and 'o'
	m, _ = m.Update(keyPress("l"))
	if got := m.Value(); got != "hello" {
		t.Fatalf("Value = %q, want %q", got, "hello")
	}
	if off := m.CursorOffset(); off != 4 {
		t.Fatalf("cursor offset = %d, want 4", off)
	}
}

func TestNewlineSplitsLine(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("abcd")
	m.SetCursorOffset(2)
	m.InsertNewline()
	if got := m.Value(); got != "ab\ncd" {
		t.Fatalf("Value = %q, want %q", got, "ab\ncd")
	}
	row, col := m.CursorRowCol()
	if row != 1 || col != 0 {
		t.Fatalf("cursor = (%d,%d), want (1,0)", row, col)
	}
}

func TestBackspaceJoinsLines(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("ab\ncd")
	m.SetCursorOffset(3) // start of "cd"
	bs := tea.KeyPressMsg{Code: tea.KeyBackspace}
	m, _ = m.Update(bs)
	if got := m.Value(); got != "abcd" {
		t.Fatalf("Value = %q, want %q", got, "abcd")
	}
	if off := m.CursorOffset(); off != 2 {
		t.Fatalf("cursor offset = %d, want 2", off)
	}
}

func TestDeleteForwardJoinsLines(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("ab\ncd")
	m.SetCursorOffset(2) // end of "ab"
	del := tea.KeyPressMsg{Code: tea.KeyDelete}
	m, _ = m.Update(del)
	if got := m.Value(); got != "abcd" {
		t.Fatalf("Value = %q, want %q", got, "abcd")
	}
}

func TestDeleteWordBackward(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("foo bar baz")
	m.CursorEnd()
	m.deleteWordBackward()
	if got := m.Value(); got != "foo bar " {
		t.Fatalf("Value = %q, want %q", got, "foo bar ")
	}
}

func TestCharLimit(t *testing.T) {
	m := newTestModel(40)
	m.CharLimit = 3
	m = typeString(m, "abcdef")
	if got := m.Value(); got != "abc" {
		t.Fatalf("Value = %q, want %q (char limit)", got, "abc")
	}
}

func TestHorizontalMovementAcrossLines(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("ab\ncd")
	m.MoveToBegin()
	// right past end of line 0 wraps to start of line 1
	for range 3 {
		m.characterRight()
	}
	row, col := m.CursorRowCol()
	if row != 1 || col != 0 {
		t.Fatalf("after 3 rights cursor = (%d,%d), want (1,0)", row, col)
	}
	m.characterLeft()
	row, col = m.CursorRowCol()
	if row != 0 || col != 2 {
		t.Fatalf("after left cursor = (%d,%d), want (0,2)", row, col)
	}
}

func TestVerticalMovementPreservesColumn(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello\nhi\nworld")
	// place on row 0 col 4 ('o')
	m.SetCursorOffset(4)
	m.refreshDesired()
	m.cursorDown() // row 1 "hi" len 2 → clamps to col 2
	if r, c := m.CursorRowCol(); r != 1 || c != 2 {
		t.Fatalf("down1 = (%d,%d), want (1,2)", r, c)
	}
	m.cursorDown() // row 2 "world" → should restore toward desired col 4
	if r, c := m.CursorRowCol(); r != 2 || c != 4 {
		t.Fatalf("down2 = (%d,%d), want (2,4)", r, c)
	}
}

// --- wrap & rendering -------------------------------------------------------

func plainLines(view string) []string {
	out := strings.Split(view, "\n")
	for i, l := range out {
		out[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return out
}

func TestSoftWrapRendersAcrossVisualRows(t *testing.T) {
	// width 10, prompt 0 → content width 10. "aaaa bbbb cccc" wraps.
	m := newTestModel(10)
	m.DynamicHeight = true
	m.MaxHeight = 10
	m.SetValue("aaaa bbbb cccc dddd")
	view := m.View()
	pl := plainLines(view)
	joined := strings.Join(pl, "|")
	// All words present, broken into multiple rows.
	if !strings.Contains(joined, "aaaa") || !strings.Contains(joined, "dddd") {
		t.Fatalf("wrapped view missing content: %q", joined)
	}
	if len(pl) < 2 {
		t.Fatalf("expected multiple visual rows, got %d: %q", len(pl), joined)
	}
}

func TestVisualRowCountMatchesWrap(t *testing.T) {
	m := newTestModel(5)     // content width 5
	m.SetValue("abcdefghij") // 10 cols → 2 rows
	if n := m.visualRowCount(); n != 2 {
		t.Fatalf("visualRowCount = %d, want 2", n)
	}
}

// --- decorations under scroll (the bug this package fixes) ------------------

func underlineRanges(line string) []string {
	// Returns the plain substrings carrying an SGR underline. lipgloss emits
	// the underline per-rune (…4:3m X \x1b[m …), so adjacent underlined runes
	// are merged: a range only closes when a *visible* character is seen while
	// underline is off.
	var out []string
	var cur strings.Builder
	on := false
	i := 0
	for i < len(line) {
		if line[i] == 0x1b { // ESC — start of an SGR sequence
			j := strings.IndexByte(line[i:], 'm')
			if j < 0 {
				break
			}
			params := line[i+2 : i+j] // between "\x1b[" and "m"
			switch {
			case params == "" || params == "0" || strings.Contains(params, "4:0"):
				on = false
			case strings.Contains(params, "4:3") || params == "4" ||
				strings.HasPrefix(params, "4;") || strings.Contains(params, ";4;"):
				on = true
			}
			i += j + 1
			continue
		}
		if on {
			cur.WriteByte(line[i])
		} else if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		i++
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func curlyDecor(start, end int) Decoration {
	return Decoration{
		Start: start,
		End:   end,
		Style: lipgloss.NewStyle().
			UnderlineStyle(lipgloss.UnderlineCurly).
			UnderlineColor(lipgloss.Color("#ff5555")),
	}
}

func TestDecorationRendersOnCorrectWord(t *testing.T) {
	m := newTestModel(40)
	m.Blur() // avoid cursor cell interfering
	m.SetValue("the quik brown fox")
	// underline "quik" at offset 4..8
	m.SetDecorations([]Decoration{curlyDecor(4, 8)})
	view := m.View()
	got := underlineRanges(view)
	if len(got) != 1 || got[0] != "quik" {
		t.Fatalf("underlined = %v, want [quik]", got)
	}
}

func TestDecorationStaysAlignedWhenScrolled(t *testing.T) {
	// The regression target: when the field scrolls, a decoration on a visible
	// line must render at the right place, and one on a scrolled-off line must
	// not appear at all.
	m := newTestModel(20)
	m.Blur()
	m.DynamicHeight = true
	m.MinHeight = 1
	m.MaxHeight = 3 // only 3 visual rows visible
	var b strings.Builder
	for i := range 8 {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("line ")
		b.WriteByte(byte('a' + i))
		b.WriteString(" wrongg")
	}
	m.SetValue(b.String()) // 8 logical lines, each "line X wrongg"

	// Decorate "wrongg" on the LAST logical line. Compute its offset.
	val := m.Value()
	lastLineStart := strings.LastIndex(val, "line h wrongg")
	wOff := lastLineStart + len("line h ")
	m.SetDecorations([]Decoration{curlyDecor(wOff, wOff+len("wrongg"))})

	// Cursor/scroll: move cursor to the end so the view scrolls to the bottom.
	m.Focus()
	m.CursorEnd()
	m.Blur()

	view := m.View()
	if got := underlineRanges(view); len(got) != 1 || got[0] != "wrongg" {
		t.Fatalf("scrolled-to-bottom underline = %v, want [wrongg]; view:\n%s", got, view)
	}

	// Now decorate "wrongg" on the FIRST logical line (offset within "line a ..").
	firstW := len("line a ")
	m.SetDecorations([]Decoration{curlyDecor(firstW, firstW+len("wrongg"))})
	view = m.View()
	if got := underlineRanges(view); len(got) != 0 {
		t.Fatalf("first-line decoration should be scrolled off, but rendered %v; view:\n%s", got, view)
	}
}

func TestScrollOffsetTracksCursor(t *testing.T) {
	m := newTestModel(20)
	m.DynamicHeight = true
	m.MinHeight = 1
	m.MaxHeight = 3
	m.SetValue("l0\nl1\nl2\nl3\nl4\nl5")
	m.Focus()
	m.CursorEnd() // cursor on l5 (row 5), only 3 rows tall
	if off := m.ScrollYOffset(); off == 0 {
		t.Fatalf("expected scroll > 0 with cursor at bottom, got 0")
	}
	// Cursor's visual row (5) must be within the window.
	if off := m.ScrollYOffset(); off+m.Height() <= 5 {
		t.Fatalf("cursor row 5 not in window [%d,%d)", off, off+m.Height())
	}
	m.MoveToBegin()
	if off := m.ScrollYOffset(); off != 0 {
		t.Fatalf("scroll should return to 0 at top, got %d", off)
	}
}

func TestDynamicHeightGrowsAndClamps(t *testing.T) {
	m := newTestModel(40)
	m.DynamicHeight = true
	m.MinHeight = 1
	m.MaxHeight = 3
	if m.Height() != 1 {
		t.Fatalf("empty height = %d, want 1", m.Height())
	}
	m.SetValue("a\nb")
	if m.Height() != 2 {
		t.Fatalf("2-line height = %d, want 2", m.Height())
	}
	m.SetValue("a\nb\nc\nd\ne")
	if m.Height() != 3 {
		t.Fatalf("5-line height = %d, want 3 (clamped)", m.Height())
	}
}

func TestPlaceholderShownWhenEmpty(t *testing.T) {
	m := newTestModel(40)
	m.Blur()
	m.Placeholder = "type here"
	view := m.View()
	if !strings.Contains(ansi.Strip(view), "type here") {
		t.Fatalf("placeholder not rendered: %q", ansi.Strip(view))
	}
	// Once content is typed, placeholder is gone.
	m.Focus()
	m = typeString(m, "x")
	if strings.Contains(ansi.Strip(m.View()), "type here") {
		t.Fatalf("placeholder should be hidden once typing")
	}
}

func TestPromptRendersOnFirstVisualLineOnly(t *testing.T) {
	m := newTestModel(12)
	m.DynamicHeight = true
	m.MaxHeight = 10
	m.SetPromptFunc(2, func(line int, _ bool) string {
		if line == 0 {
			return "> "
		}
		return "  "
	})
	m.Blur()
	m.SetValue("hello world foo bar") // wraps
	pl := strings.Split(m.View(), "\n")
	if !strings.HasPrefix(ansi.Strip(pl[0]), "> ") {
		t.Fatalf("first line should start with prompt: %q", ansi.Strip(pl[0]))
	}
	if len(pl) > 1 && strings.HasPrefix(ansi.Strip(pl[1]), "> ") {
		t.Fatalf("continuation line should not repeat prompt: %q", ansi.Strip(pl[1]))
	}
}

func TestCursorAtEndOfFullLineStaysInWidth(t *testing.T) {
	// Cursor parked just past a sub-line that exactly fills the content width
	// must not overflow the box — it lands on a phantom continuation row.
	m := newTestModel(8) // content width 8
	m.DynamicHeight = true
	m.MaxHeight = 5
	m.Focus()
	m.SetValue("abcdefgh") // exactly fills width 8
	m.CursorEnd()
	for line := range strings.SplitSeq(m.View(), "\n") {
		if w := ansi.StringWidth(line); w != 8 {
			t.Fatalf("line width = %d, want 8: %q", w, ansi.Strip(line))
		}
	}
	// The cursor should be visible (its own row exists).
	if m.Height() < 2 {
		t.Fatalf("expected a phantom row for the end cursor, height = %d", m.Height())
	}
}

func TestTrailingWordWrapsWithCursorAtEndOfLine(t *testing.T) {
	// Regression: when a word at the end of a full line grows past the content
	// width, the whole word must wrap down to the next visual row with the cursor
	// following it — matching the original textarea, which reserved a cell for the
	// end-of-line cursor so "foo bar baz|" wrapped to "foo bar" / "baz|".
	//
	//   content width 11, buffer "foo bar ba|", press z:
	//
	//     want:            not (current bug):
	//     | foo bar     |  | foo bar baz |
	//     | baz|        |  | |           |
	m := newTestModel(11)
	m.DynamicHeight = true
	m.MaxHeight = 5
	m.SetValue("foo bar ba")
	m.CursorEnd()
	m, _ = m.Update(keyPress("z")) // "foo bar baz", cursor at end

	pl := plainLines(m.View())
	if len(pl) < 2 {
		t.Fatalf("expected 2 visual rows, got %d: %q", len(pl), pl)
	}
	if pl[0] != "foo bar" {
		t.Errorf("row 0 = %q, want %q (trailing word should wrap down)", pl[0], "foo bar")
	}
	if pl[1] != "baz" {
		t.Errorf("row 1 = %q, want %q (wrapped word carries the cursor)", pl[1], "baz")
	}
	if r := m.CursorVisualRow(); r != 1 {
		t.Errorf("cursor visual row = %d, want 1", r)
	}
}

func TestPasteInsertsContent(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("ab")
	m.SetCursorOffset(1)
	m, _ = m.Update(tea.PasteMsg{Content: "XYZ"})
	if got := m.Value(); got != "aXYZb" {
		t.Fatalf("Value = %q, want %q", got, "aXYZb")
	}
}

func TestPasteWithNewlinesCreatesLines(t *testing.T) {
	m := newTestModel(40)
	m, _ = m.Update(tea.PasteMsg{Content: "one\ntwo\nthree"})
	if got := m.Value(); got != "one\ntwo\nthree" {
		t.Fatalf("Value = %q, want %q", got, "one\ntwo\nthree")
	}
}

func TestViewWidthIsStable(t *testing.T) {
	// Each rendered line must be exactly `width` cells wide (prompt + content).
	m := newTestModel(16)
	m.SetPromptFunc(2, func(int, bool) string { return "> " })
	m.Focus()
	m.SetValue("hello world this is a longer message")
	for line := range strings.SplitSeq(m.View(), "\n") {
		if w := ansi.StringWidth(line); w != 16 {
			t.Fatalf("rendered line width = %d, want 16: %q", w, ansi.Strip(line))
		}
	}
}
