package listen

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func ptrBool(b bool) *bool { return &b }

func mustCompile(t *testing.T, specs ...RuleSpec) []Rule {
	t.Helper()
	rules, err := CompileRules(specs)
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	return rules
}

func TestMatchPost(t *testing.T) {
	const meID, meName = "u-me", "corne"

	channelPost := func(msg string, data map[string]string) (*model.Post, *model.WebSocketEvent) {
		p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: msg}
		base := map[string]string{"channel_type": "O", "channel_display_name": "Engineering", "sender_name": "@bob"}
		for k, v := range data {
			base[k] = v
		}
		return p, postedEvent(t, p, base)
	}

	cases := []struct {
		name string
		spec MatchSpec
		msg  string
		data map[string]string
		want bool
	}{
		{"empty matches anything", MatchSpec{}, "anything", nil, true},
		{"author match (case-insensitive)", MatchSpec{Authors: []string{"BOB"}}, "hi", nil, true},
		{"author mismatch", MatchSpec{Authors: []string{"alice"}}, "hi", nil, false},
		{"author list (OR) hit", MatchSpec{Authors: []string{"alice", "bob"}}, "hi", nil, true},
		{"author list (OR) miss", MatchSpec{Authors: []string{"alice", "carol"}}, "hi", nil, false},
		{"channel glob", MatchSpec{Channels: []string{"Eng*"}}, "hi", nil, true},
		{"channel glob miss", MatchSpec{Channels: []string{"Ops*"}}, "hi", nil, false},
		{"channel list (OR) hit", MatchSpec{Channels: []string{"Ops*", "Eng*"}}, "hi", nil, true},
		{"channel exact id", MatchSpec{Channels: []string{"c1"}}, "hi", nil, true},
		{"message regexp", MatchSpec{Message: `(?i)deploy`}, "Please DEPLOY now", nil, true},
		{"message regexp miss", MatchSpec{Message: `deploy`}, "nothing here", nil, false},
		{"dm required but channel", MatchSpec{DM: ptrBool(true)}, "hi", nil, false},
		{"not-dm required and channel", MatchSpec{DM: ptrBool(false)}, "hi", nil, true},
		{"mention hit", MatchSpec{Mention: true}, "@corne look", map[string]string{"mentions": mentionsData(t, meID)}, true},
		{"mention miss (not named)", MatchSpec{Mention: true}, "@channel look", map[string]string{"mentions": mentionsData(t, meID)}, false},
		{"combined AND all hold", MatchSpec{Authors: []string{"bob"}, Message: "ship it"}, "ship it", nil, true},
		{"combined AND one fails", MatchSpec{Authors: []string{"bob"}, Message: "ship it"}, "hold on", nil, false},
		{"not excludes author", MatchSpec{Channels: []string{"Eng*"}, Not: &MatchSpec{Authors: []string{"bob"}}}, "hi", nil, false},
		{"not keeps others", MatchSpec{Channels: []string{"Eng*"}, Not: &MatchSpec{Authors: []string{"alice"}}}, "hi", nil, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := compileMatch(c.spec)
			if err != nil {
				t.Fatalf("compileMatch: %v", err)
			}
			p, ev := channelPost(c.msg, c.data)
			if got := matchPost(ev, p, m, meID, meName, "", nil, nil); got != c.want {
				t.Errorf("matchPost = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMatchPostDMAndThreadAndFile(t *testing.T) {
	const meID, meName = "u-me", "corne"

	dm := &model.Post{Id: "p", ChannelId: "d1", UserId: "u-bob", Message: "hey"}
	dmEv := postedEvent(t, dm, map[string]string{"channel_type": "D", "sender_name": "@bob"})
	if m, _ := compileMatch(MatchSpec{DM: ptrBool(true)}); !matchPost(dmEv, dm, m, meID, meName, "", nil, nil) {
		t.Error("DM should match dm:true")
	}

	reply := &model.Post{Id: "p2", ChannelId: "c1", UserId: "u-bob", RootId: "root", Message: "in thread"}
	replyEv := postedEvent(t, reply, map[string]string{"channel_type": "O"})
	if m, _ := compileMatch(MatchSpec{IsThread: ptrBool(true)}); !matchPost(replyEv, reply, m, meID, meName, "", nil, nil) {
		t.Error("reply should match is_thread:true")
	}
	if m, _ := compileMatch(MatchSpec{IsThread: ptrBool(false)}); matchPost(replyEv, reply, m, meID, meName, "", nil, nil) {
		t.Error("reply should not match is_thread:false")
	}

	withFile := &model.Post{Id: "p3", ChannelId: "c1", UserId: "u-bob", Message: "see attached", FileIds: []string{"f1"}}
	fileEv := postedEvent(t, withFile, map[string]string{"channel_type": "O"})
	if m, _ := compileMatch(MatchSpec{HasFile: true}); !matchPost(fileEv, withFile, m, meID, meName, "", nil, nil) {
		t.Error("post with file should match has_file")
	}
	noFile := &model.Post{Id: "p4", ChannelId: "c1", UserId: "u-bob", Message: "no file"}
	noFileEv := postedEvent(t, noFile, map[string]string{"channel_type": "O"})
	if m, _ := compileMatch(MatchSpec{HasFile: true}); matchPost(noFileEv, noFile, m, meID, meName, "", nil, nil) {
		t.Error("post without file should not match has_file")
	}
}

func TestCompileRulesErrors(t *testing.T) {
	cases := []struct {
		name  string
		specs []RuleSpec
	}{
		{"bad message regexp", []RuleSpec{{Match: MatchSpec{Message: "("}, Actions: []ActionSpec{{Type: ActionLog}}}}},
		{"no actions", []RuleSpec{{Match: MatchSpec{Authors: []string{"bob"}}}}},
		{"bad not glob", []RuleSpec{{Match: MatchSpec{Not: &MatchSpec{Message: "("}}, Actions: []ActionSpec{{Type: ActionLog}}}}},
		{"unknown action", []RuleSpec{{Actions: []ActionSpec{{Type: "explode"}}}}},
		{"exec without command", []RuleSpec{{Actions: []ActionSpec{{Type: ActionExec}}}}},
		{"webhook without url", []RuleSpec{{Actions: []ActionSpec{{Type: ActionWebhook}}}}},
		{"react without emoji", []RuleSpec{{Actions: []ActionSpec{{Type: ActionReact}}}}},
		{"action without type", []RuleSpec{{Actions: []ActionSpec{{}}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := CompileRules(c.specs); err == nil {
				t.Errorf("want error for %s", c.name)
			}
		})
	}
}

func TestCompileRulesOK(t *testing.T) {
	rules := mustCompile(t,
		RuleSpec{
			Name:    "pager",
			Match:   MatchSpec{Channels: []string{"ops/*"}, Message: "(?i)sev-1"},
			Actions: []ActionSpec{{Type: ActionExec, Command: []string{"true"}}, {Type: ActionNotify}},
			Stop:    true,
		},
		RuleSpec{Actions: []ActionSpec{{Type: ActionReact, Emoji: ":eyes:"}}},
	)
	if len(rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rules))
	}
	if rules[0].Name != "pager" || !rules[0].Stop {
		t.Errorf("rule 0 metadata wrong: %+v", rules[0])
	}
	if rules[1].Name != "rule 2" {
		t.Errorf("unnamed rule should get a positional name, got %q", rules[1].Name)
	}
	// Emoji colons are trimmed at compile time.
	if rules[1].Actions[0].Emoji != "eyes" {
		t.Errorf("emoji = %q, want eyes", rules[1].Actions[0].Emoji)
	}
}

func TestDefaultRules(t *testing.T) {
	on := defaultRules(Options{NotifyOnMention: true})
	if len(on) != 1 || !on[0].Match.builtin || on[0].Actions[0].Type != ActionNotify {
		t.Fatalf("notify-on default rule wrong: %+v", on)
	}
	if off := defaultRules(Options{NotifyOnMention: false}); off != nil {
		t.Errorf("notify off should yield no rules, got %v", off)
	}
}

func TestGlobToRegexp(t *testing.T) {
	re, err := globToRegexp("Eng*ng")
	if err != nil {
		t.Fatalf("globToRegexp: %v", err)
	}
	if !re.MatchString("engineering") { // case-insensitive
		t.Error("glob should match case-insensitively")
	}
	if re.MatchString("Engineering team") {
		t.Error("glob is anchored; trailing text should not match")
	}
	// Special regexp chars in the pattern are matched literally.
	lit, _ := globToRegexp("a.b")
	if lit.MatchString("axb") {
		t.Error("dot in a glob should be literal, not a wildcard")
	}
}

// newTestEngine builds an Engine with a discard logger for action tests that
// don't need a Mattermost/Telegram client.
func newTestEngine(t *testing.T, opts Options) *Engine {
	t.Helper()
	return &Engine{opts: opts, log: log.New(io.Discard, "", 0)}
}

func TestRunExecPipesEnvelope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "captured.json")

	e := newTestEngine(t, Options{})
	p := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u-bob", Message: "deploy now", CreateAt: 1700000000000}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "channel_display_name": "Ops", "sender_name": "@bob"})

	a := Action{Type: ActionExec, Command: []string{"sh", "-c", "cat > " + out}}
	e.wg.Add(1)
	e.runExec(t.Context(), ev, p, a) // runs synchronously here, calls wg.Done
	e.wg.Wait()

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	var got envelope
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("stdin was not the JSON envelope: %v\n%s", err, data)
	}
	if got.PostID != "p1" || got.Channel != "Ops" || got.Author != "bob" || got.Message != "deploy now" || got.IsDM {
		t.Errorf("envelope mismatch: %+v", got)
	}
}

