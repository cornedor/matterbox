package game

import (
	"image"
	"image/color"
	"math"
)

// Render draws the world at the given pixel size.
//
// The image goes to the terminal through the Kitty graphics protocol, the same
// path the inline thumbnails and the image-preview modal already use. That
// matters for more than fidelity: a kitty frame is transmitted out of band under
// a fixed image id, so re-sending it repaints the screen without the TUI
// re-rendering at all. A banana in flight therefore costs nothing on View()'s
// hot path — which a character-cell renderer could not have managed, since every
// frame would have been a full re-render of the 133KB Model.
//
// scale maps the fixed FieldW×FieldH simulation space onto whatever pixel box
// the modal was given, so the simulation never learns what size the terminal is.
// A field unit is one of the original's pixels, and everything below is drawn in
// those — which is what lets the geometry be lifted from GORILLA.BAS unchanged.
func Render(w *World, s *Shot, boom *Explosion, dance *Dance, pxW, pxH int) *image.RGBA {
	var r Renderer
	return r.Render(w, s, boom, dance, pxW, pxH)
}

// Renderer holds the frame buffer across frames. A banana in flight is redrawn
// 30 times a second and the buffer is well over a megabyte; allocating a fresh
// one per frame would hand the GC ~40MB/s for no reason.
//
// Not safe for concurrent use: one Renderer belongs to one open game modal.
type Renderer struct {
	img *image.RGBA

	// rowMap/colMap map a screen pixel back to its field unit. Kept alongside the
	// buffer because they depend only on the placement size, which changes on
	// resize, not on every frame.
	rowMap, colMap []int
}

// ensureMaps rebuilds the screen→field lookup tables when the placement changes.
func (r *Renderer) ensureMaps(pxW, pxH int, sx, sy float64) {
	if len(r.colMap) != pxW {
		r.colMap = make([]int, pxW)
		for px := range r.colMap {
			r.colMap[px] = int(float64(px) / sx)
		}
	}
	if len(r.rowMap) != pxH {
		r.rowMap = make([]int, pxH)
		for py := range r.rowMap {
			r.rowMap[py] = int(float64(py) / sy)
		}
	}
}

// Render draws the world into the Renderer's buffer and returns it. The result
// is only valid until the next call — the caller encodes it (to PNG, for the
// kitty transmit) and does not hold on to it.
func (r *Renderer) Render(w *World, s *Shot, boom *Explosion, dance *Dance, pxW, pxH int) *image.RGBA {
	if r.img == nil || r.img.Rect.Dx() != pxW || r.img.Rect.Dy() != pxH {
		r.img = image.NewRGBA(image.Rect(0, 0, pxW, pxH))
	}
	img := r.img
	sx := float64(pxW) / FieldW
	sy := float64(pxH) / FieldH

	// Sky.
	for i := range pxW * pxH {
		img.Pix[i*4+0] = colSky.R
		img.Pix[i*4+1] = colSky.G
		img.Pix[i*4+2] = colSky.B
		img.Pix[i*4+3] = 0xff
	}
	drawSun(img, w.SunHit, sx, sy)

	// Masonry, straight off the occupancy bitmap — so what is drawn and what a
	// banana can hit are by construction the same thing, craters included.
	//
	// The screen→field mapping is precomputed per row and per column rather than
	// divided out per pixel: at 350k pixels that is 700k float divisions a frame,
	// which was the bulk of the render cost.
	w.ensureSolid()
	r.ensureMaps(pxW, pxH, sx, sy)
	for py := range pxH {
		fy := r.rowMap[py]
		if fy < 0 || fy >= FieldH {
			continue
		}
		row := w.solid[fy*FieldW:]
		for px := range pxW {
			fx := r.colMap[px]
			if fx < 0 || fx >= FieldW || !row[fx] {
				continue
			}
			set(img, px, py, buildingColorAt(w, fx, fy))
		}
	}

	drawWind(img, w.Wind, sx, sy)

	// An ape has three poses and the game only ever wants one of them at a time:
	// arms up in triumph, one arm up mid-throw, or nothing much.
	//
	// The thrower holds their arm up for the first moments of the flight, then
	// drops it — PlotShot raises it, plays the throw, rests a tenth of a second and
	// PUTs the ape back down.
	for i, g := range w.Gorillas {
		if w.Dead[i] {
			continue
		}
		pose := armsDown
		switch {
		case dance != nil && dance.Player == i:
			pose = dance.pose()
		case s != nil && s.Player == i && s.T < throwPoseTime:
			pose = throwPose(i)
		}
		stampSprite(img, gorillaSprites[pose], g.X, g.Y, gorillaColors, sx, sy)
	}

	if s != nil {
		x, y := s.Pos()
		drawBanana(img, x, y, s.T, sx, sy)
	}
	if boom != nil {
		drawExplosion(img, boom, sx, sy)
	}
	return img
}

