package welcome

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/auth"
	"matterbox/internal/config"
	"matterbox/internal/mmauth"
	"matterbox/internal/opener"
)

// handleKey routes a keypress by phase. ctrl+c always quits.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		m.closeCapture()
		return m, tea.Quit
	}
	switch m.phase {
	case phaseIntro:
		// Any key skips the fly-in. Rewind the clock so the scene snaps to its
		// settled end-state (sun risen, title parked) instead of freezing it
		// mid-flight. introEnd accounts for the demo's delayed keyframes. Guard
		// against a key arriving before the first frame set m.now.
		if !m.now.IsZero() {
			end := m.introEnd()
			m.start = m.now.Add(-time.Duration(end * float64(time.Second)))
			m.t = end
		}
		m.phase = phaseWizard
		return m, nil
	case phaseDone:
		m.closeCapture()
		return m, tea.Quit
	case phaseWizard:
		switch m.step {
		case stepServer:
			return m.handleServerKey(msg)
		case stepAuth:
			return m.handleAuthKey(msg)
		case stepAdvanced:
			return m.handleAdvancedKey(msg)
		}
	}
	return m, nil
}

func (m *Model) handleServerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeCapture()
		return m, tea.Quit
	case "enter":
		s := normalizeServer(m.server.value())
		if s == "" {
			m.serverMsg = "Enter your Mattermost server URL to continue."
			return m, nil
		}
		m.server.setValue(s)
		m.cfg.ServerURL = s
		m.serverMsg = ""
		m.authMsg = ""
		m.authFocus = authFocusUser // open on the username field each time
		m.step = stepAuth
		return m, nil
	}
	editField(&m.server, msg)
	return m, nil
}

func (m *Model) handleAuthKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.validating {
		return m, nil // ignore input while a check is in flight
	}
	switch msg.String() {
	case "esc":
		// Stop listening; a changed server would make a captured token stale, and
		// re-entering the step restarts capture on the next browser-open.
		m.closeCapture()
		m.step = stepServer
		return m, nil
	case "up", "shift+tab":
		n := m.authControls()
		m.authFocus = (m.authFocus - 1 + n) % n
		return m, nil
	case "down", "tab":
		n := m.authControls()
		m.authFocus = (m.authFocus + 1) % n
		return m, nil
	case "enter":
		switch m.authFocus {
		case authFocusSSO:
			return m, m.openSSO()
		case authFocusToken:
			return m.submitToken()
		case authFocusUser:
			// Move on to the password rather than submitting half-filled creds.
			m.authFocus = authFocusPassword
			return m, nil
		default: // password or MFA field
			return m.submitPassword()
		}
	}
	// The focused text field takes typed input; the SSO button ignores it.
	if f := m.activeField(); f != nil {
		editField(f, msg)
	}
	return m, nil
}

// authControls is the number of focusable controls on the auth step. The MFA
// field (the last control) only joins the tab order once the server has asked
// for a two-factor code, so navigation skips it in the common case.
func (m *Model) authControls() int {
	if m.mfaRequired {
		return authFieldCount
	}
	return authFieldCount - 1
}

// mmauthURLMsg carries the mmauth:// callback link captured by the OS scheme
// handler after SSO, so the token can fill and validate itself.
type mmauthURLMsg string

// openSSO launches the GitLab SSO login in the browser and moves focus to the
// paste field. On Linux it also starts the scheme-handler capture so an
// approved sign-in flows straight back — no copy-paste — returning a Cmd that
// waits for the captured link; the field stays as a fallback.
func (m *Model) openSSO() tea.Cmd {
	_ = opener.Open(mmauth.LoginURL(m.cfg.ServerURL))
	m.authErr = false
	m.authFocus = authFocusToken

	if !m.capturing {
		if c, ok := mmauth.StartCapture(context.Background()); ok {
			m.cap = c
			m.capturing = true
			m.authMsg = "Browser opened — approve sign-in and you're in automatically (or paste the link below)."
			return waitForURL(c)
		}
	}
	m.authMsg = "Browser opened — approve sign-in, then paste the link below."
	return nil
}

// waitForURL blocks on the capture channel and reports the link as a message.
// The channel is closed by Capture.Close, so this never leaks once the wizard
// stops listening (it returns nil on a closed channel).
func waitForURL(c mmauth.Capture) tea.Cmd {
	if c.URL == nil {
		return nil
	}
	return func() tea.Msg {
		if u, ok := <-c.URL; ok {
			return mmauthURLMsg(u)
		}
		return nil
	}
}