func TestRunWebhookPostsEnvelope(t *testing.T) {
	var (
		mu   sync.Mutex
		body []byte
		ct   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		ct = r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	e := newTestEngine(t, Options{})
	p := &model.Post{Id: "p9", ChannelId: "d1", UserId: "u-bob", Message: "ping"}
	ev := postedEvent(t, p, map[string]string{"channel_type": "D", "sender_name": "@bob"})

	a := Action{Type: ActionWebhook, URL: srv.URL}
	e.wg.Add(1)
	e.runWebhook(t.Context(), ev, p, a)
	e.wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var got envelope
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body not JSON: %v\n%s", err, body)
	}
	if got.PostID != "p9" || !got.IsDM {
		t.Errorf("webhook envelope mismatch: %+v", got)
	}
}

// TestApplyRulesStopAndSkip verifies evaluation order/stop and that
// system/empty posts never trigger any rule. It uses log actions (synchronous,
// no client needed) and a buffer logger to observe firing.
func TestApplyRulesStopAndSkip(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	lg := log.New(writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		lines = append(lines, string(b))
		mu.Unlock()
		return len(b), nil
	}), "", 0)

	e := &Engine{log: lg}
	e.rules = mustCompile(t,
		RuleSpec{Name: "first", Match: MatchSpec{Authors: []string{"bob"}}, Actions: []ActionSpec{{Type: ActionLog, Text: "FIRST"}}, Stop: true},
		RuleSpec{Name: "second", Match: MatchSpec{Authors: []string{"bob"}}, Actions: []ActionSpec{{Type: ActionLog, Text: "SECOND"}}},
	)

	p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "hi"}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@bob"})
	e.applyRules(t.Context(), ev, p)

	// A system message must not fire anything.
	sys := &model.Post{Id: "s", ChannelId: "c1", UserId: "u-bob", Message: "joined", Type: model.PostTypeJoinChannel}
	e.applyRules(t.Context(), postedEvent(t, sys, map[string]string{"channel_type": "O", "sender_name": "@bob"}), sys)

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 {
		t.Fatalf("want exactly one log line (stop after first, none for system), got %d: %v", len(lines), lines)
	}
	if want := "FIRST"; !strings.Contains(lines[0], want) {
		t.Errorf("first rule should have fired, got %q", lines[0])
	}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(b []byte) (int, error) { return w(b) }

