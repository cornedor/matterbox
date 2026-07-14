package effects

import (
	"sort"
	"testing"
)

func sortedSpans(s []Span) []Span {
	out := append([]Span(nil), s...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		if out[i].Len != out[j].Len {
			return out[i].Len < out[j].Len
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func equalSpans(a, b []Span) bool {
	a, b = sortedSpans(a), sortedSpans(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		visible string
		spans   []Span
	}{
		{
			name:    "single directive, rune offsets",
			src:     "release is \\shimmer{today} 🚀",
			visible: "release is today 🚀",
			spans:   []Span{{Shimmer, 11, 5}},
		},
		{
			name:    "offset counts runes not bytes",
			src:     "héllo \\glow{x}",
			visible: "héllo x",
			spans:   []Span{{Glow, 6, 1}},
		},
		{
			name:    "multibyte body",
			src:     "\\shimmer{🚀ok}",
			visible: "🚀ok",
			spans:   []Span{{Shimmer, 0, 3}},
		},
		{
			name:    "unknown name stays literal",
			src:     "say \\wobble{hi} ok",
			visible: "say \\wobble{hi} ok",
			spans:   nil,
		},
		{
			name:    "escaped backslash is literal, no effect",
			src:     "\\\\shimmer{x}",
			visible: "\\shimmer{x}",
			spans:   nil,
		},
		{
			name:    "nesting records both spans",
			src:     "\\glow{ship \\pulse{it}}",
			visible: "ship it",
			spans:   []Span{{Glow, 0, 7}, {Pulse, 5, 2}},
		},
		{
			name:    "bare braces inside a body are balanced and kept",
			src:     "\\glow{a {b} c}",
			visible: "a {b} c",
			spans:   []Span{{Glow, 0, 7}},
		},
		{
			name:    "unbalanced directive is literal",
			src:     "\\shimmer{oops",
			visible: "\\shimmer{oops",
			spans:   nil,
		},
		{
			name:    "empty body records no span",
			src:     "\\shimmer{}x",
			visible: "x",
			spans:   nil,
		},
		{
			name:    "name match is case-insensitive",
			src:     "\\SHIMMER{hi}",
			visible: "hi",
			spans:   []Span{{Shimmer, 0, 2}},
		},
		{
			name:    "plain text is untouched",
			src:     "just a normal message!",
			visible: "just a normal message!",
			spans:   nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			visible, spans := Parse(c.src)
			if visible != c.visible {
				t.Errorf("visible = %q; want %q", visible, c.visible)
			}
			if !equalSpans(spans, c.spans) {
				t.Errorf("spans = %+v; want %+v", spans, c.spans)
			}
		})
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	spans := []Span{{Shimmer, 11, 5}, {Pulse, 5, 2}, {Glow, 0, 300}}
	got, ok := UnmarshalPayload(MarshalPayload(spans))
	if !ok {
		t.Fatal("UnmarshalPayload reported not-ok on its own output")
	}
	if !equalSpans(got, spans) {
		t.Fatalf("round-trip changed spans:\n got %+v\nwant %+v", got, spans)
	}
}

func TestMarshalEmptyIsNil(t *testing.T) {
	if b := MarshalPayload(nil); b != nil {
		t.Fatalf("MarshalPayload(nil) = %v; want nil", b)
	}
	if _, ok := UnmarshalPayload(nil); ok {
		t.Fatal("UnmarshalPayload(nil) reported ok; want not-ok")
	}
}

// A newer matterbox may define effects this build doesn't know; their spans must
// be skipped, not misdrawn, and the rest of the payload must still decode.
func TestUnmarshalSkipsUnknownEffectID(t *testing.T) {
	payload := MarshalPayload([]Span{{ID: 99, Start: 0, Len: 1}, {Shimmer, 2, 3}})
	got, ok := UnmarshalPayload(payload)
	if !ok {
		t.Fatal("UnmarshalPayload reported not-ok")
	}
	if !equalSpans(got, []Span{{Shimmer, 2, 3}}) {
		t.Fatalf("got %+v; want just the known span", got)
	}
}

func TestUnmarshalRejectsBadVersion(t *testing.T) {
	if _, ok := UnmarshalPayload([]byte{0x7F, 0x01, Shimmer, 0, 1}); ok {
		t.Fatal("UnmarshalPayload accepted an unknown version")
	}
}

func TestReconstructRoundTrips(t *testing.T) {
	for _, src := range []string{
		"release is \\shimmer{today}",
		"\\glow{ship \\pulse{it}}",
		"plain, no effects",
		"a literal backslash \\\\ stays",
	} {
		visible, spans := Parse(src)
		re := Reconstruct(visible, spans)
		v2, s2 := Parse(re)
		if v2 != visible || !equalSpans(s2, spans) {
			t.Errorf("Reconstruct(%q) = %q re-parsed to (%q, %+v); want (%q, %+v)",
				src, re, v2, s2, visible, spans)
		}
	}
}
