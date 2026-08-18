package listen

import (
	"strconv"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestExplain is the `matterbox rules test` contract: every rule gets a verdict,
// a rule that doesn't match says which condition stopped it, and a rule that
// doesn't listen for this kind of event says so rather than looking like a
// failed match.
func TestExplain(t *testing.T) {
	e, _ := logEngine(t)
	e.me = &model.User{Id: "u-me", Username: "corne"}
	e.rules = mustCompile(t,
		RuleSpec{
			Name:    "ops-deploys",
			Match:   MatchSpec{Channels: []string{"Ops*"}, Message: `(?i)deploy`},
			Actions: []ActionSpec{{Type: ActionLog}},
		},
		RuleSpec{
			Name:    "eng-deploys",
			Match:   MatchSpec{Channels: []string{"Eng*"}, Message: `(?i)deploy`},
			Actions: []ActionSpec{{Type: ActionExec, Command: []string{"/bin/true"}}},
		},
		RuleSpec{
			Name:     "nightly",
			On:       []string{EventSchedule},
			Schedule: &ScheduleSpec{Cron: "0 3 * * *"},
			Actions:  []ActionSpec{{Type: ActionLog}},
		},
	)

	res := e.Explain(ProbeSpec{Message: "deploy prod", Channel: "Ops-Alerts", Author: "bob"})
	if len(res) != 3 {
		t.Fatalf("want a verdict per rule, got %d", len(res))
	}
	if !res[0].Matched {
		t.Errorf("ops rule should match, why=%q", res[0].Why)
	}
	if res[1].Matched || res[1].Why != "channel" {
		t.Errorf("eng rule should fail on channel, got matched=%v why=%q", res[1].Matched, res[1].Why)
	}
	if !res[2].Skipped {
		t.Error("a schedule rule should be reported as not listening for a message")
	}
	if got := res[0].Actions; len(got) != 1 || got[0] != ActionLog {
		t.Errorf("actions = %v", got)
	}
}

// TestExplainReportsCooldown shows a matching rule that would still be held
// back — the case that otherwise looks like a broken rule.
func TestExplainReportsCooldown(t *testing.T) {
	e, _ := logEngine(t)
	e.rules = mustCompile(t, RuleSpec{
		Name:    "greeting",
		Match:   MatchSpec{Cooldown: &CooldownSpec{Every: "24h"}},
		Actions: []ActionSpec{{Type: ActionLog}},
	})
	now := time.Now()
	e.now = func() time.Time { return now }
	if err := e.store.SetMeta(cooldownMetaKey("greeting", e.rules[0].Match.cool, nil, &model.Post{}),
		strconv.FormatInt(now.Add(-time.Hour).UnixMilli(), 10)); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	res := e.Explain(ProbeSpec{Message: "hi"})
	if !res[0].Matched {
		t.Fatalf("rule should match, why=%q", res[0].Why)
	}
	if res[0].Gate == "" {
		t.Error("a rule inside its cooldown should report the gate holding it")
	}
}

// TestExplainRunsNoActions is the promise the verb makes: a dry run touches
// nothing. A state action would otherwise write the ledger just for being
// explained.
func TestExplainRunsNoActions(t *testing.T) {
	e, count := logEngine(t)
	e.rules = mustCompile(t, RuleSpec{
		Name:  "counter",
		Match: MatchSpec{},
		Actions: []ActionSpec{
			{Type: ActionStateIncr, Key: "explained"},
			{Type: ActionLog, Text: "RAN"},
		},
	})
	if res := e.Explain(ProbeSpec{Message: "hi"}); !res[0].Matched {
		t.Fatalf("rule should match, why=%q", res[0].Why)
	}
	if _, ok, _ := e.store.GetState("explained"); ok {
		t.Error("explaining a rule must not run its state action")
	}
	if count("RAN") != 0 {
		t.Error("explaining a rule must not run its log action")
	}
}

// TestSetRulesSwapsLive covers the reload path: new rules take effect without a
// restart, and an empty set falls back to the built-in notify rule rather than
// leaving the daemon inert.
func TestSetRulesSwapsLive(t *testing.T) {
	e, count := logEngine(t)
	e.opts.NotifyOnMention = true
	e.applyRuleSet(mustCompile(t, RuleSpec{Name: "first", Actions: []ActionSpec{{Type: ActionLog, Text: "FIRST"}}}))

	p, ev := bobEvent(t, "hello")
	e.applyRules(t.Context(), ev, p)
	if count("FIRST") != 1 {
		t.Fatalf("initial rule should fire, got %d", count("FIRST"))
	}

	e.SetRules(mustCompile(t, RuleSpec{Name: "second", Actions: []ActionSpec{{Type: ActionLog, Text: "SECOND"}}}))
	e.applyRules(t.Context(), ev, p)
	if count("FIRST") != 1 {
		t.Errorf("the replaced rule must not fire again, got %d", count("FIRST"))
	}
	if count("SECOND") != 1 {
		t.Errorf("the reloaded rule should fire, got %d", count("SECOND"))
	}

	e.SetRules(nil)
	if got := e.ruleSet(); len(got) != 1 || got[0].Name != "notify-mentions-and-dms" {
		t.Errorf("an empty reload should restore the built-in rule, got %+v", got)
	}
	if !e.ruleSet()[0].fires(EventMessage) {
		t.Error("the built-in rule must still react to messages")
	}
}