// throwPoseTime is how long the raised arm stays up, in simulated seconds.
const throwPoseTime = 0.3

// throwPose is PlotShot's: player one throws with GorL, player two with GorR.
func throwPose(player int) gorillaPose {
	if player == 0 {
		return leftUp
	}
	return rightUp
}

// gorillaColors maps the ape's two attributes onto the palette. Colour 0 is the
// sky, which is how it gets a brow, a nose and a chest.
var gorillaColors = []color.RGBA{colSky, colGorilla}

// stampSprite paints a prebuilt sprite at a field position, skipping its
// transparent pixels.
func stampSprite(img *image.RGBA, c *canvas, fx, fy int, pal []color.RGBA, sx, sy float64) {
	for y := range c.h {
		for x := range c.w {
			v := c.at(x, y)
			if v == transparent || int(v) >= len(pal) {
				continue
			}
			fillField(img, fx+x, fy+y, pal[v], sx, sy)
		}
	}
}

// buildingColorAt picks the colour of one masonry pixel: the owning building's
// base colour, or a window. Windows are derived from position rather than stored,
// so they cost nothing on the wire and stay put across frames.
func buildingColorAt(w *World, fx, fy int) color.RGBA {
	b := w.buildingAtCol(fx)
	if b == nil {
		return colBuilding[0]
	}
	if lit, ok := windowAt(b, fx, fy); ok {
		if lit {
			return colWindowLit
		}
		return colWindowDark
	}
	return colBuilding[b.Color%3]
}

// windowAt reports whether a masonry pixel is a window pane, and whether anyone
// is home.
//
// MakeCityScape lays its panes out from the building's *roof* down — `FOR i =
// BHeight - 3 TO 7 STEP -WDifV` — not up from the ground, so which row the bottom
// one lands on depends on how tall the building is. Getting that backwards makes
// every skyline look subtly regular in a way the original's never does.
func windowAt(b *Building, fx, fy int) (lit, ok bool) {
	const (
		paneW, paneH = 3, 6   // LINE (c, y)-(c + WWidth, y + WHeight), BF — corners inclusive
		gapX, gapY   = 10, 15 // WDifh, WDifV
		inset        = 3
	)
	ox, oy := fx-(b.X+inset), fy-(b.Y+inset)
	if ox < 0 || oy < 0 || ox%gapX > paneW || oy%gapY > paneH {
		return false, false
	}
	// The column loop stops before it reaches the far wall.
	if b.X+inset+ox/gapX*gapX >= b.X+b.W-inset {
		return false, false
	}
	// And the row loop stops before it reaches the ground.
	if b.H-inset-oy/gapY*gapY < 7 {
		return false, false
	}
	// FnRan(4) = 1 leaves a pane dark. Hashed from where it is rather than rolled,
	// so the city does not strobe at 30fps.
	h := (fx/gapX)*73 + (fy/gapY)*179 + int(b.Color)*31
	return h%4 != 0, true
}

