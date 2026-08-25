package svgimg

import "testing"

func TestNormalizePath(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{{
		name: "already one set per command",
		in:   "M 1 2 L 3 4 Z",
		want: "M 1 2 L 3 4 Z",
	}, {
		name: "implicit repeated arc gets its own letter",
		in:   "m8 2a6 6 0 0 0-6 6 6 6 0 0 0 6 6",
		want: "m 8 2 a 6 6 0 0 0 -6 6 a 6 6 0 0 0 6 6",
	}, {
		name: "packed arc flags split apart",
		in:   "M8 1a7 7 0 100 14",
		want: "M 8 1 a 7 7 0 1 0 0 14",
	}, {
		name: "implicit repeated moveto continues as lineto",
		in:   "M1 1 2 2 3 3",
		want: "M 1 1 L 2 2 L 3 3",
	}, {
		name: "relative moveto repeats as relative lineto",
		in:   "m1 1 2 2",
		want: "m 1 1 l 2 2",
	}, {
		name: "implicit repeated cubic",
		in:   "M0 0c1 1 2 2 3 3 4 4 5 5 6 6",
		want: "M 0 0 c 1 1 2 2 3 3 c 4 4 5 5 6 6",
	}, {
		name: "negative numbers abutting the previous one",
		in:   "M0 0L1-1-2 2",
		want: "M 0 0 L 1 -1 L -2 2",
	}, {
		name: "dot starts a new number",
		in:   "M.5.5L.25.75",
		want: "M .5 .5 L .25 .75",
	}, {
		name: "exponent stays one number",
		in:   "M1e3 2E-2L0 0",
		want: "M 1e3 2E-2 L 0 0",
	}, {
		name: "horizontal and vertical repeat",
		in:   "M0 0h1 2 3v4 5",
		want: "M 0 0 h 1 h 2 h 3 v 4 v 5",
	}, {
		name: "closepath then a fresh subpath",
		in:   "M0 0h4zm0 6h4z",
		want: "M 0 0 h 4 z m 0 6 h 4 z",
	}, {
		name: "trailing junk that is not a whole set is dropped",
		in:   "M0 0L1 1L2",
		want: "M 0 0 L 1 1",
	}, {
		name: "comma separators",
		in:   "M0,0 L1,1",
		want: "M 0 0 L 1 1",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizePath(tc.in); got != tc.want {
				t.Errorf("normalizePath(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLooksLikePathData(t *testing.T) {
	for _, in := range []string{"M0 0L1 1", "m 1 1 h 4 z", "M.5.5"} {
		if !looksLikePathData(in) {
			t.Errorf("looksLikePathData(%q) = false, want true", in)
		}
	}
	// A d= attribute that is not path data must be left alone.
	for _, in := range []string{"", "url(#x)", "translate(1,2)", "L1 1", "M0 0 url(#a)"} {
		if looksLikePathData(in) {
			t.Errorf("looksLikePathData(%q) = true, want false", in)
		}
	}
}

func TestNormalizePathAttrs(t *testing.T) {
	in := []byte(`<svg><path d="m8 2a6 6 0 0 0-6 6 6 6 0 0 0 6 6"/><g transform="translate(1)" d="url(#x)"/></svg>`)
	got := string(normalizePathAttrs(in))
	if want := `d="m 8 2 a 6 6 0 0 0 -6 6 a 6 6 0 0 0 6 6"`; !contains(got, want) {
		t.Errorf("path data not normalised:\n%s", got)
	}
	if !contains(got, `d="url(#x)"`) {
		t.Errorf("non-path d= attribute was rewritten:\n%s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
