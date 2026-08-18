package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/config"
)

// Fuzzy (subsequence) matching surfaces an emoji even when the query drops
// an interior letter, where the old prefix/substring matcher returned
// nothing.
func TestEmojiMatchesFuzzy(t *testing.T) {
	var m Model
	got := m.emojiMatches("smle") // "smile" with the 'i' missing
	var found bool
	for _, it := range got {
		if it.code == ":smile:" {
			found = true
		}
	}
	if !found {
		t.Errorf("fuzzy query %q did not surface :smile:, got %v", "smle", got)
	}
}

// Popularity floats a previously-accepted shortcode above an equally-strong
// (same-band) match it would otherwise lose to alphabetically.
func TestEmojiMatchesPopularity(t *testing.T) {
	var m Model
	// Gather the prefix matches for "smi"; make the last one popular and
	// confirm it jumps to the top of the (same-band) prefix group.
	var prefixes []string
	for _, it := range m.emojiMatches("smi") {
		if strings.HasPrefix(it.name, "smi") {
			prefixes = append(prefixes, it.name)
		}
	}
	if len(prefixes) < 2 {
		t.Skipf("need >=2 prefix matches for 'smi', got %d", len(prefixes))
	}
	target := prefixes[len(prefixes)-1] // not already on top
	m.emojiUsage = map[string]int{target: 5}
	got := m.emojiMatches("smi")
	if len(got) == 0 || got[0].name != target {
		t.Errorf("popular %q not floated to top, got %v", target, got)
	}
	// All results are still prefix matches — popularity reorders within the
	// band, it doesn't promote a weaker match over a stronger one.
	for _, it := range got {
		if !strings.HasPrefix(it.name, "smi") {
			t.Errorf("popularity leaked across bands: %q is not a prefix match", it.name)
		}
	}
}

// Mention matching is fuzzy and popularity-weighted, mirroring the emoji
// picker.
func TestLocalMentionMatches(t *testing.T) {
	m := Model{userNames: map[string]string{
		"1": "anders",
		"2": "alexander",
		"3": "bob",
	}}
	// Fuzzy: "andrs" (anders with the 'e' dropped) still matches anders.
	got := m.localMentionMatches("andrs")
	if len(got) == 0 || got[0].Username != "anders" {
		t.Errorf("fuzzy mention %q did not match anders, got %v", "andrs", names(got))
	}
	// Popularity: both "anders" and "alexander" are prefix matches for "a";
	// bumping alexander floats it above the alphabetically-earlier anders.
	m.mentionUsage = map[string]int{"alexander": 3}
	got = m.localMentionMatches("a")
	if len(got) < 2 {
		t.Fatalf("expected >=2 matches for 'a', got %v", names(got))
	}
	if got[0].Username != "alexander" {
		t.Errorf("popular mention not floated to top, got %v", names(got))
	}
}

func names(us []*model.User) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.Username
	}
	return out
}

// bumpEmojiStat / bumpMentionStat increment the in-memory counters so the
// very next picker open reflects the selection (persistence is best-effort
// and tested via the round-trip below).
func TestBumpPickerStats(t *testing.T) {
	var m Model
	m.bumpEmojiStat("tada")
	m.bumpEmojiStat("tada")
	if m.emojiUsage["tada"] != 2 {
		t.Errorf("emojiUsage[tada] = %d, want 2", m.emojiUsage["tada"])
	}
	m.bumpMentionStat("bob")
	if m.mentionUsage["bob"] != 1 {
		t.Errorf("mentionUsage[bob] = %d, want 1", m.mentionUsage["bob"])
	}
	// Empty keys are ignored — no panic, no phantom entry.
	m.bumpEmojiStat("")
	m.bumpMentionStat("")
	if _, ok := m.emojiUsage[""]; ok {
		t.Error("empty emoji key recorded")
	}
}

// writePickerStats → loadPickerStats round-trips all three maps through the
// configured stats path.
func TestPickerStatsRoundTrip(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	emoji := map[string]int{"tada": 3, "smile": 1}
	mention := map[string]int{"bob": 2}
	kaomoji := map[string]int{`¯\_(ツ)_/¯`: 4}
	if err := writePickerStats(emoji, mention, kaomoji); err != nil {
		t.Fatalf("writePickerStats: %v", err)
	}
	gotE, gotM, gotK := loadPickerStats()
	if gotE["tada"] != 3 || gotE["smile"] != 1 {
		t.Errorf("emoji round-trip mismatch: %v", gotE)
	}
	if gotM["bob"] != 2 {
		t.Errorf("mention round-trip mismatch: %v", gotM)
	}
	if gotK[`¯\_(ツ)_/¯`] != 4 {
		t.Errorf("kaomoji round-trip mismatch: %v", gotK)
	}
}

// A missing stats file degrades to empty (non-nil) maps rather than erroring.
func TestLoadPickerStatsMissing(t *testing.T) {
	t.Setenv(config.DirEnv, t.TempDir())
	e, mn, k := loadPickerStats()
	if e == nil || mn == nil || k == nil {
		t.Fatal("expected non-nil maps for missing file")
	}
	if len(e) != 0 || len(mn) != 0 || len(k) != 0 {
		t.Errorf("expected empty maps, got %v / %v / %v", e, mn, k)
	}
}
