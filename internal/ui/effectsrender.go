package ui

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi/kitty"

	"matterbox/internal/editor"
	"matterbox/internal/effects"
	"matterbox/internal/hidden"
)

// Effect spans survive markdown styling and soft-wrapping as invisible sentinel
// runes bracketing the affected text: a start rune that encodes the effect id and
// a shared end rune.
//
// They are the Unicode TAG characters (U+E0020–U+E007F) — the ones emoji flag
// sequences are built from. That block is deliberate: they are assigned,
// Default_Ignorable, measure zero columns in the width tables lipgloss and the
// viewport use, sit outside the variation-selector range hidden.Strip removes,
// and never appear in real messages. Zero width is the load-bearing property:
// the sentinels ride inside every wrapped, measured and cached line, so anything
// that measured them as a column would shift a wrap and drift the pane's border
// (the unassigned tag runes just above U+E0000 do exactly that — don't use them).
//
// The sentinels are injected before renderMarkdown (effectsPreprocess) and
// resolved — recoloured and stripped — at the very last moment, on the already
// rendered and cached screen box (resolveEffects), so the mapping from "rune span
// in the plain text" to "cells on screen" is carried by the runes themselves
// through every transform, with no offset arithmetic, and a new animation frame
// costs a recolour of the visible rows rather than a re-render.
const (
	effSentinelBase rune = 0xE0020 // start rune = base + effect id (1..)
	effSentinelEnd  rune = 0xE007F // shared span-end rune (CANCEL TAG)
)

// effStart returns the start sentinel that encodes effect id.
func effStart(id byte) rune { return effSentinelBase + rune(id) }

// effectStaticPhase is the resting frame: the phase a Model starts at, before
// the ticker arms. 0.5 places the shimmer band mid-span so a still frame shows
// the gradient rather than the band's fully-exited edge. Tests paint at this
// phase to get a deterministic frame.
const effectStaticPhase = 0.5

// decodeEffectSpans pulls the effect spans out of a post body's MBF1 payload, or
// ok=false if it carries none.
func decodeEffectSpans(msg string) ([]effects.Span, bool) {
	payload, ok := hidden.Decode(effects.MagicEffects, msg)
	if !ok {
		return nil, false
	}
	return effects.UnmarshalPayload(payload)
}

// effectsPreprocess turns a raw post body carrying an MBF1 payload into the text
// renderMarkdown should style: the plain visible text with effect sentinels
// injected around each span. A body with no effects is returned unchanged (so
// game posts and ordinary posts are untouched, and renderMarkdown strips their
// payload as before).
func effectsPreprocess(raw string) string {
	spans, ok := decodeEffectSpans(raw)
	if !ok || len(spans) == 0 {
		return raw
	}
	return injectEffectSentinels(hidden.Strip(raw), spans)
}

// renderMarkdownEffects styles a post body with its effect spans marked, and is
// what the message pane renders through.
//
// It also enforces the one rule the sentinels can't enforce themselves: an
// effect boundary must not land *inside* a markdown token. `**bold*\shimmer{*}`
// puts a sentinel between the two closing asterisks, and goldmark then sees a
// broken token and prints the asterisks literally — the effect would corrupt the
// text it was decorating, which is the one thing this feature must never do. So
// the marked render is compared against the plain one: if taking the sentinels
// back out doesn't reproduce it byte for byte, they changed the markdown, and the
// post is rendered without effects instead. The words always win.
//
// The extra pass runs only for posts that carry effects, and only on a miss of
// the (width-independent) markdown cache above it.
func renderMarkdownEffects(raw string, ei *emojiImages, mr mrInlineFn, self string) string {
	marked := effectsPreprocess(raw)
	if marked == raw {
		return renderMarkdown(raw, ei, mr, self) // no effects — the ordinary path
	}
	// renderMarkdown strips the payload itself, so this is the body as every
	// other client sees it.
	plain := renderMarkdown(raw, ei, mr, self)
	out := renderMarkdown(marked, ei, mr, self)
	if stripEffectSentinels(out) != plain {
		return plain // a span straddles a markdown token: drop the effects, keep the text
	}
	return out
}

