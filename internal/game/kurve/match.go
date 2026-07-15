package kurve

import "math/rand/v2"

// Match is the rules on top of the Sim: the lobby, the three-two-one, whose point
// a round is, when the round turns over and when the match does. Like
// internal/game's Match it is pure and host-only — one simulator, so a desync
// between the players is not something the protocol can express.
type Match struct {
	Sim *Sim

	Phase  Phase
	Scores []uint8 // one entry per player, index 0 = host
	Winner int8    // -1 until the match is decided

	// Players is the roster in index order: Players[0] is the host, whose id never
	// travels (it is the world post's author), and Players[1:] are the joiners.
	// The rules only ever address a player by index; this is here so the wire can
	// carry the joiner ids and a joiner can find which curve is theirs.
	Players []string

	// Countdown counts down the pre-round three-two-one, in ticks. Hold freezes
	// the field on the last frame of a finished round before the next one starts,
	// the way Gorillas holds on its victory dance.
	Countdown int
	Hold      int

	// NextSeed supplies the next round's arena. Injectable so a test can play a
	// deterministic match; nil means random.
	NextSeed func() uint16
}

// Phase is where a match is in its lifecycle. It doubles as the wire's phase
// byte.
type Phase uint8

const (
	// PhaseLobby: posted, waiting for someone to react and join.
	PhaseLobby Phase = iota
	// PhaseCountdown: both curves placed, three-two-one running. Players may
	// already pick a steering direction; the heads just are not moving yet.
	PhaseCountdown
	// PhaseRun: the curves are moving. This is the phase that streams.
	PhaseRun
	// PhaseRoundOver: a round just ended; the final frame is held for a beat
	// before the next arena goes up.
	PhaseRoundOver
	// PhaseOver: someone reached the winning score.
	PhaseOver
)

const (
	// WinScore ends the match.
	WinScore = 5

	// countdownTicks and holdTicks are timed in ticks; at the UI's frame delay
	// (~45ms) these are roughly 1.5s and 1.2s.
	countdownTicks = 33
	holdTicks      = 26

	// MaxRoundTicks bounds a round. Two cautious players making wide circles can
	// in principle never touch; a bounded arena makes that vanishingly unlikely,
	// but the cap guarantees the tick count — and so the payload — stays finite. A
	// round that reaches it is a draw.
	MaxRoundTicks = 1500
)

// EventKind is what one Step produced, for the UI to narrate.
type EventKind int

const (
	EvNothing   EventKind = iota
	EvCountdown           // the three-two-one ticked
	EvRunning             // the curves moved; nothing decided
	EvRound               // a round ended (someone died, or a draw)
	EvMatch               // that round decided the match
)

// Event is the outcome of one Step.
type Event struct {
	Kind   EventKind
	Scorer int // who took the point (EvRound/EvMatch); -1 on a draw
	Draw   bool
}

// NewMatch starts a match in its lobby, holding just the host, waiting for
// players to react in. The host's id never travels — it is the world post's
// author — so the roster's index-0 slot is left blank for the wire's sake.
func NewMatch(seed uint16) *Match {
	return &Match{
		Sim:     NewSim(seed, 1),
		Phase:   PhaseLobby,
		Scores:  make([]uint8, 1),
		Winner:  -1,
		Players: []string{""},
	}
}

// AddPlayer adds a joiner to the lobby roster and returns their player index, or
// -1 if the lobby is closed or already full. The sim is rebuilt at the new player
// count so the lobby board shows the head that just appeared.
func (m *Match) AddPlayer(userID string) int {
	if m.Phase != PhaseLobby || len(m.Players) >= MaxPlayers {
		return -1
	}
	m.Players = append(m.Players, userID)
	m.Scores = append(m.Scores, 0)
	m.Sim = NewSim(m.Sim.Seed, len(m.Players))
	return len(m.Players) - 1
}

// Start locks the roster and begins the first round's countdown. It needs at
// least two players; the sim is rebuilt fresh at the final count so no lobby
// drift leaks into the round.
func (m *Match) Start() bool {
	if m.Phase != PhaseLobby || len(m.Players) < 2 {
		return false
	}
	m.Sim = NewSim(m.Sim.Seed, len(m.Players))
	m.Phase = PhaseCountdown
	m.Countdown = countdownTicks
	return true
}

