package game

import "math"

// The sprites, built the way GORILLA.BAS builds them: drawn once into a buffer
// with the QBasic primitives, then stamped every frame. DrawGorilla does exactly
// this — it draws the ape with LINE and CIRCLE and then GETs the result into an
// array, and PlaceGorillas only ever PUTs that array. Doing the same here means
// the ape on screen is the one the original's geometry describes rather than a
// blocky guess at it.

// A sprite pixel is an EGA attribute, or transparent.
//
// Transparent is not attribute 0: the gorilla's brow, nose and chest are drawn in
// colour 0, which is the *sky* — that dark blue is how the ape gets its face —
// and those pixels must be painted, while the empty corners of its box must not.
// (The original PUTs the whole 30×30 box with PSET and so does punch a
// sky-coloured rectangle through anything standing behind the gorilla. That is a
// bug, not a look, and it is not reproduced here.)
const transparent = 0xff

type canvas struct {
	w, h int
	px   []uint8
}

func newCanvas(w, h int) *canvas {
	c := &canvas{w: w, h: h, px: make([]uint8, w*h)}
	for i := range c.px {
		c.px[i] = transparent
	}
	return c
}

func (c *canvas) at(x, y int) uint8 {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return transparent
	}
	return c.px[y*c.w+x]
}

// pset is PSET: paint one pixel, silently ignoring anything off the buffer, as
// QBasic's own clipping does.
func (c *canvas) pset(x, y int, v uint8) {
	if x >= 0 && y >= 0 && x < c.w && y < c.h {
		c.px[y*c.w+x] = v
	}
}

// rect is LINE (x0,y0)-(x1,y1), c, BF — a filled box, corners inclusive.
func (c *canvas) rect(x0, y0, x1, y1 int, v uint8) {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			c.pset(x, y, v)
		}
	}
}

// line is LINE (x0,y0)-(x1,y1), c — Bresenham, as QBasic draws it.
func (c *canvas) line(x0, y0, x1, y1 int, v uint8) {
	dx, dy := iabs(x1-x0), -iabs(y1-y0)
	sx, sy := isign(x1-x0), isign(y1-y0)
	err := dx + dy
	for {
		c.pset(x0, y0, v)
		if x0 == x1 && y0 == y1 {
			return
		}
		if 2*err >= dy {
			err += dy
			x0 += sx
		} else {
			err += dx
			y0 += sy
		}
	}
}

// arc is CIRCLE (cx,cy), r, c, start, end, aspect.
//
// QBasic's CIRCLE is an ellipse: aspect is the ratio of the y radius to the x
// radius, and whichever of the two is the larger is the one r names. In SCREEN 9
// the default aspect is defAspect — a 640×350 frame buffer on a 4:3 monitor has
// pixels taller than they are wide, so a "circle" is a squat ellipse in pixel
// space. Every radius in the game is therefore drawn narrower than it is tall,
// and reproducing that is most of why the sun and the explosions read as the
// original's rather than as clip art.
//
// Angles run counterclockwise from the +x axis, and QBasic plots them against a
// downward y, so a bottom arc — the sun's smile — is 210° to 330°.
func (c *canvas) arc(cx, cy int, r, aspect, start, end float64, v uint8) {
	rx, ry := radii(r, aspect)
	if end <= start {
		end += 2 * math.Pi // CIRCLE wraps through 0, which is how 315°→45° is written
	}
	// Step finely enough that the curve comes out connected at any radius.
	steps := max(int(4*math.Max(rx, ry)), 8)
	for i := 0; i <= steps; i++ {
		a := start + (end-start)*float64(i)/float64(steps)
		c.pset(int(math.Round(float64(cx)+rx*math.Cos(a))),
			int(math.Round(float64(cy)-ry*math.Sin(a))), v)
	}
}

// disc is CIRCLE followed by PAINT: a filled ellipse.
func (c *canvas) disc(cx, cy int, r, aspect float64, v uint8) {
	rx, ry := radii(r, aspect)
	for y := -int(math.Ceil(ry)); y <= int(math.Ceil(ry)); y++ {
		for x := -int(math.Ceil(rx)); x <= int(math.Ceil(rx)); x++ {
			if ellipseDist(float64(x), float64(y), rx, ry) <= 1 {
				c.pset(cx+x, cy+y, v)
			}
		}
	}
}

