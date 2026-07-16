package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/effects"
)

// headerTitleInline: the title line shows a header's visible text with its
// effect spans marked as sentinels, the payload stripped, folded to one line.
func TestHeaderTitleInline(t *testing.T) {
	wire := compileEffects("\\rainbow{hello}\nsecond line")
	got := headerTitleInline(wire)
	want := string(effStart(effects.Rainbow)) + "hello" + string(effSentinelEnd) + " second line"
	if got != want {
		t.Errorf("headerTitleInline = %q, want %q", got, want)
	}
	if headerTitleInline("plain header") != "plain header" {
		t.Error("a plain header should pass through untouched")
	}
	if headerTitleInline("") != "" {
		t.Error("an empty header should stay empty")
	}
}

// closeEffectSpans: a right-truncation that cut a span's end sentinel off gets
// re-closed, so an open span can't bleed its colour into the rows below the
// title line.
func TestCloseEffectSpans(t *testing.T) {
	balanced := string(effStart(effects.Rainbow)) + "hi" + string(effSentinelEnd) + " there"
	if got := closeEffectSpans(balanced); got != balanced {
		t.Errorf("a balanced line was rewritten: %q", got)
	}
	cut := "x " + string(effStart(effects.Rainbow)) + "h…"
	if got := closeEffectSpans(cut); got != cut+string(effSentinelEnd) {
		t.Errorf("closeEffectSpans(%q) = %q, want one appended end", cut, got)
	}
	nested := string(effStart(effects.Rainbow)) + string(effStart(effects.Underline)) + "x"
	if got := closeEffectSpans(nested); got != nested+strings.Repeat(string(effSentinelEnd), 2) {
		t.Errorf("nested cut = %q, want two appended ends", got)
	}
}

// The channel header rides the messages-pane title line, and its effect spans
// come out painted — through both the split (memoized upper box) and the
// degenerate (too-short terminal) render paths.
func TestTitleLineShowsHeaderWithEffects(t *testing.T) {
	cases := []struct {
		name     string
		vpHeight int // 20 splits the pane; 40 overflows it into the degenerate branch
	}{
		{"split pane", 20},
		{"degenerate pane", 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := navModel()
			m.channels["t1"][0].Header = compileEffects("\\rainbow{welcome} aboard")
			m.msgsView.SetHeight(tc.vpHeight)
			m.refreshEffectsVisibility() // what the per-event kicker does

			title := firstLine(m.renderMessagesPane(38, 80))
			if !strings.Contains(ansi.Strip(title), "welcome aboard") {
				t.Fatalf("title line missing the header text: %q", title)
			}
			if hasEffectSentinel(title) {
				t.Error("effect sentinels left unresolved on the title line")
			}
			// Rainbow paints truecolor SGRs the plain title never carries.
			if !strings.Contains(title, "\x1b[38;2;") {
				t.Error("the header's effect span was not painted")
			}
		})
	}
}

// A title too narrow for the whole header must not leak an open effect span
// into the message rows: the truncated line stays balanced, so nothing below
// the title row picks up its colour.
func TestTruncatedHeaderSpanStaysOnTitleRow(t *testing.T) {
	m := navModel()
	m.channels["t1"][0].Header = compileEffects("\\rainbow{" + strings.Repeat("wide ", 40) + "}")
	m.msgsView.SetHeight(20)
	m.refreshEffectsVisibility()

	pane := m.renderMessagesPane(38, 80)
	lines := strings.Split(pane, "\n")
	for i, l := range lines[1:] {
		if strings.Contains(l, "\x1b[38;2;") {
			t.Fatalf("row %d below the title picked up the header's colour: %q", i+1, l)
		}
	}
}
