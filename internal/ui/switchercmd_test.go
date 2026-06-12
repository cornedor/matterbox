package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
)

// commandModeModel returns a Model whose switcher is in "> command" mode with
// the given filter, ready to render. No channel is open, so the catalogue is
// just the static builtins (the mute toggle needs an open channel).
func commandModeModel(filter string) Model {
	ti := textinput.New()
	ti.SetValue(">" + filter)
	return Model{switcher: ti, width: 80}
}

// TestSwitcherCommandsTwoLine checks each command renders its name and its
// description on separate lines, so a long description can't crowd the name.
func TestSwitcherCommandsTwoLine(t *testing.T) {
	m := commandModeModel("index") // narrows to the Index-channel command
	out := m.renderSwitcherCommands(40, 36, 30)

	if !strings.Contains(out, "Index channel") {
		t.Fatalf("command name missing from output:\n%s", out)
	}
	// The description lives on its own line below the name, not appended to it.
	nameLine := lineContaining(out, "Index channel")
	if strings.Contains(nameLine, "cache N days") {
		t.Errorf("description should be on its own line, not on the name line:\n%q", nameLine)
	}
	if descLine := lineContaining(out, "cache N days"); descLine == "" {
		t.Errorf("description line missing from output:\n%s", out)
	}
}

// TestSwitcherCommandsFitsHeight pins the windowing: with a short popup the
// two-line rows are capped so the rendered height never exceeds maxH (which
// would push the footer off-screen), and the selected command stays visible.
func TestSwitcherCommandsFitsHeight(t *testing.T) {
	m := commandModeModel("") // every builtin command
	m.switcherIdx = len(m.commandResults()) - 1

	const maxH = 14
	out := m.renderSwitcherCommands(40, 36, maxH)
	if h := strings.Count(out, "\n") + 1; h > maxH {
		t.Errorf("popup height %d exceeds maxH %d:\n%s", h, maxH, out)
	}
	// The selected (last) command must be windowed into view.
	last := m.commandResults()[len(m.commandResults())-1]
	if !strings.Contains(out, last.name) {
		t.Errorf("selected command %q scrolled out of view:\n%s", last.name, out)
	}
}

// lineContaining returns the first line of s containing sub, or "".
func lineContaining(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}
