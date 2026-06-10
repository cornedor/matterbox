package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAISearchTimeoutDefault checks that a config with no ai_search timeout
// falls back to the built-in default rather than 0 (which would mean "no
// timeout" once multiplied into a Duration).
func TestAISearchTimeoutDefault(t *testing.T) {
	c := &Config{}
	c.fillDefaults()
	if c.AISearch.TimeoutMinutes != defaultAISearchTimeoutMinutes {
		t.Errorf("default timeout = %d; want %d", c.AISearch.TimeoutMinutes, defaultAISearchTimeoutMinutes)
	}
}

// TestAISearchTimeoutParse pins the yaml key so a renamed tag can't silently
// stop honouring an on-disk timeout_minutes, and confirms an explicit value
// survives fillDefaults.
func TestAISearchTimeoutParse(t *testing.T) {
	const y = "ai_search:\n  timeout_minutes: 12\n"
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.AISearch.TimeoutMinutes != 12 {
		t.Fatalf("parsed timeout = %d; want 12", c.AISearch.TimeoutMinutes)
	}
	c.fillDefaults()
	if c.AISearch.TimeoutMinutes != 12 {
		t.Errorf("fillDefaults clobbered explicit timeout: got %d", c.AISearch.TimeoutMinutes)
	}
}

// TestNavModifierDefault: an absent keybindings section defaults the arrow-nav
// modifier to ctrl, so existing users keep the ctrl+arrow aliases.
func TestNavModifierDefault(t *testing.T) {
	c := &Config{}
	c.fillDefaults()
	if c.Keybindings.NavModifier != "ctrl" {
		t.Errorf("default nav_modifier = %q; want \"ctrl\"", c.Keybindings.NavModifier)
	}
}

// TestNavModifierParse pins the yaml key and confirms an explicit value
// survives fillDefaults (e.g. super for the macOS ⌘ key).
func TestNavModifierParse(t *testing.T) {
	const y = "keybindings:\n  nav_modifier: super\n"
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c.fillDefaults()
	if c.Keybindings.NavModifier != "super" {
		t.Errorf("fillDefaults clobbered explicit nav_modifier: got %q", c.Keybindings.NavModifier)
	}
}

// TestNavModifierLegacyMigration: a pre-NavModifier config that set
// ctrl_arrow_nav: false migrates to nav_modifier: none, and the superseded
// toggle is dropped so a rewrite carries only the new key.
func TestNavModifierLegacyMigration(t *testing.T) {
	const y = "keybindings:\n  ctrl_arrow_nav: false\n"
	var c Config
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c.fillDefaults()
	if c.Keybindings.NavModifier != "none" {
		t.Errorf("legacy ctrl_arrow_nav: false migrated to %q; want \"none\"", c.Keybindings.NavModifier)
	}
	if c.Keybindings.CtrlArrowNav != nil {
		t.Errorf("legacy toggle not cleared after migration: got %v", c.Keybindings.CtrlArrowNav)
	}
}
