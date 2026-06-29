package vapor

import "math"

// Vaporwave / synthwave palette tuned to match the reference: near-black navy
// sky, a scan-lined magenta sun, and a cyan wireframe terrain with dark facets.
var (
	skyTop     = RGB{6, 4, 18}   // almost black overhead
	skyHorizon = RGB{18, 10, 46} // dark blue near the horizon
	fogColor   = RGB{16, 10, 42} // distant terrain fades into this
	starColor  = RGB{220, 220, 255}

	sunTopC = RGB{255, 120, 210} // lighter pink at the top of the sun
	sunBotC = RGB{206, 24, 140}  // deep magenta at the bottom
	sunGlow = RGB{255, 70, 150}  // halo

	faceLow  = RGB{24, 20, 66} // low terrain facets
	faceHigh = RGB{60, 30, 96} // high (peak) facets
	gridCyan = RGB{54, 236, 224}
	gridMag  = RGB{255, 52, 170} // sun reflection tint on the central road

	// sun-on-floor reflection: Phong over interpolated heightfield normals.
	// The specular falloff is rdotv^32, computed by 5 repeated squarings on the
	// per-pixel hot path in rasterFill (far cheaper than math.Pow). Strength is
	// kept below 1 so the additive highlight stays in a visible, non-clamping
	// range — a saturated streak hides the bump-driven shimmer.
	reflStrength = 0.5 // additive reflection intensity (var so tests can zero it)
)

// Scene renders one frame of the driving-through-mountains scene.
type Scene struct {
	W, H    int
	aspectY float64 // vertical pixel squash (1 for half-blocks, 0.5 for ascii cells)
	buf     []RGB
	zbuf    []float64 // inverse depth; larger = nearer

	horizonY, centerX, focal, camY float64

	// sun
	sunCX, sunCY, sunR float64
	sunYMul            float64 // vertical-position multiplier (1 = default, >1 higher)
	sunStops           []RGB   // gradient stops, top of the disk to bottom
	sunGlowC           RGB     // halo colour around the disk

	// terrain mesh
	M, N               int
	dx, dz, xSpan      float64
	maxH, vHalf, speed float64
	speedMul           float64 // user multiplier applied to the base speed
	heightMul          float64 // user multiplier applied to the peak height
	valleyMul          float64 // user multiplier applied to the valley width
	bumpMul            float64 // user multiplier applied to the valley-floor undulation
	fogStart, fogEnd   float64
	lx, ly, lz         float64 // normalized light direction

	// sun reflection (world space)
	sunDirX, sunDirY, sunDirZ float64   // unit dir from any surface point toward the sun
	camZ                      float64   // camera world z this frame (t*speed)
	wnx, wny, wnz             []float64 // per-vertex heightfield normals

	// per-frame projected vertex arrays, size (M+1)*(N+1)
	sx, sy, inv, dep []float64
	wpx, wpy, wpz    []float64
	valid            []bool

	// optional floating 3D text billboard (nil = none). textBase holds the
	// un-animated transform that animation tracks override per frame.
	text     *textOpts
	textBase textOpts

	// optional keyframe animation (nil = none), sampled at the top of Render.
	anim *Animation
}

// baseSpeed is the world-units-per-second the camera drives at speed multiplier
// 1. Both the static path (t·speed) and the animated path (∫ speed dt) use it.
const baseSpeed = 8.0

func NewScene(w, h int, aspectY, speedMul, heightMul, valleyMul, bumpMul float64, sunStops []RGB) *Scene {
	s := &Scene{aspectY: aspectY, speedMul: speedMul, heightMul: heightMul, valleyMul: valleyMul, bumpMul: bumpMul, sunYMul: 1.0}
	if len(sunStops) == 0 {
		// built-in magenta sun
		s.sunStops = []RGB{sunTopC, sunBotC}
		s.sunGlowC = sunGlow
	} else {
		s.sunStops = sunStops
		// halo picks up the bottom-most stop so it harmonises with the disk
		s.sunGlowC = sunStops[len(sunStops)-1]
	}
	s.Resize(w, h)
	return s
}

