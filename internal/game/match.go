package game

import "math/rand/v2"

// Match is the rules: whose turn it is, what a landing banana does, when a round
// ends and when the match does.
//
// It is pure — no network, no terminal, no clock. The UI drives it by calling
// Step on a timer and reacting to the Event it hands back, which is what lets
// the rules be tested without a Mattermost server or a bubbletea program (see
// match_test.go, which plays whole matches to completion).
//
// Only the host ever runs a Match. That is the point: one simulator means one
// authority on where a banana lands, so a desync between the two players is not
// something the protocol can even express.
type Match struct {
	State *State
	World *World
	Shot  *Shot
	Boom  *Explosion
	Dance *Dance

	// NextSeed supplies the city for the next round. Injectable so tests can play
	// a deterministic match; nil means random.
	NextSeed func() uint16

	// boomHit is who the fireball killed, remembered across it and across the
	// victory dance that follows: the round does not actually turn over until both
	// have finished, and the ape that was flattened shoots first in the next one.
	boomHit int
}

// EventKind is what a simulation step produced.
type EventKind int

const (
	// EvNothing: nothing is in the air.
	EvNothing EventKind = iota
	// EvFlying: the banana is still up. The caller streams the new state.
	EvFlying
	// EvMiss: it left the field.
	EvMiss
	// EvBuilding: it hit masonry, and the fireball is now burning.
	EvBuilding
	// EvRound: a gorilla was hit and the point is scored. The city is not replaced
	// until the fireball has finished collapsing.
	EvRound
	// EvMatch: a gorilla was hit and that was the winning point.
	EvMatch
	// EvBoom: a fireball advanced a frame. The caller streams it, so both players
	// watch the same explosion; when the last one lands the crater is cut and the
	// world moves on.
	EvBoom
	// EvDance: the winner's victory dance advanced a frame. Streamed for the same
	// reason, and when it ends the next city goes up.
	EvDance
)

// Event is the outcome of one Step, with everything the UI needs to narrate it.
type Event struct {
	Kind   EventKind
	Hit    int  // the gorilla that was hit (EvRound, EvMatch)
	Scorer int  // who got the point (EvRound, EvMatch)
	X, Y   int  // where the banana landed (EvBuilding, EvRound, EvMatch)
	Self   bool // the shooter hit themselves, which is the funniest outcome
}

// NewMatch starts a match in its lobby, waiting for a second player.
func NewMatch(seed uint16) *Match {
	w := NewWorld(seed)
	return &Match{
		World: w,
		State: &State{Seed: seed, Wind: w.Wind, Phase: PhaseLobby, Winner: -1},
	}
}

// FromState rebuilds a match from a state that arrived over the wire. This is how
// a joiner — or a spectator — reconstructs what the host is looking at.
func FromState(st *State) *Match {
	w := st.World()
	return &Match{
		State: st,
		World: w,
		Shot:  st.LiveShot(w),
		Boom:  st.LiveBoom(),
		Dance: st.LiveDance(),
	}
}

// Busy reports that the world is still animating and the caller should keep
// ticking: a banana is up, a fireball has not finished collapsing, or the winner
// is still celebrating.
func (m *Match) Busy() bool {
	return m.Boom != nil || m.Dance != nil || (m.State.Phase == PhaseFlight && m.Shot != nil)
}

// Join makes userID player two and starts the first round.
func (m *Match) Join(userID string) {
	if m.State.Phase != PhaseLobby {
		return
	}
	m.State.Joiner = userID
	m.State.Phase = PhaseAiming
	m.State.Turn = 0
}

// Launch puts a banana in the air. It is the caller's job to know whose turn it
// is; Launch trusts them.
func (m *Match) Launch(player int, angle, power uint8) {
	m.Shot = m.World.NewShot(player, float64(angle), float64(power))
	m.World.SunHit = false // DoShot clears it before every shot
	m.State.SunHit = false
	m.State.Turn = uint8(player)
	m.State.Phase = PhaseFlight
	m.State.SetShot(angle, power, 0)
}

