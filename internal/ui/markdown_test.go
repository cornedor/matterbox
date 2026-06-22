package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	osc8Open  = "\x1b]8;;"
	osc8Close = "\x1b]8;;\x1b\\"
)

func TestRenderInlineBareURL(t *testing.T) {
	url := "https://example.com/a/b"
	got := renderInline("see "+url+" ok", nil, nil, "")
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
	got := renderInline("[click here](https://example.com/x)", nil, nil, "")
	if !strings.Contains(got, osc8Open+"https://example.com/x\x1b\\") {
		t.Fatalf("URL not used as hyperlink target: %q", got)
	}
	if plain := ansi.Strip(got); plain != "click here" {
		t.Fatalf("link should show its text only, got %q", plain)
	}
}

func TestRenderInlineTrailingPunctuation(t *testing.T) {
	got := renderInline("look at https://example.com/a.", nil, nil, "")
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
	got := renderInline(url, nil, nil, "")
	if !strings.Contains(got, osc8Open+url+"\x1b\\") {
		t.Fatalf("balanced parens dropped from target: %q", got)
	}
}

// Underscore emphasis must render like its asterisk twin: _x_ italic, __x__
// bold. Mattermost accepts both delimiter families.
func TestRenderInlineUnderscoreEmphasis(t *testing.T) {
	tests := []struct {
		name, in, want string
		style          lipgloss.Style
	}{
		{"italic", "say _hello_ now", "hello", mdItalicStyle},
		{"bold", "say __hello__ now", "hello", mdBoldStyle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderInline(tt.in, nil, nil, "")
			if want := tt.style.Render(tt.want); !strings.Contains(got, want) {
				t.Fatalf("renderInline(%q) = %q, want styled %q", tt.in, got, want)
			}
			// The delimiters must be gone from the visible text.
			if plain := ansi.Strip(got); strings.Contains(plain, "_") {
				t.Fatalf("underscore delimiters left in visible text: %q", plain)
			}
		})
	}
}

// The original bug: a whole line wrapped in _..._ (with inline code inside)
// left the underscores literal. It must now italicise and keep the code spans.
func TestRenderInlineUnderscoreSpanWithCode(t *testing.T) {
	got := renderInline("_Type `go north` to begin._", nil, nil, "")
	if plain := ansi.Strip(got); strings.Contains(plain, "_") {
		t.Fatalf("underscores not consumed: %q", plain)
	}
	if want := mdCodeStyle.Render("go north"); !strings.Contains(got, want) {
		t.Fatalf("code span inside italic lost styling: %q", got)
	}
}

// Underscores inside identifiers must NOT become emphasis — the CommonMark
// intraword rule, which the \b guards enforce.
func TestRenderInlineUnderscoreIntrawordUntouched(t *testing.T) {
	for _, in := range []string{"call snake_case_fn here", "use a_b_c value"} {
		got := ansi.Strip(renderInline(in, nil, nil, ""))
		if got != in {
			t.Fatalf("intraword underscores altered: renderInline(%q) -> %q", in, got)
		}
	}
}

// Two adjacent italic spans on one line must both render — \b is zero-width, so
// the space between them is not consumed by the first match.
func TestRenderInlineUnderscoreAdjacentSpans(t *testing.T) {
	got := renderInline("go _north_ or _south_", nil, nil, "")
	for _, w := range []string{"north", "south"} {
		if want := mdItalicStyle.Render(w); !strings.Contains(got, want) {
			t.Fatalf("adjacent span %q not italicised in %q", w, got)
		}
	}
}

// All three CommonMark code-block forms — ``` fences, ~~~ fences, and
// four-space indented blocks — must render with the code-block style.
func TestRenderMarkdownCodeBlockForms(t *testing.T) {
	wantLine := "  " + mdCodeBlockStyle.Render("code here")
	tests := []struct {
		name string
		msg  string
	}{
		{"backtick fence", "```js\ncode here\n```"},
		{"tilde fence", "~~~\ncode here\n~~~"},
		{"indented block", "intro\n\n    code here"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderMarkdown(tt.msg, nil, nil, "")
			if !strings.Contains(got, wantLine) {
				t.Errorf("renderMarkdown(%q) = %q, want a line %q", tt.msg, got, wantLine)
			}
		})
	}
}

// A backtick line inside a ~~~ fence is content, not a closing fence, so it
// must keep the code-block style rather than the dim fence style.
func TestRenderMarkdownBacktickInsideTildeFence(t *testing.T) {
	got := renderMarkdown("~~~\n```\n~~~", nil, nil, "")
	if want := "  " + mdCodeBlockStyle.Render("```"); !strings.Contains(got, want) {
		t.Errorf("``` inside ~~~ not rendered as content: %q", got)
	}
}

// A wrapped URL must stay a single hyperlink: the OSC 8 open lands on the
// first visual row and the close on the last, so Ghostty keeps every cell
// in between clickable.
func TestLinkSurvivesBodyWrap(t *testing.T) {
	url := "https://example.com/a/very/long/path/that/will/wrap/across/rows/for/sure"
	line := renderMarkdown(url, nil, nil, "") // gains the two-space gutter
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
