package game

import (
	"math"
	"math/rand/v2"
)

// The field is the original GORILLA.BAS mode-9 coordinate space. Keeping it
// verbatim means the constants below are the 1993 ones rather than re-tuned
// guesses, and the ballistics behave the way the game is remembered. Rendering
// scales this field to whatever the terminal gives us; the simulation never
// knows about pixels or cells.
const (
	FieldW = 640
	FieldH = 350

	bottomLine   = 335 // ground: the base of every building
	htInc        = 10  // per-building height step along the skyline's slope
	defBWidth    = 37  // building width is defBWidth + ran(defBWidth)
	randomHeight = 120 // building height is newHt + ran(randomHeight)
	buildingGap  = 2

	gorillaW = 28
	gorillaH = 25
	// The sprite is anchored above the roof by these, as PlaceGorillas does.
	gorillaXAdj = 14
	gorillaYAdj = 30

	gravity = 9.8

	// craterR is the original's ScrHeight/50. Small — a hit takes a bite out of
	// a roof rather than demolishing the block, which is what keeps a match going.
	craterR = 7
	// A gorilla dying takes a bigger bite than a banana does.
	gorillaCraterR = 14
)

// Building is one tower. Y is the roof; the base is always bottomLine.
type Building struct {
	X, Y, W, H int
	Color      uint8 // 0–2, an index into the renderer's palette
}

// Crater is a bite taken out of the skyline. Craters are the only destructible
// state, and they are what travels on the wire: a world is fully described by
// its seed plus its craters, so the buildings themselves are never transmitted.
type Crater struct {
	X, Y int16
	R    uint8
}

// World is the simulated field: a skyline generated from Seed, the two gorillas
// standing on it, and the craters blown out of it so far.
type World struct {
	Seed      uint16
	Wind      int8
	Buildings []Building
	Gorillas  [2]Point

	Craters []Crater

	// solid is the occupancy bitmap every collision and every rendered pixel
	// reads. Buildings never change and craters only ever get appended, so it is
	// rebuilt when the crater count moves rather than on every query — a
	// per-pixel test against the raw rect/crater lists would be ~13M operations
	// a frame, which is not a thing to do 30 times a second.
	solid  []bool
	solidN int // craters already baked into solid

	// cols indexes column → building (-1 for sky), so the renderer can colour a
	// masonry pixel without scanning the building list for every one of them.
	cols []int16
}

// Point is a field coordinate. (Not image.Point: the simulation is pure and has
// no business importing an image package.)
type Point struct{ X, Y int }

// NewWorld builds the world a seed describes. Both clients call this with the
// same seed and must get the same skyline, which is why it draws from an
// explicitly seeded PCG rather than the global source.
func NewWorld(seed uint16) *World {
	r := rand.New(rand.NewPCG(uint64(seed), 0x9E3779B97F4A7C15))
	ran := func(n int) int { return r.IntN(n) + 1 } // QBasic's FnRan: 1..n

	w := &World{Seed: seed}
	w.Buildings = makeSkyline(ran)
	w.placeGorillas(ran)

	// ran(10)-5 gives a light breeze; one round in three gets a real gale.
	wind := ran(10) - 5
	if ran(3) == 1 {
		if wind > 0 {
			wind += ran(10)
		} else {
			wind -= ran(10)
		}
	}
	w.Wind = int8(wind)
	return w
}

// makeSkyline is MakeCityScape: pick a slope for the city, then walk left to
// right laying down buildings of random width and height along it.
func makeSkyline(ran func(int) int) []Building {
	// The city trends upward, downward, or into a "V" — and the V, being three
	// of the six cases, is the one you remember.
	var newHt int
	slope := ran(6)
	switch slope {
	case 1, 3, 4, 5:
		newHt = 15 // upward, or the V's rising first half
	default:
		newHt = 130 // downward, or the inverted V
	}

	var bs []Building
	for x := 2; x <= FieldW-htInc; {
		switch slope {
		case 1:
			newHt += htInc
		case 2:
			newHt -= htInc
		case 3, 4, 5:
			if x > FieldW/2 {
				newHt -= 2 * htInc
			} else {
				newHt += 2 * htInc
			}
		case 6:
			if x > FieldW/2 {
				newHt += 2 * htInc
			} else {
				newHt -= 2 * htInc
			}
		}

		bw := ran(defBWidth) + defBWidth
		if x+bw > FieldW {
			bw = FieldW - x - 2
		}
		if bw <= 0 {
			break
		}

		bh := ran(randomHeight) + newHt
		bh = min(max(bh, htInc), bottomLine-gorillaH-gorillaYAdj)

		bs = append(bs, Building{
			X: x, Y: bottomLine - bh, W: bw, H: bh,
			Color: uint8(ran(3) - 1),
		})
		x += bw + buildingGap
	}
	return bs
}

// placeGorillas stands each player on the second or third building in from
// their edge, centred on the roof.
func (w *World) placeGorillas(ran func(int) int) {
	last := len(w.Buildings) - 1
	idx := [2]int{ran(2), last - ran(2)}
	for i, bi := range idx {
		b := w.Buildings[max(min(bi, last), 0)]
		w.Gorillas[i] = Point{
			X: b.X + b.W/2 - gorillaXAdj, // centred on the roof
			Y: b.Y - gorillaH,            // standing on it
		}
	}
}

