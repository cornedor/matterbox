package game

// VictoryDance: the winner beats its chest over the ruins.
//
// It is not a flourish on the end of a match. DoShot calls it after *every*
// round-ending hit, before PlayGame clears the screen and puts up the next
// skyline — so it belongs exactly where the fireball left off: on the cratered
// city, with the loser still in its hole and the next seed not yet drawn.
//
// Which means it holds the round open the same way an explosion does, and for the
// same reason: it happens before the world changes, not after.

const (
	// VictoryDance is FOR i# = 1 TO 4 around a pair of PUTs and a Rest .2 — four
	// cycles of two poses, a fifth of a second each.
	dancePoseFrames = 6
	danceCycles     = 4
	danceFrames     = danceCycles * 2 * dancePoseFrames
)

// Dance is a gorilla celebrating.
type Dance struct {
	Player int
	Frame  int
}

// NewDance starts one.
func NewDance(player int) *Dance { return &Dance{Player: player} }

// Done reports that the ape has got it out of its system.
func (d *Dance) Done() bool { return d.Frame >= danceFrames }

// pose alternates the raised arm. That is the whole dance — the original does it
// by PUTting GorL and GorR back to back, and the ape has no other moves.
func (d *Dance) pose() gorillaPose {
	if (d.Frame/dancePoseFrames)%2 == 0 {
		return leftUp
	}
	return rightUp
}