// radii splits a QBasic radius and aspect into an x and a y radius. A negative
// aspect — which is what ExplodeGorilla passes — falls through the same branch a
// small positive one does, so r stays the x radius and the blast comes out tall.
func radii(r, aspect float64) (rx, ry float64) {
	if aspect > 1 {
		return r / aspect, r
	}
	return r, r * math.Abs(aspect)
}

// ellipseDist is the squared normalised distance from an ellipse's centre: 1 on
// the rim, less inside.
func ellipseDist(dx, dy, rx, ry float64) float64 {
	if rx <= 0 || ry <= 0 {
		return math.Inf(1)
	}
	return (dx*dx)/(rx*rx) + (dy*dy)/(ry*ry)
}

// defAspect is CIRCLE's default in SCREEN 9: 4/3 ÷ (640/350), the correction that
// makes a circle look round on a 4:3 monitor.
const defAspect = 4.0 / 3.0 * FieldH / FieldW

// ---------------------------------------------------------------------------
// The banana

// The four poses, decoded from the EGABanana DATA statements. Those are QBasic
// GET buffers — a 16-bit width and height followed by four interleaved bit planes
// per row — so the shapes are not readable from the DATA and are spelled out
// here instead:
//
//	BananaLeft   DATA 458758, 202116096, 471604224, 943208448, …    → 6×7
//	BananaDown   DATA 262153, -2134835200, -2134802239, …           → 9×4
//	BananaUp     DATA 262153, 4063232, 4063294, 8323072, …          → 9×4
//	BananaRight  DATA 458758, -1061109760, -522133504, …            → 6×7
//
// Every set pixel decodes to attribute 14, so the whole banana is one flat
// yellow. The order is DrawBan's: its SELECT CASE maps rotation 0,1,2,3 onto
// left, up, down, right — which is not a rotation at all but a tumble, and is why
// the banana in flight flickers rather than spins.
var bananaPoses = [4][]string{
	{ // 0 — left
		"....##",
		"...###",
		"..###.",
		"..###.",
		"..###.",
		"...###",
		"....##",
	},
	{ // 1 — up
		"..#####..",
		".#######.",
		"#########",
		"##.....##",
	},
	{ // 2 — down
		"##.....##",
		"#########",
		".#######.",
		"..#####..",
	},
	{ // 3 — right
		"##....",
		"###...",
		".###..",
		".###..",
		".###..",
		"###...",
		"##....",
	},
}

// bananaRot is PlotShot's `rot = (t# * 10) MOD 4`: the pose advances every tenth
// of a simulated second, whatever the frame rate.
func bananaRot(t float64) int {
	r := int(t*10) % 4
	if r < 0 {
		r = 0
	}
	return r
}

// ---------------------------------------------------------------------------
// The gorilla

// The ape's GET box, and DrawGorilla's origin inside it: GorillaIntro grabs
// (x-15, y-1)-(x+14, y+28), and PlaceGorillas PUTs that box at the gorilla's
// world position. So every offset in DrawGorilla can be used verbatim as long as
// the drawing starts at (15, 1).
const (
	gorillaBoxW, gorillaBoxH = 30, 30
	gorillaOriginX           = 15
	gorillaOriginY           = 1
)

// The three poses. The numbering is the original's RIGHTUP/LEFTUP/ARMSDOWN
// constants, which PlotShot uses to raise the thrower's arm.
type gorillaPose int

const (
	rightUp  gorillaPose = 1
	leftUp   gorillaPose = 2
	armsDown gorillaPose = 3
)

var gorillaSprites = map[gorillaPose]*canvas{
	rightUp:  buildGorilla(rightUp),
	leftUp:   buildGorilla(leftUp),
	armsDown: buildGorilla(armsDown),
}

