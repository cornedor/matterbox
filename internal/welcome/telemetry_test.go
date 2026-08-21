package welcome

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/telemetry"
)

// The wizard's own funnel is the only telemetry collected before the telemetry
// question is answered, which makes two things worth pinning: it must be held
// rather than sent, and it must actually describe the walk.

// TestFunnelIsHeldUntilTheAnswer: nothing may reach the client while the wizard
// runs, and declining must throw the whole buffer away.
func TestFunnelIsHeldUntilTheAnswer(t *testing.T) {
	t.Cleanup(telemetry.DropPending)
	m := newWizard(t)

	m.recordStep(stepServer)
	m.handleKey(key(tea.KeyEnter)) // no URL yet: the step is shown again
	m.server.setValue("chat.example.com")
	m.handleKey(key(tea.KeyEnter)) // → login

	if telemetry.Enabled() {
		t.Fatal("the wizard opened a telemetry client before asking")
	}
	if m.funnel.shown[stepServer] < 1 || m.funnel.shown[stepAuth] != 1 {
		t.Errorf("funnel = %v, want the server step and one login step", m.funnel.shown)
	}
	if m.funnel.last != stepAuth {
		t.Errorf("last step = %d, want stepAuth", m.funnel.last)
	}

	// Decline: the buffer goes, and no client opens.
	m.step = stepTelemetry
	m.telemetryFocus = telemetryFocusNo
	m.handleKey(key(tea.KeyEnter))
	if telemetry.Enabled() {
		t.Error("declining opened a telemetry client")
	}
}

// TestFunnelCountsRepeatedSteps: a step shown three times is a step being fought
// with, and that is the whole reason `attempt` exists. Going back counts too —
// esc from the login screen is as much a funnel event as arriving at it.
func TestFunnelCountsRepeatedSteps(t *testing.T) {
	t.Cleanup(telemetry.DropPending)
	m := newWizard(t)
	m.recordStep(stepServer)

	for range 3 {
		m.server.setValue("chat.example.com")
		m.handleKey(key(tea.KeyEnter)) // → login
		m.handleKey(key(tea.KeyEsc))   // ← back to server
	}
	if got := m.funnel.shown[stepAuth]; got != 3 {
		t.Errorf("login shown %d times, want 3", got)
	}
	if got := m.funnel.shown[stepServer]; got != 4 {
		t.Errorf("server shown %d times, want 4 (the first plus three returns)", got)
	}
}

// TestAuthMethodNamesTheRoute: setup_finished's auth_method separates "our SSO
// flow is broken" from "they typed the wrong password", so a completed wizard
// has to name the route the token actually came from — and one that never signed
// in has to say so rather than guessing.
func TestAuthMethodNamesTheRoute(t *testing.T) {
	cases := []struct {
		name                    string
		authOK, usedSSO, usedPw bool
		want                    string
	}{
		{"never signed in", false, false, false, "none"},
		{"never signed in, tried SSO", false, true, false, "none"},
		{"pasted a token", true, false, false, "token"},
		{"through SSO", true, true, false, "oauth"},
		{"username and password", true, false, true, "password"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &Model{authOK: c.authOK, usedSSO: c.usedSSO, usedPassword: c.usedPw}
			if got := m.authMethod(); got != c.want {
				t.Errorf("authMethod() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestStepNamesMatchTheCatalogue: a step label the catalogue doesn't declare is
// dropped in production, which silently loses a funnel stage. An unknown step
// must report nothing rather than being filed under the wrong label.
func TestStepNamesMatchTheCatalogue(t *testing.T) {
	spec, ok := telemetry.Spec("setup_step")
	if !ok {
		t.Fatal("setup_step is not catalogued")
	}
	var allowed []string
	for _, p := range spec.Props {
		if p.Name == "step" {
			allowed = p.Values
		}
	}
	if len(allowed) == 0 {
		t.Fatal("setup_step has no step property")
	}
	for _, step := range []int{stepServer, stepAuth, stepAdvanced, stepTelemetry} {
		name := stepName(step)
		if name == "" {
			t.Errorf("step %d has no catalogue label", step)
			continue
		}
		found := false
		for _, v := range allowed {
			if v == name {
				found = true
			}
		}
		if !found {
			t.Errorf("step %d reports %q, which the catalogue doesn't allow (%v)", step, name, allowed)
		}
	}
	if got := stepName(99); got != "" {
		t.Errorf("stepName(unknown) = %q, want an empty label rather than a wrong one", got)
	}
}

// TestLoginFailureIsRecordedAndHeld: login failures are the most likely reason a
// fresh install is abandoned and are invisible today. They must be recorded, and
// — like everything else in the wizard — not sent before the answer.
func TestLoginFailureIsRecordedAndHeld(t *testing.T) {
	t.Cleanup(telemetry.DropPending)
	m := newWizard(t)
	m.step = stepAuth
	m.usedPassword = true

	m.handlePasswordResult(passwordResultMsg{err: errors.New("401 Unauthorized")})
	if !m.authErr {
		t.Error("a rejected sign-in didn't surface in the UI")
	}
	if m.funnel.started.IsZero() {
		t.Error("a login failure before any step didn't arm the funnel")
	}
	if telemetry.Enabled() {
		t.Error("a login failure opened a telemetry client before the question was asked")
	}
}
