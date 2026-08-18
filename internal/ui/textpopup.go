package ui

import (
	tea "charm.land/bubbletea/v2"

	"matterbox/internal/viewport"
)

// textPopupState is a read-only scrollable text sheet (the same modal frame
// as the keys cheatsheet) for small utility views such as message stats.
type textPopupState struct {
	active bool
	title  string
	view   *viewport.Model
}

func (m *Model) openTextPopup(title, body string) {
	v := viewport.New()
	v.SoftWrap = true
	m.textPopup = textPopupState{active: true, title: title, view: &v}
	m.sizeTextPopup()
	m.textPopup.view.SetContent(body)
	m.textPopup.view.GotoTop()
}

func (m *Model) closeTextPopup() {
	m.textPopup = textPopupState{}
}

// sizeTextPopup keeps the popup viewport sized to the terminal. Call before
// rendering and on resize.
func (m *Model) sizeTextPopup() {
	if m.textPopup.view == nil {
		return
	}
	_, h := m.modalDims()
	m.textPopup.view.SetWidth(m.modalInnerWidth())
	m.textPopup.view.SetHeight(h)
}

// handleTextPopupKey owns every keystroke while the popup is open: esc/q
// close it (ctrl+c still quits); everything else scrolls the viewport.
func (m Model) handleTextPopupKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.closeTextPopup()
		return m, nil
	}
	if m.textPopup.view == nil {
		return m, nil
	}
	var cmd tea.Cmd
	*m.textPopup.view, cmd = m.textPopup.view.Update(msg)
	return m, cmd
}

func (m *Model) renderTextPopup() string {
	if !m.textPopup.active || m.textPopup.view == nil {
		return ""
	}
	return m.renderModal(m.textPopup.title, "esc/q close · ↑/↓ scroll", m.textPopup.view.View())
}
