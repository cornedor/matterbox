package listen

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/telemetry"
)

// The daemon's labels have to be catalogue values or they are dropped on a
// machine nobody is watching — which is the whole problem the daemon's telemetry
// exists to solve, so a silent drop here would be particularly useless.

// TestRuleActionsAreCatalogued walks every action type the config accepts and
// checks each maps to a declared label. The mapping is deliberate rather than a
// pass-through: renaming an action in config.yaml must not rename a property
// value the dashboards group on.
func TestRuleActionsAreCatalogued(t *testing.T) {
	allowed := propValues(t, "rule_fired", "action")
	for _, kind := range validActionTypes {
		got := ruleAction(kind)
		if !contains(allowed, got) {
			t.Errorf("ruleAction(%q) = %q, which the catalogue doesn't allow (%v)", kind, got, allowed)
		}
	}
	// The delivery paths people rely on stay distinguishable; the rest share
	// "other" on purpose.
	for kind, want := range map[string]string{
		ActionNotify:   "notify",
		ActionExec:     "exec",
		ActionReact:    "react",
		ActionMarkRead: "mark_read",
		ActionSend:     "reply",
		ActionWebhook:  "other",
		ActionLog:      "other",
		ActionStateSet: "other",
	} {
		if got := ruleAction(kind); got != want {
			t.Errorf("ruleAction(%q) = %q, want %q", kind, got, want)
		}
	}
}

// TestRuleTriggersSplitDMsFromMentions: "rules fire on DMs and nothing else" and
// "rules fire on everything" are very different products, and the `on:` kind
// alone cannot tell them apart.
func TestRuleTriggersSplitDMsFromMentions(t *testing.T) {
	allowed := propValues(t, "rule_fired", "trigger")
	e := &Engine{me: &model.User{Id: "u-me", Username: "corne"}}
	p := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u-bob", Message: "hi"}

	cases := []struct {
		name string
		t    trigger
		want string
	}{
		{"a schedule", trigger{kind: EventSchedule, post: p, ev: postedEvent(t, p, nil)}, "schedule"},
		{"a reaction", trigger{kind: EventReaction, post: p, ev: postedEvent(t, p, nil)}, "reaction"},
		{"an edit", trigger{kind: EventEdit, post: p, ev: postedEvent(t, p, nil)}, "other"},
		{"a DM", trigger{kind: EventMessage, post: p,
			ev: postedEvent(t, p, map[string]string{"channel_type": string(model.ChannelTypeDirect)})}, "dm"},
		{"an ordinary channel post", trigger{kind: EventMessage, post: p, ev: postedEvent(t, p, nil)}, "message"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := e.ruleTrigger(c.t)
			if got != c.want {
				t.Errorf("ruleTrigger = %q, want %q", got, c.want)
			}
			if !contains(allowed, got) {
				t.Errorf("ruleTrigger returned %q, which the catalogue doesn't allow (%v)", got, allowed)
			}
		})
	}
}

// TestDaemonChannelsReportsWhatIsWired is the denominator for everything else
// the daemon says: "nobody uses two-way replies" means nothing without knowing
// how many daemons have the inbound channel switched on at all.
func TestDaemonChannelsReportsWhatIsWired(t *testing.T) {
	allowed := propValues(t, "daemon_started", "channels_on")

	bare := &Engine{}
	if got := bare.daemonChannels(); len(got) != 0 {
		t.Errorf("a cache-warmer with nothing configured reported %v", got)
	}

	e, _ := logEngine(t)
	e.opts.Summarize = true
	rules, err := CompileRules([]RuleSpec{{
		Name:    "notify me",
		Actions: []ActionSpec{{Type: ActionExec, Command: []string{"true"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.SetRules(rules)

	got := e.daemonChannels()
	for _, id := range got {
		if !contains(allowed, id) {
			t.Errorf("daemonChannels reported %q, which the catalogue doesn't allow (%v)", id, allowed)
		}
	}
	if !contains(got, "exec") {
		t.Errorf("a rule with an exec action wasn't reported: %v", got)
	}
	if !contains(got, "summarize") {
		t.Errorf("summarize was configured but not reported: %v", got)
	}
	// No Telegram client, so nothing about delivery may be claimed.
	if contains(got, "telegram") || contains(got, "two_way") {
		t.Errorf("delivery was claimed with no bot configured: %v", got)
	}
}

// propValues returns the catalogue's allowed values for one property.
func propValues(t *testing.T, event, prop string) []string {
	t.Helper()
	spec, ok := telemetry.Spec(event)
	if !ok {
		t.Fatalf("event %q is not catalogued", event)
	}
	for _, p := range spec.Props {
		if p.Name == prop {
			return p.Values
		}
	}
	t.Fatalf("event %q has no property %q", event, prop)
	return nil
}

func contains(set []string, s string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}
