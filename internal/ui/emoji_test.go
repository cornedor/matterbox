package ui

import (
	"strings"
	"testing"
)

func TestEmojiMatches(t *testing.T) {
	var m Model
	// Exact-name prefix should surface that shortcode and rank it ahead of
	// substring-only hits.
	got := m.emojiMatches("smile")
	if len(got) == 0 {
		t.Fatal("smile: expected matches, got none")
	}
	var foundSmile bool
	for _, it := range got {
		if it.code == ":smile:" {
			foundSmile = true
		}
		if it.name != strings.Trim(it.code, ":") {
			t.Errorf("candidate %q name %q out of sync", it.code, it.name)
		}
		// Every candidate resolves to a non-empty glyph (unicode or literal).
		if m.renderEmojiGlyph(it.name) == "" {
			t.Errorf("candidate %q has empty glyph", it.code)
		}
		if !strings.HasPrefix(it.code, ":") || !strings.HasSuffix(it.code, ":") {
			t.Errorf("candidate code %q is not colon-wrapped", it.code)
		}
	}
	if !foundSmile {
		t.Errorf("smile: :smile: not in results %v", got)
	}

	// Prefix matches come before substring-only matches.
	got = m.emojiMatches("smi")
	if len(got) > 1 {
		first := strings.Trim(got[0].code, ":")
		if !strings.HasPrefix(first, "smi") {
			t.Errorf("smi: first result %q is not a prefix match", got[0].code)
		}
	}

	// Capped at emojiLimit.
	got = m.emojiMatches("a")
	if len(got) > emojiLimit {
		t.Errorf("a: returned %d results, want <= %d", len(got), emojiLimit)
	}

	// Nonsense query yields nothing.
	if got := m.emojiMatches("zzzznotanemoji"); len(got) != 0 {
		t.Errorf("garbage query returned %d results, want 0", len(got))
	}

	// Custom (server) emoji are merged in and ranked ahead of unicode hits in
	// the same tier.
	m.customEmojiNames = []string{"party_parrot"}
	got = m.emojiMatches("party")
	if len(got) == 0 || got[0].name != "party_parrot" {
		t.Errorf("party: custom emoji not surfaced first, got %v", got)
	}
}
