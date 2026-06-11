package ui

import "testing"

// reservedEditingKeys are the emacs-style editing keys a text input needs.
// A layer reachable above a typing context must not claim one (unless it
// whitelists it in shadows) or it steals the key from the composer / search
// box. ctrl+left/right are reserved only when nav_modifier ≠ ctrl; the table
// below tests the default (ctrl), where they're the legitimate nav arrows.
var reservedEditingKeys = map[string]bool{
	"ctrl+a": true, "ctrl+e": true, "ctrl+k": true, "ctrl+u": true,
	"ctrl+w": true, "ctrl+h": true, "ctrl+f": true, "ctrl+b": true,
	"ctrl+d": true, "ctrl+t": true,
	"alt+f": true, "alt+b": true, "alt+d": true,
}

// TestNoAccidentalShadows is the lasting payoff of the keyboard overhaul: for
// every model state, no two layers reachable for the same keypress claim the
// same key unless the higher layer whitelists it. All three of the original
// shadowing bugs fail this test if reintroduced as a non-whitelisted shadow.
// (shadowProbeStates / keySet / inList live in contexts.go so the startup
// conflict check shares the exact same audit.)
func TestNoAccidentalShadows(t *testing.T) {
	for _, st := range shadowProbeStates {
		m := Model{keys: newKeyMap("ctrl")} // vim_nav=global (zero value)
		st.apply(&m)
		reach := reachableContexts(&m)

		// Precompute each reachable layer's claimed key set.
		claims := make([]map[string]bool, len(reach))
		for i, c := range reach {
			claims[i] = keySet(c.claims(&m))
		}

		for hi := 0; hi < len(reach); hi++ {
			for lo := hi + 1; lo < len(reach); lo++ {
				for k := range claims[lo] {
					if claims[hi][k] && !inList(reach[hi].shadows, k) {
						t.Errorf("state %q: %q (higher) shadows %q's %q without whitelisting it",
							st.name, reach[hi].name, reach[lo].name, k)
					}
				}
			}
		}
	}
}

// TestNoEditingKeyShadows asserts no layer reachable above a typing context
// claims a reserved editing key (or an unmodified printable) without
// whitelisting it — so the composer / search box keep their editing keys.
// This is the check that flags bug 3 (global ctrl+h/ctrl+k over the composer)
// unless vim_nav=global's shadow whitelist is present.
func TestNoEditingKeyShadows(t *testing.T) {
	for _, st := range shadowProbeStates {
		m := Model{keys: newKeyMap("ctrl")}
		st.apply(&m)
		reach := reachableContexts(&m)

		for ti, c := range reach {
			if !c.typing {
				continue
			}
			// Layers above this typing context (indices < ti) must not claim a
			// reserved editing key or an unmodified printable unless whitelisted.
			for above := 0; above < ti; above++ {
				for k := range keySet(reach[above].claims(&m)) {
					if inList(reach[above].shadows, k) {
						continue
					}
					if reservedEditingKeys[k] {
						t.Errorf("state %q: %q claims reserved editing key %q above typing layer %q",
							st.name, reach[above].name, k, c.name)
					}
					if len(k) == 1 { // unmodified printable
						t.Errorf("state %q: %q claims unmodified printable %q above typing layer %q",
							st.name, reach[above].name, k, c.name)
					}
				}
			}
		}
	}
}

// TestReadingFreesEditingKeys: under vim_nav=reading the ctrl+vim keys are NOT
// claimed above the composer, so ctrl+h / ctrl+k stay as editing keys — the
// shadow audit needs no whitelist for them in that mode.
func TestReadingFreesEditingKeys(t *testing.T) {
	m := Model{keys: newKeyMap("ctrl"), vimNav: vimNavReading, focus: focusInput}
	reach := reachableContexts(&m)
	for _, c := range reach {
		if c.name == "global:nav" {
			for k := range keySet(c.claims(&m)) {
				if k == "ctrl+h" || k == "ctrl+k" {
					t.Errorf("vim_nav=reading: global:nav still claims %q above the composer", k)
				}
			}
		}
	}
}
