package hidden

import (
	"bytes"
	"testing"
	"testing/quick"
	"unicode/utf8"
)

const (
	magicA = "MBG1" // stands in for the game channel
	magicB = "MBF1" // stands in for the effects channel
)

func allBytes() []byte {
	p := make([]byte, 256)
	for i := range p {
		p[i] = byte(i)
	}
	return p
}

// Every selector must map to a distinct byte and back. A collision here would
// silently corrupt a payload rather than fail loudly.
func TestByteRuneMappingIsBijective(t *testing.T) {
	seen := make(map[rune]byte, 256)
	for i := range 256 {
		b := byte(i)
		r := encodeByte(b)
		if prev, dup := seen[r]; dup {
			t.Fatalf("bytes %#x and %#x both encode to %U", prev, b, r)
		}
		seen[r] = b
		back, ok := decodeRune(r)
		if !ok || back != b {
			t.Fatalf("decodeRune(%U) = %#x, %v; want %#x, true", r, back, ok, b)
		}
	}
	if len(seen) != 256 {
		t.Fatalf("got %d distinct runes, want 256", len(seen))
	}
}

// The blob must be invisible: every rune it emits has to come from one of the
// two variation-selector blocks, never a printable character.
func TestEncodeEmitsOnlySelectors(t *testing.T) {
	for _, r := range Encode(magicA, allBytes()) {
		if !IsPayloadRune(r) {
			t.Fatalf("Encode emitted a non-selector rune %U", r)
		}
	}
}

func TestEncodeIsValidUTF8(t *testing.T) {
	if s := Encode(magicA, allBytes()); !utf8.ValidString(s) {
		t.Fatal("Encode produced invalid UTF-8; it would not survive a JSON round-trip")
	}
}

func TestRoundTripAllByteValues(t *testing.T) {
	want := allBytes()
	got, ok := Decode(magicA, Encode(magicA, want))
	if !ok {
		t.Fatal("Decode found no payload in its own Encode output")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip corrupted the payload:\n got %v\nwant %v", got, want)
	}
}

// A payload written for one channel must not decode under another channel's
// magic — that isolation is the whole reason for a caller-chosen magic.
func TestDecodeIgnoresOtherChannelsMagic(t *testing.T) {
	body := "hi " + Encode(magicA, []byte{1, 2, 3}) + " there"
	if p, ok := Decode(magicB, body); ok {
		t.Fatalf("Decode(%q, …) read another channel's run as %v", magicB, p)
	}
	if p, ok := Decode(magicA, body); !ok || !bytes.Equal(p, []byte{1, 2, 3}) {
		t.Fatalf("Decode(%q, …) = %v, %v; want [1 2 3], true", magicA, p, ok)
	}
}

// The magic prefix's other job: an ordinary post containing an emoji carries a
// bare U+FE0F, and that must not read as a payload.
func TestDecodeIgnoresEmojiVariationSelector(t *testing.T) {
	for _, msg := range []string{
		"nice shot ️",
		"\U0001F3AE️ game night?",
		"plain text, no selectors at all",
		"",
	} {
		if p, ok := Decode(magicA, msg); ok {
			t.Fatalf("Decode(%q) claimed a payload %v; want none", msg, p)
		}
	}
}

// An empty payload is not a payload — otherwise a bare magic run would decode as
// a valid zero-length state.
func TestDecodeRejectsMagicOnly(t *testing.T) {
	if p, ok := Decode(magicA, Encode(magicA, nil)); ok {
		t.Fatalf("Decode accepted a magic-only run as payload %v", p)
	}
}

// Mattermost may reflow a body around the blob, and other short runs (an emoji,
// a decoy without magic) may sit nearby. The real payload must still come back.
func TestDecodeFindsPayloadAfterAnEarlierRun(t *testing.T) {
	want := []byte{1, 2, 3}
	decoy := string(encodeByte(0x07)) + string(encodeByte(0x08)) // no magic
	got, ok := Decode(magicA, "a"+decoy+"b"+Encode(magicA, want)+"c")
	if !ok || !bytes.Equal(got, want) {
		t.Fatalf("got %v, %v; want %v, true", got, ok, want)
	}
}

// Strip is magic-agnostic: it must remove both channels' blobs and nothing else.
func TestStripRemovesEveryChannel(t *testing.T) {
	visible := "hello world\n"
	body := visible + Encode(magicA, []byte{0, 1, 2}) + Encode(magicB, []byte{9, 8})
	if got := Strip(body); got != visible {
		t.Fatalf("Strip left residue:\n got %q\nwant %q", got, visible)
	}
	// A body with no blob is returned unchanged, except a bare emoji U+FE0F,
	// which Strip cannot tell from a payload byte and does remove.
	if got := Strip("plain ❤️"); got != "plain ❤" {
		t.Fatalf("Strip(%q) = %q", "plain ❤️", got)
	}
}

func TestRoundTripQuick(t *testing.T) {
	f := func(payload []byte) bool {
		if len(payload) == 0 {
			return true // empty is rejected by design; covered above
		}
		got, ok := Decode(magicB, "prefix "+Encode(magicB, payload)+" suffix")
		return ok && bytes.Equal(got, payload)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}