// handleCapturedURL feeds an auto-captured mmauth:// link into the token field
// and validates it, exactly as if the user had pasted it — unless we've moved
// past the auth step or a check is already running.
func (m *Model) handleCapturedURL(msg mmauthURLMsg) (tea.Model, tea.Cmd) {
	if m.phase != phaseWizard || m.step != stepAuth || m.authOK || m.validating {
		return m, nil
	}
	m.token.setValue(string(msg))
	m.authFocus = authFocusToken
	return m.submitToken()
}

// closeCapture tears down the scheme-handler capture if it's running. Idempotent
// (Close is set to nil after), so it's safe on every exit path.
func (m *Model) closeCapture() {
	if m.cap.Close != nil {
		m.cap.Close()
		m.cap.Close = nil
	}
	m.capturing = false
}

// submitToken validates the pasted/captured token-link, or skips auth when it's
// empty (the user can run `matterbox login` later). Mirrors the original flow.
func (m *Model) submitToken() (tea.Model, tea.Cmd) {
	raw := strings.TrimSpace(m.token.value())
	if raw == "" {
		m.authMsg = ""
		m.authErr = false
		m.step = stepAdvanced
		return m, nil
	}
	tok := mmauth.ExtractToken(raw)
	if tok == "" {
		m.authMsg = "That isn't a token or mmauth:// link — paste the link from the success page."
		m.authErr = true
		return m, nil
	}
	m.pendingToken = tok
	m.validating = true
	m.authErr = false
	m.authMsg = "Checking your token…"
	return m, validateCmd(m.cfg.ServerURL, tok)
}

// submitPassword signs in with the typed username + password (plus the
// two-factor code once the server has asked for one). It checks the fields are
// filled, then kicks off the network login — mirroring submitToken's
// validating/authErr dance.
func (m *Model) submitPassword() (tea.Model, tea.Cmd) {
	user := strings.TrimSpace(m.user.value())
	pass := m.password.value()
	if user == "" || pass == "" {
		m.authMsg = "Enter your username and password — or use SSO below."
		m.authErr = true
		return m, nil
	}
	mfa := strings.TrimSpace(m.mfa.value())
	if m.mfaRequired && mfa == "" {
		m.authMsg = "Enter the two-factor code from your authenticator app."
		m.authErr = true
		return m, nil
	}
	m.validating = true
	m.authErr = false
	m.authMsg = "Signing in…"
	return m, passwordLoginCmd(m.cfg.ServerURL, user, pass, mfa)
}

func (m *Model) handleAuthResult(msg authResultMsg) (tea.Model, tea.Cmd) {
	m.validating = false
	if msg.err != nil {
		m.authMsg = "Sign-in failed: " + oneLine(msg.err.Error())
		m.authErr = true
		return m, nil
	}
	if err := auth.SaveToken(m.pendingToken); err != nil {
		m.authMsg = "Validated, but couldn't save the token: " + oneLine(err.Error())
		m.authErr = true
		return m, nil
	}
	m.authOK = true
	m.authUser = msg.user
	m.authErr = false
	m.closeCapture() // got the token — stop listening on the socket
	// Persist the server URL now so the saved token is paired with it on disk
	// even if the user quits before finishing the advanced screen.
	_ = config.Save(m.cfg)
	m.authMsg = ""
	m.token.setValue("")
	m.step = stepAdvanced
	return m, nil
}

// handlePasswordResult applies the outcome of a username/password sign-in: an
// MFA-required signal reveals the two-factor field and focuses it; success saves
// the token (and server URL) and advances exactly like the token flow; an error
// is shown for the user to retry.
func (m *Model) handlePasswordResult(msg passwordResultMsg) (tea.Model, tea.Cmd) {
	m.validating = false
	if msg.mfaRequired {
		m.mfaRequired = true
		m.authFocus = authFocusMFA
		m.authMsg = "Enter your two-factor code to finish signing in."
		m.authErr = false
		return m, nil
	}
	if msg.err != nil {
		m.authMsg = "Sign-in failed: " + oneLine(msg.err.Error())
		m.authErr = true
		return m, nil
	}
	if err := auth.SaveToken(msg.token); err != nil {
		m.authMsg = "Signed in, but couldn't save the token: " + oneLine(err.Error())
		m.authErr = true
		return m, nil
	}
	m.authOK = true
	m.authUser = msg.user
	m.authErr = false
	m.closeCapture() // signed in — stop listening on the SSO socket
	_ = config.Save(m.cfg)
	m.authMsg = ""
	m.password.setValue("")
	m.mfa.setValue("")
	m.step = stepAdvanced
	return m, nil
}

