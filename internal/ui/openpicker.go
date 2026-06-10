package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// openFromPost handles the `o` action for a selected post: gather every
// openable target (attachments, images, links, bare URLs), open it
// straight away when there's exactly one, or raise the picker so the
// user can choose when there are several.
func (m Model) openFromPost(p *model.Post) (tea.Model, tea.Cmd) {
	opens := collectOpenables(p)
	switch len(opens) {
	case 0:
		m.status = "nothing to open on this message"
		return m, nil
	case 1:
		o := opens[0]
		m.status = "opening " + o.name + "…"
		return m, m.openOpenable(o)
	default:
		m.openOpenPicker(opens)
		return m, nil
	}
}

// openPickerActive reports whether the open-target picker modal is up.
func (m *Model) openPickerActive() bool {
	return len(m.openPickerItems) > 0
}

// openOpenPicker raises the modal listing every openable target on a
// post. Callers only reach here when there's more than one candidate.
func (m *Model) openOpenPicker(items []openable) {
	m.openPickerItems = items
	m.openPickerIdx = 0
}

// closeOpenPicker tears down picker state without opening anything.
func (m *Model) closeOpenPicker() {
	m.openPickerItems = nil
	m.openPickerIdx = 0
}

// applyOpenPick opens the highlighted target and closes the modal.
func (m *Model) applyOpenPick() tea.Cmd {
	if m.openPickerIdx < 0 || m.openPickerIdx >= len(m.openPickerItems) {
		return nil
	}
	o := m.openPickerItems[m.openPickerIdx]
	m.closeOpenPicker()
	m.status = "opening " + o.name + "…"
	return m.openOpenable(o)
}

// handleOpenPickerKey owns every keystroke while the open-target picker
// is up. Digit accelerators 1-9 open immediately; arrow keys navigate;
// enter opens the highlighted entry; esc cancels.
func (m Model) handleOpenPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.closeOpenPicker()
		return m, nil
	case "enter":
		cmd := m.applyOpenPick()
		return m, cmd
	}
	if key.Matches(msg, m.keys.Up) {
		if m.openPickerIdx > 0 {
			m.openPickerIdx--
		}
		return m, nil
	}
	if key.Matches(msg, m.keys.Down) {
		if m.openPickerIdx < len(m.openPickerItems)-1 {
			m.openPickerIdx++
		}
		return m, nil
	}
	// Digit accelerators 1..9 → open the matching index directly.
	if s := msg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		idx := int(s[0] - '1')
		if idx < len(m.openPickerItems) {
			m.openPickerIdx = idx
			cmd := m.applyOpenPick()
			return m, cmd
		}
	}
	return m, nil
}

// renderOpenPicker draws the modal listing the post's openable targets.
// Layout mirrors the reaction picker: rounded border, centred header,
// then one row per target with a digit accelerator and a kind marker
// (📎 attachment / 🔗 link). Long names are truncated to the dialog
// width so a stray URL can't blow the popup out or wrap.
func (m *Model) renderOpenPicker() string {
	if !m.openPickerActive() {
		return ""
	}
	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 32 {
		outerW = 32
	}
	inner := outerW - 8
	if inner < 1 {
		inner = 1
	}

	header := lipgloss.NewStyle().
		Width(inner).
		Align(lipgloss.Center).
		Bold(true).
		Render("Open")
	hint := lipgloss.NewStyle().
		Width(inner).
		Align(lipgloss.Center).
		Foreground(dimColor).
		Italic(true).
		Render("digit/↵ opens · ↑/↓ navigates · esc cancels")

	rowStyle := lipgloss.NewStyle()
	cursorStyle := lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
	rows := make([]string, 0, len(m.openPickerItems))
	for i, o := range m.openPickerItems {
		accel := " "
		if i < 9 {
			accel = fmt.Sprintf("%d", i+1)
		}
		kind := "🔗"
		if o.file != nil {
			kind = "📎"
		}
		prefix := fmt.Sprintf("[%s] %s  ", accel, kind)
		name := truncate(o.name, inner-lipgloss.Width(prefix))
		text := prefix + name
		if i == m.openPickerIdx {
			rows = append(rows, cursorStyle.Render("▸ "+text))
		} else {
			rows = append(rows, rowStyle.Render("  "+text))
		}
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		header,
		hint,
		"",
		strings.Join(rows, "\n"),
	)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(focusedColor).
		Padding(1, 3).
		Render(body)
}
