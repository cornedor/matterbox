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
// One caveat: Decode scans maximal runs of payload runes, so it will not
// separate two different-magic payloads that are directly concatenated with no
// visible text between them (the merged run only matches its leading magic). In
// practice a post belongs to one channel, so this does not arise.
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

// Strip removes every payload rune from msg, whatever channel wrote it. Callers
// that want to show or store the human-readable part of a post — the message
// pane, the SQLite cache, a search index — use this so no invisible blob ever
// reaches them.
func Strip(msg string) string {
	if !strings.ContainsFunc(msg, IsPayloadRune) {
		return msg
	}
	return strings.Map(func(r rune) rune {
		if IsPayloadRune(r) {
			return -1
		}
		return r
	}, msg)
}
