package listen

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/control"
)

// logEngine returns an engine that records its log lines, plus a counter for
// how many of them contain a marker — the same trick the frequency tests use to
// observe which rules fired without running a real action.
func logEngine(t *testing.T) (*Engine, func(string) int) {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	e := newStoreEngine(t)
	e.log = log.New(writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		lines = append(lines, string(b))
		mu.Unlock()
		return len(b), nil
	}), "", 0)
	return e, func(want string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, l := range lines {
			if strings.Contains(l, want) {
				n++
			}
		}
		return n
	}
}

// TestTriggerKindsAreHonoured is the core of the `on:` field: a rule reacts to
// the events it lists and nothing else, and a rule that lists none still reacts
// to new messages exactly as it did before triggers existed.
func TestTriggerKindsAreHonoured(t *testing.T) {
	e, count := logEngine(t)
	e.rules = mustCompile(t,
		RuleSpec{Name: "on-message", Actions: []ActionSpec{{Type: ActionLog, Text: "MSG"}}},
		RuleSpec{Name: "on-edit", On: []string{EventEdit}, Actions: []ActionSpec{{Type: ActionLog, Text: "EDIT"}}},
		RuleSpec{Name: "on-either", On: []string{EventMessage, EventEdit}, Actions: []ActionSpec{{Type: ActionLog, Text: "BOTH"}}},
	)
	p, ev := bobEvent(t, "hello")

	e.applyRules(t.Context(), ev, p)
	if count("MSG") != 1 || count("EDIT") != 0 || count("BOTH") != 1 {
		t.Errorf("message trigger: MSG=%d EDIT=%d BOTH=%d", count("MSG"), count("EDIT"), count("BOTH"))
	}

	e.applyTrigger(t.Context(), trigger{kind: EventEdit, ev: ev, post: p})
	if count("MSG") != 1 || count("EDIT") != 1 || count("BOTH") != 2 {
		t.Errorf("edit trigger: MSG=%d EDIT=%d BOTH=%d", count("MSG"), count("EDIT"), count("BOTH"))
	}
}

// TestDeleteTriggerSeesTheTombstone confirms the delete kind is exempt from the
// "no deleted posts" guard that every other kind keeps — a delete rule is about
// a deleted post by definition.
func TestDeleteTriggerSeesTheTombstone(t *testing.T) {
	e, count := logEngine(t)
	e.rules = mustCompile(t,
		RuleSpec{Name: "gone", On: []string{EventDelete}, Actions: []ActionSpec{{Type: ActionLog, Text: "GONE"}}},
	)
	p := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u-bob", Message: "oops", DeleteAt: 123}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@bob"})

	e.applyTrigger(t.Context(), trigger{kind: EventDelete, ev: ev, post: p})
	if count("GONE") != 1 {
		t.Errorf("delete rule should fire on a deleted post, got %d", count("GONE"))
	}
	// The same post must not reach a message rule.
	e.rules = mustCompile(t, RuleSpec{Name: "msg", Actions: []ActionSpec{{Type: ActionLog, Text: "MSG"}}})
	e.applyRules(t.Context(), ev, p)
	if count("MSG") != 0 {
		t.Errorf("a deleted post must not fire a message rule, got %d", count("MSG"))
	}
}

// reactionEvent builds the websocket event Mattermost sends for a reaction.
func reactionEvent(t *testing.T, r model.Reaction) *model.WebSocketEvent {
	t.Helper()
	ev := model.NewWebSocketEvent(model.WebsocketEventReactionAdded, "", "c1", "", nil, "")
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal reaction: %v", err)
	}
	ev.Add("reaction", string(b))
	return ev
}