func (s *Scene) Resize(w, h int) {
	s.W, s.H = w, h
	s.zbuf = make([]float64, w*h)

	s.centerX = float64(w) / 2
	s.horizonY = float64(h) * 0.48
	s.focal = float64(w) * 0.66
	s.camY = 1.7

	s.sunR = float64(h) * 0.32
	s.sunCX = float64(w) / 2
	s.applySunY(s.sunYMul) // sets sunCY and the reflection's sun direction

	// Low-poly mesh: fewer, larger quads give the bold faceted look.
	s.M, s.N = 32, 52
	s.xSpan = 18
	s.dx = 2 * s.xSpan / float64(s.M)
	s.dz = 1.2
	s.maxH = 17 * math.Max(0, s.heightMul)
	// Keep the flat road strictly inside the mesh so the height envelope
	// denominator (xSpan-vHalf) stays positive and well-defined.
	s.vHalf = math.Min(math.Max(0, 3.0*s.valleyMul), s.xSpan*0.9)
	s.bumpMul = math.Max(0, s.bumpMul)
	s.speed = baseSpeed * s.speedMul
	s.fogStart = 20
	s.fogEnd = 58

	// light from upper-front-right, normalized
	s.lx, s.ly, s.lz = 0.45, 0.8, -0.4
	l := math.Sqrt(s.lx*s.lx + s.ly*s.ly + s.lz*s.lz)
	s.lx, s.ly, s.lz = s.lx/l, s.ly/l, s.lz/l

	v := (s.M + 1) * (s.N + 1)
	s.sx = make([]float64, v)
	s.sy = make([]float64, v)
	s.inv = make([]float64, v)
	s.dep = make([]float64, v)
	s.wpx = make([]float64, v)
	s.wpy = make([]float64, v)
	s.wpz = make([]float64, v)
	s.wnx = make([]float64, v)
	s.wny = make([]float64, v)
	s.wnz = make([]float64, v)
	s.valid = make([]bool, v)
}

// applySunY positions the sun vertically: mul scales the default elevation above
// the horizon (1 = default, >1 higher, <1 lower, 0 = on the horizon). It also
// recomputes the reflection's sun direction, which is inverse-projected from the
// sun's screen centre and so depends on sunCY. Called once per resize for the
// static position and per frame when a sun.y track is animating it.
func (s *Scene) applySunY(mul float64) {
	s.sunYMul = mul
	s.sunCY = s.horizonY - s.sunR*0.34*mul
	dx := (s.sunCX - s.centerX) / s.focal
	dy := (s.horizonY - s.sunCY) / (s.focal * s.aspectY)
	dl := math.Sqrt(dx*dx + dy*dy + 1)
	s.sunDirX, s.sunDirY, s.sunDirZ = dx/dl, dy/dl, 1/dl
}

// SetAnimation installs (or clears) the keyframe animation sampled each frame.
func (s *Scene) SetAnimation(a *Animation) { s.anim = a }

// distanceAt is the camera's world-z this frame. With a speed track it is the
// integral of speed over time (so a changing speed never teleports the terrain);
// otherwise it is the constant-speed t·speed.
func (s *Scene) distanceAt(t float64) float64 {
	if s.anim != nil {
		if d, ok := s.anim.speedDistance(t); ok {
			return baseSpeed * d
		}
	}
	return t * s.speed
}

// applyAnim samples the animation at time t and overrides the animated scene
// properties for this frame, leaving un-tracked properties at their static value.
func (s *Scene) applyAnim(t float64) {
	a := s.anim
	lt := a.localTime(t)
	if v, ok := evalTrack(a.f.Sun.Y, lt); ok {
		s.applySunY(v)
	}
	if s.text != nil {
		tx := s.textBase // fresh copy of the static transform
		if v, ok := evalTrack(a.f.Text.Pos.X, lt); ok {
			tx.x = v
		}
		if v, ok := evalTrack(a.f.Text.Pos.Y, lt); ok {
			tx.y = v
		}
		if v, ok := evalTrack(a.f.Text.Pos.Z, lt); ok {
			tx.z = v
		}
		if v, ok := evalTrack(a.f.Text.Rot.X, lt); ok {
			tx.rotX = v
		}
		if v, ok := evalTrack(a.f.Text.Rot.Y, lt); ok {
			tx.rotY = v
		}
		if v, ok := evalTrack(a.f.Text.Rot.Z, lt); ok {
			tx.rotZ = v
		}
		*s.text = tx
	}
}

// terrainHeight is a heightfield: a flat valley in the middle (the road) that
// ramps up into jagged peaks toward the left and right edges.
func (s *Scene) terrainHeight(wx, wz float64) float64 {
	ax := math.Abs(wx)
	e := clamp01((ax - s.vHalf) / (s.xSpan - s.vHalf))
	env := math.Pow(e, 1.5) * s.maxH
	r := ridgedFbm2(wx*0.17+11.1, wz*0.15+3.3, 4242)
	r = r * r // extra-sharp spiky peaks
	peaks := env * (0.20 + 0.80*r)
	bumps := (fbm2(wx*0.22, wz*0.18, 99) - 0.5) * 1.5 * s.bumpMul // valley-floor undulation
	return peaks + bumps
}

