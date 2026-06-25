package listen

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
	"matterbox/internal/telegram"
)

func TestBackoff(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{0, time.Second},        // clamped up to 1
		{1, time.Second},        // 1s << 0
		{2, 2 * time.Second},    // 1s << 1
		{3, 4 * time.Second},    // 1s << 2
		{6, 32 * time.Second},   // 1s << 5 (the cap)
		{7, 32 * time.Second},   // shift clamped at 5
		{100, 32 * time.Second}, // way past the cap, still 32s
	}
	for _, c := range cases {
		if got := backoff(c.n); got != c.want {
			t.Errorf("backoff(%d) = %s, want %s", c.n, got, c.want)
		}
	}
}

func TestIsUnauthorized(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{context.Canceled, false},
		{errString("get me: 401 Unauthorized"), true},
		{errString("UNAUTHORIZED: token revoked"), true}, // case-insensitive
		{errString("websocket: bad handshake (401)"), true},
		{errString("dial tcp: connection refused"), false},
		{errString("403 forbidden"), false}, // a different status is not 401
	}
	for _, c := range cases {
		if got := IsUnauthorized(c.err); got != c.want {
			t.Errorf("IsUnauthorized(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// errString is a tiny error type so the table can carry literal messages.
type errString string

func (e errString) Error() string { return string(e) }

func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog("  hello\nworld  "); got != "hello world" {
		t.Errorf("newlines should collapse to spaces and trim: %q", got)
	}
	if got := truncateForLog(""); got != "" {
		t.Errorf("empty stays empty, got %q", got)
	}
	long := strings.Repeat("x", logBodyCap+50)
	got := truncateForLog(long)
	if len(got) != logBodyCap+len("…") {
		t.Errorf("over-cap body should be clipped to %d bytes + ellipsis, got %d", logBodyCap, len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("clipped body should end with the ellipsis")
	}
}

func TestSleepCtx(t *testing.T) {
	// A cancelled context returns false immediately (no wait).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(ctx, time.Hour) {
		t.Error("cancelled ctx should return false")
	}
	// A short sleep that elapses returns true.
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Error("elapsed timer should return true")
	}
}

// TestAuthorized pins the security gate: only updates from the configured
// chat_id are acted on, across every inbound kind (message, button, reaction).
func TestAuthorized(t *testing.T) {
	e := newTestEngine(t, Options{TelegramChatID: "12345"})
	me := &telegram.User{ID: 12345}
	other := &telegram.User{ID: 999}

	cases := []struct {
		name string
		u    telegram.Update
		want bool
	}{
		{"message from configured chat", telegram.Update{Message: &telegram.Message{From: me}}, true},
		{"message from someone else", telegram.Update{Message: &telegram.Message{From: other}}, false},
		{"button from configured chat", telegram.Update{CallbackQuery: &telegram.CallbackQuery{From: me}}, true},
		{"button from someone else", telegram.Update{CallbackQuery: &telegram.CallbackQuery{From: other}}, false},
		{"reaction from configured chat", telegram.Update{MessageReaction: &telegram.MessageReactionUpdated{User: me}}, true},
		{"reaction from someone else", telegram.Update{MessageReaction: &telegram.MessageReactionUpdated{User: other}}, false},
		{"message with no sender", telegram.Update{Message: &telegram.Message{}}, false},
		{"empty update", telegram.Update{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := e.authorized(c.u); got != c.want {
				t.Errorf("authorized = %v, want %v", got, c.want)
			}
		})
	}
}

func TestInboundEnabled(t *testing.T) {
	tg := telegram.New("tok")
	cases := []struct {
		name string
		e    *Engine
		want bool
	}{
		{"no telegram client", &Engine{opts: Options{TwoWay: true, TelegramChatID: "1"}}, false},
		{"two-way off", &Engine{tg: tg, opts: Options{TwoWay: false, TelegramChatID: "1"}}, false},
		{"no chat id to lock to", &Engine{tg: tg, opts: Options{TwoWay: true}}, false},
		{"all set", &Engine{tg: tg, opts: Options{TwoWay: true, TelegramChatID: "1"}}, true},
	}
	for _, c := range cases {
		if got := c.e.inboundEnabled(); got != c.want {
			t.Errorf("%s: inboundEnabled = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestCursor covers the catch-up watermark: first run seeds it to ~now and
// persists, a fresh engine reads the stored value rather than re-seeding, and
// advanceCursor only ever moves forward (and survives a reload).
func TestCursor(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "listen.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	newEng := func() *Engine { return &Engine{store: st, log: log.New(io.Discard, "", 0)} }

	e1 := newEng()
	before := time.Now().UnixMilli()
	e1.loadCursor()
	first := e1.cursor()
	if first < before {
		t.Fatalf("first run should seed the cursor to ~now, got %d (< %d)", first, before)
	}

	// A fresh engine reads the persisted watermark, not a new "now".
	e2 := newEng()
	e2.loadCursor()
	if e2.cursor() != first {
		t.Fatalf("second load should read the persisted cursor %d, got %d", first, e2.cursor())
	}

	// advanceCursor never moves backward.
	e2.advanceCursor(first - 100)
	if e2.cursor() != first {
		t.Errorf("a lower timestamp must not move the cursor back, got %d", e2.cursor())
	}
	// Forward advances are honoured and persisted.
	e2.advanceCursor(first + 100)
	if e2.cursor() != first+100 {
		t.Errorf("a higher timestamp should advance the cursor, got %d", e2.cursor())
	}
	e3 := newEng()
	e3.loadCursor()
	if e3.cursor() != first+100 {
		t.Errorf("advance should persist across a reload, got %d", e3.cursor())
	}
}

func TestSystemPrompt(t *testing.T) {
	e := newTestEngine(t, Options{NotifyPrompt: "Be terse."})
	e.me = &model.User{Id: "u-me", Username: "corne"}
	got := e.systemPrompt("DM from @bob")
	if !strings.HasPrefix(got, "Be terse.") {
		t.Errorf("system prompt should start with the configured prompt, got %q", got)
	}
	if !strings.Contains(got, "@corne") {
		t.Error("system prompt should name the reader so the model summarizes from their view")
	}
	if !strings.Contains(got, "DM from @bob") {
		t.Error("system prompt should carry the source label")
	}
}

// TestCatchupEvent verifies the synthetic "posted" event the catch-up path
// builds so a missed post can be matched against the same rules a live one is:
// it carries the channel type/name/team and sender, and only sets the mentions
// field when the reader is actually named.
func TestCatchupEvent(t *testing.T) {
	e := newTestEngine(t, Options{})
	e.me = &model.User{Id: "u-me", Username: "corne"}
	ch := &model.Channel{Type: model.ChannelTypeOpen, DisplayName: "Engineering", TeamId: "t1"}

	named := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u-bob", Message: "@corne look"}
	ev := e.catchupEvent(ch, named, "bob")
	if eventStr(ev, "channel_type") != "O" || eventStr(ev, "channel_display_name") != "Engineering" {
		t.Errorf("channel fields wrong: type=%q name=%q", eventStr(ev, "channel_type"), eventStr(ev, "channel_display_name"))
	}
	if eventStr(ev, "team_id") != "t1" {
		t.Errorf("team_id = %q, want t1", eventStr(ev, "team_id"))
	}
	if eventStr(ev, "sender_name") != "@bob" {
		t.Errorf("sender_name = %q, want @bob", eventStr(ev, "sender_name"))
	}
	if !wsMentions(ev)["u-me"] {
		t.Error("a post that names the reader should carry the mentions set")
	}

	// A post that does not name the reader carries no mentions, so a mention
	// rule won't match it on catch-up.
	plain := &model.Post{Id: "p2", ChannelId: "c1", UserId: "u-bob", Message: "@channel standup"}
	if wsMentions(e.catchupEvent(ch, plain, "bob"))["u-me"] {
		t.Error("a non-personal message should not synthesise a mention")
	}

	// A nil channel (defensive) just omits the channel fields.
	nilCh := e.catchupEvent(nil, plain, "")
	if eventStr(nilCh, "channel_type") != "" {
		t.Error("nil channel should leave channel_type empty")
	}
}

func TestPermalink(t *testing.T) {
	e := newTestEngine(t, Options{ServerURL: "https://mm.example.com/"}) // trailing slash
	e.teams = map[string]string{"t1": "core"}
	e.defaultTeam = "fallback"

	post := &model.Post{Id: "p1", ChannelId: "c1"}

	known := postedEvent(t, post, map[string]string{"team_id": "t1"})
	if got := e.permalink(known, "p1"); got != "https://mm.example.com/core/pl/p1" {
		t.Errorf("known team permalink = %q", got)
	}
	// An unknown/absent team falls back to the default team (used for DMs).
	dm := postedEvent(t, post, map[string]string{"channel_type": "D"})
	if got := e.permalink(dm, "p1"); got != "https://mm.example.com/fallback/pl/p1" {
		t.Errorf("fallback permalink = %q", got)
	}
	// No team at all → no link.
	noTeam := newTestEngine(t, Options{ServerURL: "https://mm.example.com"})
	if got := noTeam.permalink(dm, "p1"); got != "" {
		t.Errorf("no team should yield no link, got %q", got)
	}
	// No server URL or no post id → no link.
	if got := e.permalink(known, ""); got != "" {
		t.Errorf("empty post id should yield no link, got %q", got)
	}
	noURL := newTestEngine(t, Options{})
	if got := noURL.permalink(known, "p1"); got != "" {
		t.Errorf("empty server URL should yield no link, got %q", got)
	}
}

// TestIsMutedAndRefreshGate covers the mute set: isMuted reads it, and
// refreshMuted is a no-op (leaves the set untouched) when RespectMutes is off or
// there is no known reader — so it never dereferences a nil client in that mode.
func TestIsMutedAndRefreshGate(t *testing.T) {
	e := newTestEngine(t, Options{})
	e.muted = map[string]bool{"c-muted": true}
	if !e.isMuted("c-muted") {
		t.Error("c-muted should report muted")
	}
	if e.isMuted("c-open") {
		t.Error("an unlisted channel should not report muted")
	}

	// RespectMutes off: refreshMuted returns before touching the (nil) client.
	e.refreshMuted(context.Background())
	if !e.isMuted("c-muted") {
		t.Error("refresh with RespectMutes off should leave the set unchanged")
	}

	// RespectMutes on but no reader: still a no-op, no nil-client panic.
	e.opts.RespectMutes = true
	e.me = nil
	e.refreshMuted(context.Background())
	if !e.isMuted("c-muted") {
		t.Error("refresh with no reader should leave the set unchanged")
	}
}

// TestNew checks the constructor's defaulting and rule selection: ContextPosts
// falls back, quiet hours are parsed once, and rules come from the config when
// present, otherwise the synthesised default (or none when notify is off).
func TestNew(t *testing.T) {
	// No rules + notify on → the single builtin default rule.
	def := New(nil, nil, nil, nil, nil, Options{NotifyOnMention: true, QuietHours: "22:00-08:00"}, nil)
	if def.opts.ContextPosts != defaultContextPosts {
		t.Errorf("ContextPosts should default to %d, got %d", defaultContextPosts, def.opts.ContextPosts)
	}
	if !def.quietOn || def.quietStart != 1320 || def.quietEnd != 480 {
		t.Errorf("quiet hours not parsed: on=%v start=%d end=%d", def.quietOn, def.quietStart, def.quietEnd)
	}
	if len(def.rules) != 1 || !def.rules[0].Match.builtin {
		t.Errorf("notify-on with no config rules should yield the builtin default, got %+v", def.rules)
	}
	if def.usesState {
		t.Error("the default notify rule uses no state")
	}

	// No rules + notify off → a pure cache-warmer (no rules).
	warm := New(nil, nil, nil, nil, nil, Options{NotifyOnMention: false}, nil)
	if len(warm.rules) != 0 {
		t.Errorf("notify-off with no config rules should yield no rules, got %d", len(warm.rules))
	}

	// Config rules win over the default, and usesState is computed from them.
	custom := mustCompile(t, RuleSpec{
		Match:   MatchSpec{State: []StateCondSpec{{Key: "k", Exists: ptrBool(true)}}},
		Actions: []ActionSpec{{Type: ActionLog}},
	})
	withRules := New(nil, nil, nil, nil, nil, Options{Rules: custom, ContextPosts: 5}, nil)
	if withRules.opts.ContextPosts != 5 {
		t.Errorf("an explicit ContextPosts should be kept, got %d", withRules.opts.ContextPosts)
	}
	if len(withRules.rules) != 1 || withRules.rules[0].Match.builtin {
		t.Error("config rules should replace the builtin default")
	}
	if !withRules.usesState {
		t.Error("usesState should be derived from the configured rules")
	}
}
