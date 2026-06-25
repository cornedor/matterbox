package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// tableSample is a small GFM table with mixed alignment used across the tests.
const tableSample = "| Name | Age | City |\n" +
	"| :--- | --: | :-: |\n" +
	"| Alice | 30 | NYC |\n" +
	"| Bob | 7 | LA |"

// renderedTableLines renders a markdown body and returns the laid-out box lines
// for its (single) table, with ANSI escapes stripped for easy assertions.
func renderedTableLines(t *testing.T, body string, width int) []string {
	t.Helper()
	md := renderMarkdown(body, nil, nil, "")
	var enc string
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, tablePrefix) {
			enc = l
			break
		}
	}
	if enc == "" {
		t.Fatalf("no encoded table line in rendered body %q", md)
	}
	tl, ok := tableLines(enc, width)
	if !ok {
		t.Fatalf("tableLines did not recognise the encoded line")
	}
	out := make([]string, len(tl))
	for i, l := range tl {
		out[i] = ansi.Strip(l)
	}
	return out
}

func TestTableRendersBox(t *testing.T) {
	lines := renderedTableLines(t, tableSample, 80)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"┌", "┬", "┐", "├", "┼", "┤", "└", "┴", "┘", "│"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("box char %q missing from:\n%s", want, joined)
		}
	}
	// Header, a separator rule, and both body rows are present.
	for _, want := range []string{"Name", "Age", "City", "Alice", "NYC", "Bob", "LA"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("cell %q missing from:\n%s", want, joined)
		}
	}
	// Header separator sits between the header and the first body row.
	hdr, sep, alice := -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "Name"):
			hdr = i
		case strings.Contains(l, "Alice"):
			if alice < 0 {
				alice = i
			}
		case strings.Contains(l, "├"):
			sep = i
		}
	}
	if !(hdr >= 0 && sep > hdr && alice > sep) {
		t.Fatalf("expected header(%d) < sep(%d) < body(%d)\n%s", hdr, sep, alice, joined)
	}
}

func TestTableEveryLineFitsWidth(t *testing.T) {
	for _, width := range []int{120, 80, 40, 24, 16, 10} {
		lines := renderedTableLines(t, tableSample, width)
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > width {
				t.Fatalf("width=%d: line %q is %d cells wide", width, l, w)
			}
		}
	}
}

func TestTableGutterIndent(t *testing.T) {
	for _, l := range renderedTableLines(t, tableSample, 60) {
		if l != "" && !strings.HasPrefix(l, "  ") {
			t.Fatalf("table line lacks two-space gutter: %q", l)
		}
	}
}

func TestTableShrinksToFit(t *testing.T) {
	// A table with long cells so its natural width exceeds a narrow pane and the
	// shrink-to-fit pass has to kick in.
	body := "| Project | Description |\n" +
		"| --- | --- |\n" +
		"| matterbox | a from-scratch Mattermost terminal client |"
	widthOf := func(lines []string) int {
		w := 0
		for _, l := range lines {
			if n := ansi.StringWidth(l); n > w {
				w = n
			}
		}
		return w
	}
	wide := widthOf(renderedTableLines(t, body, 120))
	narrow := widthOf(renderedTableLines(t, body, 40))
	if narrow >= wide {
		t.Fatalf("narrow table (%d) should be narrower than wide (%d)", narrow, wide)
	}
	if narrow > 40 {
		t.Fatalf("shrunk table (%d) still exceeds the 40-cell pane", narrow)
	}
}

func TestTableAlignment(t *testing.T) {
	// A right-aligned numeric column should carry its padding on the left.
	lines := renderedTableLines(t, tableSample, 80)
	var ageRow string
	for _, l := range lines {
		if strings.Contains(l, "30") {
			ageRow = l
			break
		}
	}
	if ageRow == "" {
		t.Fatal("no row containing the right-aligned value 30")
	}
	// "30" is the widest age; "7" (right-aligned) should be space-padded ahead.
	var bobRow string
	for _, l := range lines {
		if strings.Contains(l, "Bob") {
			bobRow = l
			break
		}
	}
	if !strings.Contains(bobRow, " 7 │") && !strings.Contains(bobRow, "  7 ") {
		t.Fatalf("right-aligned 7 not padded on the left: %q", bobRow)
	}
}

func TestParseTableRejectsNonTables(t *testing.T) {
	cases := []string{
		"just a sentence with a | pipe in it",
		"a | b\nnot a delimiter row",
		"| a | b |\n| not | dashes |",
		"no pipes at all\nsecond line",
	}
	for _, c := range cases {
		md := renderMarkdown(c, nil, nil, "")
		if strings.Contains(md, tablePrefix) {
			t.Fatalf("non-table parsed as table: %q -> %q", c, md)
		}
	}
}

func TestTableStopsAtBlankLine(t *testing.T) {
	body := tableSample + "\n\nafter the table"
	md := renderMarkdown(body, nil, nil, "")
	if !strings.Contains(md, "after the table") {
		t.Fatalf("trailing paragraph swallowed by table: %q", md)
	}
	// The trailing paragraph must not be part of the encoded table line.
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, tablePrefix) && strings.Contains(l, "after the table") {
			t.Fatalf("paragraph leaked into table encoding: %q", l)
		}
	}
}

func TestTableNoColonAlignmentDefaultsLeft(t *testing.T) {
	body := "| a | b |\n| --- | --- |\n| 1 | 2 |"
	lines := renderedTableLines(t, body, 60)
	if len(lines) == 0 {
		t.Fatal("no table lines")
	}
}

func TestTableEscapedPipe(t *testing.T) {
	body := "| col |\n| --- |\n| a \\| b |"
	lines := renderedTableLines(t, body, 60)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "a | b") {
		t.Fatalf("escaped pipe not rendered as literal: %s", joined)
	}
}

func TestExpandTablesLeavesOtherLines(t *testing.T) {
	body := "plain line\n" + tablePrefix + "l" + tableAlignSep + "x"
	got := expandTables(body, 40)
	if !strings.HasPrefix(got, "plain line\n") {
		t.Fatalf("expandTables dropped a non-table line: %q", got)
	}
	if strings.Contains(got, tablePrefix) {
		t.Fatalf("expandTables left an encoded table line: %q", got)
	}
}