// Render paints the full scene into buf for time t (seconds).
func (s *Scene) Render(buf []RGB, t float64) {
	s.buf = buf
	if s.anim != nil {
		s.applyAnim(t)
	}
	W, H := s.W, s.H

	// 1. sky gradient + sun + stars, reset depth buffer to "infinitely far".
	// The sun is resolved first; stars are drawn only on open sky, so they can't
	// show through the disk's scanline gaps or wash into the glow.
	for y := 0; y < H; y++ {
		sky := s.skyColor(y)
		row := y * W
		starRow := float64(y) < s.horizonY*0.92
		for x := 0; x < W; x++ {
			c, openSky := s.sunAt(x, y, sky)
			if openSky && starRow && hash2(x, y) > 0.9962 {
				tw := 0.5 + 0.5*math.Sin(t*2.5+hash2(x*3+1, y*7+5)*6.2832)
				c = lerp(c, starColor, clamp01(tw))
			}
			buf[row+x] = c
			s.zbuf[row+x] = 0
		}
	}

	// 2. project the terrain vertices for this frame.
	D := s.distanceAt(t)
	s.camZ = D
	k0 := math.Floor(D / s.dz)
	M, N := s.M, s.N
	stride := M + 1
	fy := s.focal * s.aspectY
	// heightfield-normal finite-difference step: small relative to the bump
	// wavelengths (~5 world units) but well above noise quantization.
	const ne = 0.08
	for j := 0; j <= N; j++ {
		worldZ := (k0 + float64(j)) * s.dz
		camDepth := worldZ - D
		for i := 0; i <= M; i++ {
			idx := j*stride + i
			wx := -s.xSpan + float64(i)*s.dx
			wy := s.terrainHeight(wx, worldZ)
			s.wpx[idx], s.wpy[idx], s.wpz[idx] = wx, wy, worldZ
			// surface normal via central differences of the heightfield; up is +y.
			hx := (s.terrainHeight(wx+ne, worldZ) - s.terrainHeight(wx-ne, worldZ)) / (2 * ne)
			hz := (s.terrainHeight(wx, worldZ+ne) - s.terrainHeight(wx, worldZ-ne)) / (2 * ne)
			nl := math.Sqrt(hx*hx + 1 + hz*hz)
			s.wnx[idx] = -hx / nl
			s.wny[idx] = 1 / nl
			s.wnz[idx] = -hz / nl
			if camDepth > 0.25 {
				inv := 1.0 / camDepth
				s.sx[idx] = s.centerX + s.focal*wx*inv
				s.sy[idx] = s.horizonY - fy*(wy-s.camY)*inv
				s.inv[idx] = inv
				s.dep[idx] = camDepth
				s.valid[idx] = true
			} else {
				s.valid[idx] = false
			}
		}
	}

	// 3. filled facets (z-buffered).
	for j := 0; j < N; j++ {
		for i := 0; i < M; i++ {
			a := j*stride + i
			b := a + 1
			c := a + stride
			d := c + 1
			if !(s.valid[a] && s.valid[b] && s.valid[c] && s.valid[d]) {
				continue
			}
			s.fillTri(a, b, d)
			s.fillTri(a, d, c)
		}
	}

	// 4. cyan wireframe on top (z-buffered with a bias so lines sit on faces).
	for j := 0; j <= N; j++ {
		for i := 0; i <= M; i++ {
			idx := j*stride + i
			if !s.valid[idx] {
				continue
			}
			if i < M && s.valid[idx+1] {
				s.drawEdge(idx, idx+1)
			}
			if j < N && s.valid[idx+stride] {
				s.drawEdge(idx, idx+stride)
			}
		}
	}

	// 5. floating 3D text, depth-tested so terrain peaks can occlude it.
	if s.text != nil {
		s.renderText(t)
	}
}

func (s *Scene) skyColor(y int) RGB {
	ty := clamp01(float64(y) / s.horizonY)
	return lerp(skyTop, skyHorizon, ty*ty)
}

// sunAt resolves the background colour at a pixel: the sun disk (with CRT-style
// horizontal scanline gaps showing dark sky), its surrounding glow, or the plain
// sky. The returned bool is true only on open sky — outside both the disk and the
// glow — i.e. where a star may be drawn. dx is scaled by the sub-pixel aspect so
// the disk stays round on screen (in glyph mode sub-pixels are wider than tall,
// aspectY=3, so an unscaled circle in pixel space would render far too wide).
func (s *Scene) sunAt(x, y int, sky RGB) (RGB, bool) {
	dx := (float64(x) - s.sunCX) * s.aspectY
	dy := float64(y) - s.sunCY
	d2 := dx*dx + dy*dy
	r := s.sunR
	if d2 <= r*r {
		sy := (float64(y) - (s.sunCY - r)) / (2 * r) // 0 top .. 1 bottom
		period := math.Max(1.8, r*0.07)
		gap := 0.34 + 0.22*sy // scanline gaps thicken toward the bottom
		if math.Mod(float64(y), period)/period < gap {
			return sky, false // gap: dark sky shows through (no star)
		}
		return gradientAt(s.sunStops, sy), false
	}
	if d := math.Sqrt(d2) - r; d < r*0.5 {
		g := 1 - d/(r*0.5)
		return lerp(sky, s.sunGlowC, g*g*0.5), false
	}
	return sky, true // open sky: a star may be drawn here
}
