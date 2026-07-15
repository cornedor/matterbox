package kurve

import (
	"math"
	"math/rand/v2"
	"slices"
)

// The field is a fixed coordinate space, exactly as internal/game keeps
// GORILLA.BAS's mode-9 field: the simulation runs in field units and never
// learns what size the terminal is. Rendering scales it. 4:3 so the arena is
// shown the shape it is reasoned in.
const (
	FieldW = 240
	FieldH = 180

	// MaxPlayers is the arena's ceiling: a host plus up to five joiners. The wire
	// encodes the roster length in a byte and NewSim clamps to this, so a corrupt
	// or hostile payload can never make the grid owner (a uint8, 1-based) collide
	// with a real player index.
	MaxPlayers = 6

	// DisplayAspect is the shape the field is given on screen. The arena is
	// already 4:3 in field units, so unlike Gorillas there is no CRT correction
	// folded in here — a field unit is square.
	DisplayAspect = float64(FieldW) / float64(FieldH)

	// speed is how far a head travels per tick, in field units. turnRate is how
	// far it rotates per tick while steering; together they set the turning
	// radius (speed/turnRate ≈ 17 units), which is the whole feel of the game.
	speed    = 2.2
	turnRate = 0.13

	// trailRadius fattens the trail from a one-pixel thread to something you can
	// see and aim at: each stamped point is a disc of this radius, so the trail is
	// 2·trailRadius+1 units wide.
	trailRadius = 1

	// neckCells is how many of a curve's own most-recently-drawn trail cells do
	// not kill it — the "neck". Without it a curve dies instantly: its fat trail
	// means the head sits on cells it laid a fraction of a step ago. It is long
	// enough to clear the neck on the tightest turn (a few steps of travel) and far
	// short of a full loop, so closing a curve back onto its own tail is still
	// fatal — which is what stops a player stalling forever in a circle.
	neckCells = 96

	// A gap schedule punches a hole in each curve's trail on a fixed cycle, so a
	// pinned player can still thread through a wall of line. gapEvery ticks of
	// solid trail are followed by gapLen ticks of nothing. Per-curve phase offsets
	// (from the seed) keep the two curves' gaps out of lockstep.
	gapEvery = 42
	gapLen   = 7

	// startMargin keeps the two starting heads off the walls.
	startMargin = 40
)

// Dir is a steering input: a held level, not an event. A curve turns this way
// every tick until told otherwise, which is exactly what makes the input cheap
// enough to carry as an occasional post edit rather than a per-frame stream.
type Dir int8

const (
	Left     Dir = -1
	Straight Dir = 0
	Right    Dir = 1
)

// Curve is one player's snake: a head with a heading, alive or dead. The trail it
// has laid lives in the shared grid, not here — the curve is only its leading
// edge.
type Curve struct {
	X, Y float64 // head position, field units
	Head float64 // heading, radians (0 = +x, growing clockwise since y is down)

	Dead      bool
	DeathTick uint16 // the tick it died on, for a replay to stop its trail there

	// neck is the ring of its last `neckCells` drawn cells — the stretch of its own
	// trail it is allowed to overlap. Eviction only drops immunity; the cell stays
	// solid in the grid, so the tail turns lethal a fixed distance behind the head.
	neck  []int32
	nhead int
}

// Sim is the authoritative simulation: the curves (two to MaxPlayers of them),
// the grid of trail they have drawn, and the input log that produced it. The host
// owns one and steps it; a joiner rebuilds one from the wire by replaying the same
// log (see FromState).
//
// The curve, event and gap slices are all sized to the same player count, fixed
// when the sim is built. It is pure: no clock, no network, no pixels. match.go
// drives it and wire.go serialises it, the same division of labour internal/game
// uses.
type Sim struct {
	Seed   uint16
	Tick   uint16
	Curves []Curve

	// grid is owner+1 per cell (0 = empty), the trail every collision test and
	// every rendered pixel reads — so what kills you and what you can see are by
	// construction the same thing, exactly as Gorillas' occupancy bitmap is.
	grid []uint8

	// events is the steering log per curve: the authoritative record of who
	// turned which way and when. It is what travels on the wire (a few bytes per
	// direction change) and what a replay consumes. The host appends to it via
	// Steer; a rebuilt Sim is handed it whole.
	events [][]Turn

	gapOffset []int
}

