package editor

import (
	"strings"

	"matterbox/internal/textwidth"
)

// A markdown pipe table is edited as its source text — the bars stay on screen,
// like every other marker this editor keeps visible — but the source is kept
// tidy: each column is padded to its widest cell as it is typed, so the bars line
// up; a newline opens the next row (and drops in the |---| separator under a
// header that hasn't got one); Tab steps from cell to cell. All of it behind
// ContinueTables, which the composer and the jira-comment editor set.
//
// The padding is cosmetic — GFM ignores the spaces around a cell's content, and
// the message pane lays the table out to its own width anyway (ui.renderTableBox)
// — so it is dropped whenever it would push a row past the editor's width. That
// is the one place this differs from the message-pane renderer, which shrinks a
// column and truncates the cell with an ellipsis to make a table fit: here the
// cells are the message, so a table too wide to align is left unaligned rather
// than have anything cut out of it.

// cellAlign is the alignment a separator cell's colons ask for (|:--|--:|:-:|).
type cellAlign uint8

const (
	alignDefault cellAlign = iota
	alignLeft
	alignRight
	alignCenter
)

// minDelimWidth is the narrowest a column may be padded to once the table has a
// separator row: enough for ":-:" to keep both its colons.
const minDelimWidth = 3

// tableCell is one cell of a parsed row: its source text (everything between the
// bars, padding included) and the rune span that text occupies in the line.
type tableCell struct {
	text       string
	start, end int
}

// tableRow is a parsed pipe-table row.
type tableRow struct {
	indent string
	cells  []tableCell
	delim  bool        // the |---|:-:| separator under the header
	aligns []cellAlign // per cell, read off a separator row's colons
	// closed reports that the row ends on a bar. A row still being typed doesn't,
	// and is left that way: writing the closing bar for it would put a bar to the
	// right of the caret, and the one the typist then writes themselves would open
	// a phantom column. It is closed on the realign after the caret leaves.
	closed bool
}

// parseTableRow splits a pipe-table row into its cells. A row is a line whose
// first non-blank rune is '|' and that carries at least two unescaped bars — the
// leading bar is what keeps prose with a stray '|' in it from being reflowed as a
// table (the same conservatism as the trailing space in parseListItem). A "\|" is
// content, not a separator. Text after the last bar is a final cell, so a row may
// leave its closing bar off while it is still being typed.
func parseTableRow(line []rune) (tableRow, bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i >= len(line) || line[i] != '|' {
		return tableRow{}, false
	}
	row := tableRow{indent: string(line[:i])}
	bars, start := 0, 0
	var cur strings.Builder
	for ; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteString(`\|`)
			i++
		case line[i] == '|':
			if bars > 0 {
				row.cells = append(row.cells, tableCell{text: cur.String(), start: start, end: i})
			}
			bars++
			cur.Reset()
			start = i + 1
		default:
			cur.WriteRune(line[i])
		}
	}
	if bars < 2 {
		return tableRow{}, false
	}
	row.closed = true
	if s := cur.String(); strings.TrimSpace(s) != "" {
		row.cells = append(row.cells, tableCell{text: s, start: start, end: len(line)})
		row.closed = false
	}
	if len(row.cells) == 0 {
		return tableRow{}, false
	}
	row.delim, row.aligns = delimRow(row.cells)
	return row, true
}

// delimRow reports whether every cell of a row is a separator cell, and the
// alignments their colons ask for.
func delimRow(cells []tableCell) (bool, []cellAlign) {
	aligns := make([]cellAlign, len(cells))
	for i, c := range cells {
		a, ok := delimCell(c.text)
		if !ok {
			return false, nil
		}
		aligns[i] = a
	}
	return true, aligns
}

