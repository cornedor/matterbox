package listen

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

// fakeMMClient returns an mm.Client pointed at an inert in-process server: every
// request gets an empty/OK response so client calls succeed without a real
// Mattermost, and POST /posts (CreatePost, used by the send action) bumps the
// optional counter so a test can assert a message was actually sent.
func fakeMMClient(t *testing.T, posts *int32) *mm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v4/posts") {
			if posts != nil {
				atomic.AddInt32(posts, 1)
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"new"}`)
			return
		}
		// users/ids, channel view, reactions, … — an inert, parseable reply.
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[]`)
	}))
	t.Cleanup(srv.Close)
	return mm.New(srv.URL, "tok")
}

// --- notify do-not-disturb gate ------------------------------------------

// TestNotifyGate pins the do-not-disturb policy that every notify action (the
// default bridge or a user rule) flows through. The signal is the catch-up
// cursor: notify() advances it to the post's timestamp once it decides to
// deliver, so a delivered post moves the cursor and a suppressed one leaves it
// at zero. A nil Telegram client makes notify() take its log-and-advance branch
// without any real delivery.
func TestNotifyGate(t *testing.T) {
	const ts = int64(1000)

	gateEngine := func(t *testing.T) *Engine {
		e := newStoreEngine(t)
		e.client = fakeMMClient(t, nil)
		e.tg = nil // log-only delivery, but still advances the cursor
		e.me = &model.User{Id: "u-me", Username: "corne"}
		return e
	}
	channel := func() (*model.Post, *model.WebSocketEvent) {
		p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "@corne hi", CreateAt: ts}
		return p, postedEvent(t, p, map[string]string{"channel_type": "O", "channel_display_name": "Eng", "sender_name": "@bob"})
	}
	dm := func() (*model.Post, *model.WebSocketEvent) {
		p := &model.Post{Id: "p", ChannelId: "d1", UserId: "u-bob", Message: "hey", CreateAt: ts}
		return p, postedEvent(t, p, map[string]string{"channel_type": "D", "sender_name": "@bob"})
	}
	own := func() (*model.Post, *model.WebSocketEvent) {
		p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-me", Message: "note", CreateAt: ts}
		return p, postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@corne"})
	}
	// A quiet window guaranteed to contain "now" (a wide band starting at the
	// current minute), so inQuietHoursNow — which reads the wall clock — is true.
	setQuietNow := func(e *Engine) {
		now := time.Now()
		cur := now.Hour()*60 + now.Minute()
		e.quietOn, e.quietStart, e.quietEnd = true, cur, (cur+600)%1440
	}

	run := func(t *testing.T, cfg func(*Engine), p *model.Post, ev *model.WebSocketEvent, urgent bool) int64 {
		e := gateEngine(t)
		if cfg != nil {
			cfg(e)
		}
		e.notifyGate(context.Background(), ev, p, notifyOpts{urgent: urgent})
		e.wg.Wait()
		return e.cursor()
	}

	t.Run("channel mention delivers", func(t *testing.T) {
		p, ev := channel()
		if got := run(t, nil, p, ev, false); got != ts {
			t.Errorf("a normal mention should deliver (cursor %d, want %d)", got, ts)
		}
	})
	t.Run("own message suppressed", func(t *testing.T) {
		p, ev := own()
		if got := run(t, nil, p, ev, false); got != 0 {
			t.Errorf("own message should be suppressed (cursor %d, want 0)", got)
		}
	})
	t.Run("own message with notify_self delivers", func(t *testing.T) {
		p, ev := own()
		cfg := func(e *Engine) { e.opts.NotifySelf = true }
		if got := run(t, cfg, p, ev, false); got != ts {
			t.Errorf("notify_self should deliver own message (cursor %d, want %d)", got, ts)
		}
	})
	t.Run("dm suppressed without notify_dms", func(t *testing.T) {
		p, ev := dm()
		if got := run(t, nil, p, ev, false); got != 0 {
			t.Errorf("DM should be suppressed when notify_dms is off (cursor %d, want 0)", got)
		}
	})
	t.Run("dm delivers with notify_dms", func(t *testing.T) {
		p, ev := dm()
		cfg := func(e *Engine) { e.opts.NotifyDMs = true }
		if got := run(t, cfg, p, ev, false); got != ts {
			t.Errorf("DM should deliver with notify_dms (cursor %d, want %d)", got, ts)
		}
	})
	t.Run("muted channel suppressed", func(t *testing.T) {
		p, ev := channel()
		cfg := func(e *Engine) { e.opts.RespectMutes = true; e.muted = map[string]bool{"c1": true} }
		if got := run(t, cfg, p, ev, false); got != 0 {
			t.Errorf("muted channel should be suppressed (cursor %d, want 0)", got)
		}
	})
	t.Run("urgent bypasses mute", func(t *testing.T) {
		p, ev := channel()
		cfg := func(e *Engine) { e.opts.RespectMutes = true; e.muted = map[string]bool{"c1": true} }
		if got := run(t, cfg, p, ev, true); got != ts {
			t.Errorf("urgent should deliver to a muted channel (cursor %d, want %d)", got, ts)
		}
	})
	t.Run("quiet hours suppressed", func(t *testing.T) {
		p, ev := channel()
		if got := run(t, setQuietNow, p, ev, false); got != 0 {
			t.Errorf("quiet hours should suppress (cursor %d, want 0)", got)
		}
	})
	t.Run("dnd suppressed", func(t *testing.T) {
		p, ev := channel()
		cfg := func(e *Engine) { e.opts.RespectDND = true; e.myStatus = model.StatusDnd }
		if got := run(t, cfg, p, ev, false); got != 0 {
			t.Errorf("dnd should suppress (cursor %d, want 0)", got)
		}
	})
	t.Run("urgent bypasses quiet hours", func(t *testing.T) {
		p, ev := channel()
		if got := run(t, setQuietNow, p, ev, true); got != ts {
			t.Errorf("urgent should deliver during quiet hours (cursor %d, want %d)", got, ts)
		}
	})
	t.Run("urgent bypasses dnd", func(t *testing.T) {
		p, ev := channel()
		cfg := func(e *Engine) { e.opts.RespectDND = true; e.myStatus = model.StatusDnd }
		if got := run(t, cfg, p, ev, true); got != ts {
			t.Errorf("urgent should deliver during dnd (cursor %d, want %d)", got, ts)
		}
	})
	t.Run("dnd ignored when respect_dnd off", func(t *testing.T) {
		p, ev := channel()
		cfg := func(e *Engine) { e.opts.RespectDND = false; e.myStatus = model.StatusDnd }
		if got := run(t, cfg, p, ev, false); got != ts {
			t.Errorf("respect_dnd off should deliver (cursor %d, want %d)", got, ts)
		}
	})
}

