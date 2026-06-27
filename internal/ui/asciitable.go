package ui

import "strings"

// convertPastedBoxTables rewrites box-drawing / ASCII tables in pasted text into
// GitHub-flavoured Markdown pipe tables, leaving everything else untouched. It
// returns the rewritten text and whether any table was converted.
//
// A table block is a maximal run of consecutive "cell" lines (│ a │ b │ or
// | a | b |) and "border" lines (┌──┬──┐, ├──┼──┤, +--+--+, ════) holding at
// least two cell lines and one border line — enough to tell a drawn table from
// stray pipe characters, and to skip already-Markdown tables (whose only
// separator row, |---|---|, is itself a cell line, so they have no border row).
// The first cell line becomes the header. Conversion is suppressed inside fenced
// (``` / ~~~) regions of the paste, mirroring how the caret-in-code-block check
// suppresses it for the surrounding buffer.
func convertPastedBoxTables(s string) (string, bool) {
	norm := strings.ReplaceAll(s, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")
	lines := strings.Split(norm, "\n")

	out := make([]string, 0, len(lines)+4)
	changed := false
	var fenceChar rune
	var fenceLen int
	for i := 0; i < len(lines); {
		line := lines[i]
		ch, n, restBlank := pasteFenceInfo(line)
		if fenceChar != 0 { // inside a fenced block: copy verbatim until it closes
			out = append(out, line)
			if ch == fenceChar && n >= fenceLen && restBlank {
				fenceChar, fenceLen = 0, 0
			}
			i++
			continue
		}
		if ch != 0 { // a fence opener: copy and enter the block
			out = append(out, line)
			fenceChar, fenceLen = ch, n
			i++
			continue
		}
		if end, ok := boxTableBlock(lines, i); ok {
			out = append(out, boxTableToMarkdown(lines[i:end])...)
			changed = true
			i = end
			continue
		}
		out = append(out, line)
		i++
	}
	if !changed {
		return s, false
	}
	return strings.Join(out, "\n"), true
}

// boxTableBlock returns the exclusive end index of a box-table block starting at
// start, or (start, false) if the lines there don't form one.
func boxTableBlock(lines []string, start int) (end int, ok bool) {
	cellRows, borderRows := 0, 0
	j := start
	for j < len(lines) {
		if boxCellLine(lines[j]) {
			cellRows++
		} else if boxBorderLine(lines[j]) {
			borderRows++
		} else {
			break
		}
		j++
	}
	if cellRows >= 2 && borderRows >= 1 {
		return j, true
	}
	return start, false
}

// boxTableToMarkdown converts one detected block into Markdown rows: the first
// cell line is the header, a |---|---| delimiter follows, then the rest.
func boxTableToMarkdown(block []string) []string {
	var rows [][]string
	for _, l := range block {
		if boxCellLine(l) {
			rows = append(rows, boxSplitCells(l))
		}
	}
	if len(rows) < 2 {
		return block // shouldn't happen given boxTableBlock's guard
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	out := make([]string, 0, len(rows)+1)
	out = append(out, boxMarkdownRow(rows[0], cols))
	delim := make([]string, cols)
	for i := range delim {
		delim[i] = "---"
	}
	out = append(out, boxMarkdownRow(delim, cols))
	for _, r := range rows[1:] {
		out = append(out, boxMarkdownRow(r, cols))
	}
	return out
}

// boxMarkdownRow renders cells as a | a | b | row, padding to cols columns and
// escaping any literal pipe in cell content.
func boxMarkdownRow(cells []string, cols int) string {
	var b strings.Builder
	b.WriteByte('|')
	for i := 0; i < cols; i++ {
		c := ""
		if i < len(cells) {
			c = strings.ReplaceAll(cells[i], "|", "\\|")
		}
		b.WriteByte(' ')
		b.WriteString(c)
		b.WriteString(" |")
	}
	return b.String()
}

// boxSplitCells splits a cell line on the one separator rune it uses (its first
// vertical rune), so an ASCII '|' inside a Unicode-box cell stays content. It
// drops the empty segments outside the outer bars and trims each cell.
func boxSplitCells(line string) []string {
	r := []rune(strings.TrimLeft(line, " \t"))
	if len(r) == 0 || !boxVertSep(r[0]) {
		return nil
	}
	sep := r[0]
	var parts []string
	var cur strings.Builder
	for _, c := range r {
		if c == sep {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(c)
	}
	parts = append(parts, cur.String())
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// boxCellLine reports whether a line is a table data/header row: after leading
// whitespace it starts with a vertical separator and repeats that same rune at
// least twice (the outer bars), so only one separator style is ever in play.
func boxCellLine(line string) bool {
	r := []rune(strings.TrimLeft(line, " \t"))
	if len(r) == 0 || !boxVertSep(r[0]) {
		return false
	}
	sep := r[0]
	count := 0
	for _, c := range r {
		if c == sep {
			count++
		}
	}
	return count >= 2
}

// boxBorderLine reports whether a line is a horizontal rule of a box table: only
// border runes (and spaces), with at least one true horizontal segment.
func boxBorderLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	horiz := false
	for _, r := range t {
		switch r {
		case ' ', '\t':
		case '─', '━', '═', '┄', '┅', '┈', '┉', '╌', '╍', '-', '=':
			horiz = true
		case '┌', '┐', '└', '┘', '├', '┤', '┬', '┴', '┼',
			'┏', '┓', '┗', '┛', '┣', '┫', '┳', '┻', '╋',
			'╔', '╗', '╚', '╝', '╠', '╣', '╦', '╩', '╬', '╪', '╞', '╡', '╤', '╧',
			'╭', '╮', '╰', '╯', '+':
		default:
			return false
		}
	}
	return horiz
}

// boxVertSep reports whether r is a vertical cell separator.
func boxVertSep(r rune) bool {
	switch r {
	case '│', '┃', '║', '|':
		return true
	}
	return false
}

// pasteFenceInfo mirrors editor.fenceInfo: a line is a code fence when, after up
// to three leading spaces, it opens with a run of three or more backticks or
// tildes. restBlank reports whether nothing but spaces follows the run (an info
// string disqualifies a closing fence).
func pasteFenceInfo(line string) (ch rune, length int, restBlank bool) {
	rs := []rune(line)
	i := 0
	for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
		i++
	}
	if i >= len(rs) || (rs[i] != '`' && rs[i] != '~') {
		return 0, 0, false
	}
	c := rs[i]
	n := 0
	for i < len(rs) && rs[i] == c {
		i++
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	for ; i < len(rs); i++ {
		if rs[i] != ' ' && rs[i] != '\t' {
			return c, n, false
		}
	}
	return c, n, true
}
