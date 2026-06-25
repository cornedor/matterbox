package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/textwidth"
)

// GFM pipe tables are parsed (width-independently) in renderMarkdown and laid
// out (width-dependently) at the wrap stage. The two phases are bridged by a
// single encoded "body line": renderMarkdown styles every cell once — so bold,
// links and emoji inside cells go through the same passes as ordinary text, and
// the width-independent markdown cache (postMarkdownCache) keeps holding — and
// packs the styled cells, plus the per-column alignment, into one opaque line.
// renderTableBox then decodes that line and draws the box to fit the pane's
// current width, re-running only on resize (postLineCache is width-keyed). This
// is why the table doesn't bake a width into the cached body the way a naive
// renderer would, which would otherwise show a stale-width table after a resize
// until the message was edited.

type tableAlign uint8

const (
	alignLeft tableAlign = iota
	alignCenter
	alignRight
)

// Sentinels delimiting an encoded table line. They all begin with \x00, which
// renderInline never leaves in its output (every \x00 sentinel it uses is
// resolved before it returns), so they cannot collide with styled cell content.
const (
	tablePrefix   = "\x00MDTBL\x00"
	tableAlignSep = "\x00A"
	tableRowSep   = "\x00R"
	tableCellSep  = "\x00C"
)

var (
	tableBorderStyle = lipgloss.NewStyle().Foreground(dimColor)
	tableHeaderStyle = lipgloss.NewStyle().Bold(true)
)

// mdTable is a decoded table: per-column alignment plus rows of already-styled
// cells (rows[0] is the header).
type mdTable struct {
	aligns []tableAlign
	rows   [][]string
}

// encodeTable packs a parsed table into the single opaque body line that
// survives strings.Split(body, "\n") and reaches the wrap stage intact.
func encodeTable(aligns []tableAlign, rows [][]string) string {
	var b strings.Builder
	b.WriteString(tablePrefix)
	for _, a := range aligns {
		b.WriteByte("lcr"[a])
	}
	b.WriteString(tableAlignSep)
	for ri, row := range rows {
		if ri > 0 {
			b.WriteString(tableRowSep)
		}
		for ci, c := range row {
			if ci > 0 {
				b.WriteString(tableCellSep)
			}
			b.WriteString(c)
		}
	}
	return b.String()
}

// decodeTable reverses encodeTable. ok is false when line is not a table line.
func decodeTable(line string) (*mdTable, bool) {
	if !strings.HasPrefix(line, tablePrefix) {
		return nil, false
	}
	rest := line[len(tablePrefix):]
	ai := strings.Index(rest, tableAlignSep)
	if ai < 0 {
		return nil, false
	}
	alignStr := rest[:ai]
	rest = rest[ai+len(tableAlignSep):]
	aligns := make([]tableAlign, len(alignStr))
	for i := 0; i < len(alignStr); i++ {
		switch alignStr[i] {
		case 'c':
			aligns[i] = alignCenter
		case 'r':
			aligns[i] = alignRight
		default:
			aligns[i] = alignLeft
		}
	}
	var rows [][]string
	for _, rowStr := range strings.Split(rest, tableRowSep) {
		rows = append(rows, strings.Split(rowStr, tableCellSep))
	}
	return &mdTable{aligns: aligns, rows: rows}, true
}

// tableLines decodes an encoded table line and lays it out to fit width,
// returning the box's rendered lines (each already gutter-indented and no wider
// than width). ok is false when line is not a table line, so callers fall
// through to their normal per-line handling.
func tableLines(line string, width int) ([]string, bool) {
	t, ok := decodeTable(line)
	if !ok {
		return nil, false
	}
	return renderTableBox(t, width), true
}

