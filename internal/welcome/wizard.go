package welcome

import (
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/auth"
	"matterbox/internal/config"
	"matterbox/internal/opener"
)

// handleKey routes a keypress by phase. ctrl+c always quits.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.phase {
	case phaseIntro:
		// Any key skips the fly-in. Rewind the clock so the scene snaps to its
		// settled end-state (sun risen, title parked) instead of freezing it
		// mid-flight. Guard against a key arriving before the first frame set m.now.
		if !m.now.IsZero() {
			m.start = m.now.Add(-time.Duration(introSecs * float64(time.Second)))
			m.t = introSecs
		}
		m.phase = phaseWizard
		return m, nil
	case phaseDone:
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
		m.step = stepServer
		return m, nil
	case "ctrl+o":
		_ = opener.Open(ssoURL(m.cfg.ServerURL))
		m.authMsg = "Browser opened — approve sign-in, then paste the link or token below."
		m.authErr = false
		return m, nil
	case "enter":
		raw := strings.TrimSpace(m.token.value())
		if raw == "" {
			// Skip auth for now; the user can run `matterbox login` later.
			m.authMsg = ""
			m.authErr = false
			m.step = stepAdvanced
			return m, nil
		}
		tok := extractToken(raw)
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
	editField(&m.token, msg)
	return m, nil
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
	// Persist the server URL now so the saved token is paired with it on disk
	// even if the user quits before finishing the advanced screen.
	_ = config.Save(m.cfg)
	m.authMsg = ""
	m.token.setValue("")
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

// ssoURL builds the GitLab SSO mobile-login URL, mirroring `matterbox login`.
func ssoURL(server string) string {
	return strings.TrimRight(server, "/") +
		"/oauth/gitlab/mobile_login?redirect_to=" + url.QueryEscape("mmauth://callback")
}

// extractToken pulls the session token out of an mmauth://callback?MMAUTHTOKEN=…
// link or accepts a bare token, rejecting anything else (matches login.go).
func extractToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil {
		if t := strings.TrimSpace(u.Query().Get("MMAUTHTOKEN")); t != "" {
			return t
		}
	}
	if strings.ContainsAny(s, " \t/?&#") {
		return ""
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
