package listen

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// testEngine builds a bare Engine for exercising the activity/mode helpers,
// which never touch the mm client.
func testEngine(meID string) *Engine {
	return &Engine{
		me:         &model.User{Id: meID},
		selfViewed: map[string]time.Time{},
		log:        log.New(io.Discard, "", 0),
	}
}

func TestParseMode(t *testing.T) {
	cases := map[string]struct {
		want Mode
		ok   bool
	}{
		"always": {ModeAlways, true}, "ON": {ModeAlways, true}, "true": {ModeAlways, true},
		"never": {ModeNever, true}, "off": {ModeNever, true},
		"idle": {ModeIdle, true}, " Away ": {ModeIdle, true}, "inactive": {ModeIdle, true},
		"": {ModeNever, false}, "sometimes": {ModeNever, false},
	}
	for in, c := range cases {
		got, ok := ParseMode(in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseMode(%q) = (%v, %v), want (%v, %v)", in, got, ok, c.want, c.ok)
		}
	}
	if ModeAlways.String() != "always" || ModeIdle.String() != "idle" || ModeNever.String() != "never" {
		t.Errorf("Mode.String mismatch: %s %s %s", ModeAlways, ModeIdle, ModeNever)
	}
}

func TestActiveElsewhere(t *testing.T) {
	e := testEngine("me")
	window := 5 * time.Minute

	if e.activeElsewhere(window) {
		t.Fatal("fresh engine should not be active elsewhere")
	}
	e.markForeignActivity()
	if !e.activeElsewhere(window) {
		t.Fatal("expected active right after markForeignActivity")
	}
	// A window of 0 disables the check (ModeIdle behaves like ModeAlways).
	if e.activeElsewhere(0) {
		t.Fatal("window<=0 must report not-active")
	}
	// Activity older than the window no longer counts.
	e.activityMu.Lock()
	e.lastForeignAt = time.Now().Add(-10 * time.Minute)
	e.activityMu.Unlock()
	if e.activeElsewhere(window) {
		t.Fatal("stale activity should be outside the window")
	}
}

func TestObserveActivity(t *testing.T) {
	const me = "me"

	typing := func(user, channel string) *model.WebSocketEvent {
		ev := model.NewWebSocketEvent(model.WebsocketEventTyping, "", channel, "", nil, "")
		ev.Add("user_id", user)
		return ev
	}
	statusChange := func(user, status string) *model.WebSocketEvent {
		ev := model.NewWebSocketEvent(model.WebsocketEventStatusChange, "", "", user, nil, "")
		ev.Add("user_id", user)
		ev.Add("status", status)
		return ev
	}

	t.Run("self typing marks active", func(t *testing.T) {
		e := testEngine(me)
		e.observeActivity(typing(me, "c1"))
		if !e.activeElsewhere(time.Minute) {
			t.Fatal("own typing should mark active elsewhere")
		}
	})
	t.Run("other user typing ignored", func(t *testing.T) {
		e := testEngine(me)
		e.observeActivity(typing("colleague", "c1"))
		if e.activeElsewhere(time.Minute) {
			t.Fatal("a colleague typing must not count as the user being active")
		}
	})
	t.Run("status online marks active", func(t *testing.T) {
		e := testEngine(me)
		e.observeActivity(statusChange(me, model.StatusOnline))
		if !e.activeElsewhere(time.Minute) {
			t.Fatal("coming online should mark active")
		}
	})
	t.Run("status offline does not", func(t *testing.T) {
		e := testEngine(me)
		e.observeActivity(statusChange(me, model.StatusOffline))
		if e.activeElsewhere(time.Minute) {
			t.Fatal("going offline is not activity")
		}
	})
	t.Run("status from broadcast user id", func(t *testing.T) {
		e := testEngine(me)
		ev := model.NewWebSocketEvent(model.WebsocketEventStatusChange, "", "", me, nil, "")
		ev.Add("status", model.StatusAway) // no user_id in data → fall back to broadcast
		e.observeActivity(ev)
		if !e.activeElsewhere(time.Minute) {
			t.Fatal("should resolve the user from the broadcast and mark active")
		}
	})
}

func TestObserveViewed(t *testing.T) {
	const me = "me"
	viewed := func(channels ...string) *model.WebSocketEvent {
		ev := model.NewWebSocketEvent(model.WebsocketEventMultipleChannelsViewed, "", "", me, nil, "")
		ct := map[string]any{}
		for _, c := range channels {
			ct[c] = float64(time.Now().UnixMilli())
		}
		ev.Add("channel_times", ct)
		return ev
	}

	t.Run("foreign view marks active", func(t *testing.T) {
		e := testEngine(me)
		e.observeViewed(viewed("c1"))
		if !e.activeElsewhere(time.Minute) {
			t.Fatal("a channel view from another client should mark active")
		}
	})
	t.Run("own mark-read is filtered", func(t *testing.T) {
		e := testEngine(me)
		e.noteSelfView("c1") // the daemon just marked c1 read
		e.observeViewed(viewed("c1"))
		if e.activeElsewhere(time.Minute) {
			t.Fatal("the daemon's own mark-read echo must not count as activity")
		}
	})
	t.Run("mixed batch with a foreign channel still marks active", func(t *testing.T) {
		e := testEngine(me)
		e.noteSelfView("c1")
		e.observeViewed(viewed("c1", "c2")) // c2 wasn't ours
		if !e.activeElsewhere(time.Minute) {
			t.Fatal("a non-self channel in the batch should mark active")
		}
	})
}

func TestRecentSelfViewTTL(t *testing.T) {
	e := testEngine("me")
	e.noteSelfView("c1")
	if !e.recentSelfView("c1") {
		t.Fatal("just-recorded self view should be recent")
	}
	// Age it past the TTL; recentSelfView should prune and report false.
	e.activityMu.Lock()
	e.selfViewed["c1"] = time.Now().Add(-2 * selfViewTTL)
	e.activityMu.Unlock()
	if e.recentSelfView("c1") {
		t.Fatal("expired self view should no longer be recent")
	}
	e.activityMu.Lock()
	_, still := e.selfViewed["c1"]
	e.activityMu.Unlock()
	if still {
		t.Fatal("expired self view should have been pruned")
	}
}

func TestViewedChannels(t *testing.T) {
	// Decoded-map form (in-process events).
	ev := model.NewWebSocketEvent(model.WebsocketEventMultipleChannelsViewed, "", "", "me", nil, "")
	ev.Add("channel_times", map[string]any{"a": 1.0, "b": 2.0})
	got := viewedChannels(ev)
	if len(got) != 2 || !got["a"] || !got["b"] {
		t.Fatalf("map form: got %v", got)
	}
	// JSON-string form.
	ev2 := model.NewWebSocketEvent(model.WebsocketEventMultipleChannelsViewed, "", "", "me", nil, "")
	ev2.Add("channel_times", `{"x":1,"y":2}`)
	got2 := viewedChannels(ev2)
	if len(got2) != 2 || !got2["x"] || !got2["y"] {
		t.Fatalf("string form: got %v", got2)
	}
	// Absent field → empty, no panic.
	ev3 := model.NewWebSocketEvent(model.WebsocketEventMultipleChannelsViewed, "", "", "me", nil, "")
	if len(viewedChannels(ev3)) != 0 || len(viewedChannels(nil)) != 0 {
		t.Fatal("absent/nil should yield empty set")
	}
}