// TestBuildEnvelopeEnriched pins the stable exec/webhook contract: the envelope
// carries thread, mention, team, and file context, not just the basics.
func TestBuildEnvelopeEnriched(t *testing.T) {
	e := newTestEngine(t, Options{ServerURL: "https://mm.example.com"})
	e.me = &model.User{Id: "u-me", Username: "corne"}
	e.teams = map[string]string{"t1": "core"}

	p := &model.Post{
		Id: "p1", ChannelId: "c1", UserId: "u-bob", RootId: "root1",
		Message:  "@corne see this",
		FileIds:  []string{"f1"},
		Metadata: &model.PostMetadata{Files: []*model.FileInfo{{Name: "diagram.png"}}},
		CreateAt: 123,
	}
	ev := postedEvent(t, p, map[string]string{
		"channel_type": "O", "channel_display_name": "Eng", "sender_name": "@bob",
		"team_id": "t1", "mentions": mentionsData(t, "u-me"),
	})

	env := e.buildEnvelope(ev, p)
	if !env.IsThread || env.RootID != "root1" {
		t.Errorf("thread fields wrong: is_thread=%v root=%q", env.IsThread, env.RootID)
	}
	if !env.Mentioned {
		t.Error("mentioned should be true (named + server-resolved)")
	}
	if env.Team != "core" || env.TeamID != "t1" {
		t.Errorf("team fields wrong: team=%q team_id=%q", env.Team, env.TeamID)
	}
	if len(env.Files) != 1 || env.Files[0] != "diagram.png" {
		t.Errorf("files = %v, want [diagram.png]", env.Files)
	}
	if env.Permalink != "https://mm.example.com/core/pl/p1" {
		t.Errorf("permalink = %q", env.Permalink)
	}
}

