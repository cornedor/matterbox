package ui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// TestVisualWidthMatchesLipgloss guards the ASCII fast path in visualWidth: it
// must return exactly lipgloss.Width for every input, since visualRowsBefore
// (and thus the scroll geometry / wrap counts) depends on the two agreeing. The
// printable-ASCII shortcut and every fallback case (ANSI escapes, tabs, control
// bytes, multi-byte and wide runes) are checked.
func TestVisualWidthMatchesLipgloss(t *testing.T) {
	cases := []string{
		"",
		"hello",
		"#general",
		"plain ascii line 123 !@#$%^&*()",
		"a\tb",               // tab — a control byte, must fall back
		"\x7f",               // DEL — control, fall back
		"\x1b[1mbold\x1b[0m", // ANSI-styled ASCII, fall back
		"café",               // multi-byte rune
		"🚀 rocket 🔥",         // wide emoji
		"事故 incident",        // CJK wide runes
		"  two-space gutter",
	}
	for _, s := range cases {
		if got, want := visualWidth(s), lipgloss.Width(s); got != want {
			t.Errorf("visualWidth(%q) = %d; want %d (lipgloss.Width)", s, got, want)
		}
	}
}
