package stl

import (
	"bytes"
	"fmt"
	"image/color"
	"math"
	"os"
	"runtime"
	"testing"
)

// The whole licence for spreading a frame over cores is that it changes nothing
// about the frame. Bands are claimed dynamically and pass 1 runs sharded, so if
// the facet order the depth ties resolve in were not preserved, this is where it
// would show: any difference at all, in any channel, is a bug.
func TestRenderIsIndependentOfWorkerCount(t *testing.T) {
	cube, err := Decode([]byte(cubeASCII))
	if err != nil {
		t.Fatal(err)
	}
	meshes := map[string]*Mesh{
		"cube":    cube,
		"bracket": bracketMesh(), // open surface: nothing culled, both sides drawn
		"dense":   denseMesh(20_000),
	}
	cams := []Camera{
		DefaultCamera(),
		{Yaw: 1.2, Pitch: -0.4, Zoom: 1},
		{Yaw: 0, Pitch: 0, Zoom: 40, PanX: 0.3}, // eye inside the mesh: clipNear
	}
	for name, m := range meshes {
		for _, ssaa := range []int{1, 2} {
			for i, cam := range cams {
				st := Style{Base: color.RGBA{0x93, 0xa7, 0xcc, 0xff}, SSAA: ssaa}
				var one Renderer
				want := one.render(m, cam, st, 320, 240, ssaa, 1)
				ref := bytes.Clone(want.Pix)
				for _, workers := range []int{2, 3, 7, 16} {
					var many Renderer
					got := many.render(m, cam, st, 320, 240, ssaa, workers)
					if !bytes.Equal(ref, got.Pix) {
						t.Errorf("%s ssaa=%d cam %d: %d workers renders differently from 1",
							name, ssaa, i, workers)
					}
				}
			}
		}
	}
}

// A Renderer is reused across frames, and pass 1's shards are part of what it
// keeps. A frame that projects fewer facets than the one before it must not
// inherit the tail of the previous frame's — the model would grow a ghost of
// where it used to be.
func TestRenderReusesShardsWithoutLeakingFacets(t *testing.T) {
	m := denseMesh(20_000)
	st := Style{Base: color.RGBA{0x93, 0xa7, 0xcc, 0xff}, SSAA: 1}
	var r Renderer
	// Zoomed in, then framed: the second frame projects a different (smaller)
	// set of facets through the same shards.
	r.render(m, Camera{Yaw: 0.4, Pitch: 0.2, Zoom: 20}, st, 320, 240, 1, 4)
	got := bytes.Clone(r.render(m, DefaultCamera(), st, 320, 240, 1, 4).Pix)

	var fresh Renderer
	want := fresh.render(m, DefaultCamera(), st, 320, 240, 1, 4)
	if !bytes.Equal(got, want.Pix) {
		t.Error("a reused Renderer renders differently from a fresh one")
	}
}

// Small frames must stay on one core: the goroutines cost more than they save,
// and a transcript full of thumbnails would otherwise fan out per thumbnail.
func TestRenderWorkersScalesWithTheFrame(t *testing.T) {
	cores := runtime.GOMAXPROCS(0)
	if cores < 4 {
		t.Skipf("needs >=4 cores, have %d", cores)
	}
	cases := []struct {
		what           string
		tris, samples  int
		wantMin, wantN int
	}{
		{"tiny ascii cube in a thumbnail", 12, 240 * 180, 1, 1},
		{"dense mesh in a thumbnail", 200_000, 240 * 180, 4, cores - 1},
		{"real part at modal size", 140_000, 2288 * 1720, 4, cores - 1},
	}
	for _, tc := range cases {
		// cores-1 is the ceiling whatever the frame asks for, so a 4-core CI
		// runner caps the fan-out cases at 3 and a hardcoded floor of 4 would
		// be asking for a number the function is not allowed to return.
		wantMin := min(tc.wantMin, cores-1)
		got := renderWorkers(tc.tris, tc.samples)
		if got < wantMin || got > tc.wantN {
			t.Errorf("%s: renderWorkers = %d, want %d..%d", tc.what, got, wantMin, tc.wantN)
		}
	}
	if got := renderWorkers(140_000, 2288*1720); got >= cores {
		t.Errorf("renderWorkers = %d on %d cores: nothing left for the UI", got, cores)
	}
}

