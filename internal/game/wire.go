package game

import (
	"encoding/binary"
	"errors"
	"math"
)

// The wire format. Two payloads travel between the players, each in its own
// post, and neither post is ever written by more than one client:
//
//	State — the host's post. The whole world. The host is authoritative and
//	        rewrites this many times a second while a banana is in the air.
//	Input — the joiner's post, a thread reply. Just their shot. The joiner is
//	        the only one who ever touches it.
//
// That split is the reason there is no conflict resolution anywhere in this
// package: Mattermost's edit API has no compare-and-swap, so two clients editing
// one post would silently clobber each other. Nobody shares a post, so nobody can.
const (
	// StateVer / InputVer are bumped on any layout change. A client that sees a
	// version it does not know ignores the post rather than misreading it — an
	// old client must never render a new world as garbage.
	StateVer = 1
	InputVer = 1

	// idLen is the length of a Mattermost user id.
	idLen = 26

	// MaxCraters bounds the payload. At 5 bytes each this caps the blob at a few
	// hundred bytes, and a match that has cratered the city 60 times is over.
	MaxCraters = 60
)

var (
	ErrShortPayload   = errors.New("game: payload too short")
	ErrUnknownVersion = errors.New("game: unknown payload version")
	ErrTooManyCraters = errors.New("game: crater count out of range")
)

// Phase is where a match is in its lifecycle.
type Phase uint8

const (
	// PhaseLobby: posted, waiting for someone to react and join.
	PhaseLobby Phase = iota
	// PhaseAiming: it is Turn's shot; they are picking an angle and power.
	PhaseAiming
	// PhaseFlight: a banana is in the air. This is the phase that streams.
	PhaseFlight
	// PhaseOver: someone won.
	PhaseOver
)

// State is the whole game, as it travels in the host's post.
type State struct {
	Seed   uint16
	Wind   int8
	Phase  Phase
	Turn   uint8 // 0 = host, 1 = joiner
	Scores [2]uint8
	Winner int8 // -1 while nobody has won

	// Joiner is the user id of player 1. Player 0 is the post's author, so the
	// host's id never needs to travel.
	Joiner string

	// Shot is the banana in flight, present only in PhaseFlight. It travels as
	// launch parameters plus elapsed time rather than as a position, so a client
	// can evaluate the arc at any instant it likes — which is what lets it draw a
	// smooth 60fps banana between two states that arrived 33ms apart, instead of
	// snapping from one streamed position to the next.
	Shot *ShotWire

	Craters []Crater
}

// ShotWire is a Shot reduced to what has to travel: the two numbers the player
// chose, and how far into its flight it is. Everything else is recomputed.
type ShotWire struct {
	Angle uint8  // degrees, 0–180
	Power uint8  // 0–255
	T     uint16 // centiseconds since launch
}

// Input is the joiner's payload, in their own thread reply.
type Input struct {
	Angle uint8
	Power uint8
	// Seq increments with every shot the joiner takes. Without it the host cannot
	// tell a fresh shot from a re-delivered edit of the previous one — the same
	// angle and power twice in a row is a perfectly ordinary thing to do.
	Seq uint8
}

// MarshalState renders the state for the invisible blob.
func MarshalState(s *State) []byte {
	b := make([]byte, 0, 12+idLen+4+len(s.Craters)*5)
	b = append(b, StateVer)
	b = binary.LittleEndian.AppendUint16(b, s.Seed)
	b = append(b, byte(s.Wind), byte(s.Phase), s.Turn, s.Scores[0], s.Scores[1], byte(s.Winner))

	// The joiner id is fixed-width and zero-padded: a length prefix would buy 25
	// bytes on a payload that is already small, at the cost of another way to
	// mis-parse.
	var id [idLen]byte
	copy(id[:], s.Joiner)
	b = append(b, id[:]...)

	if s.Shot != nil {
		b = append(b, 1, s.Shot.Angle, s.Shot.Power)
		b = binary.LittleEndian.AppendUint16(b, s.Shot.T)
	} else {
		b = append(b, 0)
	}

	n := min(len(s.Craters), MaxCraters)
	b = append(b, byte(n))
	for _, c := range s.Craters[:n] {
		b = binary.LittleEndian.AppendUint16(b, uint16(c.X))
		b = binary.LittleEndian.AppendUint16(b, uint16(c.Y))
		b = append(b, c.R)
	}
	return b
}

