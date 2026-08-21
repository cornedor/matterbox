package welcome

import (
	"time"

	"matterbox/internal/telemetry"
)

// The activation funnel.
//
// A fresh install that fails in here never becomes a user, and today that is
// completely invisible: the wizard writes a config or it doesn't, and nobody
// finds out which step lost the person. setup_step turns it into a funnel — one
// event per step shown, with how many times this run has shown it — and
// setup_finished closes it.
//
// The catch is consent, which is the wizard's *last* question. Everything before
// it happens with the answer unknown, so nothing may be sent while it runs. The
// events are therefore held in memory (telemetry.BeginPending) and only reach
// the network once the answer is yes: recordConsent replays them, a "no"
// discards them, and a wizard abandoned before the question exits with the
// buffer unsent. So the funnel is only ever visible for people who agreed to be
// counted — which does bias it, and is the only version of it worth having.
//
// One consequence worth stating plainly: an abandonment *before* the telemetry
// step can never be reported, because nobody has been asked yet. What
// setup_finished distinguishes is a wizard that reached a working login from one
// that reached the end without one, which is the part we can honestly observe.

// stepNames maps the wizard's step constants to the catalogue's labels. Written
// out rather than derived so renaming a constant can't silently re-label a
// funnel stage; "login" rather than "auth" because that is what the step is
// called everywhere the user can see it.
var stepNames = map[int]string{
	stepServer:    "server",
	stepAuth:      "login",
	stepAdvanced:  "advanced",
	stepTelemetry: "telemetry",
}

// stepName is stepNames with a lookup that fails empty: a step the catalogue
// doesn't know reports nothing rather than being filed under the wrong label,
// and the property is simply absent from the event. Mislabelling a funnel stage
// is worse than a gap in it — a gap is visible, a mislabel isn't.
func stepName(step int) string { return stepNames[step] }

// wizardFunnel tracks the funnel across one run of the wizard: when it started,
// how many times each step has been shown, and which step it got furthest to.
type wizardFunnel struct {
	started time.Time
	shown   map[int]int
	// last is the step the wizard is on, so setup_finished can name where it
	// ended even when the end is the telemetry answer rather than a step.
	last int
}

// beginFunnel arms the funnel and starts holding events. Called when the intro
// hands over to the form, which is the first moment the wizard is a wizard
// rather than an animation.
func (m *Model) beginFunnel() {
	if m.funnel.started.IsZero() {
		m.funnel.started = time.Now()
		telemetry.BeginPending()
	}
}

// gotoStep moves to a wizard step and records the move. Every assignment to
// m.step goes through here: a step reached by going *back* (esc from advanced
// to login) is as much a funnel event as one reached by going forward, and the
// attempt count is what makes the difference legible — a step shown three times
// is a step being fought with.
func (m *Model) gotoStep(step int) {
	m.step = step
	m.recordStep(step)
}

// recordStep emits setup_step for a step becoming visible.
func (m *Model) recordStep(step int) {
	m.beginFunnel()
	if m.funnel.shown == nil {
		m.funnel.shown = make(map[int]int, 4)
	}
	m.funnel.shown[step]++
	m.funnel.last = step
	telemetry.SetupStep(stepName(step), m.funnel.shown[step])
}

// recordConsent answers the telemetry question. This is the hinge: on yes it
// opens the client and replays the whole held funnel (including this event); on
// no it throws the buffer away and nothing is ever sent.
//
// setup_finished's outcome is whether a working login came out of the wizard,
// not whether the form was completed — someone who skips the sign-in and presses
// through to the end has a config and no way in, which is an activation failure
// even though every screen was answered.
func (m *Model) recordConsent(consent bool) {
	if !consent {
		telemetry.DropPending()
		return
	}
	outcome := "abandoned"
	if m.authOK {
		outcome = "completed"
	}
	telemetry.SetupFinished(outcome, stepName(m.funnel.last), m.authMethod(),
		time.Since(m.funnel.started), true)
	// Replay: the config now records the consent this reads, so the held events
	// go through the same check every live one does.
	telemetry.ReleasePending(m.cfg, telemetry.ModeCLI)
}

// authMethod names how the login was obtained, for setup_finished. "oauth"
// covers the GitLab SSO round trip whether the token came back through the
// scheme handler or was pasted by hand — both are the same flow, and the
// difference between them is the capture working, not the method.
func (m *Model) authMethod() string {
	switch {
	case !m.authOK:
		return "none"
	case m.usedPassword:
		return "password"
	case m.usedSSO:
		return "oauth"
	default:
		return "token"
	}
}

// recordLoginFailure reports a rejected sign-in. Login failures are the most
// likely reason a new install is abandoned and are invisible to us today; the
// class separates "our SSO flow is broken" from "they typed the wrong
// password". Held like everything else in here until consent is given.
func (m *Model) recordLoginFailure(method string, err error, mfa bool) {
	m.beginFunnel()
	telemetry.LoginFailed(method, telemetry.ClassifyError(err), mfa)
}

// tokenMethod names the route a token-shaped sign-in took, for login_failed:
// the SSO round trip when the browser was opened from here, a hand-pasted
// token otherwise. The two fail for different reasons — a broken redirect
// versus a stale copy-paste — and the distinction is the point of the property.
func (m *Model) tokenMethod() string {
	if m.usedSSO {
		return "oauth"
	}
	return "token"
}
