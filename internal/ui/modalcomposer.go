package ui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"

	"matterbox/internal/editor"
)

// The centred multi-line composer box: a modal that asks for a paragraph of
// text and posts it somewhere. Two surfaces use it — the Jira comment / reply
// composer and the diff view's inline note — and they must stay identical,
// because to the user they are the same gesture: type a comment, press enter.
//
// It is one component rather than two copies because the cursor placement is
// the fiddly half: the terminal cursor is positioned by arithmetic over the
// box's own layout (see modalComposerCursor), so a box whose layout drifts from
// its cursor maths puts the caret in the wrong place.

// newModalComposer builds the editor these boxes share: dynamic height, Enter
// posts, alt/shift+enter inserts a newline, native terminal cursor — the
// message composer's keys, so the muscle memory carries over.
func newModalComposer(placeholder string) editor.Model {
	ta := editor.New()
	ta.Placeholder = placeholder
	ta.CharLimit = 32767
	ta.DynamicHeight = true
	ta.MinHeight = 3
	ta.MaxHeight = maxInputHeight
	ta.MaxContentHeight = 10000
	ta.SetHeight(3)
	ta.SetPromptFunc(2, inputPromptFunc("┃ "))
	ta.NativeCursor = true
	ta.Styles.Placeholder = lipgloss.NewStyle().Foreground(dimColor)
	ta.ContinueLists = true
	ta.ContinueTables = true
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("alt+enter", "shift+enter"),
		key.WithHelp("alt+↵/shift+↵", "newline"),
	)
	ta.Focus()
	return ta
}

// modalComposerWidth is the box's outer width: the confirm-dialog width, capped
// to the terminal.
func (m *Model) modalComposerWidth() int {
	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 40 {
		outerW = 40
	}
	return outerW
}

// renderModalComposer draws the box: a centred title, an optional context line
// per entry in above (each followed by a blank row), the editor, then the hint.
// The caller styles its own context lines — they say different things (who is
// being replied to, which line of which file is being annotated).
func (m *Model) renderModalComposer(title string, above []string, hint string, input *editor.Model) string {
	outerW := m.modalComposerWidth()
	inner := outerW - 8
	if inner < 1 {
		inner = 1
	}
	input.SetWidth(inner)

	parts := []string{
		lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).Render(title),
		"",
	}
	for _, line := range above {
		parts = append(parts, lipgloss.NewStyle().Width(inner).Render(truncate(line, inner)), "")
	}
	parts = append(parts,
		input.View(),
		"",
		lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Foreground(dimColor).Italic(true).Render(hint),
	)
	body := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(focusedColor).Padding(1, 3).Render(body)
}

// modalComposerCursor is where the terminal cursor goes for a box with
// len(above) context lines. Mirrors renderModalComposer's layout exactly: change
// one and you change the other.
func (m *Model) modalComposerCursor(above int, input *editor.Model) (col, row int, ok bool) {
	cx, cy, okPos := input.CursorViewPos()
	if !okPos {
		return 0, 0, false
	}
	bodyH := 0
	if m.vcache != nil {
		bodyH = m.vcache.bodyH
	}
	if bodyH <= 0 {
		return 0, 0, false
	}
	outerW := m.modalComposerWidth()
	// Rows stacked above the editor inside the box: the title + its blank, then
	// each context line + its blank.
	aboveEditor := 2 + 2*above
	// Box outer height: rounded border (2) + padding (2) + the rows above the
	// editor, the editor itself, then a blank and the hint.
	boxH := 4 + aboveEditor + input.Height() + 2

	boxLeft := placeOffset(m.width, outerW)
	boxTop := tabsHeight + placeOffset(bodyH, boxH)
	// Editor origin inside the box: left border (1) + left padding (3); top
	// border (1) + top padding (1) + the rows above it.
	return boxLeft + 4 + cx, boxTop + 2 + aboveEditor + cy, true
}
