package kurve

import (
	"encoding/binary"
	"errors"
)

// The wire format. Two payloads travel, each in its own post, neither ever
// written by more than one client — the same split as internal/game, for the
// same reason (Mattermost's edit API has no compare-and-swap, so two writers on
// one post silently clobber each other):
//
//	State — the host's post. Seed, scores, phase, and the two steering logs. The
//	        host rewrites it every tick while the curves are moving.
//	Input — the joiner's post, a thread reply. Their current held steering
//	        direction. The joiner rewrites it only when that direction changes.
//
// The state is not the trail — it is the recipe for the trail. A whole match is a
// seed plus two logs of direction changes; the joiner replays them through the
// identical Sim to rebuild every pixel (FromState). That is what lets an
// unbounded, per-tick-growing world ride a bounded post.
const (
	// StateVer / InputVer are bumped on any layout change. A client that sees a
	// version it does not know ignores the post rather than misreading it.
	StateVer = 1
	InputVer = 1

	// idLen is the length of a Mattermost user id.
	idLen = 26

	// alive is the death-tick sentinel for a curve that has not died.
	alive = 0xFFFF
)

var (
	ErrShortPayload   = errors.New("kurve: payload too short")
	ErrUnknownVersion = errors.New("kurve: unknown payload version")
	ErrTooManyTurns   = errors.New("kurve: steering log out of range")
	ErrBadPlayerCount = errors.New("kurve: player count out of range")
)

// State is the whole game as it travels in the host's post. The per-player slices
// — Joiners, Scores, Deaths, Turns — are what make the format carry two players or
// six with no change but their lengths.
type State struct {
	Seed      uint16
	Phase     Phase
	Tick      uint16
	Countdown uint8
	Winner    int8

	// Joiners are the joiner user ids in index order: Joiners[j] is player j+1.
	// Player 0 is the post's author (the host), whose id never needs to travel, so
	// the roster on the wire is one shorter than the player count.
	Joiners []string

	// Scores is one entry per player (host included), so len(Scores) is the player
	// count — the single number every other slice's length is checked against.
	Scores []uint8

	// Deaths is each curve's death tick, or `alive`. Carried rather than left to
	// the replay to rediscover, so the players always agree on who is dead even if
	// the pure float replay drifts a pixel from the host — the authoritative fact
	// is whose curve stopped, and that is stated outright.
	Deaths []uint16

	// Turns is the steering log per curve — the payload's bulk, and the only part
	// that grows over a round.
	Turns [][]Turn
}

// Input is the joiner's payload: their current held steering direction.
type Input struct {
	Dir Dir
	// Seq increments with every change the joiner makes. It lets the host ignore a
	// re-delivered edit; applying a held direction is idempotent anyway, but the
	// counter makes "nothing new here" cheap to see.
	Seq uint8
}

// WireState projects a host's Match onto the wire form. It reaches into the Sim's
// steering log and curves — legitimate here, since the wire is the Sim's own
// serialisation.
func WireState(m *Match) *State {
	s := m.Sim
	st := &State{
		Seed:      s.Seed,
		Phase:     m.Phase,
		Tick:      s.Tick,
		Countdown: uint8(min(max(m.Countdown, 0), 255)),
		Scores:    m.Scores,
		Winner:    m.Winner,
		Joiners:   m.Players[1:],
		Turns:     s.events,
		Deaths:    make([]uint16, len(s.Curves)),
	}
	for i := range s.Curves {
		if s.Curves[i].Dead {
			st.Deaths[i] = s.Curves[i].DeathTick
		} else {
			st.Deaths[i] = alive
		}
	}
	return st
}

// MarshalState renders the state for the invisible blob. The player count leads
// (a single byte), and every per-player section that follows is that many entries
// long, so the parser knows the shape before it reads the bulk.
func MarshalState(st *State) []byte {
	n := len(st.Scores)
	b := make([]byte, 0, 9+(n-1)*idLen+n*3)
	b = append(b, StateVer)
	b = binary.LittleEndian.AppendUint16(b, st.Seed)
	b = append(b, byte(st.Phase))
	b = binary.LittleEndian.AppendUint16(b, st.Tick)
	b = append(b, st.Countdown, byte(st.Winner), byte(n))

	for _, j := range st.Joiners {
		var id [idLen]byte
		copy(id[:], j)
		b = append(b, id[:]...)
	}
	for _, sc := range st.Scores {
		b = append(b, sc)
	}
	for _, d := range st.Deaths {
		b = binary.LittleEndian.AppendUint16(b, d)
	}
	for p := range st.Turns {
		cnt := min(len(st.Turns[p]), MaxEvents)
		b = append(b, byte(cnt))
		for _, t := range st.Turns[p][:cnt] {
			b = binary.LittleEndian.AppendUint16(b, t.Tick)
			b = append(b, byte(t.Dir))
		}
	}
	return b
}