// expandTables replaces every encoded table line in a styled body with its
// laid-out box lines at width, leaving all other lines untouched. For consumers
// that hand a whole body string to a soft-wrapping viewport (the info and ref
// panels) rather than wrapping line-by-line themselves.
func expandTables(body string, width int) string {
	if !strings.Contains(body, tablePrefix) {
		return body
	}
	in := strings.Split(body, "\n")
	out := make([]string, 0, len(in))
	for _, l := range in {
		if tl, ok := tableLines(l, width); ok {
			out = append(out, tl...)
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// renderTableBox draws a decoded table as box-drawing rows sized to fit width.
// Columns take their natural (content) widths when the whole table already fits;
// otherwise they shrink proportionally toward tableColMin so the box still fits
// the pane, wrapping cell text across extra rows. Below the width at which even
// a one-cell-per-column box fits, it falls back to a borderless rendering. Every
// returned line carries the two-space body gutter and is no wider than width.
func renderTableBox(t *mdTable, width int) []string {
	n := len(t.aligns)
	if n == 0 || len(t.rows) == 0 {
		return nil
	}

	natural := make([]int, n)
	for _, row := range t.rows {
		for c := 0; c < n; c++ {
			if c < len(row) {
				if w := textwidth.Width(row[c]); w > natural[c] {
					natural[c] = w
				}
			}
		}
	}

	const gutter = "  "
	// Box overhead: one vertical bar between and around the n columns (n+1)
	// plus a one-space pad on each side of every column (2n).
	overhead := 3*n + 1
	content := width - len(gutter) - overhead
	if content < n {
		return tablePlainFallback(t, width)
	}
	cols := fitColumns(natural, content)

	rule := func(left, mid, right string) string {
		var b strings.Builder
		b.WriteString(left)
		for i, w := range cols {
			if i > 0 {
				b.WriteString(mid)
			}
			b.WriteString(strings.Repeat("─", w+2))
		}
		b.WriteString(right)
		return gutter + tableBorderStyle.Render(b.String())
	}

	out := make([]string, 0, len(t.rows)+3)
	out = append(out, rule("┌", "┬", "┐"))
	for ri, row := range t.rows {
		out = append(out, renderTableRow(row, cols, t.aligns, ri == 0)...)
		if ri == 0 && len(t.rows) > 1 {
			out = append(out, rule("├", "┼", "┤"))
		}
	}
	out = append(out, rule("└", "┴", "┘"))
	return out
}

// renderTableRow renders one logical row, soft-wrapping each cell within its
// column so a tall cell stretches the row across several lines. Header cells are
// bolded when they carry no inner styling of their own.
func renderTableRow(row []string, cols []int, aligns []tableAlign, header bool) []string {
	const gutter = "  "
	n := len(cols)
	cellLines := make([][]string, n)
	height := 1
	for c := 0; c < n; c++ {
		cell := ""
		if c < len(row) {
			cell = row[c]
		}
		wrapped := wrapCell(cell, cols[c])
		if header {
			for k := range wrapped {
				// A header cell with its own escapes (e.g. an inline code span)
				// can't be wholesale-bolded without the inner reset cancelling the
				// bold mid-cell, so leave those as the author styled them.
				if !strings.ContainsRune(wrapped[k], '\x1b') {
					wrapped[k] = tableHeaderStyle.Render(wrapped[k])
				}
			}
		}
		cellLines[c] = wrapped
		if len(wrapped) > height {
			height = len(wrapped)
		}
	}

	bar := tableBorderStyle.Render("│")
	lines := make([]string, height)
	for h := 0; h < height; h++ {
		var b strings.Builder
		b.WriteString(gutter)
		b.WriteString(bar)
		for c := 0; c < n; c++ {
			seg := ""
			if h < len(cellLines[c]) {
				seg = cellLines[c][h]
			}
			b.WriteString(" ")
			b.WriteString(padCell(seg, cols[c], aligns[c]))
			b.WriteString(" ")
			b.WriteString(bar)
		}
		lines[h] = b.String()
	}
	return lines
}

// wrapCell hard-wraps a styled cell to width cells per line (ANSI-aware), always
// returning at least one line.
func wrapCell(cell string, width int) []string {
	if width < 1 || cell == "" {
		return []string{""}
	}
	return strings.Split(ansi.Wrap(cell, width, ""), "\n")
}

// padCell pads (or, defensively, truncates) a single wrapped cell line to
// exactly width cells according to its column alignment.
func padCell(s string, width int, a tableAlign) string {
	sw := textwidth.Width(s)
	if sw > width {
		s = ansi.Truncate(s, width, "…")
		sw = textwidth.Width(s)
	}
	pad := width - sw
	if pad <= 0 {
		return s
	}
	switch a {
	case alignRight:
		return strings.Repeat(" ", pad) + s
	case alignCenter:
		l := pad / 2
		return strings.Repeat(" ", l) + s + strings.Repeat(" ", pad-l)
	default:
		return s + strings.Repeat(" ", pad)
	}
}

// fitColumns returns per-column widths summing to at most budget. When the
// natural widths already fit they are used verbatim (the table stays as compact
// as its content). Otherwise it water-fills: it finds the largest level L such
// that capping every column at L fits the budget, so narrow columns keep their
// full width and only the columns wider than L give up room. Any few cells left
// over after capping go to the widest columns first.
func fitColumns(natural []int, budget int) []int {
	n := len(natural)
	cols := make([]int, n)
	total, maxN := 0, 0
	for _, v := range natural {
		total += v
		maxN = max(maxN, v)
	}
	if total <= budget {
		copy(cols, natural)
		return cols
	}
	if budget < n { // pathological: one cell per column (padCell truncates)
		for i := range cols {
			cols[i] = 1
		}
		return cols
	}

	// Binary-search the fill level L: the highest cap whose capped sum fits.
	level := 1
	for lo, hi := 1, maxN; lo <= hi; {
		mid := (lo + hi) / 2
		sum := 0
		for _, v := range natural {
			sum += min(v, mid)
		}
		if sum <= budget {
			level = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	used := 0
	for i, v := range natural {
		cols[i] = min(v, level)
		used += cols[i]
	}
	for leftover := budget - used; leftover > 0; leftover-- {
		best, bestDeficit := -1, 0
		for i := range cols {
			if d := natural[i] - cols[i]; d > bestDeficit {
				best, bestDeficit = i, d
			}
		}
		if best < 0 {
			break
		}
		cols[best]++
	}
	return cols
}

// tablePlainFallback renders a table without a box for panes too narrow to fit
// even a minimal grid: each row's cells joined by a dim separator and soft-
// wrapped like ordinary body text, with a rule under the header.
func tablePlainFallback(t *mdTable, width int) []string {
	sep := tableBorderStyle.Render(" │ ")
	out := make([]string, 0, len(t.rows)+1)
	for ri, row := range t.rows {
		cells := make([]string, len(t.aligns))
		for c := range cells {
			if c < len(row) {
				cells[c] = row[c]
			}
		}
		out = append(out, wrapBodyLine("  "+strings.Join(cells, sep), width)...)
		if ri == 0 && len(t.rows) > 1 {
			out = append(out, "  "+tableBorderStyle.Render(strings.Repeat("─", min(max(width-2, 1), 24))))
		}
	}
	return out
}
