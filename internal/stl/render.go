package stl

import (
	"image"
	"image/color"
	"math"
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
type Renderer struct {
	out  *image.RGBA // the returned image, at the requested size
	buf  *image.RGBA // the supersampled buffer (aliases out when SSAA is 1)
	zbuf []float32   // reciprocal depth, parallel to buf's pixels
	ss   int
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
	r.resize(pxW, pxH, ss)
	r.clear(st.Back)
	if m != nil && len(m.Tris) > 0 {
		r.draw(m, c.Clamp(), st)
	}
	r.resolve()
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

func (r *Renderer) clear(back color.RGBA) {
	pix := r.buf.Pix
	// Premultiplied, because image.RGBA is: a translucent background must not
	// carry colour, or the terminal composites a tinted haze over its own.
	back = premul(back)
	for i := 0; i < len(pix); i += 4 {
		pix[i], pix[i+1], pix[i+2], pix[i+3] = back.R, back.G, back.B, back.A
	}
	for i := range r.zbuf {
		r.zbuf[i] = 0 // reciprocal depth: 0 is infinitely far
	}
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
func (r *Renderer) resolve() {
	if r.ss == 1 {
		return
	}
	ss := r.ss
	n := uint32(ss * ss)
	w, h := r.out.Rect.Dx(), r.out.Rect.Dy()
	for y := range h {
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

// draw transforms, shades and rasterizes every facet.
func (r *Renderer) draw(m *Mesh, c Camera, st Style) {
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

	// Back faces can be dropped only while the eye is outside the model — the
	// whole argument for it is that a front face is always in the way, and from
	// inside there isn't one. The top of the zoom range flies through the surface
	// (see MaxZoom and TestRenderInsideTheModel), and from in there the only thing
	// to look at *is* the inside of the far wall.
	cull := m.Closed && dist > radius

	var poly [4]Vec3 // near-clipping a triangle yields at most a quad
	for i := range m.Tris {
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
		n := b.sub(a).cross(cc.sub(a)).norm()
		if n.Z < 0 {
			if cull {
				continue
			}
			n = Vec3{-n.X, -n.Y, -n.Z}
		}
		col := shade(st.Base, n)

		verts, ok := clipNear(&poly, a, b, cc, near)
		if !ok {
			continue
		}
		// Fan-triangulate whatever the clip left (3 or 4 vertices).
		for k := 1; k+1 < verts; k++ {
			r.tri(project(poly[0], focal, cx, cy), project(poly[k], focal, cx, cy),
				project(poly[k+1], focal, cx, cy), col)
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
func (r *Renderer) tri(a, b, c screenPt, col color.RGBA) {
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
	w, h := r.buf.Rect.Dx(), r.buf.Rect.Dy()
	x0 := max(int(min(a.x, min(b.x, c.x))), 0)
	x1 := min(int(max(a.x, max(b.x, c.x)))+1, w)
	y0 := max(int(min(a.y, min(b.y, c.y))), 0)
	y1 := min(int(max(a.y, max(b.y, c.y)))+1, h)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	inv := 1 / area
	pix := r.buf.Pix
	stride := r.buf.Stride
	for y := y0; y < y1; y++ {
		py := float32(y) + 0.5
		row := y * stride
		zrow := y * w
		for x := x0; x < x1; x++ {
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
