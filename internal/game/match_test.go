package game

import "testing"

// aimAt brute-forces an angle and power that hits the given player, so the rule
// tests can land a banana on demand without hand-tuned magic numbers that would
// rot the moment a constant moves.
func aimAt(t *testing.T, seed uint16, shooter, target int) (uint8, uint8) {
	t.Helper()
	for angle := 5; angle <= 89; angle++ {
		for power := 20; power <= 200; power += 2 {
			m := NewMatch(seed)
			m.Join("someone")
			m.Launch(shooter, uint8(angle), uint8(power))
			for range 400 {
				ev := m.Step(gorillasTestDT)
				switch ev.Kind {
				case EvFlying:
					continue
				case EvRound, EvMatch:
					if ev.Hit == target {
						return uint8(angle), uint8(power)
					}
				}
				break
			}
		}
	}
	t.Fatalf("seed %d: found no shot from player %d that hits player %d", seed, shooter, target)
	return 0, 0
}

const gorillasTestDT = 0.05

// A banana that lands on the other gorilla scores for the shooter, resets the
// city, and hands the first shot to the player who was hit.
func TestHitScoresAndStartsANewRound(t *testing.T) {
	const seed = 42
	angle, power := aimAt(t, seed, 0, 1)

	m := NewMatch(seed)
	m.NextSeed = func() uint16 { return 1234 }
	m.Join("joiner")
	m.Launch(0, angle, power)

	var ev Event
	for range 400 {
		if ev = m.Step(gorillasTestDT); ev.Kind != EvFlying {
			break
		}
	}
	if ev.Kind != EvRound {
		t.Fatalf("got %v, want EvRound", ev.Kind)
	}
	if ev.Hit != 1 || ev.Scorer != 0 {
		t.Fatalf("hit=%d scorer=%d; player 0 shot player 1", ev.Hit, ev.Scorer)
	}
	if ev.Self {
		t.Error("Self set on a shot that hit the opponent")
	}
	if m.State.Scores != [2]uint8{1, 0} {
		t.Fatalf("scores %v, want [1 0]", m.State.Scores)
	}
	if m.State.Seed != 1234 {
		t.Errorf("the city was not rebuilt: seed still %d", m.State.Seed)
	}
	if len(m.State.Craters) != 0 {
		t.Errorf("the new round kept %d craters from the old city", len(m.State.Craters))
	}
	if m.State.Turn != 1 {
		t.Errorf("turn %d; the player who was hit shoots first", m.State.Turn)
	}
	if m.State.Phase != PhaseAiming {
		t.Errorf("phase %v, want PhaseAiming", m.State.Phase)
	}
}

// Hitting yourself scores for your opponent. This is the funniest outcome in the
// game and it had better work.
func TestSelfHitScoresForTheOpponent(t *testing.T) {
	const seed = 7
	angle, power := aimAt(t, seed, 0, 0) // player 0 hits player 0

	m := NewMatch(seed)
	m.NextSeed = func() uint16 { return 99 }
	m.Join("joiner")
	m.Launch(0, angle, power)

	var ev Event
	for range 400 {
		if ev = m.Step(gorillasTestDT); ev.Kind != EvFlying {
			break
		}
	}
	if ev.Kind != EvRound {
		t.Fatalf("got %v, want EvRound", ev.Kind)
	}
	if !ev.Self {
		t.Error("Self not set on a shot that hit the shooter")
	}
	if ev.Scorer != 1 {
		t.Fatalf("scorer=%d; player 0 hit themselves, so player 1 scores", ev.Scorer)
	}
	if m.State.Scores != [2]uint8{0, 1} {
		t.Fatalf("scores %v, want [0 1]", m.State.Scores)
	}
}

// Every shot must end, and end in a bounded number of frames. Each frame is a
// PATCH, so a banana that never comes down is not just a stuck turn — it is an
// unbounded stream of edits at the server. The pathological case is straight up
// at full power, which apexes around 26 simulated seconds.
func TestEveryShotTerminatesWithinTheFlightBudget(t *testing.T) {
	maxFrames := int(MaxFlightTime/gorillasTestDT) + 2

	for _, shot := range [][2]uint8{
		{89, 255}, // straight up, full power: the worst case
		{90, 255},
		{1, 255}, // flat out
		{45, 1},  // barely a lob
		{0, 0},   // no power at all
	} {
		m := NewMatch(3)
		m.Join("joiner")
		m.Launch(0, shot[0], shot[1])

		frames := 0
		var ev Event
		for range maxFrames + 50 {
			ev = m.Step(gorillasTestDT)
			if ev.Kind != EvFlying {
				break
			}
			frames++
		}
		if ev.Kind == EvFlying {
			t.Fatalf("angle=%d power=%d: still flying after %d frames", shot[0], shot[1], frames)
		}
		if frames > maxFrames {
			t.Fatalf("angle=%d power=%d: took %d frames, budget is %d", shot[0], shot[1], frames, maxFrames)
		}
	}
}