// TestRunWebhookHeaders verifies custom headers are sent and their values are
// expanded from the environment (so a token need not sit in the config file).
func TestRunWebhookHeaders(t *testing.T) {
	t.Setenv("MB_TEST_TOKEN", "s3cret")
	var (
		mu             sync.Mutex
		gotAuth, gotST string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		gotST = r.Header.Get("X-Static")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	e := newTestEngine(t, Options{})
	p := &model.Post{Id: "p", ChannelId: "c", UserId: "u-bob", Message: "hi"}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O"})
	a := Action{Type: ActionWebhook, URL: srv.URL, Headers: map[string]string{
		"Authorization": "Bearer ${MB_TEST_TOKEN}",
		"X-Static":      "plain",
	}}
	e.wg.Add(1)
	e.runWebhook(t.Context(), ev, p, a)
	e.wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if gotAuth != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want expanded token", gotAuth)
	}
	if gotST != "plain" {
		t.Errorf("X-Static = %q", gotST)
	}
}

// TestNotifyOptsFor checks per-rule notify overrides resolve over the daemon
// defaults (and that absent overrides inherit them).
func TestNotifyOptsFor(t *testing.T) {
	e := newTestEngine(t, Options{Summarize: true, TelegramChatID: "main"})

	def := e.notifyOptsFor(Action{Type: ActionNotify})
	if !def.summarize || def.urgent || def.chatID != "main" {
		t.Errorf("defaults not inherited: %+v", def)
	}

	over := e.notifyOptsFor(Action{Type: ActionNotify, Summarize: ptrBool(false), Urgent: true, ChatID: "other"})
	if over.summarize || !over.urgent || over.chatID != "other" {
		t.Errorf("overrides not applied: %+v", over)
	}
}

// TestNotifyMatches verifies the catch-up helpers: hasNotifyRule reflects
// whether any rule can notify, and notifyMatches only counts posts a notify
// rule actually matches (a react-only rule does not produce a catch-up entry).
func TestNotifyMatches(t *testing.T) {
	e := newTestEngine(t, Options{})
	e.me = &model.User{Id: "u-me", Username: "corne"}
	e.rules = mustCompile(t,
		RuleSpec{Match: MatchSpec{Channels: []string{"Eng*"}}, Actions: []ActionSpec{{Type: ActionNotify}}},
		RuleSpec{Match: MatchSpec{Channels: []string{"Op*"}}, Actions: []ActionSpec{{Type: ActionReact, Emoji: "eyes"}}},
	)
	if !e.hasNotifyRule() {
		t.Fatal("hasNotifyRule should be true")
	}

	p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "hi"}
	eng := postedEvent(t, p, map[string]string{"channel_type": "O", "channel_display_name": "Engineering"})
	if !e.notifyMatches(eng, p) {
		t.Error("post in Engineering should match the notify rule")
	}
	ops := postedEvent(t, p, map[string]string{"channel_type": "O", "channel_display_name": "Operations"})
	if e.notifyMatches(ops, p) {
		t.Error("post matching only a react rule should not count as a notify match")
	}

	e.rules = mustCompile(t, RuleSpec{Match: MatchSpec{}, Actions: []ActionSpec{{Type: ActionLog}}})
	if e.hasNotifyRule() {
		t.Error("a log-only ruleset has no notify rule")
	}
}
