package listen

import (
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
)

// newStoreEngine builds an Engine backed by a throwaway on-disk store, for the
// state actions (which need real persistence) and template rendering.
func newStoreEngine(t *testing.T) *Engine {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "listen.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return &Engine{
		opts:        Options{},
		log:         log.New(io.Discard, "", 0),
		store:       st,
		freqWindows: map[string][]time.Time{},
	}
}

func bobEvent(t *testing.T, msg string) (*model.Post, *model.WebSocketEvent) {
	t.Helper()
	p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: msg}
	return p, postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@bob"})
}

// --- frequency gate -------------------------------------------------------

func TestCompileFrequency(t *testing.T) {
	f, err := compileFrequency(FrequencySpec{Count: 3, Within: "10m"})
	if err != nil {
		t.Fatalf("compileFrequency: %v", err)
	}
	if f.count != 3 || f.within != 10*time.Minute || f.by != "global" {
		t.Fatalf("compiled = %+v, want count=3 within=10m by=global", f)
	}

	bad := []FrequencySpec{
		{Count: 0, Within: "1m"},             // count must be >= 1
		{Count: 2, Within: "nope"},           // unparsable duration
		{Count: 2, Within: "0s"},             // non-positive window
		{Count: 2, Within: "1m", By: "team"}, // unknown grouping
	}
	for _, s := range bad {
		if _, err := compileFrequency(s); err == nil {
			t.Errorf("compileFrequency(%+v) should have errored", s)
		}
	}
}

func TestFrequencyThresholdAndReset(t *testing.T) {
	e := newTestEngine(t, Options{})
	cur := time.Unix(1_700_000_000, 0)
	e.now = func() time.Time { return cur }
	f, _ := compileFrequency(FrequencySpec{Count: 3, Within: "10m"})
	p, ev := bobEvent(t, "sev-1")

	if e.frequencyAllows(0, f, ev, p) {
		t.Fatal("1st match must not fire")
	}
	cur = cur.Add(time.Minute)
	if e.frequencyAllows(0, f, ev, p) {
		t.Fatal("2nd match must not fire")
	}
	cur = cur.Add(time.Minute)
	if !e.frequencyAllows(0, f, ev, p) {
		t.Fatal("3rd match within the window must fire")
	}
	// Fired → window reset; the next match starts a fresh burst.
	cur = cur.Add(time.Minute)
	if e.frequencyAllows(0, f, ev, p) {
		t.Fatal("match right after firing must not fire (re-arming)")
	}
}

func TestFrequencyWindowExpiry(t *testing.T) {
	e := newTestEngine(t, Options{})
	cur := time.Unix(1_700_000_000, 0)
	e.now = func() time.Time { return cur }
	f, _ := compileFrequency(FrequencySpec{Count: 2, Within: "10m"})
	p, ev := bobEvent(t, "x")

	if e.frequencyAllows(0, f, ev, p) {
		t.Fatal("1st must not fire")
	}
	// The 2nd arrives after the window, so the 1st has expired and the count is
	// back to one — no burst, no fire.
	cur = cur.Add(11 * time.Minute)
	if e.frequencyAllows(0, f, ev, p) {
		t.Fatal("a hit outside the window must not count toward the threshold")
	}
}

func TestFrequencyByAuthorSeparatesBuckets(t *testing.T) {
	e := newTestEngine(t, Options{})
	cur := time.Unix(1_700_000_000, 0)
	e.now = func() time.Time { return cur }
	f, _ := compileFrequency(FrequencySpec{Count: 2, Within: "10m", By: "author"})

	mk := func(author string) (*model.Post, *model.WebSocketEvent) {
		p := &model.Post{Id: "p", ChannelId: "c1", Message: "x"}
		return p, postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@" + author})
	}
	pb, eb := mk("bob")
	pa, ea := mk("alice")

	if e.frequencyAllows(0, f, eb, pb) {
		t.Fatal("bob 1st must not fire")
	}
	if e.frequencyAllows(0, f, ea, pa) {
		t.Fatal("alice 1st is a separate bucket; must not fire")
	}
	if !e.frequencyAllows(0, f, eb, pb) {
		t.Fatal("bob 2nd reaches bob's threshold and must fire")
	}
}

