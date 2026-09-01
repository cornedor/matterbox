package editor

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func shiftKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: tea.ModShift}
}

// A first shift+arrow anchors at the caret; further ones grow the selection,
// and the opposite direction shrinks it back to nothing.
func TestShiftArrowSelects(t *testing.T) {
	m := New()
	m.SetValue("hello world")
	m.SetCursorOffset(0)
	for i := 0; i < 5; i++ {
		m.handleKey(shiftKey(tea.KeyRight))
	}
	if got := m.SelectedText(); got != "hello" {
		t.Fatalf("shift+→ ×5 selected %q, want %q", got, "hello")
	}
	m.handleKey(shiftKey(tea.KeyLeft))
	if got := m.SelectedText(); got != "hell" {
		t.Fatalf("shift+← selected %q, want %q", got, "hell")
	}
	for i := 0; i < 4; i++ {
		m.handleKey(shiftKey(tea.KeyLeft))
	}
	if m.HasSelection() {
		t.Fatalf("shrinking back to the anchor left a selection %q", m.SelectedText())
	}
}

// shift+↓ selects across a line break, newline included.
func TestShiftDownSelectsAcrossLines(t *testing.T) {
	m := New()
	m.SetWidth(20)
	m.SetValue("ab\ncd")
	m.SetCursorOffset(1)
	m.handleKey(shiftKey(tea.KeyDown))
	if got := m.SelectedText(); got != "b\nc" {
		t.Fatalf("shift+↓ selected %q, want %q", got, "b\nc")
	}
	m.handleKey(shiftKey(tea.KeyUp))
	if m.HasSelection() {
		t.Fatalf("shift+↑ back to the anchor left a selection %q", m.SelectedText())
	}
}

// A plain arrow after a shift-selection collapses it (the existing behaviour),
// and typing replaces it.
func TestShiftSelectionThenTypeReplaces(t *testing.T) {
	m := New()
	m.SetValue("hello world")
	m.SetCursorOffset(6)
	for i := 0; i < 5; i++ {
		m.handleKey(shiftKey(tea.KeyRight))
	}
	m.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if got := m.Value(); got != "hello x" {
		t.Fatalf("value after typing over a shift-selection = %q, want %q", got, "hello x")
	}
}
