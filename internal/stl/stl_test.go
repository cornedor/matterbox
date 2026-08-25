package stl

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"math"
	"testing"
)

// cubeASCII is a unit cube from (0,0,0) to (1,1,1), 12 facets, written the way a
// real exporter writes one (including a normal we deliberately ignore).
const cubeASCII = `solid cube
facet normal 0 0 -1
  outer loop
    vertex 0 0 0
    vertex 1 1 0
    vertex 1 0 0
  endloop
endfacet
facet normal 0 0 -1
  outer loop
    vertex 0 0 0
    vertex 0 1 0
    vertex 1 1 0
  endloop
endfacet
facet normal 0 0 1
  outer loop
    vertex 0 0 1
    vertex 1 0 1
    vertex 1 1 1
  endloop
endfacet
facet normal 0 0 1
  outer loop
    vertex 0 0 1
    vertex 1 1 1
    vertex 0 1 1
  endloop
endfacet
facet normal -1 0 0
  outer loop
    vertex 0 0 0
    vertex 0 0 1
    vertex 0 1 1
  endloop
endfacet
facet normal -1 0 0
  outer loop
    vertex 0 0 0
    vertex 0 1 1
    vertex 0 1 0
  endloop
endfacet
facet normal 1 0 0
  outer loop
    vertex 1 0 0
    vertex 1 1 0
    vertex 1 1 1
  endloop
endfacet
facet normal 1 0 0
  outer loop
    vertex 1 0 0
    vertex 1 1 1
    vertex 1 0 1
  endloop
endfacet
facet normal 0 -1 0
  outer loop
    vertex 0 0 0
    vertex 1 0 0
    vertex 1 0 1
  endloop
endfacet
facet normal 0 -1 0
  outer loop
    vertex 0 0 0
    vertex 1 0 1
    vertex 0 0 1
  endloop
endfacet
facet normal 0 1 0
  outer loop
    vertex 0 1 0
    vertex 1 1 1
    vertex 1 1 0
  endloop
endfacet
facet normal 0 1 0
  outer loop
    vertex 0 1 0
    vertex 0 1 1
    vertex 1 1 1
  endloop
endfacet
endsolid cube
`

// binaryCube re-encodes the ASCII cube as a binary STL, so both parsers are
// tested against a mesh with a known answer.
func binaryCube(t *testing.T, header string) []byte {
	t.Helper()
	m, err := Decode([]byte(cubeASCII))
	if err != nil {
		t.Fatalf("ascii cube: %v", err)
	}
	var b bytes.Buffer
	h := make([]byte, 80)
	copy(h, header)
	b.Write(h)
	binary.Write(&b, binary.LittleEndian, uint32(len(m.Tris)))
	for _, tr := range m.Tris {
		for range 3 { // the stored normal, which the parser ignores
			binary.Write(&b, binary.LittleEndian, float32(0))
		}
		for _, v := range [3]Vec3{tr.A, tr.B, tr.C} {
			binary.Write(&b, binary.LittleEndian, v.X)
			binary.Write(&b, binary.LittleEndian, v.Y)
			binary.Write(&b, binary.LittleEndian, v.Z)
		}
		binary.Write(&b, binary.LittleEndian, uint16(0))
	}
	return b.Bytes()
}

func TestDecodeASCII(t *testing.T) {
	m, err := Decode([]byte(cubeASCII))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(m.Tris) != 12 {
		t.Fatalf("facets = %d, want 12", len(m.Tris))
	}
	if m.Min != (Vec3{0, 0, 0}) || m.Max != (Vec3{1, 1, 1}) {
		t.Errorf("bounds = %v..%v, want 0,0,0..1,1,1", m.Min, m.Max)
	}
	if got := m.Center(); got != (Vec3{0.5, 0.5, 0.5}) {
		t.Errorf("Center = %v, want 0.5,0.5,0.5", got)
	}
	if got, want := m.Radius(), float32(math.Sqrt(3)/2); math.Abs(float64(got-want)) > 1e-6 {
		t.Errorf("Radius = %v, want %v", got, want)
	}
}

