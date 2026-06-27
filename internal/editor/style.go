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
	// Markdown styles the inline markdown spans drawn when Model.MarkdownHighlight
	// is on. It is themeable but only consulted while that toggle is set.
	Markdown MarkdownStyles
}

// MarkdownStyles paints the inline markdown spans (see Model.MarkdownHighlight).
// Marker styles the syntax tokens themselves (`*`, `_`, `~`, backticks, fence
// lines) — they stay on screen, dimmed, so the user can see what's changing —
// while the remaining fields style the content the markers enclose. Each is
// merged over the active text style, so leaving a field at its zero value just
// inherits the surrounding text.
type MarkdownStyles struct {
	Marker    lipgloss.Style
	Bold      lipgloss.Style
	Italic    lipgloss.Style
	Strike    lipgloss.Style
	Code      lipgloss.Style
	CodeBlock lipgloss.Style
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
		Markdown:    DefaultMarkdownStyles(),
	}
}

// DefaultMarkdownStyles dims the markers and renders bold/italic/strikethrough
// with the matching SGR attribute; inline code and fenced blocks pick up a cyan
// foreground, mirroring the message-pane renderer.
func DefaultMarkdownStyles() MarkdownStyles {
	return MarkdownStyles{
		Marker:    lipgloss.NewStyle().Faint(true),
		Bold:      lipgloss.NewStyle().Bold(true),
		Italic:    lipgloss.NewStyle().Italic(true),
		Strike:    lipgloss.NewStyle().Strikethrough(true),
		Code:      lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		CodeBlock: lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
	}
}

// attr returns the raw attribute style for a markdown class (before it is merged
// over the text style). mdNone yields the zero style.
func (s MarkdownStyles) attr(c mdClass) lipgloss.Style {
	switch c {
	case mdMarker:
		return s.Marker
	case mdBold:
		return s.Bold
	case mdItalic:
		return s.Italic
	case mdStrike:
		return s.Strike
	case mdCode:
		return s.Code
	case mdCodeBlock:
		return s.CodeBlock
	default:
		return lipgloss.Style{}
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
