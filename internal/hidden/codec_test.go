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

// The reason Append exists: two channels on one post. Concatenating the runs
// directly merges them, and the second channel's payload disappears behind the
// first one's magic. Append must keep both readable, in either order.
func TestAppendKeepsBothChannelsReadable(t *testing.T) {
	a, b := []byte{1, 2, 3}, []byte{9, 9}
	body := Append(Append("hello", magicA, a), magicB, b)

	got, ok := Decode(magicA, body)
	if !ok || !bytes.Equal(got, a) {
		t.Fatalf("Decode(%s) = %v, %v; want %v, true", magicA, got, ok, a)
	}
	got, ok = Decode(magicB, body)
	if !ok || !bytes.Equal(got, b) {
		t.Fatalf("Decode(%s) = %v, %v; want %v, true", magicB, got, ok, b)
	}
	if vis := Strip(body); vis != "hello" {
		t.Fatalf("Strip left residue: %q", vis)
	}
}

// Direct concatenation is the failure Append is there to avoid. If this ever
// starts passing, the separator has become unnecessary — and that is a
// deliberate change to make, not one to discover.
func TestConcatenatedRunsHideTheSecondChannel(t *testing.T) {
	body := "hello" + Encode(magicA, []byte{1}) + Encode(magicB, []byte{2})
	if _, ok := Decode(magicB, body); ok {
		t.Fatal("a directly concatenated second channel decoded; Append's separator is no longer needed")
	}
}

// A body that carries nothing yet must come back byte-identical to plain
// concatenation, so every post written before Append existed still decodes the
// way it always did.
func TestAppendAddsNoSeparatorToPlainText(t *testing.T) {
	if got, want := Append("hello", magicA, []byte{1}), "hello"+Encode(magicA, []byte{1}); got != want {
		t.Fatalf("Append added scaffolding to a plain body:\n got %q\nwant %q", got, want)
	}
}

func TestAppendIgnoresEmptyPayload(t *testing.T) {
	if got := Append("hello", magicA, nil); got != "hello"+Encode(magicA, nil) {
		t.Fatalf("Append(nil payload) = %q", got)
	}
}

// Strip removes the separator where it is scaffolding — between two runs — and
// leaves an author's own U+2060 alone.
func TestStripLeavesAuthorsWordJoiner(t *testing.T) {
	if got := Strip("a⁠b"); got != "a⁠b" {
		t.Fatalf("Strip ate a typed WORD JOINER: %q", got)
	}
	body := Append(Append("a⁠b", magicA, []byte{1}), magicB, []byte{2})
	if got := Strip(body); got != "a⁠b" {
		t.Fatalf("Strip(%q) = %q; want the visible text with its own joiner intact", body, got)
	}
}

// Remove takes one channel back off and leaves the others exactly as they were,
// including the separators that keep them apart.
func TestRemoveTakesOneChannelOff(t *testing.T) {
	a, b := []byte{1, 2, 3}, []byte{9, 9}

	// Removed run last.
	body := Append(Append("hello", magicA, a), magicB, b)
	got := Remove(body, magicB)
	if want := Append("hello", magicA, a); got != want {
		t.Fatalf("Remove(last):\n got %q\nwant %q", got, want)
	}

	// Removed run first — the separator on its other side must go with it, or a
	// lone joiner is stranded in the visible text.
	got = Remove(body, magicA)
	if want := Append("hello", magicB, b); got != want {
		t.Fatalf("Remove(first):\n got %q\nwant %q", got, want)
	}
	if p, ok := Decode(magicB, got); !ok || !bytes.Equal(p, b) {
		t.Fatalf("the surviving channel stopped decoding: %v, %v", p, ok)
	}
	if vis := Strip(got); vis != "hello" {
		t.Fatalf("visible text after Remove = %q", vis)
	}
}

func TestRemoveIsANoOpForAChannelThatIsntThere(t *testing.T) {
	body := Append("hello", magicA, []byte{1})
	if got := Remove(body, magicB); got != body {
		t.Fatalf("Remove changed a body it had no business touching: %q", got)
	}
	if got := Remove("plain text", magicA); got != "plain text" {
		t.Fatalf("Remove(plain) = %q", got)
	}
}
