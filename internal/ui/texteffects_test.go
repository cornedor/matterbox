package ui

import (
	"testing"

	"matterbox/internal/effects"
	"matterbox/internal/hidden"
)

// A message with no effect markup must go on the wire byte-for-byte as typed —
// no payload, and crucially no disturbance to ordinary backslashes.
func TestCompileEffectsLeavesPlainTextAlone(t *testing.T) {
	for _, in := range []string{
		"just a normal message!",
		"windows path C:\\temp\\logs",
		"regex \\d+ and an unknown \\wobble{x}",
		"",
	} {
		if got := compileEffects(in); got != in {
			t.Errorf("compileEffects(%q) = %q; want it unchanged", in, got)
		}
	}
}

// With an effect present, the wire text carries the clean visible text (what
// other clients see, via Strip) plus a decodable MBF1 payload of spans.
func TestCompileEffectsEmbedsPayload(t *testing.T) {
	wire := compileEffects("ship \\shimmer{it} now")

	if got := hidden.Strip(wire); got != "ship it now" {
		t.Fatalf("visible text = %q; want %q", got, "ship it now")
	}

	payload, ok := hidden.Decode(effects.MagicEffects, wire)
	if !ok {
		t.Fatal("no MBF1 payload found in the wire text")
	}
	spans, ok := effects.UnmarshalPayload(payload)
	if !ok {
		t.Fatal("MBF1 payload did not decode")
	}
	want := []effects.Span{{ID: effects.Shimmer, Start: 5, Len: 2}}
	if len(spans) != 1 || spans[0] != want[0] {
		t.Fatalf("spans = %+v; want %+v", spans, want)
	}

	// The game channel must not see the effects payload as its own.
	if _, ok := hidden.Decode("MBG1", wire); ok {
		t.Fatal("effects payload decoded under the game magic")
	}
}

// The edit round-trip: what a post was sent as is what re-opening it puts back
// in the composer, and saving it again without touching a thing must reproduce
// the very same body — otherwise every edit would erode the effects.
func TestEffectsEditRoundTrip(t *testing.T) {
	for _, src := range []string{
		"ship \\shimmer{it} now",
		"\\rainbow{gg} well played, \\pulse{everyone}",
		"\\shimmer{\\glow{nested}} spans",
		"a path C:\\temp and a \\shimmer{live} word", // a real backslash alongside an effect
	} {
		wire := compileEffects(src)
		markup := decompileEffects(wire)
		if markup != src {
			t.Errorf("decompileEffects round-trip:\n got  %q\n want %q", markup, src)
		}
		if again := compileEffects(markup); again != wire {
			t.Errorf("re-saving an untouched edit changed the body:\n got  %q\n want %q", again, wire)
		}
	}
}

// A body that carries no effects payload is handed to the composer untouched —
// including one carrying another channel's payload, like a Gorillas game post,
// whose bytes are not ours to rewrite.
func TestDecompileEffectsLeavesOtherBodiesAlone(t *testing.T) {
	plain := "just a message"
	if got := decompileEffects(plain); got != plain {
		t.Errorf("decompileEffects(%q) = %q; want it unchanged", plain, got)
	}

	game := "Gorillas" + hidden.Encode("MBG1", []byte{1, 2, 3})
	if got := decompileEffects(game); got != game {
		t.Error("decompileEffects rewrote a game post's body")
	}
}

// A span that no longer fits the text (someone edited the post from another
// client) is dropped rather than applied to the wrong words.
func TestDecompileEffectsDropsStaleSpans(t *testing.T) {
	// A payload claiming a span far past the end of a short body.
	stale := "hi" + hidden.Encode(effects.MagicEffects,
		effects.MarshalPayload([]effects.Span{{ID: effects.Shimmer, Start: 5, Len: 20}}))

	if got := decompileEffects(stale); got != "hi" {
		t.Errorf("decompileEffects(stale) = %q; want the plain text %q", got, "hi")
	}
}

// The per-effect slash commands apply one effect to the entire message. The text
// is taken literally, so a path or a brace in it is shimmered rather than parsed.
func TestWholeMessageEffect(t *testing.T) {
	wire := wholeMessageEffect(effects.Shimmer, "ship it")
	if got := hidden.Strip(wire); got != "ship it" {
		t.Fatalf("visible text = %q; want %q", got, "ship it")
	}
	payload, ok := hidden.Decode(effects.MagicEffects, wire)
	if !ok {
		t.Fatal("no payload")
	}
	spans, _ := effects.UnmarshalPayload(payload)
	want := effects.Span{ID: effects.Shimmer, Start: 0, Len: 7}
	if len(spans) != 1 || spans[0] != want {
		t.Fatalf("spans = %+v; want one span over the whole message (%+v)", spans, want)
	}

	// Literal, not parsed: no escaping hazard.
	lit := wholeMessageEffect(effects.Glow, "C:\\temp {braces}")
	if got := hidden.Strip(lit); got != "C:\\temp {braces}" {
		t.Errorf("the text was reinterpreted: %q", got)
	}
	// An unknown effect id can't produce a payload.
	if got := wholeMessageEffect(0, "x"); got != "x" {
		t.Errorf("an unknown effect produced a payload: %q", got)
	}
}