// TestReactionTrigger walks the whole reaction path: the event names only ids,
// so the engine looks the post up (from the cache here) and synthesises the
// posted-shaped event the matcher reads — and the rule matches on the emoji and
// on whose post it landed on.
func TestReactionTrigger(t *testing.T) {
	e, count := logEngine(t)
	e.client = fakeMMClient(t, nil)
	e.me = &model.User{Id: "u-me", Username: "corne"}
	p := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u-me", Message: "ship it", CreateAt: 1}
	if err := e.store.UpsertMany([]*model.Post{p}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	e.rules = mustCompile(t,
		RuleSpec{
			Name:    "eyes-on-mine",
			On:      []string{EventReaction},
			Match:   MatchSpec{Emoji: []string{"eyes"}, FromMe: ptrBool(true)},
			Actions: []ActionSpec{{Type: ActionLog, Text: "SEEN"}},
		},
		RuleSpec{
			Name:    "other-emoji",
			On:      []string{EventReaction},
			Match:   MatchSpec{Emoji: []string{"tada"}},
			Actions: []ActionSpec{{Type: ActionLog, Text: "PARTY"}},
		},
	)

	trg, ok := e.reactionTrigger(t.Context(), reactionEvent(t, model.Reaction{UserId: "u-bob", PostId: "p1", EmojiName: "eyes"}), EventReaction)
	if !ok {
		t.Fatal("reactionTrigger should resolve a cached post")
	}
	if trg.post.Id != "p1" {
		t.Fatalf("trigger post = %q, want p1", trg.post.Id)
	}
	if got := eventStr(trg.ev, emojiKey); got != "eyes" {
		t.Fatalf("emoji = %q, want eyes", got)
	}
	e.applyTrigger(t.Context(), trg)
	if count("SEEN") != 1 {
		t.Errorf("emoji+from_me rule should fire, got %d", count("SEEN"))
	}
	if count("PARTY") != 0 {
		t.Errorf("a rule for another emoji must not fire, got %d", count("PARTY"))
	}
}

// TestReactorCondition pins the split the reaction kind introduces: author and
// from_me stay about the post's writer, and who reacted is `reactor`.
func TestReactorCondition(t *testing.T) {
	m, err := compileMatch(MatchSpec{Reactors: []string{"alice"}})
	if err != nil {
		t.Fatalf("compileMatch: %v", err)
	}
	p := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u-me", Message: "hi"}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@corne", reactorKey: "alice"})
	if !matchPost(ev, p, m, "u-me", "corne", "", nil, nil, control.Status{}) {
		t.Error("reactor should match the user who reacted")
	}
	ev2 := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@corne", reactorKey: "bob"})
	if matchPost(ev2, p, m, "u-me", "corne", "", nil, nil, control.Status{}) {
		t.Error("reactor should not match somebody else")
	}
}

// TestReactionFieldsNeedAReactionRule keeps a condition that can never hold
// from compiling: an emoji test on a message-only rule would just never fire.
func TestReactionFieldsNeedAReactionRule(t *testing.T) {
	_, err := CompileRules([]RuleSpec{{
		Name:    "oops",
		Match:   MatchSpec{Emoji: []string{"eyes"}},
		Actions: []ActionSpec{{Type: ActionLog}},
	}})
	if err == nil {
		t.Fatal("emoji on a message rule should be a compile error")
	}
	if !strings.Contains(err.Error(), "on: reaction") {
		t.Errorf("error should point at the fix, got %v", err)
	}
}

// TestSelfReactionEchoIsIgnored covers the loop-breaker: the reaction the react
// action adds comes straight back over the websocket, and a rule that reacts to
// reactions would otherwise re-fire on its own output forever. A reaction the
// user made elsewhere still counts.
func TestSelfReactionEchoIsIgnored(t *testing.T) {
	e, _ := logEngine(t)
	e.client = fakeMMClient(t, nil)
	e.me = &model.User{Id: "u-me", Username: "corne"}
	p := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u-bob", Message: "hi", CreateAt: 1}
	if err := e.store.UpsertMany([]*model.Post{p}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	e.wg.Add(1)
	act, err := compileAction(ActionSpec{Type: ActionReact, Emoji: "eyes"})
	if err != nil {
		t.Fatalf("compileAction: %v", err)
	}
	e.runReact(t.Context(), msgTrigger(postedEvent(t, p, nil), p), act)
	e.wg.Wait()

	echo := reactionEvent(t, model.Reaction{UserId: "u-me", PostId: "p1", EmojiName: "eyes"})
	if _, ok := e.reactionTrigger(t.Context(), echo, EventReaction); ok {
		t.Error("the echo of our own reaction must not trigger rules")
	}
	// The note is consumed, so reacting again from another client does trigger.
	if _, ok := e.reactionTrigger(t.Context(), echo, EventReaction); !ok {
		t.Error("a later reaction with the same emoji should trigger")
	}
	// Somebody else's reaction is never an echo.
	other := reactionEvent(t, model.Reaction{UserId: "u-bob", PostId: "p1", EmojiName: "eyes"})
	if _, ok := e.reactionTrigger(t.Context(), other, EventReaction); !ok {
		t.Error("another user's reaction should trigger")
	}
}
