package welcome

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/auth"
	"matterbox/internal/config"
)

// ansiSGR matches SGR colour/style escapes so tests can compare rendered text
// without the per-cell colour codes the translucent panel interleaves.
var ansiSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// newWizard builds a sized wizard already past the intro, with config isolated
// to a temp dir so the test never touches the real config.yaml / token.
func newWizard(t *testing.T) *Model {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	m := New(cfg, false)
	m.width, m.height = 100, 40
	m.rend.Resize(100, 40)
	m.phase, m.step = phaseWizard, stepServer
	return m
}

func TestWizardHappyPathSavesConfig(t *testing.T) {
	m := newWizard(t)

	// Step 1: server URL (scheme is defaulted to https://).
	m.server.setValue("mattermost.example.com")
	m.handleKey(key(tea.KeyEnter))
	if m.step != stepAuth {
		t.Fatalf("after server enter: step = %d, want stepAuth", m.step)
	}
	if m.cfg.ServerURL != "https://mattermost.example.com" {
		t.Fatalf("server URL = %q", m.cfg.ServerURL)
	}

	// Step 2: the username field is focused first. Skip sign-in for now by
	// moving to the token paste field and pressing enter with it empty.
	if m.authFocus != authFocusUser {
		t.Fatalf("auth step opened with focus %d, want the username field", m.authFocus)
	}
	m.authFocus = authFocusToken
	m.handleKey(key(tea.KeyEnter)) // empty token -> skip
	if m.step != stepAdvanced {
		t.Fatalf("after empty auth enter: step = %d, want stepAdvanced", m.step)
	}

	// Step 3: focus "SQL tab" (row 1) and toggle it on, then finish.
	m.handleKey(key(tea.KeyDown))  // focus row 1
	m.handleKey(key(tea.KeyRight)) // toggle SQL tab on
	if !m.adv.sqlTab {
		t.Fatal("SQL tab did not toggle on")
	}
	m.handleKey(key(tea.KeyEnter)) // finish
	if m.phase != phaseDone {
		t.Fatalf("after finish: phase = %d, want phaseDone", m.phase)
	}

	// The config was written: reload from disk and verify.
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerURL != "https://mattermost.example.com" {
		t.Errorf("persisted server URL = %q", got.ServerURL)
	}
	if got.SQLTab == nil || !*got.SQLTab {
		t.Errorf("persisted sql_tab = %v, want true", got.SQLTab)
	}
	if got.Mouse == nil || !*got.Mouse {
		t.Errorf("persisted mouse = %v, want true (default kept)", got.Mouse)
	}
}

func TestWizardRequiresServerURL(t *testing.T) {
	m := newWizard(t)
	m.handleKey(key(tea.KeyEnter)) // empty server
	if m.step != stepServer {
		t.Fatalf("empty server advanced to step %d", m.step)
	}
	if m.serverMsg == "" {
		t.Fatal("expected a validation hint for the empty server URL")
	}
}

func TestMarkReadDigitsAndAdjust(t *testing.T) {
	m := newWizard(t)
	m.step = stepAdvanced
	m.adv.focus = 0
	m.adv.markRead = 0
	// Type "12".
	m.handleKey(tea.KeyPressMsg{Code: '1', Text: "1"})
	m.handleKey(tea.KeyPressMsg{Code: '2', Text: "2"})
	if m.adv.markRead != 12 {
		t.Fatalf("markRead = %d, want 12", m.adv.markRead)
	}
	m.handleKey(key(tea.KeyRight)) // +1
	if m.adv.markRead != 13 {
		t.Fatalf("markRead = %d, want 13", m.adv.markRead)
	}
	m.handleKey(key(tea.KeyBackspace)) // 13 -> 1
	if m.adv.markRead != 1 {
		t.Fatalf("markRead = %d, want 1", m.adv.markRead)
	}
}

