package vapor

import (
	"math"
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

func TestDemoBobsLetters(t *testing.T) {
	mk := func(demo bool) *Renderer {
		stops, _ := ParseHexStops("#ffd21e,#ff9b2f,#ff3d7f,#ec1e63")
		r := New(Options{
			Mode: "glyph", Coverage: "octant",
			Speed: 0.5, Height: 0.7, Valley: 1, ValleyHeight: 0.3, SunY: 1,
			SunStops: stops,
			Text:     &TextOpts{Text: "Matterbox", X: 0, Y: 4, Z: 22, Scale: 1.5, Depth: 1, RotX: 25, Demo: demo},
		})
		r.Resize(100, 30)
		return r
	}
	// At a fixed time the two renderers share an identical scene (same drive,
	// stars, sun), so the only thing that can differ is the per-letter bob.
	const tt = 1.0
	if mk(true).Frame(tt) == mk(false).Frame(tt) {
		t.Fatal("demo frame identical to static; letter bob has no effect")
	}
}

func TestDemoFlipWaveAndRest(t *testing.T) {
	const letters = 9 // "Matterbox"
	// Nothing flips during the initial settle delay.
	for ci := 0; ci < letters; ci++ {
		if _, ok := demoFlipAngle(demoFlipStartDelay-0.1, ci); ok {
			t.Fatalf("letter %d flipping before the start delay", ci)
		}
	}
	// The flip ripples across letters: just after the first wave starts letter 0
	// is turning but a later letter has not begun.
	if _, ok := demoFlipAngle(demoFlipStartDelay+0.1, 0); !ok {
		t.Fatal("letter 0 should be flipping just after the first wave starts")
	}
	if _, ok := demoFlipAngle(demoFlipStartDelay+0.1, 3); ok {
		t.Fatal("letter 3 should still be idle while the wave reaches it")
	}
	// Mid-window, a letter is partway through a single full turn.
	a, ok := demoFlipAngle(demoFlipStartDelay+demoFlipDuration/2, 0)
	if !ok || a <= 0 || a >= 2*math.Pi {
		t.Fatalf("mid-flip angle = %v ok=%v, want within (0, 2π)", a, ok)
	}
	// By the tail of a cycle every letter has finished — the rest period.
	for ci := 0; ci < letters; ci++ {
		if _, ok := demoFlipAngle(demoFlipStartDelay+demoFlipCycle-0.1, ci); ok {
			t.Fatalf("letter %d still flipping during the rest period", ci)
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

func TestSerializeUnderline(t *testing.T) {
	grid := [][]Cell{{
		{R: 'a', Fg: RGB{R: 255}, HasBg: false},
		{R: 'b', Fg: RGB{R: 255}, HasBg: false, Underline: true},
		{R: 'c', Fg: RGB{R: 255}, HasBg: false},
	}}
	s := Serialize(grid)
	if !strings.Contains(s, "\x1b[4m") {
		t.Fatal("underlined cell missing the SGR underline-on escape")
	}
	if !strings.Contains(s, "\x1b[24m") {
		t.Fatal("missing the SGR underline-off escape after the underlined run")
	}
	// A grid with no underline must not emit either escape.
	plain := Serialize([][]Cell{{{R: 'x', Fg: RGB{R: 255}}}})
	if strings.Contains(plain, "\x1b[4m") || strings.Contains(plain, "\x1b[24m") {
		t.Fatal("plain text emitted an underline escape")
	}
}