// delimCell matches a separator cell — a run of dashes, optionally colon-flanked
// (---, :--, --:, :-:) — and returns the alignment it asks for.
func delimCell(s string) (cellAlign, bool) {
	t := strings.TrimSpace(s)
	left := strings.HasPrefix(t, ":")
	right := len(t) > 1 && strings.HasSuffix(t, ":")
	body := t
	if left {
		body = body[1:]
	}
	if right {
		body = body[:len(body)-1]
	}
	if body == "" {
		return alignDefault, false
	}
	for _, r := range body {
		if r != '-' {
			return alignDefault, false
		}
	}
	switch {
	case left && right:
		return alignCenter, true
	case right:
		return alignRight, true
	case left:
		return alignLeft, true
	}
	return alignDefault, true
}

// caretTableRow returns the parsed table row the caret is on, in a table-aware
// editor and outside a code block. It is the gate on everything in this file that
// answers a keystroke.
func (m *Model) caretTableRow() (tableRow, bool) {
	if !m.ContinueTables {
		return tableRow{}, false
	}
	row, ok := parseTableRow(m.lines[m.row])
	if !ok || m.InCodeBlock() {
		return tableRow{}, false
	}
	return row, true
}

// InTableRow reports whether the caret sits inside a pipe table's grid. The owner
// asks before routing Tab: in the grid Tab steps to the next cell, everywhere else
// it keeps whatever meaning the owner gives it (focus-cycle in the composer — see
// ui.handleInputKey).
//
// The head of the line, ahead of the opening bar, counts as outside the grid. That
// is the far end of the way out: shift+tab walks back through the cells, off the
// first one to the head of the row, and from there the next one is the owner's
// again and cycles focus out of the composer. Nothing here is a key the typist
// cannot press their way out of.
func (m *Model) InTableRow() bool {
	row, ok := m.caretTableRow()
	return ok && m.col >= row.cells[0].start
}

// tableBlock returns the half-open row range of the run of table rows containing
// row. ok is false when that line isn't a table row.
func (m *Model) tableBlock(row int) (start, end int, ok bool) {
	if row < 0 || row >= len(m.lines) {
		return 0, 0, false
	}
	if _, ok := parseTableRow(m.lines[row]); !ok {
		return 0, 0, false
	}
	start, end = row, row+1
	for start > 0 {
		if _, ok := parseTableRow(m.lines[start-1]); !ok {
			break
		}
		start--
	}
	for end < len(m.lines) {
		if _, ok := parseTableRow(m.lines[end]); !ok {
			break
		}
		end++
	}
	return start, end, true
}

// realignTables re-pads the table the caret is in after an edit. Only that one:
// an edit elsewhere leaves other tables alone, and costs nothing but the parse of
// the current line, which gives up on its first non-'|' rune.
func (m *Model) realignTables() {
	if !m.ContinueTables {
		return
	}
	if _, ok := parseTableRow(m.lines[m.row]); !ok {
		return
	}
	if m.InCodeBlock() {
		return
	}
	start, end, ok := m.tableBlock(m.row)
	if ok {
		m.alignBlock(start, end)
	}
}

// continueTable handles a newline pressed inside a pipe table: it opens a fresh
// row below, never splitting the current one (a GFM row cannot span lines). A
// newline on a row that is still empty ends the table instead, dropping that row,
// the way a newline on an empty list item ends a list. Reports whether it handled
// the key; false means insert a plain line break.
func (m *Model) continueTable() bool {
	// A caret at the very start of the row breaks the line like any other text,
	// so there is still a way to open space above a table.
	if m.col == 0 || m.InCodeBlock() {
		return false
	}
	start, _, ok := m.tableBlock(m.row)
	if !ok {
		return false
	}
	row, _ := parseTableRow(m.lines[m.row])
	if !row.delim && m.row > start && blankCells(row.cells) {
		m.endTable()
		return true
	}
	m.openTableRow()
	return true
}

// endTable drops the row the caret is on — which is empty — and leaves the caret on
// the bare line it becomes, under the table. It is how both a newline and a tab on
// a row left empty say "done", and it is the way out of a table at the end of the
// buffer, where there is no line below to move down to.
func (m *Model) endTable() {
	m.lines[m.row] = []rune{}
	m.col = 0
	m.afterEdit()
}

