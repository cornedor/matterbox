package game

import (
	"image"
	"image/color"
	"math"
)

// The palette. Buildings come in the original's three drab blues and greys so
// the banana and the explosion — the only warm colours on screen — are always
// the thing your eye goes to.
var (
	colSky      = color.RGBA{0x0b, 0x10, 0x26, 0xff}
	colBuilding = [3]color.RGBA{
		{0x3a, 0x4a, 0x7a, 0xff},
		{0x4a, 0x5a, 0x6a, 0xff},
		{0x5a, 0x4a, 0x6a, 0xff},
	}
	colWindowLit  = color.RGBA{0xf7, 0xd9, 0x6b, 0xff}
	colWindowDark = color.RGBA{0x1e, 0x26, 0x40, 0xff}
	colGorilla    = [2]color.RGBA{
		{0x8b, 0x5a, 0x2b, 0xff},
		{0xa8, 0x6f, 0x3a, 0xff},
	}
	colBanana    = color.RGBA{0xff, 0xe0, 0x3a, 0xff}
	colExplosion = color.RGBA{0xff, 0x6b, 0x1a, 0xff}
	colSun       = color.RGBA{0xff, 0xd7, 0x2a, 0xff}
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
func Render(w *World, s *Shot, boom *Explosion, pxW, pxH int) *image.RGBA {
	var r Renderer
	return r.Render(w, s, boom, pxW, pxH)
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
func (r *Renderer) Render(w *World, s *Shot, boom *Explosion, pxW, pxH int) *image.RGBA {
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
	drawSun(img, sx, sy)

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

	for i, g := range w.Gorillas {
		drawGorilla(img, g, colGorilla[i], sx, sy)
	}
	if s != nil {
		x, y := s.Pos()
		drawBanana(img, x, y, sx, sy, s.T)
	}
	if boom != nil {
		drawExplosion(img, boom, sx, sy)
	}
	return img
}

// buildingColorAt picks the colour of one masonry pixel: the owning building's
// base colour, or a window. Windows are derived from position rather than stored,
// so they cost nothing on the wire and stay put across frames.
func buildingColorAt(w *World, fx, fy int) color.RGBA {
	b := w.buildingAtCol(fx)
	if b == nil {
		return colBuilding[0]
	}
	// Window grid, matching the original's spacing (3×6 panes, 10×15 apart).
	const wW, wH, gapX, gapY = 3, 6, 10, 15
	ox, oy := fx-(b.X+3), (bottomLine-3)-fy
	if ox >= 0 && oy >= 0 && ox%gapX < wW && oy%gapY < wH && fy > b.Y+2 && fx < b.X+b.W-3 {
		// A quarter of the panes are dark: somebody's gone home.
		if (fx/gapX*7+fy/gapY*13+int(b.Color))%4 == 0 {
			return colWindowDark
		}
		return colWindowLit
	}
	return colBuilding[b.Color%3]
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

// drawGorilla stamps the ape: a blocky body with its arms up, which is the pose
// the 1993 sprite is remembered in.
func drawGorilla(img *image.RGBA, g Point, c color.RGBA, sx, sy float64) {
	fill := func(x0, y0, x1, y1 int) {
		for fy := y0; fy < y1; fy++ {
			for fx := x0; fx < x1; fx++ {
				fillField(img, fx, fy, c, sx, sy)
			}
		}
	}
	x, y := g.X, g.Y
	fill(x+8, y+0, x+20, y+8)   // head
	fill(x+6, y+8, x+22, y+18)  // chest
	fill(x+0, y+2, x+6, y+12)   // left arm, raised
	fill(x+22, y+2, x+28, y+12) // right arm, raised
	fill(x+7, y+18, x+12, y+25) // legs
	fill(x+16, y+18, x+21, y+25)
	// Eyes, so it reads as a face at small sizes.
	fillField(img, x+11, y+3, colSky, sx, sy)
	fillField(img, x+16, y+3, colSky, sx, sy)
}

// drawBanana draws the banana, spinning as it flies.
func drawBanana(img *image.RGBA, fx, fy, sx, sy, t float64) {
	// The original rotates the banana through four poses; a cheap approximation is
	// to swing a short bar around its centre. It is drawn a little larger than
	// scale strictly wants — at terminal sizes a pixel-accurate banana is a speck,
	// and you cannot play a game you cannot see the projectile in.
	ang := t * 8
	dx, dy := math.Cos(ang)*4, math.Sin(ang)*4
	for i := -4; i <= 4; i++ {
		f := float64(i) / 4
		x := fx + dx*f
		y := fy + dy*f
		for oy := -2; oy <= 2; oy++ {
			for ox := -2; ox <= 2; ox++ {
				fillField(img, int(x)+ox, int(y)+oy, colBanana, sx, sy)
			}
		}
	}
}

// Explosion is the fireball drawn over an impact. It is purely cosmetic — the
// crater it corresponds to is already in the world — and it is not transmitted:
// both clients derive it from the same impact, so it costs nothing on the wire.
type Explosion struct {
	X, Y  int
	R     float64 // current radius, grown by the modal's tick
	MaxR  float64
	Frame int
}

func drawExplosion(img *image.RGBA, e *Explosion, sx, sy float64) {
	r := int(e.R)
	for fy := e.Y - r; fy <= e.Y+r; fy++ {
		for fx := e.X - r; fx <= e.X+r; fx++ {
			dx, dy := fx-e.X, fy-e.Y
			d2 := dx*dx + dy*dy
			if d2 > r*r {
				continue
			}
			// A hot core fading to a ragged rim.
			c := colExplosion
			if float64(d2) < float64(r*r)*0.4 {
				c = colBanana
			}
			fillField(img, fx, fy, c, sx, sy)
		}
	}
}

func drawSun(img *image.RGBA, sx, sy float64) {
	cx, cy, r := FieldW/2, 26, 12
	for fy := cy - r; fy <= cy+r; fy++ {
		for fx := cx - r; fx <= cx+r; fx++ {
			if dx, dy := fx-cx, fy-cy; dx*dx+dy*dy <= r*r {
				fillField(img, fx, fy, colSun, sx, sy)
			}
		}
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
