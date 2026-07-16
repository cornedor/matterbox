package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/game"
	"matterbox/internal/game/kurve"
)

// Long enough to survive the 26-byte id field the state marshals joiners into.
var (
	rjHost   = strings.Repeat("h", 26)
	rjJoiner = strings.Repeat("j", 26)
	rjOther  = strings.Repeat("o", 26)
)

func gorillasWorldPost(t *testing.T, finished bool) *model.Post {
	t.Helper()
	mt := game.NewMatch(4242)
	mt.Join(rjJoiner)
	if finished {
		mt.State.Phase = game.PhaseOver
		mt.State.Winner = 0
	}
	return &model.Post{
		Id: "world", UserId: rjHost, ChannelId: "chan",
		Message: gorillasBody(mt, [2]string{"host", "joiner"}),
	}
}

func kurveWorldPost(t *testing.T, finished bool) *model.Post {
	t.Helper()
	mt := kurve.NewMatch(4242)
	mt.AddPlayer(rjJoiner)
	mt.Start()
	if finished {
		mt.Phase = kurve.PhaseOver
		mt.Winner = 0
	}
	return &model.Post{
		Id: "world", UserId: rjHost, ChannelId: "chan",
		Message: kurveBody(mt, []string{"host", "joiner"}),
	}
}

// The whole rejoin turns on reading a role back out of the world post: the author
// is the host, the payload names the joiner, and anyone else is a bystander who
// cannot resume it. A finished or non-game post is nobody's to resume.
func TestRejoinRoleDetection(t *testing.T) {
	t.Run("gorillas", func(t *testing.T) {
		post := gorillasWorldPost(t, false)
		if _, role, ok := gorillasResumeRole(post, rjHost); !ok || role != 0 {
			t.Errorf("author should resume as host (0): role=%d ok=%v", role, ok)
		}
		if _, role, ok := gorillasResumeRole(post, rjJoiner); !ok || role != 1 {
			t.Errorf("the joiner should resume as joiner (1): role=%d ok=%v", role, ok)
		}
		if _, _, ok := gorillasResumeRole(post, rjOther); ok {
			t.Error("a bystander must not be able to resume the game")
		}
		if _, _, ok := gorillasResumeRole(gorillasWorldPost(t, true), rjHost); ok {
			t.Error("a finished match is not resumable")
		}
		if _, _, ok := gorillasResumeRole(&model.Post{Message: "just a message"}, rjHost); ok {
			t.Error("an ordinary post is not a resumable game")
		}
	})

	t.Run("kurve", func(t *testing.T) {
		post := kurveWorldPost(t, false)
		if _, role, ok := kurveResumeRole(post, rjHost); !ok || role != 0 {
			t.Errorf("author should resume as host (0): role=%d ok=%v", role, ok)
		}
		if _, role, ok := kurveResumeRole(post, rjJoiner); !ok || role != 1 {
			t.Errorf("the joiner should resume as joiner (1): role=%d ok=%v", role, ok)
		}
		if _, _, ok := kurveResumeRole(post, rjOther); ok {
			t.Error("a bystander must not be able to resume the game")
		}
		if _, _, ok := kurveResumeRole(kurveWorldPost(t, true), rjHost); ok {
			t.Error("a finished match is not resumable")
		}
		if _, _, ok := kurveResumeRole(&model.Post{Message: "just a message"}, rjHost); ok {
			t.Error("an ordinary post is not a resumable game")
		}
	})
}

// The seq counters live only in the controller post, so a resume has to find it in
// the thread — the joiner's for Gorillas, whichever author's for Kurve — and read
// the shot it currently holds, without mistaking the world post for it.
func TestControllerInThread(t *testing.T) {
	t.Run("gorillas", func(t *testing.T) {
		world := gorillasWorldPost(t, false)
		ctrl := &model.Post{
			Id: "ctrl", UserId: rjJoiner, RootId: "world",
			Message: gorillasController(&game.Input{Angle: 40, Power: 75, Seq: 6}),
		}
		pl := &model.PostList{
			Order: []string{"ctrl", "world"},
			Posts: map[string]*model.Post{"world": world, "ctrl": ctrl},
		}
		p, in := gorillasControllerInThread(pl, rjJoiner)
		if p == nil || p.Id != "ctrl" {
			t.Fatalf("did not find the joiner's controller: %v", p)
		}
		if in.Seq != 6 || in.Angle != 40 || in.Power != 75 {
			t.Errorf("controller read back wrong: %+v", in)
		}
		// The world post decodes to a State, not an Input, so it must never be
		// mistaken for the controller.
		if p, _ := gorillasControllerInThread(pl, rjHost); p != nil {
			t.Errorf("the host has no controller, yet one was found: %v", p)
		}
	})

	t.Run("kurve", func(t *testing.T) {
		world := kurveWorldPost(t, false)
		ctrl := &model.Post{
			Id: "ctrl", UserId: rjJoiner, RootId: "world",
			Message: kurveController(&kurve.Input{Dir: kurve.Left, Seq: 3}),
		}
		pl := &model.PostList{
			Order: []string{"ctrl", "world"},
			Posts: map[string]*model.Post{"world": world, "ctrl": ctrl},
		}
		p, in := kurveControllerInThread(pl, rjJoiner)
		if p == nil || p.Id != "ctrl" {
			t.Fatalf("did not find the joiner's controller: %v", p)
		}
		if in.Seq != 3 || in.Dir != kurve.Left {
			t.Errorf("controller read back wrong: %+v", in)
		}
	})
}

// runRejoin scans the open channel newest-first and hands the match to the right
// game's resume. Here it is proven by where it fails: with no Kitty terminal each
// resume bails with a game-specific status, so that status names which game (and
// therefore which resume) it dispatched to.
func TestRunRejoinDispatch(t *testing.T) {
	newModel := func(posts []*model.Post) *Model {
		return &Model{me: &model.User{Id: rjJoiner}, posts: posts}
	}

	if m := newModel([]*model.Post{gorillasWorldPost(t, false)}); true {
		runRejoin(m, "")
		if !strings.Contains(m.status, "gorillas") {
			t.Errorf("a gorillas post should dispatch to gorillas resume; status=%q", m.status)
		}
	}
	if m := newModel([]*model.Post{kurveWorldPost(t, false)}); true {
		runRejoin(m, "")
		if !strings.Contains(m.status, "kurve") {
			t.Errorf("a kurve post should dispatch to kurve resume; status=%q", m.status)
		}
	}

	// Nothing of ours in the channel: say so, don't silently no-op.
	m := newModel([]*model.Post{{Id: "x", UserId: rjOther, Message: "hi"}})
	runRejoin(m, "")
	if !strings.Contains(m.status, "no game") {
		t.Errorf("an empty channel should report nothing to rejoin; status=%q", m.status)
	}

	// A game already open: refuse rather than stack a second one.
	m = newModel([]*model.Post{gorillasWorldPost(t, false)})
	m.gorillas.active = true
	runRejoin(m, "")
	if !strings.Contains(m.status, "already open") {
		t.Errorf("an open game should block a rejoin; status=%q", m.status)
	}
}
