package game

import (
	"bytes"
	"strings"
	"testing"
	"testing/quick"
	"unicode/utf8"
)

// allBytes is the payload the live round-trip probe uses too: every byte value,
// so a server that mangles any single one of the 256 selectors is caught.
func allBytes() []byte {
	p := make([]byte, 256)
	for i := range p {
		p[i] = byte(i)
	}
	return p
}

func TestEncodeDecodeAllByteValues(t *testing.T) {
	want := allBytes()
	got, ok := Decode(Encode(want))
	if !ok {
		t.Fatal("Decode found no payload in its own Encode output")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip corrupted the payload:\n got %v\nwant %v", got, want)
	}
}

// Every selector must map to a distinct byte and back. A collision here would
// silently corrupt game state rather than fail loudly.
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
	for _, r := range Encode(allBytes()) {
		if !isPayloadRune(r) {
			t.Fatalf("Encode emitted a non-selector rune %U", r)
		}
	}
}

func TestEncodeIsValidUTF8(t *testing.T) {
	if s := Encode(allBytes()); !utf8.ValidString(s) {
		t.Fatal("Encode produced invalid UTF-8; it would not survive a JSON round-trip")
	}
}

// The whole point of the magic prefix: an ordinary post containing an emoji
// carries a bare U+FE0F, and that must not read as a payload.
func TestDecodeIgnoresEmojiVariationSelector(t *testing.T) {
	for _, msg := range []string{
		"nice shot ️",
		"\U0001F3AE️ game night?",
		"plain text, no selectors at all",
		"",
	} {
		if p, ok := Decode(msg); ok {
			t.Fatalf("Decode(%q) claimed a payload %v; want none", msg, p)
		}
	}
}

// A real post body: visible header, fenced ASCII board, blob appended. The blob
// must survive having text on both sides of it, and the emoji in the header must
// not derail the scan.
func TestDecodeInsideARealPostBody(t *testing.T) {
	want := []byte{0x00, 0xFF, 0x10, 0x0F, 0x42}
	body := "\U0001F4A3 Gorillas — @corne vs @bob\n" +
		"```\n  _|_|_   O   _|_|_\n```\n" +
		Encode(want) +
		"\ntrailing text\n"
	got, ok := Decode(body)
	if !ok {
		t.Fatal("Decode found no payload in a realistic post body")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// An empty payload is not a payload — otherwise a bare magic run, or any future
// caller that encodes nothing, would decode as a valid zero-length state.
func TestDecodeRejectsMagicOnly(t *testing.T) {
	if p, ok := Decode(Encode(nil)); ok {
		t.Fatalf("Decode accepted a magic-only run as payload %v", p)
	}
}

// Mattermost may reflow a body around the blob. Wherever the run ends up, and
// whatever else sits nearby, the payload must still come back.
func TestDecodeFindsPayloadAfterAnEarlierRun(t *testing.T) {
	want := []byte{1, 2, 3}
	// A decoy run that is not ours (no magic) precedes the real one.
	decoy := string(encodeByte(0x07)) + string(encodeByte(0x08))
	got, ok := Decode("a" + decoy + "b" + Encode(want) + "c")
	if !ok || !bytes.Equal(got, want) {
		t.Fatalf("got %v, %v; want %v, true", got, ok, want)
	}
}

func TestStripRemovesTheBlobAndNothingElse(t *testing.T) {
	visible := "\U0001F4A3 Gorillas\n```\nboard\n```\n"
	if got := Strip(visible + Encode(allBytes())); got != visible {
		t.Fatalf("Strip left residue:\n got %q\nwant %q", got, visible)
	}
	// Strip must be a no-op on a body that carries no blob, including one whose
	// emoji ends in a bare U+FE0F... which Strip does remove, since it cannot
	// tell that selector apart from a payload byte. Pin the behaviour so the
	// message pane never surprises us.
	if got := Strip("plain ❤️"); got != "plain ❤" {
		t.Fatalf("Strip(%q) = %q", "plain ❤️", got)
	}
}

func TestRoundTripQuick(t *testing.T) {
	f := func(payload []byte) bool {
		if len(payload) == 0 {
			return true // empty is rejected by design; covered above
		}
		got, ok := Decode("prefix " + Encode(payload) + " suffix")
		return ok && bytes.Equal(got, payload)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}

// The blob is ~1 rune per byte and 3–4 bytes of UTF-8 per rune. A Gorillas world
// is ~130 bytes, so a frame must stay far under Mattermost's 16383-rune post
// limit even in the worst case.
func TestBlobStaysWellUnderThePostLimit(t *testing.T) {
	const postRuneLimit = 16383
	blob := Encode(make([]byte, 512)) // 4× the expected worst-case world
	if n := utf8.RuneCountInString(blob); n > postRuneLimit/4 {
		t.Fatalf("blob for a 512-byte world is %d runes; too close to the %d limit", n, postRuneLimit)
	}
	if strings.Count(blob, "\n") != 0 {
		t.Fatal("blob contains a newline; it would break the fenced board above it")
	}
}
