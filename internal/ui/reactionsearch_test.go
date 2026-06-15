package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	"github.com/mattermost/mattermost/server/public/model"
)

// reactionPickerModel builds a model with the reaction picker open on post p1,
// a small configured quick-list, and a focused search box.
func reactionPickerModel() Model {
	m := navModel()
	m.reactionEmojis = []string{"+1", "-1", "heart", "tada", "eyes"}
	m.reactionSearch = textinput.New()
	m.me = &model.User{Id: "u1"}
	m.posts = []*model.Post{{Id: "p1"}}
	m.openReactionPicker("p1")
	return m
}

// TestReactionPickerEmptyShowsConfigured: with no query the picker shows the
// configured quick-list verbatim.
func TestReactionPickerEmptyShowsConfigured(t *testing.T) {
	m := reactionPickerModel()
	names := m.reactionPickerNames()
	if len(names) != len(m.reactionEmojis) || names[0] != "+1" {
		t.Fatalf("empty picker names = %v, want the configured list %v", names, m.reactionEmojis)
	}
}

// TestReactionPickerTypingFiltersFullSet: typing reaches emoji that aren't in
// the configured list (e.g. :rocket:), proving the search spans the full set.
func TestReactionPickerTypingFiltersFullSet(t *testing.T) {
	m := reactionPickerModel()
	for _, ch := range "rocket" {
		out, _ := m.handleReactionPickerKey(keyStr(string(ch)))
		m = out.(Model)
	}
	if got := m.reactionSearch.Value(); got != "rocket" {
		t.Fatalf("search value = %q, want %q", got, "rocket")
	}
	names := m.reactionPickerNames()
	found := false
	for _, n := range names {
		if n == "rocket" {
			found = true
		}
	}
	if !found {
		t.Fatalf("typing 'rocket' did not surface :rocket: in matches %v", names)
	}
}

// TestReactionPickerQClosesNothingWhileSearchable: q is a typeable character
// now, not a close key — only esc cancels.
func TestReactionPickerQTypesNotCloses(t *testing.T) {
	m := reactionPickerModel()
	out, _ := m.handleReactionPickerKey(keyStr("q"))
	m = out.(Model)
	if m.reactionPickerPostID == "" {
		t.Fatalf("q closed the picker; want it to type into the search box")
	}
	if m.reactionSearch.Value() != "q" {
		t.Fatalf("q not typed into search: value = %q, want \"q\"", m.reactionSearch.Value())
	}
}

// TestReactionPickerEscCloses: esc tears the picker down and clears the box.
func TestReactionPickerEscCloses(t *testing.T) {
	m := reactionPickerModel()
	out, _ := m.handleReactionPickerKey(keyStr("a")) // start a search first
	m = out.(Model)
	out, _ = m.handleReactionPickerKey(keyStr("esc"))
	m = out.(Model)
	if m.reactionPickerPostID != "" {
		t.Fatalf("esc did not close the picker")
	}
	if m.reactionSearch.Value() != "" {
		t.Fatalf("esc left a stale search value %q", m.reactionSearch.Value())
	}
}

// TestReactionPickerDigitTypesWhileSearching: once a query is active the
// digit keys are query characters, not accelerators — the picker stays open.
func TestReactionPickerDigitTypesWhileSearching(t *testing.T) {
	m := reactionPickerModel()
	out, _ := m.handleReactionPickerKey(keyStr("u")) // enter search mode
	m = out.(Model)
	out, _ = m.handleReactionPickerKey(keyStr("1"))
	m = out.(Model)
	if m.reactionPickerPostID == "" {
		t.Fatalf("digit fired a reaction while searching; want it typed")
	}
	if !strings.HasSuffix(m.reactionSearch.Value(), "1") {
		t.Fatalf("digit not appended to query: value = %q", m.reactionSearch.Value())
	}
}

// TestReactionPickerDigitFiresWhenEmpty: with an empty box the digit
// accelerators still fire immediately against the configured list.
func TestReactionPickerDigitFiresWhenEmpty(t *testing.T) {
	m := reactionPickerModel()
	out, cmd := m.handleReactionPickerKey(keyStr("3")) // → "heart"
	m = out.(Model)
	if m.reactionPickerPostID != "" {
		t.Fatalf("digit accelerator did not fire+close on an empty box")
	}
	if cmd == nil {
		t.Fatalf("digit accelerator returned no command")
	}
}

// TestReactionPickerNavClamps: ctrl+n (input_down) advances the cursor and
// stops at the last row rather than running past it.
func TestReactionPickerNavClamps(t *testing.T) {
	m := reactionPickerModel()
	n := len(m.reactionEmojis)
	for i := 0; i < n+3; i++ {
		out, _ := m.handleReactionPickerKey(ctrlKey('n'))
		m = out.(Model)
	}
	if m.reactionPickerIdx != n-1 {
		t.Fatalf("cursor = %d after over-scrolling, want clamped to %d", m.reactionPickerIdx, n-1)
	}
}
