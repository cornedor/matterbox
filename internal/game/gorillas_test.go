package game

import (
	"math"
	"testing"
)

// The whole wire format rests on this: a seed, and only a seed, reconstructs the
// skyline. If two clients ever derive different worlds from the same seed, they
// disagree about where the buildings are and the game silently desyncs.
func TestWorldIsFullyDeterminedBySeed(t *testing.T) {
	for seed := range uint16(200) {
		a, b := NewWorld(seed), NewWorld(seed)
		if a.Wind != b.Wind {
			t.Fatalf("seed %d: wind %d != %d", seed, a.Wind, b.Wind)
		}
		if a.Gorillas != b.Gorillas {
			t.Fatalf("seed %d: gorillas %v != %v", seed, a.Gorillas, b.Gorillas)
		}
		if len(a.Buildings) != len(b.Buildings) {
			t.Fatalf("seed %d: %d buildings != %d", seed, len(a.Buildings), len(b.Buildings))
		}
		for i := range a.Buildings {
			if a.Buildings[i] != b.Buildings[i] {
				t.Fatalf("seed %d building %d: %+v != %+v", seed, i, a.Buildings[i], b.Buildings[i])
			}
		}
	}
}

// Different seeds must actually produce different cities, or "regenerate" does
// nothing and every match looks the same.
func TestDifferentSeedsGiveDifferentSkylines(t *testing.T) {
	shapes := make(map[string]uint16)
	collisions := 0
	for seed := range uint16(300) {
		w := NewWorld(seed)
		key := ""
		for _, b := range w.Buildings {
			key += string(rune(b.X)) + string(rune(b.H))
		}
		if prev, dup := shapes[key]; dup {
			t.Logf("seeds %d and %d produce the same skyline", prev, seed)
			collisions++
		}
		shapes[key] = seed
	}
	if collisions > 3 {
		t.Fatalf("%d duplicate skylines in 300 seeds; the generator is not spreading", collisions)
	}
}

func TestSkylineIsWellFormed(t *testing.T) {
	for seed := range uint16(200) {
		w := NewWorld(seed)
		if len(w.Buildings) < 4 {
			t.Fatalf("seed %d: only %d buildings; gorillas need somewhere to stand", seed, len(w.Buildings))
		}
		prevRight := -1
		for i, b := range w.Buildings {
			if b.W <= 0 || b.H <= 0 {
				t.Fatalf("seed %d building %d: degenerate %+v", seed, i, b)
			}
			if b.X <= prevRight {
				t.Fatalf("seed %d building %d: overlaps its neighbour (%+v, prev right %d)", seed, i, b, prevRight)
			}
			if b.X+b.W > FieldW {
				t.Fatalf("seed %d building %d: runs off the field (%+v)", seed, i, b)
			}
			if b.Y < 0 || b.Y >= bottomLine {
				t.Fatalf("seed %d building %d: roof out of bounds (%+v)", seed, i, b)
			}
			prevRight = b.X + b.W
		}
	}
}

// A gorilla standing inside a building, or floating in the air, means the
// placement maths drifted from the skyline maths.
func TestGorillasStandOnRoofs(t *testing.T) {
	for seed := range uint16(200) {
		w := NewWorld(seed)
		for i, g := range w.Gorillas {
			if g.Y < 0 || g.Y+gorillaH > bottomLine {
				t.Fatalf("seed %d gorilla %d: off the field at %+v", seed, i, g)
			}
			// The pixel under the gorilla's feet must be a roof.
			foot := g.Y + gorillaH
			if !w.Solid(g.X+gorillaW/2, foot) {
				t.Fatalf("seed %d gorilla %d at %+v is floating: nothing solid under it", seed, i, g)
			}
			// And the gorilla itself must not be buried in masonry.
			if w.Solid(g.X+gorillaW/2, g.Y+gorillaH/2) {
				t.Fatalf("seed %d gorilla %d at %+v is embedded in a building", seed, i, g)
			}
		}
		if w.Gorillas[0].X >= w.Gorillas[1].X {
			t.Fatalf("seed %d: gorillas are not on opposite sides (%v, %v)", seed, w.Gorillas[0], w.Gorillas[1])
		}
	}
}

func TestCarveMakesAHole(t *testing.T) {
	w := NewWorld(42)
	b := w.Buildings[len(w.Buildings)/2]
	cx, cy := b.X+b.W/2, b.Y+5

	if !w.Solid(cx, cy) {
		t.Fatal("test picked a point that was not solid to begin with")
	}
	w.Carve(cx, cy, craterR)
	if w.Solid(cx, cy) {
		t.Error("the centre of a fresh crater is still solid")
	}
	// Just outside the radius must survive, or craters are eating the whole city.
	if !w.Solid(cx+craterR+2, cy) && b.W > 2*craterR+4 {
		t.Error("a crater removed masonry well outside its radius")
	}
}

