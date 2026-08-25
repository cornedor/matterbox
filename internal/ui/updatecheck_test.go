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
			// The channel switcher is something the user asked for and is looking
			// at. Stamping a release announcement across it is exactly the nagging
			// this is trying not to be.
			name: "a popup owns the body",
			setup: func(m *Model) {
				m.updateFound = rel("v9.9.9")
				m.switcherMode = true
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

			if cmd := m.flushUpdateNotice(); cmd != nil {
				t.Error("flushUpdateNotice returned a command, want it to hold off")
			}
			if m.toast.active() {
				t.Errorf("toast = %q, want nothing up yet", m.toast.title)
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
	if !strings.Contains(m.toast.title, "v9.9.9") {
		t.Errorf("toast title = %q, want it to name the release", m.toast.title)
	}
	if !strings.Contains(m.toast.body, "matterbox upgrade") {
		t.Errorf("toast body = %q, want it to name the command", m.toast.body)
	}
	// The footer slot is a mode line and no longer carries this — that is the
	// whole point of the overlay.
	if m.status != "" {
		t.Errorf("status = %q, want the notice to stay off the footer", m.status)
	}

	// Said once. A second event must not re-raise it, or a release nobody
	// installs becomes a box on screen for the rest of the session.
	m.toast = toastState{}
	if cmd := m.flushUpdateNotice(); cmd != nil {
		t.Error("flushUpdateNotice fired twice in one session")
	}
	if m.toast.active() {
		t.Errorf("toast = %q on the second call, want it left down", m.toast.title)
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
