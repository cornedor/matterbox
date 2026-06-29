package welcome

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/config"
)

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
	m := New(cfg)
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

	// Step 2: skip auth (empty token).
	m.handleKey(key(tea.KeyEnter))
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

func TestViewRendersAcrossPhases(t *testing.T) {
	m := newWizard(t)
	m.t = 7 // settled scene
	for _, ph := range []phase{phaseIntro, phaseWizard, phaseDone} {
		m.phase = ph
		_ = m.View() // must not panic at any phase
	}
}

func TestNormalizeAndExtract(t *testing.T) {
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
	if got := extractToken("mmauth://callback?MMAUTHTOKEN=abc123"); got != "abc123" {
		t.Errorf("extractToken(link) = %q, want abc123", got)
	}
	if got := extractToken("rawtoken123"); got != "rawtoken123" {
		t.Errorf("extractToken(raw) = %q", got)
	}
	if got := extractToken("https://no/token/here"); got != "" {
		t.Errorf("extractToken(non-token URL) = %q, want empty", got)
	}
}
