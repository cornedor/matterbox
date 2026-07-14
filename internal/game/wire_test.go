package game

import (
	"math"
	"reflect"
	"testing"
	"testing/quick"
)

func sampleState() *State {
	return &State{
		Seed:   40000,
		Wind:   -13,
		Phase:  PhaseFlight,
		Turn:   1,
		Scores: [2]uint8{3, 2},
		Winner: -1,
		Joiner: "abcdefghijklmnopqrstuvwxyz", // a full-length Mattermost id
		Shot:   &ShotWire{Angle: 137, Power: 88, T: 1234},
		Boom:   &BoomWire{X: 120, Y: 240, Kind: uint8(BoomGorilla), Frame: 5},
		Dance:  &DanceWire{Player: 1, Frame: 9},
		SunHit: true,
		Craters: []Crater{
			{X: 0, Y: 0, RX: 7, RY: 5},
			{X: 639, Y: 349, RX: 48, RY: 75},
			{X: 320, Y: 175, RX: 255, RY: 255},
		},
	}
}

func TestStateRoundTrip(t *testing.T) {
	want := sampleState()
	got, err := UnmarshalState(MarshalState(want))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip changed the state:\n got %+v (shot %+v)\nwant %+v (shot %+v)",
			got, got.Shot, want, want.Shot)
	}
}

func TestStateRoundTripThroughTheBlob(t *testing.T) {
	want := sampleState()
	body := "\U0001F4A3 Gorillas\n```\nboard\n```\n" + Encode(MarshalState(want))
	payload, ok := Decode(body)
	if !ok {
		t.Fatal("no payload found in the post body")
	}
	got, err := UnmarshalState(payload)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the blob corrupted the state:\n got %+v\nwant %+v", got, want)
	}
}

