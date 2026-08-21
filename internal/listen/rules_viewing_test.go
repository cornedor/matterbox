package listen

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/control"
	"matterbox/internal/testsock"
)

// fakeTUI serves a control socket that answers `status` with the given Status,
// standing in for a running TUI. The counter (optional) records how many
// queries actually reached it, so a test can prove the TTL cache collapses a
// burst.
func fakeTUI(t *testing.T, s control.Status, queries *int32) string {
	t.Helper()
	path := testsock.Path(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc := bufio.NewScanner(conn)
				for sc.Scan() {
					if strings.TrimSpace(sc.Text()) != control.VerbStatus {
						continue
					}
					if queries != nil {
						atomic.AddInt32(queries, 1)
					}
					b, _ := json.Marshal(s)
					conn.Write(append(b, '\n'))
				}
			}()
		}
	}()
	return path
}

// --- the viewing condition ------------------------------------------------

// viewing: false is the whole point — a notification rule that stays quiet
// about the conversation on your screen and fires for everything else.
func TestMatchViewing(t *testing.T) {
	p, ev := bobEvent(t, "hi") // channel c1
	reading := control.Status{ChannelID: "c1", Focused: true}
	elsewhere := control.Status{ChannelID: "c2", Focused: true}
	buried := control.Status{ChannelID: "c1"} // open, but the window is behind others

	cases := []struct {
		name string
		spec MatchSpec
		tui  control.Status
		want bool
	}{
		{"not viewing, reading it", MatchSpec{Viewing: ptrBool(false)}, reading, false},
		{"not viewing, reading something else", MatchSpec{Viewing: ptrBool(false)}, elsewhere, true},
		{"not viewing, window buried", MatchSpec{Viewing: ptrBool(false)}, buried, true},
		{"not viewing, no TUI at all", MatchSpec{Viewing: ptrBool(false)}, control.Status{}, true},
		{"viewing, reading it", MatchSpec{Viewing: ptrBool(true)}, reading, true},
		{"viewing, no TUI at all", MatchSpec{Viewing: ptrBool(true)}, control.Status{}, false},
		{"unset matches either way", MatchSpec{}, reading, true},
	}
	for _, c := range cases {
		m, err := compileMatch(c.spec)
		if err != nil {
			t.Fatalf("%s: compile: %v", c.name, err)
		}
		if got := matchPost(ev, p, m, "", "", "", nil, nil, c.tui); got != c.want {
			t.Errorf("%s: matched %t, want %t", c.name, got, c.want)
		}
	}
}

