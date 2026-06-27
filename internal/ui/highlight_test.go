package ui

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

// equalSlices is a tiny helper so the fallback tests can assert highlightCode
// returned byte-for-byte what flatCodeLines would have.
func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// An unknown fence language has no chroma lexer, so the block must fall back to
// the flat single-colour rendering rather than erroring or dropping lines.
func TestHighlightCodeUnknownLanguageFallsBack(t *testing.T) {
	body := []string{"some content", "more"}
	got := highlightCode(body, "not-a-real-language-xyz")
	if want := flatCodeLines(body); !equalSlices(got, want) {
		t.Errorf("unknown language: got %q, want flat %q", got, want)
	}
}

// An empty info string (a bare ``` fence) also takes the flat path — we only
// highlight when the author named a language.
func TestHighlightCodeEmptyLanguageFallsBack(t *testing.T) {
	body := []string{"x = 1"}
	got := highlightCode(body, "")
	if want := flatCodeLines(body); !equalSlices(got, want) {
		t.Errorf("empty language: got %q, want flat %q", got, want)
	}
}

// NO_COLOR (codeColorEnabled=false) disables highlighting entirely, so even a
// known language renders flat — matching how the rest of the UI degrades.
func TestHighlightCodeColorDisabledFallsBack(t *testing.T) {
	defer func(prev bool) { codeColorEnabled = prev }(codeColorEnabled)
	codeColorEnabled = false
	body := []string{"func main() {}"}
	got := highlightCode(body, "go")
	if want := flatCodeLines(body); !equalSlices(got, want) {
		t.Errorf("colour disabled: got %q, want flat %q", got, want)
	}
}

// An empty block returns an empty slice (not a one-element slice with a blank
// styled line) so callers append nothing.
func TestHighlightCodeEmptyBody(t *testing.T) {
	if got := highlightCode(nil, "go"); len(got) != 0 {
		t.Errorf("empty body: got %d lines, want 0", len(got))
	}
}

// The foreground-only guarantee: highlighted output may set foreground colours
// (and bold/italic) but must never emit a background-colour SGR, so code never
// renders as a shaded block. Includes an Error token (chroma's clearBackground
// leaves per-token backgrounds; our style Transform is what strips them).
func TestHighlightCodeEmitsNoBackground(t *testing.T) {
	if !codeColorEnabled {
		t.Skip("colour disabled (NO_COLOR)")
	}
	body := []string{
		`func main() { fmt.Println("hi") }`,
		"\x07 // an invalid byte yields an Error token",
	}
	for _, ln := range highlightCode(body, "go") {
		if strings.Contains(ln, "\x1b[48") { // any background SGR (48;2;… truecolor)
			t.Errorf("highlighted line carries a background SGR: %q", ln)
		}
	}
}

// Highlighting must preserve the block's shape: exactly one output line per
// input line (blank lines included), and stripping the ANSI must reproduce the
// source verbatim — no characters added, dropped, or reordered. This is what
// keeps a highlighted block aligned with the surrounding message rows.
func TestHighlightCodePreservesLinesAndText(t *testing.T) {
	if !codeColorEnabled {
		t.Skip("colour disabled (NO_COLOR)")
	}
	body := []string{
		"package main",
		"",
		"func add(a, b int) int {",
		"\treturn a + b",
		"}",
	}
	got := highlightCode(body, "go")
	if len(got) != len(body) {
		t.Fatalf("line count: got %d, want %d (%q)", len(got), len(body), got)
	}
	for i, ln := range got {
		if plain := ansi.Strip(ln); plain != body[i] {
			t.Errorf("line %d text changed: got %q, want %q", i, plain, body[i])
		}
	}
}

// Every highlighted line ends with a reset so colour can't bleed into the next
// viewport row once wrapBodyLine soft-wraps a long line.
func TestHighlightCodeLinesEndReset(t *testing.T) {
	if !codeColorEnabled {
		t.Skip("colour disabled (NO_COLOR)")
	}
	for _, ln := range highlightCode([]string{"const x = 42"}, "go") {
		if ln != "" && !strings.HasSuffix(ln, "\x1b[0m") {
			t.Errorf("highlighted line not reset-terminated: %q", ln)
		}
	}
}

// The bundled everforest-dark style must be registered with chroma so it can be
// selected by `code_theme: everforest-dark` like any built-in.
func TestEverforestRegistered(t *testing.T) {
	if _, ok := styles.Registry["everforest-dark"]; !ok {
		t.Fatal("everforest-dark not registered with chroma")
	}
}

// setCodeTheme switches the active palette: an everforest theme recolours tokens
// with everforest accents (and not monokai's), proving config actually drives
// the highlighter. Restores the default theme so other tests are unaffected.
func TestSetCodeThemeSwitchesPalette(t *testing.T) {
	if !codeColorEnabled {
		t.Skip("colour disabled (NO_COLOR)")
	}
	defer setCodeTheme(fallbackCodeTheme)

	setCodeTheme("everforest-dark")
	out := strings.Join(highlightCode([]string{`const s = "hi"`}, "go"), "\n")
	const everforestGreen = "\x1b[38;2;167;192;128m" // #a7c080 — everforest string
	const monokaiYellow = "\x1b[38;2;230;219;116m"   // #e6db74 — monokai string
	if !strings.Contains(out, everforestGreen) {
		t.Errorf("everforest string colour not applied: %q", out)
	}
	if strings.Contains(out, monokaiYellow) {
		t.Errorf("monokai colour leaked after switching to everforest: %q", out)
	}
}

// An unknown code_theme name falls back to the default rather than chroma's
// near-colourless swapoff Fallback, so a typo can't silently kill highlighting.
func TestSetCodeThemeUnknownFallsBackToDefault(t *testing.T) {
	defer setCodeTheme(fallbackCodeTheme)
	setCodeTheme("no-such-theme-zzz")
	if got := codeHLStyle().Name; got != fallbackCodeTheme {
		t.Errorf("unknown theme resolved to %q; want %q", got, fallbackCodeTheme)
	}
}

// Background-stripping holds for the bundled style too: everforest highlighting
// is foreground-only, never a shaded block.
func TestEverforestEmitsNoBackground(t *testing.T) {
	if !codeColorEnabled {
		t.Skip("colour disabled (NO_COLOR)")
	}
	defer setCodeTheme(fallbackCodeTheme)
	setCodeTheme("everforest-dark")
	for _, ln := range highlightCode([]string{`x := "s"`, "\x07"}, "go") {
		if strings.Contains(ln, "\x1b[48") {
			t.Errorf("everforest line carries a background SGR: %q", ln)
		}
	}
}

func TestEnsureLineReset(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty stays empty", "", ""},
		{"adds missing reset", "\x1b[38;2;1;2;3mhi", "\x1b[38;2;1;2;3mhi\x1b[0m"},
		{"no double reset", "hi\x1b[0m", "hi\x1b[0m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ensureLineReset(tt.in); got != tt.want {
				t.Errorf("ensureLineReset(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
