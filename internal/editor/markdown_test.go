package editor

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
)

// classRune maps an mdClass to a compact symbol for table-driven assertions:
// '.' none, 'm' marker, 'B' bold, 'i' italic, 's' strike, 'c' code, 'C' block.
func classRune(c mdClass) byte {
	switch c {
	case mdMarker:
		return 'm'
	case mdBold:
		return 'B'
	case mdItalic:
		return 'i'
	case mdStrike:
		return 's'
	case mdCode:
		return 'c'
	case mdCodeBlock:
		return 'C'
	default:
		return '.'
	}
}

func classString(value string) string {
	m := New()
	m.SetValue(value)
	cl := m.markdownClasses()
	b := make([]byte, len(cl))
	for i, c := range cl {
		b[i] = classRune(c)
	}
	return string(b)
}

func TestMarkdownClassesInline(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"a *b* c", "..mim.."}, // italic
		{"**b**", "mmBmm"},     // bold
		{"~~x~~", "mmsmm"},     // strikethrough
		{"`x`", "mcm"},         // inline code
		{"__b__", "mmBmm"},     // bold via underscores
		{"_b_", "mim"},         // italic via underscores
		{"`*x*`", "mcccm"},     // code suppresses emphasis inside it
		{"*abc", "...."},       // unterminated marker stays plain
		{"a_b_c", "....."},     // intraword underscores ignored (snake_case)
		{"snake_case", ".........."},
		{"__init__", "mmBBBBmm"},     // space/edge-flanked dunder bolds (CommonMark)
		{"foo__bar__", ".........."}, // but truly intraword underscores stay literal
		{"é*b*", ".mim"},             // multibyte head, rune-addressed offsets
	}
	for _, c := range cases {
		if got := classString(c.in); got != c.want {
			t.Errorf("classString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMarkdownClassesFencedBlock(t *testing.T) {
	const src = "text\n```go\ncode line\n```\ndone"
	// text + nl, ```go fence, nl, code content, nl, ``` fence, nl, done.
	const want = ".....mmmmm.CCCCCCCCC.mmm....."
	if got := classString(src); got != want {
		t.Fatalf("fenced classes = %q, want %q", got, want)
	}
}

func TestMarkdownHighlightKeepsMarkersVisible(t *testing.T) {
	m := newTestModel(40)
	m.Blur() // keep the cursor cell out of the way
	m.MarkdownHighlight = true
	m.SetValue("a **bold** b")

	view := m.View()
	if got := strings.TrimRight(ansi.Strip(strings.Split(view, "\n")[0]), " "); got != "a **bold** b" {
		t.Fatalf("markers not kept visible: %q", got)
	}
	// The enclosed word carries the bold attribute (its run is styled exactly as a
	// standalone bold render of "bold").
	wantBold := lipgloss.NewStyle().Bold(true).Inline(true).Render("bold")
	if !strings.Contains(view, wantBold) {
		t.Fatalf("expected bold-styled content in view:\n%q", view)
	}
}

func TestMarkdownHighlightOffByDefault(t *testing.T) {
	m := newTestModel(40)
	m.Blur()
	m.SetValue("a **bold** b")
	// With an empty text style and no highlighting, nothing emits SGR sequences.
	if strings.ContainsRune(m.View(), 0x1b) {
		t.Fatalf("unexpected styling without MarkdownHighlight:\n%q", m.View())
	}
}

// TestMarkdownComposesWithDecorations checks that a grammar-style underline and
// a markdown bold span over the same word both survive into the output.
func TestMarkdownComposesWithDecorations(t *testing.T) {
	m := newTestModel(40)
	m.Blur()
	m.MarkdownHighlight = true
	m.SetValue("**bad**")
	// Underline the inner "bad" (offsets 2..5).
	m.SetDecorations([]Decoration{curlyDecor(2, 5)})

	view := m.View()
	// The decoration's underline survives...
	if got := underlineRanges(view); len(got) != 1 || got[0] != "bad" {
		t.Fatalf("underlined = %v, want [bad]", got)
	}
	// ...and the markers stay on screen.
	if got := strings.TrimRight(ansi.Strip(strings.Split(view, "\n")[0]), " "); got != "**bad**" {
		t.Fatalf("markers not kept visible: %q", got)
	}
}
