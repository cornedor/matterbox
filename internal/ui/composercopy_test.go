package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// copyComposerModel is a model focused on the composer with `text` in it.
func copyComposerModel(text string) Model {
	m := composerModel(nil, 0)
	m.focus = focusInput
	m.input.SetValue(text)
	m.input.Focus()
	return m
}

// ctrl+c copies a live composer selection and leaves the draft alone.
func TestComposerCopySelection(t *testing.T) {
	m := copyComposerModel("hello world")
	m.input.SetSelection(0, 5)
	out, cmd := m.handleInputKey(ctrlKey('c'))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("ctrl+c on a selection returned no command")
	}
	if m.input.Value() != "hello world" {
		t.Fatalf("copy changed the draft: %q", m.input.Value())
	}
	if got := m.input.SelectedText(); got != "hello" {
		t.Fatalf("copy dropped the selection: %q", got)
	}
}

// ctrl+x copies and removes the selection.
func TestComposerCutSelection(t *testing.T) {
	m := copyComposerModel("hello world")
	m.input.SetSelection(5, 11) // " world"
	out, cmd := m.handleInputKey(ctrlKey('x'))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("ctrl+x on a selection returned no command")
	}
	if got := m.input.Value(); got != "hello" {
		t.Fatalf("value after cut = %q, want %q", got, "hello")
	}
	if m.input.HasSelection() {
		t.Fatal("cut left the selection live")
	}
}

// With nothing selected ctrl+c still quits — the terminal-app reflex wins over
// a copy that would have nothing to copy.
func TestComposerCtrlCQuitsWithoutSelection(t *testing.T) {
	m := copyComposerModel("hello")
	_, cmd := m.handleInputKey(ctrlKey('c'))
	if cmd == nil {
		t.Fatal("ctrl+c with no selection returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c with no selection = %T, want tea.QuitMsg", cmd())
	}
}

// ctrl+x with nothing selected is not a cut — it must not eat the keystroke.
func TestComposerCutNeedsSelection(t *testing.T) {
	m := copyComposerModel("hello")
	out, _ := m.handleInputKey(ctrlKey('x'))
	after := out.(Model)
	if got := after.input.Value(); got != "hello" {
		t.Fatalf("ctrl+x with no selection changed the draft: %q", got)
	}
}

// shift+→ in the composer selects, so ctrl+c has something to take without a
// mouse.
func TestComposerShiftArrowSelects(t *testing.T) {
	m := copyComposerModel("hello world")
	m.input.SetCursorOffset(0)
	for i := 0; i < 5; i++ {
		out, _ := m.handleInputKey(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
		m = out.(Model)
	}
	if got := m.input.SelectedText(); got != "hello" {
		t.Fatalf("shift+→ ×5 in the composer selected %q, want %q", got, "hello")
	}
}
