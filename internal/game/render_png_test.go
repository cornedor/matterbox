package game

import (
	"image/color"
	"testing"
)

func at(t *testing.T, w *World, s *Shot, boom *Explosion, pxW, pxH, x, y int) color.RGBA {
	t.Helper()
	img := Render(w, s, boom, nil, pxW, pxH)
	i := img.PixOffset(x, y)
	return color.RGBA{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
}

func TestRenderFillsTheWholeBox(t *testing.T) {
	w := NewWorld(42)
	for _, size := range [][2]int{{320, 175}, {640, 350}, {960, 525}, {1280, 700}} {
		img := Render(w, nil, nil, nil, size[0], size[1])
		if got := img.Rect.Dx(); got != size[0] {
			t.Errorf("width %d, want %d", got, size[0])
		}
		if got := img.Rect.Dy(); got != size[1] {
			t.Errorf("height %d, want %d", got, size[1])
		}
		// Every pixel must be opaque: a transparent one would show the terminal
		// background through the city.
		for i := 3; i < len(img.Pix); i += 4 {
			if img.Pix[i] != 0xff {
				t.Fatalf("%dx%d: pixel %d is not opaque", size[0], size[1], i/4)
			}
		}
	}
}

// The sky is above, the city is below. A trivial check, but it catches a flipped
// y-axis — the single easiest mistake to make between a simulation whose y grows
// downward and an image whose y also grows downward but through a scale factor.
func TestSkyIsAboveTheCity(t *testing.T) {
	w := NewWorld(42)
	if got := at(t, w, nil, nil, 640, 350, 5, 5); got != colSky {
		t.Errorf("top-left corner is %v, want sky %v", got, colSky)
	}
	// The ground line: every building reaches it, so the bottom row is masonry
	// wherever a building stands.
	b := w.Buildings[len(w.Buildings)/2]
	px := b.X + b.W/2
	if got := at(t, w, nil, nil, 640, 350, px, bottomLine-2); got == colSky {
		t.Errorf("ground level under building %+v is sky; the city is upside down", b)
	}
}

// The renderer and the collision test must read the same occupancy bitmap, or the
// game will show a wall a banana flies through — or worse, an empty sky it stops
// dead in.
func TestRenderAgreesWithCollision(t *testing.T) {
	w := NewWorld(19)
	b := w.Buildings[3]
	w.Carve(b.X+b.W/2, b.Y+10, craterRX, craterRY)

	img := Render(w, nil, nil, nil, FieldW, FieldH) // 1:1, so pixel == field unit
	for y := range FieldH {
		for x := range FieldW {
			i := img.PixOffset(x, y)
			c := color.RGBA{img.Pix[i], img.Pix[i+1], img.Pix[i+2], 0xff}
			isSky := c == colSky
			// Skip the sun, the gorillas and the wind arrow: they are drawn over
			// the world, not part of it.
			if !isSky && !w.Solid(x, y) {
				if isSun(x, y) || isGorilla(w, x, y) || isWind(y) {
					continue
				}
				t.Fatalf("(%d,%d) is painted %v but nothing solid is there", x, y, c)
			}
			if isSky && w.Solid(x, y) {
				t.Fatalf("(%d,%d) is painted sky but collision says it is solid", x, y)
			}
		}
	}
}

// isSun is the box DoSun clears before it redraws — big enough to cover the rays,
// which reach well past the disc.
func isSun(x, y int) bool {
	dx, dy := x-sunCX, y-sunCY
	return dx >= -22 && dx <= 22 && dy >= -18 && dy <= 18
}

func isGorilla(w *World, x, y int) bool {
	_, ok := w.gorillaAt(x, y)
	return ok
}

// The wind arrow lives in the strip below the buildings' feet, which is not part
// of the world at all.
func isWind(y int) bool { return y >= FieldH-8 }

// A crater must actually appear. The wire format ships craters and nothing else
// about the destruction, so if they did not render, damage would be invisible.
func TestCraterIsVisible(t *testing.T) {
	w := NewWorld(19)
	b := w.Buildings[3]
	cx, cy := b.X+b.W/2, b.Y+10

	before := at(t, w, nil, nil, FieldW, FieldH, cx, cy)
	if before == colSky {
		t.Fatal("test picked a spot that was already sky")
	}
	w.Carve(cx, cy, craterRX, craterRY)
	if got := at(t, w, nil, nil, FieldW, FieldH, cx, cy); got != colSky {
		t.Errorf("a cratered pixel renders as %v, want sky %v", got, colSky)
	}
}

// The same world must render identically every time: window lighting is derived
// from position, not rolled per frame, or the city would strobe at 30fps.
func TestRenderIsDeterministic(t *testing.T) {
	w := NewWorld(5)
	a := Render(w, nil, nil, nil, 400, 220)
	b := Render(w, nil, nil, nil, 400, 220)
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			t.Fatalf("two renders of the same world differ at byte %d; the city is strobing", i)
		}
	}
}

// The modal keeps one Renderer for the life of a game and redraws through it 30
// times a second, so that — not the allocating package-level Render — is what
// the frame budget has to be measured against.
func BenchmarkRenderer(b *testing.B) {
	w := NewWorld(42)
	for i := range 20 {
		bl := w.Buildings[i%len(w.Buildings)]
		w.Carve(bl.X+bl.W/2, bl.Y+i, craterRX, craterRY)
	}
	s := w.NewShot(0, 45, 90)
	s.T = 1.5

	var r Renderer
	r.Render(w, s, nil, nil, 800, 440) // warm the buffer and the lookup tables
	b.ResetTimer()
	for b.Loop() {
		r.Render(w, s, nil, nil, 800, 440)
	}
}
