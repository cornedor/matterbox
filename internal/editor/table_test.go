package editor

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// newTableModel returns an editor with table editing on, typed into as the
// composer is.
func newTableModel(width int) Model {
	m := newTestModel(width)
	m.ContinueTables = true
	return m
}

// tab presses tab (or shift+tab), going through Update the way a keystroke does.
func tab(m Model, back bool) Model {
	code := tea.KeyTab
	mod := tea.KeyMod(0)
	if back {
		mod = tea.ModShift
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
	return m
}

// backspace / del press the delete keys, going through Update as a keystroke does.
func backspace(m Model) Model {
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	return m
}

func del(m Model) Model {
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	return m
}

// A newline on a header opens the |---| separator and the first data row, with
// the caret waiting in its first cell.
func TestTableNewlineOpensSeparatorAndRow(t *testing.T) {
	m := enter(typeString(newTableModel(60), "| Name | Qty |"))
	want := "| Name | Qty |\n| ---- | --- |\n|      |     |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
	// The caret sits in the first cell of the new row, past its pad space.
	if got, want := m.CursorOffset(), len("| Name | Qty |\n| ---- | --- |\n| "); got != want {
		t.Fatalf("cursor offset = %d, want %d", got, want)
	}
	m = typeString(m, "Milk")
	want = "| Name | Qty |\n| ---- | --- |\n| Milk |     |"
	if got := m.Value(); got != want {
		t.Fatalf("after typing Value =\n%q\nwant\n%q", got, want)
	}
}

// A newline in a data row opens the next one; the columns re-pad to the widest
// cell as the rows are filled in.
func TestTableNewlineOpensNextRowAndResizes(t *testing.T) {
	m := enter(typeString(newTableModel(60), "| A | B |"))
	m = typeString(m, "Milk")
	m = enter(m)
	m = typeString(m, "Chocolate")
	want := "| A         | B   |\n| --------- | --- |\n| Milk      |     |\n| Chocolate |     |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
	// The widened column carried the caret with it: it is still after "Chocolate".
	if got, want := m.CursorOffset(), strings.Index(want, "Chocolate")+len("Chocolate"); got != want {
		t.Fatalf("cursor offset = %d, want %d", got, want)
	}
}

// The heart of live realignment: a space typed inside a cell must survive the
// re-pad that same keystroke triggers, or a two-word cell could never be typed.
func TestTableTypingSpaceInCellIsKept(t *testing.T) {
	m := enter(typeString(newTableModel(60), "| A | B |"))
	m = typeString(m, "Full fat milk")
	want := "| A             | B   |\n| ------------- | --- |\n| Full fat milk |     |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
}

// A newline on a row that was left empty ends the table, dropping the row.
func TestTableNewlineOnEmptyRowEndsTable(t *testing.T) {
	m := enter(typeString(newTableModel(60), "| A | B |"))
	m = typeString(m, "Milk")
	m = enter(m) // opens a row...
	m = enter(m) // ...which is still empty, so the table ends
	want := "| A    | B   |\n| ---- | --- |\n| Milk |     |\n"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
	// Typing carries on as an ordinary paragraph, outside the table.
	typed := typeString(m, "done")
	if got := typed.Value(); got != want+"done" {
		t.Fatalf("after typing: Value = %q, want %q", got, want+"done")
	}
}

// Tab steps to the next cell, and off the last one it opens a fresh row.
func TestTableTabStepsCells(t *testing.T) {
	m := enter(typeString(newTableModel(60), "| A | B |"))
	m = typeString(m, "Milk")
	m = tab(m, false)
	m = typeString(m, "2")
	want := "| A    | B   |\n| ---- | --- |\n| Milk | 2   |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
	// Tab off the last cell opens the next row, caret in its first cell.
	m = tab(m, false)
	m = typeString(m, "Eggs")
	want = "| A    | B   |\n| ---- | --- |\n| Milk | 2   |\n| Eggs |     |"
	if got := m.Value(); got != want {
		t.Fatalf("after tab off the end Value =\n%q\nwant\n%q", got, want)
	}
	// Shift+tab from the first cell steps back to the previous row's last cell.
	m = tab(m, true)
	m = typeString(m, "4")
	want = "| A    | B   |\n| ---- | --- |\n| Milk | 24  |\n| Eggs |     |"
	if got := m.Value(); got != want {
		t.Fatalf("after shift+tab Value =\n%q\nwant\n%q", got, want)
	}
}

// The separator row's colons survive a re-pad, and they steer the padding: a
// right-aligned column pads on the left.
func TestTableKeepsAlignmentColons(t *testing.T) {
	m := newTableModel(60)
	m.SetValue("| Item | Cost |\n|:--|--:|\n| Milk | 2 |")
	m.SetCursorOffset(len("| Item | Cost |\n|:--|--:|\n| Milk"))
	m = typeString(m, "shake")
	want := "| Item      | Cost |\n| :-------- | ---: |\n| Milkshake |    2 |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
}

// A table too wide to pad is written in its compact form rather than having its
// cells cut down to fit: the text is the message.
func TestTableTooWideDropsPaddingRatherThanContent(t *testing.T) {
	m := newTableModel(24)
	m.SetValue("| Item | Cost |\n| --- | --- |\n| A very long cell indeed | 2 |")
	m.SetCursorOffset(len("| Item"))
	m = typeString(m, "s")
	want := "| Items | Cost |\n| --- | --- |\n| A very long cell indeed | 2 |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
	// Widen the editor and the same table pads out.
	m.SetWidth(60)
	m = typeString(m, "!")
	want = "| Items!                  | Cost |\n| ----------------------- | ---- |\n| A very long cell indeed | 2    |"
	if got := m.Value(); got != want {
		t.Fatalf("after widening Value =\n%q\nwant\n%q", got, want)
	}
}

// An escaped pipe is cell content, not a separator, and stays that way.
func TestTableEscapedPipeIsContent(t *testing.T) {
	m := newTableModel(60)
	m.SetValue(`| A | B |
| --- | --- |
| a \| b | c |`)
	m.SetCursorOffset(len(`| A | B |
| --- | --- |
| a \| b`))
	m = typeString(m, "!")
	want := "| A       | B   |\n| ------- | --- |\n| a \\| b! | c   |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
}

// Prose with a stray pipe in it is not a table: one bar is not a row.
func TestTableNeedsTwoBars(t *testing.T) {
	m := typeString(newTableModel(60), "| a b c")
	m = enter(m)
	if got, want := m.Value(), "| a b c\n"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}
}

