// Package textwidth computes the rendered terminal cell width of a string,
// returning exactly the same value as github.com/charmbracelet/x/ansi's
// StringWidth but several times faster on the content a TUI actually emits.
//
// ansi.StringWidth (and lipgloss.Width, and the bubbles viewport's internal
// row math) measure width by segmenting the string into grapheme clusters via
// uax29 and looking each one up — correct for arbitrary Unicode, but the
// segmentation dominates CPU on a render hot path that re-measures the same
// styled lines every keystroke. Width fast-paths the three things that make up
// nearly all rendered output — printable ASCII, ANSI SGR escape sequences, and
// box-drawing / block-element runes — with a plain byte scan, and only falls
// back to ansi.StringWidth for spans that could form a wide or multi-rune
// grapheme cluster (CJK, emoji, ZWJ sequences, regional-indicator flags,
// combining marks, variation selectors). The fallback guarantees the result is
// always identical to ansi.StringWidth (see the fuzz test).
package textwidth

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Width returns the rendered cell width of s, identical to ansi.StringWidth.
func Width(s string) int {
	w := 0
	i := 0
	n := len(s)
	for i < n {
		b := s[i]
		switch {
		case b >= 0x20 && b <= 0x7e:
			// Printable ASCII — the overwhelmingly common byte. Every
			// printable-ASCII byte followed by another ASCII byte is a
			// grapheme boundary, so consume the whole run with no per-byte
			// lookahead and resolve only the final byte's boundary once.
			start := i
			i++
			for i < n {
				c := s[i]
				if c < 0x20 || c > 0x7e {
					break
				}
				i++
			}
			if boundaryAfter(s, i) {
				w += i - start
				continue
			}
			// The last printable byte may merge with a following combining
			// mark or variation selector into one cluster; defer from it so
			// the cluster is measured whole.
			w += i - start - 1
			return w + ansi.StringWidth(s[i-1:])
		case b == 0x1b:
			// A plain CSI sequence — ESC '[' then only parameter bytes
			// (0x30–0x3f) then one final byte (0x40–0x7e) — is SGR colours
			// and the like: zero width, skip it inline. This is the exact
			// shape lipgloss emits. Anything else (an intermediate byte, an
			// embedded control, a non-CSI escape, a truncation) has fiddly
			// VT-parser semantics, so defer the remainder to the exact path.
			if i+1 < n && s[i+1] == '[' {
				j := i + 2
				for j < n && s[j] >= 0x30 && s[j] <= 0x3f {
					j++
				}
				if j < n && s[j] >= 0x40 && s[j] <= 0x7e {
					i = j + 1
					continue
				}
			}
			return w + ansi.StringWidth(s[i:])
		case b < utf8.RuneSelf:
			// C0 control / DEL: zero width.
			i++
		case isBoxAt(s, i):
			// Box Drawing + Block Elements (U+2500–U+259F) — the bulk of every
			// rendered table — are all width 1 and never combine. Consume a
			// whole run straight from the bytes (no rune decode), resolving the
			// final boundary once; each is three bytes.
			start := i
			i += 3
			for isBoxAt(s, i) {
				i += 3
			}
			if boundaryAfter(s, i) {
				w += (i - start) / 3
				continue
			}
			// The last box rune may be a base for a following combining mark;
			// defer from it so the cluster is measured whole.
			w += (i-start)/3 - 1
			return w + ansi.StringWidth(s[i-3:])
		default:
			// Wide / combining / emoji territory: exact for the rest of the
			// string.
			return w + ansi.StringWidth(s[i:])
		}
	}
	return w
}

// isBoxAt reports whether a Box Drawing or Block Element rune (U+2500–U+259F)
// starts at byte k. Those encode as E2 94 80 .. E2 96 9F, so the check is three
// byte comparisons — no utf8 decode.
func isBoxAt(s string, k int) bool {
	if k+2 >= len(s) || s[k] != 0xE2 {
		return false
	}
	b1, b2 := s[k+1], s[k+2]
	if b2 < 0x80 { // not a UTF-8 continuation byte — invalid/truncated sequence
		return false
	}
	switch b1 {
	case 0x94, 0x95: // U+2500–U+257F
		return b2 <= 0xbf
	case 0x96: // U+2580–U+259F (block elements; U+25A0+ are not box/block)
		return b2 <= 0x9f
	}
	return false
}

// boundaryAfter reports whether position j is a guaranteed grapheme-cluster
// boundary that needs no further lookahead: end of string, an ASCII byte, or
// the start of another box-drawing / block-element rune. None of those extend
// or widen the preceding rune's cluster, so the preceding rune's standalone
// width is exact. Anything else (a possible combining mark, ZWJ, variation
// selector, …) returns false, so the caller defers that rune onward to the
// exact implementation rather than risk mis-splitting a cluster.
func boundaryAfter(s string, j int) bool {
	if j >= len(s) {
		return true
	}
	if s[j] < utf8.RuneSelf {
		return true
	}
	return isBoxAt(s, j)
}

// spaces is a slab of blanks Pad hands out sub-slices of, so padding a line to
// width costs no allocation of its own. Wider than any terminal anybody is
// padding a pane to; Pad falls back to strings.Repeat past it.
const spaces = "                                                                " +
	"                                                                " +
	"                                                                " +
	"                                                                "

// Spaces returns a string of exactly n blanks (empty for n <= 0), without
// allocating for the terminal-sized runs a render pass actually asks for.
func Spaces(n int) string {
	if n <= 0 {
		return ""
	}
	if n <= len(spaces) {
		return spaces[:n]
	}
	return strings.Repeat(" ", n)
}

// Pad reports the number of blanks s needs to reach exactly w cells, and
// whether padding is all it needs. It is false when s is already wider than w
// (the caller has to wrap, which is not this package's job) or contains a tab
// (whose width depends on the renderer's tab stops). Both cases mean "hand
// this line to the full renderer"; everything else is a plain append of
// Spaces(n), which is what a render hot path does with lines it built to width
// in the first place.
func Pad(s string, w int) (n int, ok bool) {
	if strings.IndexByte(s, '\t') >= 0 {
		return 0, false
	}
	if d := w - Width(s); d >= 0 {
		return d, true
	}
	return 0, false
}
