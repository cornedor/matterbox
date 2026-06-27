package editor

import "unicode"

// mdClass tags a rune with the markdown role it plays, so View can paint markers
// and content differently. Offsets are into Value() (newlines counted), matching
// the editor's other rune-offset coordinates (decorations, lineStartOffset).
type mdClass uint8

const (
	mdNone      mdClass = iota // ordinary text
	mdMarker                   // a syntax token: * _ ~ ` or a ``` fence line
	mdBold                     // **x** / __x__ content
	mdItalic                   // *x* / _x_ content
	mdStrike                   // ~~x~~ content
	mdCode                     // `x` inline-code content
	mdCodeBlock                // content inside a fenced (``` / ~~~) or indented block
)

// markdownClasses scans the whole buffer and returns one mdClass per rune of
// Value() (newline slots stay mdNone). It is the parser behind MarkdownHighlight:
// a single left-to-right pass per logical line that mirrors the subset of
// Mattermost markdown the message pane renders — bold/italic/strikethrough,
// inline code, and code blocks — keeping every marker visible.
//
// Code blocks come in two forms, both tracked across lines (inside them no
// inline markup is recognised, matching real markdown where code suppresses
// emphasis):
//   - Fenced: a ``` or ~~~ run of three or more, indented less than four spaces.
//     A block closes only on a matching fence (same char, at least as long, with
//     nothing but spaces after) — so a ~~~ inside a ``` block stays content.
//   - Indented: a run of lines indented four or more columns. Per CommonMark it
//     can't interrupt a paragraph, so the first such line must follow a blank
//     line (or the buffer start); otherwise it's a lazy paragraph continuation.
//
// Returns nil for an empty buffer.
func (m *Model) markdownClasses() []mdClass {
	total := m.length()
	if total == 0 {
		return nil
	}
	cl := make([]mdClass, total)
	var fenceChar rune // 0 when not inside a fenced block
	var fenceLen int
	inIndent := false // inside an indented code block
	prevBlank := true // buffer start is a block boundary (lets indented code begin)
	off := 0
	for _, line := range m.lines {
		ch, n, restBlank := fenceInfo(line)
		switch {
		case fenceChar != 0:
			// Inside a fenced block: only a matching closing fence ends it.
			if ch == fenceChar && n >= fenceLen && restBlank {
				markFenceLine(cl, off, line)
				fenceChar, fenceLen = 0, 0
			} else {
				fillCodeBlock(cl, off, line)
			}
			prevBlank = false
		case ch != 0 && indentColumns(line) < 4:
			markFenceLine(cl, off, line)
			fenceChar, fenceLen = ch, n
			inIndent, prevBlank = false, false
		case isBlankLine(line):
			inIndent, prevBlank = false, true
		case indentColumns(line) >= 4 && (inIndent || prevBlank):
			fillCodeBlock(cl, off, line)
			inIndent, prevBlank = true, false
		default:
			markInline(cl, off, line)
			inIndent, prevBlank = false, false
		}
		off += len(line) + 1 // +1 for the newline separator (harmless past the end)
	}
	return cl
}

// InCodeBlock reports whether the caret currently sits inside a fenced (``` /
// ~~~) or indented code block. Callers use it to suppress markup-aware paste
// handling (e.g. auto-formatting a pasted table) so raw text dropped into code
// is kept verbatim. It mirrors the block tracking in markdownClasses, but stops
// at the caret's row and reports the state the row is entered in.
func (m *Model) InCodeBlock() bool {
	var fenceChar rune
	var fenceLen int
	inIndent := false
	prevBlank := true // buffer start is a block boundary
	for i, line := range m.lines {
		if i == m.row {
			return fenceChar != 0 || ((inIndent || prevBlank) && indentColumns(line) >= 4)
		}
		ch, n, restBlank := fenceInfo(line)
		switch {
		case fenceChar != 0:
			if ch == fenceChar && n >= fenceLen && restBlank {
				fenceChar, fenceLen = 0, 0
			}
			prevBlank = false
		case ch != 0 && indentColumns(line) < 4:
			fenceChar, fenceLen = ch, n
			inIndent, prevBlank = false, false
		case isBlankLine(line):
			inIndent, prevBlank = false, true
		case indentColumns(line) >= 4 && (inIndent || prevBlank):
			inIndent, prevBlank = true, false
		default:
			inIndent, prevBlank = false, false
		}
	}
	return fenceChar != 0
}

// fenceInfo inspects a line as a possible code fence: after up to its leading
// whitespace, a run of three or more backticks or tildes. It returns the fence
// rune (0 if the line isn't a fence), the run length, and whether everything
// after the run is blank (an info string disqualifies a closing fence).
func fenceInfo(line []rune) (ch rune, length int, restBlank bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return 0, 0, false
	}
	c := line[i]
	n := 0
	for i < len(line) && line[i] == c {
		i++
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	for ; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' {
			return c, n, false
		}
	}
	return c, n, true
}

