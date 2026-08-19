// Package hidden smuggles arbitrary bytes through a Mattermost post body as an
// invisible run of Unicode variation selectors. A post carries its normal
// human-visible text; a payload appended to it costs no visible space, so a post
// can be edited many times a second to stream state without the body ever
// looking any different.
//
// One selector encodes one byte, with no escaping and no bit-packing:
//
//	VS1–VS16    U+FE00–U+FE0F   16 values → bytes 0x00–0x0F
//	VS17–VS256  U+E0100–U+E01EF 240 values → bytes 0x10–0xFF
//
// Both blocks are Default_Ignorable_Code_Point, which is what makes the payload
// invisible rather than merely obscure.
//
// A caller-chosen magic prefixes each payload, for two reasons. It separates
// independent channels — the Gorillas game (MBG1) and message effects (MBF1)
// never decode each other's runs — and it stops a stray U+FE0F (the ordinary
// emoji presentation selector) from reading as a one-byte payload. Give each
// channel its own short magic. Strip is the exception: it is magic-agnostic and
// removes every payload rune whatever channel wrote it.
//
// Decode scans maximal runs of payload runes, so two different-magic payloads
// concatenated with no visible text between them would merge into one run that
// only matches its leading magic. Append exists for exactly that case: it parts
// the two runs with an invisible separator (see sepRune) so each keeps its own
// identity. Anything that appends a second payload to a body must go through it.
package hidden

import (
	"strings"
	"unicode/utf8"
)

const (
	vsLowStart  = 0xFE00
	vsLowEnd    = 0xFE0F
	vsHighStart = 0xE0100
	vsHighEnd   = 0xE01EF
	vsLowCount  = vsLowEnd - vsLowStart + 1 // 16

	// sepRune parts two adjacent payload runs so each stays a run of its own.
	// U+2060 WORD JOINER is Default_Ignorable like the selectors themselves —
	// invisible in every client — but carries no payload byte, which is the
	// whole point: a rune that ends a run without adding to it. Nothing else
	// about it matters here; it is never emitted anywhere but between two
	// payload runes.
	sepRune = '\u2060'
)

// encodeByte maps a byte onto its variation selector.
func encodeByte(b byte) rune {
	if b < vsLowCount {
		return rune(vsLowStart + int(b))
	}
	return rune(vsHighStart + int(b) - vsLowCount)
}

// decodeRune is encodeByte's inverse; ok is false for any rune that is not a
// payload carrier.
func decodeRune(r rune) (byte, bool) {
	switch {
	case r >= vsLowStart && r <= vsLowEnd:
		return byte(r - vsLowStart), true
	case r >= vsHighStart && r <= vsHighEnd:
		return byte(int(r) - vsHighStart + vsLowCount), true
	}
	return 0, false
}

// IsPayloadRune reports whether r carries a payload byte.
func IsPayloadRune(r rune) bool {
	_, ok := decodeRune(r)
	return ok
}

// IsRunSeparator reports whether r is the rune Append writes between two
// payload runs. It carries no byte of its own — a debug view (`matterbox
// decode`) needs to tell it apart from both payload runes and real text, since
// it is invisible like the former but empty like neither.
func IsRunSeparator(r rune) bool { return r == sepRune }

// PayloadByte reports the byte r carries, or ok=false if r is not one of ours.
// It exists for debug views (`matterbox decode`) that walk a post body rune
// by rune: the encoding's whole purpose is that nothing on screen reveals it, so
// a debug view is the only way anyone ever sees where the blob sits.
func PayloadByte(r rune) (byte, bool) { return decodeRune(r) }

// Encode renders payload as an invisible run of variation selectors, prefixed by
// magic. The result is safe to append to any post body.
func Encode(magic string, payload []byte) string {
	var b strings.Builder
	b.Grow((len(magic) + len(payload)) * utf8.UTFMax)
	for i := 0; i < len(magic); i++ {
		b.WriteRune(encodeByte(magic[i]))
	}
	for _, c := range payload {
		b.WriteRune(encodeByte(c))
	}
	return b.String()
}

