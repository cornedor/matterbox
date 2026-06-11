package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/config"
)

// cfgWith builds a config carrying just the given keybinding overrides.
func cfgWith(b map[string]config.StringOrList) *config.Config {
	c := &config.Config{}
	c.Keybindings.Bindings = b
	return c
}

// TestOverrideReplacesKeys: an override replaces an action's defaults wholesale
// and the help label reflects the new keys.
func TestOverrideReplacesKeys(t *testing.T) {
	km, err := keyMapForConfig(cfgWith(map[string]config.StringOrList{"compose": {"i", "a"}}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := km.Compose.Keys(); len(got) != 2 || got[0] != "i" || got[1] != "a" {
		t.Fatalf("compose keys = %v, want [i a]", got)
	}
	if got := km.Compose.Help().Key; got != "i/a" {
		t.Errorf("compose help label = %q, want \"i/a\" (generated from override)", got)
	}
}

// TestOverrideUnbind: an empty list unbinds the action (no keys, dropped from
// any matching) — ctrl+c still quits via the hardwired path.
func TestOverrideUnbind(t *testing.T) {
	km, err := keyMapForConfig(cfgWith(map[string]config.StringOrList{"quit": {}}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := km.Quit.Keys(); len(got) != 0 {
		t.Fatalf("quit keys after unbind = %v, want none", got)
	}
}

// TestOverrideUnknownAction: a typo'd action id is a loud error naming the
// valid actions.
func TestOverrideUnknownAction(t *testing.T) {
	_, err := keyMapForConfig(cfgWith(map[string]config.StringOrList{"compoze": {"i"}}))
	if err == nil {
		t.Fatal("expected an error for an unknown action id")
	}
	if !strings.Contains(err.Error(), "compoze") || !strings.Contains(err.Error(), "compose") {
		t.Errorf("error %q should name the typo and list valid actions", err)
	}
}

// TestOverrideBadChord: an unparseable chord is rejected.
func TestOverrideBadChord(t *testing.T) {
	if _, err := keyMapForConfig(cfgWith(map[string]config.StringOrList{"compose": {"ctrl+"}})); err == nil {
		t.Error("expected an error for a chord with no key after the modifier")
	}
	if _, err := keyMapForConfig(cfgWith(map[string]config.StringOrList{"compose": {"hyperctrl+x"}})); err == nil {
		t.Error("expected an error for an unknown modifier")
	}
}

// TestOverrideConflict: an override that collides with another action in a
// co-active layer fails CheckKeybindings (delete_post→i shadows compose).
func TestOverrideConflict(t *testing.T) {
	err := CheckKeybindings(cfgWith(map[string]config.StringOrList{"delete_post": {"i"}}))
	if err == nil {
		t.Fatal("expected a conflict error for delete_post bound to i (collides with compose)")
	}
	if !strings.Contains(err.Error(), "i") {
		t.Errorf("conflict error %q should mention the colliding key", err)
	}
}

// TestCheckKeybindingsHappy: a valid override (and a nil config) pass cleanly.
func TestCheckKeybindingsHappy(t *testing.T) {
	if err := CheckKeybindings(nil); err != nil {
		t.Errorf("nil config should validate: %v", err)
	}
	if err := CheckKeybindings(cfgWith(map[string]config.StringOrList{"delete_post": {"shift+d"}})); err != nil {
		t.Errorf("a clean override should validate: %v", err)
	}
}

// TestNavOverrideReplacesArrowAlias: rebinding a nav action drops its
// modifier-arrow alias and routes the new key instead.
func TestNavOverrideReplacesArrowAlias(t *testing.T) {
	km, err := keyMapForConfig(cfgWith(map[string]config.StringOrList{"channel_next": {"alt+j"}}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := km.NavChanNext.Keys(); len(got) != 1 || got[0] != "alt+j" {
		t.Fatalf("channel_next keys = %v, want [alt+j] (arrow alias + vim default replaced)", got)
	}

	// The new key navigates...
	m := navModel()
	m.keys = km
	out, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'j', Mod: tea.ModAlt}))
	if got := out.(Model); got.openChannelID != "c2" {
		t.Fatalf("alt+j should switch channel; opened %q want c2", got.openChannelID)
	}

	// ...and the old ctrl+↓ alias no longer does.
	m2 := navModel()
	m2.keys = km
	out2, _ := m2.handleKey(ctrlKey(tea.KeyDown))
	if got := out2.(Model); got.openChannelID != "c1" {
		t.Fatalf("ctrl+↓ should be inert after the override; opened %q want c1", got.openChannelID)
	}
}