func TestCodeThemeCyclesAndPersists(t *testing.T) {
	m := newWizard(t)
	m.step = stepAdvanced

	// The cycler seeds from config (monokai by default) and the row sits last in
	// the tab order, so a full focus cycle reaches it.
	if got := m.currentThemeName(); got != "monokai" {
		t.Fatalf("seeded theme = %q, want monokai", got)
	}
	m.adv.focus = 5

	start := m.adv.codeThemeIdx
	m.handleKey(key(tea.KeyRight))
	if m.adv.codeThemeIdx == start {
		t.Fatal("right should advance the theme index")
	}
	picked := m.currentThemeName()
	m.handleKey(key(tea.KeyLeft))
	if m.adv.codeThemeIdx != start {
		t.Fatal("left should step back to the previous theme")
	}

	// Re-pick and finish: applyAdvanced writes the choice into the config.
	m.handleKey(key(tea.KeyRight))
	m.handleKey(key(tea.KeyEnter))
	if m.cfg.CodeTheme != picked {
		t.Fatalf("saved code_theme = %q, want %q", m.cfg.CodeTheme, picked)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CodeTheme != picked {
		t.Fatalf("reloaded code_theme = %q, want %q", reloaded.CodeTheme, picked)
	}
}

func TestAdvancedRendersThemePreview(t *testing.T) {
	m := newWizard(t)
	m.t = 7 // settled scene
	m.step = stepAdvanced
	text := ansiSGR.ReplaceAllString(m.View().Content, "")
	for _, want := range []string{"Code theme", "Preview", "func greet"} {
		if !strings.Contains(text, want) {
			t.Fatalf("advanced step missing %q in render", want)
		}
	}
}

func TestAuthFocusNavigation(t *testing.T) {
	m := newWizard(t)
	m.step = stepAuth
	m.authFocus = authFocusUser

	// Tab order: user → password → SSO → token (the MFA field is hidden until
	// the server asks for a code, so it's outside the cycle here).
	m.handleKey(key(tea.KeyUp)) // wraps back to the last visible control
	if m.authFocus != authFocusToken {
		t.Fatalf("up from username: focus = %d, want token (wrap-around)", m.authFocus)
	}
	m.handleKey(key(tea.KeyDown)) // wraps forward to the first
	if m.authFocus != authFocusUser {
		t.Fatalf("down from token: focus = %d, want username (wrap-around)", m.authFocus)
	}

	// Without an MFA prompt, advancing past the token field skips the hidden
	// MFA control and wraps to the username field.
	m.authFocus = authFocusToken
	m.handleKey(key(tea.KeyDown))
	if m.authFocus != authFocusUser {
		t.Fatalf("down from token (no MFA): focus = %d, want username, not the hidden MFA field", m.authFocus)
	}

	// Once the server requires MFA, the field joins the cycle as the last control.
	m.mfaRequired = true
	m.authFocus = authFocusToken
	m.handleKey(key(tea.KeyDown))
	if m.authFocus != authFocusMFA {
		t.Fatalf("down from token (MFA required): focus = %d, want the MFA field", m.authFocus)
	}
}

func TestPasswordSubmitRequiresBothFields(t *testing.T) {
	m := newWizard(t)
	m.step = stepAuth
	m.authFocus = authFocusPassword

	// Username present, password empty → error, nothing in flight.
	m.user.setValue("alice")
	if _, cmd := m.submitPassword(); cmd != nil {
		t.Fatal("expected no login command with an empty password")
	}
	if !m.authErr || m.authMsg == "" {
		t.Fatal("expected an error message for the empty password")
	}
	if m.validating {
		t.Fatal("should not be validating with an empty password")
	}

	// Both present → a login kicks off.
	m.password.setValue("hunter2")
	_, cmd := m.submitPassword()
	if cmd == nil {
		t.Fatal("expected a login command once username + password are set")
	}
	if !m.validating {
		t.Fatal("expected validating=true once the login is in flight")
	}
}

func TestPasswordResultRevealsMFAThenSucceeds(t *testing.T) {
	m := newWizard(t)
	m.step = stepAuth
	m.validating = true

	// An MFA-required result reveals the two-factor field and focuses it.
	m.handlePasswordResult(passwordResultMsg{mfaRequired: true, err: errors.New("mfa")})
	if !m.mfaRequired {
		t.Fatal("expected mfaRequired to be set")
	}
	if m.authFocus != authFocusMFA {
		t.Fatalf("focus = %d, want the MFA field after an MFA-required result", m.authFocus)
	}
	if m.validating {
		t.Fatal("validating should clear once a result lands")
	}

	// A success saves the token, clears the password, and advances.
	m.validating = true
	m.password.setValue("hunter2")
	m.handlePasswordResult(passwordResultMsg{token: "tok-xyz", user: "alice"})
	if m.step != stepAdvanced {
		t.Fatalf("step = %d, want stepAdvanced after a successful sign-in", m.step)
	}
	if !m.authOK || m.authUser != "alice" {
		t.Fatalf("authOK=%v authUser=%q, want true/alice", m.authOK, m.authUser)
	}
	if m.password.value() != "" {
		t.Fatal("password should be cleared after a successful sign-in")
	}
	if !auth.HasToken() {
		t.Fatal("expected a saved token after a successful sign-in")
	}
}

func TestAuthStepRendersButtonAndUnderlinedLink(t *testing.T) {
	m := newWizard(t)
	m.t = 7 // settled scene
	m.step = stepAuth
	out := m.View().Content
	// Strip SGR escapes for the text check: the translucent panel blends the
	// animated scene per cell, so background escapes are interleaved between
	// letters and the raw string isn't contiguous.
	text := ansiSGR.ReplaceAllString(out, "")
	if !strings.Contains(text, "Open GitLab SSO in your browser") {
		t.Fatal("auth step missing the SSO browser button")
	}
	if !strings.Contains(text, "please click the link") {
		t.Fatal("auth step missing the success-page snippet")
	}
	// The only underline in this step is the "link" word in the snippet.
	if !strings.Contains(out, "\x1b[4m") {
		t.Fatal("auth step did not underline the link word in the snippet")
	}
}

func TestAuthAutoCaptureFillsAndValidates(t *testing.T) {
	m := newWizard(t)
	m.step = stepAuth

	// A captured mmauth:// link behaves like a paste: the token fills and a
	// validation kicks off (we don't run the returned network Cmd here).
	_, cmd := m.handleCapturedURL("mmauth://callback?MMAUTHTOKEN=tok999&MMCSRF=z")
	if m.pendingToken != "tok999" {
		t.Fatalf("pendingToken = %q, want tok999", m.pendingToken)
	}
	if !m.validating {
		t.Fatal("expected a validation to be in flight after capture")
	}
	if cmd == nil {
		t.Fatal("expected a validate command from the captured link")
	}

	// A capture that arrives once we've left the auth step is ignored.
	m2 := newWizard(t)
	m2.step = stepAdvanced
	if _, cmd := m2.handleCapturedURL("mmauth://callback?MMAUTHTOKEN=late"); cmd != nil {
		t.Fatal("captured URL off the auth step should be ignored")
	}
	if m2.pendingToken != "" {
		t.Fatalf("pendingToken = %q, want empty (ignored)", m2.pendingToken)
	}
}

func TestAuthStepShowsCredentialsAndMasksPassword(t *testing.T) {
	m := newWizard(t)
	m.t = 7 // settled scene
	m.step = stepAuth
	m.user.setValue("alice")
	m.password.setValue("hunter2")
	// Strip the per-cell SGR escapes the translucent panel interleaves so the
	// rendered glyphs read as a contiguous string.
	text := ansiSGR.ReplaceAllString(m.View().Content, "")
	if !strings.Contains(text, "Username or email") || !strings.Contains(text, "Password") {
		t.Fatal("auth step missing the username/password field labels")
	}
	if !strings.Contains(text, "alice") {
		t.Fatal("username should render in clear text")
	}
	if strings.Contains(text, "hunter2") {
		t.Fatal("password must not render in clear text")
	}
	if !strings.Contains(text, "•") {
		t.Fatal("masked password should render as bullets")
	}
}

func TestViewRendersAcrossPhases(t *testing.T) {
	m := newWizard(t)
	m.t = 7 // settled scene
	for _, ph := range []phase{phaseIntro, phaseWizard, phaseDone, phaseHidden} {
		m.phase = ph
		_ = m.View() // must not panic at any phase
	}
}

func TestDemoDoneSpaceHidesWithoutQuitting(t *testing.T) {
	m := newWizard(t)
	m.demo = true
	m.phase = phaseDone

	// Space on the closing screen dismisses the panel but does NOT quit: the
	// program keeps running so the demo animation/soundtrack plays on.
	if _, cmd := m.handleKey(key(tea.KeySpace)); cmd != nil {
		t.Fatalf("space on the done screen should not return a command (got %T), so the program keeps running", cmd)
	}
	if m.phase != phaseHidden {
		t.Fatalf("phase = %d, want phaseHidden after space", m.phase)
	}

	// Once hidden, keys keep the demo running rather than closing it (only ctrl+c
	// quits), and the bare scene still renders without panicking.
	if _, cmd := m.handleKey(key(tea.KeyEnter)); cmd != nil {
		t.Fatal("keys after hiding should keep the demo running, not quit")
	}
	m.t = 7
	_ = m.View()
}

func TestDoneSpaceQuitsOutsideDemo(t *testing.T) {
	m := newWizard(t)
	m.phase = phaseDone // not demo mode

	// Without --demo, space is just another exit key on the closing screen.
	if _, cmd := m.handleKey(key(tea.KeySpace)); cmd == nil {
		t.Fatal("space on the done screen should quit outside demo mode")
	}
	if m.phase != phaseDone {
		t.Fatalf("phase changed to %d outside demo mode; want it to quit, not hide", m.phase)
	}
}

func TestNormalizeServer(t *testing.T) {
	cases := map[string]string{
		"  https://mm.test/ ":   "https://mm.test",
		"mm.test":               "https://mm.test",
		"http://localhost:8065": "http://localhost:8065",
		"":                      "",
	}
	for in, want := range cases {
		if got := normalizeServer(in); got != want {
			t.Errorf("normalizeServer(%q) = %q, want %q", in, got, want)
		}
	}
	// Token extraction lives in internal/mmauth now (tested there).
}
