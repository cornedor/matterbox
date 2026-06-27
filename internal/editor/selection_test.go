package editor

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestBeginExtendSelectionVisual(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello world")
	m.BeginSelection(0, 0)
	if m.HasSelection() {
		t.Fatal("bare BeginSelection should not activate a selection")
	}
	m.ExtendSelectionToVisual(0, 5)
	s, e, ok := m.SelectionRange()
	if !ok || s != 0 || e != 5 {
		t.Fatalf("SelectionRange = (%d,%d,%v), want (0,5,true)", s, e, ok)
	}
	if got := m.SelectedText(); got != "hello" {
		t.Fatalf("SelectedText = %q, want %q", got, "hello")
	}
}

func TestSetSelectionAndDelete(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello world")
	m.SetSelection(0, 5)
	if got := m.SelectedText(); got != "hello" {
		t.Fatalf("SelectedText = %q, want %q", got, "hello")
	}
	if !m.DeleteSelection() {
		t.Fatal("DeleteSelection returned false")
	}
	if got := m.Value(); got != " world" {
		t.Fatalf("Value = %q, want %q", got, " world")
	}
	if m.HasSelection() {
		t.Fatal("selection should be cleared after delete")
	}
	if off := m.CursorOffset(); off != 0 {
		t.Fatalf("cursor offset = %d, want 0", off)
	}
}

func TestTypingReplacesSelection(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello world")
	m.SetSelection(6, 11) // "world"
	m = typeString(m, "there")
	if got := m.Value(); got != "hello there" {
		t.Fatalf("Value = %q, want %q", got, "hello there")
	}
	if m.HasSelection() {
		t.Fatal("selection should be gone after typing")
	}
}

func TestBackspaceDeletesSelection(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello world")
	m.SetSelection(0, 6) // "hello "
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := m.Value(); got != "world" {
		t.Fatalf("Value = %q, want %q", got, "world")
	}
}

func TestDeleteForwardDeletesSelection(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello world")
	m.SetSelection(0, 6) // "hello "
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	if got := m.Value(); got != "world" {
		t.Fatalf("Value = %q, want %q", got, "world")
	}
}

func TestMovementCollapsesSelection(t *testing.T) {
	left := tea.KeyPressMsg{Code: tea.KeyLeft}
	right := tea.KeyPressMsg{Code: tea.KeyRight}

	m := newTestModel(40)
	m.SetValue("hello")
	m.SetSelection(1, 4)
	m, _ = m.Update(left)
	if m.HasSelection() {
		t.Fatal("left should clear the selection")
	}
	if off := m.CursorOffset(); off != 1 {
		t.Fatalf("left collapse cursor = %d, want 1 (selection start)", off)
	}

	m.SetSelection(1, 4)
	m, _ = m.Update(right)
	if m.HasSelection() {
		t.Fatal("right should clear the selection")
	}
	if off := m.CursorOffset(); off != 4 {
		t.Fatalf("right collapse cursor = %d, want 4 (selection end)", off)
	}
}

func TestDeleteSelectionSpansLines(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("ab\ncd\nef")
	m.SetSelection(1, 7) // "b\ncd\ne"
	m.DeleteSelection()
	if got := m.Value(); got != "af" {
		t.Fatalf("Value = %q, want %q", got, "af")
	}
	if r, c := m.CursorRowCol(); r != 0 || c != 1 {
		t.Fatalf("cursor = (%d,%d), want (0,1)", r, c)
	}
}

func TestSelectWordAtVisual(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello world")
	m.SelectWordAtVisual(0, 7) // on the "o" of "world"
	if got := m.SelectedText(); got != "world" {
		t.Fatalf("word select = %q, want %q", got, "world")
	}
	// A double-click on whitespace selects the whitespace run, not a word.
	m.SelectWordAtVisual(0, 5) // the space between the words
	if got := m.SelectedText(); got != " " {
		t.Fatalf("whitespace select = %q, want %q", got, " ")
	}
}

func TestSelectWordPunctuationRun(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("foo, bar")
	m.SelectWordAtVisual(0, 0) // on "foo"
	if got := m.SelectedText(); got != "foo" {
		t.Fatalf("word select = %q, want %q (punctuation must not join the word)", got, "foo")
	}
}

func TestSelectLineAtVisual(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("ab\ncd\nef")
	m.SelectLineAtVisual(1, 0) // the "cd" line
	if got := m.SelectedText(); got != "cd" {
		t.Fatalf("line select = %q, want %q", got, "cd")
	}
}

func TestWordDragExtendsByWord(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("the quick brown")
	m.SelectWordAtVisual(0, 5) // double-click "quick"
	if got := m.SelectedText(); got != "quick" {
		t.Fatalf("initial word = %q, want %q", got, "quick")
	}
	// Drag forward onto "brown": the whole word joins, not just the runes
	// under the pointer.
	m.ExtendSelectionToVisual(0, 11)
	if got := m.SelectedText(); got != "quick brown" {
		t.Fatalf("forward word-drag = %q, want %q", got, "quick brown")
	}
	// Drag back past the anchor word onto "the": the anchor word stays covered.
	m.ExtendSelectionToVisual(0, 1)
	if got := m.SelectedText(); got != "the quick" {
		t.Fatalf("backward word-drag = %q, want %q", got, "the quick")
	}
}

func TestShiftClickExtendsFromCaret(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello world")
	m.SetCursorOffset(0)
	m.ExtendSelectionFromCaret(0, 5) // shift-click after "hello"
	if got := m.SelectedText(); got != "hello" {
		t.Fatalf("shift-extend = %q, want %q", got, "hello")
	}
}

func TestShiftClickKeepsWordGranularity(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("the quick brown")
	m.SelectWordAtVisual(0, 1)        // double-click "the"
	m.ExtendSelectionFromCaret(0, 12) // shift-click on "brown"
	if got := m.SelectedText(); got != "the quick brown" {
		t.Fatalf("shift word-extend = %q, want %q", got, "the quick brown")
	}
}

func TestSelectionRendersHighlight(t *testing.T) {
	m := newTestModel(40)
	m.SetValue("hello world")
	m.SetCursorOffset(6) // caret fixed so only the selection differs
	plain := m.View()
	m.SetSelection(2, 6) // anchor 2, caret stays at 6
	sel := m.View()
	if plain == sel {
		t.Fatal("selection did not change the rendered output")
	}
	if ansi.Strip(plain) != ansi.Strip(sel) {
		t.Fatalf("selection changed the text, not just styling:\n plain=%q\n sel=%q",
			ansi.Strip(plain), ansi.Strip(sel))
	}
}
