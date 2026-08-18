package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"matterbox/internal/config"
	"matterbox/internal/listen"
)

func compileTestRules(t *testing.T, cfg []config.RuleConfig) []listen.Rule {
	t.Helper()
	rules, err := listen.CompileRules(ruleSpecs(cfg))
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	return rules
}

// TestRuleSpecsMapNewFields pins the config → listen mapping for the fields the
// triggers and the new conditions added: a gap here is a rule that silently
// loses a condition between the YAML and the matcher.
func TestRuleSpecsMapNewFields(t *testing.T) {
	yes := true
	cfg := []config.RuleConfig{{
		Name:     "everything",
		On:       config.StringList{"reaction"},
		Schedule: nil,
		Match: config.RuleMatchConfig{
			Emoji:       config.StringList{"eyes"},
			Reactor:     config.StringList{"bob"},
			ChannelType: config.StringList{"private"},
			FromBot:     &yes,
			Time:        &config.RuleTimeConfig{After: "09:00", Before: "17:00", Days: config.StringList{"mon"}},
		},
		Actions: []config.RuleActionConfig{{Type: "log"}},
	}}
	specs := ruleSpecs(cfg)
	m := specs[0].Match
	if len(specs[0].On) != 1 || specs[0].On[0] != "reaction" {
		t.Errorf("on = %v", specs[0].On)
	}
	if len(m.Emoji) != 1 || len(m.Reactors) != 1 || len(m.ChannelTypes) != 1 {
		t.Errorf("reaction/channel conditions lost: %+v", m)
	}
	if m.FromBot == nil || !*m.FromBot {
		t.Error("from_bot lost")
	}
	if m.Time == nil || m.Time.After != "09:00" || len(m.Time.Days) != 1 {
		t.Errorf("time window lost: %+v", m.Time)
	}
	if _, err := listen.CompileRules(specs); err != nil {
		t.Fatalf("mapped spec should compile: %v", err)
	}
}

// TestRuleSpecsMapSchedule covers the timer half of the mapping.
func TestRuleSpecsMapSchedule(t *testing.T) {
	cfg := []config.RuleConfig{{
		Name:     "standup",
		On:       config.StringList{"schedule"},
		Schedule: &config.RuleScheduleConfig{Cron: "0 9 * * 1-5"},
		Actions:  []config.RuleActionConfig{{Type: "send", Text: "standup", Channel: "eng/general"}},
	}}
	rules := compileTestRules(t, cfg)
	if got := rules[0].ScheduleText(); got != "cron 0 9 * * 1-5" {
		t.Errorf("ScheduleText = %q", got)
	}
	if got := rules[0].Kinds(); len(got) != 1 || got[0] != listen.EventSchedule {
		t.Errorf("kinds = %v", got)
	}
}

// TestListRules checks the listing renders the trigger, the conditions and the
// actions — the answer to "what did the daemon actually load".
func TestListRules(t *testing.T) {
	no := false
	cfg := []config.RuleConfig{
		{
			Name:    "watch-ops",
			Match:   config.RuleMatchConfig{Channel: config.StringList{"Ops*"}, Message: "(?i)sev-1", FromMe: &no},
			Actions: []config.RuleActionConfig{{Type: "log"}, {Type: "notify"}},
		},
		{
			Name:     "standup",
			On:       config.StringList{"schedule"},
			Schedule: &config.RuleScheduleConfig{Cron: "0 9 * * 1-5"},
			Actions:  []config.RuleActionConfig{{Type: "send", Text: "hi", Channel: "eng/general"}},
		},
	}
	var out bytes.Buffer
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.Local) // a Friday
	if err := listRules(&out, cfg, compileTestRules(t, cfg), now); err != nil {
		t.Fatalf("listRules: %v", err)
	}
	got := out.String()
	for _, want := range []string{"watch-ops", "on:      message", "channel=Ops*", "from_me=false", "log → notify", "cron 0 9 * * 1-5", "next Mon 24 Aug 09:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("listing should mention %q:\n%s", want, got)
		}
	}
}

// TestListRulesEmpty makes the no-rules case say what the daemon will actually
// do, rather than printing nothing.
func TestListRulesEmpty(t *testing.T) {
	var out bytes.Buffer
	if err := listRules(&out, nil, nil, time.Now()); err != nil {
		t.Fatalf("listRules: %v", err)
	}
	if !strings.Contains(out.String(), "built-in") {
		t.Errorf("empty listing should explain the fallback, got %q", out.String())
	}
}

// TestPrintExplanations covers the shape of `rules test` output: a match, a
// miss with its reason, and a rule that isn't listening for this kind.
func TestPrintExplanations(t *testing.T) {
	res := []listen.Explanation{
		{Rule: "fires", Matched: true, Actions: []string{"log"}},
		{Rule: "misses", Matched: false, Why: "channel"},
		{Rule: "scheduled", Skipped: true, Kinds: []string{"schedule"}},
		{Rule: "gated", Matched: true, Gate: "cooldown: 3h left of 24h", Actions: []string{"send"}},
	}
	var out bytes.Buffer
	if err := printExplanations(&out, listen.ProbeSpec{Message: "hi", Channel: "Ops"}, res, time.Now()); err != nil {
		t.Fatalf("printExplanations: %v", err)
	}
	got := out.String()
	for _, want := range []string{"fires", "channel doesn't match", "reacts to schedule", "held by cooldown", "1 of 4 rules would fire"} {
		if !strings.Contains(got, want) {
			t.Errorf("output should mention %q:\n%s", want, got)
		}
	}
}