func TestStateWithoutAShot(t *testing.T) {
	want := sampleState()
	want.Shot = nil
	want.Phase = PhaseAiming
	got, err := UnmarshalState(MarshalState(want))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Shot != nil {
		t.Fatalf("a state with no shot came back carrying one: %+v", got.Shot)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLobbyStateHasNoJoiner(t *testing.T) {
	want := &State{Seed: 1, Phase: PhaseLobby, Winner: -1}
	got, err := UnmarshalState(MarshalState(want))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Joiner != "" {
		t.Fatalf("lobby state came back with a joiner %q", got.Joiner)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A truncated or corrupt payload must be an error, never a half-populated state:
// a partially-read world would render as a plausible-looking lie rather than an
// obvious failure.
func TestUnmarshalStateRejectsBadPayloads(t *testing.T) {
	full := MarshalState(sampleState())

	for _, tc := range []struct {
		name string
		b    []byte
		want error
	}{
		{"empty", nil, ErrShortPayload},
		{"header only", full[:5], ErrShortPayload},
		{"truncated mid-id", full[:20], ErrShortPayload},
		{"truncated mid-crater", full[:len(full)-3], ErrShortPayload},
	} {
		if _, err := UnmarshalState(tc.b); err == nil {
			t.Errorf("%s: accepted a bad payload", tc.name)
		}
	}

	// An unknown version must be refused outright, not misread as v1.
	future := append([]byte(nil), full...)
	future[0] = StateVer + 1
	if _, err := UnmarshalState(future); err != ErrUnknownVersion {
		t.Errorf("future version: got %v, want ErrUnknownVersion", err)
	}

	// A crater count larger than the cap must not be trusted enough to allocate on.
	// Built from a shot-less, crater-less state so the count is simply the last
	// byte, rather than hard-coding an offset that a format change would rot.
	bogus := MarshalState(&State{Seed: 1, Winner: -1})
	bogus[len(bogus)-1] = 255
	if _, err := UnmarshalState(bogus); err != ErrTooManyCraters {
		t.Errorf("out-of-range crater count: got %v, want ErrTooManyCraters", err)
	}
}

func TestInputRoundTrip(t *testing.T) {
	want := &Input{Angle: 45, Power: 200, Seq: 17}
	got, err := UnmarshalInput(MarshalInput(want))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if *got != *want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if _, err := UnmarshalInput([]byte{InputVer, 1}); err == nil {
		t.Error("accepted a truncated input")
	}
	if _, err := UnmarshalInput([]byte{InputVer + 1, 1, 2, 3}); err != ErrUnknownVersion {
		t.Error("accepted an unknown input version")
	}
}

// The budget claim: a full match's state stays a small fraction of Mattermost's
// 16383-rune post limit, so a frame can be streamed without ever risking a reject.
func TestPayloadStaysSmall(t *testing.T) {
	s := sampleState()
	s.Craters = make([]Crater, MaxCraters)
	blob := Encode(MarshalState(s))

	n := len([]rune(blob))
	if n > 500 {
		t.Fatalf("a fully-cratered world encodes to %d runes; too fat to stream", n)
	}
	t.Logf("worst-case world: %d bytes → %d runes", len(MarshalState(s)), n)
}

// The state carries launch parameters and elapsed time, not a position, so that a
// client can evaluate the arc wherever it wants. Prove the reconstruction lands
// where the host's own shot was.
func TestLiveShotReconstructsTheHostsBanana(t *testing.T) {
	w := NewWorld(99)
	orig := w.NewShot(1, 42, 90)
	orig.T = 3.21

	s := &State{Seed: 99, Turn: 1, Phase: PhaseFlight, Winner: -1}
	s.SetShot(42, 90, orig.T)

	rw := s.World()
	got := s.LiveShot(rw)
	if got == nil {
		t.Fatal("a flight state reconstructed no shot")
	}
	wx, wy := orig.Pos()
	gx, gy := got.Pos()
	// Time quantizes to centiseconds, so allow the banana a couple of field units.
	if math.Abs(wx-gx) > 2 || math.Abs(wy-gy) > 2 {
		t.Fatalf("reconstructed banana is at (%.1f,%.1f); the host's is at (%.1f,%.1f)", gx, gy, wx, wy)
	}
}

func TestSetShotClampsFlightTime(t *testing.T) {
	s := &State{}
	s.SetShot(1, 2, 10_000) // absurdly long flight
	if s.Shot.T != math.MaxUint16 {
		t.Fatalf("flight time did not clamp: got %d", s.Shot.T)
	}
	s.SetShot(1, 2, -5)
	if s.Shot.T != 0 {
		t.Fatalf("negative flight time did not clamp: got %d", s.Shot.T)
	}
}

// Every truncation of a valid payload must be refused, and none may panic.
//
// The blob rides in a post, and posts get mangled: a client that eats invisible
// runes, a copy/paste that stops halfway, a body that hit a length cap. A parser
// that indexes past the end of a short payload takes the whole TUI down with it,
// and the input is attacker-supplied in the sense that anyone in the channel can
// edit a post.
func TestTruncatedPayloadsAreRejectedNotFatal(t *testing.T) {
	full := MarshalState(sampleState())
	for n := range len(full) {
		st, err := UnmarshalState(full[:n])
		if err == nil {
			t.Errorf("a %d-byte payload (of %d) parsed as %+v; it is truncated", n, len(full), st)
		}
	}
	// And the whole thing still parses, so the loop above is not passing vacuously.
	if _, err := UnmarshalState(full); err != nil {
		t.Fatalf("the untruncated payload does not parse: %v", err)
	}
}

func TestStateRoundTripQuick(t *testing.T) {
	f := func(seed uint16, wind int8, turn, s0, s1 uint8, nc uint8) bool {
		st := &State{
			Seed: seed, Wind: wind, Phase: PhaseAiming,
			Turn: turn % 2, Scores: [2]uint8{s0, s1}, Winner: -1,
		}
		for i := range int(nc) % (MaxCraters + 1) {
			st.Craters = append(st.Craters, Crater{X: int16(i * 7), Y: int16(i * 3), RX: uint8(i), RY: uint8(i * 2)})
		}
		got, err := UnmarshalState(MarshalState(st))
		return err == nil && reflect.DeepEqual(got, st)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}