// buildGorilla is DrawGorilla. Scl() is the identity in mode 9,
// so its arguments — which carry a trailing .9 only to round the other way when
// halved for CGA — are just rounded here.
func buildGorilla(arms gorillaPose) *canvas {
	const (
		obj  = 1 // OBJECTCOLOR
		dark = 0 // the sky, which is what gives the ape its face
	)
	c := newCanvas(gorillaBoxW, gorillaBoxH)
	x, y := gorillaOriginX, gorillaOriginY

	// Head.
	c.rect(x-4, y, x+3, y+6, obj)   // Scl(2.9) = 3
	c.rect(x-5, y+2, x+4, y+4, obj) //
	c.line(x-3, y+2, x+2, y+2, dark)
	for i := -2; i <= -1; i++ { // the nose, two nostrils
		c.pset(x+i, y+4, dark)
		c.pset(x+i+3, y+4, dark)
	}

	c.line(x-3, y+7, x+2, y+7, obj) // neck

	// Body.
	c.rect(x-8, y+8, x+7, y+14, obj)  // Scl(6.9) = 7
	c.rect(x-6, y+15, x+5, y+20, obj) // Scl(4.9) = 5

	// Legs: five overlapping arcs each, swept sideways, which is what gives them
	// their thickness and their bow.
	for i := range 5 {
		c.arc(x+i, y+25, 10, defAspect, 3*math.Pi/4, 9*math.Pi/8, obj)
		c.arc(x-6+i, y+25, 10, defAspect, 15*math.Pi/8, math.Pi/4, obj)
	}

	// Chest: two dark arcs, the pectorals.
	c.arc(x-5, y+10, 5, defAspect, 3*math.Pi/2, 2*math.Pi, dark)
	c.arc(x+5, y+10, 5, defAspect, math.Pi, 3*math.Pi/2, dark)

	// Arms, likewise five arcs each. A raised arm is the same arc twelve pixels
	// further up the body.
	leftY, rightY := y+14, y+14
	switch arms {
	case rightUp:
		rightY = y + 4
	case leftUp:
		leftY = y + 4
	}
	for i := -5; i <= -1; i++ {
		c.arc(x+i, leftY, 9, defAspect, 3*math.Pi/4, 5*math.Pi/4, obj)
		c.arc(x+5+i, rightY, 9, defAspect, 7*math.Pi/4, math.Pi/4, obj)
	}
	return c
}

// gorillaSolid reports whether the ape's own pixels — not the empty corners of
// its box — cover a point in it. This is the collision surface, so a banana has to
// actually touch the gorilla to kill it, the way the original's POINT test
// against OBJECTCOLOR did.
func gorillaSolid(bx, by int) bool {
	v := gorillaSprites[armsDown].at(bx, by)
	return v != transparent
}

// ---------------------------------------------------------------------------
// The sun

// The sun's box is the one DoSun clears before redrawing: (x±22, y±18).
const (
	sunCX, sunCY           = FieldW / 2, 25 // DoSun: x = ScrWidth \ 2, y = Scl(25)
	sunBoxW, sunBoxH       = 45, 37
	sunOriginX, sunOriginY = 22, 18

	// SunHt: above this line the sun is the only thing a banana can be touching.
	sunHt = 39
)

var (
	sunHappy   = buildSun(false)
	sunShocked = buildSun(true)
)

// buildSun is DoSun. The rays are eight lines through the centre — sixteen spokes —
// and the face is drawn in colour 0, so it reads as holes punched through to the
// sky.
func buildSun(shocked bool) *canvas {
	const (
		sun  = 3 // SUNATTR
		dark = 0
	)
	c := newCanvas(sunBoxW, sunBoxH)
	x, y := sunOriginX, sunOriginY

	c.disc(x, y, 12, defAspect, sun)

	for _, r := range [][4]int{
		{-20, 0, 20, 0},
		{0, -15, 0, 15},
		{-15, -10, 15, 10}, {-15, 10, 15, -10},
		{-8, -13, 8, 13}, {-8, 13, 8, -13},
		{-18, -5, 18, 5}, {-18, 5, 18, -5},
	} {
		c.line(x+r[0], y+r[1], x+r[2], y+r[3], sun)
	}

	if shocked {
		c.disc(x, y+5, 3, defAspect, dark) // Scl(2.9) = 3 — an "o" of alarm
	} else {
		c.arc(x, y, 8, defAspect, 210*math.Pi/180, 330*math.Pi/180, dark)
	}

	for _, ex := range []int{-3, 3} {
		c.arc(x+ex, y-2, 1, defAspect, 0, 2*math.Pi, dark)
		c.pset(x+ex, y-2, dark)
	}
	return c
}

// sunSolid reports whether a field point is on the sun itself — its disc or its
// rays, but not the sky-coloured face, exactly as the original's POINT = SUNATTR
// test behaved. A banana that touches it does not explode; the sun just takes it
// badly.
func sunSolid(fx, fy int) bool {
	if fy >= sunHt {
		return false
	}
	return sunHappy.at(fx-sunCX+sunOriginX, fy-sunCY+sunOriginY) == 3
}

func iabs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func isign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}