// PlayerCount is the roster size — host plus joiners.
func (m *Match) PlayerCount() int { return len(m.Players) }

// Join is the two-player quick path: add one player and start at once. The
// hotseat and the tests use it; the online lobby adds players one reaction at a
// time and starts on the host's word.
func (m *Match) Join(userID string) {
	if m.AddPlayer(userID) >= 0 {
		m.Start()
	}
}

// Busy reports that the host should keep ticking: anything but a still lobby or a
// decided match is live and streaming.
func (m *Match) Busy() bool {
	return m.Phase == PhaseCountdown || m.Phase == PhaseRun || m.Phase == PhaseRoundOver
}

// CanSteer reports whether a curve may take steering input right now — during the
// countdown (so a player can be turning the instant the round starts) and the
// run, while it is alive.
func (m *Match) CanSteer(player int) bool {
	if m.Phase != PhaseCountdown && m.Phase != PhaseRun {
		return false
	}
	if player < 0 || player >= len(m.Sim.Curves) {
		return false
	}
	return !m.Sim.Curves[player].Dead
}

// Steer applies a held steering direction to a curve, if it may steer.
func (m *Match) Steer(player int, d Dir) {
	if m.CanSteer(player) {
		m.Sim.Steer(player, d)
	}
}

// Step advances the match one tick and returns what happened. The State it leaves
// behind is always exactly what should go on the wire next.
func (m *Match) Step() Event {
	switch m.Phase {
	case PhaseCountdown:
		m.Countdown--
		if m.Countdown <= 0 {
			m.Phase = PhaseRun
		}
		return Event{Kind: EvCountdown}

	case PhaseRun:
		return m.stepRun()

	case PhaseRoundOver:
		m.Hold--
		if m.Hold <= 0 {
			if m.Winner >= 0 {
				m.Phase = PhaseOver
			} else {
				m.newRound()
			}
		}
		return Event{Kind: EvNothing}
	}
	return Event{Kind: EvNothing}
}

// stepRun advances the live simulation a tick and resolves the round once it has
// come down to a single survivor — the last curve standing takes the point. A
// death mid-round no longer ends anything; the others play on over the wreck's
// trail. A round that empties the arena on one tick, or runs to the length cap
// with more than one curve still alive, is a pointless draw.
func (m *Match) stepRun() Event {
	m.Sim.Step()
	alive := m.aliveCount()
	timeout := m.Sim.Tick >= MaxRoundTicks

	if alive > 1 && !timeout {
		return Event{Kind: EvRunning, Scorer: -1}
	}

	ev := Event{Kind: EvRound, Scorer: -1}
	if alive == 1 {
		ev.Scorer = m.soleSurvivor()
		m.Scores[ev.Scorer]++
		if m.Scores[ev.Scorer] >= WinScore {
			m.Winner = int8(ev.Scorer)
			ev.Kind = EvMatch
		}
	} else {
		ev.Draw = true // everyone crashed together, or the cap was reached
	}

	m.Phase = PhaseRoundOver
	m.Hold = holdTicks
	return ev
}

// aliveCount is how many curves are still running.
func (m *Match) aliveCount() int {
	n := 0
	for i := range m.Sim.Curves {
		if !m.Sim.Curves[i].Dead {
			n++
		}
	}
	return n
}

// soleSurvivor returns the index of the one live curve, or -1 if there is not
// exactly one. Called only when aliveCount is 1, so it always finds it.
func (m *Match) soleSurvivor() int {
	for i := range m.Sim.Curves {
		if !m.Sim.Curves[i].Dead {
			return i
		}
	}
	return -1
}

// newRound rebuilds the arena from a fresh seed and restarts the countdown. Like
// Gorillas' newRound the whole world changes for two bytes on the wire, because
// the world is a function of the seed.
func (m *Match) newRound() {
	m.Sim = NewSim(m.nextSeed(), len(m.Players))
	m.Phase = PhaseCountdown
	m.Countdown = countdownTicks
	m.Hold = 0
}

func (m *Match) nextSeed() uint16 {
	if m.NextSeed != nil {
		return m.NextSeed()
	}
	return uint16(rand.IntN(1 << 16))
}
