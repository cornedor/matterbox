package editor

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// Update handles a key press or paste, mutating the buffer. It returns the
// model by value and a nil command (the editor has no async work — the cursor
// is static), matching the `m.field, cmd = m.field.Update(msg)` call pattern.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		m.insert([]rune(msg.Content))
	case tea.KeyPressMsg:
		m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyPressMsg) {
	k := m.KeyMap
	switch {
	case key.Matches(msg, k.DeleteAfterCursor):
		m.deleteAfterCursor()
	case key.Matches(msg, k.DeleteBeforeCursor):
		m.deleteBeforeCursor()
	case key.Matches(msg, k.DeleteCharacterBackward):
		m.deleteBackward()
	case key.Matches(msg, k.DeleteCharacterForward):
		m.deleteForward()
	case key.Matches(msg, k.DeleteWordBackward):
		m.deleteWordBackward()
	case key.Matches(msg, k.DeleteWordForward):
		m.deleteWordForward()
	case key.Matches(msg, k.InsertNewline):
		m.InsertNewline()
	case key.Matches(msg, k.LineEnd):
		m.cursorLineEnd()
	case key.Matches(msg, k.LineStart):
		m.CursorStart()
	case key.Matches(msg, k.CharacterForward):
		m.characterRight()
	case key.Matches(msg, k.CharacterBackward):
		m.characterLeft()
	case key.Matches(msg, k.LineNext):
		m.cursorDown()
	case key.Matches(msg, k.LinePrevious):
		m.cursorUp()
	case key.Matches(msg, k.WordForward):
		m.wordRight()
	case key.Matches(msg, k.WordBackward):
		m.wordLeft()
	case key.Matches(msg, k.InputBegin):
		m.MoveToBegin()
	case key.Matches(msg, k.InputEnd):
		m.CursorEnd()
	default:
		if msg.Text != "" {
			m.insert([]rune(msg.Text))
		}
	}
}

// cursorLineEnd moves the cursor to the end of the current logical line.
func (m *Model) cursorLineEnd() {
	m.col = len(m.lines[m.row])
	m.refreshDesired()
	m.clampScroll()
}
