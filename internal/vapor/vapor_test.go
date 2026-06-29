package vapor

import (
	"strings"
	"testing"
)

func newTestRenderer(mode string) *Renderer {
	stops, _ := ParseHexStops("#ffd21e,#ff9b2f,#ff3d7f,#ec1e63")
	return New(Options{
		Mode: mode, Coverage: "octant",
		Speed: 0.5, Height: 0.7, Valley: 1, ValleyHeight: 0.3, SunY: 1,
		SunStops: stops,
		Text:     &TextOpts{Text: "Matterbox", X: 0, Y: 4, Z: 22, Scale: 1.5, Depth: 1, RotX: 25},
	})
}

func TestRenderGridDimensions(t *testing.T) {
	for _, mode := range []string{"glyph", "blocks", "ascii"} {
		r := newTestRenderer(mode)
		r.Resize(80, 24)
		if r.Cols() != 80 || r.Rows() != 24 {
			t.Fatalf("%s: size = %dx%d, want 80x24", mode, r.Cols(), r.Rows())
		}
		grid := r.Render(0)
		if len(grid) != 24 {
			t.Fatalf("%s: rows = %d, want 24", mode, len(grid))
		}
		for y, row := range grid {
			if len(row) != 80 {
				t.Fatalf("%s: row %d width = %d, want 80", mode, y, len(row))
			}
		}
	}
}

func TestFrameAnimatesAndCarriesColor(t *testing.T) {
	r := newTestRenderer("glyph")
	r.Resize(100, 30)
	a := r.Frame(0)
	b := r.Frame(3)
	if a == "" || b == "" {
		t.Fatal("empty frame")
	}
	if a == b {
		t.Fatal("frames identical across time; animation is not advancing")
	}
	if !strings.Contains(a, "\x1b[38;2;") || !strings.Contains(a, "\x1b[48;2;") {
		t.Fatal("frame missing 24-bit fg/bg SGR escapes")
	}
}

func TestRenderWithAnimationDoesNotPanic(t *testing.T) {
	anim, err := LoadAnimationJSON([]byte(`{"duration":6,"loop":false,
		"sun":{"y":[{"t":0,"v":-3},{"t":6,"v":0,"ease":"smooth"}]},
		"mountain":{"speed":[{"t":5,"v":1},{"t":6,"v":0.2,"ease":"smooth"}]},
		"text":{"pos":{"z":[{"t":0,"v":55},{"t":5,"v":22,"ease":"smooth"}]},
		"rot":{"x":[{"t":0,"v":-110},{"t":6,"v":25,"ease":"smooth"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := New(Options{Mode: "glyph", Speed: 0.5, Height: 0.7, Valley: 1, ValleyHeight: 0.3, SunY: 1,
		Text: &TextOpts{Text: "Hi", Z: 22, Scale: 1, Depth: 1}, Anim: anim})
	r.Resize(90, 28)
	for _, tt := range []float64{0, 1.5, 3, 6, 9} {
		if r.Frame(tt) == "" {
			t.Fatalf("empty frame at t=%v", tt)
		}
	}
}

func TestResizeSameSizeIsStable(t *testing.T) {
	r := newTestRenderer("glyph")
	r.Resize(80, 24)
	r.Render(1)
	r.Resize(80, 24) // no-op resize must not corrupt the grid
	if g := r.Render(2); len(g) != 24 || len(g[0]) != 80 {
		t.Fatalf("grid = %dx%d after re-resize", len(g), len(g[0]))
	}
}
