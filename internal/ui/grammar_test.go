package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/editor"
	"matterbox/internal/languagetool"
)

// composerEditor builds an editor configured like Model.New's composer: prompt
// width 2, given width, blurred so no cursor cell interferes with rendered-line
// comparisons.
func composerEditor(value string, width int) editor.Model {
	ta := editor.New()
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 8
	ta.SetPromptFunc(2, inputPromptFunc("> "))
	ta.SetWidth(width)
	ta.SetValue(value)
	ta.Blur()
	return ta
}

// grammarModel builds a minimal Model with a populated composer + findings,
// for exercising the cursor/popup/apply state machine.
func grammarModel(value string, matches []languagetool.Match) Model {
	var m Model
	ta := composerEditor(value, 40)
	ta.Focus()
	m.input = ta
	m.focus = focusInput
	m.lastInputHeight = ta.Height()
	m.keys = newKeyMap("ctrl")
	m.grammar = newGrammarState()
	m.grammar.checkedText = value
	m.grammar.matches = matches
	m.ltClient = languagetool.New("http://localhost:8010/v2", "auto", false, 0)
	return m
}

// TestSyncGrammarDecorations checks the wiring from findings to the editor's
// inline decorations: each match becomes a [offset,offset+length) span, and the
// decorations clear when the findings go stale against the live draft.
func TestSyncGrammarDecorations(t *testing.T) {
	matches := []languagetool.Match{
		{Offset: 0, Length: 3, IssueType: "misspelling"},
		{Offset: 4, Length: 7, IssueType: "grammar"},
	}
	m := grammarModel("som sentnce", matches)
	m.syncComposerDecorations()

	decos := m.input.Decorations()
	if len(decos) != 2 {
		t.Fatalf("got %d decorations, want 2", len(decos))
	}
	if decos[0].Start != 0 || decos[0].End != 3 {
		t.Errorf("decoration 0 = [%d,%d), want [0,3)", decos[0].Start, decos[0].End)
	}
	if decos[1].Start != 4 || decos[1].End != 11 {
		t.Errorf("decoration 1 = [%d,%d), want [4,11)", decos[1].Start, decos[1].End)
	}

	// Findings tied to a draft that no longer matches the live value must not
	// paint: simulate the draft moving under the findings.
	m.grammar.checkedText = "different"
	m.syncComposerDecorations()
	if got := len(m.input.Decorations()); got != 0 {
		t.Errorf("stale findings left %d decorations, want 0", got)
	}
}

// TestClearGrammarDropsDecorations confirms clearing findings also clears the
// editor's underlines (e.g. when the draft is sent).
func TestClearGrammarDropsDecorations(t *testing.T) {
	matches := []languagetool.Match{{Offset: 0, Length: 3, IssueType: "misspelling"}}
	m := grammarModel("som text", matches)
	m.syncComposerDecorations()
	if len(m.input.Decorations()) == 0 {
		t.Fatal("expected decorations before clear")
	}
	m.clearGrammar()
	if got := len(m.input.Decorations()); got != 0 {
		t.Errorf("clearGrammar left %d decorations, want 0", got)
	}
}

// underlinedSpans returns the substrings carrying a curly underline across all
// rendered lines of s. lipgloss emits the underline per-rune, so adjacent
// underlined runes are merged: a span only closes on a visible un-underlined
// character.
func underlinedSpans(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		var cur strings.Builder
		on, i := false, 0
		for i < len(line) {
			if line[i] == 0x1b {
				j := strings.IndexByte(line[i:], 'm')
				if j < 0 {
					break
				}
				params := line[i+2 : i+j]
				switch {
				case params == "" || params == "0" || strings.Contains(params, "4:0"):
					on = false
				case strings.Contains(params, "4:3"):
					on = true
				}
				i += j + 1
				continue
			}
			if on {
				cur.WriteByte(line[i])
			} else if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			i++
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
		}
	}
	return out
}

// TestComposerUnderlineTracksScroll is the end-to-end guard for the bug this
// rewrite fixes: with a composer scrolled to its bottom, a finding on a visible
// (formerly off-by-the-scroll-offset) line must underline exactly the flagged
// word — and a finding on a scrolled-off line must not paint at all.
func TestComposerUnderlineTracksScroll(t *testing.T) {
	var m Model
	ta := editor.New()
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 3 // only 3 rows visible → the draft scrolls
	ta.SetPromptFunc(2, inputPromptFunc("> "))
	ta.SetWidth(24)
	var b strings.Builder
	for i := range 8 {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "line %c wrongg", 'a'+i)
	}
	val := b.String()
	ta.SetValue(val) // cursor parks at the end → scrolled to the bottom
	ta.Focus()
	m.input = ta
	m.focus = focusInput
	m.grammar = newGrammarState()
	m.ltClient = languagetool.New("http://localhost:8010/v2", "auto", false, 0)

	// Flag "wrongg" on the LAST line (visible at the bottom).
	lastOff := strings.LastIndex(val, "wrongg")
	m.setGrammarMatches(val, []languagetool.Match{
		{Offset: lastOff, Length: len("wrongg"), IssueType: "misspelling"},
	})
	got := underlinedSpans(m.renderInputBox(40))
	if len(got) != 1 || got[0] != "wrongg" {
		t.Fatalf("scrolled composer underlined %v, want [wrongg]", got)
	}

	// Flag "wrongg" on the FIRST line (scrolled off the top): nothing paints.
	firstOff := strings.Index(val, "wrongg")
	m.setGrammarMatches(val, []languagetool.Match{
		{Offset: firstOff, Length: len("wrongg"), IssueType: "misspelling"},
	})
	if got := underlinedSpans(m.renderInputBox(40)); len(got) != 0 {
		t.Fatalf("off-screen finding painted %v, want nothing", got)
	}
}

func TestTabAppliesTopSuggestionAtCursor(t *testing.T) {
	matches := []languagetool.Match{
		{Offset: 0, Length: 3, Replacements: []string{"some"}, IssueType: "misspelling"},
	}
	m := grammarModel("som text", matches)
	m.input.CursorStart() // cursor sits on "som"

	out, _ := m.handleInputKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = out.(Model)
	if got := m.input.Value(); got != "some text" {
		t.Errorf("tab on a mistake: value=%q, want %q", got, "some text")
	}
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