// The bars, the padding around them and the separator's dashes are structure, not
// text: the realign draws them from the cells' content, so a delete key that ate
// one would only have it drawn straight back, and would read as a dead key. So the
// delete keys work in terms of the content instead. A row that is nothing but
// structure — an empty one, or the separator — goes as a whole; a delete at the
// edge of a cell's content steps over the structure to the next cell rather than
// nibbling at it; and a delete out in the padding is taken from the content the
// padding belongs to. Every press moves something, and a held backspace takes a
// table apart cell by cell and row by row until it is gone.

// deleteBackwardTable answers a backspace inside a pipe table. Reports whether it
// handled the key; false leaves the plain backspace to run (having first, where
// needed, pulled the caret out of the padding and onto the content, so what the
// plain backspace takes is a character the typist can actually see).
func (m *Model) deleteBackwardTable() bool {
	row, ok := m.caretTableRow()
	if !ok {
		return false
	}
	// At or ahead of the opening bar, a backspace deletes the bar (which drops the
	// row out of the table) or joins the line above: both are real progress.
	if m.col <= row.cells[0].start {
		return false
	}
	if row.delim {
		// The dashes are redrawn from the column widths, so deleting one is a dead
		// key — take the whole separator row instead. A colon is the typist's own
		// (it sets the column's alignment), so that one deletes as itself.
		if m.col > 0 && m.lines[m.row][m.col-1] == ':' {
			return false
		}
		m.deleteRow(m.row, true)
		return true
	}
	if blankCells(row.cells) {
		// Nothing in the row to delete — and this is how a row opened by mistake is
		// taken back.
		m.deleteRow(m.row, true)
		return true
	}
	idx, _ := cellAtCol(row, m.col)
	if idx < 0 { // parked past the closing bar: the plain backspace eats the bar
		return false
	}
	cell := row.cells[idx]
	head, tail := cellBounds(cell)
	switch {
	case m.col > cell.start+tail:
		// Out in the cell's trailing padding: bring the caret back onto the content
		// so the backspace takes a character rather than a space it would get back.
		m.col = cell.start + tail
		return false
	case m.col > cell.start+head:
		return false // inside the content: an ordinary backspace
	case idx > 0:
		m.moveToCell(m.row, idx-1, true) // step back over the bar into the cell before
	case m.row > m.tableStart():
		prev, ok := parseTableRow(m.lines[m.row-1])
		if !ok {
			return false
		}
		m.moveToCell(m.row-1, len(prev.cells)-1, true) // and back up into the row before
	default:
		// The head of the table's first row, with text still in it: the typist is
		// backing out of a table they have changed their mind about, so give up the
		// formatting and keep what they wrote.
		m.untableRow(m.row)
		return true
	}
	m.afterEdit()
	return true
}

// deleteForwardTable answers the delete key inside a pipe table — the mirror of
// deleteBackwardTable.
func (m *Model) deleteForwardTable() bool {
	row, ok := m.caretTableRow()
	if !ok {
		return false
	}
	if row.delim {
		if m.col < len(m.lines[m.row]) && m.lines[m.row][m.col] == ':' {
			return false
		}
		m.deleteRow(m.row, false)
		return true
	}
	if blankCells(row.cells) {
		m.deleteRow(m.row, false)
		return true
	}
	idx, _ := cellAtCol(row, m.col)
	if idx < 0 {
		return false // past the closing bar: the plain delete joins the line below
	}
	_, end, _ := m.tableBlock(m.row)
	cell := row.cells[idx]
	head, tail := cellBounds(cell)
	switch {
	case head < tail && m.col < cell.start+head:
		m.col = cell.start + head // out in the leading padding: onto the content
		return false
	case head < tail && m.col < cell.start+tail:
		return false // inside the content: an ordinary delete
	case idx+1 < len(row.cells):
		m.moveToCell(m.row, idx+1, false) // step over the bar into the cell after
	case m.row+1 < end:
		// Nothing left ahead in this row. Step into the row below rather than join it
		// on: rows don't merge, they empty out and go (the separator included, which
		// is why this one doesn't skip it the way tab does).
		m.moveToCell(m.row+1, 0, false)
	default:
		m.col = len(m.lines[m.row]) // out past the closing bar; the next delete joins on
	}
	m.afterEdit()
	return true
}

