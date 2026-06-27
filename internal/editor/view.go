package editor

import (
	"strings"

	"charm.land/lipgloss/v2"

	"matterbox/internal/textwidth"
)

// View renders the visible scroll window: prompt gutter, wrapped text, inline
// decorations, and the cursor — all in one pass over the same wrap geometry, so
// decorations and the caret always sit exactly where the text does, even when
// the field has scrolled.
func (m *Model) View() string {
	cw := m.contentWidth()
	h := max(m.height, 1)
	base := m.textStyle()

	if m.isEmpty() && m.Placeholder != "" {
		return m.viewPlaceholder(cw, h, base)
	}

	rows := m.layout(true)
	ci, _ := m.cursorVis(rows)

	// Pre-merge each decoration's style with the text style once per render.
	decorStyles := make([]lipgloss.Style, len(m.decorations))
	for i, d := range m.decorations {
		decorStyles[i] = d.Style.Inherit(base).Inline(true)
	}

	lines := make([]string, 0, h)
	for i := m.yOffset; i < m.yOffset+h; i++ {
		prompt := m.renderPrompt(i)
		if i < len(rows) {
			lines = append(lines, prompt+m.renderRow(rows[i], i == ci, cw, base, decorStyles))
		} else {
			lines = append(lines, prompt+base.Render(strings.Repeat(" ", cw)))
		}
	}
	return strings.Join(lines, "\n")
}

// isEmpty reports whether the buffer holds no characters.
func (m *Model) isEmpty() bool {
	return len(m.lines) == 1 && len(m.lines[0]) == 0
}

// viewPlaceholder renders the dimmed placeholder over an otherwise empty buffer,
// with the caret on its first cell when focused.
func (m *Model) viewPlaceholder(cw, h int, base lipgloss.Style) string {
	ph := []rune(m.Placeholder)
	ph = truncateToWidth(ph, cw)
	phStyle := m.Styles.Placeholder.Inline(true)

	var first strings.Builder
	used := 0
	if m.focus {
		if len(ph) > 0 {
			first.WriteString(m.Styles.Cursor.Inline(true).Render(string(ph[0])))
			used += textwidth.Width(string(ph[0]))
			if len(ph) > 1 {
				first.WriteString(phStyle.Render(string(ph[1:])))
				used += textwidth.Width(string(ph[1:]))
			}
		} else {
			first.WriteString(m.Styles.Cursor.Inline(true).Render(" "))
			used++
		}
	} else {
		first.WriteString(phStyle.Render(string(ph)))
		used += textwidth.Width(string(ph))
	}
	if used < cw {
		first.WriteString(base.Render(strings.Repeat(" ", cw-used)))
	}

	lines := make([]string, 0, h)
	for i := range h {
		if i == 0 {
			lines = append(lines, m.renderPrompt(0)+first.String())
			continue
		}
		lines = append(lines, m.renderPrompt(i)+base.Render(strings.Repeat(" ", cw)))
	}
	return strings.Join(lines, "\n")
}

// renderPrompt renders the prompt for the given global visual line, left-padded
// to promptWidth.
func (m *Model) renderPrompt(visualLine int) string {
	if m.promptWidth <= 0 {
		return ""
	}
	var p string
	if m.promptFunc != nil {
		p = m.promptFunc(visualLine, m.focus)
	}
	if w := textwidth.Width(p); w < m.promptWidth {
		p = strings.Repeat(" ", m.promptWidth-w) + p
	}
	return m.Styles.Prompt.Render(p)
}

// renderRow renders one visual row's content (no prompt) padded to cw, drawing
// decorations and, on the cursor row, the caret.
func (m *Model) renderRow(vr visRow, isCursorRow bool, cw int, base lipgloss.Style, decorStyles []lipgloss.Style) string {
	rs := m.lines[vr.line][vr.a:vr.b]
	lineStart := m.lineStartOffset(vr.line)

	var b strings.Builder
	used := 0
	var run []rune
	var runStyle lipgloss.Style
	curDecor := -2 // sentinel distinct from -1 (no decoration)
	flush := func() {
		if len(run) > 0 {
			b.WriteString(runStyle.Render(string(run)))
			run = run[:0]
		}
	}
	for k := range rs {
		if m.focus && isCursorRow && vr.a+k == m.col {
			flush()
			curDecor = -2
			b.WriteString(m.Styles.Cursor.Inline(true).Render(string(rs[k])))
			used += textwidth.Width(string(rs[k]))
			continue
		}
		di := m.decorIndexAt(lineStart + vr.a + k)
		if di != curDecor {
			flush()
			curDecor = di
			if di >= 0 {
				runStyle = decorStyles[di]
			} else {
				runStyle = base
			}
		}
		run = append(run, rs[k])
		used += textwidth.Width(string(rs[k]))
	}
	flush()

	// Caret at the end of the row (or on a reserved trailing row): a
	// reverse-video space.
	if m.focus && isCursorRow && m.col == vr.b {
		b.WriteString(m.Styles.Cursor.Inline(true).Render(" "))
		used++
	}

	if used < cw {
		b.WriteString(base.Render(strings.Repeat(" ", cw-used)))
	}
	return b.String()
}

// lineStartOffset is the rune offset of the start of logical line idx within
// Value() (newlines counted).
func (m *Model) lineStartOffset(idx int) int {
	off := 0
	for i := 0; i < idx && i < len(m.lines); i++ {
		off += len(m.lines[i]) + 1
	}
	return off
}

// decorIndexAt returns the index of the first decoration covering rune offset
// off, or -1 if none.
func (m *Model) decorIndexAt(off int) int {
	for i, d := range m.decorations {
		if off >= d.Start && off < d.End {
			return i
		}
	}
	return -1
}

// truncateToWidth trims runes so their display width does not exceed w.
func truncateToWidth(rs []rune, w int) []rune {
	total := 0
	for i, r := range rs {
		cwid := textwidth.Width(string(r))
		if total+cwid > w {
			return rs[:i]
		}
		total += cwid
	}
	return rs
}
