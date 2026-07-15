package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"

	"matterbox/internal/effects"
)

// The \scroll{} effect is geometric: it marquees a span's letters sideways rather
// than recolouring them. It runs as a second paint pass (resolveGeometry) after
// the colour pass (resolveEffects), on the same already-rendered, cached box — a
// frame is still a repaint of the visible rows, never a re-render. resolveEffects
// hands the scroll spans on untouched (it re-emits their sentinels instead of
// stripping them); this file rotates their cells and then strips the sentinels.
// Width is preserved throughout, so no vertical or horizontal space is reserved:
// a marquee is a pure permutation of the span's own cells.

// resolveGeometry is the paint-time half of the geometric effects. It runs after
// resolveEffects, on the same rendered box: it rotates every scroll span's cells
// (so the text slides left and wraps) and strips the now-spent geometric
// sentinels. Every row keeps its exact visual width. chrome is unused today (the
// marquee needs no frame offset) but kept for symmetry with resolveEffects.
//
// Cheap no-op fast path: after resolveEffects, the only sentinels left on a line
// are the geometric ones, so hasEffectSentinel is exactly "this line has geometry
// to resolve."
func resolveGeometry(lines []string, phase float64, chrome int) []string {
	any := false
	for _, l := range lines {
		if hasEffectSentinel(l) {
			any = true
			break
		}
	}
	if !any {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = finishGeomLine(l, phase)
	}
	return out
}

// finishGeomLine rotates any scroll span on the line and then strips every
// remaining geometric sentinel (width-preserving, since they are all zero-width).
func finishGeomLine(line string, phase float64) string {
	if !hasEffectSentinel(line) {
		return line
	}
	line = scrollLine(line, phase)
	return stripEffectSentinels(line)
}

// scrollLine marquees each \scroll{} span that opens and closes on this line: its
// cells are rotated so the text slides left and wraps. A span that soft-wrapped
// (no matching close on this line) or that holds an image/link is left static —
// the sentinels are stripped later and the text stands still. Total visual width
// is unchanged: rotation is a permutation of the span's cells.
func scrollLine(line string, phase float64) string {
	scrollStart := effStart(effects.Scroll)
	if !strings.ContainsRune(line, scrollStart) {
		return line
	}
	var b strings.Builder
	b.Grow(len(line) + 32)
	i := 0
	for i < len(line) {
		if line[i] == 0x1b {
			end := escSeqEnd(line, i)
			b.WriteString(line[i:end])
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if r == scrollStart {
			if end := matchGeomEnd(line, i+size); end >= 0 {
				if rot, ok := rotateScroll(line[i+size:end], phase); ok {
					b.WriteRune(r) // keep the sentinel; stripped after
					b.WriteString(rot)
					i = end // resume at the matching END
					continue
				}
			}
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// matchGeomEnd returns the byte index of the effSentinelEnd that closes a
// geometric span whose open sits just before `from`, or −1 if it doesn't close
// on this line. Only geometric sentinels remain at paint time, so nesting is
// tracked by a simple depth count.
func matchGeomEnd(line string, from int) int {
	depth := 0
	for i := from; i < len(line); {
		if line[i] == 0x1b {
			i = escSeqEnd(line, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		switch {
		case r == effSentinelEnd:
			if depth == 0 {
				return i
			}
			depth--
		case r > effSentinelBase && r < effSentinelEnd:
			depth++
		}
		i += size
	}
	return -1
}

// rotateScroll rotates the styled cells of a scroll span's body by phase, so the
// text scrolls left and wraps. It returns ok=false — asking the caller to leave
// the span static — when the body holds an image placeholder or an OSC-8 link
// (scrambling either would break it) or is empty.
func rotateScroll(body string, phase float64) (string, bool) {
	type cell struct {
		sgr, s string
		w      int
	}
	var cells []cell
	sgr := ""
	for i := 0; i < len(body); {
		if body[i] == 0x1b {
			end := escSeqEnd(body, i)
			seg := body[i:end]
			if isOSC8(seg) {
				return "", false
			}
			if isSGR(seg) {
				sgr = updateSGR(sgr, seg)
			}
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(body[i:])
		switch {
		case isEffectSentinel(r):
			// a nested geometric sentinel: drop it, keep the run intact
		case r == kitty.Placeholder:
			return "", false
		case runeCells(r) == 0 && len(cells) > 0:
			cells[len(cells)-1].s += string(r) // combining mark rides its base
		default:
			cells = append(cells, cell{sgr: sgr, s: string(r), w: runeCells(r)})
		}
		i += size
	}
	if len(cells) == 0 {
		return "", false
	}
	shift := int(phase * float64(len(cells)))
	if shift >= len(cells) {
		shift = len(cells) - 1
	}
	var b strings.Builder
	for k := range cells {
		c := cells[(k+shift)%len(cells)]
		b.WriteString(c.sgr)
		b.WriteString(c.s)
	}
	b.WriteString("\x1b[0m")
	return b.String(), true
}

// runeCells is a rune's terminal column width (0 for combining/zero-width, 2 for
// wide, else 1).
func runeCells(r rune) int { return ansi.StringWidth(string(r)) }

// isOSC8 reports whether an escape sequence opens or closes an OSC-8 hyperlink.
func isOSC8(esc string) bool { return strings.HasPrefix(esc, "\x1b]8") }

// isSGR reports whether an escape sequence is an SGR colour/attribute run (ESC [ … m).
func isSGR(esc string) bool {
	return len(esc) >= 3 && esc[0] == 0x1b && esc[1] == '[' && esc[len(esc)-1] == 'm'
}

// updateSGR folds one SGR sequence into the running style: a reset clears it,
// anything else accumulates — so the result reproduces the live style at a point,
// the way sgrState does over a whole string.
func updateSGR(cur, esc string) string {
	params := esc[2 : len(esc)-1]
	if params == "" || params == "0" || strings.HasPrefix(params, "0;") {
		return ""
	}
	return cur + esc
}
