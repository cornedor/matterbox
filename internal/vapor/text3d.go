package vapor

import "math"

// textOpts configures the floating 3D text object. A nil pointer (or empty
// runes) disables it. The text is an extruded block of glyphs placed in world
// space: x/y are its centre, z is its depth *ahead of the camera* (so it keeps a
// stable position on screen as the camera drives forward), and it can be rotated
// about its own centre.
type textOpts struct {
	runes               []rune
	x, y, z             float64
	scale               float64 // size multiplier (1 = default)
	depth               float64 // extrusion depth in world units at scale 1 (0 = flat)
	rotX, rotY, rotZ    float64 // base rotation about each axis, in degrees (pitch, yaw, roll)
	spinX, spinY, spinZ float64 // rotation speed about each axis, in degrees/second
	stops               []RGB   // vertical colour gradient, top row to bottom row
}

// Font cell metrics. Glyphs are 5×7 with one column of spacing between them.
const (
	fontW       = 5
	fontH       = 7
	fontAdvance = fontW + 1
	textCell    = 0.35 // world units per font pixel at scale 1
	deg2rad     = math.Pi / 180
)

// SetText installs (or clears) the floating 3D text, filling in defaults for an
// unset scale and an empty gradient. Pass nil or empty text to disable.
func (s *Scene) SetText(t *textOpts) {
	if t == nil || len(t.runes) == 0 {
		s.text = nil
		return
	}
	cp := *t
	if cp.scale <= 0 {
		cp.scale = 1
	}
	if len(cp.stops) == 0 {
		// built-in vaporwave gradient: cyan crown fading to hot pink.
		cp.stops = []RGB{{99, 246, 255}, {255, 92, 198}}
	}
	s.textBase = cp // un-animated transform that animation tracks override
	s.text = &cp
}

// renderText rasterizes the extruded, rotatable 3D text into the framebuffer,
// depth-tested against the terrain so peaks can occlude it. Each lit font pixel
// is a small cell on one of several depth slices that stack along the glyphs'
// local z to give thickness; the whole block is rotated about its own centre by
// the configured pitch/yaw/roll. Because rotation tilts the slices, every cell
// can sit at its own depth, so the four corners are projected individually and
// filled as two flat triangles. Deeper slices are shaded darker, which (together
// with the perspective spread of the corners) reads as solid extruded letters.
// The z-buffer keeps the nearest fragment per pixel, so slice order is free.
// now is the frame time in seconds, driving any configured spin.
func (s *Scene) renderText(now float64) {
	t := s.text
	if t.z <= 0.25 { // anchor at or behind the near plane: nothing to draw
		return
	}
	cell := textCell * t.scale
	half := 0.5 * cell
	depth := t.depth * t.scale

	// Rotation matrix R = Rz·Ry·Rx, applied to each cell's local offset from the
	// text centre before translating to the anchor and projecting. The angle on
	// each axis is the base rotation plus the spin accumulated over time.
	angX := (t.rotX + t.spinX*now) * deg2rad
	angY := (t.rotY + t.spinY*now) * deg2rad
	angZ := (t.rotZ + t.spinZ*now) * deg2rad
	cosX, sinX := math.Cos(angX), math.Sin(angX)
	cosY, sinY := math.Cos(angY), math.Sin(angY)
	cosZ, sinZ := math.Cos(angZ), math.Sin(angZ)
	r00 := cosZ * cosY
	r01 := cosZ*sinY*sinX - sinZ*cosX
	r02 := cosZ*sinY*cosX + sinZ*sinX
	r10 := sinZ * cosY
	r11 := sinZ*sinY*sinX + cosZ*cosX
	r12 := sinZ*sinY*cosX - cosZ*sinX
	r20 := -sinY
	r21 := cosY * sinX
	r22 := cosY * cosX
	fy := s.focal * s.aspectY

	// project maps a local offset (lx,ly,lz) from the text centre to a screen
	// point and its camera depth; ok is false if it falls behind the near plane.
	project := func(lx, ly, lz float64) (px, py, camDepth float64, ok bool) {
		vx := r00*lx + r01*ly + r02*lz
		vy := r10*lx + r11*ly + r12*lz
		vz := r20*lx + r21*ly + r22*lz
		camDepth = t.z + vz
		if camDepth <= 0.25 {
			return 0, 0, camDepth, false
		}
		inv := 1.0 / camDepth
		px = s.centerX + s.focal*(t.x+vx)*inv
		py = s.horizonY - fy*((t.y+vy)-s.camY)*inv
		return px, py, camDepth, true
	}

	// Local frame: x right, y up, z into the screen; the extrusion is centred on
	// the anchor (front face at -depth/2) so rotation pivots about the centroid.
	stringW := float64(len(t.runes)*fontAdvance) * cell
	localStartX := -stringW/2 + half
	zFront := -depth / 2

	slices := textSlices(project, stringW/2+half, float64(fontH)*cell/2+half, zFront, depth)

	// Back-to-front: deeper slices are darker and recede toward the horizon.
	for si := slices - 1; si >= 0; si-- {
		f := 0.0
		lz := zFront
		if slices > 1 {
			f = float64(si) / float64(slices-1)
			lz = zFront + f*depth
		}
		shade := 1.0 - 0.65*f // front face full bright, back wall dark
		for ci, r := range t.runes {
			g := glyph5x7(r)
			baseLX := localStartX + float64(ci*fontAdvance)*cell
			for gy := 0; gy < fontH; gy++ {
				bitsRow := g[gy]
				if bitsRow == 0 {
					continue
				}
				// Row 0 is the top; local y runs up, so higher rows get larger y.
				ly := (float64(fontH-1)*0.5 - float64(gy)) * cell
				col := scale(gradientAt(t.stops, float64(gy)/float64(fontH-1)), shade)
				for gx := 0; gx < fontW; gx++ {
					if bitsRow&(1<<(fontW-1-gx)) == 0 {
						continue
					}
					lx := baseLX + float64(gx)*cell
					p0x, p0y, d0, o0 := project(lx-half, ly-half, lz)
					p1x, p1y, d1, o1 := project(lx+half, ly-half, lz)
					p2x, p2y, d2, o2 := project(lx+half, ly+half, lz)
					p3x, p3y, d3, o3 := project(lx-half, ly+half, lz)
					if !(o0 && o1 && o2 && o3) {
						continue
					}
					z := 4.0 / (d0 + d1 + d2 + d3) // inverse depth at the cell centre
					s.fillFlatTri(p0x, p0y, p1x, p1y, p2x, p2y, z, col)
					s.fillFlatTri(p0x, p0y, p2x, p2y, p3x, p3y, z, col)
				}
			}
		}
	}
}

