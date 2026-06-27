package ui

import (
	"os"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// codeColorEnabled reports whether to emit ANSI colour for fenced code blocks.
// We honour NO_COLOR (https://no-color.org); a colourless terminal then gets the
// same flat rendering lipgloss already produces for the rest of the message,
// rather than chroma's raw escapes (chroma bypasses lipgloss' colour profile).
var codeColorEnabled = os.Getenv("NO_COLOR") == ""

// codeHLStyle is the chroma style used for terminal syntax highlighting, with
// every background and border stripped so highlighted code sits on the normal
// pane background (foreground-only) instead of as a shaded block. Built once on
// first use; the Background token's stripping removes the block-level shade.
var codeHLStyle = sync.OnceValue(func() *chroma.Style {
	base := styles.Get("monokai")
	if base == nil {
		base = styles.Fallback
	}
	s, err := base.Builder().Transform(func(e chroma.StyleEntry) chroma.StyleEntry {
		e.Background = 0
		e.Border = 0
		return e
	}).Build()
	if err != nil {
		return base
	}
	return s
})

// highlightCode syntax-highlights a fenced block's body lines for the terminal,
// returning exactly one rendered line per input line. lang is the fence info
// string (may be empty). chroma and all its lexers are already linked (glamour
// pulls them in), so this adds no binary weight. Falls back to flatCodeLines —
// the historical single-colour rendering — when colour is disabled, the language
// is unknown/unset, or chroma errors, so callers always get len(body) lines.
func highlightCode(body []string, lang string) []string {
	if !codeColorEnabled || lang == "" || len(body) == 0 {
		return flatCodeLines(body)
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		return flatCodeLines(body)
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, strings.Join(body, "\n"))
	if err != nil {
		return flatCodeLines(body)
	}
	var buf strings.Builder
	if err := formatters.TTY16m.Format(&buf, codeHLStyle(), it); err != nil {
		return flatCodeLines(body)
	}
	// We joined with "\n", so chroma should emit exactly len(body) lines. If it
	// ever disagrees (e.g. a trailing newline), fall back rather than risk
	// mis-aligning the block against the surrounding rows.
	lines := strings.Split(buf.String(), "\n")
	if len(lines) != len(body) {
		return flatCodeLines(body)
	}
	for i, ln := range lines {
		lines[i] = ensureLineReset(ln)
	}
	return lines
}

// flatCodeLines renders code-block lines in the historical single-colour style,
// each line self-contained (lipgloss resets at the end).
func flatCodeLines(body []string) []string {
	out := make([]string, len(body))
	for i, ln := range body {
		out[i] = mdCodeBlockStyle.Render(ln)
	}
	return out
}

// ensureLineReset guarantees a styled line ends with a full SGR reset so an
// unclosed colour can't bleed into the next viewport row after the ANSI-aware
// soft-wrap in wrapBodyLine splits a long code line.
func ensureLineReset(s string) string {
	if s == "" || strings.HasSuffix(s, "\x1b[0m") {
		return s
	}
	return s + "\x1b[0m"
}
