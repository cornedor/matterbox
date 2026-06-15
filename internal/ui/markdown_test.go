package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const (
	osc8Open  = "\x1b]8;;"
	osc8Close = "\x1b]8;;\x1b\\"
)

func TestRenderInlineBareURL(t *testing.T) {
	url := "https://example.com/a/b"
	got := renderInline("see "+url+" ok", nil, nil)
	if !strings.Contains(got, osc8Open+url+"\x1b\\") {
		t.Fatalf("missing OSC 8 open for %q in %q", url, got)
	}
	if !strings.Contains(got, osc8Close) {
		t.Fatalf("missing OSC 8 close in %q", got)
	}
	// The visible text (escapes stripped) still reads as the plain message.
	if plain := ansi.Strip(got); plain != "see "+url+" ok" {
		t.Fatalf("visible text changed: %q", plain)
	}
}

func TestRenderInlineMarkdownLink(t *testing.T) {
	got := renderInline("[click here](https://example.com/x)", nil, nil)
	if !strings.Contains(got, osc8Open+"https://example.com/x\x1b\\") {
		t.Fatalf("URL not used as hyperlink target: %q", got)
	}
	if plain := ansi.Strip(got); plain != "click here" {
		t.Fatalf("link should show its text only, got %q", plain)
	}
}

func TestRenderInlineTrailingPunctuation(t *testing.T) {
	got := renderInline("look at https://example.com/a.", nil, nil)
	// The period must fall outside the hyperlink target.
	if !strings.Contains(got, osc8Open+"https://example.com/a\x1b\\") {
		t.Fatalf("trailing period not trimmed from target: %q", got)
	}
	if plain := ansi.Strip(got); plain != "look at https://example.com/a." {
		t.Fatalf("visible text changed: %q", plain)
	}
}

func TestRenderInlineBalancedParensKept(t *testing.T) {
	url := "https://en.wikipedia.org/wiki/Go_(language)"
	got := renderInline(url, nil, nil)
	if !strings.Contains(got, osc8Open+url+"\x1b\\") {
		t.Fatalf("balanced parens dropped from target: %q", got)
	}
}

// A wrapped URL must stay a single hyperlink: the OSC 8 open lands on the
// first visual row and the close on the last, so Ghostty keeps every cell
// in between clickable.
func TestLinkSurvivesBodyWrap(t *testing.T) {
	url := "https://example.com/a/very/long/path/that/will/wrap/across/rows/for/sure"
	line := renderMarkdown(url, nil, nil) // gains the two-space gutter
	rows := wrapBodyLine(line, 30)
	if len(rows) < 2 {
		t.Fatalf("expected the long URL to wrap, got %d row(s)", len(rows))
	}
	joined := strings.Join(rows, "\n")
	if strings.Count(joined, osc8Open) != strings.Count(line, osc8Open) {
		t.Fatalf("OSC 8 open sequences lost during wrap: %q", joined)
	}
	if !strings.Contains(joined, osc8Close) {
		t.Fatalf("OSC 8 close lost during wrap: %q", joined)
	}
}