// --- send action loop-prevention + skips ---------------------------------

// TestRunSendSkips covers the send action's guards: it never replies to the
// reader's own message (so an ungated send rule can't loop on the very message
// it just posted), but does post for someone else's, and skips a body that
// renders empty.
func TestRunSendSkips(t *testing.T) {
	mk := func(t *testing.T, posts *int32) *Engine {
		e := newStoreEngine(t)
		e.client = fakeMMClient(t, posts)
		e.me = &model.User{Id: "u-me", Username: "corne"}
		return e
	}
	send := func(e *Engine, p *model.Post, a Action) {
		ev := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@x"})
		e.wg.Add(1)
		e.runSend(context.Background(), ev, p, a)
		e.wg.Wait()
	}
	greet, err := compileAction(ActionSpec{Type: ActionSend, Text: "hi {{ .author }}"})
	if err != nil {
		t.Fatalf("compileAction(send): %v", err)
	}

	t.Run("own message is not answered", func(t *testing.T) {
		var posts int32
		e := mk(t, &posts)
		send(e, &model.Post{Id: "p", ChannelId: "c1", UserId: "u-me", Message: "mine"}, greet)
		if n := atomic.LoadInt32(&posts); n != 0 {
			t.Errorf("send must skip the reader's own message, got %d posts", n)
		}
	})

	t.Run("other's message is answered", func(t *testing.T) {
		var posts int32
		e := mk(t, &posts)
		send(e, &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "yours"}, greet)
		if n := atomic.LoadInt32(&posts); n != 1 {
			t.Errorf("send should post a reply for another user's message, got %d posts", n)
		}
	})

	t.Run("empty rendered body is skipped", func(t *testing.T) {
		var posts int32
		e := mk(t, &posts)
		// {{ .channel }} is empty (no channel_display_name on the event), so the
		// body renders blank and the send is skipped rather than posting an empty.
		empty, err := compileAction(ActionSpec{Type: ActionSend, Text: "{{ .channel }}"})
		if err != nil {
			t.Fatalf("compileAction(send): %v", err)
		}
		send(e, &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "yours"}, empty)
		if n := atomic.LoadInt32(&posts); n != 0 {
			t.Errorf("an empty body should be skipped, got %d posts", n)
		}
	})
}

// TestResolveSendTargetBadSpec covers the spec-parse errors of a configured send
// target: a channel must be "team/channel" or "@user". These fail before any
// network call, so a bare engine exercises them.
func TestResolveSendTargetBadSpec(t *testing.T) {
	e := newTestEngine(t, Options{})
	for _, spec := range []string{"noslash", "team/", "/channel", ""} {
		if _, err := e.resolveSendTarget(context.Background(), spec); err == nil {
			t.Errorf("resolveSendTarget(%q) should error", spec)
		}
	}
}