// markFenceLine dims the whole fence line (the delimiters and any language tag)
// as a marker, leaving leading indentation untouched.
func markFenceLine(cl []mdClass, start int, line []rune) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	for ; i < len(line); i++ {
		cl[start+i] = mdMarker
	}
}

// fillCodeBlock classifies an entire line as code-block content.
func fillCodeBlock(cl []mdClass, start int, line []rune) {
	for j := range line {
		cl[start+j] = mdCodeBlock
	}
}

// indentColumns counts the leading-whitespace columns of a line, with a tab
// advancing to the next multiple of four (input typed into the editor is
// tab-expanded, but SetValue can carry tabs through). Stops at the first
// non-whitespace rune; an all-whitespace line returns its full width.
func indentColumns(line []rune) int {
	col := 0
	for _, r := range line {
		switch r {
		case ' ':
			col++
		case '\t':
			col += 4 - col%4
		default:
			return col
		}
	}
	return col
}

// isBlankLine reports whether a line is empty or only whitespace.
func isBlankLine(line []rune) bool {
	for _, r := range line {
		if r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

// markInline classifies one logical line's runes in place. start is the absolute
// rune offset of line[0] within Value(). The first delimiter wins: a span
// consumes its content (so a code span suppresses emphasis inside it, and an
// unterminated delimiter is left as plain text).
func markInline(cl []mdClass, start int, line []rune) {
	n := len(line)
	set := func(a, b int, c mdClass) { // half-open [a, b)
		for k := a; k < b; k++ {
			cl[start+k] = c
		}
	}
	// span marks an emphasis run with markerLen-rune delimiters on each side:
	// markers at [open, open+markerLen) and [close, close+markerLen), content
	// (class c) between. Returns the index just past the closing delimiter.
	span := func(open, close, markerLen int, c mdClass) int {
		set(open, open+markerLen, mdMarker)
		set(open+markerLen, close, c)
		set(close, close+markerLen, mdMarker)
		return close + markerLen
	}
	i := 0
	for i < n {
		switch {
		case line[i] == '`':
			// Inline code: closes at the next backtick, content non-empty.
			if j := nextRune(line, i+1, '`'); j > i+1 {
				i = span(i, j, 1, mdCode)
				continue
			}
		case line[i] == '*' && i+1 < n && line[i+1] == '*':
			if j := nextDouble(line, i+2, '*'); j > i+2 {
				i = span(i, j, 2, mdBold)
				continue
			}
		case line[i] == '*':
			// Single-asterisk italic: content can't start with a space.
			if j := nextRune(line, i+1, '*'); j > i+1 && !unicode.IsSpace(line[i+1]) {
				i = span(i, j, 1, mdItalic)
				continue
			}
		case line[i] == '~' && i+1 < n && line[i+1] == '~':
			if j := nextDouble(line, i+2, '~'); j > i+2 {
				i = span(i, j, 2, mdStrike)
				continue
			}
		case line[i] == '_' && i+1 < n && line[i+1] == '_':
			// Bold underscore — only at word boundaries, so intraword underscores
			// like foo__bar__ and snake_case stay literal (mirrors the message-pane
			// \b__ … __\b rule, and CommonMark). Space-flanked __init__ does bold,
			// which is exactly how Mattermost renders it.
			if openBoundary(line, i) {
				if j := nextDouble(line, i+2, '_'); j > i+2 && closeBoundary(line, j+2) {
					i = span(i, j, 2, mdBold)
					continue
				}
			}
		case line[i] == '_':
			if openBoundary(line, i) {
				if j := nextRune(line, i+1, '_'); j > i+1 && !unicode.IsSpace(line[i+1]) && closeBoundary(line, j+1) {
					i = span(i, j, 1, mdItalic)
					continue
				}
			}
		}
		i++
	}
}

// nextRune returns the index of the first ch at or after from, or -1.
func nextRune(line []rune, from int, ch rune) int {
	for k := from; k < len(line); k++ {
		if line[k] == ch {
			return k
		}
	}
	return -1
}

// nextDouble returns the index of the first doubled ch (ch immediately followed
// by ch) at or after from, or -1.
func nextDouble(line []rune, from int, ch rune) int {
	for k := from; k+1 < len(line); k++ {
		if line[k] == ch && line[k+1] == ch {
			return k
		}
	}
	return -1
}

// openBoundary reports whether an underscore delimiter starting at i is preceded
// by a non-word rune (or the line start) — the left half of CommonMark's no
// intraword-underscore rule.
func openBoundary(line []rune, i int) bool {
	return i == 0 || !isWordRune(line[i-1])
}

// closeBoundary reports whether the rune at after (just past a closing underscore
// delimiter) is a non-word rune or the line end — the right half of the rule.
func closeBoundary(line []rune, after int) bool {
	return after >= len(line) || !isWordRune(line[after])
}

// isWordRune matches the characters a word boundary is measured against: letters,
// digits, and underscore.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