// Turn is one steering change: from Tick onward, that curve holds Dir. The log is
// sparse — a curve going straight for a second adds nothing — which is what keeps
// a whole match down to a couple of hundred bytes.
type Turn struct {
	Tick uint16
	Dir  Dir
}

// MaxEvents caps one curve's steering log, and with it the payload. A round is
// almost always decided long before a player changes direction this many times;
// past the cap the curve simply holds its last heading rather than growing the
// post without bound.
const MaxEvents = 200

// NewSim builds the opening position a seed describes for n players: every head
// placed and aimed, with per-curve gap phases. Both clients call this with the
// same seed and the same n and must get the same setup, so it draws from an
// explicitly seeded PCG, as NewWorld does. n is clamped to [1, MaxPlayers] — one
// head is the lobby preview, two or more is a game.
func NewSim(seed uint16, n int) *Sim {
	n = min(max(n, 1), MaxPlayers)
	r := rand.New(rand.NewPCG(uint64(seed), 0x9E3779B97F4A7C15))
	s := &Sim{
		Seed:      seed,
		grid:      make([]uint8, FieldW*FieldH),
		Curves:    make([]Curve, n),
		events:    make([][]Turn, n),
		gapOffset: make([]int, n),
	}

	// The heads sit evenly spaced on a ring around the arena's centre, each aimed
	// along the ring (tangential) so nobody opens pointed straight at a wall or
	// into the pack. The ring starts on the left and runs clockwise, so the classic
	// two-player game still opens curve 0 on the left, curve 1 on the right. A
	// little jitter on the radius and the heading keeps rounds from opening
	// identically; the radius keeps every head a comfortable margin off the walls
	// whatever n is.
	cx, cy := float64(FieldW)/2, float64(FieldH)/2
	radius := float64(FieldH)/2 - startMargin
	for i := range s.Curves {
		a := math.Pi + 2*math.Pi*float64(i)/float64(n)
		rad := radius + (r.Float64()-0.5)*20
		head := a + math.Pi/2 + (r.Float64()-0.5)*(math.Pi/6)
		s.Curves[i] = Curve{X: cx + rad*math.Cos(a), Y: cy + rad*math.Sin(a), Head: head}

		s.Curves[i].neck = make([]int32, neckCells)
		for j := range s.Curves[i].neck {
			s.Curves[i].neck[j] = -1
		}
		s.gapOffset[i] = r.IntN(gapEvery)
	}
	return s
}

// Steer records a steering change for a curve at the current tick. It is how the
// host feeds in both its own key presses and the joiner's controller edits; a
// replay never calls it (it is handed the finished log). Redundant calls — the
// same direction twice, or two changes between two ticks — collapse, so the log
// stays a true list of changes.
func (s *Sim) Steer(player int, d Dir) {
	c := &s.Curves[player]
	if c.Dead {
		return
	}
	ev := s.events[player]
	if n := len(ev); n > 0 && ev[n-1].Tick == s.Tick {
		ev[n-1].Dir = d // same tick: overwrite, don't stack
		return
	}
	if s.dirAt(player, s.Tick) == d {
		return // no actual change
	}
	if len(ev) >= MaxEvents {
		return
	}
	s.events[player] = append(ev, Turn{Tick: s.Tick, Dir: d})
}

// dirAt is the direction a curve holds at a tick: the last logged change at or
// before it, Straight if none. This single definition is used both by the host
// stepping forward and by a replay reconstructing the past, which is what keeps
// the two in step.
func (s *Sim) dirAt(player int, tick uint16) Dir {
	ev := s.events[player]
	d := Straight
	for _, e := range ev {
		if e.Tick > tick {
			break
		}
		d = e.Dir
	}
	return d
}