// tableStart returns the first row of the table block the caret is in.
func (m *Model) tableStart() int {
	start, _, _ := m.tableBlock(m.row)
	return start
}

// deleteRow removes a whole line of a table. back leaves the caret at the end of
// the line above — a backspace walking up and out of the table; otherwise it goes
// to the head of the line that has taken this one's place, so a delete carries on
// through that row from its first cell rather than from wherever the old row's
// caret happened to be (which would leave a cell behind, unreachable by the key
// that was deleting the table).
func (m *Model) deleteRow(row int, back bool) {
	if len(m.lines) == 1 {
		m.lines[0] = []rune{}
		m.row, m.col = 0, 0
		m.afterEdit()
		return
	}
	m.lines = append(m.lines[:row], m.lines[row+1:]...)
	if back && row > 0 {
		m.row = row - 1
		m.col = len(m.lines[m.row])
	} else {
		m.row, m.col = min(row, len(m.lines)-1), 0
	}
	m.afterEdit()
}

// untableRow gives up a row's table formatting, rewriting it as its cell contents
// with a space between them. The row is the first of its table, so what is left is
// plain text with no table above it to be joined back into.
func (m *Model) untableRow(row int) {
	r, ok := parseTableRow(m.lines[row])
	if !ok {
		return
	}
	parts := make([]string, 0, len(r.cells))
	for _, c := range r.cells {
		if s := strings.TrimSpace(c.text); s != "" {
			parts = append(parts, s)
		}
	}
	m.lines[row] = []rune(r.indent + strings.Join(parts, " "))
	m.row = row
	m.col = len([]rune(r.indent))
	m.afterEdit()
}

// cellBounds returns the rune offsets, within a cell's source text, that its content
// runs between — the padding on either side is structure, not text.
func cellBounds(c tableCell) (head, tail int) {
	rs := []rune(c.text)
	head = 0
	for head < len(rs) && rs[head] == ' ' {
		head++
	}
	tail = len(rs)
	for tail > head && rs[tail-1] == ' ' {
		tail--
	}
	return head, tail
}

// blankCells reports whether every cell of a row is empty or spaces.
func blankCells(cells []tableCell) bool {
	for _, c := range cells {
		if strings.TrimSpace(c.text) != "" {
			return false
		}
	}
	return true
}

// openTableRow appends an empty row under the caret's row, squared to the block's
// column count, and parks the caret in its first cell. Under a header that has no
// separator yet it puts the |---| row in first — the row that makes a header a
// header. The new rows are inserted minimally spaced; the realign that every edit
// runs pads them out.
func (m *Model) openTableRow() {
	start, end, ok := m.tableBlock(m.row)
	if !ok {
		return
	}
	row, _ := parseTableRow(m.lines[m.row])
	cols := 0
	hasDelim := false
	for i := start; i < end; i++ {
		r, _ := parseTableRow(m.lines[i])
		cols = max(cols, len(r.cells))
		hasDelim = hasDelim || r.delim
	}

	var b strings.Builder
	if m.row == start && !hasDelim {
		b.WriteString("\n" + row.indent)
		for range cols {
			b.WriteString("| --- ")
		}
		b.WriteString("|")
	}
	b.WriteString("\n" + row.indent)
	for range cols {
		b.WriteString("|  ")
	}
	b.WriteString("|")

	m.col = len(m.lines[m.row]) // never split a row: the break goes at its end
	m.insert([]rune(b.String()))
	m.moveToCell(m.row, 0, true)
	m.afterEdit()
}

