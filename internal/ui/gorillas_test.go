package ui

import (
	"math"
	"strings"
	"testing"

	"matterbox/internal/game"
	"matterbox/internal/textwidth"
)

// The post body is the entire contract between two clients: whatever the host
// writes, the joiner has to be able to read back out. Everything else in the game
// is downstream of this round-trip working.
func TestGorillasBodyRoundTripsTheState(t *testing.T) {
	m := game.NewMatch(4242)
	m.Join("joinerjoinerjoinerjoinerxx")
	m.Launch(0, 47, 88)
	for range 5 {
		m.Step(0.05)
	}

	body := gorillasBody(m, [2]string{"alice", "bob"})

	payload, ok := game.Decode(body)
	if !ok {
		t.Fatal("the body carries no payload; the joiner would see nothing")
	}
	st, err := game.UnmarshalState(payload)
	if err != nil {
		t.Fatalf("the joiner cannot parse the host's body: %v", err)
	}
	if st.Seed != m.State.Seed || st.Turn != m.State.Turn || st.Phase != m.State.Phase {
		t.Fatalf("state came back changed:\n got %+v\nwant %+v", st, m.State)
	}
	if st.Shot == nil {
		t.Fatal("a banana was in the air but did not survive the body")
	}
	if st.Joiner != m.State.Joiner {
		t.Fatalf("joiner id %q, want %q", st.Joiner, m.State.Joiner)
	}
}

// Users on the official clients see the visible half. It must be legible: a
// header naming both players, and a fenced board — with none of the invisible
// payload leaking into it.
func TestGorillasBodyIsReadableToOtherClients(t *testing.T) {
	m := game.NewMatch(1)
	m.Join("someone")
	body := gorillasBody(m, [2]string{"alice", "bob"})

	visible := game.Strip(body)
	for _, want := range []string{"Gorillas", "alice", "bob", "wind", "```"} {
		if !strings.Contains(visible, want) {
			t.Errorf("the visible body is missing %q:\n%s", want, visible)
		}
	}
	// The board must survive stripping — the fence has to hold real content.
	if strings.Count(visible, "```") != 2 {
		t.Errorf("expected exactly one fenced board, got:\n%s", visible)
	}
	if !strings.Contains(visible, "#") {
		t.Error("the fenced board has no buildings in it")
	}
}

// A game still in its lobby has to advertise how to join it, or nobody will.
func TestLobbyBodyAdvertisesTheJoinReaction(t *testing.T) {
	m := game.NewMatch(1)
	body := game.Strip(gorillasBody(m, [2]string{"alice", "…"}))
	if !strings.Contains(body, gorillasJoinEmoji) {
		t.Errorf("a lobby post does not say how to join:\n%s", body)
	}
}

// The controller is the joiner's whole side of the wire.
func TestControllerBodyRoundTrips(t *testing.T) {
	want := &game.Input{Angle: 60, Power: 120, Seq: 3}
	payload, ok := game.Decode(gorillasController(want))
	if !ok {
		t.Fatal("the controller body carries no payload")
	}
	got, err := game.UnmarshalInput(payload)
	if err != nil {
		t.Fatalf("the host cannot parse the controller: %v", err)
	}
	if *got != *want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestWindArrow(t *testing.T) {
	for _, tc := range []struct {
		wind int8
		want string
	}{
		{0, "calm"},
		{3, "→ 3"},
		{-3, "← -3"},
		{12, "→→→ 12"},
		{-12, "←←← -12"},
	} {
		if got := windArrow(tc.wind); got != tc.want {
			t.Errorf("windArrow(%d) = %q, want %q", tc.wind, got, tc.want)
		}
	}
	// However hard it blows, the arrow must not run away with the footer. Measured
	// in display width, not bytes — an arrow is three bytes and one column.
	if n := textwidth.Width(windArrow(127)); n > 12 {
		t.Errorf("a gale renders %d columns of arrow: %q", n, windArrow(127))
	}
}

// The field must fit the terminal it is given, at any size, without ever asking
// for more rows or columns than exist.
func TestSizeGorillasFitsTheTerminal(t *testing.T) {
	for _, dim := range [][2]int{{80, 24}, {200, 60}, {40, 12}, {24, 8}} {
		m := &Model{width: dim[0], height: dim[1]}
		m.gorillas.active = true
		m.sizeGorillas()

		g := m.gorillas
		if g.cols <= 0 || g.rows <= 0 {
			t.Fatalf("%dx%d: degenerate field %dx%d", dim[0], dim[1], g.cols, g.rows)
		}
		if g.cols > max(dim[0]-6, 20) {
			t.Errorf("%dx%d: field is %d cols wide, wider than the terminal", dim[0], dim[1], g.cols)
		}
		if g.rows > max(dim[1]-8, 8) {
			t.Errorf("%dx%d: field is %d rows tall, taller than the terminal", dim[0], dim[1], g.rows)
		}
	}
}

// The field must come out 4:3 in *pixels*, whatever the terminal's cell size.
//
// SCREEN 9's 640×350 buffer was only ever seen on a 4:3 monitor, so the field's
// units are 1.37 times taller than they are wide — which the game's own geometry
// already assumes, drawing circles as squat ellipses so they come out round. Give
// the field a 640:350 box and every one of those ellipses stays an ellipse.
//
// Two corrections stack: the display aspect, and the terminal cell's own. Test
// both a tall cell and a nearly square one, since applying only one of the two
// still passes at some cell sizes by luck.
func TestSizeGorillasIsFourThree(t *testing.T) {
	for _, cell := range [][2]int{{8, 16}, {10, 20}, {9, 18}, {8, 10}} {
		for _, dim := range [][2]int{{200, 60}, {160, 50}, {120, 44}} {
			m := &Model{width: dim[0], height: dim[1], cellPxW: cell[0], cellPxH: cell[1]}
			m.gorillas.active = true
			m.sizeGorillas()

			g := m.gorillas
			pxW := float64(g.cols * cell[0])
			pxH := float64(g.rows * cell[1])
			got := pxW / pxH

			// One cell of rounding slop at these sizes is a few percent.
			if math.Abs(got-game.DisplayAspect) > 0.06 {
				t.Errorf("cell %dx%d, terminal %dx%d: field is %d×%d cells = %.0f×%.0f px, "+
					"aspect %.2f; want %.2f",
					cell[0], cell[1], dim[0], dim[1], g.cols, g.rows, pxW, pxH, got, game.DisplayAspect)
			}
		}
	}
}

// A post is only a game post while the game is open — the persistence guard keys
// off it, and a stale true would silently stop caching an ordinary post.
func TestGorillasPostOnlyMatchesTheOpenGame(t *testing.T) {
	m := &Model{}
	if m.gorillasPost("anything") {
		t.Error("a closed game claimed a post")
	}
	m.gorillas = gorillasState{active: true, postID: "world1", replyID: "ctrl1"}
	if !m.gorillasPost("world1") || !m.gorillasPost("ctrl1") {
		t.Error("the open game does not claim its own posts")
	}
	if m.gorillasPost("someone-elses-post") || m.gorillasPost("") {
		t.Error("the open game claimed a post that is not its own")
	}
}
