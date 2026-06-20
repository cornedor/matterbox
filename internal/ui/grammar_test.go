package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/languagetool"
)

// composerTextarea builds a textarea configured like Model.New's composer:
// prompt width 2, no line numbers, given width. Blurred so no cursor cell
// interferes with rendered-line comparisons.
func composerTextarea(value string, width int) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 8
	ta.SetPromptFunc(2, inputPromptFunc("> "))
	ta.SetWidth(width)
	ta.SetValue(value)
	ta.Blur()
	return ta
}

// rightTrim drops trailing spaces — both our wrap and the textarea pad lines.
func rightTrim(s string) string { return strings.TrimRight(s, " ") }

// TestTaWrapMatchesTextarea pins our wrap port against the real textarea's
// rendered line breaks, so a library bump that changes wrapping is caught.
func TestTaWrapMatchesTextarea(t *testing.T) {
	cases := []struct {
		name  string
		value string
		width int
	}{
		{"short", "som sentnce here", 40},
		{"wraps once", "aaaa bbbb sentnce cccc", 12},
		{"wraps several", "the quick brown fox jumps over the lazy dog", 14},
		{"long word", "supercalifragilisticexpialidocious word", 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ta := composerTextarea(tc.value, tc.width)
			// Content wrap width as grammarOverlay derives it.
			rendered := strings.Split(ta.View(), "\n")
			contentWidth := ansi.StringWidth(rendered[0]) - grammarPromptWidth

			subs := taWrap([]rune(tc.value), contentWidth)
			for i, sub := range subs {
				if i >= len(rendered) {
					t.Fatalf("wrap produced %d sub-lines, textarea rendered %d", len(subs), len(rendered))
				}
				// Strip ANSI, drop the 2-col prompt, right-trim padding.
				plain := ansi.Strip(rendered[i])
				if len(plain) >= grammarPromptWidth {
					plain = plain[grammarPromptWidth:]
				}
				if got, want := rightTrim(string(sub)), rightTrim(plain); got != want {
					t.Errorf("sub-line %d: wrap=%q textarea=%q", i, got, want)
				}
			}
		})
	}
}

func TestComputeUnderlineRangesSingleLine(t *testing.T) {
	// "som sentnce": som=[0,3) misspelling, sentnce=[4,11) grammar.
	matches := []languagetool.Match{
		{Offset: 0, Length: 3, IssueType: "misspelling"},
		{Offset: 4, Length: 7, IssueType: "grammar"},
	}
	got := computeUnderlineRanges("som sentnce", 40, grammarPromptWidth, matches)
	want := []underlineRange{
		{line: 0, c0: 2, c1: 5, plain: "som", issueType: "misspelling"},
		{line: 0, c0: 6, c1: 13, plain: "sentnce", issueType: "grammar"},
	}
	assertRanges(t, got, want)
}

func TestComputeUnderlineRangesWrapped(t *testing.T) {
	// At width 10 this wraps to "aaaa bbbb " / "sentnce". The mistake sits at
	// offset 10 on the second visual line.
	value := "aaaa bbbb sentnce"
	matches := []languagetool.Match{{Offset: 10, Length: 7, IssueType: "misspelling"}}
	got := computeUnderlineRanges(value, 10, grammarPromptWidth, matches)
	want := []underlineRange{{line: 1, c0: 2, c1: 9, plain: "sentnce", issueType: "misspelling"}}
	assertRanges(t, got, want)
}

func TestComputeUnderlineRangesSecondLogicalLine(t *testing.T) {
	// A hard newline: the mistake is on the second logical line. Offsets count
	// the newline as one rune, so "bad" starts at offset 6.
	value := "hello\nbad"
	matches := []languagetool.Match{{Offset: 6, Length: 3, IssueType: "misspelling"}}
	got := computeUnderlineRanges(value, 40, grammarPromptWidth, matches)
	want := []underlineRange{{line: 1, c0: 2, c1: 5, plain: "bad", issueType: "misspelling"}}
	assertRanges(t, got, want)
}

// TestOverlayPreservesTextAndAddsUnderline runs the full pipeline the way
// grammarOverlay does: derive ranges, paint them onto the textarea's real
// rendered output, and confirm the visible text is untouched while the flagged
// word gains a coloured curly underline.
func TestOverlayPreservesTextAndAddsUnderline(t *testing.T) {
	value := "som sentnce"
	ta := composerTextarea(value, 40)
	view := ta.View()
	lines := strings.Split(view, "\n")
	contentWidth := ansi.StringWidth(lines[0]) - grammarPromptWidth

	matches := []languagetool.Match{
		{Offset: 0, Length: 3, IssueType: "misspelling"},
		{Offset: 4, Length: 7, IssueType: "grammar"},
	}
	ranges := computeUnderlineRanges(value, contentWidth, grammarPromptWidth, matches)
	base := ta.Styles().Focused.Text.Inherit(ta.Styles().Focused.Base).Inline(true)
	out := applyUnderlineOverlay(append([]string(nil), lines...), ranges, base)

	if got, want := ansi.Strip(out), ansi.Strip(view); got != want {
		t.Fatalf("overlay changed visible text:\n got=%q\nwant=%q", got, want)
	}
	// lipgloss emits curly underline as SGR 4:3 and the underline colour as 58.
	if !strings.Contains(out, "4:3") {
		t.Errorf("overlay missing curly-underline SGR (4:3)")
	}
	if !strings.Contains(out, "58") {
		t.Errorf("overlay missing underline-colour SGR (58)")
	}
}

