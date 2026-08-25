package stl

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"testing"
)

// TestDumpRenders is not an assertion, it is a window. Set STL_DUMP to a
// directory and it writes what the rasterizer actually produces, which is the
// only way to review a renderer: whether a bracket reads as a bracket is not a
// thing a unit test can have an opinion about.
//
//	STL_DUMP=/tmp/stl go test ./internal/stl -run TestDumpRenders
//
// The renders are composited over a flat terminal-ish background before being
// written, because that is how they are seen: the renderer leaves its background
// transparent and the terminal supplies its own.
func TestDumpRenders(t *testing.T) {
	dir := os.Getenv("STL_DUMP")
	if dir == "" {
		t.Skip("set STL_DUMP=<dir> to write renders")
	}
	var (
		darkBg   = color.RGBA{0x1c, 0x1f, 0x26, 0xff}
		lightBg  = color.RGBA{0xfa, 0xfa, 0xf7, 0xff}
		darkMat  = Style{Base: color.RGBA{0x93, 0xa7, 0xcc, 0xff}, SSAA: 2}
		lightMat = Style{Base: color.RGBA{0x5f, 0x73, 0x96, 0xff}, SSAA: 2}
	)
	var r Renderer
	dump := func(name string, m *Mesh, c Camera, st Style, bg color.RGBA, w, h int) {
		t.Helper()
		src := r.Render(m, c, st, w, h)
		out := image.NewRGBA(src.Bounds())
		draw.Draw(out, out.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
		draw.Draw(out, out.Bounds(), src, src.Bounds().Min, draw.Over)
		f, err := os.Create(dir + "/" + name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
	}

	br := bracketMesh()
	sp := denseMesh(4608)
	cube, err := Decode([]byte(cubeASCII))
	if err != nil {
		t.Fatal(err)
	}

	const w, h = 480, 360
	dump("bracket-dark", br, DefaultCamera(), darkMat, darkBg, w, h)
	dump("bracket-light", br, DefaultCamera(), lightMat, lightBg, w, h)
	dump("sphere-dark", sp, DefaultCamera(), darkMat, darkBg, w, h)
	dump("cube-dark", cube, DefaultCamera(), darkMat, darkBg, w, h)

	// The inline thumbnail's real size: 26 cols × 10 rows on an 8×16 cell.
	dump("thumbnail", br, DefaultCamera(), darkMat, darkBg, 208, 160)

	turned := DefaultCamera()
	turned.Yaw, turned.Pitch = 2.2, -0.4
	dump("turned", br, turned, darkMat, darkBg, w, h)

	top := DefaultCamera()
	top.Yaw, top.Pitch = 0, PitchLimit
	dump("top", br, top, darkMat, darkBg, w, h)

	front := DefaultCamera()
	front.Yaw, front.Pitch = 0, 0
	dump("front", br, front, darkMat, darkBg, w, h)

	zoomed := DefaultCamera()
	zoomed.Zoom = 6
	dump("zoomed", br, zoomed, darkMat, darkBg, w, h)

	inside := DefaultCamera()
	inside.Zoom = MaxZoom
	dump("inside", br, inside, darkMat, darkBg, w, h)

	panned := DefaultCamera()
	panned.Zoom, panned.PanX, panned.PanY = 3, 0.3, -0.2
	dump("panned", br, panned, darkMat, darkBg, w, h)
}

// bracketMesh is an L-bracket with a stiffening rib and a nose, built Z-up the
// way a real STL is: the base plate lies on the XY build plate, the upright
// rises in +Z, and the nose points toward -Y, which is the face a front view
// looks at. That asymmetry is the point — a cube renders identically under any
// axis convention, so only a mesh with an unmistakable top and front makes a
// wrong up-axis visible (see TestRenderIsZUp).
//
// It is also a far better test of shading than a cube: parallel faces at
// different depths, a concave corner, and thin features that only read
// correctly if the z-buffer and the two-sided lighting are both right.
func bracketMesh() *Mesh {
	var tris []Triangle
	box := func(x0, y0, z0, x1, y1, z1 float32) {
		tris = append(tris, boxMesh(x0, y0, z0, x1, y1, z1).Tris...)
	}
	box(0, 0, 0, 60, 40, 6)   // base plate, flat on the build plate
	box(0, 0, 0, 6, 40, 40)   // upright, rising in +Z
	box(4, 16, 4, 10, 24, 34) // rib bracing the two
	box(20, -10, 0, 32, 0, 6) // nose, pointing toward the front (-Y)
	m := &Mesh{Tris: tris}
	m.bounds()
	return m
}

var _ = math.Pi // keep math imported when the dump is skipped