// NextTableCell moves the caret one cell along (dir > 0) or back (dir < 0) in the
// table it is in, tidying the cell it leaves. Off the end of the last row it opens
// a fresh row, so a table can be filled in with tab alone — unless that row is one
// it opened and the typist left empty, which is them saying they are done: the row
// goes and the caret lands under the table. Off the front of the first cell it
// steps to the head of the row, which is outside the grid, so the next shift+tab is
// the owner's and cycles focus. Reports whether it moved.
func (m *Model) NextTableCell(dir int) bool {
	if !m.InTableRow() {
		return false
	}
	start, end, ok := m.tableBlock(m.row)
	if !ok {
		return false
	}
	m.alignBlock(start, end)
	row, ok := parseTableRow(m.lines[m.row])
	if !ok {
		return false
	}
	idx, _ := cellAtCol(row, m.col)
	if idx < 0 { // parked past the closing bar: as good as being in the last cell
		idx = len(row.cells) - 1
	}
	next := m.contentRow(m.row, dir, start, end)
	switch {
	case dir > 0 && idx+1 < len(row.cells):
		m.moveToCell(m.row, idx+1, true)
	case dir > 0 && next >= 0:
		m.moveToCell(next, 0, true)
	case dir > 0 && m.row > start && blankCells(row.cells):
		m.endTable() // tabbed to the end of a row left empty: that is the way out
		return true
	case dir > 0:
		m.openTableRow()
		return true
	case dir < 0 && idx > 0:
		m.moveToCell(m.row, idx-1, true)
	case dir < 0 && next >= 0:
		prev, _ := parseTableRow(m.lines[next])
		m.moveToCell(next, len(prev.cells)-1, true)
	default:
		m.col = 0 // the first cell of the first row: step out to the head of the row
	}
	m.afterEdit()
	return true
}

// contentRow returns the row a tab lands on stepping off the end (dir > 0) or the
// front (dir < 0) of row's cells: the next row of the table that isn't the
// separator, whose dashes are drawn from the column widths and so are nothing to
// tab into. -1 when there is none.
func (m *Model) contentRow(row, dir, start, end int) int {
	for r := row + dir; r >= start && r < end; r += dir {
		if pr, ok := parseTableRow(m.lines[r]); ok && !pr.delim {
			return r
		}
	}
	return -1
}

// moveToCell parks the caret in a cell: at the end of its content (atEnd), where
// tabbing into it carries on filling it rather than landing in front of what is
// already there, or at the start. An empty cell takes the caret just past the bar's
// pad space either way, where its content will go.
func (m *Model) moveToCell(row, idx int, atEnd bool) {
	r, ok := parseTableRow(m.lines[row])
	if !ok || idx < 0 || idx >= len(r.cells) {
		return
	}
	c := r.cells[idx]
	head, tail := cellBounds(c)
	switch {
	case head >= tail: // an empty cell
		m.col = c.start + min(1, len([]rune(c.text)))
	case atEnd:
		m.col = c.start + tail
	default:
		m.col = c.start + head
	}
	m.row = row
}

// cellAtCol maps a caret column to the cell it sits in and its rune offset within
// that cell's source text. A caret on a bar belongs to the cell ending there; one
// parked past a closing bar — where the caret rests after a row is typed out — is
// in no cell at all and reports idx < 0, so the realign leaves it at the line's
// end instead of dragging it back into the last cell (which would take the rest of
// the row with it).
func cellAtCol(r tableRow, col int) (idx, off int) {
	for i, c := range r.cells {
		if col >= c.start && col <= c.end {
			return i, col - c.start
		}
	}
	if len(r.cells) > 0 && col < r.cells[0].start {
		return 0, 0 // in the indent or on the opening bar: ride along to the first cell
	}
	return -1, 0
}

