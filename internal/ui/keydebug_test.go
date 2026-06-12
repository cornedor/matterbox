package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestModNames(t *testing.T) {
	cases := []struct {
		mod  tea.KeyMod
		want string
	}{
		{0, "-"},
		{tea.ModAlt, "alt"},
		{tea.ModCtrl | tea.ModShift, "ctrl+shift"},
		{tea.ModSuper, "super"},
	}
	for _, c := range cases {
		if got := modNames(c.mod); got != c.want {
			t.Errorf("modNames(%d) = %q, want %q", c.mod, got, c.want)
		}
	}
}

// TestFormatKeyDebugAlt is the diagnostic the inspector exists for: an
// option+arrow that reaches the app as alt+up must report the alt modifier,
// whereas a bare arrow must not — that difference is exactly what tells the
// user whether their terminal is forwarding Option as Alt.
func TestFormatKeyDebugAlt(t *testing.T) {
	withAlt := formatKeyDebug(tea.KeyPressMsg{Mod: tea.ModAlt, Code: tea.KeyUp})
	if !strings.Contains(withAlt, "Keystroke=alt+up") || !strings.Contains(withAlt, "Mod=alt") {
		t.Errorf("alt+up not decoded as alt: %q", withAlt)
	}
	bare := formatKeyDebug(tea.KeyPressMsg{Code: tea.KeyUp})
	if strings.Contains(bare, "alt") {
		t.Errorf("bare up should carry no alt modifier: %q", bare)
	}
}

func TestKeyDebugCaptureAndClose(t *testing.T) {
	m := Model{keyDebugMode: true}
	// A normal key is captured, not acted on.
	next, _ := m.handleKeyDebugKey(tea.KeyPressMsg{Mod: tea.ModAlt, Code: tea.KeyLeft})
	m = next.(Model)
	if !m.keyDebugMode {
		t.Fatal("a captured key should not close the inspector")
	}
	if len(m.keyDebugLog) != 1 {
		t.Fatalf("expected 1 logged key, got %d", len(m.keyDebugLog))
	}
	// esc closes and clears.
	next, _ = m.handleKeyDebugKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.keyDebugMode || m.keyDebugLog != nil {
		t.Errorf("esc should close and clear: mode=%v log=%v", m.keyDebugMode, m.keyDebugLog)
	}
}
