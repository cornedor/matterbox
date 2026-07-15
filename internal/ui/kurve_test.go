package ui

import (
	"strings"
	"testing"

	"matterbox/internal/game"
	"matterbox/internal/game/kurve"
)

// The post body is the whole contract between two clients: whatever the host
// writes, the joiner has to read back out. Everything else is downstream of this
// round-trip working.
func TestKurveBodyRoundTripsTheState(t *testing.T) {
	m := kurve.NewMatch(4242)
	m.Join("joinerjoinerjoinerjoinerxx")
	for range 40 {
		m.Steer(0, kurve.Right)
		m.Steer(1, kurve.Left)
		m.Step()
	}

	body := kurveBody(m, []string{"alice", "bob"})

	payload, ok := kurve.Decode(body)
	if !ok {
		t.Fatal("the body carries no payload; the joiner would see nothing")
	}
	st, err := kurve.UnmarshalState(payload)
	if err != nil {
		t.Fatalf("the joiner cannot parse the host's body: %v", err)
	}
	if st.Seed != m.Sim.Seed || st.Phase != m.Phase || st.Tick != m.Sim.Tick ||
		len(st.Joiners) != 1 || st.Joiners[0] != "joinerjoinerjoinerjoinerxx" {
		t.Fatalf("state came back changed:\n got seed=%d phase=%d tick=%d joiners=%q",
			st.Seed, st.Phase, st.Tick, st.Joiners)
	}

	// And the reconstructed world must match the host's, cell for cell — the join
	// is worthless if the joiner sees a different trail.
	replay := kurve.FromState(st)
	for y := 0; y < kurve.FieldH; y += 3 {
		for x := 0; x < kurve.FieldW; x += 3 {
			if m.Sim.Owner(x, y) != replay.Sim.Owner(x, y) {
				t.Fatalf("rebuilt world differs at (%d,%d)", x, y)
			}
		}
	}
}

// Users on the official clients see the visible half. It must be legible — a
// header naming both players and a fenced board — with none of the invisible
// payload leaking into it.
func TestKurveBodyIsReadableToOtherClients(t *testing.T) {
	m := kurve.NewMatch(1)
	m.Join("someone")
	body := kurveBody(m, []string{"alice", "bob"})

	visible := game.Strip(body)
	for _, want := range []string{"Achtung", "alice", "bob", "first to", "```"} {
		if !strings.Contains(visible, want) {
			t.Errorf("the visible body is missing %q:\n%s", want, visible)
		}
	}
	if strings.Count(visible, "```") != 2 {
		t.Errorf("expected exactly one fenced board, got:\n%s", visible)
	}
	// The strip must remove every payload rune: nothing invisible may survive into
	// what the pane shows or the cache stores.
	if game.Strip(visible) != visible {
		t.Error("stripping twice changed the text; a payload rune leaked into the visible body")
	}
}

// The lobby body invites a join; a started body names the players instead.
func TestKurveBodyShowsLobbyThenOpponent(t *testing.T) {
	lobby := kurve.NewMatch(9)
	if got := game.Strip(kurveBody(lobby, []string{"alice"})); !strings.Contains(got, "react") {
		t.Errorf("a lobby body should invite a join:\n%s", got)
	}
	joined := kurve.NewMatch(9)
	joined.Join("someone")
	if got := game.Strip(kurveBody(joined, []string{"alice", "rival"})); !strings.Contains(got, "rival") {
		t.Errorf("a joined body should name the opponent:\n%s", got)
	}
}