// Decode extracts the payload of the first run that opens with magic, or reports
// ok=false if msg carries none for this channel.
//
// It scans every maximal run of payload runes rather than assuming the blob sits
// at the end, because Mattermost is free to reflow a body and because an emoji
// elsewhere in the text produces its own short run. A run is ours only if it
// opens with magic; anything else — including the lone U+FE0F trailing an emoji,
// or another channel's run — is skipped.
func Decode(magic, msg string) ([]byte, bool) {
	var run []byte
	flush := func() ([]byte, bool) {
		if len(run) > len(magic) && string(run[:len(magic)]) == magic {
			return run[len(magic):], true
		}
		return nil, false
	}
	for _, r := range msg {
		if b, ok := decodeRune(r); ok {
			run = append(run, b)
			continue
		}
		if p, ok := flush(); ok {
			return p, true
		}
		run = run[:0]
	}
	return flush()
}

// Append adds a payload to a body that may already carry one. Concatenating two
// encoded runs directly would merge them into a single run that Decode can only
// read as its leading channel, so when msg already ends in a payload rune the two
// are parted by an invisible separator. A body ending in ordinary text needs no
// separator and gets none, which is why every pre-existing post still decodes
// byte-for-byte the way it always did.
//
// This is the only supported way to put a second channel on one post. Callers
// building a body from scratch may still concatenate Encode's output onto their
// own visible text directly.
func Append(msg, magic string, payload []byte) string {
	run := Encode(magic, payload)
	if run == "" {
		return msg
	}
	if endsInPayload(msg) {
		return msg + string(sepRune) + run
	}
	return msg + run
}

// endsInPayload reports whether msg's last rune carries a payload byte — i.e.
// whether appending another run would merge with it.
func endsInPayload(msg string) bool {
	r, size := utf8.DecodeLastRuneInString(msg)
	return size > 0 && IsPayloadRune(r)
}

// Remove takes one channel's payload back off a body and leaves every other
// channel's alone — the inverse of Append for a single magic. The separator that
// was parting the removed run from its neighbour goes with it, so what is left
// is exactly the body that would have been written without that channel.
//
// Callers that want the human-readable text want Strip instead. Remove is for
// rewriting a post that is about to be sent again: an edit re-derives its own
// payload, so the old one must not ride along behind the new one.
func Remove(msg, magic string) string {
	if !strings.Contains(msg, Encode(magic, nil)) {
		return msg
	}
	rs := []rune(msg)
	// Find the run that opens with magic, as Decode reads it.
	start := -1
	var run []byte
	for i := 0; i <= len(rs); i++ {
		if i < len(rs) {
			if b, ok := decodeRune(rs[i]); ok {
				if start < 0 {
					start, run = i, run[:0]
				}
				run = append(run, b)
				continue
			}
		}
		if start >= 0 {
			if len(run) > len(magic) && string(run[:len(magic)]) == magic {
				from, to := cutRange(rs, start, i)
				return string(rs[:from]) + string(rs[to:])
			}
			start = -1
		}
	}
	return msg
}

// cutRange widens the span [start,end) of a run about to be removed to swallow
// the separator parting it from a neighbouring run — whichever side that
// separator is on. Leaving it behind would strand a lone WORD JOINER in the
// visible text, since Strip only removes one that still sits between two runs.
func cutRange(rs []rune, start, end int) (int, int) {
	switch {
	case start > 1 && rs[start-1] == sepRune && IsPayloadRune(rs[start-2]):
		return start - 1, end
	case end+1 < len(rs) && rs[end] == sepRune && IsPayloadRune(rs[end+1]):
		return start, end + 1
	}
	return start, end
}

// Strip removes every payload rune from msg, whatever channel wrote it. Callers
// that want to show or store the human-readable part of a post — the message
// pane, the SQLite cache, a search index — use this so no invisible blob ever
// reaches them.
//
// A run separator (see Append) goes too, but only where it is doing that job:
// sitting between two payload runes. A WORD JOINER a human actually typed, or
// one that arrived in a paste from somewhere else, is left alone — Strip removes
// matterbox's own scaffolding, not the author's text.
func Strip(msg string) string {
	if !strings.ContainsFunc(msg, IsPayloadRune) {
		return msg
	}
	rs := []rune(msg)
	out := make([]rune, 0, len(rs))
	for i, r := range rs {
		if IsPayloadRune(r) {
			continue
		}
		if r == sepRune && i > 0 && i+1 < len(rs) && IsPayloadRune(rs[i-1]) && IsPayloadRune(rs[i+1]) {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
