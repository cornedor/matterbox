// Package effects defines matterbox's in-message text-effect language: the
// composer markup a user types (\shimmer{ship it}), the spans it compiles to,
// and the wire payload those spans travel in.
//
// The grammar is one construct — a backslash, a *known* effect name, and a
// brace-delimited body:
//
//	\shimmer{today}          one word shimmers
//	\glow{ship \pulse{it}}   effects nest
//	\\shimmer{x}             a literal backslash; renders as \shimmer{x}
//
// The safety rule is that only a recognised name activates: an unknown
// \wobble{...}, a bare backslash, and an unbalanced brace are all left as
// literal text, so ordinary prose never triggers an effect. On send, Parse
// strips the markup to the text a plain Mattermost client sees and returns the
// effect spans separately; those spans ride the post as an invisible MBF1
// payload (see MarshalPayload and internal/hidden), so other clients see clean
// text while matterbox animates the marked runes.
//
// Effect IDs are stable wire values: never renumber an existing effect, only
// append. Span offsets are rune indices into the visible text, matched at the
// version present when the post was written — a later edit from another client
// can shift the text out from under them, in which case the effect simply stops
// applying (it never corrupts the text).
package effects

import (
	"encoding/binary"
	"sort"
	"strings"
)

// The recognised effects. IDs are stable wire values (see the package doc).
const (
	Shimmer byte = 1
	Rainbow byte = 2
	Pulse   byte = 3
	Glow    byte = 4
)

// MagicEffects tags the hidden payload that carries effect spans, distinct from
// the game channel (game.Magic) so the two never decode each other's runs.
const MagicEffects = "MBF1"

// payloadVersion prefixes MarshalPayload output so a future format change is
// detectable rather than silently misread.
const payloadVersion = 1

var idByName = map[string]byte{
	"shimmer": Shimmer,
	"rainbow": Rainbow,
	"pulse":   Pulse,
	"glow":    Glow,
}

var nameByID = map[byte]string{
	Shimmer: "shimmer",
	Rainbow: "rainbow",
	Pulse:   "pulse",
	Glow:    "glow",
}

// Name returns an effect's directive name, or "" if id is not recognised.
func Name(id byte) string { return nameByID[id] }

// Known reports whether name is a recognised effect. This is the safety gate:
// Parse leaves any \name{...} whose name is not Known as literal text.
func Known(name string) bool {
	_, ok := idByName[name]
	return ok
}

// Span marks a run of the visible text that carries an effect. Start and Len are
// rune offsets into the visible string (the half-open range [Start, Start+Len)).
type Span struct {
	ID    byte
	Start int
	Len   int
}

// Parse splits composer source into the text a plain client sees and the effect
// spans matterbox draws over it. See the package doc for the grammar. Directives
// nest; each contributes its own span, and an empty body (\shimmer{}) records no
// span. Offsets in the returned spans are rune indices into visible.
func Parse(src string) (visible string, spans []Span) {
	rs := []rune(src)
	var b strings.Builder
	pos := 0 // runes written to b so far

	// frame tracks an open '{'. A directive frame (start >= 0) records a span
	// when its matching '}' is reached; a bare-brace frame (start < 0) just keeps
	// the braces balanced so a later '}' doesn't close a directive early.
	type frame struct {
		id    byte
		start int
	}
	var stack []frame

	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '\\' && i+1 < len(rs) && rs[i+1] == '\\':
			b.WriteRune('\\')
			pos++
			i += 2
		case r == '\\':
			if name, brace, ok := directiveAt(rs, i); ok {
				if _, closed := matchBrace(rs, brace); closed {
					stack = append(stack, frame{id: idByName[name], start: pos})
					i = brace + 1 // consume "\name{"; the body follows
					continue
				}
			}
			b.WriteRune('\\') // a bare or unbalanced backslash is literal
			pos++
			i++
		case r == '{':
			stack = append(stack, frame{start: -1}) // bare brace
			b.WriteRune('{')
			pos++
			i++
		case r == '}':
			if n := len(stack); n > 0 {
				f := stack[n-1]
				stack = stack[:n-1]
				if f.start >= 0 {
					if pos > f.start { // skip empty-body directives
						spans = append(spans, Span{ID: f.id, Start: f.start, Len: pos - f.start})
					}
				} else {
					b.WriteRune('}')
					pos++
				}
			} else {
				b.WriteRune('}') // unmatched close is literal
				pos++
			}
			i++
		default:
			b.WriteRune(r)
			pos++
			i++
		}
	}
	// Frames still open here are unbalanced; their bodies are already in the
	// visible text, so there is nothing to record — the effect just doesn't apply.
	return b.String(), spans
}

// directiveAt reports the directive opening at rs[i] (which must be '\'): its
// lower-cased name and the index of the '{' that follows. ok is false unless the
// backslash is followed by one or more ASCII letters, a '{', and a *known* name.
func directiveAt(rs []rune, i int) (name string, brace int, ok bool) {
	j := i + 1
	for j < len(rs) && isNameRune(rs[j]) {
		j++
	}
	if j == i+1 || j >= len(rs) || rs[j] != '{' {
		return "", 0, false
	}
	name = strings.ToLower(string(rs[i+1 : j]))
	if !Known(name) {
		return "", 0, false
	}
	return name, j, true
}

func isNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// matchBrace returns the index of the '}' that closes the '{' at open, counting
// nested braces, or ok=false if it is never closed. It honours the same "\\"
// escape as Parse so the two agree on which braces are literal.
func matchBrace(rs []rune, open int) (close int, ok bool) {
	depth := 0
	for i := open; i < len(rs); i++ {
		switch {
		case rs[i] == '\\' && i+1 < len(rs) && rs[i+1] == '\\':
			i++ // skip the escaped backslash pair
		case rs[i] == '{':
			depth++
		case rs[i] == '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// Ordered returns spans in the order their directives *open*: by start, then
// outermost first. This is not the order Parse emits them — a span is recorded
// when it closes, so the inner of two nested directives is appended first — and
// for two spans covering exactly the same runes (`\shimmer{\glow{x}}`) length
// alone cannot tell them apart. There the original order decides: the one
// recorded later closed later, so it is the outer one.
//
// Anything that has to re-nest spans needs this — the composer rebuilding the
// markup (Reconstruct) and the renderer bracketing the text — or the two
// directives silently swap.
func Ordered(spans []Span) []Span {
	out := make([]Span, 0, len(spans))
	for i := len(spans) - 1; i >= 0; i-- {
		out = append(out, spans[i]) // reversed, so the outer of an identical pair leads
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].Len > out[j].Len
	})
	return out
}

// Reconstruct is Parse's inverse: it rebuilds composer source from visible text
// and its spans, so an own-post edit can show the markup again rather than the
// bare words. Parse(Reconstruct(v, spans)) returns (v, spans).
//
// A literal backslash is escaped only where leaving it alone would change that:
// where it opens a known directive, or where it would pair up with the next
// backslash into an escape. Ordinary ones are left exactly as the author typed
// them — a Windows path in an edited message must not grow a slash every time it
// is re-opened.
func Reconstruct(visible string, spans []Span) string {
	rs := []rune(visible)
	ordered := Ordered(spans)
	var b strings.Builder
	for p := 0; p <= len(rs); p++ {
		// Close every span ending here. Only the count matters — a close is just
		// '}', so their relative order can't be observed.
		for _, s := range spans {
			if s.Start+s.Len == p {
				b.WriteByte('}')
			}
		}
		for _, s := range ordered {
			if s.Start == p {
				b.WriteByte('\\')
				b.WriteString(nameByID[s.ID])
				b.WriteByte('{')
			}
		}
		if p < len(rs) {
			if rs[p] == '\\' && backslashNeedsEscape(rs, p) {
				b.WriteString(`\\`)
			} else {
				b.WriteRune(rs[p])
			}
		}
	}
	return b.String()
}

// backslashNeedsEscape reports whether the literal backslash at rs[p] would be
// re-read as something other than itself: as the escape of a following backslash,
// or as the opening of a known directive.
func backslashNeedsEscape(rs []rune, p int) bool {
	if p+1 < len(rs) && rs[p+1] == '\\' {
		return true
	}
	_, _, isDirective := directiveAt(rs, p)
	return isDirective
}

// MarshalPayload encodes spans as the MBF1 payload body:
//
//	version(1) | count(1) | count×[ id(1), start(uvarint), len(uvarint) ]
//
// It returns nil for no spans (nothing to carry). At most 255 spans are encoded;
// more than that in one message is absurd and the tail is dropped.
func MarshalPayload(spans []Span) []byte {
	if len(spans) == 0 {
		return nil
	}
	if len(spans) > 255 {
		spans = spans[:255]
	}
	buf := []byte{payloadVersion, byte(len(spans))}
	var tmp [binary.MaxVarintLen64]byte
	for _, s := range spans {
		buf = append(buf, s.ID)
		buf = append(buf, tmp[:binary.PutUvarint(tmp[:], uint64(s.Start))]...)
		buf = append(buf, tmp[:binary.PutUvarint(tmp[:], uint64(s.Len))]...)
	}
	return buf
}

// UnmarshalPayload decodes MarshalPayload's output. It reports ok=false on a
// version mismatch or truncated data. Spans naming an unrecognised effect id are
// skipped (forward compatibility: a newer matterbox may define more effects than
// this build knows), so the returned slice may be shorter than the encoded count.
func UnmarshalPayload(b []byte) (spans []Span, ok bool) {
	if len(b) < 2 || b[0] != payloadVersion {
		return nil, false
	}
	n := int(b[1])
	rest := b[2:]
	spans = make([]Span, 0, n)
	for range n {
		if len(rest) < 1 {
			return nil, false
		}
		id := rest[0]
		rest = rest[1:]
		start, m1 := binary.Uvarint(rest)
		if m1 <= 0 {
			return nil, false
		}
		rest = rest[m1:]
		length, m2 := binary.Uvarint(rest)
		if m2 <= 0 {
			return nil, false
		}
		rest = rest[m2:]
		if _, known := nameByID[id]; !known {
			continue // unknown effect from a newer build; skip, don't corrupt
		}
		spans = append(spans, Span{ID: id, Start: int(start), Len: int(length)})
	}
	return spans, true
}