// textSlices picks how many depth slices the extrusion needs so the receding
// body stays gap-free: enough that each corner of the text box moves under ~1px
// on screen between adjacent slices, accounting for the current rotation.
// Returns 1 for flat text (depth ≤ 0).
func textSlices(project func(lx, ly, lz float64) (float64, float64, float64, bool), exX, exY, zFront, depth float64) int {
	if depth <= 0 {
		return 1
	}
	maxD := 0.0
	for _, sx := range []float64{-1, 1} {
		for _, sy := range []float64{-1, 1} {
			fxF, fyF, _, okF := project(sx*exX, sy*exY, zFront)
			fxB, fyB, _, okB := project(sx*exX, sy*exY, zFront+depth)
			if okF && okB {
				if d := math.Hypot(fxF-fxB, fyF-fyB); d > maxD {
					maxD = d
				}
			}
		}
	}
	ns := int(math.Ceil(maxD / 0.7))
	if ns < 1 {
		ns = 1
	}
	if ns > 1024 {
		ns = 1024
	}
	return ns
}

// fillFlatTri rasterizes screen-space triangle (0,1,2) with a flat colour at a
// constant inverse depth z, depth-tested and clipped to the framebuffer.
func (s *Scene) fillFlatTri(x0, y0, x1, y1, x2, y2, z float64, col RGB) {
	minX := int(math.Floor(min3(x0, x1, x2)))
	maxX := int(math.Ceil(max3(x0, x1, x2)))
	minY := int(math.Floor(min3(y0, y1, y2)))
	maxY := int(math.Ceil(max3(y0, y1, y2)))
	if minX < 0 {
		minX = 0
	}
	if minY < 0 {
		minY = 0
	}
	if maxX > s.W-1 {
		maxX = s.W - 1
	}
	if maxY > s.H-1 {
		maxY = s.H - 1
	}
	if minX > maxX || minY > maxY {
		return
	}
	det := (y1-y2)*(x0-x2) + (x2-x1)*(y0-y2)
	if math.Abs(det) < 1e-9 {
		return
	}
	inv := 1.0 / det
	for py := minY; py <= maxY; py++ {
		fy := float64(py) + 0.5
		row := py * s.W
		for px := minX; px <= maxX; px++ {
			fx := float64(px) + 0.5
			w0 := ((y1-y2)*(fx-x2) + (x2-x1)*(fy-y2)) * inv
			w1 := ((y2-y0)*(fx-x2) + (x0-x2)*(fy-y2)) * inv
			w2 := 1 - w0 - w1
			if w0 < 0 || w1 < 0 || w2 < 0 {
				continue
			}
			if z > s.zbuf[row+px] {
				s.zbuf[row+px] = z
				s.buf[row+px] = col
			}
		}
	}
}

// glyph5x7 returns the 7-row, 5-bit-wide bitmap for a rune (bit 4 = leftmost
// column). Lowercase maps to uppercase; unknown runes render blank.
func glyph5x7(r rune) [fontH]uint8 {
	if r >= 'a' && r <= 'z' {
		r -= 'a' - 'A'
	}
	if g, ok := font5x7[r]; ok {
		return g
	}
	return [fontH]uint8{}
}

