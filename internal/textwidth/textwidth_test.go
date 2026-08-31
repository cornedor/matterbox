package textwidth

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// cases that exercise every branch plus the grapheme edge cases the fast path
// must defer on.
var widthCases = []string{
	"",
	"hello world",
	"plain ascii !@#$%^&*()_+-=[]{};':\",.<>/?`~",
	"\x1b[1mbold\x1b[0m plain",
	"\x1b[38;5;4mcolored\x1b[0m and \x1b[38;2;255;0;0mtruecolor\x1b[0m",
	"┌──────┬──────────┬───────┐",
	"  \x1b[2m│\x1b[0m up   \x1b[2m│\x1b[0m  12ms \x1b[2m│\x1b[0m",
	"▎ selected bar line",
	"█▉▊▋▌▍▎▏ blocks",
	"emoji 😀 here",
	"flag 🇳🇱 and family 👨‍👩‍👧‍👦",
	"café (precomposed é) and café (combining)",
	"CJK 日本語 width テスト",
	"box then combiner: ─́ weird",
	"box then VS16: ─️ weird",
	"ascii then VS16: a️",
	"tab\there and \x07 bell",
	"\x1b]8;;https://example.com\x07link\x1b]8;;\x07", // OSC-8 hyperlink
	"mixed │ 名 │ 😀 │ done",
	"\x1b[0m",
	"\x1b", // lone ESC
}

func TestWidthMatchesAnsi(t *testing.T) {
	for _, c := range widthCases {
		if got, want := Width(c), ansi.StringWidth(c); got != want {
			t.Errorf("Width(%q) = %d, want %d (ansi)", c, got, want)
		}
	}
}

func FuzzWidth(f *testing.F) {
	for _, c := range widthCases {
		f.Add(c)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := Width(s), ansi.StringWidth(s); got != want {
			t.Errorf("Width(%q) = %d, want %d (ansi)", s, got, want)
		}
	})
}

// TestPad: padding a line is exactly "make it w cells wide", and refuses the
// two inputs a renderer must handle itself.
func TestPad(t *testing.T) {
	for _, tc := range []struct {
		s  string
		w  int
		n  int
		ok bool
	}{
		{"abc", 5, 2, true},
		{"abc", 3, 0, true},
		{"abcd", 3, 0, false},
		{"a\tb", 8, 0, false},
		{"", 4, 4, true},
		{"\x1b[38;5;4mred\x1b[m", 6, 3, true},
		{"日本語", 8, 2, true},
	} {
		n, ok := Pad(tc.s, tc.w)
		if n != tc.n || ok != tc.ok {
			t.Errorf("Pad(%q, %d) = %d, %v; want %d, %v", tc.s, tc.w, n, ok, tc.n, tc.ok)
		}
		if ok && Width(tc.s+Spaces(n)) != tc.w {
			t.Errorf("Pad(%q, %d): padded line is not %d cells", tc.s, tc.w, tc.w)
		}
	}
}

// TestSpaces covers the slab boundary — a run longer than the canned blanks
// still comes back the right length.
func TestSpaces(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 63, 256, 257, 1000} {
		if got := len(Spaces(n)); got != max(n, 0) {
			t.Errorf("Spaces(%d) is %d bytes", n, got)
		}
	}
}