// buildingAtCol finds the building spanning a column, or nil for open sky.
//
// Via a column index, not a scan. Every masonry pixel asks this question, and a
// scan over a dozen buildings per pixel is ~4M iterations a frame — which was
// most of the render cost before this existed.
func (w *World) buildingAtCol(fx int) *Building {
	if fx < 0 || fx >= FieldW {
		return nil
	}
	w.ensureCols()
	if i := w.cols[fx]; i >= 0 {
		return &w.Buildings[i]
	}
	return nil
}

// ensureCols builds the column→building index. The skyline never changes after
// generation, so this is done once per world.
func (w *World) ensureCols() {
	if w.cols != nil {
		return
	}
	w.cols = make([]int16, FieldW)
	for i := range w.cols {
		w.cols[i] = -1
	}
	for i := range w.Buildings {
		b := &w.Buildings[i]
		for x := b.X; x < b.X+b.W && x < FieldW; x++ {
			if x >= 0 {
				w.cols[x] = int16(i)
			}
		}
	}
}

// drawBanana stamps the banana, tumbling as it flies. It is the original's
// bitmap, at the original's size — six pixels by seven — and it is drawn centred
// on the point that collides, so what you aim is what hits.
func drawBanana(img *image.RGBA, fx, fy, t, sx, sy float64) {
	pose := bananaPoses[bananaRot(t)]
	h := len(pose)
	w := len(pose[0])
	x0 := int(math.Round(fx)) - w/2
	y0 := int(math.Round(fy)) - h/2
	for y, row := range pose {
		for x, c := range row {
			if c == '#' {
				fillField(img, x0+x, y0+y, colBanana, sx, sy)
			}
		}
	}
}

// drawSun stamps the sun, which spends the game beaming and is briefly appalled
// when a banana goes through it.
func drawSun(img *image.RGBA, shocked bool, sx, sy float64) {
	c := sunHappy
	if shocked {
		c = sunShocked
	}
	stampSprite(img, c, sunCX-sunOriginX, sunCY-sunOriginY, sunColors, sx, sy)
}

// sunColors: the disc and its rays, and the sky the face is punched out of.
var sunColors = []color.RGBA{colSky, {}, {}, colSun}

// drawWind is MakeCityScape's arrow: a bar along the bottom of the field whose
// length is the wind and whose head points the way it blows.
func drawWind(img *image.RGBA, wind int8, sx, sy float64) {
	if wind == 0 {
		return
	}
	const y = FieldH - 5
	x0 := FieldW / 2
	x1 := x0 + int(wind)*3*(FieldW/320) // WindLine = Wind * 3 * (ScrWidth \ 320)

	for x := min(x0, x1); x <= max(x0, x1); x++ {
		fillField(img, x, y, colExplosion, sx, sy)
	}
	head := 2
	if wind > 0 {
		head = -2
	}
	for i := 0; i <= iabs(head); i++ {
		d := i * isign(head)
		fillField(img, x1+d, y-i, colExplosion, sx, sy)
		fillField(img, x1+d, y+i, colExplosion, sx, sy)
	}
}

// fillField paints the pixel block one field unit maps onto. The simulation runs
// in a fixed 640×350 space; at a larger placement one field unit is several
// pixels, and painting only its top-left corner would leave the sprites full of
// holes.
func fillField(img *image.RGBA, fx, fy int, c color.RGBA, sx, sy float64) {
	x0, y0 := int(float64(fx)*sx), int(float64(fy)*sy)
	x1, y1 := int(math.Ceil(float64(fx+1)*sx)), int(math.Ceil(float64(fy+1)*sy))
	for py := y0; py < y1; py++ {
		for px := x0; px < x1; px++ {
			set(img, px, py, c)
		}
	}
}

func set(img *image.RGBA, x, y int, c color.RGBA) {
	if x < 0 || y < 0 || x >= img.Rect.Dx() || y >= img.Rect.Dy() {
		return
	}
	i := img.PixOffset(x, y)
	img.Pix[i+0] = c.R
	img.Pix[i+1] = c.G
	img.Pix[i+2] = c.B
	img.Pix[i+3] = 0xff
}
