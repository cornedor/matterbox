package kurve

import (
	"bytes"
	"slices"
	"testing"
)

// countOwned counts the trail cells belonging to one curve (1-based owner).
func countOwned(s *Sim, owner uint8) int {
	n := 0
	for _, o := range s.grid {
		if o == owner {
			n++
		}
	}
	return n
}

// The whole wire format rests on this: a seed, and only a seed, fixes the opening
// position and the gap schedule. If two clients derive different setups from the
// same seed they disagree about the world before a curve has even moved.
func TestSetupIsFullyDeterminedBySeed(t *testing.T) {
	for seed := range uint16(300) {
		// Vary the player count with the seed too, so determinism is checked across
		// the whole two-to-six range, not just two.
		n := 2 + int(seed)%(MaxPlayers-1)
		a, b := NewSim(seed, n), NewSim(seed, n)
		// Curve holds a slice (recent), so a struct == won't compile — compare the
		// fields that actually define the opening.
		for i := range a.Curves {
			if a.Curves[i].X != b.Curves[i].X || a.Curves[i].Y != b.Curves[i].Y || a.Curves[i].Head != b.Curves[i].Head {
				t.Fatalf("seed %d curve %d: opening differs", seed, i)
			}
		}
		if !slices.Equal(a.gapOffset, b.gapOffset) {
			t.Fatalf("seed %d: gap offsets differ %v vs %v", seed, a.gapOffset, b.gapOffset)
		}
	}
}

// Curves must open on the field, not in a wall, and spread apart so the match is
// a contest rather than an instant pile-up — whatever the player count. The
// classic two-player game keeps its convention too: curve 0 on the left, curve 1
// on the right.
func TestCurvesOpenWellPlaced(t *testing.T) {
	for seed := range uint16(300) {
		for n := 2; n <= MaxPlayers; n++ {
			s := NewSim(seed, n)
			for i, c := range s.Curves {
				ww := float64(wallWidth)
				if c.X < ww || c.X >= FieldW-ww || c.Y < ww || c.Y >= FieldH-ww {
					t.Fatalf("seed %d n %d curve %d opens in or past the wall at (%.0f,%.0f)", seed, n, i, c.X, c.Y)
				}
			}
			// No two heads may open on the same cell, or two players would die on
			// the first tick they both draw.
			for i := range s.Curves {
				for j := i + 1; j < len(s.Curves); j++ {
					if int(s.Curves[i].X) == int(s.Curves[j].X) && int(s.Curves[i].Y) == int(s.Curves[j].Y) {
						t.Fatalf("seed %d n %d: curves %d and %d open on the same cell", seed, n, i, j)
					}
				}
			}
		}
		if s := NewSim(seed, 2); s.Curves[0].X >= s.Curves[1].X {
			t.Fatalf("seed %d: two-player curves not on opposite sides (%.0f, %.0f)", seed, s.Curves[0].X, s.Curves[1].X)
		}
	}
}

// The neck must not kill: a curve sits on the trail it just laid every single
// step, so without immunity it would die on its second move. And a curve that
// keeps turning the same way must eventually close a loop onto its own tail and
// die — otherwise a player could stall forever by circling.
func TestNeckImmunityThenSelfCollision(t *testing.T) {
	s := NewSim(31, 2)
	death := -1
	for tick := range 200 {
		s.Steer(0, Left)
		s.Steer(1, Left) // both circle on their own side; they can't reach each other
		s.Step()
		if s.Curves[0].Dead && death < 0 {
			death = tick
		}
	}
	if death < 0 {
		t.Fatal("a curve circling in place never died on its own trail")
	}
	// It must clear its own neck — a false death there shows up as dying within a
	// couple of steps, long before the loop can possibly close.
	if death < 5 {
		t.Fatalf("curve died at tick %d — inside the neck; immunity is not protecting it", death)
	}
}

// A curve driven straight in a bounded arena has to hit a wall.
func TestStraightRunHitsAWall(t *testing.T) {
	s := NewSim(7, 2)
	for range 300 {
		s.Step() // no steering: both go straight
		if s.Curves[0].Dead {
			return
		}
	}
	t.Fatalf("curve 0 never hit a wall going straight (head at %.0f,%.0f)", s.Curves[0].X, s.Curves[0].Y)
}

// During a gap the curve lays no trail — that is the whole point of a gap, the
// hole a cornered player threads through.
func TestGapsLeaveNoTrail(t *testing.T) {
	s := NewSim(3, 2)
	sawGap, sawDraw := false, false
	for range gapEvery * 2 {
		drawing := s.drawing(0)
		before := countOwned(s, 1)
		s.Step()
		after := countOwned(s, 1)
		if s.Curves[0].Dead {
			break
		}
		if drawing {
			sawDraw = true
		} else {
			sawGap = true
			if after != before {
				t.Fatalf("curve drew %d cells during a gap tick", after-before)
			}
		}
	}
	if !sawGap || !sawDraw {
		t.Fatalf("expected both gap and drawing ticks in two cycles (gap=%v draw=%v)", sawGap, sawDraw)
	}
}

// Same seed, same steering, same result — the property a replay depends on.
func TestSteppingIsDeterministic(t *testing.T) {
	play := func() *Sim {
		s := NewSim(99, 2)
		for tick := range 120 {
			switch tick {
			case 10:
				s.Steer(0, Right)
			case 25:
				s.Steer(1, Left)
			case 60:
				s.Steer(0, Left)
			}
			s.Step()
		}
		return s
	}
	a, b := play(), play()
	if !bytes.Equal(a.grid, b.grid) {
		t.Fatal("two identical playthroughs produced different trails")
	}
	for i := range a.Curves {
		if a.Curves[i].Dead != b.Curves[i].Dead || a.Curves[i].DeathTick != b.Curves[i].DeathTick {
			t.Fatalf("curve %d resolved differently across identical playthroughs", i)
		}
	}
}
