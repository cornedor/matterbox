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
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Width returns the rendered cell width of s, identical to ansi.StringWidth.
func Width(s string) int {
	w := 0
	i := 0
	for i < len(s) {
		b := s[i]
		if b < utf8.RuneSelf {
			switch {
			case b == 0x1b:
				// A plain CSI sequence — ESC '[' then only parameter bytes
				// (0x30–0x3f) then one final byte (0x40–0x7e) — is SGR colours
				// and the like: zero width, skip it inline. This is the exact
				// shape lipgloss emits. Anything else (an intermediate byte, an
				// embedded control, a non-CSI escape, a truncation) has fiddly
				// VT-parser semantics, so defer the remainder to the exact path.
				if i+1 < len(s) && s[i+1] == '[' {
					j := i + 2
					for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
						j++
					}
					if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
						i = j + 1
						continue
					}
				}
				return w + ansi.StringWidth(s[i:])
			case b >= 0x20 && b != 0x7f:
				// Printable ASCII is width 1 — unless a combining mark or
				// variation selector follows and merges into one cluster with
				// it, in which case defer so the cluster is measured whole.
				if !boundaryAfter(s, i+1) {
					return w + ansi.StringWidth(s[i:])
				}
				w++
				i++
			default:
				// C0 control / DEL: zero width.
				i++
			}
			continue
		}
		// Non-ASCII. Box Drawing + Block Elements (U+2500–U+259F) — the bulk of
		// every rendered table — are all width 1 and never combine. Recognise
		// them straight from the bytes (no rune decode) and fast-path.
		if isBoxAt(s, i) && boundaryAfter(s, i+3) {
			w++
			i += 3
			continue
		}
		// Wide / combining / emoji territory: exact for the rest of the string.
		return w + ansi.StringWidth(s[i:])
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
