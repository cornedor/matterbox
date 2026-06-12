package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"matterbox/internal/config"
)

// sheetModel builds a renderable Model with the cheatsheet viewport wired up.
func sheetModel(km keyMap) Model {
	vp := viewport.New()
	vp.SoftWrap = true
	m := Model{
		keys:          km,
		focus:         focusMessages,
		width:         100,
		height:        44,
		keysSheetView: vp,
	}
	return m
}

// TestKeysCommandOpensSheet: the "> Keys" command runs runShowKeys, which
// raises the cheatsheet popup (and inModal/the context table track it).
func TestKeysCommandOpensSheet(t *testing.T) {
	var keysCmd *switcherCommand
	for _, c := range builtinCommands() {
		if c.name == "Keys" {
			cc := c
			keysCmd = &cc
			break
		}
	}
	if keysCmd == nil {
		t.Fatal("no \"Keys\" command registered in builtinCommands()")
	}
	m := sheetModel(newKeyMap("ctrl"))
	keysCmd.run(&m, "")
	if !m.keysSheetMode {
		t.Fatal("running the Keys command should set keysSheetMode")
	}
	if !m.inModal() {
		t.Error("an open cheatsheet should count as a modal (gates the global chords)")
	}
}

// TestKeysSheetEscCloses: esc and q close the popup; routing goes through the
// real handleKey modal ladder.
func TestKeysSheetEscCloses(t *testing.T) {
	for _, tok := range []string{"esc", "q"} {
		m := sheetModel(newKeyMap("ctrl"))
		m.openKeysSheet()
		out, _ := m.handleKey(keyStr(tok))
		if got := out.(Model); got.keysSheetMode {
			t.Errorf("%q should close the cheatsheet", tok)
		}
	}
}

// TestKeysSheetGroupsReflectEffectiveBindings: the cheatsheet is built from the
// live keymap, so a rebind shows the user's key, not the stale default.
func TestKeysSheetGroupsReflectEffectiveBindings(t *testing.T) {
	km, err := keyMapForConfig(cfgWith(map[string]config.StringOrList{"compose": {"a"}}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	m := sheetModel(km)
	groups := m.keysSheetGroups()
	if len(groups) == 0 {
		t.Fatal("expected non-empty cheatsheet groups")
	}
	// The compose action lives in the Global section; the leader section has a
	// distinct ",i → compose" jump hint, so scope the lookup to Global.
	var composeRow *keysSheetRow
	for _, g := range groups {
		if g.title != "Global" {
			continue
		}
		for i := range g.rows {
			if g.rows[i].desc == "compose" {
				composeRow = &g.rows[i]
			}
		}
	}
	if composeRow == nil {
		t.Fatal("compose action missing from the cheatsheet Global section")
	}
	if composeRow.keys != "a" {
		t.Errorf("compose row keys = %q, want %q (the override, not the default i)", composeRow.keys, "a")
	}
}

// TestKeysSheetUnboundDropped: an action unbound via override doesn't appear in
// the cheatsheet (its context contributes no row for it).
func TestKeysSheetUnboundDropped(t *testing.T) {
	km, err := keyMapForConfig(cfgWith(map[string]config.StringOrList{"filter": {}}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	m := sheetModel(km)
	for _, g := range m.keysSheetGroups() {
		for _, r := range g.rows {
			if r.desc == "filter" {
				t.Fatalf("unbound 'filter' should be dropped from the cheatsheet, found %+v", r)
			}
		}
	}
}

// TestKeybindingsListMarksOverrides: the CLI-facing list flags overridden
// actions and reports both the effective keys and the replaced default.
func TestKeybindingsListMarksOverrides(t *testing.T) {
	binds, err := KeybindingsList(cfgWith(map[string]config.StringOrList{"delete_post": {"shift+d"}}))
	if err != nil {
		t.Fatalf("KeybindingsList: %v", err)
	}
	var found bool
	for _, b := range binds {
		switch b.ID {
		case "delete_post":
			found = true
			if !b.Overridden {
				t.Error("delete_post should be marked overridden")
			}
			if strings.Join(b.Keys, ",") != "shift+d" {
				t.Errorf("delete_post keys = %v, want [shift+d]", b.Keys)
			}
			if strings.Join(b.Default, ",") != "D" {
				t.Errorf("delete_post default = %v, want [D]", b.Default)
			}
		case "compose":
			if b.Overridden {
				t.Error("compose should not be marked overridden")
			}
		}
	}
	if !found {
		t.Error("delete_post missing from KeybindingsList")
	}
}

// TestKeysSheetRenders: the popup renders with its title and at least one
// section heading, and scroll keys route to the viewport without panicking.
func TestKeysSheetRenders(t *testing.T) {
	m := sheetModel(newKeyMap("ctrl"))
	m.openKeysSheet()
	out := m.renderKeysSheetPopup()
	if !strings.Contains(out, "Keyboard shortcuts") {
		t.Error("popup should carry the title")
	}
	if !strings.Contains(out, "Messages") {
		t.Error("popup should include the Messages section heading")
	}
	// A scroll key is consumed by the viewport, not the close path.
	res, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if got := res.(Model); !got.keysSheetMode {
		t.Error("a scroll key should not close the cheatsheet")
	}
}