func TestMissHandsOverTheTurn(t *testing.T) {
	m := NewMatch(3)
	m.Join("joiner")
	m.Launch(0, 89, 255) // straight up and far away

	var ev Event
	for range 500 {
		if ev = m.Step(gorillasTestDT); ev.Kind != EvFlying {
			break
		}
	}
	if ev.Kind != EvMiss && ev.Kind != EvBuilding {
		t.Fatalf("got %v, want a miss or a building hit", ev.Kind)
	}
	if m.State.Turn != 1 {
		t.Errorf("turn %d after player 0 shot; want 1", m.State.Turn)
	}
	if m.State.Phase != PhaseAiming {
		t.Errorf("phase %v, want PhaseAiming", m.State.Phase)
	}
	if m.State.Shot != nil {
		t.Error("a resolved shot is still on the wire")
	}
	if m.State.Scores != [2]uint8{0, 0} {
		t.Errorf("a miss scored: %v", m.State.Scores)
	}
}

// A whole match, played to its end. WinScore points and it is over — and once it
// is over, nothing further moves.
func TestMatchEndsAtWinScore(t *testing.T) {
	const seed = 42
	seeds := []uint16{seed, seed, seed, seed}
	i := 0
	next := func() uint16 { i++; return seeds[min(i, len(seeds)-1)] }

	m := NewMatch(seed)
	m.NextSeed = next
	m.Join("joiner")

	angle, power := aimAt(t, seed, 0, 1)

	var last Event
	for shots := 0; shots < 10 && m.State.Phase != PhaseOver; shots++ {
		// Player 0 keeps landing the same shot on the same city; after each round
		// the turn goes to player 1, so give it back to player 0.
		m.State.Turn = 0
		m.Launch(0, angle, power)
		for range 400 {
			if last = m.Step(gorillasTestDT); last.Kind != EvFlying {
				break
			}
		}
	}

	if m.State.Phase != PhaseOver {
		t.Fatalf("match never ended; scores %v", m.State.Scores)
	}
	if last.Kind != EvMatch {
		t.Fatalf("final event %v, want EvMatch", last.Kind)
	}
	if m.State.Winner != 0 {
		t.Fatalf("winner %d, want 0", m.State.Winner)
	}
	if m.State.Scores[0] != WinScore {
		t.Fatalf("winner has %d points, want %d", m.State.Scores[0], WinScore)
	}
	// A finished match is inert.
	if ev := m.Step(gorillasTestDT); ev.Kind != EvNothing {
		t.Errorf("a finished match still produced %v", ev.Kind)
	}
}

func TestLobbyDoesNotSimulate(t *testing.T) {
	m := NewMatch(1)
	if ev := m.Step(gorillasTestDT); ev.Kind != EvNothing {
		t.Fatalf("a lobby produced %v", ev.Kind)
	}
	if m.State.Phase != PhaseLobby {
		t.Fatal("the lobby started itself")
	}
	m.Join("joiner")
	if m.State.Phase != PhaseAiming || m.State.Joiner != "joiner" {
		t.Fatalf("join did not start the match: %+v", m.State)
	}
	// A second person cannot barge in.
	m.Join("interloper")
	if m.State.Joiner != "joiner" {
		t.Fatalf("a second joiner took the seat: %q", m.State.Joiner)
	}
}

func TestMyTurn(t *testing.T) {
	m := NewMatch(1)
	m.Join("joiner")
	if !m.MyTurn(0) || m.MyTurn(1) {
		t.Fatal("player 0 opens")
	}
	// Nobody may fire while a banana is up.
	m.Launch(0, 45, 90)
	if m.MyTurn(0) || m.MyTurn(1) {
		t.Fatal("a player may fire mid-flight")
	}
}

// The joiner rebuilds the match from what the host streamed. Whatever the host is
// looking at, they must be looking at too — including the banana's position.
func TestFromStateReconstructsTheHostsMatch(t *testing.T) {
	host := NewMatch(1234)
	host.Join("joiner")
	host.Launch(0, 50, 100)
	for range 8 {
		host.Step(gorillasTestDT)
	}

	// Round-trip the state exactly as the wire would.
	blob, err := UnmarshalState(MarshalState(host.State))
	if err != nil {
		t.Fatal(err)
	}
	joiner := FromState(blob)

	if joiner.Shot == nil {
		t.Fatal("the joiner sees no banana while one is in the air")
	}
	hx, hy := host.Shot.Pos()
	jx, jy := joiner.Shot.Pos()
	if diff := abs(hx-jx) + abs(hy-jy); diff > 4 {
		t.Fatalf("joiner's banana at (%.0f,%.0f), host's at (%.0f,%.0f)", jx, jy, hx, hy)
	}
	if len(joiner.World.Buildings) != len(host.World.Buildings) {
		t.Fatal("the joiner rebuilt a different city")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