// font5x7 is a compact 5×7 bitmap font covering the characters useful for a
// title: A–Z, 0–9, space, and common punctuation. Each row's low 5 bits are the
// pixels, leftmost column in bit 4.
var font5x7 = map[rune][fontH]uint8{
	' ':  {0, 0, 0, 0, 0, 0, 0},
	'A':  {0b01110, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'B':  {0b11110, 0b10001, 0b10001, 0b11110, 0b10001, 0b10001, 0b11110},
	'C':  {0b01110, 0b10001, 0b10000, 0b10000, 0b10000, 0b10001, 0b01110},
	'D':  {0b11110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b11110},
	'E':  {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b11111},
	'F':  {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b10000},
	'G':  {0b01110, 0b10001, 0b10000, 0b10111, 0b10001, 0b10001, 0b01111},
	'H':  {0b10001, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'I':  {0b01110, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'J':  {0b00111, 0b00010, 0b00010, 0b00010, 0b00010, 0b10010, 0b01100},
	'K':  {0b10001, 0b10010, 0b10100, 0b11000, 0b10100, 0b10010, 0b10001},
	'L':  {0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b11111},
	'M':  {0b10001, 0b11011, 0b10101, 0b10101, 0b10001, 0b10001, 0b10001},
	'N':  {0b10001, 0b10001, 0b11001, 0b10101, 0b10011, 0b10001, 0b10001},
	'O':  {0b01110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'P':  {0b11110, 0b10001, 0b10001, 0b11110, 0b10000, 0b10000, 0b10000},
	'Q':  {0b01110, 0b10001, 0b10001, 0b10001, 0b10101, 0b10010, 0b01101},
	'R':  {0b11110, 0b10001, 0b10001, 0b11110, 0b10100, 0b10010, 0b10001},
	'S':  {0b01111, 0b10000, 0b10000, 0b01110, 0b00001, 0b00001, 0b11110},
	'T':  {0b11111, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100},
	'U':  {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'V':  {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01010, 0b00100},
	'W':  {0b10001, 0b10001, 0b10001, 0b10101, 0b10101, 0b11011, 0b10001},
	'X':  {0b10001, 0b10001, 0b01010, 0b00100, 0b01010, 0b10001, 0b10001},
	'Y':  {0b10001, 0b10001, 0b01010, 0b00100, 0b00100, 0b00100, 0b00100},
	'Z':  {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b10000, 0b11111},
	'0':  {0b01110, 0b10001, 0b10011, 0b10101, 0b11001, 0b10001, 0b01110},
	'1':  {0b00100, 0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'2':  {0b01110, 0b10001, 0b00001, 0b00110, 0b01000, 0b10000, 0b11111},
	'3':  {0b11111, 0b00010, 0b00100, 0b00010, 0b00001, 0b10001, 0b01110},
	'4':  {0b00010, 0b00110, 0b01010, 0b10010, 0b11111, 0b00010, 0b00010},
	'5':  {0b11111, 0b10000, 0b11110, 0b00001, 0b00001, 0b10001, 0b01110},
	'6':  {0b00110, 0b01000, 0b10000, 0b11110, 0b10001, 0b10001, 0b01110},
	'7':  {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b01000, 0b01000},
	'8':  {0b01110, 0b10001, 0b10001, 0b01110, 0b10001, 0b10001, 0b01110},
	'9':  {0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00010, 0b01100},
	'.':  {0, 0, 0, 0, 0, 0b01100, 0b01100},
	',':  {0, 0, 0, 0, 0, 0b00100, 0b01000},
	'!':  {0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0, 0b00100},
	'?':  {0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0, 0b00100},
	'-':  {0, 0, 0, 0b11111, 0, 0, 0},
	'_':  {0, 0, 0, 0, 0, 0, 0b11111},
	':':  {0, 0b00100, 0, 0, 0, 0b00100, 0},
	';':  {0, 0b00100, 0, 0, 0, 0b00100, 0b01000},
	'\'': {0b00100, 0b00100, 0b01000, 0, 0, 0, 0},
	'"':  {0b01010, 0b01010, 0b01010, 0, 0, 0, 0},
	'/':  {0b00001, 0b00010, 0b00100, 0b00100, 0b01000, 0b10000, 0b10000},
	'\\': {0b10000, 0b01000, 0b00100, 0b00100, 0b00010, 0b00001, 0b00001},
	'(':  {0b00010, 0b00100, 0b01000, 0b01000, 0b01000, 0b00100, 0b00010},
	')':  {0b01000, 0b00100, 0b00010, 0b00010, 0b00010, 0b00100, 0b01000},
	'&':  {0b01100, 0b10010, 0b10100, 0b01000, 0b10101, 0b10010, 0b01101},
	'*':  {0, 0b00100, 0b10101, 0b01110, 0b10101, 0b00100, 0},
	'+':  {0, 0b00100, 0b00100, 0b11111, 0b00100, 0b00100, 0},
	'=':  {0, 0, 0b11111, 0, 0b11111, 0, 0},
	'<':  {0b00010, 0b00100, 0b01000, 0b10000, 0b01000, 0b00100, 0b00010},
	'>':  {0b01000, 0b00100, 0b00010, 0b00001, 0b00010, 0b00100, 0b01000},
}