// --- exec command rendering ----------------------------------------------

// TestRenderCommandEmptyExecutable pins the guard against exec'ing "": a command
// whose first argument renders empty is an error, not a confusing failed spawn.
func TestRenderCommandEmptyExecutable(t *testing.T) {
	a, err := compileAction(ActionSpec{Type: ActionExec, Command: []string{"{{ .channel }}", "arg"}})
	if err != nil {
		t.Fatalf("compileAction(exec): %v", err)
	}
	if _, err := renderCommand(a, envelope{}); err == nil {
		t.Error("an empty rendered executable must be an error")
	}
	// With a non-empty executable the argv renders normally.
	argv, err := renderCommand(a, envelope{Channel: "echo"})
	if err != nil {
		t.Fatalf("renderCommand: %v", err)
	}
	if len(argv) != 2 || argv[0] != "echo" || argv[1] != "arg" {
		t.Errorf("argv = %v, want [echo arg]", argv)
	}
}

// --- ledger key → env var sanitisation ------------------------------------

func TestEnvKeySanitize(t *testing.T) {
	cases := map[string]string{
		"failure_count": "FAILURE_COUNT",
		"hot:c1":        "HOT_C1",
		"odd key!":      "ODD_KEY", // trailing run trimmed
		"  spaced  ":    "SPACED",
		"123abc":        "", // leading digit → not its own var (only MATTERBOX_STATE)
		"!!!":           "", // nothing alphanumeric → empty
	}
	for in, want := range cases {
		if got := envKeySanitize(in); got != want {
			t.Errorf("envKeySanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- state action: empty key skip ----------------------------------------

// TestRunStateEmptyKeySkip checks that a templated key that renders blank is
// skipped (not written under an empty key).
func TestRunStateEmptyKeySkip(t *testing.T) {
	e := newStoreEngine(t)
	a, err := compileAction(ActionSpec{Type: ActionStateIncr, Key: "{{ .channel }}"}) // renders empty
	if err != nil {
		t.Fatalf("compileAction(state_incr): %v", err)
	}
	p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "x"}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@bob"}) // no channel_display_name
	e.runState(ev, p, a)

	if st, _ := e.store.AllState(); len(st) != 0 {
		t.Errorf("an empty key should write nothing, got ledger %v", st)
	}
}

// --- cooldown: team grouping + fail-open ----------------------------------

// TestCooldownByTeam checks that by:team keeps an independent interval per team,
// and that an unreadable persisted timestamp fails open (the rule is allowed to
// fire) rather than wedging the gate shut.
func TestCooldownByTeam(t *testing.T) {
	e := newStoreEngine(t)
	clk := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return clk }
	e.rules = mustCompile(t, RuleSpec{
		Name:    "weekly",
		Match:   MatchSpec{Cooldown: &CooldownSpec{Every: "24h", By: "team"}},
		Actions: []ActionSpec{{Type: ActionStateIncr, Key: "fires"}},
	})
	post := func(team string) {
		p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "hi"}
		ev := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@bob", "team_id": team})
		e.applyRules(context.Background(), ev, p)
	}
	count := func() int { v, _, _ := e.store.GetState("fires"); n, _ := strconv.Atoi(v); return n }

	post("t1") // fires for team t1
	post("t2") // fires for team t2, independently
	if count() != 2 {
		t.Fatalf("two teams should each fire once: %d", count())
	}
	post("t1") // t1 still within its 24h interval
	if count() != 2 {
		t.Fatalf("same team within the interval must not re-fire: %d", count())
	}

	// Fail-open: a corrupt last-fire timestamp is treated as ready.
	r := e.rules[0]
	p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "hi"}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@bob", "team_id": "t1"})
	if err := e.store.SetMeta(cooldownMetaKey(r.Name, r.Match.cool, ev, p), "not-a-number"); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}
	if !e.cooldownReady(r, ev, p) {
		t.Error("an unparseable last-fire timestamp should fail open (ready)")
	}
}

// --- small helpers --------------------------------------------------------

func TestPostHasFile(t *testing.T) {
	if !postHasFile(&model.Post{FileIds: []string{"f1"}}) {
		t.Error("a post with file ids has a file")
	}
	if !postHasFile(&model.Post{Metadata: &model.PostMetadata{Files: []*model.FileInfo{{Name: "a"}}}}) {
		t.Error("a post with embedded file metadata has a file")
	}
	if postHasFile(&model.Post{Message: "no files"}) {
		t.Error("a plain post has no file")
	}
}

func TestLogSuffix(t *testing.T) {
	if got := logSuffix("   "); got != "" {
		t.Errorf("blank output should produce no suffix, got %q", got)
	}
	if got := logSuffix("done\n"); got != ": done" {
		t.Errorf("logSuffix = %q, want %q", got, ": done")
	}
}
