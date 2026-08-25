package stl

import (
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
)

// The software rasterizer. There is no GPU here and no dependency doing the
// work: one z-buffered, flat-shaded triangle fill, sized to whatever pixel box
// the terminal gives us. That is enough because of where the pixels go — under a
// fixed Kitty image id, out of band, so a frame costs the TUI's View() nothing
// (the same trick the games use; see internal/ui/stlview.go). What it has to be
// is *fast enough to orbit*, which is why the Renderer keeps its buffers and why
// shading is per-facet rather than per-pixel.
//
// Flat shading is not a shortcut taken reluctantly — it is the correct model for
// STL. The format stores no vertex normals and no shared topology, so there is
// nothing to interpolate between: every facet is its own flat plane. A printed
// part looks like a printed part.

// Camera is the orbit view: two angles, a zoom, and a pan. It holds no matrices
// and no derived state, so the UI can keep one in its model, nudge a field on a
// keypress or a drag, and hand it straight back to Render.
type Camera struct {
	// Yaw turns the model about its vertical axis, Pitch tips it toward or away
	// from the viewer. Radians.
	Yaw, Pitch float32
	// Zoom scales the fitted distance: 1 frames the whole model, larger moves in.
	Zoom float32
	// PanX, PanY shift the model across the frame, in fractions of the smaller
	// screen axis. Screen-space rather than model-space on purpose: a drag that
	// pans has to keep the model under the cursor, and it has to do that
	// identically at every zoom level.
	PanX, PanY float32
}

// DefaultCamera is the three-quarter view every CAD tool opens on, and the one
// the inline thumbnail always uses: turned off-axis so two faces and the top are
// visible at once, which is what makes a 2D picture read as a 3D object.
func DefaultCamera() Camera {
	return Camera{Yaw: -0.62, Pitch: 0.52, Zoom: 1}
}

// Zoom limits. The floor keeps a model from receding to a dot; the ceiling is
// generous because near-plane clipping (see clipNear) means flying inside the
// mesh is handled properly rather than avoided.
const (
	MinZoom = 0.25
	MaxZoom = 64
)

// PitchLimit stops the orbit just short of straight down / straight up. At
// exactly ±π/2 the yaw axis and the view axis line up and the model spins about
// the screen normal instead of turning, which reads as the controls breaking.
const PitchLimit = float32(math.Pi/2 - 0.01)

// Clamp brings a camera back into range. Called after every user nudge, so no
// input path has to remember the limits: yaw wraps (spinning is continuous),
// pitch and zoom clamp, pan is bounded so the model can't be lost off-screen.
func (c Camera) Clamp() Camera {
	twoPi := float32(2 * math.Pi)
	c.Yaw = float32(math.Mod(float64(c.Yaw), float64(twoPi)))
	c.Pitch = min(max(c.Pitch, -PitchLimit), PitchLimit)
	c.Zoom = min(max(c.Zoom, MinZoom), MaxZoom)
	// Bounded so the model can always be found again: at ±1 it is exactly one
	// screen off-centre, and half a screen further is as far as anyone needs to
	// push a detail into a corner.
	c.PanX = min(max(c.PanX, -1.5), 1.5)
	c.PanY = min(max(c.PanY, -1.5), 1.5)
	return c
}

// Style is the look of a render: the material, the ground the model sits
// against, and how hard we work for the edges.
type Style struct {
	// Base is the material colour. Lit two-sided, so this is the colour of a
	// surface facing the light rather than an average.
	Base color.RGBA
	// Back fills everywhere the model isn't. A zero alpha leaves the background
	// transparent, which is what both callers want: the terminal's own background
	// shows through, so a render sits in the transcript (and in the modal) without
	// a rectangle of the wrong colour around it.
	Back color.RGBA
	// SSAA is the supersampling factor: the frame is rasterized SSAA times
	// oversampled on each axis and box-filtered down. 1 is off. At terminal
	// resolution a model is only a few dozen cells across, so its silhouette is
	// the whole picture and 2 is worth the 4× fill — see ssaaFor.
	SSAA int
}

// ssaaMax bounds supersampling so a triangle-heavy model at a large modal size
// can't quietly turn one frame into tens of millions of samples.
const ssaaMax = 2

// ssaaFor picks the supersampling factor for a mesh at a given output size:
// antialias while the total sample count stays sane, and drop to native pixels
// for the models where it wouldn't. Keeping this here rather than at the call
// site means the thumbnail and the interactive view make the same trade.
func ssaaFor(tris, pxW, pxH int) int {
	const maxSamples = 6 << 20 // ~6M samples/frame keeps a drag responsive
	if tris <= 0 || pxW <= 0 || pxH <= 0 {
		return 1
	}
	if pxW*pxH*ssaaMax*ssaaMax > maxSamples {
		return 1
	}
	return ssaaMax
}