// Step advances the world one frame — the banana if one is up, otherwise the
// fireball if one is burning — and applies whatever rule that triggers. The State
// it leaves behind is always exactly what should go on the wire next.
func (m *Match) Step(dt float64) Event {
	if m.Boom != nil {
		return m.stepBoom()
	}
	if m.Dance != nil {
		return m.stepDance()
	}
	if m.State.Phase != PhaseFlight || m.Shot == nil {
		return Event{Kind: EvNothing}
	}

	// A banana lobbed straight up at full power apexes around 26 simulated
	// seconds, and the top of the field is deliberately open sky so a high shot
	// can come back down. Without a budget that is a thousand frames — and since
	// every frame is a PATCH, an unbounded flight is an unbounded number of edits.
	// Long enough to be gone is long enough to be a miss.
	if m.Shot.T > MaxFlightTime {
		m.endShot()
		return Event{Kind: EvMiss}
	}

	out, hit := m.World.Step(m.Shot, dt)
	m.State.SetShot(m.State.Shot.Angle, m.State.Shot.Power, m.Shot.T)
	m.State.SunHit = m.World.SunHit
	x, y := m.Shot.Pos()

	switch out {
	case InFlight:
		return Event{Kind: EvFlying, X: int(x), Y: int(y)}

	case OffField:
		m.endShot()
		return Event{Kind: EvMiss}

	case HitBuilding:
		m.light(NewExplosion(BoomBanana, int(x), int(y)))
		return Event{Kind: EvBuilding, X: int(x), Y: int(y)}

	case HitGorilla:
		shooter := int(m.State.Turn)
		// The point goes to whoever did not get hit. That covers the ordinary case
		// and the self-hit — where the shooter has scored for their opponent — with
		// the same line.
		scorer := 1 - hit
		m.State.Scores[scorer]++

		// ExplodeGorilla blasts the ape, not the banana: it centres on the chest
		// wherever the fruit actually landed.
		bx, by := m.World.GorillaCentre(hit)
		m.boomHit = hit
		m.World.Dead[hit] = true
		m.light(NewExplosion(BoomGorilla, bx, by))

		ev := Event{Hit: hit, Scorer: scorer, X: bx, Y: by, Self: hit == shooter}
		if m.State.Scores[scorer] >= WinScore {
			// The match is decided, but the fireball still has to burn: PhaseOver
			// rather than PhaseBoom, since there is no next round to hold open.
			m.State.Phase = PhaseOver
			m.State.Winner = int8(scorer)
			ev.Kind = EvMatch
			return ev
		}
		ev.Kind = EvRound
		return ev
	}
	return Event{Kind: EvNothing}
}

// light starts a fireball. The banana is down, nobody may fire, and the world is
// left exactly as it was — the crater comes later.
func (m *Match) light(e *Explosion) {
	m.Shot = nil
	m.State.Shot = nil
	m.Boom = e
	m.State.SetBoom(e)
	if m.State.Phase != PhaseOver {
		m.State.Phase = PhaseBoom
	}
}

// stepBoom advances the fireball a frame, and when it has finished collapsing
// cuts the hole it ate and lets the world move on.
//
// This is the whole reason PhaseBoom exists. In the original the crater is not a
// consequence of the impact, it is a consequence of the *animation*: the erase
// pass that cleans the fireball off the screen takes the masonry with it. So the
// city stands, whole, for as long as the blast is burning — and only then does
// the bite appear. Carving on impact instead would open the hole a beat early,
// which on a gorilla hit is glaring, its crater being far larger than its blast.
func (m *Match) stepBoom() Event {
	m.Boom.Frame++
	if !m.Boom.Done() {
		m.State.SetBoom(m.Boom)
		return Event{Kind: EvBoom}
	}

	boom := m.Boom
	m.Boom = nil
	m.State.SetBoom(nil)

	rx, ry := boom.Crater()
	m.World.Carve(boom.X, boom.Y, rx, ry)
	m.State.Craters = m.World.Craters

	if boom.Kind != BoomGorilla {
		m.endShot()
		return Event{Kind: EvBoom}
	}

	// A gorilla died, so somebody gets to gloat about it — and they do it here, on
	// the cratered city, before the next one exists. A won match dances too; it
	// just has no round to hand on to afterwards.
	m.Dance = NewDance(1 - m.boomHit)
	m.State.SetDance(m.Dance)
	if m.State.Phase != PhaseOver {
		m.State.Phase = PhaseDance
	}
	return Event{Kind: EvBoom}
}

// stepDance advances the victory dance, and when the winner has finally settled
// down, puts up the next city.
func (m *Match) stepDance() Event {
	m.Dance.Frame++
	if !m.Dance.Done() {
		m.State.SetDance(m.Dance)
		return Event{Kind: EvDance}
	}

	m.Dance = nil
	m.State.SetDance(nil)
	if m.State.Phase != PhaseOver {
		m.newRound(m.boomHit)
	}
	return Event{Kind: EvDance}
}

const (
	// WinScore ends the match.
	WinScore = 3

	// MaxFlightTime caps a single banana, in simulated seconds. It bounds both the
	// wait (a shot that is never coming back still has to end the turn) and the
	// edit count, since the flight streams one PATCH per frame.
	MaxFlightTime = 20.0
)

// endShot hands the turn to the other player.
func (m *Match) endShot() {
	m.Shot = nil
	m.State.Shot = nil
	m.State.Phase = PhaseAiming
	m.State.Turn = 1 - m.State.Turn
}

// newRound rebuilds the city. The seed changes, so the entire world changes —
// and because the world is derived from the seed, that costs two bytes on the
// wire rather than a new skyline.
//
// The player who was just flattened shoots first, which is the only mercy in this
// game.
func (m *Match) newRound(hit int) {
	seed := m.nextSeed()
	m.World = NewWorld(seed)
	m.Shot = nil
	m.Boom = nil
	m.Dance = nil
	m.State.Seed = seed
	m.State.Wind = m.World.Wind
	m.State.Craters = nil
	m.State.Shot = nil
	m.State.Boom = nil
	m.State.Dance = nil
	m.State.SunHit = false
	m.State.Phase = PhaseAiming
	m.State.Turn = uint8(hit)
}

func (m *Match) nextSeed() uint16 {
	if m.NextSeed != nil {
		return m.NextSeed()
	}
	return uint16(rand.IntN(1 << 16))
}

// MyTurn reports whether the given player may fire right now.
func (m *Match) MyTurn(player int) bool {
	return m.State.Phase == PhaseAiming && int(m.State.Turn) == player
}
