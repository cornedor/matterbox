package emoji

import "testing"

func TestGlyph(t *testing.T) {
	tests := []struct{ name, want string }{
		{"+1", "👍"},
		{"thumbsup", "👍"}, // alias
		{"smile", "😄"},
		{"SMILE", "😄"}, // the web client lowercases before it looks up
		// The two classes a gemoji-derived table gets wrong.
		{"man_farmer_light_skin_tone", "👨🏻‍🌾"},
		{"runner", "🏃"},
		// Not Mattermost's spelling — must stay unresolved so the caller falls
		// through to the custom-emoji path instead of drawing a glyph nobody
		// else sees.
		{"+1_tone1", ""},
		{"men-with-bunny-ears-partying", ""},
		{"party_parrot", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Glyph(tt.name); got != tt.want {
			t.Errorf("Glyph(%q) = %q (% x), want %q (% x)", tt.name, got, []byte(got), tt.want, []byte(tt.want))
		}
	}
}

func TestName(t *testing.T) {
	tests := []struct {
		glyph, want string
		ok          bool
	}{
		{"👍", "+1", true},
		{"👎", "-1", true},
		{"🎉", "tada", true},
		{"😂", "joy", true},
		// Telegram sends the variation selector on some reactions and not on
		// others; both spellings must land on the same name.
		{"❤️", "heart", true},
		{"❤", "heart", true},
		{"👨🏻‍🌾", "male-farmer_light_skin_tone", true},
		{"x", "", false},
	}
	for _, tt := range tests {
		got, ok := Name(tt.glyph)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Name(%q) = %q, %v; want %q, %v", tt.glyph, got, ok, tt.want, tt.ok)
		}
	}
}

// TestNamesComplete guards the two halves of the table against drifting apart:
// every shortcode the picker offers must resolve to a glyph, and the reverse
// map must cover every emoji.
func TestNamesComplete(t *testing.T) {
	if len(Names()) < 4000 {
		t.Fatalf("Names() = %d shortcodes, expected the full Mattermost set", len(Names()))
	}
	for _, n := range Names() {
		if Glyph(n) == "" {
			t.Errorf("Names() offers %q, which Glyph() cannot resolve", n)
		}
	}
	for _, e := range entries {
		if _, ok := Name(e.glyph); !ok {
			t.Errorf("no canonical name for %q (%s)", e.glyph, e.name)
		}
	}
}

// TestSorted keeps the picker's assumption honest: emojiMatches relies on
// Names() being sorted for deterministic ranking within a match tier.
func TestSorted(t *testing.T) {
	n := Names()
	for i := 1; i < len(n); i++ {
		if n[i-1] >= n[i] {
			t.Fatalf("Names() not sorted at %d: %q >= %q", i, n[i-1], n[i])
		}
	}
}