// TestApplyRulesFrequencyGate checks the gate end-to-end: the rule's actions
// run only on the burst, and a sub-threshold match neither runs actions nor
// honours stop (the second rule still gets to log).
func TestApplyRulesFrequencyGate(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	lg := log.New(writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		lines = append(lines, string(b))
		mu.Unlock()
		return len(b), nil
	}), "", 0)

	e := &Engine{log: lg, freqWindows: map[string][]time.Time{}}
	cur := time.Unix(1_700_000_000, 0)
	e.now = func() time.Time { return cur }
	e.rules = mustCompile(t,
		RuleSpec{
			Name:    "burst",
			Match:   MatchSpec{Authors: []string{"bob"}, Frequency: &FrequencySpec{Count: 3, Within: "10m"}},
			Actions: []ActionSpec{{Type: ActionLog, Text: "BURST"}},
			Stop:    true,
		},
		RuleSpec{
			Name:    "always",
			Match:   MatchSpec{Authors: []string{"bob"}},
			Actions: []ActionSpec{{Type: ActionLog, Text: "ALWAYS"}},
		},
	)
	p, ev := bobEvent(t, "hi")
	for i := 0; i < 3; i++ {
		e.applyRules(t.Context(), ev, p)
		cur = cur.Add(time.Minute)
	}

	mu.Lock()
	defer mu.Unlock()
	var bursts, always int
	for _, l := range lines {
		if strings.Contains(l, "BURST") {
			bursts++
		}
		if strings.Contains(l, "ALWAYS") {
			always++
		}
	}
	if bursts != 1 {
		t.Errorf("burst rule should fire exactly once (on the 3rd), got %d", bursts)
	}
	// The gated rule's stop only bites on the message it actually fires: messages
	// 1 and 2 fall through to the second rule, message 3 is stopped after BURST.
	if always != 2 {
		t.Errorf("ungated rule should run on the 2 sub-threshold matches, got %d", always)
	}
}

// --- state actions --------------------------------------------------------

func TestStateIncrAction(t *testing.T) {
	e := newStoreEngine(t)
	e.rules = mustCompile(t, RuleSpec{
		Match:   MatchSpec{Authors: []string{"bob"}, Message: "Failed"},
		Actions: []ActionSpec{{Type: ActionStateIncr, Key: "failure_count"}},
	})
	p, ev := bobEvent(t, "Build Failed")
	e.applyRules(t.Context(), ev, p)
	e.applyRules(t.Context(), ev, p)

	if v, ok, _ := e.store.GetState("failure_count"); !ok || v != "2" {
		t.Fatalf("failure_count = %q ok=%v, want 2", v, ok)
	}
}

func TestStateSetTemplated(t *testing.T) {
	e := newStoreEngine(t)
	e.rules = mustCompile(t, RuleSpec{
		Match: MatchSpec{Authors: []string{"bob"}},
		Actions: []ActionSpec{
			{Type: ActionStateSet, Key: "last_failure_time", Value: "{{ .create_at }}"},
			{Type: ActionStateSet, Key: "last_msg:{{ .author }}", Value: "{{ .message }}"},
		},
	})
	p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "boom", CreateAt: 1_700_000_000_000}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@bob"})
	e.applyRules(t.Context(), ev, p)

	// Exact epoch text — proves create_at isn't widened to a float and rendered
	// in scientific notation.
	if v, _, _ := e.store.GetState("last_failure_time"); v != "1700000000000" {
		t.Fatalf("last_failure_time = %q, want exact epoch ms", v)
	}
	if v, _, _ := e.store.GetState("last_msg:bob"); v != "boom" {
		t.Fatalf("per-author key = %q, want boom", v)
	}
}

