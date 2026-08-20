package ui

import (
	"encoding/base64"
	"fmt"
	"image/color"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
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

// effectGeometric reports whether an effect moves runes around the screen rather
// than just recolouring them. A geometric span is left uncoloured by
// resolveEffects and its sentinels are handed on to resolveEffects's second pass
// (resolveGeometry), which does the moving. scroll marquees its letters sideways.
func effectGeometric(id byte) bool {
	return id == effects.Scroll
}

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
func renderMarkdownEffects(raw string, ei *emojiImages, mr changeInlineFn, self string) string {
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
	// A \copy{} span becomes a click-to-copy OSC 8 hyperlink. It is baked in here,
	// once, rather than at paint time: the click hit-test reads this rendered body
	// (via the viewport), not the per-frame recolour, so the link has to live in
	// the body itself. It is phase-independent, so caching it is correct.
	return injectActionLinks(out)
}

// The internal OSC 8 URL schemes that make an effect span a mouse target. They
// are not real schemes: activateLink intercepts them before the open-link
// machinery (see openTarget). Copy carries the base64 of its text; spoiler
// carries its index within the post, so a hover can reveal exactly the one the
// pointer is over (revealSpoiler).
const (
	copyURLScheme    = "matterbox-copy:"
	spoilerURLScheme = "matterbox-spoiler:"
)

// copyGlyph is the little chip icon that marks a \copy{} span as clickable.
const copyGlyph = "⧉"

// encodeCopyPayload / decodeCopyPayload round-trip the chip's text through the
// OSC 8 URL. Base64 keeps arbitrary text — spaces, punctuation, control bytes —
// out of the escape sequence's own syntax.
func encodeCopyPayload(text string) string {
	return base64.StdEncoding.EncodeToString([]byte(text))
}

func decodeCopyPayload(enc string) (string, bool) {
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// injectActionLinks wraps every \copy{} and \spoiler{} span in the rendered body
// in an OSC 8 hyperlink, so the mouse can act on it: the same hit-test that opens
// ordinary links resolves it (see linkAt / hoverLinkAt). The span's effect
// sentinels stay inside the link, so resolveEffects still paints it — only a
// zero-width wrapper (and, for copy, a leading chip icon) is added.
//
// It runs on the unwrapped body, where each span is contiguous, and tracks the
// sentinel nesting so a span that itself contains effects closes at the right
// brace. A body with neither kind of span is returned untouched.
func injectActionLinks(s string) string {
	if !strings.ContainsRune(s, effStart(effects.Copy)) &&
		!strings.ContainsRune(s, effStart(effects.Spoiler)) {
		return s
	}
	base := &strings.Builder{}
	base.Grow(len(s) + 48)
	// A frame per open sentinel. An action frame (copy or spoiler) owns a buffer
	// that captures its whole rendered body (sentinels and all); writes go to the
	// innermost such buffer, or to base when none is open, so an action span's
	// bytes are held back until its close, when they're wrapped in the hyperlink.
	type frame struct {
		id      byte
		buf     *strings.Builder // non-nil for an action span
		spoiler int              // this span's spoiler index, when id == Spoiler
	}
	var stack []frame
	spoilers := 0
	cur := func() *strings.Builder {
		for k := len(stack) - 1; k >= 0; k-- {
			if stack[k].buf != nil {
				return stack[k].buf
			}
		}
		return base
	}
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // an escape (SGR, an inner OSC 8 link): copy it through
			j := escSeqEnd(s, i)
			cur().WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r > effSentinelBase && r < effSentinelEnd: // a span opens
			f := frame{id: byte(r - effSentinelBase)}
			switch f.id {
			case effects.Copy:
				f.buf = &strings.Builder{}
			case effects.Spoiler:
				f.buf = &strings.Builder{}
				f.spoiler = spoilers
				spoilers++
			}
			stack = append(stack, f)
			cur().WriteRune(r) // START goes inside its own action buffer, if any
		case r == effSentinelEnd: // a span closes
			if n := len(stack); n > 0 {
				f := stack[n-1]
				stack = stack[:n-1]
				if f.buf != nil {
					f.buf.WriteRune(r)
					cur().WriteString(actionLink(f.id, f.spoiler, f.buf.String()))
				} else {
					cur().WriteRune(r)
				}
			}
		default:
			cur().WriteRune(r)
		}
		i += size
	}
	// Any action frame still open is unbalanced; flush its raw bytes so nothing is
	// swallowed (Parse would not have produced such a span, but never lose text).
	for _, f := range stack {
		if f.buf != nil {
			base.WriteString(f.buf.String())
		}
	}
	return base.String()
}

