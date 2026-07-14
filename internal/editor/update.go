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
	// A live selection turns the next key into a selection action: a delete key
	// removes it; left/right collapse to the matching edge; any other navigation
	// drops it and proceeds. Self-inserting keys (and newline) fall through to
	// the switch below, where insert() replaces the selection.
	if m.HasSelection() {
		switch {
		case key.Matches(msg, k.DeleteCharacterBackward, k.DeleteCharacterForward,
			k.DeleteWordBackward, k.DeleteWordForward, k.DeleteAfterCursor, k.DeleteBeforeCursor):
			m.DeleteSelection()
			return
		case key.Matches(msg, k.CharacterBackward):
			s, _, _ := m.SelectionRange()
			m.ClearSelection()
			m.SetCursorOffset(s)
			return
		case key.Matches(msg, k.CharacterForward):
			_, e, _ := m.SelectionRange()
			m.ClearSelection()
			m.SetCursorOffset(e)
			return
		case key.Matches(msg, k.LineNext, k.LinePrevious, k.WordForward, k.WordBackward,
			k.LineStart, k.LineEnd, k.InputBegin, k.InputEnd):
			m.ClearSelection()
			// fall through to perform the move from the caret
		}
	}
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
	// Tab steps through a table's cells, but only from inside one: the InTableRow
	// test comes second so an ordinary keystroke never pays for it, and a tab that
	// lands anywhere else falls through to the owner's binding for the key.
	case key.Matches(msg, k.NextTableCell) && m.InTableRow():
		m.NextTableCell(1)
	case key.Matches(msg, k.PrevTableCell) && m.InTableRow():
		m.NextTableCell(-1)
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
