// Package safeterm strips terminal control characters out of untrusted text.
//
// Anything a remote peer authored — message bodies, usernames, channel and
// team names, emoji names, filenames, custom-status text — ends up on a
// terminal that interprets escape sequences. A raw ESC in that text lets the
// sender clear or repaint the screen, spoof a shell prompt, or write the
// user's clipboard with OSC 52. Run every such string through Text or Line
// before it reaches a render path.
package safeterm

import "strings"

// Text returns s with terminal control characters removed. Newline and tab
// survive (they are ordinary layout in a multi-line message body);
// everything else below U+0020, DEL, and the C1 block U+0080–U+009F — which
// carries single-byte CSI (U+009B) and OSC (U+009D) — is dropped.
func Text(s string) string { return strip(s, true) }

// Line is Text for single-line fields: newline and tab go too, so a value
// can never break out of the line it is rendered on.
func Line(s string) string { return strip(s, false) }

func strip(s string, keepLayout bool) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return unsafeRune(r, keepLayout) }) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unsafeRune(r, keepLayout) {
			return -1
		}
		return r
	}, s)
}

func unsafeRune(r rune, keepLayout bool) bool {
	if keepLayout && (r == '\n' || r == '\t') {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}