// realMesh loads an actual file, for the benchmarks that have to be measured on
// one: a downloaded part's facet count, its winding, and how much of the frame it
// covers are all nothing like a tessellated sphere's.
//
//	MB_STL_DIR=/path/to/stls go test ./internal/stl -run xxx -bench RenderWorkers
func realMesh(tb testing.TB, name string) *Mesh {
	dir := os.Getenv("MB_STL_DIR")
	if dir == "" {
		tb.Skip("set MB_STL_DIR to a directory of .stl files")
	}
	raw, err := os.ReadFile(dir + "/" + name)
	if err != nil {
		tb.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil {
		tb.Fatal(err)
	}
	return m
}

// BenchmarkRenderWorkers is the scaling curve, at the pixel box a HiDPI
// terminal's 16x40 cells actually give the modal.
func BenchmarkRenderWorkers(b *testing.B) {
	const w, h = 2288, 1720
	st := Style{Base: color.RGBA{0x93, 0xa7, 0xcc, 0xff}, SSAA: 1}
	for _, name := range []string{"eiffel.stl", "suzanne.stl"} {
		mesh := realMesh(b, name)
		b.Logf("%s: %d tris closed=%v", name, len(mesh.Tris), mesh.Closed)
		for _, n := range []int{1, 2, 4, 8, runtime.GOMAXPROCS(0) - 1, 0} {
			label := fmt.Sprint(n)
			if n == 0 {
				label = "auto"
			}
			b.Run(name+"/workers="+label, func(b *testing.B) {
				var r Renderer
				cam := DefaultCamera()
				for b.Loop() {
					cam.Yaw += 0.02
					if n == 0 {
						r.Render(mesh, cam, st, w, h)
					} else {
						r.render(mesh, cam, st, w, h, 1, n)
					}
				}
			})
		}
	}
}

// BenchmarkRenderZoom is what a zoomed-in orbit costs. Two things change when you
// fly in: backface culling switches off (cull needs the eye outside the bounding
// sphere, which a spindly part loses long before the eye is inside any actual
// solid), and facets that straddle the near plane project to vertices far off
// screen, so their bounding boxes stop resembling their area.
func BenchmarkRenderZoom(b *testing.B) {
	const w, h = 2288, 1720
	st := Style{Base: color.RGBA{0x93, 0xa7, 0xcc, 0xff}, SSAA: 1}
	mesh := realMesh(b, "eiffel.stl")
	for _, zoom := range []float32{1, 3.5, 4, 8, 24, 64} {
		for _, view := range []struct {
			name  string
			pitch float32
		}{{"3q", 0.52}, {"top", PitchLimit}} {
			cam := Camera{Yaw: -0.62, Pitch: view.pitch, Zoom: zoom}
			b.Run(fmt.Sprintf("zoom=%g/%s", zoom, view.name), func(b *testing.B) {
				var r Renderer
				for b.Loop() {
					cam.Yaw += 0.02
					r.Render(mesh, cam, st, w, h)
				}
			})
		}
	}
}

// The per-row span is an optimisation of the bounding-box scan and nothing more:
// it decides which pixels are *offered* to the edge tests, never which ones pass.
// So the two have to agree to the byte, on the awkward cameras as much as the
// ordinary ones — a span one pixel too narrow is a seam down the middle of a
// facet, and the near-clipped views at high zoom are where the arithmetic that
// computes it is least trustworthy (hence spanSafe, which this also exercises by
// forcing every facet down the fallback).
func TestRasterSpanMatchesTheFullScan(t *testing.T) {
	defer func(v float32) { spanSafe = v }(spanSafe)
	cube, err := Decode([]byte(cubeASCII))
	if err != nil {
		t.Fatal(err)
	}
	meshes := map[string]*Mesh{
		"cube":    cube,
		"bracket": bracketMesh(),
		"dense":   denseMesh(20_000),
	}
	cams := []Camera{
		DefaultCamera(),
		{Yaw: -0.62, Pitch: PitchLimit, Zoom: 1},
		{Yaw: -0.62, Pitch: PitchLimit, Zoom: 6},
		{Yaw: 1.2, Pitch: -0.4, Zoom: 8},
		{Yaw: 0.3, Pitch: 0.1, Zoom: 40, PanX: 0.4, PanY: -0.2},
		{Yaw: 2.7, Pitch: -1.4, Zoom: MaxZoom},
	}
	for name, m := range meshes {
		for ci, cam := range cams {
			for _, ssaa := range []int{1, 2} {
				st := Style{Base: color.RGBA{0x93, 0xa7, 0xcc, 0xff}, SSAA: ssaa}
				spanSafe = 0 // every facet takes the bounding-box path
				var full Renderer
				want := bytes.Clone(full.render(m, cam, st, 300, 220, ssaa, 3).Pix)
				spanSafe = 1 << 14
				var span Renderer
				got := span.render(m, cam, st, 300, 220, ssaa, 3)
				if !bytes.Equal(want, got.Pix) {
					var n int
					for i := range want {
						if want[i] != got.Pix[i] {
							n++
						}
					}
					t.Errorf("%s cam %d ssaa=%d: %d bytes differ between the span and the full scan",
						name, ci, ssaa, n)
				}
			}
		}
	}
}

// Culling is allowed exactly while the eye is outside the *solid*. The bounding
// sphere is only the cheap way to say yes to that: a cube leaves its own sphere
// at zoom 3.6 while the eye is still well clear of the box, and on a spindly part
// the gap is the whole rest of the zoom range.
func TestEyeOutsideTracksTheSolidNotTheSphere(t *testing.T) {
	cube, err := Decode([]byte(cubeASCII))
	if err != nil {
		t.Fatal(err)
	}
	if !cube.Closed {
		t.Fatal("precondition: the cube should parse as closed")
	}
	center, radius := cube.Center(), cube.Radius()
	at := func(zoom float32) (dist float32, sphereSaysOutside, reallyOutside bool) {
		cam := Camera{Yaw: -0.62, Pitch: 0.52, Zoom: zoom}
		sy, cy := math.Sincos(float64(cam.Yaw))
		sp, cp := math.Sincos(float64(cam.Pitch))
		dist = radius / float32(math.Sin(float64(fov/2))) / zoom
		return dist, dist > radius,
			eyeOutside(cube, center, dist, float32(sy), float32(cy), float32(sp), float32(cp), 2)
	}
	if _, sphere, real := at(1); !sphere || !real {
		t.Errorf("framed: sphere=%v ray=%v, both should say outside", sphere, real)
	}
	// Between leaving the sphere and reaching the box: the sphere gives up, the
	// ray does not. This is the whole point of the change.
	if dist, sphere, real := at(4); sphere || !real {
		t.Errorf("zoom 4 (dist %.2f, radius %.2f): sphere=%v ray=%v, want false/true",
			dist, radius, sphere, real)
	}
	if _, _, real := at(MaxZoom); real {
		t.Error("inside the cube at max zoom, the ray says outside: culling would empty the frame")
	}
}