// injectEffectSentinels brackets each span of visible with start/end sentinels.
// Spans whose offsets fall outside visible (stale after a foreign edit) or name
// an unknown effect are dropped — the effect simply stops applying, it never
// corrupts the text. Nested spans open outermost-first and close innermost-first
// so resolveEffects's stack stays balanced.
func injectEffectSentinels(visible string, spans []effects.Span) string {
	rs := []rune(visible)
	valid := make([]effects.Span, 0, len(spans))
	for _, s := range spans {
		if s.Len > 0 && s.Start >= 0 && s.Start+s.Len <= len(rs) && effects.Name(s.ID) != "" {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		return visible
	}
	// Open in the directives' own nesting order (effects.Ordered), so the
	// innermost of a nested pair is pushed last and therefore wins the colour —
	// the same nesting the composer's markup shows.
	ordered := effects.Ordered(valid)
	var b strings.Builder
	b.Grow(len(visible) + len(valid)*8)
	for p := 0; p <= len(rs); p++ {
		for _, s := range valid {
			if s.Start+s.Len == p {
				b.WriteRune(effSentinelEnd) // a close carries no id; only the count matters
			}
		}
		for _, s := range ordered {
			if s.Start == p {
				b.WriteRune(effStart(s.ID))
			}
		}
		if p < len(rs) {
			b.WriteRune(rs[p])
		}
	}
	return b.String()
}

// hasEffectSentinel reports whether s carries any effect sentinel — the fast
// path so resolveEffects is a no-op on the overwhelming majority of posts.
func hasEffectSentinel(s string) bool {
	return strings.ContainsFunc(s, isEffectSentinel)
}

// isEffectSentinel reports whether r is one of the invisible span markers.
func isEffectSentinel(r rune) bool { return r > effSentinelBase && r <= effSentinelEnd }

// stripEffectSentinels removes the span markers without painting anything. The
// rendered lines carry them (that is how a span's position survives wrapping and
// caching), so any path that takes text *out* of those lines rather than to the
// screen — copying a selection, most of all — has to drop them, or an invisible
// marker rides along into the clipboard.
func stripEffectSentinels(s string) string {
	if !hasEffectSentinel(s) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isEffectSentinel(r) {
			return -1
		}
		return r
	}, s)
}

// resolveEffects rewrites already-rendered lines: it strips the effect sentinels
// and paints each run they bracket with the effect's colour at the given phase.
// It tracks the active-effect stack across lines, so a span split over a
// soft-wrap keeps one continuous gradient, and resets colour at each span end and
// each line end so it never bleeds. Lines with no sentinel are returned untouched.
//
// chrome is the number of leading runes on every line that belong to the pane's
// frame rather than the message — 1 when painting a bordered box (the `│`), 0
// when painting a bare viewport. They are copied through unpainted: a span that
// is still open across a soft-wrap would otherwise colour the border column of
// the continuation line. Escape sequences don't count against it.
//
// The visual width is unchanged: every rune it removes (a sentinel) or adds (an
// SGR sequence) has zero display width, so the caller's row geometry still holds.
func resolveEffects(lines []string, phase float64, chrome int) []string {
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
	lens := effectSpanLengths(lines, chrome) // rune length of each span, in start order
	out := make([]string, len(lines))
	type frame struct {
		id  byte
		n   int // total rune length of the span (for the shimmer sweep)
		pos int // rune position reached within the span
	}
	var stack []frame
	spanSeq := 0
	for li, l := range lines {
		var b strings.Builder
		b.Grow(len(l) + 16)
		i := chromeEnd(l, chrome)
		b.WriteString(l[:i])
		for i < len(l) {
			if l[i] == 0x1b { // copy an escape sequence through verbatim
				j := escSeqEnd(l, i)
				b.WriteString(l[i:j])
				i = j
				continue
			}
			r, size := utf8.DecodeRuneInString(l[i:])
			switch {
			case r == effSentinelEnd:
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				b.WriteString("\x1b[0m")
			case r > effSentinelBase && r < effSentinelEnd:
				n := 0
				if spanSeq < len(lens) {
					n = lens[spanSeq]
				}
				spanSeq++
				stack = append(stack, frame{id: byte(r - effSentinelBase), n: n})
			case len(stack) == 0:
				b.WriteRune(r)
			case r == kitty.Placeholder || unicode.Is(unicode.Mn, r):
				// A Kitty image placeholder cell (a custom emoji or thumbnail that
				// happens to sit inside the span) carries its 24-bit image id in the
				// foreground the placeholder itself just set — painting over it would
				// clobber the id and break the image (see kittyPlaceholder). Copy it
				// through, colour untouched. Its combining marks go the same way, and
				// so do ordinary combining marks, which simply inherit the base rune's
				// colour.
				b.WriteRune(r)
				for k := range stack {
					stack[k].pos++
				}
			default:
				top := stack[len(stack)-1]
				b.WriteString(effectColorSGR(top.id, top.pos, top.n, phase))
				b.WriteRune(r)
				for k := range stack { // every open span advances one rune
					stack[k].pos++
				}
			}
			i += size
		}
		if len(stack) > 0 {
			b.WriteString("\x1b[0m") // don't let colour bleed past the row
		}
		out[li] = b.String()
	}
	return out
}

// chromeEnd returns the byte index at which the message content of line l starts:
// just past its first n visible runes (the pane frame), escape sequences around
// them not counting. Painting must not touch those cells (see resolveEffects).
func chromeEnd(l string, n int) int {
	i := 0
	for skipped := 0; i < len(l) && skipped < n; {
		if l[i] == 0x1b {
			i = escSeqEnd(l, i)
			continue
		}
		_, size := utf8.DecodeRuneInString(l[i:])
		i += size
		skipped++
	}
	return i
}

