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

// resolve flies a banana until it stops, and returns the event that stopped it.
//
// The world has *not* finished changing when this returns: the fireball is still
// burning, and until it has collapsed there is no crater and the round has not
// turned over. That is the original's order of events, and settle is what waits
// for it.
func resolve(m *Match) Event {
	var ev Event
	// The flight budget is MaxFlightTime, and a banana lobbed straight up at full
	// power uses nearly all of it, so this has to outlast that.
	for range int(MaxFlightTime/gorillasTestDT) + 50 {
		if ev = m.Step(gorillasTestDT); ev.Kind != EvFlying {
			return ev
		}
	}
	return ev
}

// settle runs out whatever is still animating — the fireball, and then the
// victory dance a dead gorilla earns the other one. That is what cuts the crater
// and hands the turn or the round on.
func settle(m *Match) {
	for range gorillaBoomFrames + danceFrames + 10 {
		if m.Boom == nil && m.Dance == nil {
			return
		}
		m.Step(gorillasTestDT)
	}
}

// A banana that lands on the other gorilla scores for the shooter, resets the
// city, and hands the first shot to the player who was hit.
func TestHitScoresAndStartsANewRound(t *testing.T) {
	const seed = 42
	angle, power := aimAt(t, seed, 0, 1)

	m := NewMatch(seed)
	m.NextSeed = func() uint16 { return 1234 }
	m.Join("joiner")
	m.Launch(0, angle, power)

	ev := resolve(m)
	if ev.Kind != EvRound {
		t.Fatalf("got %v, want EvRound", ev.Kind)
	}

	// The point is scored the moment the banana lands, but the city is not: the
	// blast has to finish collapsing first, and while it does, the old city is
	// still standing.
	if m.State.Seed != seed {
		t.Errorf("the city was replaced while the fireball was still burning")
	}
	if m.State.Phase != PhaseBoom {
		t.Errorf("phase %v during the blast, want PhaseBoom", m.State.Phase)
	}
	if m.MyTurn(0) || m.MyTurn(1) {
		t.Error("a player may fire while a gorilla is exploding")
	}
	settle(m)

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

	ev := resolve(m)
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

	ev := resolve(m)
	if ev.Kind != EvMiss && ev.Kind != EvBuilding {
		t.Fatalf("got %v, want a miss or a building hit", ev.Kind)
	}
	settle(m) // a building hit leaves a fireball; the turn passes when it is out
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
		last = resolve(m)
		settle(m)
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

// The crater is cut by the fireball collapsing, not by the banana landing.
//
// This is the load-bearing detail of both explosions. In GORILLA.BAS the hole is
// a side effect of the erase pass that cleans the blast off the screen, so for as
// long as the blast is burning the masonry it is about to eat is still standing —
// and still on screen, around it. Carve on impact instead and the skyline opens up
// a beat before the explosion that is supposed to have destroyed it, which on a
// gorilla hit is unmissable: its crater is half again wider than its fireball ever
// gets.
func TestTheCraterIsCutWhenTheFireballDies(t *testing.T) {
	m := NewMatch(3)
	m.Join("joiner")
	m.Launch(0, 45, 70)

	ev := resolve(m)
	if ev.Kind != EvBuilding {
		t.Skipf("this shot did not hit a building (%v); the invariant is tested on the one that does", ev.Kind)
	}
	if m.Boom == nil {
		t.Fatal("a banana hit masonry and nothing is burning")
	}
	if len(m.State.Craters) != 0 {
		t.Fatalf("the crater was cut on impact: %d craters while the fireball is still burning",
			len(m.State.Craters))
	}
	// And the masonry it landed on is still there.
	if !m.World.Solid(m.Boom.X, m.Boom.Y) {
		t.Error("the building under the fireball is already gone")
	}

	settle(m)

	if len(m.State.Craters) != 1 {
		t.Fatalf("the fireball burned out and left %d craters, want 1", len(m.State.Craters))
	}
	c := m.State.Craters[0]
	if m.World.Solid(int(c.X), int(c.Y)) {
		t.Error("the fireball burned out and the masonry survived")
	}
	if m.State.Phase != PhaseAiming || m.State.Turn != 1 {
		t.Errorf("the turn did not pass when the blast went out: phase %v turn %d",
			m.State.Phase, m.State.Turn)
	}
}

// A gorilla's crater is the tall ellipse ExplodeGorilla's aspect of -1.57 makes of
// it, and it is a great deal bigger than a banana's — that is the difference
// between chipping a roof and taking the top off the building.
func TestGorillaCraterIsTallAndLarge(t *testing.T) {
	const seed = 42
	angle, power := aimAt(t, seed, 0, 1)

	m := NewMatch(seed)
	m.NextSeed = func() uint16 { return seed } // same city, so the crater survives the round
	m.Join("joiner")
	m.Launch(0, angle, power)

	if ev := resolve(m); ev.Kind != EvRound {
		t.Fatalf("got %v, want EvRound", ev.Kind)
	}
	if m.Boom.Kind != BoomGorilla {
		t.Fatalf("a direct hit lit a %v, want BoomGorilla", m.Boom.Kind)
	}
	rx, ry := m.Boom.Crater()
	if ry <= rx {
		t.Errorf("the gorilla's crater is %.0f×%.0f; ExplodeGorilla's blast is taller than it is wide", rx, ry)
	}
	if rx <= craterRX {
		t.Errorf("a dead gorilla (%.0f) craters no wider than a banana (%d)", rx, craterRX)
	}
}

// The winner dances on the wreckage, and the next city does not go up until they
// are done. DoShot calls VictoryDance after every round-ending hit — before
// PlayGame clears the screen — so the celebration happens on the cratered city,
// not on the fresh one.
func TestTheWinnerDancesBeforeTheNextCityGoesUp(t *testing.T) {
	const seed = 42
	angle, power := aimAt(t, seed, 0, 1)

	m := NewMatch(seed)
	m.NextSeed = func() uint16 { return 1234 }
	m.Join("joiner")
	m.Launch(0, angle, power)

	if ev := resolve(m); ev.Kind != EvRound {
		t.Fatalf("got %v, want EvRound", ev.Kind)
	}
	// Burn the fireball down, but no further.
	for m.Boom != nil {
		m.Step(gorillasTestDT)
	}

	if m.Dance == nil {
		t.Fatal("the fireball went out and nobody is dancing")
	}
	if m.Dance.Player != 0 {
		t.Errorf("player %d is dancing; player 0 won the round", m.Dance.Player)
	}
	if m.State.Phase != PhaseDance {
		t.Errorf("phase %v while dancing, want PhaseDance", m.State.Phase)
	}
	if m.MyTurn(0) || m.MyTurn(1) {
		t.Error("a player may fire during the victory dance")
	}
	if m.State.Seed != seed {
		t.Error("the next city went up while the winner was still celebrating")
	}
	if len(m.State.Craters) != 1 {
		t.Errorf("the winner is dancing on %d craters; the blast left one", len(m.State.Craters))
	}

	// The dance alternates the raised arm, which is the whole of it.
	poses := map[gorillaPose]bool{}
	for m.Dance != nil {
		poses[m.Dance.pose()] = true
		m.Step(gorillasTestDT)
	}
	if !poses[leftUp] || !poses[rightUp] {
		t.Errorf("the ape did not alternate arms: %v", poses)
	}

	// And only now does the world move on.
	if m.State.Seed != 1234 || m.State.Phase != PhaseAiming || m.State.Turn != 1 {
		t.Errorf("the round did not turn over when the dance ended: seed %d phase %v turn %d",
			m.State.Seed, m.State.Phase, m.State.Turn)
	}
}

// A won match dances too — it just has no round to hand on to afterwards, and the
// loser stays in the hole.
func TestTheMatchWinnerDancesAndTheLoserStaysDown(t *testing.T) {
	const seed = 42
	angle, power := aimAt(t, seed, 0, 1)

	m := NewMatch(seed)
	m.NextSeed = func() uint16 { return seed }
	m.Join("joiner")
	for range WinScore {
		m.State.Turn = 0
		m.Launch(0, angle, power)
		resolve(m)
		settle(m)
	}

	if m.State.Phase != PhaseOver || m.State.Winner != 0 {
		t.Fatalf("match did not end with player 0 winning: phase %v winner %d",
			m.State.Phase, m.State.Winner)
	}
	if m.Dance != nil {
		t.Error("the winner is still dancing after the match settled")
	}
	if !m.World.Dead[1] {
		t.Error("the loser got back up out of their crater")
	}
	if m.World.Dead[0] {
		t.Error("the winner is dead")
	}
	// And a finished match is inert.
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
