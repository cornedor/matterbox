package listen

import (
	"strings"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/telemetry"
)

// Telemetry for the daemon.
//
// `matterbox listen` is the least observable thing matterbox does: it runs
// unattended for weeks on a machine nobody is looking at, its rules engine is
// the largest configuration surface in the product, and every action it takes
// fails into a log file that has no reader. So a rule whose exec action has been
// broken since a path changed, or a Telegram delivery that stopped working when
// a token expired, is invisible until someone notices the silence — which for a
// notification daemon is exactly the failure mode it is meant to prevent.
//
// Two events answer that: rule_fired says which rule *kinds* run and whether
// their actions succeed, and notification_actioned says whether the buttons and
// inline replies on a delivered notification are used and then work. Neither
// carries a rule's name, its command line, a channel or a message — a rule name
// is written by the user and can name an organisation's internal tooling, so
// only the action type and the outcome are sent.

// ruleAction maps an action type to the catalogue's label. Written out rather
// than passing a.Type through, because the config's vocabulary is ours to change
// and a metric's isn't: renaming an action in config.yaml must not silently
// rename a property value the dashboards group on.
//
// The state actions, the log action and the webhook share "other". They are
// counted so a rule that only writes the ledger isn't invisible, but they are
// not the question — which delivery paths people rely on is.
func ruleAction(kind string) string {
	switch kind {
	case ActionNotify:
		return "notify"
	case ActionExec:
		return "exec"
	case ActionReact:
		return "react"
	case ActionMarkRead:
		return "mark_read"
	case ActionSend:
		return "reply"
	}
	return "other"
}

// ruleTrigger maps a trigger to the catalogue's label. A message trigger is
// split into the three cases the daemon exists for — a DM, a personal mention,
// or any other post — because "rules fire on DMs and nothing else" and "rules
// fire on everything" are very different products, and the `on:` kind alone
// can't tell them apart.
func (e *Engine) ruleTrigger(t trigger) string {
	switch t.kind {
	case EventSchedule:
		return "schedule"
	case EventReaction, EventUnreact:
		return "reaction"
	case EventMessage:
		if eventStr(t.ev, "channel_type") == string(model.ChannelTypeDirect) {
			return "dm"
		}
		if e.me != nil && wsMentions(t.ev)[e.me.Id] {
			return "mention"
		}
		return "message"
	}
	return "other"
}

// reportRule records one action of one fired rule, with what happened to it.
// Called from each action runner rather than from runActions, because the
// outcome is only known where the work is done — and the outcome is the half
// that matters: a rule that fires a thousand times and fails every time looks
// identical to a working one in the fire counters `matterbox rules stats` reads.
func (e *Engine) reportRule(t trigger, actionType string, err error) {
	if !telemetry.Enabled() {
		return
	}
	outcome, _ := telemetry.Classify(err)
	telemetry.RuleFired(ruleAction(actionType), outcome, e.ruleTrigger(t))
}

// reportRuleOutcome is reportRule for a runner whose failure isn't an error
// value — a non-2xx webhook response, a template that rendered empty.
func (e *Engine) reportRuleOutcome(t trigger, actionType, outcome string) {
	if !telemetry.Enabled() {
		return
	}
	telemetry.RuleFired(ruleAction(actionType), outcome, e.ruleTrigger(t))
}

// daemonChannels lists the delivery and rule capabilities this daemon is
// configured with, for daemon_started. It is the denominator for everything
// else the daemon reports: "nobody uses two-way replies" means nothing without
// knowing how many daemons have the inbound channel switched on at all.
//
// "desktop" is deliberately absent. The daemon has no desktop delivery of its
// own — a desktop notification is an exec action running a helper — and from
// here that is indistinguishable from any other command, so claiming it would
// be a guess dressed as a fact.
func (e *Engine) daemonChannels() []string {
	var on []string
	add := func(id string, yes bool) {
		if yes {
			on = append(on, id)
		}
	}
	add("telegram", e.tg != nil && e.opts.TelegramChatID != "")
	add("two_way", e.inboundEnabled())
	// /digest and the other bot commands ride on the same inbound channel.
	add("digest", e.inboundEnabled())
	add("summarize", e.opts.Summarize)
	add("exec", rulesUseAction(e.ruleSet(), ActionExec))
	return on
}

// rulesUseAction reports whether any rule carries an action of this type.
func rulesUseAction(rules []Rule, kind string) bool {
	for _, r := range rules {
		for _, a := range r.Actions {
			if strings.EqualFold(a.Type, kind) {
				return true
			}
		}
	}
	return false
}

// ReportStart sends daemon_started once the engine has its rules and its
// delivery wiring. Exported because the command layer owns the version string
// and starts telemetry, and it is the command layer that knows the daemon
// actually got as far as running.
func (e *Engine) ReportStart(version string) {
	if !telemetry.Enabled() {
		return
	}
	telemetry.DaemonStarted(version, len(e.ruleSet()), e.daemonChannels())
}

// reportNotifAction records that a delivered notification was acted on, and
// whether carrying the action out worked. This is the only view we have of the
// two-way bridge: it happens entirely outside the app, on a phone, and a reply
// that fails to post is a message the user believes they sent.
func reportNotifAction(action string, err error) {
	if !telemetry.Enabled() {
		return
	}
	outcome, _ := telemetry.Classify(err)
	telemetry.NotificationActioned("telegram", action, outcome)
}

// reportNotifExpired records a reply or reaction aimed at a notification the
// daemon no longer has context for — a restart dropped the mapping, so the
// action can't be carried out at all. Worth its own outcome: it is not a
// failure of the action, it is the bridge forgetting, and how often that
// happens decides whether the mapping should outlive a restart.
func reportNotifExpired(action string) {
	if !telemetry.Enabled() {
		return
	}
	telemetry.NotificationActioned("telegram", action, "unavailable")
}