// Step advances the world one tick and reports which players died on it (0, 1 or
// both). Only the host calls this; it is the sole authority on collisions.
func (s *Sim) Step() []int {
	var died []int
	for i := range s.Curves {
		c := &s.Curves[i]
		if c.Dead {
			continue
		}
		if s.advance(i, s.dirAt(i, s.Tick)) {
			c.Dead = true
			c.DeathTick = s.Tick
			died = append(died, i)
		}
	}
	s.Tick++
	return died
}

// advance moves one curve a single tick and returns whether it died doing so. It
// turns, then walks the segment from the old head to the new one one unit at a
// time — sampling rather than jumping, so a fast head cannot tunnel through a
// thin trail — testing each sample for a collision before drawing it.
func (s *Sim) advance(i int, d Dir) bool {
	c := &s.Curves[i]
	c.Head += float64(d) * turnRate

	x0, y0 := c.X, c.Y
	x1 := x0 + math.Cos(c.Head)*speed
	y1 := y0 + math.Sin(c.Head)*speed

	drawing := s.drawing(i)
	dist := math.Hypot(x1-x0, y1-y0)
	steps := max(int(dist), 1)
	for step := 1; step <= steps; step++ {
		f := float64(step) / float64(steps)
		x := x0 + (x1-x0)*f
		y := y0 + (y1-y0)*f
		ix, iy := int(math.Round(x)), int(math.Round(y))

		if ix < 0 || ix >= FieldW || iy < 0 || iy >= FieldH {
			c.X, c.Y = x, y
			return true // into a wall
		}
		if s.blocked(i, ix, iy) {
			c.X, c.Y = x, y
			return true // into a trail
		}
		if drawing {
			s.stamp(i, ix, iy)
		}
	}
	c.X, c.Y = x1, y1
	return false
}

// drawing reports whether curve i is laying trail this tick, or is in one of its
// periodic gaps. Derived from the tick and the curve's seeded phase, so both
// clients agree on exactly where the holes are.
func (s *Sim) drawing(i int) bool {
	return (int(s.Tick)+s.gapOffset[i])%gapEvery >= gapLen
}

// blocked reports whether cell (x,y) kills curve i: any trail cell, except the
// stretch of its own neck it is still allowed to overlap.
func (s *Sim) blocked(i, x, y int) bool {
	o := s.grid[y*FieldW+x]
	if o == 0 {
		return false
	}
	if int(o) == i+1 && s.isNeck(i, int32(y*FieldW+x)) {
		return false
	}
	return true
}

// stamp draws a trail disc centred on (x,y) for curve i. Every cell it fills goes
// solid immediately — so the other curve sees it as wall at once — and is pushed
// onto this curve's neck ring, earning immunity from its own head until the ring
// evicts it a fixed distance back.
func (s *Sim) stamp(i, x, y int) {
	c := &s.Curves[i]
	for dy := -trailRadius; dy <= trailRadius; dy++ {
		for dx := -trailRadius; dx <= trailRadius; dx++ {
			nx, ny := x+dx, y+dy
			if nx < 0 || nx >= FieldW || ny < 0 || ny >= FieldH {
				continue
			}
			idx := int32(ny*FieldW + nx)
			if s.grid[idx] == 0 {
				s.grid[idx] = uint8(i + 1)
			}
			c.neck[c.nhead] = idx
			c.nhead = (c.nhead + 1) % len(c.neck)
		}
	}
}

func (s *Sim) isNeck(i int, idx int32) bool {
	return slices.Contains(s.Curves[i].neck, idx)
}

// Owner returns the curve that owns cell (x,y), or 0 for empty. The renderers and
// the ASCII board read this so the trail they draw is exactly the trail that
// kills.
func (s *Sim) Owner(x, y int) uint8 {
	if x < 0 || x >= FieldW || y < 0 || y >= FieldH {
		return 0
	}
	return s.grid[y*FieldW+x]
}
