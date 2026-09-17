package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// The footer's right slot (my name + presence dot, or a status message) is the
// one part that can't ellipsize away, so the help bubble has to fit around it.
// It used to be budgeted without the "type to send" prefix, which pushed the
// name off the right edge as soon as the composer had focus.
func TestFooterFitsTheTerminalWidth(t *testing.T) {
	modes := []struct {
		name  string
		setup func(m *Model)
	}{
		{"idle", func(m *Model) {}},
		{"composing", func(m *Model) { m.focus = focusInput }},
		{"editing", func(m *Model) { m.focus = focusInput; m.editingPostID = "p1" }},
		{"thread", func(m *Model) { m.focus = focusInput; m.threadOpen = true }},
		{"filter", func(m *Model) { m.filterMode = true }},
		{"full help", func(m *Model) { m.help.ShowAll = true }},
		{"long status", func(m *Model) {
			m.focus = focusInput
			m.status = strings.Repeat("indexing… ", 30)
		}},
	}
	for _, mode := range modes {
		for _, w := range []int{20, 40, 80, 100, 140} {
			m := tabJoinModel()
			m.me = &model.User{Id: "u1", Username: "cdorrestijn"}
			m.width = w
			mode.setup(&m)
			for i, ln := range strings.Split(m.renderFooter(), "\n") {
				if got := lipgloss.Width(ln); got > w {
					t.Errorf("%s at width %d: row %d is %d cells\n%q",
						mode.name, w, i, got, ln)
				}
			}
		}
	}
}

// Whatever has to go, the name stays: the help ellipsizes first.
func TestFooterKeepsTheNameVisible(t *testing.T) {
	for _, w := range []int{40, 60, 80, 120} {
		m := tabJoinModel()
		m.me = &model.User{Id: "u1", Username: "cdorrestijn"}
		m.width = w
		m.focus = focusInput
		if !strings.Contains(lastLine(stripANSI(m.renderFooter())), "cdorrestijn") {
			t.Errorf("width %d: username missing from footer\n%q", w, m.renderFooter())
		}
	}
}
