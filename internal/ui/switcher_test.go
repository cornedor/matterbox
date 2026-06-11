package ui

import "testing"

// fuzzyScore's band is the primary switcher ranking key: a stronger
// textual match (lower band) always outranks a weaker one, so typing a
// name jumps to it. Within a band, attention/usage decide — that part is
// exercised via switcherResults elsewhere; here we pin the classification.
func TestFuzzyScoreBands(t *testing.T) {
	cases := []struct {
		haystack, needle string
		wantBand         int
		wantOK           bool
	}{
		{"general", "", 0, true},        // empty needle → everyone in band 0
		{"general", "general", 0, true}, // exact
		{"general", "gen", 1, true},     // prefix
		{"x-general", "gen", 2, true},   // interior substring
		{"agenda", "gnd", 3, true},      // subsequence (g..n..d)
		{"general", "xyz", 0, false},    // no match
	}
	for _, c := range cases {
		band, _, ok := fuzzyScore(c.haystack, c.needle)
		if ok != c.wantOK {
			t.Errorf("fuzzyScore(%q,%q) ok=%v, want %v", c.haystack, c.needle, ok, c.wantOK)
			continue
		}
		if ok && band != c.wantBand {
			t.Errorf("fuzzyScore(%q,%q) band=%d, want %d", c.haystack, c.needle, band, c.wantBand)
		}
	}
}

// Within a single band the finer score still orders by earliest match
// position, so it remains a sensible last-resort tiebreaker.
func TestFuzzyScoreWithinBandPosition(t *testing.T) {
	_, early, _ := fuzzyScore("ab-team", "team")  // substring at index 3
	_, late, _ := fuzzyScore("abcd-team", "team") // substring at index 5
	if !(early < late) {
		t.Errorf("expected earlier substring to score lower: early=%d late=%d", early, late)
	}
}