// Renderer holds the frame buffers across frames. A drag redraws at ~20fps and
// the buffers are megabytes; allocating them per frame would hand the GC tens of
// MB/s for nothing. One Renderer belongs to one open viewer — not safe for
// concurrent use, the same contract the game renderers keep.
//
// A frame is spread across cores, because at modal size this is emphatically
// *pixel*-bound rather than mesh-bound: measured on a real 140k-facet part at
// 2288×1720 (what a HiDPI terminal's 16×40px cells give the modal — 3.9 Mpx),
// the rasterizer was 60ms of an 88ms frame, and a **968**-facet mesh still
// missed 30fps. Fill is what costs, and fill is what splits.
//
// The split has to be by *rows*, not by facet, because the z-buffer is shared
// state: a facet can land anywhere on screen, so two workers drawing two facets
// can contend for one pixel. A worker that owns a band of scanlines owns those
// pixels and those depths exclusively and needs no synchronisation at all — but
// which bands a facet touches is only known once it has been transformed and
// projected. Hence two passes: pass 1 transforms/shades/projects every facet
// (sharded by facet, each worker appending to its own slice), pass 2 hands out
// row bands. Deterministic despite the concurrency — each band is drawn by one
// worker, in facet order — so the frame is bit-identical to a single-threaded
// one, which TestRenderIsIndependentOfWorkerCount pins.
type Renderer struct {
	out  *image.RGBA // the returned image, at the requested size
	buf  *image.RGBA // the supersampled buffer (aliases out when SSAA is 1)
	zbuf []float32   // reciprocal depth, parallel to buf's pixels
	ss   int

	// shards is pass 1's output, one slice per worker, kept across frames for
	// the same reason the pixel buffers are: at 140k facets it is megabytes.
	// Pass 2 reads them in order rather than a merged slice, which saves
	// copying all of it every frame.
	shards [][]projTri
}

// projTri is one facet after pass 1: screen-space vertices, the flat colour it
// shaded to, and the rows it covers — which is all pass 2 needs to decide
// whether the facet concerns its band.
type projTri struct {
	a, b, c screenPt
	col     color.RGBA
	y0, y1  int32
}

// renderWorkers spreads a frame over cores when there is enough of it to pay for
// the goroutines, and stays on one core when there isn't — a transcript
// thumbnail is a few hundred pixels wide.
//
// One core is left to the rest of the program on purpose. The render runs in a
// Cmd while bubbletea keeps handling the drag that asked for it, and the last
// core is worth more to input latency than to the frame: on a 12-core machine
// 11 workers and 12 measure the same anyway.
func renderWorkers(tris, samples int) int {
	n := runtime.GOMAXPROCS(0) - 1
	if n <= 1 {
		return 1
	}
	// A facet costs about as much as ten samples — 16ms per 140k facets against
	// 11.2ms per Mpx of fill, from a linear fit over four frame sizes — so this
	// is the frame's cost in one currency.
	units := tris*10 + samples
	// ~1.3ms of work per worker. Below that the goroutines, the shard
	// bookkeeping and the band scan are a bigger share than what the
	// parallelism gives back.
	return min(max(units>>17, 1), n)
}

// bands splits h rows evenly over workers and runs fn on each range. One worker
// runs it inline: a frame small enough not to want the parallelism should not
// pay for a goroutine and a WaitGroup either.
func bands(h, workers int, fn func(y0, y1 int)) {
	if workers <= 1 || h < workers {
		fn(0, h)
		return
	}
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fn(i*h/workers, (i+1)*h/workers)
		}(i)
	}
	wg.Wait()
}

// fov is the vertical field of view. Narrow enough that a mechanical part looks
// measured rather than fish-eyed, wide enough to still read as perspective
// rather than an isometric drawing.
const fov = float32(32 * math.Pi / 180)

// Render draws the mesh into the Renderer's buffer and returns it. The result is
// valid only until the next call — the caller encodes it (to PNG, for the Kitty
// transmit) and does not keep it.
//
// A nil or empty mesh yields a background-filled frame rather than nil, so a
// caller mid-load has something to put on screen.
func (r *Renderer) Render(m *Mesh, c Camera, st Style, pxW, pxH int) *image.RGBA {
	pxW, pxH = max(pxW, 1), max(pxH, 1)
	ss := st.SSAA
	if ss <= 0 {
		ss = ssaaFor(triCount(m), pxW, pxH)
	}
	ss = min(max(ss, 1), ssaaMax)
	return r.render(m, c, st, pxW, pxH, ss,
		renderWorkers(triCount(m), pxW*ss*pxH*ss))
}

