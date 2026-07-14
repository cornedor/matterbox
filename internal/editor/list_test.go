package editor

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// newListModel returns an editor with list continuation on, typed into as the
// composer is.
func newListModel() Model {
	m := newTestModel(40)
	m.ContinueLists = true
	return m
}

// enter presses the newline key, going through Update the way a keystroke does.
func enter(m Model) Model {
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return m
}

func TestListContinuation(t *testing.T) {
	tests := []struct {
		name  string
		typed string
		want  string
	}{
		{"bullet dash", "- milk", "- milk\n- "},
		{"bullet star", "* milk", "* milk\n* "},
		{"bullet plus", "+ milk", "+ milk\n+ "},
		{"ordered", "1. first", "1. first\n2. "},
		{"ordered paren", "1) first", "1) first\n2) "},
		{"ordered mid-list", "9. ninth", "9. ninth\n10. "},
		{"indent preserved", "  - nested", "  - nested\n  - "},
		{"task item", "- [ ] wash up", "- [ ] wash up\n- [ ] "},
		{"checked task item", "- [x] wash up", "- [x] wash up\n- [ ] "},
		{"not a list: emphasis", "*bold*", "*bold*\n"},
		{"not a list: decimal", "1.5 is a number", "1.5 is a number\n"},
		{"not a list: rule", "--- ", "--- \n"},
		{"not a list: plain text", "hello", "hello\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := enter(typeString(newListModel(), tt.typed))
			if got := m.Value(); got != tt.want {
				t.Fatalf("Value = %q, want %q", got, tt.want)
			}
			if off := m.CursorOffset(); off != len([]rune(tt.want)) {
				t.Fatalf("cursor offset = %d, want %d (caret past the new marker)", off, len([]rune(tt.want)))
			}
		})
	}
}

// A second newline on an item that stayed empty ends the list rather than
// opening another item.
func TestListContinuationEmptyItemEndsList(t *testing.T) {
	tests := []struct {
		name  string
		typed string
		want  string
	}{
		{"bullet", "- milk", "- milk\n"},
		{"ordered", "1. first", "1. first\n"},
		{"task", "- [ ] wash up", "- [ ] wash up\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := enter(enter(typeString(newListModel(), tt.typed)))
			if got := m.Value(); got != tt.want {
				t.Fatalf("Value = %q, want %q", got, tt.want)
			}
			if off := m.CursorOffset(); off != len([]rune(tt.want)) {
				t.Fatalf("cursor offset = %d, want %d", off, len([]rune(tt.want)))
			}
			// Typing then carries on as an ordinary paragraph.
			typed := typeString(m, "done")
			if got := typed.Value(); got != tt.want+"done" {
				t.Fatalf("after typing: Value = %q, want %q", got, tt.want+"done")
			}
		})
	}
}

// A newline in the middle of an item splits it, carrying the marker onto the
// tail; one inside the marker itself splits like plain text.
func TestListContinuationSplitsItem(t *testing.T) {
	m := typeString(newListModel(), "- milk and eggs")
	m.SetCursorOffset(len("- milk"))
	m = enter(m)
	if got, want := m.Value(), "- milk\n-  and eggs"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}

	m = typeString(newListModel(), "- milk")
	m.SetCursorOffset(1) // between the '-' and its space
	m = enter(m)
	if got, want := m.Value(), "-\n milk"; got != want {
		t.Fatalf("caret inside marker: Value = %q, want %q", got, want)
	}
}

// Inside a fenced code block a list-looking line is code, not a list.
func TestListContinuationSkipsCodeBlock(t *testing.T) {
	m := newListModel()
	m.SetValue("```\n- not a list")
	m = enter(m)
	if got, want := m.Value(), "```\n- not a list\n"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}
}

// Off by default: an editor that didn't opt in (the SQL tab) is unchanged.
func TestListContinuationOffByDefault(t *testing.T) {
	m := typeString(newTestModel(40), "- milk")
	m = enter(m)
	if got, want := m.Value(), "- milk\n"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}
}

// Typing over a selection that spans a list item replaces it: the newline is a
// plain break, not a continuation.
func TestListContinuationWithSelectionReplaces(t *testing.T) {
	m := typeString(newListModel(), "- milk")
	m.SetSelection(0, len("- milk"))
	m = enter(m)
	if got, want := m.Value(), "\n"; got != want {
		t.Fatalf("Value = %q, want %q", got, want)
	}
}