// A nested not: must see the same reading of the TUI as the outer match —
// the fact is resolved once per post and passed down, not re-asked.
func TestMatchViewingInsideNot(t *testing.T) {
	p, ev := bobEvent(t, "hi")
	m, err := compileMatch(MatchSpec{Not: &MatchSpec{Viewing: ptrBool(true)}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if matchPost(ev, p, m, "", "", "", nil, nil, control.Status{ChannelID: "c1", Focused: true}) {
		t.Error("not: {viewing: true} must not match while the channel is being read")
	}
	if !matchPost(ev, p, m, "", "", "", nil, nil, control.Status{}) {
		t.Error("not: {viewing: true} must match when nothing is being read")
	}
}

func TestRulesUseViewing(t *testing.T) {
	logs := []ActionSpec{{Type: ActionLog}}
	plain := mustCompile(t, RuleSpec{Match: MatchSpec{Mention: true}, Actions: logs})
	direct := mustCompile(t, RuleSpec{Match: MatchSpec{Viewing: ptrBool(false)}, Actions: logs})
	nested := mustCompile(t, RuleSpec{Match: MatchSpec{Not: &MatchSpec{Viewing: ptrBool(true)}}, Actions: logs})

	if rulesUseViewing(plain) {
		t.Error("a ruleset without viewing must not ask the TUI")
	}
	if !rulesUseViewing(direct) || !rulesUseViewing(nested) {
		t.Error("a viewing condition (nested or not) must be detected")
	}
}

// --- asking the TUI -------------------------------------------------------

func TestTUIStatusCaches(t *testing.T) {
	var queries int32
	e := newStoreEngine(t)
	e.needsTUIStatus = true
	e.tuiSocket = fakeTUI(t, control.Status{ChannelID: "c1", Focused: true}, &queries)
	e.tuiTTL = time.Minute

	for i := 0; i < 5; i++ {
		if !e.tuiStatus().Viewing("c1") {
			t.Fatalf("query %d: expected the TUI to report viewing c1", i)
		}
	}
	if got := atomic.LoadInt32(&queries); got != 1 {
		t.Fatalf("the TUI was asked %d times, want 1 — the TTL cache should collapse the burst", got)
	}
}

// Everything that isn't a clear yes reads as "not viewing", so a rule keeps
// firing rather than going silently dark.
func TestTUIStatusFailsOpen(t *testing.T) {
	t.Run("no socket", func(t *testing.T) {
		e := newStoreEngine(t)
		e.needsTUIStatus = true
		e.tuiSocket = testsock.Path(t)
		if e.tuiStatus() != (control.Status{}) {
			t.Error("a missing socket must read as no TUI")
		}
	})
	t.Run("ruleset can't use it", func(t *testing.T) {
		e := newStoreEngine(t)
		e.needsTUIStatus = false
		e.tuiSocket = fakeTUI(t, control.Status{ChannelID: "c1", Focused: true}, nil)
		if e.tuiStatus() != (control.Status{}) {
			t.Error("a cache-warmer daemon must not consult the TUI at all")
		}
	})
}

// New wires the socket and decides whether it's worth asking: a notify-capable
// ruleset needs it (the gate) even with no viewing condition anywhere.
func TestNewSetsTUIWiring(t *testing.T) {
	sock := testsock.Path(t)
	mk := func(opts Options) *Engine {
		return New(nil, nil, nil, nil, nil, opts, nil)
	}

	if e := mk(Options{TUISocket: sock}); e.tuiSocket != sock {
		t.Errorf("tuiSocket = %q, want the configured %q", e.tuiSocket, sock)
	}
	if e := mk(Options{TUISocket: sock, NotifyOnMention: true}); !e.needsTUIStatus {
		t.Error("the built-in notify bridge should consult the TUI")
	}
	if e := mk(Options{TUISocket: sock}); e.needsTUIStatus {
		t.Error("a pure cache-warmer should not consult the TUI")
	}
	viewingRule := mustCompile(t, RuleSpec{
		Match:   MatchSpec{Viewing: ptrBool(false)},
		Actions: []ActionSpec{{Type: ActionLog}},
	})
	if e := mk(Options{TUISocket: sock, Rules: viewingRule}); !e.needsTUIStatus {
		t.Error("a viewing rule should consult the TUI")
	}
}

// --- the notify gate ------------------------------------------------------

// The Telegram bridge gets the same treatment without any rule change: no push
// for the conversation you're focused on. The cursor is the signal — notify()
// advances it only when it decides to deliver.
func TestNotifyGateSkipsWhatYoureReading(t *testing.T) {
	const ts = int64(1000)
	p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "@corne hi", CreateAt: ts}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "channel_display_name": "Eng", "sender_name": "@bob"})

	run := func(t *testing.T, s control.Status, urgent bool) int64 {
		t.Helper()
		e := newStoreEngine(t)
		e.client = fakeMMClient(t, nil)
		e.me = &model.User{Id: "u-me", Username: "corne"}
		e.needsTUIStatus = true
		e.tuiSocket = fakeTUI(t, s, nil)
		e.notifyGate(context.Background(), msgTrigger(ev, p), notifyOpts{urgent: urgent})
		e.wg.Wait()
		return e.cursor()
	}

	if got := run(t, control.Status{ChannelID: "c1", Focused: true}, false); got != 0 {
		t.Errorf("a mention in the channel you're reading should be suppressed (cursor %d, want 0)", got)
	}
	// Urgent doesn't bypass it: this isn't a preference about being disturbed,
	// it's the fact that the message is already on your screen.
	if got := run(t, control.Status{ChannelID: "c1", Focused: true}, true); got != 0 {
		t.Errorf("urgent should not bypass the viewing gate (cursor %d, want 0)", got)
	}
	if got := run(t, control.Status{ChannelID: "c2", Focused: true}, false); got != ts {
		t.Errorf("a mention elsewhere should deliver (cursor %d, want %d)", got, ts)
	}
	if got := run(t, control.Status{ChannelID: "c1"}, false); got != ts {
		t.Errorf("an unfocused window must not suppress (cursor %d, want %d)", got, ts)
	}
}
