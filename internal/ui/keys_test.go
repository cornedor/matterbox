package ui

import (
	"reflect"
	"testing"

	"charm.land/bubbles/v2/key"
)

// bindingType is the reflect.Type of a key.Binding struct, used to find the
// keyMap fields the registry must cover.
var bindingType = reflect.TypeOf(key.Binding{})

// TestActionRegistryCoversEveryField pins the registry invariant: ids are
// unique and every keyMap binding field is populated by exactly one
// actionDef (no field left hand-written, none claimed twice).
func TestActionRegistryCoversEveryField(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range actionDefs {
		if d.id == "" {
			t.Errorf("actionDef with empty id (field unknown)")
		}
		if seen[d.id] {
			t.Errorf("duplicate action id %q", d.id)
		}
		seen[d.id] = true
	}

	var km keyMap
	ptrs := map[uintptr]bool{}
	for _, d := range actionDefs {
		p := reflect.ValueOf(d.field(&km)).Pointer()
		if ptrs[p] {
			t.Errorf("action %q maps to a field already claimed by another action", d.id)
		}
		ptrs[p] = true
	}

	nFields := 0
	tkm := reflect.TypeOf(keyMap{})
	for i := 0; i < tkm.NumField(); i++ {
		if tkm.Field(i).Type == bindingType {
			nFields++
		}
	}
	if len(actionDefs) != nFields {
		t.Errorf("actionDefs has %d entries but keyMap has %d binding fields", len(actionDefs), nFields)
	}
	if len(ptrs) != nFields {
		t.Errorf("actionDefs cover %d distinct fields, keyMap has %d", len(ptrs), nFields)
	}
}

// TestNewKeyMapPopulatesEveryBinding ensures the build leaves no field as a
// zero (key-less) binding.
func TestNewKeyMapPopulatesEveryBinding(t *testing.T) {
	km := newKeyMap("ctrl")
	v := reflect.ValueOf(km)
	tkm := v.Type()
	for i := 0; i < tkm.NumField(); i++ {
		if tkm.Field(i).Type != bindingType {
			continue
		}
		b := v.Field(i).Interface().(key.Binding)
		if len(b.Keys()) == 0 {
			t.Errorf("keyMap.%s has no keys after newKeyMap", tkm.Field(i).Name)
		}
	}
}

// TestPrettyKeyLabel checks the generated help labels: glyph mapping, the
// first-two-keys join, and modifier preservation.
func TestPrettyKeyLabel(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"up", "k"}, "↑/k"},
		{[]string{"down", "j"}, "↓/j"},
		{[]string{"enter"}, "↵"},
		{[]string{"ctrl+down", "ctrl+j"}, "ctrl+↓/ctrl+j"},
		{[]string{"left", "right", "h", "l"}, "←/→"},
		{[]string{"alt+enter", "shift+enter"}, "alt+↵/shift+↵"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := prettyKeyLabel(c.in); got != c.want {
			t.Errorf("prettyKeyLabel(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestParseVimNav maps config values (and the empty default) to modes.
func TestParseVimNav(t *testing.T) {
	cases := map[string]vimNavMode{
		"":        vimNavGlobal,
		"global":  vimNavGlobal,
		"reading": vimNavReading,
		"off":     vimNavOff,
		"none":    vimNavOff,
		"bogus":   vimNavGlobal,
	}
	for in, want := range cases {
		if got := parseVimNav(in); got != want {
			t.Errorf("parseVimNav(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestNavModifierAliasing: with arrow-nav enabled the nav actions gain the
// modifier-arrow alias (leading, so it heads the help label); with "none"
// only the vim keys remain.
func TestNavModifierAliasing(t *testing.T) {
	km := newKeyMap("ctrl")
	if got := km.NavChanNext.Keys(); len(got) != 2 || got[0] != "ctrl+down" || got[1] != "ctrl+j" {
		t.Errorf("channel_next keys = %v, want [ctrl+down ctrl+j]", got)
	}

	off := newKeyMap("none")
	if got := off.NavChanNext.Keys(); len(got) != 1 || got[0] != "ctrl+j" {
		t.Errorf("channel_next keys with nav off = %v, want [ctrl+j]", got)
	}
}