func (m *Model) handleAdvancedKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.step = stepAuth
		return m, nil
	case "up", "shift+tab":
		m.adv.focus = (m.adv.focus - 1 + advFieldCount) % advFieldCount
		return m, nil
	case "down", "tab":
		m.adv.focus = (m.adv.focus + 1) % advFieldCount
		return m, nil
	case "left":
		m.adjustAdvanced(-1)
		return m, nil
	case "right", " ", "space":
		m.adjustAdvanced(1)
		return m, nil
	case "backspace":
		if m.adv.focus == 0 {
			m.adv.markRead = clampDelay(m.adv.markRead / 10)
		}
		return m, nil
	case "enter":
		m.applyAdvanced()
		if err := config.Save(m.cfg); err != nil {
			m.authMsg = "Couldn't save config: " + oneLine(err.Error())
			return m, nil
		}
		m.closeCapture() // setup's done; don't leave the socket listening
		m.phase = phaseDone
		return m, nil
	}
	// Type digits to set the mark-read delay directly when it's focused.
	if m.adv.focus == 0 && len(msg.Text) == 1 && msg.Text[0] >= '0' && msg.Text[0] <= '9' {
		m.adv.markRead = clampDelay(m.adv.markRead*10 + int(msg.Text[0]-'0'))
	}
	return m, nil
}

// adjustAdvanced applies a left/right (or space) change to the focused field:
// ±1 for the numeric delay, a toggle for the booleans.
func (m *Model) adjustAdvanced(delta int) {
	switch m.adv.focus {
	case 0:
		m.adv.markRead = clampDelay(m.adv.markRead + delta)
	case 1:
		m.adv.sqlTab = !m.adv.sqlTab
	case 2:
		m.adv.mouse = !m.adv.mouse
	case 3:
		m.adv.animations = !m.adv.animations
	case 4:
		m.adv.ctrlArrow = !m.adv.ctrlArrow
	}
}

// applyAdvanced copies the wizard's choices back into the config struct.
func (m *Model) applyAdvanced() {
	d := m.adv.markRead
	m.cfg.MarkReadDelaySeconds = &d
	sql := m.adv.sqlTab
	m.cfg.SQLTab = &sql
	mouse := m.adv.mouse
	m.cfg.Mouse = &mouse
	anim := m.adv.animations
	m.cfg.Animations.CustomEmoji = &anim
	ip := m.adv.animations
	m.cfg.Animations.ImagePreview = &ip
	if m.adv.ctrlArrow {
		m.cfg.Keybindings.NavModifier = "ctrl"
	} else {
		m.cfg.Keybindings.NavModifier = "none"
	}
	m.cfg.ServerURL = normalizeServer(m.server.value())
}

// editField applies the common single-line editing keys to a text field.
func editField(f *textField, msg tea.KeyPressMsg) {
	switch msg.String() {
	case "left":
		f.left()
	case "right":
		f.right()
	case "home", "ctrl+a":
		f.home()
	case "end", "ctrl+e":
		f.end()
	case "backspace":
		f.backspace()
	case "delete":
		f.deleteForward()
	case "ctrl+u":
		f.setValue("")
	default:
		if msg.Text != "" {
			f.insert(msg.Text)
		}
	}
}

// normalizeServer trims a server URL and defaults the scheme to https://.
func normalizeServer(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "/")
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	return s
}

// navModEnabled reports whether a nav_modifier value keeps arrow navigation on.
func navModEnabled(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "off", "false", "disabled", "no":
		return false
	}
	return true
}

func clampDelay(n int) int {
	if n < 0 {
		return 0
	}
	if n > 600 {
		return 600
	}
	return n
}

// oneLine flattens and trims a (possibly multi-line) error for the status row.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 88 {
		return string(r[:87]) + "…"
	}
	return s
}