func TestDecodeBinary(t *testing.T) {
	raw := binaryCube(t, "matterbox test cube")
	m, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(m.Tris) != 12 {
		t.Fatalf("facets = %d, want 12", len(m.Tris))
	}
	if m.Min != (Vec3{0, 0, 0}) || m.Max != (Vec3{1, 1, 1}) {
		t.Errorf("bounds = %v..%v, want the unit cube", m.Min, m.Max)
	}
}

// A binary STL whose 80-byte header begins with the word "solid" is the classic
// trap: the usual keyword sniff calls it ASCII and produces an empty mesh. The
// size arithmetic in isBinary is what has to catch it.
func TestBinaryWithSolidHeader(t *testing.T) {
	raw := binaryCube(t, "solid exported_by_some_cad_package")
	if !isBinary(raw) {
		t.Fatal("isBinary = false for a binary file with a solid header")
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(m.Tris) != 12 {
		t.Fatalf("facets = %d, want 12 — parsed as ASCII?", len(m.Tris))
	}
}

// Trailing padding after the last facet is tolerated; some writers pad.
func TestBinaryTrailingBytes(t *testing.T) {
	raw := append(binaryCube(t, "cube"), 0, 0, 0, 0, 0)
	m, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(m.Tris) != 12 {
		t.Fatalf("facets = %d, want 12", len(m.Tris))
	}
}

// A binary file that claims more facets than it carries is a truncated download;
// parse what is there rather than panicking or refusing.
func TestBinaryTruncated(t *testing.T) {
	raw := binaryCube(t, "cube")
	m, err := Decode(raw[:binaryHeader+6*binaryFacet])
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(m.Tris) != 6 {
		t.Fatalf("facets = %d, want the 6 that survived", len(m.Tris))
	}
}

func TestDecodeRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrNotSTL},
		{"png", []byte("\x89PNG\r\n\x1a\n............................................................................................"), ErrNotSTL},
		{"solid but no facets", []byte("solid nothing\nendsolid nothing\n"), ErrEmpty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decode(tc.in); err == nil {
				t.Fatal("Decode succeeded, want an error")
			} else if tc.want != nil && !errorsIs(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestLooks(t *testing.T) {
	if !Looks([]byte(cubeASCII)) {
		t.Error("Looks = false for an ASCII cube")
	}
	if !Looks(binaryCube(t, "cube")) {
		t.Error("Looks = false for a binary cube")
	}
	if Looks([]byte("GIF89a............................................................................................")) {
		t.Error("Looks = true for a GIF")
	}
}

// --- rendering ------------------------------------------------------------

func renderCube(t *testing.T, c Camera, w, h int) (*Renderer, []byte) {
	t.Helper()
	m, err := Decode([]byte(cubeASCII))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	var r Renderer
	img := r.Render(m, c, Style{
		Base: color.RGBA{0x9a, 0xa6, 0xd0, 0xff},
		SSAA: 1,
	}, w, h)
	if img.Rect.Dx() != w || img.Rect.Dy() != h {
		t.Fatalf("size = %v, want %dx%d", img.Rect, w, h)
	}
	return &r, img.Pix
}

// The cube must land in the middle of the frame, cover a sensible share of it,
// and leave the corners empty — the fit is on the bounding sphere, so the
// silhouette can never touch the edge.
func TestRenderFramesTheModel(t *testing.T) {
	const w, h = 120, 120
	_, pix := renderCube(t, DefaultCamera(), w, h)

	opaque := func(x, y int) bool { return pix[(y*w+x)*4+3] > 0 }

	if !opaque(w/2, h/2) {
		t.Error("centre pixel is background — the model isn't in frame")
	}
	for _, p := range [][2]int{{0, 0}, {w - 1, 0}, {0, h - 1}, {w - 1, h - 1}} {
		if opaque(p[0], p[1]) {
			t.Errorf("corner %v is covered — the fit lets the model clip the frame", p)
		}
	}
	var covered int
	for y := range h {
		for x := range w {
			if opaque(x, y) {
				covered++
			}
		}
	}
	frac := float64(covered) / float64(w*h)
	if frac < 0.15 || frac > 0.75 {
		t.Errorf("coverage = %.2f, want the model to fill a sensible part of the frame", frac)
	}
}

// A background alpha of zero has to stay genuinely transparent: the terminal
// composites the PNG over its own background, and a tinted "transparent" fill
// shows up as a rectangle around the model.
func TestRenderTransparentBackground(t *testing.T) {
	const w, h = 64, 64
	_, pix := renderCube(t, DefaultCamera(), w, h)
	p := pix[0:4] // top-left corner: background
	if p[3] != 0 || p[0] != 0 || p[1] != 0 || p[2] != 0 {
		t.Errorf("background pixel = %v, want fully transparent and unpremultiplied", p)
	}
}

// Three faces of a cube seen three-quarter-on must come out as three distinct
// shades, or the render is flat and reads as a hexagon rather than a box.
func TestRenderShadesFacesDistinctly(t *testing.T) {
	const w, h = 160, 160
	_, pix := renderCube(t, DefaultCamera(), w, h)
	shades := map[[3]byte]int{}
	for y := range h {
		for x := range w {
			p := pix[(y*w+x)*4:]
			if p[3] == 0 {
				continue
			}
			shades[[3]byte{p[0], p[1], p[2]}]++
		}
	}
	big := 0
	for _, n := range shades {
		if n > w*h/100 { // ignore antialiasing crumbs and edge-on slivers
			big++
		}
	}
	if big < 3 {
		t.Errorf("distinct large shades = %d (of %d total), want 3 visible faces", big, len(shades))
	}
}

// Turning the camera has to change the picture, and coming back to where it
// started has to reproduce it — the whole interaction rests on the camera being
// the only state a frame depends on.
func TestRenderCameraIsDeterministic(t *testing.T) {
	const w, h = 80, 80
	base := DefaultCamera()
	_, a := renderCube(t, base, w, h)
	a = bytes.Clone(a)

	turned := base
	turned.Yaw += 0.8
	_, b := renderCube(t, turned, w, h)
	if bytes.Equal(a, b) {
		t.Error("turning the camera produced an identical frame")
	}

	_, again := renderCube(t, base, w, h)
	if !bytes.Equal(a, again) {
		t.Error("the same camera produced a different frame")
	}
}

// Zooming in must cover more of the frame; zooming out, less.
func TestRenderZoom(t *testing.T) {
	const w, h = 100, 100
	coverage := func(z float32) float64 {
		c := DefaultCamera()
		c.Zoom = z
		_, pix := renderCube(t, c, w, h)
		var n int
		for i := 3; i < len(pix); i += 4 {
			if pix[i] > 0 {
				n++
			}
		}
		return float64(n) / float64(w*h)
	}
	out, mid, in := coverage(0.5), coverage(1), coverage(2)
	if !(out < mid && mid < in) {
		t.Errorf("coverage out=%.3f mid=%.3f in=%.3f, want strictly increasing with zoom", out, mid, in)
	}
}

// Panning moves the model across the frame without changing how much of it there
// is, and it does so in the direction asked for.
func TestRenderPan(t *testing.T) {
	const w, h = 120, 120
	centroid := func(c Camera) (float64, float64) {
		_, pix := renderCube(t, c, w, h)
		var sx, sy, n float64
		for y := range h {
			for x := range w {
				if pix[(y*w+x)*4+3] > 0 {
					sx += float64(x)
					sy += float64(y)
					n++
				}
			}
		}
		if n == 0 {
			t.Fatal("nothing rendered")
		}
		return sx / n, sy / n
	}
	c := DefaultCamera()
	x0, y0 := centroid(c)
	right := c
	right.PanX = 0.2
	x1, y1 := centroid(right)
	if x1 <= x0+5 {
		t.Errorf("pan right moved x from %.1f to %.1f, want a clear rightward shift", x0, x1)
	}
	if math.Abs(y1-y0) > 2 {
		t.Errorf("pan right also moved y from %.1f to %.1f", y0, y1)
	}
	up := c
	up.PanY = 0.2
	_, y2 := centroid(up)
	if y2 >= y0-5 {
		t.Errorf("pan up moved y from %.1f to %.1f, want a clear upward shift", y0, y2)
	}
}

// The z-buffer has to hide the far side of a solid: with a cube in front of the
// camera every visible pixel belongs to a front face, so the shade histogram
// must not contain the back faces' (darker, light-averted) tones spread across
// the silhouette. The blunt version of that: the picture must not change when
// the facet order is reversed.
func TestRenderDepthOrderIndependent(t *testing.T) {
	m, err := Decode([]byte(cubeASCII))
	if err != nil {
		t.Fatal(err)
	}
	const w, h = 100, 100
	st := Style{Base: color.RGBA{0x9a, 0xa6, 0xd0, 0xff}, SSAA: 1}
	var r1, r2 Renderer
	a := bytes.Clone(r1.Render(m, DefaultCamera(), st, w, h).Pix)

	rev := &Mesh{Tris: make([]Triangle, len(m.Tris)), Min: m.Min, Max: m.Max}
	for i, t := range m.Tris {
		rev.Tris[len(m.Tris)-1-i] = t
	}
	b := r2.Render(rev, DefaultCamera(), st, w, h).Pix
	if !bytes.Equal(a, b) {
		t.Error("reversing facet order changed the image — the z-buffer isn't resolving depth")
	}
}

// Clamp is what every input path relies on to stay in range.
func TestCameraClamp(t *testing.T) {
	c := Camera{Pitch: 3, Zoom: 1e6, PanX: 99, PanY: -99, Yaw: 100}.Clamp()
	if c.Pitch > PitchLimit || c.Pitch < -PitchLimit {
		t.Errorf("Pitch = %v, want within ±%v", c.Pitch, PitchLimit)
	}
	if c.Zoom != MaxZoom {
		t.Errorf("Zoom = %v, want %v", c.Zoom, MaxZoom)
	}
	if c.PanX > 1.5 || c.PanY < -1.5 {
		t.Errorf("pan = %v,%v, want clamped", c.PanX, c.PanY)
	}
	if c.Yaw < -2*math.Pi || c.Yaw > 2*math.Pi {
		t.Errorf("Yaw = %v, want wrapped into a turn", c.Yaw)
	}
	if got := (Camera{Zoom: 0}.Clamp()); got.Zoom != MinZoom {
		t.Errorf("zero Zoom clamped to %v, want %v", got.Zoom, MinZoom)
	}
}

// Zooming past the model's own surface must keep drawing: near-plane clipping is
// what makes the top of the zoom range usable instead of a black screen.
func TestRenderInsideTheModel(t *testing.T) {
	const w, h = 80, 80
	c := DefaultCamera()
	c.Zoom = MaxZoom
	_, pix := renderCube(t, c, w, h)
	var n int
	for i := 3; i < len(pix); i += 4 {
		if pix[i] > 0 {
			n++
		}
	}
	if n < w*h/2 {
		t.Errorf("covered %d of %d pixels at max zoom, want the view filled by the surface", n, w*h)
	}
}

// A mesh with no volume (all facets in one plane) or a single degenerate point
// must not divide by zero or panic.
func TestRenderDegenerate(t *testing.T) {
	flat := &Mesh{Tris: []Triangle{{Vec3{0, 0, 0}, Vec3{1, 0, 0}, Vec3{0, 1, 0}}}}
	flat.bounds()
	var r Renderer
	r.Render(flat, DefaultCamera(), Style{Base: color.RGBA{255, 255, 255, 255}}, 40, 40)

	point := &Mesh{Tris: []Triangle{{}}}
	point.bounds()
	r.Render(point, DefaultCamera(), Style{Base: color.RGBA{255, 255, 255, 255}}, 40, 40)

	r.Render(nil, DefaultCamera(), Style{Base: color.RGBA{255, 255, 255, 255}}, 40, 40)
	r.Render(&Mesh{}, DefaultCamera(), Style{Base: color.RGBA{255, 255, 255, 255}}, 0, 0)
}

// Supersampling has to produce intermediate tones along the silhouette; without
// them a model a few dozen cells across is all staircase.
func TestRenderSupersamples(t *testing.T) {
	m, err := Decode([]byte(cubeASCII))
	if err != nil {
		t.Fatal(err)
	}
	const w, h = 60, 60
	var r Renderer
	img := r.Render(m, DefaultCamera(), Style{Base: color.RGBA{0xff, 0xff, 0xff, 0xff}, SSAA: 2}, w, h)
	var partial int
	for i := 3; i < len(img.Pix); i += 4 {
		if a := img.Pix[i]; a > 0 && a < 255 {
			partial++
		}
	}
	if partial == 0 {
		t.Error("no partially-covered pixels with SSAA 2 — supersampling isn't resolving")
	}
}

func BenchmarkRender(b *testing.B) {
	m, err := Decode([]byte(cubeASCII))
	if err != nil {
		b.Fatal(err)
	}
	var r Renderer
	st := Style{Base: color.RGBA{0x9a, 0xa6, 0xd0, 0xff}, SSAA: 2}
	for b.Loop() {
		r.Render(m, DefaultCamera(), st, 640, 480)
	}
}

// denseMesh builds a tessellated sphere with roughly n triangles — a stand-in
// for a real scanned or CAD-exported part, where the triangle count rather than
// the pixel fill is what costs.
func denseMesh(n int) *Mesh {
	seg := int(math.Sqrt(float64(n) / 2))
	m := &Mesh{}
	at := func(i, j int) Vec3 {
		u := float64(i) / float64(seg) * 2 * math.Pi
		v := float64(j) / float64(seg) * math.Pi
		return Vec3{
			float32(math.Sin(v) * math.Cos(u)),
			float32(math.Cos(v)),
			float32(math.Sin(v) * math.Sin(u)),
		}
	}
	for i := range seg {
		for j := range seg {
			a, b, c, d := at(i, j), at(i+1, j), at(i+1, j+1), at(i, j+1)
			m.Tris = append(m.Tris, Triangle{a, b, c}, Triangle{a, c, d})
		}
	}
	m.bounds()
	return m
}

func BenchmarkRenderDense(b *testing.B) {
	m := denseMesh(200_000)
	b.Logf("%d triangles", len(m.Tris))
	var r Renderer
	st := Style{Base: color.RGBA{0x9a, 0xa6, 0xd0, 0xff}}
	for b.Loop() {
		r.Render(m, DefaultCamera(), st, 900, 600)
	}
}

func BenchmarkRenderThumb(b *testing.B) {
	m := denseMesh(200_000)
	var r Renderer
	st := Style{Base: color.RGBA{0x9a, 0xa6, 0xd0, 0xff}, SSAA: 2}
	for b.Loop() {
		r.Render(m, DefaultCamera(), st, 240, 180)
	}
}

func BenchmarkDecodeBinary(b *testing.B) {
	m := denseMesh(200_000)
	var buf bytes.Buffer
	h := make([]byte, 80)
	buf.Write(h)
	binary.Write(&buf, binary.LittleEndian, uint32(len(m.Tris)))
	for _, tr := range m.Tris {
		for range 3 {
			binary.Write(&buf, binary.LittleEndian, float32(0))
		}
		for _, v := range [3]Vec3{tr.A, tr.B, tr.C} {
			binary.Write(&buf, binary.LittleEndian, v.X)
			binary.Write(&buf, binary.LittleEndian, v.Y)
			binary.Write(&buf, binary.LittleEndian, v.Z)
		}
		binary.Write(&buf, binary.LittleEndian, uint16(0))
	}
	raw := buf.Bytes()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		if _, err := Decode(raw); err != nil {
			b.Fatal(err)
		}
	}
}

// boxMesh is an axis-aligned box, for tests that need to know which way is up.
func boxMesh(x0, y0, z0, x1, y1, z1 float32) *Mesh {
	p := [8]Vec3{
		{x0, y0, z0}, {x1, y0, z0}, {x1, y1, z0}, {x0, y1, z0},
		{x0, y0, z1}, {x1, y0, z1}, {x1, y1, z1}, {x0, y1, z1},
	}
	var tris []Triangle
	quad := func(a, b, c, d int) {
		tris = append(tris, Triangle{p[a], p[b], p[c]}, Triangle{p[a], p[c], p[d]})
	}
	quad(0, 1, 2, 3)
	quad(4, 5, 6, 7)
	quad(0, 1, 5, 4)
	quad(2, 3, 7, 6)
	quad(1, 2, 6, 5)
	quad(0, 3, 7, 4)
	m := &Mesh{Tris: tris}
	m.bounds()
	return m
}

// extent measures the rendered silhouette's bounding box in pixels.
func extent(t *testing.T, m *Mesh, c Camera, w, h int) (dx, dy int) {
	t.Helper()
	var r Renderer
	img := r.Render(m, c, Style{Base: color.RGBA{0xff, 0xff, 0xff, 0xff}, SSAA: 1}, w, h)
	x0, y0, x1, y1 := w, h, -1, -1
	for y := range h {
		for x := range w {
			if img.Pix[(y*w+x)*4+3] > 0 {
				x0, y0 = min(x0, x), min(y0, y)
				x1, y1 = max(x1, x), max(y1, y)
			}
		}
	}
	if x1 < 0 {
		t.Fatal("nothing rendered")
	}
	return x1 - x0 + 1, y1 - y0 + 1
}

// TestRenderIsZUp pins the axis convention, which is the one thing in this
// renderer that is not a free choice. STL has no up-axis field, but everything
// that produces one — CAD, slicers, Blender's exporter — is Z-up, with the part
// standing on the build plate. Getting it wrong renders every real model tipped
// a quarter turn onto its face, and no test over a symmetric mesh can see it.
func TestRenderIsZUp(t *testing.T) {
	const w, h = 200, 200
	front := Camera{Yaw: 0, Pitch: 0, Zoom: 1}

	// A mast: thin in X and Y, long in Z. Seen from the front it must be tall.
	mast := boxMesh(-1, -1, -10, 1, 1, 10)
	dx, dy := extent(t, mast, front, w, h)
	if dy <= dx*3 {
		t.Errorf("a Z-long box renders %dx%d from the front — Z is not up", dx, dy)
	}

	// The same box laid along Y is pointing away from the eye at the front view,
	// so it must render small in both directions, not tall.
	beam := boxMesh(-1, -10, -1, 1, 10, 1)
	dx, dy = extent(t, beam, front, w, h)
	if dy > dx*3 {
		t.Errorf("a Y-long box renders %dx%d from the front — Y is being treated as up", dx, dy)
	}

	// And laid along X it must be wide.
	rail := boxMesh(-10, -1, -1, 10, 1, 1)
	dx, dy = extent(t, rail, front, w, h)
	if dx <= dy*3 {
		t.Errorf("an X-long box renders %dx%d from the front — X is not screen right", dx, dy)
	}

	// Looking straight down, the mast is end-on: small in both directions.
	top := Camera{Yaw: 0, Pitch: PitchLimit, Zoom: 1}
	dx, dy = extent(t, mast, top, w, h)
	if dy > dx*3 {
		t.Errorf("the mast renders %dx%d from above — we are not looking down its length", dx, dy)
	}
}

// Pitching up must look down *on* the model: the top face comes into view. With
// a box whose top is the only face at that height, "the top is visible" is the
// same statement as "more of the frame is covered from above than edge-on".
func TestRenderPitchLooksDown(t *testing.T) {
	const w, h = 160, 160
	plate := boxMesh(-10, -10, -1, 10, 10, 1) // a wide, flat plate on the XY plane

	edge := Camera{Yaw: 0, Pitch: 0, Zoom: 1}
	above := Camera{Yaw: 0, Pitch: 1.2, Zoom: 1}

	_, edgeH := extent(t, plate, edge, w, h)
	_, aboveH := extent(t, plate, above, w, h)
	if aboveH <= edgeH {
		t.Errorf("plate is %dpx tall edge-on and %dpx from above — pitching up is not looking down on it", edgeH, aboveH)
	}
}

// TestYawTurnsModelLeft pins which way the model turns, which is a fact about
// the *screen* and therefore invisible to any test that only reads Camera.Yaw.
// The UI's two inputs disagree on purpose — the arrow keys orbit the camera,
// a mouse drag grabs the model — and neither can be reviewed without knowing
// what increasing Yaw does to a face you are looking at.
//
// Tested through viewXform directly rather than through a rendered silhouette:
// the question is where a known point lands, and asking it that way says so.
func TestYawTurnsModelLeft(t *testing.T) {
	var (
		center = Vec3{}
		front  = Vec3{0, -1, 0} // the face a front view looks at
		right  = Vec3{1, 0, 0}  // the model's right-hand side
	)
	// viewXform's arguments are the pre-computed sincos pair for each angle.
	at := func(p Vec3, yaw float32) Vec3 {
		sy, cy := math.Sincos(float64(yaw))
		return viewXform(p, center, float32(sy), float32(cy), 0, 1, 10)
	}

	if x := at(front, 0).X; math.Abs(float64(x)) > 1e-6 {
		t.Fatalf("at yaw 0 the front face sits at view x %v, want centred", x)
	}
	if x := at(right, 0).X; x <= 0 {
		t.Fatalf("at yaw 0 the model's right side is at view x %v, want screen-right", x)
	}

	// Increasing yaw swings the front face to the left. Everything about the
	// controls' signs follows from this one fact.
	if x := at(front, 0.3).X; x >= 0 {
		t.Errorf("increasing yaw put the front face at view x %v, want it moved left", x)
	}
	// A quarter turn puts what was the right-hand side at the front (centred).
	if x := at(right, math.Pi/2).X; math.Abs(float64(x)) > 1e-6 {
		t.Errorf("a quarter turn left the right side at view x %v, want it swung to the front", x)
	}
	// …and, being at the front, nearest the eye.
	if d := -at(right, math.Pi/2).Z; d >= 10 {
		t.Errorf("the swung-round side is at depth %v, want nearer than the orbit centre (10)", d)
	}
}

// --- backface culling ------------------------------------------------------

func decodeCube(t *testing.T) *Mesh {
	t.Helper()
	m, err := Decode([]byte(cubeASCII))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A closed solid is the case culling exists for, and the parse has to recognise
// it without help.
func TestClosedSolidIsCullable(t *testing.T) {
	if m := decodeCube(t); !m.Closed {
		t.Error("a closed cube wasn't recognised as closed")
	}
	// One facet short of closed is a hole, and a hole is exactly what culling
	// would show through.
	holed := decodeCube(t)
	holed.Tris, holed.Closed = holed.Tris[:len(holed.Tris)-1], false
	holed.orient()
	if holed.Closed {
		t.Error("a cube missing a facet was culled anyway")
	}
	// So is a surface that was never closed to begin with.
	open := &Mesh{Tris: []Triangle{
		{Vec3{0, 0, 0}, Vec3{1, 0, 0}, Vec3{0, 1, 0}},
		{Vec3{1, 0, 0}, Vec3{1, 1, 0}, Vec3{0, 1, 0}},
		{Vec3{0, 0, 0}, Vec3{0, 1, 0}, Vec3{0, 0, 1}},
		{Vec3{0, 0, 0}, Vec3{0, 0, 1}, Vec3{1, 0, 0}},
	}}
	open.bounds()
	open.orient()
	if open.Closed {
		t.Error("an open surface was culled")
	}
}

// An inside-out mesh is consistently wound too, so it passes both tests — and
// culling it as-is would drop every face you can actually see. It has to be
// turned the right way round instead.
func TestInvertedSolidIsReoriented(t *testing.T) {
	m := decodeCube(t)
	for i := range m.Tris {
		m.Tris[i].B, m.Tris[i].C = m.Tris[i].C, m.Tris[i].B
	}
	m.Closed = false
	m.orient()
	if !m.Closed {
		t.Fatal("an inside-out cube wasn't recognised as closed")
	}
	if v := m.signedVolume(); v <= 0 {
		t.Errorf("signed volume = %v after re-orienting, want positive (outward)", v)
	}
	// And it draws the same as the version that was never inverted.
	if diff := renderDiff(t, decodeCube(t), m); diff > 0.005 {
		t.Errorf("re-oriented cube differs from the original in %.2f%% of pixels", diff*100)
	}
}

// The two tests in orient() cover different defects, and a mesh needs both: an
// edge count is blind to a region flipped as a block (only its boundary edges
// come out odd), and the normal sum is blind to flips scattered one facet at a
// time (they cancel each other out).
func TestBothWindingTestsEarnTheirPlace(t *testing.T) {
	block := decodeCube(t)
	block.Tris[0].B, block.Tris[0].C = block.Tris[0].C, block.Tris[0].B
	block.Tris[1].B, block.Tris[1].C = block.Tris[1].C, block.Tris[1].B
	block.Closed = false
	block.orient()
	if block.Closed {
		t.Error("a flipped face was culled")
	}
	if block.windingClosure() <= 0.01 {
		t.Errorf("windingClosure = %v on a flipped face, want it to notice", block.windingClosure())
	}
}

// Culling must not change the picture — that is the entire justification for it.
func TestCullDrawsTheSameAsFlipping(t *testing.T) {
	for _, c := range []Camera{
		DefaultCamera(),
		func() Camera { c := DefaultCamera(); c.Yaw += 1.1; c.Pitch += 0.4; return c }(),
		func() Camera { c := DefaultCamera(); c.Pitch = -1.2; return c }(),
	} {
		flip, cull := decodeCube(t), decodeCube(t)
		if !cull.Closed {
			t.Fatal("setup: the cube should be cullable")
		}
		flip.Closed = false
		if diff := renderDiffAt(t, flip, cull, c); diff > 0.01 {
			t.Errorf("culled render differs in %.2f%% of pixels at %v", diff*100, c)
		}
	}
}

// From inside the model there is no front face to hide the back ones, so culling
// has to switch itself off — see TestRenderInsideTheModel for the symptom.
func TestCullStopsInsideTheModel(t *testing.T) {
	m := decodeCube(t)
	c := DefaultCamera()
	c.Zoom = MaxZoom
	var r Renderer
	img := r.Render(m, c, Style{Base: color.RGBA{0x9a, 0xa6, 0xd0, 0xff}}, 60, 60)
	var n int
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0 {
			n++
		}
	}
	if n < 60*60/2 {
		t.Errorf("covered %d of %d pixels from inside a cullable model", n, 60*60)
	}
}

func renderDiff(t *testing.T, a, b *Mesh) float64 {
	t.Helper()
	return renderDiffAt(t, a, b, DefaultCamera())
}

// renderDiffAt is the fraction of pixels where two meshes draw differently.
func renderDiffAt(t *testing.T, a, b *Mesh, c Camera) float64 {
	t.Helper()
	const w, h = 120, 120
	st := Style{Base: color.RGBA{0x9a, 0xa6, 0xd0, 0xff}, SSAA: 2}
	var r Renderer
	ia := r.Render(a, c, st, w, h)
	pa := make([]byte, len(ia.Pix))
	copy(pa, ia.Pix)
	ib := r.Render(b, c, st, w, h)

	var diff int
	for i := 0; i < len(pa); i += 4 {
		for k := range 4 {
			if d := int(pa[i+k]) - int(ib.Pix[i+k]); d > 8 || d < -8 {
				diff++
				break
			}
		}
	}
	return float64(diff) / float64(len(pa)/4)
}
