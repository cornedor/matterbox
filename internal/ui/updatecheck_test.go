package ui

import (
	"strings"
	"testing"

	"matterbox/internal/update"
)

func rel(version string) *update.Release {
	return &update.Release{Version: version, URL: "https://example.invalid/" + version}
}

// The notice waits for a moment rather than taking one. Each of these is a
// moment it must not take.
func TestFlushUpdateNoticeWaitsItsTurn(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m *Model)
	}{
		{
			name:  "nothing found",
			setup: func(m *Model) { m.updateFound = nil },
		},
		{
			name: "the startup splash is still up",
			setup: func(m *Model) {
				m.updateFound = rel("v9.9.9")
				m.splash.active = true
			},
		},
		{
			// "downloading 3 files…" is something the user asked for and is
			// waiting on. Stepping on it to advertise a release is exactly the
			// nagging this is trying not to be.
			name: "the status slot is busy",
			setup: func(m *Model) {
				m.updateFound = rel("v9.9.9")
				m.status = "downloading 3 files…"
			},
		},
		{
			name: "it has already been said this session",
			setup: func(m *Model) {
				m.updateFound = rel("v9.9.9")
				m.updateNoticed = true
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m Model
			c.setup(&m)
			before := m.status

			if cmd := m.flushUpdateNotice(); cmd != nil {
				t.Error("flushUpdateNotice returned a command, want it to hold off")
			}
			if m.status != before {
				t.Errorf("status = %q, want it left as %q", m.status, before)
			}
		})
	}
}

func TestFlushUpdateNoticeShowsItOnceTheSplashIsDown(t *testing.T) {
	var m Model
	m.updateFound = rel("v9.9.9")

	cmd := m.flushUpdateNotice()
	if cmd == nil {
		t.Fatal("flushUpdateNotice returned nothing, want the toast")
	}
	if !strings.Contains(m.status, "v9.9.9") {
		t.Errorf("status = %q, want it to name the release", m.status)
	}
	if !strings.Contains(m.status, "matterbox upgrade") {
		t.Errorf("status = %q, want it to name the command", m.status)
	}

	// Said once. A second event must not re-flash it, or a release nobody
	// installs becomes a toast on every keystroke for the rest of the session.
	m.status = ""
	if cmd := m.flushUpdateNotice(); cmd != nil {
		t.Error("flushUpdateNotice fired twice in one session")
	}
	if m.status != "" {
		t.Errorf("status = %q on the second call, want it left alone", m.status)
	}
}

// The check is a config switch and an unstamped build away from running, and
// neither should cost a goroutine or a request.
func TestCheckUpdateCmdIsNilWhenItCannotHelp(t *testing.T) {
	t.Cleanup(func() { SetBuildInfo("", "") })

	SetBuildInfo("v1.0.0", "")
	var off Model
	off.updateCheck = false
	if cmd := off.checkUpdateCmd(); cmd != nil {
		t.Error("checkUpdateCmd ran with update_check off")
	}

	SetBuildInfo("", "")
	var unstamped Model
	unstamped.updateCheck = true
	if cmd := unstamped.checkUpdateCmd(); cmd != nil {
		t.Error("checkUpdateCmd ran for a build with no version to compare")
	}
}