// TestPrintExplanationsStops reflects evaluation order: nothing after a firing
// `stop` rule is reported, because nothing after it runs.
func TestPrintExplanationsStops(t *testing.T) {
	res := []listen.Explanation{
		{Rule: "first", Matched: true, Stop: true, Actions: []string{"exec"}},
		{Rule: "later", Matched: true, Actions: []string{"log"}},
	}
	var out bytes.Buffer
	if err := printExplanations(&out, listen.ProbeSpec{}, res, time.Now()); err != nil {
		t.Fatalf("printExplanations: %v", err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "✓  later") {
			t.Errorf("rules after a firing stop rule must not be listed:\n%s", out.String())
		}
	}
	if !strings.Contains(out.String(), "1 of 2 rules would fire") {
		t.Errorf("the count should still cover every rule:\n%s", out.String())
	}
}

// fakeMeta is a stand-in for the store's meta table.
type fakeMeta map[string]string

func (f fakeMeta) GetMeta(key string) (string, bool, error) {
	v, ok := f[key]
	return v, ok, nil
}

// TestPrintRuleStats renders the counters, including the never-fired dash that
// is the point of the verb.
func TestPrintRuleStats(t *testing.T) {
	cfg := []config.RuleConfig{
		{Name: "busy", Actions: []config.RuleActionConfig{{Type: "log"}}},
		{Name: "silent", Actions: []config.RuleActionConfig{{Type: "log"}}},
	}
	meta := fakeMeta{
		listen.RuleStatKey("busy", "count"): "42",
		listen.RuleStatKey("busy", "last"):  "1755500000000",
	}
	var out bytes.Buffer
	if err := printRuleStats(&out, meta, compileTestRules(t, cfg)); err != nil {
		t.Fatalf("printRuleStats: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "busy") || !strings.Contains(got, "42") {
		t.Errorf("stats should show the counter:\n%s", got)
	}
	if !strings.Contains(got, "never") {
		t.Errorf("a rule that never fired should say so:\n%s", got)
	}
}

// fakeLedger is a stand-in for the store's rule_state table.
type fakeLedger map[string]string

func (f fakeLedger) AllState() (map[string]string, error) { return map[string]string(f), nil }
func (f fakeLedger) GetState(key string) (string, bool, error) {
	v, ok := f[key]
	return v, ok, nil
}
func (f fakeLedger) SetState(key, value string) error { f[key] = value; return nil }
func (f fakeLedger) DeleteState(key string) error     { delete(f, key); return nil }

// TestRulesState walks the ledger verb: list, get, set, del, and the errors for
// a missing key or a bad sub-command.
func TestRulesState(t *testing.T) {
	led := fakeLedger{"zork:active": "p1"}

	var out bytes.Buffer
	if err := runRulesState(&out, led, nil); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "zork:active = p1") {
		t.Errorf("list should show the key: %q", out.String())
	}

	out.Reset()
	if err := runRulesState(&out, led, []string{"set", "greeted", "today"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if led["greeted"] != "today" {
		t.Errorf("set should write the ledger, got %v", led)
	}

	out.Reset()
	if err := runRulesState(&out, led, []string{"get", "greeted"}); err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.TrimSpace(out.String()) != "today" {
		t.Errorf("get = %q", out.String())
	}

	out.Reset()
	if err := runRulesState(&out, led, []string{"del", "zork:active"}); err != nil {
		t.Fatalf("del: %v", err)
	}
	if _, ok := led["zork:active"]; ok {
		t.Error("del should remove the key")
	}

	if err := runRulesState(&out, led, []string{"get", "nope"}); err == nil {
		t.Error("get on a missing key should error")
	}
	if err := runRulesState(&out, led, []string{"frobnicate"}); err == nil {
		t.Error("an unknown sub-command should error")
	}
}

// TestPostID accepts what a user actually has to hand: an id, or the permalink
// the UI copies.
func TestPostID(t *testing.T) {
	for in, want := range map[string]string{
		"8x4k9y": "8x4k9y",
		"https://chat.example.com/core/pl/8x4k9y":  "8x4k9y",
		"https://chat.example.com/core/pl/8x4k9y/": "8x4k9y",
	} {
		if got := postID(in); got != want {
			t.Errorf("postID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseProbeTime covers --at: a bare clock time means today, which is how a
// `time:` window gets tested without waiting for the hour.
func TestParseProbeTime(t *testing.T) {
	now := time.Date(2026, 8, 18, 15, 0, 0, 0, time.Local)
	got, err := parseProbeTime("09:30", now)
	if err != nil {
		t.Fatalf("parseProbeTime: %v", err)
	}
	want := time.Date(2026, 8, 18, 9, 30, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("parseProbeTime(09:30) = %s, want %s", got, want)
	}
	if got, err = parseProbeTime("2026-08-22 11:00", now); err != nil {
		t.Fatalf("parseProbeTime(date): %v", err)
	}
	if got.Day() != 22 {
		t.Errorf("parseProbeTime(date) = %s", got)
	}
	if _, err := parseProbeTime("half past nine", now); err == nil {
		t.Error("an unparseable time should error")
	}
}

// TestValidateProbeFlags rejects the probes that would otherwise test something
// other than what was asked for.
func TestValidateProbeFlags(t *testing.T) {
	if err := validateProbeFlags(probeFlags{message: "hi"}); err != nil {
		t.Errorf("a plain message probe should be valid: %v", err)
	}
	if err := validateProbeFlags(probeFlags{kind: "reaction", emoji: "eyes"}); err != nil {
		t.Errorf("a reaction probe should be valid: %v", err)
	}
	for _, f := range []probeFlags{
		{kind: "whenever"},
		{channelType: "priv"},
		{emoji: "eyes"},
		{reactor: "bob", kind: "message"},
	} {
		if err := validateProbeFlags(f); err == nil {
			t.Errorf("probeFlags %+v should be rejected", f)
		}
	}
}