// TestStateActionsOrdered proves state writes run inline and in order: a
// state_set later in the list sees the value an earlier state_incr just wrote
// (via the .state template namespace).
func TestStateActionsOrdered(t *testing.T) {
	e := newStoreEngine(t)
	e.rules = mustCompile(t, RuleSpec{
		Match: MatchSpec{},
		Actions: []ActionSpec{
			{Type: ActionStateIncr, Key: "count"},
			{Type: ActionStateSet, Key: "last_count", Value: "{{ .state.count }}"},
		},
	})
	p, ev := bobEvent(t, "x")
	e.applyRules(t.Context(), ev, p)
	if v, _, _ := e.store.GetState("last_count"); v != "1" {
		t.Fatalf("last_count = %q, want 1 (state_set must observe the incr)", v)
	}
}

func TestStateDelAction(t *testing.T) {
	e := newStoreEngine(t)
	if err := e.store.SetState("k", "v"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e.rules = mustCompile(t, RuleSpec{
		Match:   MatchSpec{},
		Actions: []ActionSpec{{Type: ActionStateDel, Key: "k"}},
	})
	p, ev := bobEvent(t, "x")
	e.applyRules(t.Context(), ev, p)
	if _, ok, _ := e.store.GetState("k"); ok {
		t.Fatal("state_del should have removed k")
	}
}

func TestBuildEnvelopeCarriesState(t *testing.T) {
	e := newStoreEngine(t)
	_ = e.store.SetState("failure_count", "3")
	p, ev := bobEvent(t, "x")
	env := e.buildEnvelope(ev, p)
	if env.State["failure_count"] != "3" {
		t.Fatalf("envelope state = %v, want failure_count=3", env.State)
	}
}

func TestExecEnvIncludesState(t *testing.T) {
	env := envelope{PostID: "p", State: map[string]string{"failure_count": "3", "odd key!": "x"}}
	var hasJSON, hasCount, hasOdd bool
	for _, kv := range execEnv(env) {
		switch {
		case strings.HasPrefix(kv, "MATTERBOX_STATE={"):
			hasJSON = true
		case kv == "MATTERBOX_STATE_FAILURE_COUNT=3":
			hasCount = true
		case kv == "MATTERBOX_STATE_ODD_KEY=x":
			hasOdd = true
		}
	}
	if !hasJSON {
		t.Error("MATTERBOX_STATE (full JSON) missing")
	}
	if !hasCount {
		t.Error("MATTERBOX_STATE_FAILURE_COUNT missing")
	}
	if !hasOdd {
		t.Error("MATTERBOX_STATE_ODD_KEY (sanitized) missing")
	}
}

func TestCompileStateActionErrors(t *testing.T) {
	cases := []struct {
		name  string
		specs []RuleSpec
	}{
		{"state_set no key", []RuleSpec{{Actions: []ActionSpec{{Type: ActionStateSet, Value: "x"}}}}},
		{"state_incr no key", []RuleSpec{{Actions: []ActionSpec{{Type: ActionStateIncr}}}}},
		{"state_del no key", []RuleSpec{{Actions: []ActionSpec{{Type: ActionStateDel}}}}},
		{"bad key template", []RuleSpec{{Actions: []ActionSpec{{Type: ActionStateSet, Key: "{{ .author", Value: "x"}}}}},
		{"bad value template", []RuleSpec{{Actions: []ActionSpec{{Type: ActionStateSet, Key: "k", Value: "{{ .x"}}}}},
		{"bad frequency", []RuleSpec{{Match: MatchSpec{Frequency: &FrequencySpec{Count: 2, Within: "nope"}}, Actions: []ActionSpec{{Type: ActionLog}}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := CompileRules(c.specs); err == nil {
				t.Errorf("want compile error for %s", c.name)
			}
		})
	}
}

func TestCompileStateIncrBy(t *testing.T) {
	rules := mustCompile(t, RuleSpec{Actions: []ActionSpec{{Type: ActionStateIncr, Key: "k"}}})
	if rules[0].Actions[0].by != 1 {
		t.Fatalf("default by = %d, want 1", rules[0].Actions[0].by)
	}
	five := 5
	rules = mustCompile(t, RuleSpec{Actions: []ActionSpec{{Type: ActionStateIncr, Key: "k", By: &five}}})
	if rules[0].Actions[0].by != 5 {
		t.Fatalf("by = %d, want 5", rules[0].Actions[0].by)
	}
}
