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
	m.reactionSearch = ptrTo(textinput.New())
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

// TestReactionPickerListsPlacedReactions: the modal lists who placed each
// existing reaction — the current user as "you", others by cached username,
// grouped by emoji — above the pickable list.
func TestReactionPickerListsPlacedReactions(t *testing.T) {
	m := reactionPickerModel()
	m.width, m.height = 80, 40
	m.userNames["u2"] = "bob"
	p := m.posts[0]
	p.Metadata = &model.PostMetadata{Reactions: []*model.Reaction{
		{UserId: "u1", PostId: "p1", EmojiName: "tada"},
		{UserId: "u2", PostId: "p1", EmojiName: "tada"},
		{UserId: "u2", PostId: "p1", EmojiName: "heart"},
	}}
	out := m.renderReactionPicker()
	if !strings.Contains(out, "placed reactions") {
		t.Fatalf("picker missing the placed-reactions section:\n%s", out)
	}
	for _, want := range []string{"you", "bob"} {
		if !strings.Contains(out, want) {
			t.Fatalf("placed reactions missing reactor %q:\n%s", want, out)
		}
	}
}

// TestReactionPickerNoReactionsHidesSection: a post without reactions shows no
// placed-reactions section at all.
func TestReactionPickerNoReactionsHidesSection(t *testing.T) {
	m := reactionPickerModel()
	m.width, m.height = 80, 40
	if got := m.renderReactionReactors(m.posts[0], 52); got != "" {
		t.Fatalf("renderReactionReactors on a reaction-less post = %q, want empty", got)
	}
}

// indexOfName returns the position of name in the picker's current list, or -1.
func indexOfName(m Model, name string) int {
	for i, n := range m.reactionPickerNames() {
		if n == name {
			return i
		}
	}
	return -1
}

// TestReactionPickerSurfacesExistingReactions: with an empty query the picker
// lists the post's existing reactions first — including one (rocket) that isn't
// in the configured quick-list — so any placed reaction is reachable to toggle.
func TestReactionPickerSurfacesExistingReactions(t *testing.T) {
	m := reactionPickerModel()
	p := m.posts[0]
	p.Metadata = &model.PostMetadata{Reactions: []*model.Reaction{
		{UserId: "u1", PostId: "p1", EmojiName: "tada"},   // mine
		{UserId: "u2", PostId: "p1", EmojiName: "rocket"}, // someone else's, not in quick-list
	}}
	names := m.reactionPickerNames()
	if len(names) < 2 || names[0] != "tada" || names[1] != "rocket" {
		t.Fatalf("existing reactions not surfaced first: got %v", names)
	}
	// The quick-list still follows, with tada (an existing reaction that is also
	// configured) not duplicated.
	if strings.Count(strings.Join(names, ","), "tada") != 1 {
		t.Fatalf("tada duplicated between existing reactions and quick-list: %v", names)
	}
	for _, want := range m.reactionEmojis {
		if indexOfName(m, want) < 0 {
			t.Fatalf("configured emoji %q dropped from picker: %v", want, names)
		}
	}
}

// TestReactionPickerJoinsOthersReaction: picking a reaction only someone else
// placed adds the current user to it (the "+1" / send-the-same-reaction case).
func TestReactionPickerJoinsOthersReaction(t *testing.T) {
	m := reactionPickerModel()
	m.width, m.height = 80, 40
	p := m.posts[0]
	p.Metadata = &model.PostMetadata{Reactions: []*model.Reaction{
		{UserId: "u2", PostId: "p1", EmojiName: "rocket"},
	}}
	m.reactionPickerIdx = indexOfName(m, "rocket")
	cmd := m.applyReactionPick()
	if cmd == nil {
		t.Fatalf("joining a reaction returned no command")
	}
	if !m.userHasReacted(p, "rocket") {
		t.Fatalf("picking someone else's reaction did not add the current user to it")
	}
}

// TestReactionPickerRemovesOwnReaction: re-picking a reaction the user already
// placed removes it locally (the toggle-off case), even for an emoji that isn't
// in the configured quick-list.
func TestReactionPickerRemovesOwnReaction(t *testing.T) {
	m := reactionPickerModel()
	m.width, m.height = 80, 40
	p := m.posts[0]
	p.Metadata = &model.PostMetadata{Reactions: []*model.Reaction{
		{UserId: "u1", PostId: "p1", EmojiName: "rocket"},
	}}
	m.reactionPickerIdx = indexOfName(m, "rocket")
	cmd := m.applyReactionPick()
	if cmd == nil {
		t.Fatalf("removing a reaction returned no command")
	}
	if m.userHasReacted(p, "rocket") {
		t.Fatalf("re-picking the user's own reaction did not remove it")
	}
}

// TestReactionPickerShowsCount: an existing reaction's row carries its count so
// the user can see how many people placed it before joining/removing.
func TestReactionPickerShowsCount(t *testing.T) {
	m := reactionPickerModel()
	m.width, m.height = 80, 40
	p := m.posts[0]
	p.Metadata = &model.PostMetadata{Reactions: []*model.Reaction{
		{UserId: "u1", PostId: "p1", EmojiName: "tada"},
		{UserId: "u2", PostId: "p1", EmojiName: "tada"},
	}}
	out := m.renderReactionPicker()
	if !strings.Contains(out, "2  :tada:") {
		t.Fatalf("picker did not show the tada count:\n%s", out)
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
