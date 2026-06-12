package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// keyDebugMax caps how many captured keystrokes are kept in memory; the popup
// shows the most recent that fit the terminal height.
const keyDebugMax = 100

// keyDebugMaxWidth caps the inspector popup's outer width, mirroring
// keysSheetMaxWidth. Wide enough for a decoded line on one row.
const keyDebugMaxWidth = 100

// openKeyDebug opens the key-inspector popup (switcher "> Debug: key
// inspector"). The switcher has already closed itself before the command
// runs, so there's no overlap. Starts with a fresh, empty log.
func (m *Model) openKeyDebug() {
	m.keyDebugMode = true
	m.keyDebugLog = nil
}

func (m *Model) closeKeyDebug() {
	m.keyDebugMode = false
	m.keyDebugLog = nil
}

// handleKeyDebugKey owns every keystroke while the inspector is open: esc
// closes it (ctrl+c still quits the app); everything else is decoded and
// appended to the log instead of acting on the app, so the user can read off
// exactly what the terminal delivered.
func (m Model) handleKeyDebugKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeKeyDebug()
		return m, nil
	}
	m.keyDebugLog = append(m.keyDebugLog, formatKeyDebug(msg))
	if len(m.keyDebugLog) > keyDebugMax {
		m.keyDebugLog = m.keyDebugLog[len(m.keyDebugLog)-keyDebugMax:]
	}
	return m, nil
}

// formatKeyDebug decodes a key press into one diagnostic line. Keystroke()
// (the canonical "alt+up" form bindings match against) leads, then the textual
// String(), the raw modifier bits, the key code, and the literal text. That's
// enough to tell, e.g., option+arrow arriving as "alt+up" (the binding should
// fire) from a bare "up" (the Option modifier was swallowed by the terminal)
// — and a keypress that produces no line at all means the terminal never sent
// it (eaten by the OS / not emitted).
func formatKeyDebug(msg tea.KeyPressMsg) string {
	k := msg.Key()
	return fmt.Sprintf("Keystroke=%-14s String=%-12q Mod=%-18s Code=%-10q Text=%q",
		k.Keystroke(), k.String(), modNames(k.Mod), k.Code, k.Text)
}

// modNames renders the active modifier bits as a readable "+"-joined list
// ("ctrl+alt"), or "-" when none are set.
func modNames(mod tea.KeyMod) string {
	bits := []struct {
		bit  tea.KeyMod
		name string
	}{
		{tea.ModCtrl, "ctrl"}, {tea.ModAlt, "alt"}, {tea.ModShift, "shift"},
		{tea.ModMeta, "meta"}, {tea.ModHyper, "hyper"}, {tea.ModSuper, "super"},
		{tea.ModCapsLock, "caps"}, {tea.ModNumLock, "num"},
	}
	var parts []string
	for _, b := range bits {
		if mod.Contains(b.bit) {
			parts = append(parts, b.name)
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "+")
}

// renderKeyDebugPopup composes the bordered inspector popup: a title, a rule,
// then the most recent decoded keystrokes that fit the terminal height (newest
// at the bottom). Each line is truncated to the inner width so the box stays
// clean — Keystroke/String/Mod lead, so a truncation only drops the trailing
// Code/Text.
func (m *Model) renderKeyDebugPopup() string {
	if !m.keyDebugMode {
		return ""
	}
	outerW := m.width * 4 / 5
	if outerW > keyDebugMaxWidth {
		outerW = keyDebugMaxWidth
	}
	if outerW > m.width-2 {
		outerW = m.width - 2
	}
	if outerW < 20 {
		outerW = 20
	}
	inner := outerW - 4 // border (2) + padding (1) left/right
	if inner < 1 {
		inner = 1
	}

	dim := lipgloss.NewStyle().Foreground(dimColor)
	title := titleStyle.Render("Key inspector") + "  " +
		dim.Render("press keys to inspect · esc closes")
	rule := dim.Render(strings.Repeat("─", inner))

	// Show as many of the most recent lines as fit; leave room for the chrome
	// (border 2 + title 1 + rule 1 + a couple rows of vertical margin).
	maxRows := m.height - 8
	if maxRows < 3 {
		maxRows = 3
	}
	lines := m.keyDebugLog
	if len(lines) > maxRows {
		lines = lines[len(lines)-maxRows:]
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = truncate(l, inner)
	}
	body := strings.Join(out, "\n")
	if body == "" {
		body = dim.Render(truncate("(waiting for a keypress — try option+arrow, then ctrl+arrow / shift+arrow)", inner))
	}

	rows := []string{title, rule, body}
	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Width(outerW).
		Render(strings.Join(rows, "\n"))
}
