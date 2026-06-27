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
	mdCodeBlock                // content inside a ```-fenced block
)

// markdownClasses scans the whole buffer and returns one mdClass per rune of
// Value() (newline slots stay mdNone). It is the parser behind MarkdownHighlight:
// a single left-to-right pass per logical line that mirrors the subset of
// Mattermost markdown the message pane renders — bold/italic/strikethrough,
// inline code, and fenced code blocks — keeping every marker visible. Fenced
// blocks are tracked across lines; inside them no inline markup is recognised
// (matching real markdown, where code suppresses emphasis). Returns nil for an
// empty buffer.
func (m *Model) markdownClasses() []mdClass {
	total := m.length()
	if total == 0 {
		return nil
	}
	cl := make([]mdClass, total)
	inFence := false
	off := 0
	for _, line := range m.lines {
		switch {
		case isFenceLine(line):
			markFenceLine(cl, off, line)
			inFence = !inFence
		case inFence:
			for j := range line {
				cl[off+j] = mdCodeBlock
			}
		default:
			markInline(cl, off, line)
		}
		off += len(line) + 1 // +1 for the newline separator (harmless past the end)
	}
	return cl
}

// isFenceLine reports whether a logical line opens or closes a fenced code block:
// optional leading spaces followed by at least three backticks (a language tag
// may follow, e.g. "```go").
func isFenceLine(line []rune) bool {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	return len(line)-i >= 3 && line[i] == '`' && line[i+1] == '`' && line[i+2] == '`'
}

// markFenceLine dims the whole fence line (the backticks and any language tag)
// as a marker, leaving leading indentation untouched.
func markFenceLine(cl []mdClass, start int, line []rune) {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	for ; i < len(line); i++ {
		cl[start+i] = mdMarker
	}
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