// A caret at the very start of a row breaks the line like any other text, so
// there is still a way to open space above a table.
func TestTableNewlineAtRowStartSplits(t *testing.T) {
	m := newTableModel(60)
	m.SetValue("| A | B |")
	m.SetCursorOffset(0)
	m = enter(m)
	if got, want := m.Value(), "\n| A | B |"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}
}

// Inside a fenced code block a table-looking line is code: it is neither padded
// nor continued.
func TestTableSkipsCodeBlock(t *testing.T) {
	m := newTableModel(60)
	m.SetValue("```\n| A | B |")
	m = enter(m)
	if got, want := m.Value(), "```\n| A | B |\n"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}
	m = typeString(m, "| c | d |")
	if got, want := m.Value(), "```\n| A | B |\n| c | d |"; got != want {
		t.Fatalf("code-block row was reflowed: Value = %q, want %q", got, want)
	}
}

// Off by default: an editor that didn't opt in (the SQL tab) is untouched.
func TestTableOffByDefault(t *testing.T) {
	m := typeString(newTestModel(60), "| A | B |")
	m = enter(m)
	if got, want := m.Value(), "| A | B |\n"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}
	if m.InTableRow() {
		t.Fatal("InTableRow = true for an editor without ContinueTables")
	}
}

// Padding never costs the message its last characters: with the buffer up against
// CharLimit the table is left compact rather than padded past the cap.
func TestTableRespectsCharLimit(t *testing.T) {
	const table = "| A | Bee |\n| --- | --- |\n| Milk | 2 |"
	m := newTableModel(60)
	m.CharLimit = len(table) + 1 // room for the keystroke below, not for padding
	m.SetValue(table)
	m.SetCursorOffset(len("| A"))
	m = typeString(m, "!")
	if got, want := m.Value(), "| A! | Bee |\n| --- | --- |\n| Milk | 2 |"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}
}