// UnmarshalState parses a state payload. It is strict: anything it does not fully
// understand is an error, never a half-populated state — a partially-read world
// renders as a plausible lie.
func UnmarshalState(b []byte) (*State, error) {
	const head = 1 + 2 + 1 + 2 + 1 + 1 + 1 // ver, seed, phase, tick, countdown, winner, count
	if len(b) < head {
		return nil, ErrShortPayload
	}
	if b[0] != StateVer {
		return nil, ErrUnknownVersion
	}
	st := &State{
		Seed:      binary.LittleEndian.Uint16(b[1:3]),
		Phase:     Phase(b[3]),
		Tick:      binary.LittleEndian.Uint16(b[4:6]),
		Countdown: b[6],
		Winner:    int8(b[7]),
	}
	n := int(b[8])
	if n < 1 || n > MaxPlayers {
		return nil, ErrBadPlayerCount
	}
	p := head

	if len(b) < p+(n-1)*idLen {
		return nil, ErrShortPayload
	}
	st.Joiners = make([]string, n-1)
	for i := range st.Joiners {
		st.Joiners[i] = string(trimZeros(b[p : p+idLen]))
		p += idLen
	}

	if len(b) < p+n {
		return nil, ErrShortPayload
	}
	st.Scores = make([]uint8, n)
	for i := range st.Scores {
		st.Scores[i] = b[p]
		p++
	}

	if len(b) < p+n*2 {
		return nil, ErrShortPayload
	}
	st.Deaths = make([]uint16, n)
	for i := range st.Deaths {
		st.Deaths[i] = binary.LittleEndian.Uint16(b[p : p+2])
		p += 2
	}

	st.Turns = make([][]Turn, n)
	for player := range st.Turns {
		if len(b) < p+1 {
			return nil, ErrShortPayload
		}
		cnt := int(b[p])
		p++
		if cnt > MaxEvents {
			return nil, ErrTooManyTurns
		}
		if len(b) < p+cnt*3 {
			return nil, ErrShortPayload
		}
		if cnt == 0 {
			continue
		}
		turns := make([]Turn, cnt)
		for i := range cnt {
			off := p + i*3
			turns[i] = Turn{
				Tick: binary.LittleEndian.Uint16(b[off : off+2]),
				Dir:  Dir(int8(b[off+2])),
			}
		}
		st.Turns[player] = turns
		p += cnt * 3
	}
	return st, nil
}

// MarshalInput renders the controller payload.
func MarshalInput(in *Input) []byte {
	return []byte{InputVer, byte(in.Dir), in.Seq}
}

// UnmarshalInput parses a controller payload.
func UnmarshalInput(b []byte) (*Input, error) {
	if len(b) < 3 {
		return nil, ErrShortPayload
	}
	if b[0] != InputVer {
		return nil, ErrUnknownVersion
	}
	return &Input{Dir: Dir(int8(b[1])), Seq: b[2]}, nil
}

// FromState rebuilds a live match from a state that arrived over the wire — how a
// joiner (or a spectator) reconstructs what the host is looking at.
//
// It replays the steering logs through a fresh Sim up to the streamed tick, which
// redraws every trail cell, then overlays the authoritative death ticks. The
// replay is a pure function of seed+logs, identical on both machines, so the
// joiner's trail is the host's trail — the same guarantee that lets a Gorillas
// joiner rebuild the skyline from a seed.
func FromState(st *State) *Match {
	n := len(st.Scores)
	sim := NewSim(st.Seed, n)
	sim.events = st.Turns
	for sim.Tick < st.Tick {
		sim.Step()
	}
	for i := range sim.Curves {
		if st.Deaths[i] != alive {
			sim.Curves[i].Dead = true
			sim.Curves[i].DeathTick = st.Deaths[i]
		}
	}
	return &Match{
		Sim:       sim,
		Phase:     st.Phase,
		Scores:    st.Scores,
		Winner:    st.Winner,
		Countdown: int(st.Countdown),
		// Index 0 is the host, whose id is the world post's author and never
		// travels; the caller fills it in from the post if it needs the name.
		Players: append([]string{""}, st.Joiners...),
	}
}

func trimZeros(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}