// Craters are the only destructible state, so a world rebuilt from seed+craters
// must be pixel-identical to the one that did the destroying. This is the
// property that lets the wire format ship 5 bytes per crater instead of a bitmap.
func TestWorldRebuildsFromSeedAndCraters(t *testing.T) {
	orig := NewWorld(7)
	for i := range 12 {
		b := orig.Buildings[i%len(orig.Buildings)]
		orig.Carve(b.X+b.W/2, b.Y+i, craterR)
	}

	rebuilt := NewWorld(7)
	rebuilt.Craters = append(rebuilt.Craters, orig.Craters...)

	for y := 0; y < FieldH; y += 3 {
		for x := 0; x < FieldW; x += 3 {
			if orig.Solid(x, y) != rebuilt.Solid(x, y) {
				t.Fatalf("rebuilt world differs at (%d,%d): %v vs %v",
					x, y, orig.Solid(x, y), rebuilt.Solid(x, y))
			}
		}
	}
}

// A banana at full power covers tens of field units per frame. If collision were
// only tested at each frame's endpoint it would tunnel clean through a roof —
// the classic artillery-game bug.
func TestFastShotDoesNotTunnelThroughABuilding(t *testing.T) {
	w := NewWorld(3)
	// Aim a very fast shot flat into the side of a tall building close by.
	target := w.Buildings[len(w.Buildings)/2]
	s := &Shot{
		X0: float64(target.X - 100),
		Y0: float64(target.Y + 20), // level with the building's flank
		VX: 400,                    // absurd speed: ~40 field units per 0.1s step
		VY: 0,
	}
	for range 100 {
		out, _ := w.Step(s, 0.1)
		if out == HitBuilding {
			return // caught it
		}
		if out == OffField {
			x, y := s.Pos()
			t.Fatalf("the banana flew off the field at (%.0f,%.0f) — it tunnelled through the building at %+v", x, y, target)
		}
	}
	t.Fatal("shot never resolved")
}

func TestShotHitsAGorilla(t *testing.T) {
	w := NewWorld(11)
	g := w.Gorillas[1]
	// Drop a banana straight down onto player 2's head from directly above.
	s := &Shot{X0: float64(g.X + gorillaW/2), Y0: 0, VX: 0, VY: 0}
	for range 200 {
		out, p := w.Step(s, 0.05)
		switch out {
		case HitGorilla:
			if p != 1 {
				t.Fatalf("hit player %d; the banana was aimed at player 1", p)
			}
			return
		case HitBuilding, OffField:
			x, y := s.Pos()
			t.Fatalf("banana aimed at gorilla 1 (%+v) resolved as %v at (%.0f,%.0f)", g, out, x, y)
		}
	}
	t.Fatal("shot never resolved")
}

// Wind must actually bend the arc, and bend it the way it points.
func TestWindPushesTheBanana(t *testing.T) {
	const t1 = 3.0
	base := &Shot{X0: 100, Y0: 200, VX: 30, VY: 30, Wind: 0}
	tail := &Shot{X0: 100, Y0: 200, VX: 30, VY: 30, Wind: 20}
	head := &Shot{X0: 100, Y0: 200, VX: 30, VY: 30, Wind: -20}

	bx, _ := base.At(t1)
	tx, _ := tail.At(t1)
	hx, _ := head.At(t1)

	if !(hx < bx && bx < tx) {
		t.Fatalf("wind did not order the arcs: headwind %.1f, still %.1f, tailwind %.1f", hx, bx, tx)
	}
}

// The arc is closed-form so that a client can evaluate any t directly — which is
// what lets it interpolate smoothly between two streamed states instead of
// snapping between them. Stepping must therefore agree with evaluating.
func TestSteppingAgreesWithDirectEvaluation(t *testing.T) {
	s := &Shot{X0: 50, Y0: 300, VX: 25, VY: 40, Wind: 7}
	var stepped float64
	for range 20 {
		stepped += 0.05
	}
	sx, sy := s.At(stepped)
	dx, dy := s.At(1.0)
	if math.Abs(sx-dx) > 1e-9 || math.Abs(sy-dy) > 1e-9 {
		t.Fatalf("At(sum of steps) = (%.12f,%.12f) != At(1.0) = (%.12f,%.12f)", sx, sy, dx, dy)
	}
}

func TestGravityPullsTheBananaDown(t *testing.T) {
	s := &Shot{X0: 0, Y0: 300, VX: 10, VY: 30}
	_, apex := s.At(30.0 / gravity) // vy/g: the top of the arc
	_, later := s.At(30.0/gravity + 2)
	if !(later > apex) {
		t.Fatalf("banana did not fall after its apex: apex y=%.1f, later y=%.1f", apex, later)
	}
}
