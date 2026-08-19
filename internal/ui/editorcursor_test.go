package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// cellAt returns the rune in the rendered screen at visual column col of row,
// after stripping ANSI styling. It walks runes by display width so a wide glyph
// (e.g. the 🔎 search prompt, which is one rune two columns wide) maps to the
// right column.
func cellAt(t *testing.T, screen string, col, row int) rune {
	t.Helper()
	lines := strings.Split(screen, "\n")
	if row < 0 || row >= len(lines) {
		t.Fatalf("row %d out of range (%d lines)", row, len(lines))
	}
	w := 0
	for _, r := range []rune(ansi.Strip(lines[row])) {
		rw := ansi.StringWidth(string(r))
		if col >= w && col < w+rw {
			return r
		}
		w += rw
	}
	t.Fatalf("col %d out of range (row %d is %d cells wide: %q)", col, row, w, ansi.Strip(lines[row]))
	return 0
}

// Each case places a lone 'Z' under the caret and asserts editorCursor reports
// the exact screen cell that 'Z' renders to — i.e. the absolute geometry of the
// native cursor lands on the character it should sit on. The composer path
// isn't covered here (it needs a fully-open channel to render); its geometry
// reuses composerGeom, which production already relies on for mouse hit-testing.

func TestSQLCursorLandsOnCaret(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 120, 40
	m.showSQL = true // register the synthetic SQL tab
	if cmd := m.openSQLTab(); cmd != nil {
		cmd() // run the editor's Focus command
	}
	if !m.onSQLTab() {
		t.Fatal("openSQLTab did not select the SQL tab")
	}
	m.sizeSQLView(m.width, 30) // give the editor a width
	m.sql.input.SetValue("Z")
	m.sql.input.SetCursorOffset(0)
	if m.vcache != nil {
		m.vcache.viewValid = false
	}

	screen := m.viewContent()
	col, row, ok := m.editorCursor()
	if !ok {
		t.Fatal("editorCursor: ok=false, want a SQL cursor")
	}
	if got := cellAt(t, screen, col, row); got != 'Z' {
		t.Fatalf("SQL cursor at (col=%d,row=%d) sits on %q, want 'Z'", col, row, string(got))
	}
}

func TestSearchCursorLandsOnCaret(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 120, 40
	if cmd := m.openSearchTab(); cmd != nil {
		cmd() // run the input's Focus command
	}
	if !m.onSearchTab() {
		t.Fatal("openSearchTab did not select the Search tab")
	}
	m.sizeSearchView(m.width, 30) // give the input a width
	m.search.input.SetValue("Z")
	m.search.input.SetCursor(0)
	if m.vcache != nil {
		m.vcache.viewValid = false
	}

	screen := m.viewContent()
	col, row, ok := m.editorCursor()
	if !ok {
		t.Fatal("editorCursor: ok=false, want a Search cursor")
	}
	if got := cellAt(t, screen, col, row); got != 'Z' {
		t.Fatalf("Search cursor at (col=%d,row=%d) sits on %q, want 'Z'", col, row, string(got))
	}
}

func TestSwitcherCursorLandsOnCaret(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 120, 40
	tm, _ := m.openSwitcher()
	m = tm.(Model)
	m.switcher.SetValue("Z")
	m.switcher.SetCursor(0)
	if m.vcache != nil {
		m.vcache.viewValid = false
	}

	screen := m.viewContent()
	col, row, ok := m.editorCursor()
	if !ok {
		t.Fatal("editorCursor: ok=false, want a switcher cursor")
	}
	if got := cellAt(t, screen, col, row); got != 'Z' {
		t.Fatalf("switcher cursor at (col=%d,row=%d) sits on %q, want 'Z'", col, row, string(got))
	}
}

func TestJiraCommentCursorLandsOnCaret(t *testing.T) {
	// reply=true adds a "replying to" line above the editor, shifting it down.
	for _, reply := range []bool{false, true} {
		m := newTestModel()
		m.width, m.height = 120, 40
		m.jiraCommentActive = true
		m.jiraCommentInput = newCommentTextarea()
		if reply {
			m.jiraCommentReplyTo = "someone"
		}
		m.jiraCommentInput.SetValue("Z")
		m.jiraCommentInput.SetCursorOffset(0)
		if m.vcache != nil {
			m.vcache.viewValid = false
		}

		screen := m.viewContent()
		col, row, ok := m.editorCursor()
		if !ok {
			t.Fatalf("reply=%v: editorCursor ok=false, want a jira-comment cursor", reply)
		}
		if got := cellAt(t, screen, col, row); got != 'Z' {
			t.Fatalf("reply=%v: jira cursor at (col=%d,row=%d) sits on %q, want 'Z'", reply, col, row, string(got))
		}
	}
}