// render is Render with the two decisions it makes — supersampling and how many
// cores to use — handed in, which is what lets a test render the same scene at
// one worker and at twelve and compare the bytes.
func (r *Renderer) render(m *Mesh, c Camera, st Style, pxW, pxH, ss, workers int) *image.RGBA {
	r.resize(pxW, pxH, ss)
	r.clear(st.Back, workers)
	if m != nil && len(m.Tris) > 0 {
		c = c.Clamp()
		r.project(m, c, st, workers)
		r.raster(workers)
	}
	r.resolve(workers)
	return r.out
}

func triCount(m *Mesh) int {
	if m == nil {
		return 0
	}
	return len(m.Tris)
}

// resize (re)allocates the buffers when the output size or supersampling factor
// changes, and reuses them when it hasn't — the common case, since a drag holds
// both fixed.
func (r *Renderer) resize(pxW, pxH, ss int) {
	if r.out == nil || r.out.Rect.Dx() != pxW || r.out.Rect.Dy() != pxH {
		r.out = image.NewRGBA(image.Rect(0, 0, pxW, pxH))
		r.buf = nil
	}
	bw, bh := pxW*ss, pxH*ss
	if ss == 1 {
		// Nothing to filter: rasterize straight into the output and skip resolve.
		r.buf = r.out
	} else if r.buf == nil || r.ss != ss || r.buf.Rect.Dx() != bw || r.buf.Rect.Dy() != bh {
		r.buf = image.NewRGBA(image.Rect(0, 0, bw, bh))
	}
	r.ss = ss
	if n := bw * bh; len(r.zbuf) != n {
		r.zbuf = make([]float32, n)
	}
}

// clear resets the pixel and depth buffers, banded across the workers — at modal
// size it is 30MB of stores, which is milliseconds even though it is nothing but
// stores.
//
// The four-byte pattern loop stays even for the transparent background every
// caller actually asks for, where `for i := range pix { pix[i] = 0 }` would
// compile to memclr. That looks like the obvious win and measures as the
// opposite: 6.3ms against 3.9ms for the same 30MB on the arm64 machine this was
// profiled on, reproducibly. Whatever memclr does for a block that size, it is
// slower than just storing zeroes.
func (r *Renderer) clear(back color.RGBA, workers int) {
	// Premultiplied, because image.RGBA is: a translucent background must not
	// carry colour, or the terminal composites a tinted haze over its own.
	back = premul(back)
	stride, w := r.buf.Stride, r.buf.Rect.Dx()
	bands(r.buf.Rect.Dy(), workers, func(y0, y1 int) {
		pix := r.buf.Pix[y0*stride : y1*stride]
		for i := 0; i < len(pix); i += 4 {
			pix[i], pix[i+1], pix[i+2], pix[i+3] = back.R, back.G, back.B, back.A
		}
		zb := r.zbuf[y0*w : y1*w]
		for i := range zb {
			zb[i] = 0 // reciprocal depth: 0 is infinitely far
		}
	})
}

func premul(c color.RGBA) color.RGBA {
	if c.A == 0xff {
		return c
	}
	a := uint32(c.A)
	return color.RGBA{
		uint8(uint32(c.R) * a / 255),
		uint8(uint32(c.G) * a / 255),
		uint8(uint32(c.B) * a / 255),
		c.A,
	}
}

// resolve box-filters the supersampled buffer down into the output. With SSAA 1
// the two are the same image and there is nothing to do.
func (r *Renderer) resolve(workers int) {
	if r.ss == 1 {
		return
	}
	ss := r.ss
	n := uint32(ss * ss)
	w, h := r.out.Rect.Dx(), r.out.Rect.Dy()
	bands(h, workers, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			dst := r.out.Pix[y*r.out.Stride:]
			for x := range w {
				var sr, sg, sb, sa uint32
				for dy := range ss {
					row := r.buf.Pix[(y*ss+dy)*r.buf.Stride+(x*ss)*4:]
					for dx := range ss {
						p := row[dx*4:]
						sr += uint32(p[0])
						sg += uint32(p[1])
						sb += uint32(p[2])
						sa += uint32(p[3])
					}
				}
				o := dst[x*4:]
				o[0] = uint8(sr / n)
				o[1] = uint8(sg / n)
				o[2] = uint8(sb / n)
				o[3] = uint8(sa / n)
			}
		}
	})
}

