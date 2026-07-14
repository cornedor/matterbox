package editor

import (
	"strconv"
	"strings"
	"unicode"
)

// listItem describes the markdown list marker a logical line starts with.
type listItem struct {
	indent string // leading whitespace, repeated on the continuation line
	bullet rune   // '-', '*' or '+' for a bullet list; 0 for an ordered one
	num    int    // the number of an ordered item
	delim  rune   // the '.' or ')' after that number
	task   bool   // the item carries a "[ ]" / "[x]" checkbox
	// body is the rune index where the item's content starts (past the marker,
	// and past the checkbox for a task item).
	body int
}

// next returns the marker to open the following item with: the same indent and
// bullet, or the next number for an ordered list. A checked or unchecked task
// item continues with a fresh unchecked box.
func (it listItem) next() string {
	var b strings.Builder
	b.WriteString(it.indent)
	if it.bullet != 0 {
		b.WriteRune(it.bullet)
	} else {
		b.WriteString(strconv.Itoa(it.num + 1))
		b.WriteRune(it.delim)
	}
	b.WriteByte(' ')
	if it.task {
		b.WriteString("[ ] ")
	}
	return b.String()
}

// continueList handles a newline pressed inside a markdown list item: the new
// line opens with the same marker ("- x" ⏎ → "- ", "1. x" ⏎ → "2. "). Pressing
// it again on the still-empty item ends the list instead, stripping the marker.
// Reports whether it handled the key; false means insert a plain line break.
func (m *Model) continueList() bool {
	if m.InCodeBlock() {
		return false
	}
	line := m.lines[m.row]
	it, ok := parseListItem(line)
	// A caret inside the marker itself splits the line like any other text.
	if !ok || m.col < it.body {
		return false
	}
	if isBlankLine(line[it.body:]) {
		m.lines[m.row] = []rune{}
		m.col = 0
		m.afterEdit()
		return true
	}
	m.insert([]rune("\n" + it.next()))
	return true
}

// parseListItem reads the leading list marker of a line: an indent, a bullet
// ('-', '*', '+') or a number followed by '.' or ')', then a space, then an
// optional task checkbox. The trailing space is what keeps "*bold*", "1.5" and
// "--" from parsing as list items.
func parseListItem(line []rune) (listItem, bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	it := listItem{indent: string(line[:i])}
	switch {
	case i < len(line) && (line[i] == '-' || line[i] == '*' || line[i] == '+'):
		it.bullet = line[i]
		i++
	default:
		start := i
		for i < len(line) && unicode.IsDigit(line[i]) {
			i++
		}
		// Cap the digits so a pasted wall of them can't overflow the counter.
		if i == start || i-start > 9 {
			return listItem{}, false
		}
		if i >= len(line) || (line[i] != '.' && line[i] != ')') {
			return listItem{}, false
		}
		n, err := strconv.Atoi(string(line[start:i]))
		if err != nil {
			return listItem{}, false
		}
		it.num, it.delim = n, line[i]
		i++
	}
	if i >= len(line) || line[i] != ' ' {
		return listItem{}, false
	}
	i++
	it.body = i
	if cb := parseCheckbox(line, i); cb > 0 {
		it.task = true
		it.body = cb
	}
	return it, true
}

// parseCheckbox matches a "[ ]" / "[x]" task marker at i, followed by a space or
// the line end. It returns the index past the marker (and its space), or 0.
func parseCheckbox(line []rune, i int) int {
	if i+2 >= len(line) || line[i] != '[' || line[i+2] != ']' {
		return 0
	}
	switch line[i+1] {
	case ' ', 'x', 'X':
	default:
		return 0
	}
	switch {
	case i+3 >= len(line):
		return i + 3
	case line[i+3] == ' ':
		return i + 4
	}
	return 0
}