// UnmarshalState parses a state payload. It is strict: anything it does not
// fully understand is an error, never a partially-populated state, because a
// half-read world would render as a plausible-looking lie.
func UnmarshalState(b []byte) (*State, error) {
	const head = 8 + idLen + 1 // version..winner, id, shot flag
	if len(b) < head {
		return nil, ErrShortPayload
	}
	if b[0] != StateVer {
		return nil, ErrUnknownVersion
	}
	s := &State{
		Seed:   binary.LittleEndian.Uint16(b[1:3]),
		Wind:   int8(b[3]),
		Phase:  Phase(b[4]),
		Turn:   b[5],
		Scores: [2]uint8{b[6], b[7]},
		Winner: int8(b[8]),
	}
	p := 9
	s.Joiner = string(trimZeros(b[p : p+idLen]))
	p += idLen

	hasShot := b[p] == 1
	p++
	if hasShot {
		if len(b) < p+4 {
			return nil, ErrShortPayload
		}
		s.Shot = &ShotWire{
			Angle: b[p],
			Power: b[p+1],
			T:     binary.LittleEndian.Uint16(b[p+2 : p+4]),
		}
		p += 4
	}

	if len(b) < p+1 {
		return nil, ErrShortPayload
	}
	n := int(b[p])
	p++
	if n > MaxCraters {
		return nil, ErrTooManyCraters
	}
	if len(b) < p+n*5 {
		return nil, ErrShortPayload
	}
	if n == 0 {
		return s, nil // leave Craters nil rather than empty: a fresh world has none
	}
	s.Craters = make([]Crater, n)
	for i := range n {
		off := p + i*5
		s.Craters[i] = Crater{
			X: int16(binary.LittleEndian.Uint16(b[off : off+2])),
			Y: int16(binary.LittleEndian.Uint16(b[off+2 : off+4])),
			R: b[off+4],
		}
	}
	return s, nil
}

func MarshalInput(in *Input) []byte {
	return []byte{InputVer, in.Angle, in.Power, in.Seq}
}

func UnmarshalInput(b []byte) (*Input, error) {
	if len(b) < 4 {
		return nil, ErrShortPayload
	}
	if b[0] != InputVer {
		return nil, ErrUnknownVersion
	}
	return &Input{Angle: b[1], Power: b[2], Seq: b[3]}, nil
}

func trimZeros(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}

// World rebuilds the playable world a State describes: the skyline from the
// seed, the craters replayed onto it. This is how the joiner and any spectator
// reconstruct what the host is looking at.
func (s *State) World() *World {
	w := NewWorld(s.Seed)
	w.Craters = append(w.Craters, s.Craters...)
	return w
}

// LiveShot rebuilds the in-flight banana, positioned at the time the state says
// it has reached. Returns nil when nothing is in the air.
func (s *State) LiveShot(w *World) *Shot {
	if s.Shot == nil {
		return nil
	}
	sh := w.NewShot(int(s.Turn), float64(s.Shot.Angle), float64(s.Shot.Power))
	sh.T = float64(s.Shot.T) / 100
	return sh
}

// SetShot records a shot's launch parameters and its current flight time.
func (s *State) SetShot(angle, power uint8, t float64) {
	ct := math.Round(t * 100)
	s.Shot = &ShotWire{Angle: angle, Power: power, T: uint16(min(max(ct, 0), math.MaxUint16))}
}