// lightDir is the key light, fixed in *view* space rather than model space: it
// rides the camera like a headlamp offset up and to the left. That is what makes
// orbiting legible — a light fixed to the model would swing the shading around
// as you turn it and the shape would be hard to read at half the angles.
// Off-axis rather than straight down the view axis: a near-headlight lights
// every visible face about equally, which flattens a curved surface into a disc.
// Pushed up and to the left, the classic three-quarter key, it gives a sphere a
// light side and a shadow side and a part its edges.
var lightDir = Vec3{-0.55, 0.62, 0.50}.norm()

// project is pass 1: transform, shade, clip and project every facet into
// r.shards, sharded over the workers so no two touch the same slice. Nothing is
// drawn here — see the Renderer comment for why the passes are split.
func (r *Renderer) project(m *Mesh, c Camera, st Style, workers int) {
	w, h := r.buf.Rect.Dx(), r.buf.Rect.Dy()
	center := m.Center()
	radius := m.Radius()

	// Fit: put the camera far enough back that the bounding sphere is inscribed
	// in the (vertical) field of view, then let Zoom move it in. Because the fit
	// is on a sphere, not the box, the model never clips the frame as it turns.
	dist := radius / float32(math.Sin(float64(fov/2))) / c.Zoom

	// One focal length for both axes so the picture is never stretched; the
	// smaller screen axis is the one that has to contain the model.
	focal := float32(min(w, h)) / (2 * float32(math.Tan(float64(fov/2))))
	span := float32(min(w, h))
	cx := float32(w)/2 + c.PanX*span
	cy := float32(h)/2 - c.PanY*span

	sy, cyaw := math.Sincos(float64(c.Yaw))
	sp, cp := math.Sincos(float64(c.Pitch))
	syf, cyf, spf, cpf := float32(sy), float32(cyaw), float32(sp), float32(cp)

	// near is a fraction of the model's own size, so it scales with the mesh
	// rather than being an absolute that clips a 2mm part or lets a 2m one poke
	// through.
	near := radius / 512

	// Back faces can be dropped only while the eye is outside the solid — the
	// whole argument for it is that a front face is always in the way, and from
	// inside there isn't one. The top of the zoom range flies through the surface
	// (see MaxZoom and TestRenderInsideTheModel), and from in there the only thing
	// to look at *is* the inside of the far wall.
	//
	// "Outside" was the bounding sphere, which is a poor stand-in for the solid
	// and is what made a zoomed-in orbit expensive. A spindly part leaves that
	// sphere at a fraction of the zoom range: the Eiffel tower's sphere has a
	// radius of 72 against a 55×55×121 box, so culling switched off at zoom 3.6
	// and stayed off for the whole rest of the range — while the eye was in open
	// air between struts, nowhere near inside anything. That doubled the facets
	// from there up, and doubled them exactly where each one is at its largest on
	// screen. Measured on the tower from above at modal size: 32ms with the sphere
	// test against 16ms with this one.
	//
	// So the sphere is kept only as the cheap way to say yes, and the real
	// question is asked when it can't: cast a ray from the eye and count the
	// facets it crosses, odd being inside. Exact for a closed mesh, and free at
	// the zoom levels people actually sit at, since the sphere answers those.
	cull := m.Closed && (dist > radius || eyeOutside(m, center, dist, syf, cyf, spf, cpf, workers))

	if len(r.shards) != workers {
		r.shards = make([][]projTri, workers)
	}
	n := len(m.Tris)
	shard := func(k, i0, i1 int) {
		out := r.shards[k][:0]
		var poly [4]Vec3 // near-clipping a triangle yields at most a quad
		for i := i0; i < i1; i++ {
			t := &m.Tris[i]
			// Model → view: recentre on the orbit point, yaw, pitch, then push away
			// from the eye. Written out rather than via a matrix type because this is
			// the hot loop and the compiler keeps it all in registers.
			a := viewXform(t.A, center, syf, cyf, spf, cpf, dist)
			b := viewXform(t.B, center, syf, cyf, spf, cpf, dist)
			cc := viewXform(t.C, center, syf, cyf, spf, cpf, dist)

			// The facet normal, in view space. Recomputed from the geometry — the
			// file's own normal is not trusted, see Triangle.
			//
			// Pointing away from the camera then means one of two things. On a closed
			// solid seen from outside (cull, above) it is a back face, hidden behind a
			// front one by definition, and dropping it here rather than letting the
			// depth buffer reject it pixel by pixel is worth about a third of the
			// rasterizer on a real part. On anything else — an open surface, or a mesh
			// whose winding disagrees with itself, which a great many STLs do — there
			// is no such guarantee, and the normal is flipped toward the camera
			// instead so the facet still shades as solid rather than leaving a hole.
			nv := b.sub(a).cross(cc.sub(a)).norm()
			if nv.Z < 0 {
				if cull {
					continue
				}
				nv = Vec3{-nv.X, -nv.Y, -nv.Z}
			}
			col := shade(st.Base, nv)

			verts, ok := clipNear(&poly, a, b, cc, near)
			if !ok {
				continue
			}
			// Fan-triangulate whatever the clip left (3 or 4 vertices).
			for k := 1; k+1 < verts; k++ {
				pa := project(poly[0], focal, cx, cy)
				pb := project(poly[k], focal, cx, cy)
				pc := project(poly[k+1], focal, cx, cy)
				// The row span, which is what pass 2 bands on. Facets entirely
				// above or below the frame are dropped here rather than in the
				// rasterizer, so they cost the band scan nothing.
				y0 := int32(min(pa.y, min(pb.y, pc.y)))
				y1 := int32(max(pa.y, max(pb.y, pc.y))) + 1
				if y1 <= 0 || int(y0) >= h {
					continue
				}
				out = append(out, projTri{pa, pb, pc, col, y0, y1})
			}
		}
		r.shards[k] = out
	}
	if workers <= 1 {
		shard(0, 0, n)
		return
	}
	var wg sync.WaitGroup
	for k := range workers {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			shard(k, k*n/workers, (k+1)*n/workers)
		}(k)
	}
	wg.Wait()
}

