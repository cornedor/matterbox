package vapor

import (
	"testing"
)

// benchRenderer reproduces the welcome wizard's exact scene options (see
// welcome.New): glyph/octant, the 4-stop sun, the flying "Matterbox" title.
func benchRenderer(cols, rows int) *Renderer {
	stops, _ := ParseHexStops("#ffd21e,#ff9b2f,#ff3d7f,#ec1e63")
	r := New(Options{
		Mode: "glyph", Coverage: "octant",
		Speed: 0.5, Height: 0.7, Valley: 1, ValleyHeight: 0.3, SunY: 1,
		SunStops: stops,
		Text:     &TextOpts{Text: "Matterbox", X: 0, Y: 4, Z: 22, Scale: 1.5, Depth: 1, RotX: 25},
	})
	r.Resize(cols, rows)
	return r
}

var benchSizes = []struct {
	name       string
	cols, rows int
}{
	{"80x24", 80, 24},
	{"120x40", 120, 40},
	{"200x50", 200, 50},
}

// BenchmarkFrame measures the full per-frame cost the welcome View() pays:
// scene.Render (3D rasterize into the framebuffer) + presentGlyphCells (glyph
// fit) + Serialize (ANSI string). t=3 sits mid-intro with the terrain and title
// both on screen — the heaviest steady-state frame.
func BenchmarkFrame(b *testing.B) {
	for _, sz := range benchSizes {
		b.Run(sz.name, func(b *testing.B) {
			r := benchRenderer(sz.cols, sz.rows)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				grid := r.Render(3.0)
				_ = Serialize(grid)
			}
		})
	}
}

// BenchmarkRender isolates scene.Render + present (no Serialize).
func BenchmarkRender(b *testing.B) {
	for _, sz := range benchSizes {
		b.Run(sz.name, func(b *testing.B) {
			r := benchRenderer(sz.cols, sz.rows)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = r.Render(3.0)
			}
		})
	}
}

// BenchmarkSerialize isolates the ANSI serialization of a rendered grid.
func BenchmarkSerialize(b *testing.B) {
	for _, sz := range benchSizes {
		b.Run(sz.name, func(b *testing.B) {
			r := benchRenderer(sz.cols, sz.rows)
			grid := r.Render(3.0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = Serialize(grid)
			}
		})
	}
}