// alignBlock re-pads the table rows in [start, end) so every column is as wide as
// its widest cell and the bars line up, rewriting the lines in place and carrying
// the caret along. Widths are display cells (textwidth), so emoji and CJK line up
// like everything else.
//
// When the padded rows would outgrow the editor's content width the padding is
// dropped and the rows are written in their most compact "| a | b |" form — the
// narrowest they can be without cutting into what was typed.
func (m *Model) alignBlock(start, end int) {
	rows := make([]tableRow, 0, end-start)
	for i := start; i < end; i++ {
		r, ok := parseTableRow(m.lines[i])
		if !ok {
			return
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return
	}

	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r.cells))
	}
	aligns := make([]cellAlign, cols)
	hasDelim := false
	for _, r := range rows {
		if !r.delim {
			continue
		}
		hasDelim = true
		copy(aligns, r.aligns)
		break
	}

	// The caret's cell keeps the spaces typed up to the caret: trimming them would
	// eat the space the moment one was typed between two words. It is tidied on the
	// next edit, once the caret has moved on.
	//
	// A caret at the head of the line, ahead of the opening bar, is outside the grid
	// (that is where shift+tab leaves it on the way out of the table) and stays
	// where it is: the indent and the bar in front of it don't move, and dragging it
	// into the first cell would shut the way out.
	caretRow, caretCell, caretOff := -1, -1, 0
	caretHead := false
	if m.row >= start && m.row < end {
		caretRow = m.row - start
		if m.col < rows[caretRow].cells[0].start {
			caretHead = true
		} else {
			caretCell, caretOff = cellAtCol(rows[caretRow], m.col)
		}
	}

	content := make([][]string, len(rows))
	for ri, r := range rows {
		content[ri] = make([]string, cols)
		for ci := 0; ci < cols && ci < len(r.cells); ci++ {
			switch {
			case ri == caretRow && ci == caretCell:
				content[ri][ci], caretOff = caretCellText(r.cells[ci].text, caretOff)
			default:
				content[ri][ci] = strings.TrimSpace(r.cells[ci].text)
			}
		}
	}

	// A caret resting past the closing bar of a row the grid has just squared out
	// with fresh cells steps into the first of them — where the next thing typed
	// belongs. Without this it would sit past the row's new end, and typing there
	// would open a column instead of filling one.
	if caretRow >= 0 && caretCell < 0 && len(rows[caretRow].cells) < cols {
		caretCell, caretOff = len(rows[caretRow].cells), 0
	}

	// A column is as wide as its widest cell. Separator cells get no say — they are
	// dashes stretched to whatever the content needs — but their presence sets a
	// floor, so ":-:" always fits.
	widths := make([]int, cols)
	for ri, r := range rows {
		if r.delim {
			continue
		}
		for ci := 0; ci < cols; ci++ {
			widths[ci] = max(widths[ci], textwidth.Width(content[ri][ci]))
		}
	}
	if hasDelim {
		for ci := range widths {
			widths[ci] = max(widths[ci], minDelimWidth)
		}
	}

	// Each column costs its width plus three cells of chrome ("| " and the trailing
	// space), and the row closes with one more bar.
	budget := m.contentWidth() - textwidth.Width(rows[0].indent) - (3*cols + 1)
	total := 0
	for _, w := range widths {
		total += w
	}

	lines, col := m.buildTableLines(rows, content, widths, aligns, total <= budget, caretRow, caretCell, caretOff)
	if !m.tableFitsLimit(start, end, lines) {
		// Padding cannot cost the message its last characters: fall back to the
		// compact form, and if even that doesn't fit, leave the rows alone.
		lines, col = m.buildTableLines(rows, content, widths, aligns, false, caretRow, caretCell, caretOff)
		if !m.tableFitsLimit(start, end, lines) {
			return
		}
	}
	for i, line := range lines {
		m.lines[start+i] = line
	}
	if caretRow >= 0 && !caretHead {
		m.col = col
	}
}