// raster is pass 2: fill the projected facets, one band of rows per worker.
//
// The bands are handed out one at a time rather than split evenly up front,
// because a model's coverage down the frame is nowhere near uniform — an Eiffel
// tower is a spike with nothing either side of it — and an even split leaves
// most workers finished and idle while one draws the middle. Four bands per
// worker is enough slack to even that out without making the per-band facet scan
// the new cost.
func (r *Renderer) raster(workers int) {
	w, h := r.buf.Rect.Dx(), r.buf.Rect.Dy()
	if workers <= 1 {
		r.rasterBand(0, h, w)
		return
	}
	nb := workers * 4
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				b := int(next.Add(1)) - 1
				if b >= nb {
					return
				}
				r.rasterBand(b*h/nb, (b+1)*h/nb, w)
			}
		}()
	}
	wg.Wait()
}

// rasterBand draws every facet that touches rows [y0, y1) into them, and nothing
// outside them. The shards are walked in order, so the facet order — and
// therefore the outcome of every depth tie — is the order the mesh is in,
// whatever the worker count.
func (r *Renderer) rasterBand(y0, y1, w int) {
	for _, sh := range r.shards {
		for i := range sh {
			t := &sh[i]
			if int(t.y1) <= y0 || int(t.y0) >= y1 {
				continue
			}
			r.tri(t.a, t.b, t.c, t.col, w, y0, y1)
		}
	}
}

// viewXform takes a model-space point to view space: recentred on the orbit
// point, yawed, pitched, and pushed away from the eye.
//
// The axis convention is the one thing here that is not a free choice. STL
// carries no up-axis field, but the world that uses it — 3D printing, CAD,
// every slicer, and Blender's exporter — is uniformly **Z-up**, with the part
// standing on the XY build plate. So Z is up and yaw turns about it. Treating Y
// as up (the convention a graphics API would suggest) renders every real model
// face-down, tipped a quarter turn onto its nose.
//
// Base orientation, at yaw and pitch zero: screen right is model +X, screen up
// is model +Z, and the eye sits on the -Y side looking toward +Y — which is the
// "front" view every CAD tool opens on.
func viewXform(p, center Vec3, sy, cy, sp, cp, dist float32) Vec3 {
	x, y, z := p.X-center.X, p.Y-center.Y, p.Z-center.Z
	// Yaw about the up axis.
	rx := cy*x + sy*y
	fwd := -sy*x + cy*y
	// Into the base orientation: up is model Z, and +Z in view space points back
	// at the eye, so the depth axis is the negated forward axis.
	up, back := z, -fwd
	// Pitch about the screen's horizontal axis. Positive tips the model's top
	// toward the eye — i.e. you look down on it — which is the direction a drag
	// downward moves it in every 3D application.
	ry := cp*up - sp*back
	rz := sp*up + cp*back
	// The eye is at +dist on the view Z axis looking back along it, so a visible
	// point has a negative view Z and its depth is -Z.
	return Vec3{rx, ry, rz - dist}
}