// TestOverlayCurlyUnderlineSurvivesRender guards the core risk: that the
// underline attribute actually lands on the flagged cells once the terminal
// renderer re-parses the styled string (an inner reset would otherwise cancel
// it). We approximate the renderer by re-styling and checking the curly
// underline is asserted right before the flagged word's first rune.
func TestOverlayNoStrayResetBeforeFlagged(t *testing.T) {
	base := lipgloss.NewStyle()
	lines := []string{"\x1b[40m> som here\x1b[m"}
	ranges := []underlineRange{{line: 0, c0: 2, c1: 5, plain: "som", issueType: "misspelling"}}
	out := applyUnderlineOverlay(lines, ranges, base)

	styledSom := base.UnderlineStyle(lipgloss.UnderlineCurly).
		UnderlineColor(grammarColor("misspelling")).Render("som")
	if !strings.Contains(out, styledSom) {
		t.Errorf("flagged span not present as a single cleanly-styled run\n out=%q\nwant substr=%q", out, styledSom)
	}
}

// grammarModel builds a minimal Model with a populated composer + findings,
// for exercising the cursor/popup/apply state machine.
func grammarModel(value string, matches []languagetool.Match) Model {
	var m Model
	ta := composerTextarea(value, 40)
	ta.Focus()
	m.input = ta
	m.focus = focusInput
	m.lastInputHeight = ta.Height()
	m.grammar = newGrammarState()
	m.grammar.checkedText = value
	m.grammar.matches = matches
	m.ltClient = languagetool.New("http://localhost:8010/v2", "auto", 0)
	return m
}

func TestMatchAtCursor(t *testing.T) {
	matches := []languagetool.Match{
		{Offset: 0, Length: 3, IssueType: "misspelling"}, // som  [0,3]
		{Offset: 4, Length: 7, IssueType: "grammar"},     // sentnce [4,11]
	}
	m := grammarModel("som sentnce", matches)

	m.input.CursorStart()
	if got := m.matchAtCursor(); got != 0 {
		t.Errorf("cursor at start: matchAtCursor=%d, want 0", got)
	}
	m.input.CursorEnd()
	if got := m.matchAtCursor(); got != 1 {
		t.Errorf("cursor at end: matchAtCursor=%d, want 1", got)
	}
	// Stale findings (text moved) report nothing.
	m.grammar.checkedText = "different"
	if got := m.matchAtCursor(); got != -1 {
		t.Errorf("stale findings: matchAtCursor=%d, want -1", got)
	}
}

func TestPopupCycle(t *testing.T) {
	matches := []languagetool.Match{
		{Offset: 0, Length: 3, IssueType: "misspelling"},
		{Offset: 4, Length: 7, IssueType: "grammar"},
	}
	m := grammarModel("som sentnce", matches)
	m.input.CursorStart()

	m.openOrCycleGrammarPopup() // opens on the mistake at the cursor
	if !m.grammar.popup || m.grammar.popupIdx != 0 {
		t.Fatalf("after open: popup=%v idx=%d, want true/0", m.grammar.popup, m.grammar.popupIdx)
	}
	m.openOrCycleGrammarPopup() // already open → advance
	if m.grammar.popupIdx != 1 {
		t.Errorf("after cycle: idx=%d, want 1", m.grammar.popupIdx)
	}
	m.cycleGrammarPopup(1) // wraps
	if m.grammar.popupIdx != 0 {
		t.Errorf("after wrap: idx=%d, want 0", m.grammar.popupIdx)
	}
	m.closeGrammarPopup()
	if m.grammar.popup {
		t.Error("popup should be closed")
	}
}

func TestApplyGrammarSuggestion(t *testing.T) {
	matches := []languagetool.Match{
		{Offset: 0, Length: 3, Replacements: []string{"some"}, IssueType: "misspelling"},
	}
	m := grammarModel("som text", matches)
	m.grammar.popup = true
	m.grammar.popupIdx = 0

	m.applyGrammarSuggestion(0)
	if got := m.input.Value(); got != "some text" {
		t.Errorf("after apply: value=%q, want %q", got, "some text")
	}
	if m.grammar.popup {
		t.Error("popup should close after applying")
	}
	if m.grammar.matches != nil {
		t.Error("findings should be cleared (stale) after applying")
	}
}

func TestApplyGrammarSuggestionOutOfRange(t *testing.T) {
	matches := []languagetool.Match{
		{Offset: 0, Length: 3, Replacements: []string{"some"}, IssueType: "misspelling"},
	}
	m := grammarModel("som text", matches)
	m.grammar.popup = true

	// Digit '2' → index 1, but only one suggestion exists: a no-op, popup stays.
	m.applyGrammarSuggestion(1)
	if got := m.input.Value(); got != "som text" {
		t.Errorf("out-of-range apply changed text to %q", got)
	}
	if !m.grammar.popup {
		t.Error("popup should stay open on a no-op digit")
	}
}

func assertRanges(t *testing.T, got, want []underlineRange) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d ranges, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
