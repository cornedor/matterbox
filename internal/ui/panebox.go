package ui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"matterbox/internal/textwidth"
)

// A pane is a bordered box: side borders down every row, a bottom border, and
// content padded out to the pane's inner width. lipgloss renders that shape
// from a Style, and does it by segmenting every line into grapheme clusters to
// measure it — fine once per keystroke, expensive on a pane that animates.
// The feed's empty-state blob field repaints the whole pane at up to 60 fps,
// and that measurement was the single biggest cost in the frame.
//
// renderPaneBox draws the same box from lines that are already the right
// width, using textwidth.Width instead. It is byte-for-byte identical to the
// lipgloss expression it replaces (TestPaneBoxMatchesLipgloss holds that), and
// reports false for anything it can't draw that way — a line wider than the
// pane, a tab, a degenerate size — so those keep taking the full path.
func renderPaneBox(content string, width, height int, borderColor color.Color) (string, bool) {
	inner := width - 2 // between the side borders
	rows := height - 1 // content rows; the last row is the bottom border
	if inner <= 0 || rows <= 0 {
		return "", false
	}
	lines := strings.Split(content, "\n")
	pads := make([]int, len(lines))
	for i, line := range lines {
		n, ok := textwidth.Pad(line, inner)
		if !ok {
			return "", false
		}
		pads[i] = n
	}

	// Two lipgloss renders per pane instead of one per row: the side border and
	// the bottom rule carry the same escapes on every line, so they are styled
	// once and pasted.
	bs := lipgloss.NewStyle().Foreground(borderColor)
	side := bs.Render(border.Left)
	bottom := bs.Render(border.BottomLeft + strings.Repeat(border.Bottom, inner) + border.BottomRight)

	var b strings.Builder
	b.Grow((len(lines)+max(rows-len(lines), 0))*(2*len(side)+inner+1) + len(bottom))
	for i, line := range lines {
		b.WriteString(side)
		b.WriteString(line)
		b.WriteString(textwidth.Spaces(pads[i]))
		b.WriteString(side)
		b.WriteByte('\n')
	}
	for i := len(lines); i < rows; i++ { // pad the pane out to its height
		b.WriteString(side)
		b.WriteString(textwidth.Spaces(inner))
		b.WriteString(side)
		b.WriteByte('\n')
	}
	b.WriteString(bottom)
	return b.String(), true
}

// joinVerticalLeft stacks blocks left-aligned, padding every line out to the
// widest line in the stack — lipgloss.JoinVertical(lipgloss.Left, …), byte for
// byte, measured with textwidth.Width instead of grapheme segmentation. This
// is the last thing every frame does, over the whole screen, so it measures
// every visible line twice in lipgloss's version (once to find the width, once
// to pad).
//
// It reports false for blocks lipgloss would rewrite before measuring — tabs
// (expanded to four spaces) and CRLF line endings — leaving those to it.
func joinVerticalLeft(blocks ...string) (string, bool) {
	if len(blocks) < 2 {
		return "", false
	}
	lines := make([]string, 0, 64)
	widths := make([]int, 0, 64)
	widest := 0
	for _, block := range blocks {
		if strings.IndexByte(block, '\t') >= 0 || strings.Contains(block, "\r\n") {
			return "", false
		}
		for _, line := range strings.Split(block, "\n") {
			w := textwidth.Width(line)
			lines = append(lines, line)
			widths = append(widths, w)
			widest = max(widest, w)
		}
	}

	total := 0
	for i, line := range lines {
		total += len(line) + widest - widths[i] + 1
	}
	var b strings.Builder
	b.Grow(total)
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		b.WriteString(textwidth.Spaces(widest - widths[i]))
	}
	return b.String(), true
}