// eyeOutside reports whether the camera's eye is outside the solid, by parity of
// the facets a ray from it crosses. Only asked when the bounding sphere can't
// answer (see project), and only meaningful for a closed mesh.
//
// Three rays rather than one, majority wins. Parity is exact for a ray in general
// position but not for one that grazes an edge or a vertex — the crossing there
// is counted twice or not at all — and a static camera would hold a wrong answer
// indefinitely rather than passing through it. Two extra rays a degree or so off
// cost nothing next to the frame they protect: getting this wrong either shows
// through a wall or gives up the culling.
func eyeOutside(m *Mesh, center Vec3, dist, sy, cy, sp, cp float32, workers int) bool {
	// The eye and the view direction back in model space. The rotation project
	// applies is orthonormal, so its inverse is its transpose, and the eye — the
	// origin of view space — comes back as the third row of it scaled by dist.
	fwd := Vec3{cp * sy, -cp * cy, sp}
	eye := Vec3{center.X + dist*fwd.X, center.Y + dist*fwd.Y, center.Z + dist*fwd.Z}
	dir := Vec3{-fwd.X, -fwd.Y, -fwd.Z}

	// Two directions across the view axis, to tilt the spare rays along.
	up := Vec3{0, 0, 1}
	if abs32(dir.Z) > 0.9 {
		up = Vec3{1, 0, 0}
	}
	s1 := dir.cross(up).norm()
	s2 := dir.cross(s1).norm()
	const tilt = 0.017 // ~1°
	rays := [3]Vec3{
		dir,
		Vec3{dir.X + tilt*s1.X, dir.Y + tilt*s1.Y, dir.Z + tilt*s1.Z}.norm(),
		Vec3{dir.X + tilt*s2.X, dir.Y + tilt*s2.Y, dir.Z + tilt*s2.Z}.norm(),
	}

	var outside int
	for _, ray := range rays {
		if rayCrossings(m, eye, ray, workers)%2 == 0 {
			outside++
		}
	}
	return outside >= 2
}

// rayCrossings counts the facets a ray from orig crosses ahead of itself,
// Möller-Trumbore, sharded over the workers.
func rayCrossings(m *Mesh, orig, dir Vec3, workers int) int {
	// eps is relative to the model, like near is: an absolute one would treat a
	// 2mm part's every facet as edge-on.
	eps := m.Radius() * 1e-7
	count := func(i0, i1 int) int {
		var n int
		for i := i0; i < i1; i++ {
			t := &m.Tris[i]
			e1, e2 := t.B.sub(t.A), t.C.sub(t.A)
			pv := dir.cross(e2)
			det := e1.dot(pv)
			if det > -eps && det < eps {
				continue // ray parallel to the facet
			}
			inv := 1 / det
			tv := orig.sub(t.A)
			u := tv.dot(pv) * inv
			if u < 0 || u > 1 {
				continue
			}
			qv := tv.cross(e1)
			v := dir.dot(qv) * inv
			if v < 0 || u+v > 1 {
				continue
			}
			if e2.dot(qv)*inv > eps {
				n++
			}
		}
		return n
	}
	if workers <= 1 {
		return count(0, len(m.Tris))
	}
	parts := make([]int, workers)
	var wg sync.WaitGroup
	for k := range workers {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			parts[k] = count(k*len(m.Tris)/workers, (k+1)*len(m.Tris)/workers)
		}(k)
	}
	wg.Wait()
	var n int
	for _, p := range parts {
		n += p
	}
	return n
}

// spanSafe is the coordinate magnitude beyond which tri stops narrowing rows and
// walks the bounding box instead — see the comment there. A var, not a const, so
// TestRasterSpanMatchesTheFullScan can zero it and render the same frame both
// ways; nothing else writes it.
var spanSafe float32 = 1 << 14

// screenPt is a projected vertex: pixel position plus reciprocal depth, which is
// what interpolates linearly across a triangle in screen space (depth itself
// does not) and so is what the z-buffer stores.
type screenPt struct {
	x, y float32
	invZ float32
}

func project(v Vec3, focal, cx, cy float32) screenPt {
	depth := -v.Z
	inv := 1 / depth
	return screenPt{cx + v.X*focal*inv, cy - v.Y*focal*inv, inv}
}

