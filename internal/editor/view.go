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

	// Selection bounds in rune-offset space, drawn only while focused (Blur drops
	// the selection anyway; this also guards a stale highlight). -1/-1 means none.
	selStart, selEnd := -1, -1
	if m.focus {
		if s, e, ok := m.SelectionRange(); ok {
			selStart, selEnd = s, e
		}
	}

	// Pre-merge each decoration's style with the text style once per render.
	decorStyles := make([]lipgloss.Style, len(m.decorations))
	for i, d := range m.decorations {
		decorStyles[i] = d.Style.Inherit(base).Inline(true)
	}

	// Inline markdown: a per-rune class map plus the per-class styles merged over
	// the text style once. Both stay nil/empty when the toggle is off, so the
	// render path is unchanged for non-markdown fields (SQL editor, etc.).
	var classes []mdClass
	var mdStyles []lipgloss.Style
	if m.MarkdownHighlight {
		classes = m.markdownClasses()
		mdStyles = make([]lipgloss.Style, mdCodeBlock+1)
		for c := mdMarker; c <= mdCodeBlock; c++ {
			mdStyles[c] = m.Styles.Markdown.attr(c).Inherit(base).Inline(true)
		}
	}

	lines := make([]string, 0, h)
	for i := m.yOffset; i < m.yOffset+h; i++ {
		prompt := m.renderPrompt(i)
		if i < len(rows) {
			lines = append(lines, prompt+m.renderRow(rows[i], i == ci, cw, base, decorStyles, classes, mdStyles, selStart, selEnd))
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
// markdown spans and decorations and, on the cursor row, the caret. A run is
// flushed whenever the markdown class or decoration index changes, so each run
// carries a single composed style: markdown attributes over the text style, with
// any decoration's underline layered on top (decorations and markdown compose).
func (m *Model) renderRow(vr visRow, isCursorRow bool, cw int, base lipgloss.Style, decorStyles []lipgloss.Style, classes []mdClass, mdStyles []lipgloss.Style, selStart, selEnd int) string {
	rs := m.lines[vr.line][vr.a:vr.b]
	lineStart := m.lineStartOffset(vr.line)

	var b strings.Builder
	used := 0
	var run []rune
	var runStyle lipgloss.Style
	curDecor := -2 // sentinel distinct from -1 (no decoration)
	curClass := mdClass(255)
	curSel := false
	flush := func() {
		if len(run) > 0 {
			b.WriteString(runStyle.Render(string(run)))
			run = run[:0]
		}
	}
	classAt := func(off int) mdClass {
		if off >= 0 && off < len(classes) {
			return classes[off]
		}
		return mdNone
	}
	for k := range rs {
		if m.focus && isCursorRow && vr.a+k == m.col {
			flush()
			curDecor, curClass, curSel = -2, mdClass(255), false
			b.WriteString(m.Styles.Cursor.Inline(true).Render(string(rs[k])))
			used += textwidth.Width(string(rs[k]))
			continue
		}
		off := lineStart + vr.a + k
		// A recognised slash command paints bold with the moving orange shimmer,
		// per cell (each gets its own gradient colour), overriding markdown and
		// decorations within the span.
		if pos, ok := m.commandSpanAt(off); ok {
			flush()
			curDecor, curClass, curSel = -2, mdClass(255), false
			st := lipgloss.NewStyle().Bold(true).
				Foreground(shimmerColor(pos, m.cmdEnd-m.cmdStart, m.cmdPhase)).
				Inherit(base).Inline(true)
			b.WriteString(st.Render(string(rs[k])))
			used += textwidth.Width(string(rs[k]))
			continue
		}
		di := m.decorIndexAt(off)
		mc := classAt(off)
		sel := off >= selStart && off < selEnd
		if di != curDecor || mc != curClass || sel != curSel {
			flush()
			curDecor, curClass, curSel = di, mc, sel
			runStyle = m.composeStyle(base, decorStyles, mdStyles, di, mc)
			if sel {
				// Layer the selection attributes (reverse video) over the run's own
				// style so the highlight reads the same over plain and styled text.
				runStyle = m.Styles.Selection.Inherit(runStyle).Inline(true)
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

// composeStyle returns the style for a run with markdown class mc and decoration
// index di. The precomputed slices cover the common single-layer cases; only a
// run that is both styled markdown and decorated pays for an extra merge, and
// there the decoration's underline layers over the markdown style (Inherit fills
// only unset fields, so it never clobbers the markdown attributes).
func (m *Model) composeStyle(base lipgloss.Style, decorStyles, mdStyles []lipgloss.Style, di int, mc mdClass) lipgloss.Style {
	switch {
	case mc == mdNone && di < 0:
		return base
	case mc == mdNone:
		return decorStyles[di]
	case di < 0:
		return mdStyles[mc]
	default:
		return m.decorations[di].Style.Inherit(mdStyles[mc]).Inline(true)
	}
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
