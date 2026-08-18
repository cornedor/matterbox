package ui

import (
	"sort"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"

	"matterbox/internal/languagetool"
)

// hardwiredDismiss are the keys every modal answers to without declaring them
// as an action (ctrl+c always quits). The footer may advertise them anywhere.
var hardwiredDismiss = map[string]bool{"ctrl+c": true}

// claimedKeys is the union of the key strings every context reachable in this
// state consumes — i.e. exactly the keys that do something right now.
func claimedKeys(m *Model) map[string]bool {
	out := map[string]bool{}
	for _, c := range reachableContexts(m) {
		for k := range keySet(c.claims(m)) {
			out[k] = true
		}
	}
	return out
}

// TestShortHelpAdvertisesLiveKeys is the footer's honesty check: every binding
// the one-line help offers in a given state must be claimed by a layer that is
// reachable in that state. It fails on the classic footer bug — advertising the
// reading panes' ↑/k for a list that hangs off a text input, where k types.
func TestShortHelpAdvertisesLiveKeys(t *testing.T) {
	for _, st := range shadowProbeStates {
		m := &Model{keys: newKeyMap("ctrl")}
		st.apply(m)
		live := claimedKeys(m)
		for _, b := range m.ShortHelp() {
			if len(b.Keys()) == 0 {
				t.Errorf("state %q: footer offers an unbound action (%q)", st.name, b.Help().Desc)
				continue
			}
			for _, k := range b.Keys() {
				if !live[k] && !hardwiredDismiss[k] {
					t.Errorf("state %q: footer offers %q (%s) but no reachable layer claims it",
						st.name, k, b.Help().Desc)
				}
			}
		}
	}
}

// TestFullHelpAdvertisesLiveKeys holds the expanded (`?`) help to the same
// standard, and asserts it never repeats a row.
func TestFullHelpAdvertisesLiveKeys(t *testing.T) {
	for _, st := range shadowProbeStates {
		m := &Model{keys: newKeyMap("ctrl")}
		st.apply(m)
		live := claimedKeys(m)
		seen := map[string]bool{}
		rows := 0
		for _, col := range m.FullHelp() {
			if len(col) > helpColumnRows {
				t.Errorf("state %q: full-help column has %d rows (max %d)", st.name, len(col), helpColumnRows)
			}
			for _, b := range col {
				rows++
				id := b.Help().Key + "\x00" + b.Help().Desc
				if seen[id] {
					t.Errorf("state %q: full help repeats %q %q", st.name, b.Help().Key, b.Help().Desc)
				}
				seen[id] = true
				for _, k := range b.Keys() {
					if !live[k] && !hardwiredDismiss[k] {
						t.Errorf("state %q: full help offers %q (%s) but no reachable layer claims it",
							st.name, k, b.Help().Desc)
					}
				}
			}
		}
		if rows == 0 {
			t.Errorf("state %q: full help is empty", st.name)
		}
	}
}

// TestEveryActionIsClaimed: an action nobody claims is invisible — it can't
// reach the cheatsheet, and the shadow audit can't see it collide. Every
// registry action must be consumed by some layer in some state.
func TestEveryActionIsClaimed(t *testing.T) {
	km := newKeyMap("ctrl")
	claimed := map[string]bool{}
	for _, st := range shadowProbeStates {
		m := &Model{keys: km, ltClient: &languagetool.Client{}} // non-nil: the grammar key counts
		st.apply(m)
		for _, c := range keyContexts {
			if !c.active(m) {
				continue
			}
			for k := range keySet(c.claims(m)) {
				claimed[k] = true
			}
		}
	}
	var missing []string
	for _, d := range actionDefs {
		bound := d.field(&km).Keys()
		if len(bound) == 0 {
			continue
		}
		hit := false
		for _, k := range bound {
			if claimed[k] {
				hit = true
				break
			}
		}
		if !hit {
			missing = append(missing, d.id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("actions no context claims (so they never show up in the cheatsheet): %s",
			strings.Join(missing, ", "))
	}
}

// TestKeysSheetCoversEveryContext: every rung of the ladder is documented in
// the cheatsheet. A new modal that forgets its section fails here rather than
// quietly shipping undocumented keys.
func TestKeysSheetCoversEveryContext(t *testing.T) {
	covered := map[string]bool{}
	for _, sec := range keysSheetSections {
		for _, c := range sec.contexts {
			covered[c] = true
		}
	}
	for _, c := range keyContexts {
		if !covered[c.name] {
			t.Errorf("context %q has no cheatsheet section (add it to keysSheetSections)", c.name)
		}
	}
	// …and no section names a context that doesn't exist (a rename would
	// otherwise silently drop its rows).
	known := map[string]bool{}
	for _, c := range keyContexts {
		known[c.name] = true
	}
	for _, sec := range keysSheetSections {
		for _, c := range sec.contexts {
			if !known[c] {
				t.Errorf("cheatsheet section %q names unknown context %q", sec.title, c)
			}
		}
	}
}

// TestKeysSheetSectionsRender: with the default keymap every section produces
// rows (an empty one means its contexts claim nothing) and no row is blank.
func TestKeysSheetSectionsRender(t *testing.T) {
	m := &Model{keys: newKeyMap("ctrl")}
	groups := m.keysSheetGroups()
	if len(groups) != len(keysSheetSections) {
		t.Errorf("cheatsheet rendered %d of %d sections", len(groups), len(keysSheetSections))
	}
	for _, g := range groups {
		for _, r := range g.rows {
			if strings.TrimSpace(r.keys) == "" || strings.TrimSpace(r.desc) == "" {
				t.Errorf("section %q has an incomplete row %q / %q", g.title, r.keys, r.desc)
			}
		}
	}
}

// TestHardwiredLabelsFoldDigits keeps a picker's accelerator row readable.
func TestHardwiredLabelsFoldDigits(t *testing.T) {
	b := hardwired("pick", digitKeys...)
	if got := b.Help().Key; got != "1…9" {
		t.Errorf("digit accelerator label = %q, want %q", got, "1…9")
	}
	if len(b.Keys()) != 9 {
		t.Errorf("digit accelerator binds %d keys, want 9", len(b.Keys()))
	}
	var _ key.Binding = b
}