// clipNear clips a triangle against the near plane (view Z = -near), writing the
// resulting 3- or 4-gon into out and returning its vertex count. ok is false when
// the whole triangle is behind the plane.
//
// This is what lets MaxZoom be 64: at high zoom the eye ends up inside the mesh,
// and facets that straddle the eye plane project to nonsense. Rejecting those
// triangles outright would punch holes that flicker as you move; clipping them
// draws the part that is genuinely in front, which is what you want when you have
// deliberately zoomed into a wall.
func clipNear(out *[4]Vec3, a, b, c Vec3, near float32) (int, bool) {
	in := [3]Vec3{a, b, c}
	// A vertex is visible when it is at least `near` in front of the eye, i.e.
	// its view Z is more negative than -near.
	vis := [3]bool{a.Z <= -near, b.Z <= -near, c.Z <= -near}
	switch {
	case vis[0] && vis[1] && vis[2]:
		out[0], out[1], out[2] = a, b, c
		return 3, true
	case !vis[0] && !vis[1] && !vis[2]:
		return 0, false
	}
	n := 0
	for i := range 3 {
		cur, nxt := in[i], in[(i+1)%3]
		if vis[i] {
			out[n] = cur
			n++
		}
		if vis[i] != vis[(i+1)%3] {
			// Cross the plane: lerp to Z == -near.
			t := (-near - cur.Z) / (nxt.Z - cur.Z)
			out[n] = Vec3{
				cur.X + t*(nxt.X-cur.X),
				cur.Y + t*(nxt.Y-cur.Y),
				-near,
			}
			n++
		}
		if n == 4 {
			break
		}
	}
	if n < 3 {
		return 0, false
	}
	return n, true
}

// shade lights one facet: ambient, a two-sided lambert key light, a hemispheric
// fill that keeps the shadow side from going flat, and a tight specular so a
// curved surface shows a highlight and reads as curved. All computed once per
// triangle, which is why this can afford to look like something.
//
// The balance is set by two constraints the terminal imposes. The ambient floor
// is high — a face turned fully away from the light still comes out at about
// half the material's value — because the background is transparent: a face that
// went nearly black would merge into a dark terminal and put a hole in the
// silhouette. And the key light is correspondingly restrained so ambient + key +
// fill lands at roughly 1.0 rather than clipping, since a model whose lit faces
// all saturate is a white blob with no shape in it.
func shade(base color.RGBA, n Vec3) color.RGBA {
	lam := n.dot(lightDir)
	if lam < 0 {
		lam = -lam * 0.35 // the far side catches a little bounce, not nothing
	}
	// Fill from straight ahead — a cheap stand-in for a softbox at the camera.
	fill := n.Z
	if fill < 0 {
		fill = -fill
	}
	lit := 0.26 + 0.60*lam + 0.15*fill

	// Blinn-Phong against a half-vector that also lives in view space. Raised to
	// the 32nd by squaring five times: no math.Pow in the inner loop. Kept modest
	// — a printed part is matte, and a hot highlight on one facet of a tessellated
	// curve reads as a rendering artefact rather than as gloss.
	spec := n.dot(halfVec)
	if spec < 0 {
		spec = 0
	}
	s2 := spec * spec
	s4 := s2 * s2
	s8 := s4 * s4
	s16 := s8 * s8
	hi := 0.16 * s16 * s16

	return color.RGBA{
		clamp8(float32(base.R)*lit + 255*hi),
		clamp8(float32(base.G)*lit + 255*hi),
		clamp8(float32(base.B)*lit + 255*hi),
		0xff,
	}
}

// halfVec is normalize(lightDir + eye), the eye being +Z in view space.
var halfVec = Vec3{lightDir.X, lightDir.Y, lightDir.Z + 1}.norm()

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func clamp8(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v)
}

