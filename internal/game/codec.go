// Package game implements the in-message multiplayer transport and the
// Gorillas artillery game that rides on it.
//
// The transport smuggles binary state through a Mattermost post body. A post
// carries a human-visible part (a header line and an ASCII board in a code
// fence, so users on the official clients see something) followed by an
// invisible blob: the game state, one byte per Unicode variation selector.
// Variation selectors render as nothing everywhere, so the blob costs no
// visible space, and a post can be edited many times a second to stream state
// without the body ever looking any different.
package game

import (
	"strings"
	"unicode/utf8"
)

// The two variation-selector blocks together hold exactly 256 code points, so
// one selector encodes one byte with no escaping and no bit-packing:
//
//	VS1–VS16    U+FE00–U+FE0F   16 values → bytes 0x00–0x0F
//	VS17–VS256  U+E0100–U+E01EF 240 values → bytes 0x10–0xFF
//
// Both blocks are Default_Ignorable_Code_Point, which is what makes the blob
// invisible rather than merely obscure.
const (
	vsLowStart  = 0xFE00
	vsLowEnd    = 0xFE0F
	vsHighStart = 0xE0100
	vsHighEnd   = 0xE01EF
	vsLowCount  = vsLowEnd - vsLowStart + 1 // 16
)

// magic prefixes every payload. It exists because U+FE0F is also the ordinary
// emoji presentation selector, so any post containing an emoji already ends up
// with stray payload runes in it. Without a magic prefix, Decode would happily
// read a 🎮 as a one-byte payload. Four bytes is enough that a false positive
// needs a deliberate effort.
var magic = []byte{'M', 'B', 'G', '1'}

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

// isPayloadRune reports whether r carries a payload byte.
func isPayloadRune(r rune) bool {
	_, ok := decodeRune(r)
	return ok
}

// Encode renders payload as an invisible run of variation selectors, magic
// included. The result is safe to append to any post body.
func Encode(payload []byte) string {
	var b strings.Builder
	b.Grow((len(magic) + len(payload)) * utf8.UTFMax)
	for _, c := range magic {
		b.WriteRune(encodeByte(c))
	}
	for _, c := range payload {
		b.WriteRune(encodeByte(c))
	}
	return b.String()
}

// Decode extracts the payload from a post body, or reports ok=false if the body
// carries none.
//
// It scans every maximal run of payload runes rather than assuming the blob sits
// at the end, because Mattermost is free to reflow a body and because an emoji
// elsewhere in the text produces its own short run. A run is ours only if it
// opens with magic; anything else — including the lone U+FE0F trailing an emoji
// — is skipped.
func Decode(msg string) ([]byte, bool) {
	var run []byte
	flush := func() ([]byte, bool) {
		if len(run) > len(magic) && string(run[:len(magic)]) == string(magic) {
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

// Strip removes every payload rune from msg. Callers that want to show or store
// the human-readable part of a game post — the message pane, the SQLite cache,
// a search index — use this so the invisible blob never reaches them.
func Strip(msg string) string {
	if !strings.ContainsFunc(msg, isPayloadRune) {
		return msg
	}
	return strings.Map(func(r rune) rune {
		if isPayloadRune(r) {
			return -1
		}
		return r
	}, msg)
}
