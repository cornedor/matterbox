package editor

import "charm.land/lipgloss/v2"

// Styles controls how the editor renders. Unlike textarea's eight-field
// focused/blurred StyleState pair, this keeps only what matterbox uses: a text
// style per focus state, a placeholder style, a prompt style, and the cursor
// style. There is no cursor-line highlight and no blink.
type Styles struct {
	FocusedText lipgloss.Style
	BlurredText lipgloss.Style
	Placeholder lipgloss.Style
	Prompt      lipgloss.Style
	Cursor      lipgloss.Style
}

// DefaultStyles returns plain styles: a reverse-video block cursor and a dim
// placeholder. Callers typically override colours to match their theme.
func DefaultStyles() Styles {
	return Styles{
		FocusedText: lipgloss.NewStyle(),
		BlurredText: lipgloss.NewStyle(),
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		Prompt:      lipgloss.NewStyle(),
		Cursor:      lipgloss.NewStyle().Reverse(true),
	}
}

// textStyle is the active text style for the current focus state, set inline so
// it never emits stray newlines into a single rendered row.
func (m *Model) textStyle() lipgloss.Style {
	if m.focus {
		return m.Styles.FocusedText.Inline(true)
	}
	return m.Styles.BlurredText.Inline(true)
}

// Decoration is an inline styled span addressed by rune offset into Value()
// (the half-open range [Start, End)). It is drawn during the editor's own
// wrap+scroll render pass, so it always lines up with the on-screen text — the
// grammar/spell underline overlay is expressed as a set of these.
type Decoration struct {
	Start, End int
	Style      lipgloss.Style
}

// SetDecorations replaces the inline decorations. Offsets are clamped at render
// time, so stale spans never corrupt output (they simply don't draw).
func (m *Model) SetDecorations(d []Decoration) { m.decorations = d }

// ClearDecorations removes all inline decorations.
func (m *Model) ClearDecorations() { m.decorations = nil }

// Decorations returns the current inline decorations (read-only view).
func (m *Model) Decorations() []Decoration { return m.decorations }
