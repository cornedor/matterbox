package kurve

import (
	"image"
	"image/color"
	"math"
)

// The palette. Up to MaxPlayers curves want that many colours an eye — including
// a colour-blind one — can still tell apart, so they are spread around the wheel
// rather than picked for prettiness: amber, cyan, magenta, green, violet, rose.
// colHead is the same hue lightened, for the brighter leading disc. The arena is
// near-black with a cool grey wall.
var (
	colArena = color.RGBA{0x12, 0x14, 0x1b, 0xff}
	colWall  = color.RGBA{0x3a, 0x40, 0x4e, 0xff}
	colTrail = [MaxPlayers]color.RGBA{
		{0xff, 0xb0, 0x2e, 0xff}, // amber
		{0x36, 0xd6, 0xe7, 0xff}, // cyan
		{0xff, 0x5d, 0xb1, 0xff}, // magenta
		{0x7e, 0xd9, 0x57, 0xff}, // green
		{0xa9, 0x7b, 0xff, 0xff}, // violet
		{0xff, 0x6b, 0x5e, 0xff}, // rose
	}
	colHead = [MaxPlayers]color.RGBA{
		{0xff, 0xe6, 0xa8, 0xff},
		{0xc6, 0xf7, 0xfc, 0xff},
		{0xff, 0xc2, 0xe2, 0xff},
		{0xd6, 0xf5, 0xc6, 0xff},
		{0xdd, 0xcc, 0xff, 0xff},
		{0xff, 0xc9, 0xc2, 0xff},
	}
	colDead   = color.RGBA{0x6a, 0x6f, 0x7b, 0xff}
	colFlash  = color.RGBA{0xff, 0xff, 0xff, 0xff}
	wallWidth = 2 // field units of border on every side
)

// Render draws the sim at the given pixel size — a convenience wrapper for
// callers (tests, the decode CLI) that do not keep a Renderer.
func Render(s *Sim, phase Phase, countdown int, pxW, pxH int) *image.RGBA {
	var r Renderer
	return r.Render(s, phase, countdown, pxW, pxH)
}

// Renderer holds the frame buffer across frames. A live curve is redrawn ~20
// times a second and the buffer is over a megabyte; a fresh one per frame would
// hand the GC tens of MB/s for nothing.
//
// Not safe for concurrent use: one Renderer belongs to one open game modal, the
// same contract internal/game.Renderer keeps.
type Renderer struct {
	img *image.RGBA
}

// Render draws the sim into the Renderer's buffer and returns it. The result is
// valid only until the next call — the caller encodes it (to PNG, for the kitty
// transmit) and does not hold on to it.
//
// The whole arena is redrawn every frame from the owner grid, so what is painted
// and what a head collides with are the same thing. That is cheap because the
// image goes to the terminal out of band under a fixed kitty image id: re-sending
// it repaints the placeholder cells already on screen without the TUI
// re-rendering at all, so a moving curve costs the View() hot path nothing.
func (r *Renderer) Render(s *Sim, phase Phase, countdown, pxW, pxH int) *image.RGBA {
	if r.img == nil || r.img.Rect.Dx() != pxW || r.img.Rect.Dy() != pxH {
		r.img = image.NewRGBA(image.Rect(0, 0, pxW, pxH))
	}
	img := r.img
	sx := float64(pxW) / FieldW
	sy := float64(pxH) / FieldH

	// Background, then the border.
	for i := range pxW * pxH {
		put(img.Pix[i*4:], colArena)
	}

	// Trails, straight off the owner grid — one scan, one colour lookup per field
	// cell, expanded to the pixel block it maps onto.
	for fy := range FieldH {
		for fx := range FieldW {
			o := s.grid[fy*FieldW+fx]
			if o == 0 {
				continue
			}
			c := colTrail[o-1]
			if s.Curves[o-1].Dead {
				c = blend(colTrail[o-1], colDead, 0.45)
			}
			fillField(img, fx, fy, c, sx, sy)
		}
	}

	drawWalls(img, sx, sy)

	// Heads. During the countdown they flash so a player can find their curve
	// before it moves; a dead head is a dim cross where it fell.
	for i := range s.Curves {
		c := &s.Curves[i]
		switch {
		case c.Dead:
			drawCross(img, c.X, c.Y, colDead, sx, sy)
		default:
			col := colHead[i]
			if phase == PhaseCountdown && countdown%8 < 4 {
				col = colFlash
			}
			drawHead(img, c.X, c.Y, col, sx, sy)
		}
	}
	return img
}

// drawWalls frames the arena in the lethal border colour, wallWidth field units
// thick on each edge.
func drawWalls(img *image.RGBA, sx, sy float64) {
	for fy := range FieldH {
		for fx := range FieldW {
			if fx < wallWidth || fx >= FieldW-wallWidth || fy < wallWidth || fy >= FieldH-wallWidth {
				fillField(img, fx, fy, colWall, sx, sy)
			}
		}
	}
}

// drawHead stamps a small filled disc at a head position.
func drawHead(img *image.RGBA, fx, fy float64, c color.RGBA, sx, sy float64) {
	const rad = 2
	cx, cy := int(math.Round(fx)), int(math.Round(fy))
	for dy := -rad; dy <= rad; dy++ {
		for dx := -rad; dx <= rad; dx++ {
			if dx*dx+dy*dy <= rad*rad {
				fillField(img, cx+dx, cy+dy, c, sx, sy)
			}
		}
	}
}

// drawCross marks where a curve died: a small X, so the wreck reads differently
// from a live head at a glance.
func drawCross(img *image.RGBA, fx, fy float64, c color.RGBA, sx, sy float64) {
	const rad = 2
	cx, cy := int(math.Round(fx)), int(math.Round(fy))
	for d := -rad; d <= rad; d++ {
		fillField(img, cx+d, cy+d, c, sx, sy)
		fillField(img, cx+d, cy-d, c, sx, sy)
	}
}

// fillField paints the pixel block one field unit maps onto — the same
// upsampling internal/game uses, so a one-unit trail is a solid line at any
// placement instead of a dotted one.
func fillField(img *image.RGBA, fx, fy int, c color.RGBA, sx, sy float64) {
	if fx < 0 || fy < 0 || fx >= FieldW || fy >= FieldH {
		return
	}
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
	put(img.Pix[img.PixOffset(x, y):], c)
}

func put(p []byte, c color.RGBA) {
	p[0], p[1], p[2], p[3] = c.R, c.G, c.B, 0xff
}

// blend mixes a toward b by t (0..1), for dimming a dead player's trail.
func blend(a, b color.RGBA, t float64) color.RGBA {
	mix := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xff}
}