// Widths are display cells, so a CJK or emoji cell lines the columns up by what
// the terminal actually draws, not by rune count.
func TestTableAlignsByDisplayWidth(t *testing.T) {
	m := newTableModel(60)
	m.SetValue("| A | B |\n| --- | --- |\n| 日本語 | x |\n| ab | y |")
	m.SetCursorOffset(len("| A"))
	m = typeString(m, "!")
	want := "| A!     | B   |\n| ------ | --- |\n| 日本語 | x   |\n| ab     | y   |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
}

// The property that keeps a typist out of trouble: every backspace moves
// something. A held backspace walks a table apart — content, then rows — and never
// stalls on a bar or a pad space the realign would only put back.
func TestTableBackspaceAlwaysMakesProgress(t *testing.T) {
	m := newTableModel(60)
	m.SetValue("| Item | Qty |\n| ---- | --- |\n| Milk | 2   |\n| Eggs | 12  |")
	m.CursorEnd()
	for i := 0; !isEmptyBuffer(m); i++ {
		if i > 200 {
			t.Fatalf("backspace never emptied the buffer; stuck at %q", m.Value())
		}
		before, beforeCol := m.Value(), m.CursorOffset()
		m = backspace(m)
		if m.Value() == before && m.CursorOffset() == beforeCol {
			t.Fatalf("backspace #%d was a dead key at offset %d: %q", i, beforeCol, before)
		}
	}
}

// The same for the delete key, worked from the front of the table.
func TestTableDeleteForwardAlwaysMakesProgress(t *testing.T) {
	m := newTableModel(60)
	m.SetValue("| Item | Qty |\n| ---- | --- |\n| Milk | 2   |\n| Eggs | 12  |")
	m.MoveToBegin()
	for i := 0; !isEmptyBuffer(m); i++ {
		if i > 200 {
			t.Fatalf("delete never emptied the buffer; stuck at %q", m.Value())
		}
		before, beforeCol := m.Value(), m.CursorOffset()
		m = del(m)
		if m.Value() == before && m.CursorOffset() == beforeCol {
			t.Fatalf("delete #%d was a dead key at offset %d: %q", i, beforeCol, before)
		}
	}
}

func isEmptyBuffer(m Model) bool { return m.Value() == "" }

// A backspace in a row left empty takes the row back, caret to the row above.
func TestTableBackspaceOnEmptyRowDropsIt(t *testing.T) {
	m := enter(typeString(newTableModel(60), "| A | B |"))
	m = typeString(m, "Milk")
	m = enter(m) // opens an empty row
	m = backspace(m)
	want := "| A    | B   |\n| ---- | --- |\n| Milk |     |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
	if got, want := m.CursorOffset(), len(want); got != want {
		t.Fatalf("cursor offset = %d, want %d (end of the row above)", got, want)
	}
}

// The separator's dashes are drawn from the column widths, so a backspace takes the
// whole row rather than a dash that would come straight back. A colon is the
// typist's own and deletes as itself, dropping the column's alignment.
func TestTableBackspaceInSeparatorRow(t *testing.T) {
	m := newTableModel(60)
	m.SetValue(alignedTable)
	m.SetCursorOffset(len("| A    | B   |\n| ---"))
	m = backspace(m)
	want := "| A    | B |\n| Milk | 2 |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}

	m = newTableModel(60)
	m.SetValue("| A    | B   |\n| :--- | --- |\n| Milk | 2   |")
	m.SetCursorOffset(len("| A    | B   |\n| :"))
	m = backspace(m)
	want = "| A    | B   |\n| ---- | --- |\n| Milk | 2   |"
	if got := m.Value(); got != want {
		t.Fatalf("after deleting the colon Value =\n%q\nwant\n%q", got, want)
	}
}