// actionLink wraps a copy/spoiler span's rendered body in its OSC 8 hyperlink.
// Copy carries the base64 of its plain text and gains a leading chip icon (styled
// like the chip, kept out of the payload); spoiler carries its per-post index so
// revealSpoiler can lift the block from exactly the one being hovered.
func actionLink(id byte, spoilerIdx int, body string) string {
	switch id {
	case effects.Copy:
		payload := ansi.Strip(stripEffectSentinels(body))
		icon := effectVisualAt(effects.Copy, 0, 1, 0).ansi() + copyGlyph + "\x1b[0m "
		return osc8Link(copyURLScheme+encodeCopyPayload(payload), icon+body)
	case effects.Spoiler:
		return osc8Link(spoilerURLScheme+strconv.Itoa(spoilerIdx), body)
	default:
		return body
	}
}

// revealSpoiler lifts the block from the spoiler the pointer is over, so its text
// shows while hovered. url is spoilerURLScheme + index; the index-th spoiler span
// in body (by opening order, matching injectActionLinks) has its own effect
// sentinels dropped, so resolveEffects paints it as ordinary text — while every
// other spoiler, and any effect nested inside this one, is untouched. The OSC 8
// wrapper stays, so the pointer keeps resting on it and the reveal holds.
func revealSpoiler(body, url string) string {
	idxStr, ok := strings.CutPrefix(url, spoilerURLScheme)
	if !ok {
		return body
	}
	want, err := strconv.Atoi(idxStr)
	if err != nil {
		return body
	}
	spoilerStart := effStart(effects.Spoiler)
	var b strings.Builder
	b.Grow(len(body))
	seen := -1     // spoiler spans opened so far
	depth := 0     // current sentinel nesting depth
	skipDepth := 0 // depth at which the wanted spoiler opened; 0 = not inside it
	for i := 0; i < len(body); {
		if body[i] == 0x1b {
			j := escSeqEnd(body, i)
			b.WriteString(body[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(body[i:])
		write := true
		switch {
		case r > effSentinelBase && r < effSentinelEnd: // a span opens
			depth++
			if r == spoilerStart {
				seen++
				if seen == want {
					skipDepth = depth
					write = false // drop the wanted spoiler's START
				}
			}
		case r == effSentinelEnd: // a span closes
			if skipDepth != 0 && depth == skipDepth {
				skipDepth = 0
				write = false // drop the matching END
			}
			depth--
		}
		if write {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// actionHoverHint is the footer text for a hovered copy/spoiler link — a friendly
// label rather than the internal URL a plain link would show. ok is false for an
// ordinary link, which keeps its "↗ url".
func actionHoverHint(url string) (string, bool) {
	switch {
	case strings.HasPrefix(url, copyURLScheme):
		return copyGlyph + " click to copy", true
	case strings.HasPrefix(url, spoilerURLScheme):
		return "spoiler — hidden until you hover it", true
	}
	return "", false
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

// headerTitleInline is a channel header as the messages-pane title line shows
// it: one line of the visible text with effect sentinels injected, so the same
// paintEffects pass that colours the message rows colours the header too. Any
// payload is stripped (ours after injecting its spans, a foreign one outright)
// and newlines fold to spaces — the title is a single row.
func headerTitleInline(raw string) string {
	s := hidden.Strip(effectsPreprocess(raw))
	return strings.Join(strings.Fields(s), " ")
}

// nameEffectAllowed reports whether an effect makes sense inside a channel
// name. The interactive and geometric ones don't: a copy chip and a spoiler
// bar want a mouse target the sidebar rows don't offer, and a scroll marquee
// in a channel list would be chaos. Their spans render as plain text.
func nameEffectAllowed(id byte) bool {
	switch id {
	case effects.Copy, effects.Spoiler, effects.Scroll:
		return false
	}
	return true
}

// nameEffectSentinels is a channel name as the styled renders want it: the
// visible text with the allowed effect spans marked as sentinels, the payload
// stripped. A name with no payload passes through untouched.
func nameEffectSentinels(raw string) string {
	spans, ok := decodeEffectSpans(raw)
	if !ok || len(spans) == 0 {
		return hidden.Strip(raw)
	}
	kept := make([]effects.Span, 0, len(spans))
	for _, s := range spans {
		if nameEffectAllowed(s.ID) {
			kept = append(kept, s)
		}
	}
	return injectEffectSentinels(hidden.Strip(raw), kept)
}

// resolveStaticLine paints one line's effect sentinels at the resting phase and
// returns it, colours baked in. Channel names resolve through here rather than
// the per-frame paint pass: they live in fingerprint-cached renders (the
// sidebar, the memoized title) in too many places to repaint per frame, so a
// name's effect is a steady colour — rainbow a fixed gradient, ok a calm green
// — never an animation. A line with no sentinels comes back as-is.
func resolveStaticLine(s string) string {
	if !hasEffectSentinel(s) {
		return s
	}
	return resolveEffects([]string{closeEffectSpans(s)}, effectStaticPhase, 0)[0]
}

// closeEffectSpans appends the end sentinels a right-truncation cut off, so a
// clipped line stays balanced: resolveEffects carries its span stack across
// lines (that is how a soft-wrapped span keeps one gradient), and a title line
// left with an open span would paint every message row below it.
func closeEffectSpans(l string) string {
	depth := 0
	for i := 0; i < len(l); {
		if l[i] == 0x1b {
			i = escSeqEnd(l, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(l[i:])
		switch {
		case r == effSentinelEnd:
			if depth > 0 {
				depth--
			}
		case r > effSentinelBase && r < effSentinelEnd:
			depth++
		}
		i += size
	}
	if depth == 0 {
		return l
	}
	return l + strings.Repeat(string(effSentinelEnd), depth)
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
				geo := false
				if len(stack) > 0 {
					geo = effectGeometric(stack[len(stack)-1].id)
					stack = stack[:len(stack)-1]
				}
				if geo {
					b.WriteRune(r) // hand the geometric span's close to the geometry pass
				} else {
					b.WriteString("\x1b[0m")
				}
			case r > effSentinelBase && r < effSentinelEnd:
				n := 0
				if spanSeq < len(lens) {
					n = lens[spanSeq]
				}
				spanSeq++
				id := byte(r - effSentinelBase)
				stack = append(stack, frame{id: id, n: n})
				if effectGeometric(id) {
					b.WriteRune(r) // hand the geometric span's open to the geometry pass
				}
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
				// Merge every open effect, outer→inner, so nested effects compose:
				// the innermost colour wins but an outer \underline{} still underlines
				// the coloured run. (Only the top used to paint, so an attribute on an
				// outer span was lost the moment a colour opened inside it.) A geometric
				// span (scroll) adds no colour — the geometry pass moves the rune instead
				// — but a colour nested inside one still paints, so the moved glyph
				// carries it.
				var v effectVisual
				for k := range stack {
					if effectGeometric(stack[k].id) {
						continue
					}
					v = v.merge(effectVisualAt(stack[k].id, stack[k].pos, stack[k].n, phase))
				}
				b.WriteString(v.ansi())
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

// effectVisual is everything an effect contributes to a rune: an optional
// foreground/background colour, SGR attribute flags, and — for the copy chip — a
// note that the span is a click target (resolved into an OSC 8 hyperlink at
// render time, not here). A stack of these merges outer→inner so nested effects
// compose. It is the one description every consumer derives from: the painter
// (ansi), the composer preview and the picker sample.
type effectVisual struct {
	fg, bg   color.Color // nil = leave the rune's existing colour
	attr     effectAttr
	copyLink bool
}

// effectAttr is a set of SGR attributes an effect can add on top of its colour.
type effectAttr uint8

const (
	attrUnderline effectAttr = 1 << iota
	attrStrike
)

// merge layers o on top of v (o is the inner, more specific effect): a colour o
// sets wins, attributes accumulate. Used to fold the active-effect stack into one
// style per rune.
func (v effectVisual) merge(o effectVisual) effectVisual {
	if o.fg != nil {
		v.fg = o.fg
	}
	if o.bg != nil {
		v.bg = o.bg
	}
	v.attr |= o.attr
	v.copyLink = v.copyLink || o.copyLink
	return v
}

// ansi is the SGR sequence that paints a rune in this style, or "" for the empty
// style (which leaves the rune untouched). It never carries the copy hyperlink —
// that is an OSC 8 wrapper injected once at render time (injectCopyLinks), not a
// per-rune escape.
func (v effectVisual) ansi() string {
	var codes []string
	if v.attr&attrUnderline != 0 {
		codes = append(codes, "4")
	}
	if v.attr&attrStrike != 0 {
		codes = append(codes, "9")
	}
	if v.fg != nil {
		r, g, b := rgb8(v.fg)
		codes = append(codes, fmt.Sprintf("38;2;%d;%d;%d", r, g, b))
	}
	if v.bg != nil {
		r, g, b := rgb8(v.bg)
		codes = append(codes, fmt.Sprintf("48;2;%d;%d;%d", r, g, b))
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

// The colours the non-gradient effects paint with. Spoiler paints fg and bg the
// same, so its runes are an opaque bar whatever the terminal background — the text
// is there (copy still yields it) but unreadable until revealed. spoilerHint is
// the legible stand-in the composer and picker show instead of that bar.
var (
	spoilerBlock = color.RGBA{R: 0x64, G: 0x64, B: 0x64, A: 0xff}
	spoilerHint  = color.RGBA{R: 0x96, G: 0x96, B: 0x96, A: 0xff}
	copyChip     = color.RGBA{R: 0x56, G: 0xb6, B: 0xde, A: 0xff} // link-cyan
)

// effectVisualAt is how an effect paints the rune at position pos within a span
// of n runes, at animation phase — the message-pane truth. The colour effects
// defer to effectColor; the rest add attributes or a background block.
func effectVisualAt(id byte, pos, n int, phase float64) effectVisual {
	switch id {
	case effects.Underline:
		return effectVisual{attr: attrUnderline}
	case effects.Spoiler:
		return effectVisual{fg: spoilerBlock, bg: spoilerBlock}
	case effects.Copy:
		return effectVisual{fg: copyChip, attr: attrUnderline, copyLink: true}
	default:
		return effectVisual{fg: effectColor(id, pos, n, phase)}
	}
}

// effectHintVisual is how an effect shows itself where it must stay legible — the
// composer preview and the "\" picker — rather than perform. It matches the
// message look except that spoiler, which would be an unreadable block, becomes a
// struck-through grey ("this will be redacted"), and the copy chip drops its
// hyperlink (nothing to click in a preview).
func effectHintVisual(id byte, pos, n int, phase float64) effectVisual {
	if id == effects.Spoiler {
		return effectVisual{fg: spoilerHint, attr: attrStrike}
	}
	v := effectVisualAt(id, pos, n, phase)
	v.copyLink = false
	return v
}

// rgb8 is a color.Color's 8-bit RGB components.
func rgb8(c color.Color) (uint8, uint8, uint8) {
	r, g, b, _ := c.RGBA() // 16-bit per channel
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}

// effectColor is the colour an effect paints the rune at position pos within a
// span of n runes, at animation phase (in [0,1)), or nil if id is unknown.
// Shimmer reuses the composer's command gradient (editor.ShimmerColor) so a
// /command token and a \shimmer{} span are the same thing; rainbow cycles hue
// along the span; pulse, glow and warn breathe uniformly; ok, bad and whisper
// are steady single colours (their result is phase-independent, which is what
// effectAnimated keys on). Scroll is geometric — the message pane marquees its
// letters, not their colour — so here it yields only a steady hint tint for the
// composer preview and picker. The composer's static preview reads from here too,
// so it can't drift from what gets sent.
func effectColor(id byte, pos, n int, phase float64) color.Color {
	if id == effects.Shimmer {
		return editor.ShimmerColor(pos, n, phase)
	}
	var h, s, v float64
	x := float64(pos)
	breath := 0.5 + 0.5*math.Sin(2*math.Pi*phase) // 0..1, one calm cycle per loop
	switch id {
	case effects.Rainbow:
		h, s, v = 0.06*x+phase, 0.85, 1.0
	case effects.Scroll:
		// Scroll is geometric — the message pane marquees the letters, it does not
		// colour them (see effectGeometric / resolveGeometry). This steady magenta
		// is only a hint: the composer preview and the "\" picker can't animate
		// geometry, so they show the body in a still tint that reads as "this moves."
		h, s, v = 0.85, 0.85, 0.95
	case effects.Pulse:
		h, s = 0.02, 0.75
		v = 0.45 + 0.55*breath
	case effects.Glow:
		h, s = 0.12, 0.80
		v = 0.70 + 0.30*breath
	case effects.Warn:
		// Amber that breathes: enough movement to pull the eye to a caution,
		// never so much it strobes. The other three below hold still — a status
		// or an aside should read as itself, not perform.
		h, s = 0.09, 0.95
		v = 0.72 + 0.28*breath
	case effects.Ok:
		h, s, v = 0.34, 0.70, 0.85 // a calm, confident green
	case effects.Bad:
		h, s, v = 0.00, 0.80, 0.95 // a clear, alarming red
	case effects.Whisper:
		h, s, v = 0.00, 0.00, 0.45 // a faint grey — present, but stood back
	default:
		return nil
	}
	r, g, b := hsv2rgb(h, s, clamp01(v))
	return color.RGBA{R: r, G: g, B: b, A: 0xff}
}

// effectAnimated reports whether an effect's colour depends on the animation
// phase. It is the frame ticker's gate (see effectsanim.go): a post carrying
// only static effects — a status colour, a quiet aside — is painted once and
// never keeps the 90ms loop running. Keep this in step with effectColor: an
// effect whose case there reads phase must be listed here.
func effectAnimated(id byte) bool {
	switch id {
	case effects.Shimmer, effects.Rainbow, effects.Scroll, effects.Pulse, effects.Glow, effects.Warn:
		return true
	default:
		return false // ok, bad, whisper — one steady colour
	}
}

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
