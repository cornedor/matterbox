package kurve

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
)

// scriptedMatch plays a host match with a fixed steering script, so a test can
// reproduce the same round every time. It returns once a round has been decided
// or the tick budget runs out.
func scriptedMatch(seed uint16) *Match {
	m := NewMatch(seed)
	m.NextSeed = func() uint16 { return seed + 1 }
	m.Join("joinerjoinerjoinerjoinerxx")
	for range countdownTicks + 400 {
		switch m.Sim.Tick {
		case 15:
			m.Steer(0, Right)
			m.Steer(1, Left)
		case 40:
			m.Steer(0, Left)
		case 70:
			m.Steer(1, Right)
		}
		m.Step()
		if m.Phase == PhaseRoundOver || m.Phase == PhaseOver {
			break
		}
	}
	return m
}

// scriptedMatchN is scriptedMatch for an arbitrary player count: it fills the
// lobby with n-1 joiners, starts, and gives every curve a couple of turns so each
// lays a distinct trail worth reconstructing.
func scriptedMatchN(seed uint16, n int) *Match {
	m := NewMatch(seed)
	m.NextSeed = func() uint16 { return seed + 1 }
	for i := 1; i < n; i++ {
		m.AddPlayer(fmt.Sprintf("joiner%020d", i)) // a distinct 26-byte id per joiner
	}
	m.Start()
	for range countdownTicks + 400 {
		switch m.Sim.Tick {
		case 15:
			for p := range n {
				m.Steer(p, Dir((p%2)*2-1)) // even curves left, odd curves right
			}
		case 45:
			for p := range n {
				m.Steer(p, Left)
			}
		}
		m.Step()
		if m.Phase == PhaseRoundOver || m.Phase == PhaseOver {
			break
		}
	}
	return m
}

// The transport has to carry two players or six with equal fidelity: whatever the
// count, the joiner rebuilds exactly the host's trail from the seed and the
// steering logs, and the round-tripped roster matches.
func TestMultiplayerReplayReconstructsTheHostsWorld(t *testing.T) {
	for n := 2; n <= MaxPlayers; n++ {
		for seed := uint16(1); seed < 12; seed++ {
			host := scriptedMatchN(seed, n)
			st := UnmarshalStateOrDie(t, MarshalState(WireState(host)))
			if len(st.Scores) != n || len(st.Joiners) != n-1 || len(st.Deaths) != n || len(st.Turns) != n {
				t.Fatalf("n %d seed %d: round-tripped a %d-player roster (%d joiners)", n, seed, len(st.Scores), len(st.Joiners))
			}

			replay := FromState(st)
			if !bytes.Equal(host.Sim.grid, replay.Sim.grid) {
				t.Fatalf("n %d seed %d: replayed trail differs from the host's", n, seed)
			}
			for i := range host.Sim.Curves {
				if host.Sim.Curves[i].Dead != replay.Sim.Curves[i].Dead ||
					host.Sim.Curves[i].DeathTick != replay.Sim.Curves[i].DeathTick {
					t.Fatalf("n %d seed %d curve %d: replay disagrees on life/death", n, seed, i)
				}
			}
		}
	}
}

// The state has to survive the round trip its whole existence depends on:
// whatever the host writes, the joiner must read back byte-for-byte.
func TestStateRoundTrips(t *testing.T) {
	m := scriptedMatch(4242)
	st := WireState(m)

	got, err := UnmarshalState(MarshalState(st))
	if err != nil {
		t.Fatalf("the joiner cannot parse the host's state: %v", err)
	}
	if got.Seed != st.Seed || got.Phase != st.Phase || got.Tick != st.Tick ||
		got.Countdown != st.Countdown || got.Winner != st.Winner ||
		!slices.Equal(got.Scores, st.Scores) || !slices.Equal(got.Joiners, st.Joiners) ||
		!slices.Equal(got.Deaths, st.Deaths) {
		t.Fatalf("scalar state changed:\n got %+v\nwant %+v", got, st)
	}
	for p := range st.Turns {
		if len(got.Turns[p]) != len(st.Turns[p]) {
			t.Fatalf("player %d: %d turns, want %d", p, len(got.Turns[p]), len(st.Turns[p]))
		}
		for i := range st.Turns[p] {
			if got.Turns[p][i] != st.Turns[p][i] {
				t.Fatalf("player %d turn %d: %+v != %+v", p, i, got.Turns[p][i], st.Turns[p][i])
			}
		}
	}
}

// The heart of the transport: a joiner that only ever sees the seed and the
// steering logs must rebuild exactly the trail the host drew. If the replay
// diverges by a single cell the two players are looking at different games.
func TestReplayReconstructsTheHostsWorld(t *testing.T) {
	for seed := uint16(1); seed < 40; seed++ {
		host := scriptedMatch(seed)
		replay := FromState(UnmarshalStateOrDie(t, MarshalState(WireState(host))))

		if !bytes.Equal(host.Sim.grid, replay.Sim.grid) {
			t.Fatalf("seed %d: replayed trail differs from the host's", seed)
		}
		for i := range host.Sim.Curves {
			if host.Sim.Curves[i].Dead != replay.Sim.Curves[i].Dead ||
				host.Sim.Curves[i].DeathTick != replay.Sim.Curves[i].DeathTick {
				t.Fatalf("seed %d curve %d: replay disagrees on life/death", seed, i)
			}
		}
	}
}

func UnmarshalStateOrDie(t *testing.T, b []byte) *State {
	t.Helper()
	st, err := UnmarshalState(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return st
}

// A payload from a version this build does not speak, or one truncated in
// transit, must be refused outright rather than read as a half-world.
func TestUnmarshalRejectsBadPayloads(t *testing.T) {
	good := MarshalState(WireState(scriptedMatch(5)))

	bad := bytes.Clone(good)
	bad[0] = 99
	if _, err := UnmarshalState(bad); err != ErrUnknownVersion {
		t.Fatalf("bad version: got %v, want ErrUnknownVersion", err)
	}
	if _, err := UnmarshalState(good[:8]); err != ErrShortPayload {
		t.Fatalf("truncated: got %v, want ErrShortPayload", err)
	}
	if _, err := UnmarshalState(nil); err != ErrShortPayload {
		t.Fatalf("empty: got %v, want ErrShortPayload", err)
	}
}

// The controller is tiny but it is the joiner's entire voice; it has to round
// trip too, and never be mistaken for a state (they differ by length alone).
func TestInputRoundTripsAndIsDistinctFromState(t *testing.T) {
	in := &Input{Dir: Left, Seq: 7}
	got, err := UnmarshalInput(MarshalInput(in))
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	if got.Dir != in.Dir || got.Seq != in.Seq {
		t.Fatalf("input changed: %+v != %+v", got, in)
	}
	// A state must never parse as an input, or the host would read a whole world
	// as a single steering nudge.
	if len(MarshalInput(in)) >= len(MarshalState(WireState(scriptedMatch(1)))) {
		t.Fatal("an input is not shorter than a state; the two are confusable")
	}
}