// alignedTable is a table already laid out the way the editor would lay it out, so
// a test can seed it with SetValue (which doesn't realign — only an edit does) and
// count offsets straight off the text.
const alignedTable = "| A    | B   |\n| ---- | --- |\n| Milk | 2   |"

// A backspace at the head of a cell's content steps back over the bar into the cell
// before, rather than eating a pad space the realign would put back.
func TestTableBackspaceAtCellHeadStepsBack(t *testing.T) {
	m := newTableModel(60)
	m.SetValue(alignedTable)
	m.SetCursorOffset(len("| A    | B   |\n| ---- | --- |\n| Milk | "))
	m = backspace(m)
	if got := m.Value(); got != alignedTable {
		t.Fatalf("the row was changed: %q", got)
	}
	if got, want := m.CursorOffset(), len("| A    | B   |\n| ---- | --- |\n| Milk"); got != want {
		t.Fatalf("cursor offset = %d, want %d (end of the cell before)", got, want)
	}
	// And from there it deletes text, as ever — the column narrowing to suit.
	m = backspace(m)
	if got, want := m.Value(), "| A   | B   |\n| --- | --- |\n| Mil | 2   |"; got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
}

// A backspace at the head of the table's first row is the typist backing out of a
// table they have changed their mind about: the formatting goes, the text stays.
func TestTableBackspaceAtHeadUntablesTheRow(t *testing.T) {
	m := newTableModel(60)
	m.SetValue("| Item | Qty |\n| ---- | --- |\n| Milk | 2   |")
	m.SetCursorOffset(len("| "))
	m = backspace(m)
	want := "Item Qty\n| ---- | --- |\n| Milk | 2   |"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
	if got := m.CursorOffset(); got != 0 {
		t.Fatalf("cursor offset = %d, want 0 (the head of the line)", got)
	}
}

// Tab off the last cell of a row left empty ends the table, instead of opening yet
// another row to tab through.
func TestTableTabOnEmptyRowEndsTable(t *testing.T) {
	m := enter(typeString(newTableModel(60), "| A | B |"))
	m = typeString(m, "Milk")
	m = tab(m, false)
	m = typeString(m, "2")
	m = tab(m, false) // opens a row
	m = tab(m, false) // through its cells...
	m = tab(m, false) // ...and out
	want := "| A    | B   |\n| ---- | --- |\n| Milk | 2   |\n"
	if got := m.Value(); got != want {
		t.Fatalf("Value =\n%q\nwant\n%q", got, want)
	}
	if m.InTableRow() {
		t.Fatal("still in the table after tabbing out of it")
	}
	typed := typeString(m, "done")
	if got := typed.Value(); got != want+"done" {
		t.Fatalf("after typing: Value = %q, want %q", got, want+"done")
	}
}

// Shift+tab walks back through the cells and off the front of the table, leaving the
// caret outside the grid — so the next shift+tab is the owner's again and cycles
// focus out of the composer.
func TestTableShiftTabLeavesTheGrid(t *testing.T) {
	m := newTableModel(60)
	m.SetValue(alignedTable)
	m.SetCursorOffset(len("| A    | B   |\n| ---- | --- |\n| Milk | 2"))
	if !m.InTableRow() {
		t.Fatal("InTableRow = false inside the table")
	}
	m = tab(m, true) // back into "Milk"
	if got, want := m.CursorOffset(), len("| A    | B   |\n| ---- | --- |\n| Milk"); got != want {
		t.Fatalf("cursor offset = %d, want %d", got, want)
	}
	m = tab(m, true) // up into the header's last cell, stepping over the separator
	if got, want := m.CursorOffset(), len("| A    | B"); got != want {
		t.Fatalf("cursor offset = %d, want %d (the separator row is not tabbed into)", got, want)
	}
	m = tab(m, true) // back into the header's first cell
	m = tab(m, true) // and off the front of it
	if m.InTableRow() {
		t.Fatalf("still in the grid at offset %d after shift+tabbing off the front", m.CursorOffset())
	}
	if got := m.CursorOffset(); got != 0 {
		t.Fatalf("cursor offset = %d, want 0 (the head of the first row)", got)
	}
}
