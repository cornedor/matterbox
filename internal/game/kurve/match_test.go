package kurve

import (
	"image/color"
	"testing"
)

// A match has to actually end. Two curves with different fates — one circling,
// one running straight — resolve a decisive round almost every time, so the score
// climbs to the winning post and the match reaches PhaseOver rather than looping.
func TestMatchReachesAWinner(t *testing.T) {
	m := NewMatch(2024)
	m.NextSeed = func() uint16 { return m.Sim.Seed + 7 } // vary each round, deterministically
	m.Join("joinerjoinerjoinerjoinerxx")

	for range 12000 {
		m.Steer(0, Left) // curve 0 circles; curve 1 runs straight into a wall
		m.Step()
		if m.Phase == PhaseOver {
			break
		}
	}

	if m.Phase != PhaseOver {
		t.Fatalf("match never ended: phase %d, scores %v", m.Phase, m.Scores)
	}
	if m.Winner < 0 {
		t.Fatal("match over but no winner recorded")
	}
	if m.Scores[m.Winner] != WinScore {
		t.Fatalf("winner has %d points, want the winning score %d", m.Scores[m.Winner], WinScore)
	}
}

// A round-ending death has to award the point to the survivor, and a mutual death
// to nobody. Drive both curves in a tight circle from the same seed; if they die
// on the same tick it is a draw, otherwise the survivor scores — either way the
// score bookkeeping must be self-consistent.
func TestRoundScoresTheSurvivor(t *testing.T) {
	m := NewMatch(88)
	m.NextSeed = func() uint16 { return 88 }
	m.Join("x")

	var ev Event
	for range countdownTicks + MaxRoundTicks {
		m.Steer(0, Left)
		m.Steer(1, Right)
		ev = m.Step()
		if ev.Kind == EvRound || ev.Kind == EvMatch {
			break
		}
	}
	if ev.Kind != EvRound && ev.Kind != EvMatch {
		t.Fatal("no round resolved")
	}
	total := int(m.Scores[0]) + int(m.Scores[1])
	if ev.Draw {
		if total != 0 {
			t.Fatalf("a draw scored a point: %v", m.Scores)
		}
	} else if total != 1 {
		t.Fatalf("a decisive round scored %d points, want 1: %v", total, m.Scores)
	}
}

// The whole point of the multiplayer change: a crash no longer ends the round.
// With three curves in play, a round must keep running after the first one dies
// and only resolve once a single survivor is left — who takes the one point (or
// nobody, on a mutual wipe-out).
func TestRoundEndsOnLastSurvivorNotFirstCrash(t *testing.T) {
	sawCrashMidRound := false
	for seed := uint16(1); seed <= 40; seed++ {
		m := NewMatch(seed)
		m.NextSeed = func() uint16 { return seed }
		m.AddPlayer("p1")
		m.AddPlayer("p2")
		if !m.Start() {
			t.Fatalf("seed %d: three-player match refused to start", seed)
		}

		var ev Event
		for range countdownTicks + MaxRoundTicks {
			m.Steer(0, Left) // all three circle on their own patch of arena
			m.Steer(1, Left)
			m.Steer(2, Left)
			ev = m.Step()
			// A curve has crashed but the round is still live: proof it did not end
			// on the first crash.
			if m.Phase == PhaseRun && m.aliveCount() < len(m.Sim.Curves) {
				sawCrashMidRound = true
			}
			if ev.Kind == EvRound || ev.Kind == EvMatch {
				break
			}
		}

		if ev.Kind != EvRound && ev.Kind != EvMatch {
			continue // this seed's round hit the cap; scoring is checked on the ones that resolve
		}
		total := int(m.Scores[0]) + int(m.Scores[1]) + int(m.Scores[2])
		if ev.Draw {
			if total != 0 {
				t.Fatalf("seed %d: a draw scored a point: %v", seed, m.Scores)
			}
		} else if total != 1 {
			t.Fatalf("seed %d: a decisive round scored %d points, want 1: %v", seed, total, m.Scores)
		}
	}
	if !sawCrashMidRound {
		t.Fatal("no round ever continued past a crash — rounds are still ending on the first crash")
	}
}

// The renderer must actually paint the world: an arena, a wall, and — once the
// curves have moved — trail in both players' colours.
func TestRenderPaintsTheWorld(t *testing.T) {
	m := NewMatch(5)
	m.Join("x")
	for range 80 {
		m.Steer(0, Left)
		m.Steer(1, Right)
		m.Step()
	}

	var r Renderer
	img := r.Render(m.Sim, m.Phase, m.Countdown, 480, 360)
	if img == nil || img.Rect.Dx() != 480 || img.Rect.Dy() != 360 {
		t.Fatalf("render returned a %v image", img.Rect)
	}

	seen := map[color.RGBA]int{}
	for y := range 360 {
		for x := range 480 {
			r, g, b, _ := img.At(x, y).RGBA()
			seen[color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 0xff}]++
		}
	}
	for _, want := range []struct {
		name string
		c    color.RGBA
	}{
		{"arena", colArena},
		{"wall", colWall},
		{"player 0 trail", colTrail[0]},
		{"player 1 trail", colTrail[1]},
	} {
		if seen[want.c] == 0 {
			t.Errorf("the render shows no %s pixels (%v)", want.name, want.c)
		}
	}
}
