package ui

import (
	"strings"
	"testing"
)

func TestEmojiMatches(t *testing.T) {
	// Exact-name prefix should surface that shortcode and rank it ahead of
	// substring-only hits.
	got := emojiMatches("smile")
	if len(got) == 0 {
		t.Fatal("smile: expected matches, got none")
	}
	var foundSmile bool
	for _, it := range got {
		if it.code == ":smile:" {
			foundSmile = true
		}
		if it.glyph == "" {
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
	got = emojiMatches("smi")
	if len(got) > 1 {
		first := strings.Trim(got[0].code, ":")
		if !strings.HasPrefix(first, "smi") {
			t.Errorf("smi: first result %q is not a prefix match", got[0].code)
		}
	}

	// Capped at emojiLimit.
	got = emojiMatches("a")
	if len(got) > emojiLimit {
		t.Errorf("a: returned %d results, want <= %d", len(got), emojiLimit)
	}

	// Nonsense query yields nothing.
	if got := emojiMatches("zzzznotanemoji"); len(got) != 0 {
		t.Errorf("garbage query returned %d results, want 0", len(got))
	}
}
