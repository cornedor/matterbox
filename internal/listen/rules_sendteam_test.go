package listen

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// --- team matcher ---------------------------------------------------------

func TestMatchPostTeam(t *testing.T) {
	// A post in team t-core (URL name "core"), in an open channel.
	p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "hi"}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "team_id": "t-core"})

	cases := []struct {
		name     string
		teams    []string
		teamName string // the resolved slug matches() would pass
		want     bool
	}{
		{"glob on slug hits", []string{"cor*"}, "core", true},
		{"exact slug hits", []string{"core"}, "core", true},
		{"exact team id hits (raw fallback)", []string{"t-core"}, "core", true},
		{"list ORs", []string{"ops", "cor*"}, "core", true},
		{"miss", []string{"ops*"}, "core", false},
		// A DM carries no team: team_id and the resolved name are both empty, so a
		// team condition never matches one.
		{"dm has no team", []string{"core"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := compileMatch(MatchSpec{Teams: c.teams})
			if err != nil {
				t.Fatalf("compileMatch: %v", err)
			}
			evt := ev
			if c.teamName == "" { // emulate a DM event (no team_id)
				evt = postedEvent(t, p, map[string]string{"channel_type": "D"})
			}
			if got := matchPost(evt, p, m, "", "", c.teamName, nil, nil); got != c.want {
				t.Errorf("matchPost = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMatchTeamAndChannelANDed(t *testing.T) {
	p := &model.Post{Id: "p", ChannelId: "c1", UserId: "u-bob", Message: "hi"}
	ev := postedEvent(t, p, map[string]string{"channel_type": "O", "team_id": "t-core", "channel_display_name": "General"})
	m, err := compileMatch(MatchSpec{Teams: []string{"core"}, Channels: []string{"Gen*"}})
	if err != nil {
		t.Fatalf("compileMatch: %v", err)
	}
	if !matchPost(ev, p, m, "", "", "core", nil, nil) {
		t.Error("team+channel both holding should match")
	}
	// Right channel, wrong team → no match (fields are ANDed).
	if matchPost(ev, p, m, "", "", "other", nil, nil) {
		t.Error("wrong team should fail even when the channel matches")
	}
}

// --- send action ----------------------------------------------------------

func TestCompileActionSend(t *testing.T) {
	ca, err := compileAction(ActionSpec{Type: ActionSend, Text: "Good morning {{ .author }}", Channel: "eng/general", Thread: true})
	if err != nil {
		t.Fatalf("compileAction(send): %v", err)
	}
	if ca.textTmpl == nil {
		t.Error("send must compile its text into textTmpl")
	}
	if ca.Channel != "eng/general" || !ca.Thread {
		t.Errorf("send channel/thread = %q/%v, want eng/general/true", ca.Channel, ca.Thread)
	}

	if _, err := compileAction(ActionSpec{Type: ActionSend}); err == nil {
		t.Error("send without text should error")
	}
	if _, err := compileAction(ActionSpec{Type: ActionSend, Text: "{{ .author "}); err == nil {
		t.Error("send with a bad template should error at compile time")
	}
}

// --- now/today template helpers ------------------------------------------

func TestTemplateDateFuncs(t *testing.T) {
	orig := templateClock
	t.Cleanup(func() { templateClock = orig })
	templateClock = func() time.Time { return time.Date(2026, 6, 22, 9, 30, 0, 0, time.UTC) }

	cases := map[string]string{
		"greeted:{{ today }}":         "greeted:2026-06-22",
		`at {{ now.Format "15:04" }}`: "at 09:30",
	}
	for tmplText, want := range cases {
		tmpl, err := compileTemplate(tmplText)
		if err != nil {
			t.Fatalf("compileTemplate(%q): %v", tmplText, err)
		}
		var b strings.Builder
		if err := tmpl.Execute(&b, map[string]any{}); err != nil {
			t.Fatalf("execute(%q): %v", tmplText, err)
		}
		if b.String() != want {
			t.Errorf("%q rendered %q, want %q", tmplText, b.String(), want)
		}
	}
}

// --- cooldown gate --------------------------------------------------------

func TestCompileCooldown(t *testing.T) {
	c, err := compileCooldown(CooldownSpec{Every: "48h", By: "team"})
	if err != nil {
		t.Fatalf("compileCooldown: %v", err)
	}
	if c.every != 48*time.Hour || c.by != "team" {
		t.Fatalf("compiled = %+v, want every=48h by=team", c)
	}
	bad := []CooldownSpec{
		{Every: ""},               // missing duration
		{Every: "nope"},           // unparsable
		{Every: "0s"},             // non-positive
		{Every: "1h", By: "user"}, // unknown grouping
	}
	for _, s := range bad {
		if _, err := compileCooldown(s); err == nil {
			t.Errorf("compileCooldown(%+v) should have errored", s)
		}
	}
}

// TestCooldownGate is the general "every N" gate: a rule fires at most once per
// interval. A state_incr counts firings (standing in for the actual side effect)
// and the engine clock is driven by hand.
func TestCooldownGate(t *testing.T) {
	e := newStoreEngine(t)
	clk := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return clk }
	e.rules = mustCompile(t, RuleSpec{
		Name:    "every-2-days",
		Match:   MatchSpec{Cooldown: &CooldownSpec{Every: "48h"}},
		Actions: []ActionSpec{{Type: ActionStateIncr, Key: "fires"}},
	})

	fire := func() {
		p, ev := bobEvent(t, "hi")
		e.applyRules(t.Context(), ev, p)
	}
	count := func() int { v, _, _ := e.store.GetState("fires"); n, _ := strconv.Atoi(v); return n }

	fire() // t0 → fires
	if count() != 1 {
		t.Fatalf("first message should fire: fires=%d", count())
	}
	clk = clk.Add(time.Hour)
	fire() // +1h → still within the interval
	if count() != 1 {
		t.Fatalf("a message within the interval must not re-fire: fires=%d", count())
	}
	clk = clk.Add(48 * time.Hour)
	fire() // +49h → interval elapsed, re-arms
	if count() != 2 {
		t.Fatalf("a message after the interval should fire again: fires=%d", count())
	}
}

// TestCooldownByGroup checks that by:channel keeps an independent interval per
// channel — one busy channel can't suppress a greeting in another.
func TestCooldownByGroup(t *testing.T) {
	e := newStoreEngine(t)
	clk := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return clk }
	e.rules = mustCompile(t, RuleSpec{
		Name:    "per-channel",
		Match:   MatchSpec{Cooldown: &CooldownSpec{Every: "24h", By: "channel"}},
		Actions: []ActionSpec{{Type: ActionStateIncr, Key: "fires"}},
	})
	post := func(ch string) {
		p := &model.Post{Id: "p", ChannelId: ch, UserId: "u-bob", Message: "hi"}
		ev := postedEvent(t, p, map[string]string{"channel_type": "O", "sender_name": "@bob"})
		e.applyRules(t.Context(), ev, p)
	}
	count := func() int { v, _, _ := e.store.GetState("fires"); n, _ := strconv.Atoi(v); return n }

	post("c1") // fires for c1
	post("c2") // fires for c2, independently
	if count() != 2 {
		t.Fatalf("two channels should each fire once: %d", count())
	}
	post("c1") // c1 is still on cooldown
	if count() != 2 {
		t.Fatalf("same channel within the interval must not re-fire: %d", count())
	}
}

// TestApplyRulesOncePerDay is the calendar-aligned alternative to the cooldown
// gate: a rule fires once per calendar day (resetting at local midnight, not on a
// rolling 24h window), keyed by a per-day ledger key built from {{ today }}. A
// state_incr stands in for the "good morning" send so the test can count firings.
func TestApplyRulesOncePerDay(t *testing.T) {
	orig := templateClock
	t.Cleanup(func() { templateClock = orig })
	day := time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC)
	templateClock = func() time.Time { return day }

	e := newStoreEngine(t)
	e.rules = mustCompile(t, RuleSpec{
		Name:  "good-morning",
		Match: MatchSpec{State: []StateCondSpec{{Key: "greeted:{{ today }}", Exists: ptrBool(false)}}},
		Actions: []ActionSpec{
			{Type: ActionStateIncr, Key: "greetings"},
			{Type: ActionStateSet, Key: "greeted:{{ today }}", Value: "1"},
		},
	})
	e.usesState = rulesUseState(e.rules)

	p, ev := bobEvent(t, "morning all")
	e.applyRules(t.Context(), ev, p)
	e.applyRules(t.Context(), ev, p) // a second message the same day must not re-greet

	if v, _, _ := e.store.GetState("greetings"); v != "1" {
		t.Fatalf("greetings = %q after two same-day messages, want 1", v)
	}

	// A new calendar day → the per-day key differs → it re-arms and greets again.
	day = day.AddDate(0, 0, 1)
	e.applyRules(t.Context(), ev, p)
	if v, _, _ := e.store.GetState("greetings"); v != "2" {
		t.Fatalf("greetings = %q on the next day, want 2", v)
	}
}