// escSeqEnd returns the index just past the escape sequence starting at l[i]
// (l[i] must be 0x1b). It must consume each kind whole, because a painter that
// walked into a sequence's payload would inject its colour SGRs *inside* it: a
// CSI (ESC [ … final) ends at its final byte; a string sequence — OSC, APC, DCS,
// PM — runs to its terminator (ST or BEL), which matters because the message
// lines carry OSC-8 hyperlinks (see linkclick.go) whose URL would otherwise be
// torn apart and printed. Anything else is a two-byte escape.
func escSeqEnd(l string, i int) int {
	j := i + 1
	if j >= len(l) {
		return j
	}
	switch l[j] {
	case '[': // CSI
		j++
		for j < len(l) && (l[j] < 0x40 || l[j] > 0x7e) {
			j++
		}
		if j < len(l) {
			j++
		}
	case ']', '_', 'P', '^': // OSC, APC, DCS, PM — string sequences
		j++
		for j < len(l) {
			if l[j] == 0x07 { // BEL
				j++
				break
			}
			if l[j] == 0x1b && j+1 < len(l) && l[j+1] == '\\' { // ST
				j += 2
				break
			}
			j++
		}
	default:
		j++
	}
	return j
}

// effectSpanLengths returns the rune length of every effect span in the order
// its start sentinel appears, counting nested spans (a rune inside k nested
// spans counts toward all k). resolveEffects consumes these in the same order,
// and must be given the same chrome width, or the two walks disagree on which
// runes belong to a span.
func effectSpanLengths(lines []string, chrome int) []int {
	var lens []int
	var open []int // indices into lens for currently-open spans
	for _, l := range lines {
		i := chromeEnd(l, chrome)
		for i < len(l) {
			if l[i] == 0x1b {
				i = escSeqEnd(l, i)
				continue
			}
			r, size := utf8.DecodeRuneInString(l[i:])
			switch {
			case r == effSentinelEnd:
				if len(open) > 0 {
					open = open[:len(open)-1]
				}
			case r > effSentinelBase && r < effSentinelEnd:
				lens = append(lens, 0)
				open = append(open, len(lens)-1)
			default:
				for _, idx := range open {
					lens[idx]++
				}
			}
			i += size
		}
	}
	return lens
}

// effectColorSGR returns the truecolor foreground SGR for the rune at position
// pos within a span of n runes, at animation phase (in [0,1)). Empty for an
// unknown effect, which leaves the rune's existing colour alone.
func effectColorSGR(id byte, pos, n int, phase float64) string {
	c := effectColor(id, pos, n, phase)
	if c == nil {
		return ""
	}
	return rgbSGR(c)
}

// effectColor is the colour an effect paints the rune at position pos within a
// span of n runes, at animation phase (in [0,1)), or nil if id is unknown.
// Shimmer reuses the composer's command gradient (editor.ShimmerColor) so a
// /command token and a \shimmer{} span are the same thing; rainbow cycles hue
// along the span; pulse and glow breathe uniformly. The composer's static
// preview reads from here too, so it can't drift from what gets sent.
func effectColor(id byte, pos, n int, phase float64) color.Color {
	if id == effects.Shimmer {
		return editor.ShimmerColor(pos, n, phase)
	}
	var h, s, v float64
	x := float64(pos)
	switch id {
	case effects.Rainbow:
		h, s, v = 0.06*x+phase, 0.85, 1.0
	case effects.Pulse:
		h, s = 0.02, 0.75
		v = 0.45 + 0.55*(0.5+0.5*math.Sin(2*math.Pi*phase))
	case effects.Glow:
		h, s = 0.12, 0.80
		v = 0.70 + 0.30*(0.5+0.5*math.Sin(2*math.Pi*phase))
	default:
		return nil
	}
	r, g, b := hsv2rgb(h, s, clamp01(v))
	return color.RGBA{R: r, G: g, B: b, A: 0xff}
}

// rgbSGR renders a color.Color as a truecolor foreground SGR.
func rgbSGR(c color.Color) string {
	cr, cg, cb, _ := c.RGBA() // 16-bit per channel
	return sgr(uint8(cr>>8), uint8(cg>>8), uint8(cb>>8))
}

func sgr(r, g, b uint8) string { return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b) }

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// hsv2rgb converts HSV (h wrapped to [0,1), s and v in [0,1]) to 8-bit RGB.
func hsv2rgb(h, s, v float64) (uint8, uint8, uint8) {
	h -= math.Floor(h)
	i := int(h * 6)
	f := h*6 - float64(i)
	p := v * (1 - s)
	q := v * (1 - f*s)
	t := v * (1 - (1-f)*s)
	var r, g, b float64
	switch i % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return uint8(r*255 + 0.5), uint8(g*255 + 0.5), uint8(b*255 + 0.5)
}
