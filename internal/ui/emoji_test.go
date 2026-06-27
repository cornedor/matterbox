package ui

import (
	"strings"
	"testing"

	"matterbox/internal/editor"
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

func TestIsEmojiQuery(t *testing.T) {
	// Shortcode-shaped queries open the picker.
	for _, q := range []string{"smile", "smi", "+1", "-1", "e-mail", "non_potable", "8ball", "a"} {
		if !isEmojiQuery(q) {
			t.Errorf("isEmojiQuery(%q) = false, want true", q)
		}
	}
	// Text emoticons carry punctuation no shortcode starts with — these must
	// not open the picker, so ":)" + Enter still sends the message rather than
	// accepting a ")"-containing shortcode like ":flag_Myanmar_(Burma):".
	for _, q := range []string{")", "(", "/", "\\", "|", "*", "'(", "p)", ")smile"} {
		if isEmojiQuery(q) {
			t.Errorf("isEmojiQuery(%q) = true, want false", q)
		}
	}
}

func TestUpdateEmojiTrigger(t *testing.T) {
	// The picker opens only for ":" followed by >=2 shortcode characters at a
	// word boundary. The cursor sits at the end of the value after SetValue.
	cases := []struct {
		text string
		want bool
	}{
		// Text emoticons must not open it — this is the reported bug: ":)"
		// would otherwise match ":flag_Myanmar_(Burma):" and Enter would
		// accept it instead of sending.
		{"Welcome :)", false},
		{"hey :D", false}, // single char after colon
		{":o", false},
		{":P", false},
		{"foo :-)", false}, // 2 chars, but ")" isn't a shortcode char
		{":", false},       // bare colon
		{":a", false},      // only one char
		// Real shortcodes in progress open it.
		{"nice :sm", true},
		{":+1", true},
		{"see :tada", true},
		// A ':' mid-word (e.g. a URL) is not a word-boundary trigger.
		{"http://ab", false},
	}
	for _, tc := range cases {
		var m Model
		m.input = editor.New()
		m.input.SetWidth(40)
		m.input.SetValue(tc.text)
		m.updateEmoji()
		if m.emoji.active != tc.want {
			t.Errorf("updateEmoji(%q): active = %v, want %v", tc.text, m.emoji.active, tc.want)
		}
	}
}

func TestUnicodeEmojiGlyph(t *testing.T) {
	tests := []struct {
		name, want string
	}{
		// Plain codemap lookups still resolve.
		{"+1", "👍"},
		{"smile", "😄"},
		// Mattermost skin-tone naming kyokomi doesn't carry, composed from the
		// base glyph + Fitzpatrick modifier (matches kyokomi's own _toneN form).
		{"+1_light_skin_tone", "👍🏻"},
		{"+1_medium_light_skin_tone", "👍🏼"},
		{"+1_medium_skin_tone", "👍🏽"},
		{"+1_medium_dark_skin_tone", "👍🏾"},
		{"+1_dark_skin_tone", "👍🏿"},
		{"wave_medium_skin_tone", "👋🏽"},
		// Base glyph carrying a VS16 drops it before the modifier so the
		// sequence is canonical (one grapheme, not glyph + swatch).
		{"point_up_medium_skin_tone", "☝🏽"},
		// Unknown base or non-emoji shortcode stays unresolved.
		{"party_parrot", ""},
		{"definitely_not_an_emoji_dark_skin_tone", ""},
	}
	for _, tt := range tests {
		if got := unicodeEmojiGlyph(tt.name); got != tt.want {
			t.Errorf("unicodeEmojiGlyph(%q) = %q (% x), want %q (% x)",
				tt.name, got, []byte(got), tt.want, []byte(tt.want))
		}
	}
}
