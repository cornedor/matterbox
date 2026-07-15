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

// Every effect the picker offers must round-trip through the name/id maps and be
// Known — the guard that catches a new effect added to All() but forgotten in
// idByName or nameByID, which would parse as literal text instead.
func TestAllConsistent(t *testing.T) {
	for _, e := range All() {
		if !Known(e.Name) {
			t.Errorf("%q is offered by All() but is not Known", e.Name)
		}
		if idByName[e.Name] != e.ID {
			t.Errorf("%q: idByName = %d, All() ID = %d", e.Name, idByName[e.Name], e.ID)
		}
		if Name(e.ID) != e.Name {
			t.Errorf("id %d: Name = %q, All() name = %q", e.ID, Name(e.ID), e.Name)
		}
		if e.Desc == "" {
			t.Errorf("%q has no description for the picker", e.Name)
		}
	}
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
		// Two directives over exactly the same runes: length can't tell them
		// apart, so only their recorded order says which is the outer one.
		"\\shimmer{\\glow{nested}}",
		// A backslash that is neither an escape nor a directive must survive
		// untouched — an edited Windows path must not grow a slash per edit.
		"a path C:\\temp and a \\shimmer{live} word",
		"regex \\d+ next to \\rainbow{colour}",
		// A literal, escaped directive must stay literal.
		"\\\\shimmer{not an effect} but \\pulse{this is}",
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

// The markup a re-opened post shows must be the markup that was typed, not just
// something that happens to compile to the same thing.
func TestReconstructReproducesTheSource(t *testing.T) {
	for _, src := range []string{
		"release is \\shimmer{today}",
		"\\shimmer{\\glow{nested}}",
		"a path C:\\temp and a \\shimmer{live} word",
		"regex \\d+ next to \\rainbow{colour}",
	} {
		visible, spans := Parse(src)
		if got := Reconstruct(visible, spans); got != src {
			t.Errorf("Reconstruct = %q; want the original source %q", got, src)
		}
	}
}

// Ordered puts nested spans in opening order — outermost first — even when they
// cover exactly the same runes and Parse recorded the inner one first.
func TestOrderedNestsOutermostFirst(t *testing.T) {
	_, spans := Parse("\\shimmer{\\glow{x}}")
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %+v", spans)
	}
	if spans[0].ID != Glow {
		t.Fatalf("Parse should record the inner span first; got %+v", spans)
	}
	if got := Ordered(spans); got[0].ID != Shimmer || got[1].ID != Glow {
		t.Errorf("Ordered = %+v; want shimmer (outer) before glow (inner)", got)
	}
}

// Highlight addresses the composer *source*, not the visible text — the composer
// is still showing the markup, so Parse's offsets would point at the wrong runes.
func TestHighlightRegions(t *testing.T) {
	// 0123456789...
	// "go \shimmer{now}"
	regions := Highlight("go \\shimmer{now}")
	want := []Region{
		{ID: Shimmer, Start: 3, End: 12},              // "\shimmer{"
		{ID: Shimmer, Start: 12, End: 15, Body: true}, // "now"
		{ID: Shimmer, Start: 15, End: 16},             // "}"
	}
	if len(regions) != len(want) {
		t.Fatalf("regions = %+v; want %+v", regions, want)
	}
	for i := range want {
		if regions[i] != want[i] {
			t.Errorf("region %d = %+v; want %+v", i, regions[i], want[i])
		}
	}
}

// Only what Parse would actually act on lights up — that is the whole feedback
// value: a typo looks different from a working directive.
func TestHighlightIgnoresWhatParseIgnores(t *testing.T) {
	for _, src := range []string{
		"\\shimer{typo}",       // unknown name
		"\\shimmer{unbalanced", // never closed
		"a path C:\\temp",      // not a directive
		"\\\\shimmer{escaped}", // an escaped backslash
		"nothing at all",
	} {
		if got := Highlight(src); len(got) != 0 {
			t.Errorf("Highlight(%q) lit up %+v; want nothing", src, got)
		}
	}
}

// Nested directives are reported innermost-first, so a UI resolving overlaps by
// "first match wins" gives the inner effect the body.
func TestHighlightNestsInnermostFirst(t *testing.T) {
	regions := Highlight("\\shimmer{a \\glow{b} c}")
	var firstBody Region
	for _, r := range regions {
		if r.Body {
			firstBody = r
			break
		}
	}
	if firstBody.ID != Glow {
		t.Errorf("first body region is %+v; want the inner glow to come first", firstBody)
	}
}