// Solid reports whether the field is occupied at (x,y) — building, minus any
// crater blown through it. This is both the collision surface and what the
// renderer fills, so a banana can only ever hit what you can see.
func (w *World) Solid(x, y int) bool {
	if x < 0 || x >= FieldW || y < 0 || y >= FieldH {
		return false
	}
	w.ensureSolid()
	return w.solid[y*FieldW+x]
}

// ensureSolid rebuilds the occupancy bitmap if craters have been added since it
// was last baked. Appending a crater only ever clears bits, so a rebuild carves
// the new craters out of the existing bitmap instead of starting over.
func (w *World) ensureSolid() {
	if w.solid == nil {
		w.solid = make([]bool, FieldW*FieldH)
		for _, b := range w.Buildings {
			for y := b.Y; y < bottomLine && y < FieldH; y++ {
				for x := b.X; x < b.X+b.W && x < FieldW; x++ {
					if x >= 0 && y >= 0 {
						w.solid[y*FieldW+x] = true
					}
				}
			}
		}
		w.solidN = 0
	}
	for _, c := range w.Craters[w.solidN:] {
		w.carveSolid(c)
	}
	w.solidN = len(w.Craters)
}

func (w *World) carveSolid(c Crater) {
	r := int(c.R)
	cx, cy := int(c.X), int(c.Y)
	for y := cy - r; y <= cy+r; y++ {
		if y < 0 || y >= FieldH {
			continue
		}
		for x := cx - r; x <= cx+r; x++ {
			if x < 0 || x >= FieldW {
				continue
			}
			if dx, dy := x-cx, y-cy; dx*dx+dy*dy <= r*r {
				w.solid[y*FieldW+x] = false
			}
		}
	}
}

// Carve blows a crater in the world. Callers append through here so the cached
// bitmap and the wire state can never disagree.
func (w *World) Carve(x, y, r int) {
	w.Craters = append(w.Craters, Crater{X: int16(x), Y: int16(y), R: uint8(r)})
}

// Shot is a banana in flight. It is a closed-form parabola rather than an
// integrated velocity, exactly as PlotShot computes it, so any (t) can be
// evaluated directly: a client that misses a frame — or wants to interpolate
// between two streamed states — just asks for the position at the time it wants.
type Shot struct {
	X0, Y0 float64
	VX, VY float64
	Wind   int8
	T      float64
}

// NewShot launches from a player's gorilla at the given angle (degrees) and
// power. Player 1 throws to the left, so their angle is mirrored.
func (w *World) NewShot(player int, angleDeg, power float64) *Shot {
	g := w.Gorillas[player]
	if player == 1 {
		angleDeg = 180 - angleDeg
	}
	rad := angleDeg / 180 * math.Pi
	return &Shot{
		X0:   float64(g.X + gorillaW/2),
		Y0:   float64(g.Y - 4),
		VX:   math.Cos(rad) * power,
		VY:   math.Sin(rad) * power,
		Wind: w.Wind,
	}
}

// At evaluates the arc at time t. Wind is a constant horizontal acceleration and
// gravity a constant vertical one; y grows downward.
func (s *Shot) At(t float64) (x, y float64) {
	x = s.X0 + s.VX*t + 0.5*(float64(s.Wind)/5)*t*t
	y = s.Y0 - s.VY*t + 0.5*gravity*t*t
	return x, y
}

// Pos is the banana's position at its current time.
func (s *Shot) Pos() (x, y float64) { return s.At(s.T) }

// Outcome is what a simulation step produced.
type Outcome int

const (
	InFlight Outcome = iota
	HitBuilding
	HitGorilla
	OffField
)

// Step advances the banana by dt and reports what it ran into. On HitBuilding or
// HitGorilla it carves the crater itself, so the world is always consistent with
// the outcome the caller is handed. hitPlayer is meaningful only for HitGorilla.
//
// Collision is sampled along the step rather than only at its end: at full power
// a banana covers tens of pixels per frame and would tunnel straight through a
// roof if only the endpoints were tested.
func (w *World) Step(s *Shot, dt float64) (out Outcome, hitPlayer int) {
	const sampleStep = 2.0 // field units between collision samples

	x0, y0 := s.At(s.T)
	x1, y1 := s.At(s.T + dt)
	dist := math.Hypot(x1-x0, y1-y0)
	steps := max(int(dist/sampleStep), 1)

	for i := 1; i <= steps; i++ {
		f := float64(i) / float64(steps)
		x, y := s.At(s.T + dt*f)
		ix, iy := int(math.Round(x)), int(math.Round(y))

		// Leaving the sides or the bottom is a miss. The top is open sky: a
		// banana lobbed over the screen is expected to come back down.
		if ix < 0 || ix >= FieldW || iy >= FieldH {
			s.T += dt * f
			return OffField, 0
		}
		if iy < 0 {
			continue
		}
		if p, ok := w.gorillaAt(ix, iy); ok {
			s.T += dt * f
			w.Carve(ix, iy, gorillaCraterR)
			return HitGorilla, p
		}
		if w.Solid(ix, iy) {
			s.T += dt * f
			w.Carve(ix, iy, craterR)
			return HitBuilding, 0
		}
	}
	s.T += dt
	return InFlight, 0
}

// gorillaAt reports whether (x,y) is inside a gorilla's box, and whose.
func (w *World) gorillaAt(x, y int) (int, bool) {
	for i, g := range w.Gorillas {
		if x >= g.X && x < g.X+gorillaW && y >= g.Y && y < g.Y+gorillaH {
			return i, true
		}
	}
	return 0, false
}