// tri fills one projected triangle with a z-buffer test. Half-space edge
// functions over the triangle's pixel bounding box: no clipping cases, no
// per-scanline setup, and every test is a couple of multiply-adds.
//
// bandY0/bandY1 are the rows the calling worker owns; the fill is clamped to
// them, which is the whole of what makes the concurrency safe (see raster).
func (r *Renderer) tri(a, b, c screenPt, col color.RGBA, w, bandY0, bandY1 int) {
	// Signed area. Zero means the facet is edge-on and covers nothing; the sign
	// tells us the winding, which we normalise so one inside test serves both.
	area := (b.x-a.x)*(c.y-a.y) - (b.y-a.y)*(c.x-a.x)
	if area == 0 {
		return
	}
	if area < 0 {
		b, c = c, b
		area = -area
	}
	x0 := max(int(min(a.x, min(b.x, c.x))), 0)
	x1 := min(int(max(a.x, max(b.x, c.x)))+1, w)
	y0 := max(int(min(a.y, min(b.y, c.y))), bandY0)
	y1 := min(int(max(a.y, max(b.y, c.y)))+1, bandY1)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	inv := 1 / area
	pix := r.buf.Pix
	stride := r.buf.Stride
	// Each edge function is linear in px along a row, so the row's covered span is
	// where three lines cross it, and solving for that beats walking the bounding
	// box looking for it.
	//
	// This is what a real part makes expensive. A machined bracket is fat
	// triangles whose box is about their area; a lattice — a truss, printed
	// infill, the Eiffel tower — is thin diagonal struts, where the box is 20× the
	// triangle and 95% of the scan finds nothing. Measured on the tower at modal
	// size: 22M box pixels walked to fill 1.1M, at the *default* camera, and 686M
	// to fill 40M once zoomed in from above.
	//
	// The span is deliberately conservative — widened a pixel each side, with the
	// per-pixel edge tests below left exactly as they were — rather than exact. An
	// exact span would have to agree with those tests to the last bit of float
	// rounding on the boundary pixel, and getting that wrong is a seam down the
	// middle of a facet. This way the arithmetic that decides a pixel is
	// untouched; all that changes is how many pixels are offered to it.
	//
	// spanSafe is the other half of not trusting float32 too far. The crossings
	// are computed as a lerp along each edge, which is accurate to a thousandth of
	// a pixel while the vertices are anywhere near the frame — but a facet the
	// near plane clipped can project to coordinates in the millions, where
	// subtracting two of them leaves an error of many pixels and the span would be
	// wrong rather than merely loose. Those facets fall back to the box, which is
	// the frame for them anyway, and there are few of them.
	//
	// Skipping this cost one pixel of one facet at zoom 40, which is how it was
	// found: TestRasterSpanMatchesTheFullScan renders both ways and compares.
	narrow := max(abs32(a.x), max(abs32(b.x), abs32(c.x))) < spanSafe &&
		max(abs32(a.y), max(abs32(b.y), abs32(c.y))) < spanSafe
	d0, d1, d2 := b.y-a.y, c.y-b.y, a.y-c.y
	loLim, hiLim := float32(x0), float32(x1-1)
	for y := y0; y < y1; y++ {
		py := float32(y) + 0.5
		row := y * stride
		zrow := y * w

		rx0, rx1 := x0, x1
		if narrow {
			lo, hi := loLim, hiLim
			// Where each edge crosses this row. A positive d means the inside is
			// to the left of the crossing, a negative one to the right; the -0.5
			// converts a pixel centre back to a pixel index.
			if d0 > 0 {
				hi = min(hi, a.x+(py-a.y)*(b.x-a.x)/d0-0.5)
			} else if d0 < 0 {
				lo = max(lo, a.x+(py-a.y)*(b.x-a.x)/d0-0.5)
			} else if (b.x-a.x)*(py-a.y) < 0 {
				continue // horizontal edge with the row on its outside
			}
			if d1 > 0 {
				hi = min(hi, b.x+(py-b.y)*(c.x-b.x)/d1-0.5)
			} else if d1 < 0 {
				lo = max(lo, b.x+(py-b.y)*(c.x-b.x)/d1-0.5)
			} else if (c.x-b.x)*(py-b.y) < 0 {
				continue
			}
			if d2 > 0 {
				hi = min(hi, c.x+(py-c.y)*(a.x-c.x)/d2-0.5)
			} else if d2 < 0 {
				lo = max(lo, c.x+(py-c.y)*(a.x-c.x)/d2-0.5)
			} else if (a.x-c.x)*(py-c.y) < 0 {
				continue
			}
			// Clamped back into the box before the conversion: a float-to-int
			// conversion out of int range is not defined to do anything sensible.
			lo, hi = min(max(lo, loLim), hiLim), min(max(hi, loLim), hiLim)
			if hi < lo {
				continue
			}
			// One pixel of slack each side absorbs the rounding in the divisions,
			// so the span can only ever come out too wide.
			rx0, rx1 = max(int(lo)-1, x0), min(int(hi)+2, x1)
		}
		for x := rx0; x < rx1; x++ {
			px := float32(x) + 0.5
			// Barycentrics as edge functions; all three non-negative is inside.
			w0 := (b.x-a.x)*(py-a.y) - (b.y-a.y)*(px-a.x)
			if w0 < 0 {
				continue
			}
			w1 := (c.x-b.x)*(py-b.y) - (c.y-b.y)*(px-b.x)
			if w1 < 0 {
				continue
			}
			w2 := (a.x-c.x)*(py-c.y) - (a.y-c.y)*(px-c.x)
			if w2 < 0 {
				continue
			}
			// w1 weights a, w2 weights b, w0 weights c.
			z := (w1*a.invZ + w2*b.invZ + w0*c.invZ) * inv
			if z <= r.zbuf[zrow+x] {
				continue
			}
			r.zbuf[zrow+x] = z
			p := pix[row+x*4:]
			p[0], p[1], p[2], p[3] = col.R, col.G, col.B, col.A
		}
	}
}
