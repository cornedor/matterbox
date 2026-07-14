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