// buildTableLines renders the block's rows. With pad set, every cell is padded to
// its column's width; without it each cell keeps its own width and the columns
// merely get their single separating space. It returns the lines and the caret's
// new column within its row.
//
// The row the caret is still typing keeps its missing closing bar (see
// tableRow.closed), and its last cell keeps its width — padding a cell the caret
// is about to grow only puts spaces in the typist's way.
func (m *Model) buildTableLines(rows []tableRow, content [][]string, widths []int, aligns []cellAlign, pad bool, caretRow, caretCell, caretOff int) ([][]rune, int) {
	cols := len(widths)
	out := make([][]rune, len(rows))
	caretCol := 0
	for ri, r := range rows {
		open := !r.closed && ri == caretRow
		line := append([]rune(r.indent), '|')
		for ci := 0; ci < cols; ci++ {
			last := ci == cols-1
			line = append(line, ' ')
			cellStart := len(line)
			text, lead := content[ri][ci], 0
			switch {
			case r.delim:
				w := widths[ci]
				if !pad {
					w = minDelimWidth
				}
				text = delimText(aligns[ci], w)
			case pad && !(open && last):
				text, lead = padCellText(text, widths[ci], aligns[ci])
			}
			line = append(line, []rune(text)...)
			if ri == caretRow && ci == caretCell {
				caretCol = cellStart + lead + caretOff
			}
			if open && last {
				break
			}
			line = append(line, ' ', '|')
		}
		if ri == caretRow && caretCell < 0 {
			caretCol = len(line) // parked past the row's closing bar; keep it there
		}
		out[ri] = line
	}
	return out, caretCol
}

// tableFitsLimit reports whether swapping the block's lines for these keeps the
// buffer within CharLimit (which alignment, unlike an insert, would otherwise slip
// past — it adds spaces without going through insert).
func (m *Model) tableFitsLimit(start, end int, lines [][]rune) bool {
	if m.CharLimit <= 0 {
		return true
	}
	delta := 0
	for i := start; i < end; i++ {
		delta -= len(m.lines[i])
	}
	for _, line := range lines {
		delta += len(line)
	}
	return m.length()+delta <= m.CharLimit
}

// caretCellText tidies the cell the caret is in without disturbing what is being
// typed in it: it drops the pad space after the bar and the padding beyond the
// caret, but keeps every space typed up to the caret — otherwise a space typed
// between two words would be swallowed as it was typed.
func caretCellText(text string, off int) (string, int) {
	rs := []rune(text)
	if len(rs) > 0 && rs[0] == ' ' {
		rs = rs[1:]
		off = max(off-1, 0)
	}
	end := len(rs)
	for end > off && rs[end-1] == ' ' {
		end--
	}
	rs = rs[:end]
	off = min(off, len(rs))
	return string(rs), off
}

// padCellText pads text to width display cells under its column's alignment,
// returning the padded text and the pad runes put in front of it — which the caret
// rides along, in a centred or right-aligned column.
func padCellText(s string, width int, a cellAlign) (string, int) {
	gap := width - textwidth.Width(s)
	if gap <= 0 {
		return s, 0
	}
	switch a {
	case alignRight:
		return strings.Repeat(" ", gap) + s, gap
	case alignCenter:
		l := gap / 2
		return strings.Repeat(" ", l) + s + strings.Repeat(" ", gap-l), l
	default:
		return s + strings.Repeat(" ", gap), 0
	}
}

// delimText draws a separator cell: dashes filling width, keeping the colons that
// carry the column's alignment.
func delimText(a cellAlign, width int) string {
	w := max(width, minDelimWidth)
	switch a {
	case alignLeft:
		return ":" + strings.Repeat("-", w-1)
	case alignRight:
		return strings.Repeat("-", w-1) + ":"
	case alignCenter:
		return ":" + strings.Repeat("-", w-2) + ":"
	default:
		return strings.Repeat("-", w)
	}
}
