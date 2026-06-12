package cli

import (
	"strings"
	"testing"

	"matterbox/internal/config"
)

// cfgWith builds a config carrying just the given keybinding overrides.
func cfgWith(b map[string]config.StringOrList) *config.Config {
	c := &config.Config{}
	c.Keybindings.NavModifier = "ctrl"
	c.Keybindings.VimNav = "global"
	c.Keybindings.Bindings = b
	return c
}

// TestWriteKeysMarksOverride: an overridden action is flagged with `*` and the
// row shows both the new key and the default it replaced; an untouched action
// is not flagged.
func TestWriteKeysMarksOverride(t *testing.T) {
	var b strings.Builder
	if err := writeKeys(&b, cfgWith(map[string]config.StringOrList{"delete_post": {"shift+d"}})); err != nil {
		t.Fatalf("writeKeys: %v", err)
	}
	out := b.String()

	var deleteLine, composeLine string
	for _, ln := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(ln, "delete_post"):
			deleteLine = ln
		case strings.Contains(ln, "compose"):
			composeLine = ln
		}
	}
	if deleteLine == "" {
		t.Fatal("delete_post row missing from output")
	}
	if !strings.HasPrefix(strings.TrimSpace(deleteLine), "*") {
		t.Errorf("overridden delete_post row should start with *: %q", deleteLine)
	}
	if !strings.Contains(deleteLine, "shift+d") {
		t.Errorf("delete_post row should show the override key: %q", deleteLine)
	}
	if !strings.Contains(deleteLine, "default: D") {
		t.Errorf("delete_post row should note the replaced default: %q", deleteLine)
	}
	if strings.HasPrefix(strings.TrimSpace(composeLine), "*") {
		t.Errorf("untouched compose row should not be flagged: %q", composeLine)
	}
}

// TestWriteKeysUnbound: an action unbound by an empty override renders as
// "(unbound)" rather than a blank cell.
func TestWriteKeysUnbound(t *testing.T) {
	var b strings.Builder
	if err := writeKeys(&b, cfgWith(map[string]config.StringOrList{"quit": {}})); err != nil {
		t.Fatalf("writeKeys: %v", err)
	}
	for _, ln := range strings.Split(b.String(), "\n") {
		if strings.Contains(ln, "quit") {
			if !strings.Contains(ln, "(unbound)") {
				t.Errorf("unbound quit row should say (unbound): %q", ln)
			}
			return
		}
	}
	t.Fatal("quit row missing from output")
}

// TestWriteKeysBadOverride: a bad override surfaces as an error (consistent
// with the TUI failing loud), not a silent fallback to defaults.
func TestWriteKeysBadOverride(t *testing.T) {
	var b strings.Builder
	if err := writeKeys(&b, cfgWith(map[string]config.StringOrList{"nope": {"x"}})); err == nil {
		t.Error("expected an error for an unknown action id")
	}
}
