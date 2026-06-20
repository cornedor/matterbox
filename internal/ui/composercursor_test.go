package ui

import (
	"testing"

	"matterbox/internal/languagetool"
)

func TestChangedRegionEnd(t *testing.T) {
	cases := []struct {
		name       string
		prev, next string
		want       int
	}{
		// A space inserted in the middle: cursor lands just past it, not at end.
		{"mid insert", "helloworld", "hello world", 6},
		// Trailing text restored (undo a delete-to-end): end of draft.
		{"tail restored", "hello", "hello world", 11},
		// Leading text restored: cursor at the end of the re-added prefix.
		{"head restored", "world", "hello world", 6},
		// A transposition fixed in place ("iu" -> "ui"): cursor lands at the end
		// of just the differing span, trimming the shared "ck fox" suffix.
		{"transposition", "the qiuck fox", "the quick fox", 7},
		// Identical strings (no real change) fall through to the end.
		{"identical", "same", "same", 4},
		// Everything cleared then restored.
		{"from empty", "", "hello", 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := changedRegionEnd(c.prev, c.next); got != c.want {
				t.Errorf("changedRegionEnd(%q,%q)=%d, want %d", c.prev, c.next, got, c.want)
			}
		})
	}
}

func TestSetInputValueCursorLandsAtOffset(t *testing.T) {
	var m Model
	ta := composerTextarea("", 40)
	ta.Focus()
	m.input = ta

	m.setInputValueCursor("hello world", 5)
	if got := m.input.Value(); got != "hello world" {
		t.Fatalf("value=%q, want %q", got, "hello world")
	}
	if got := m.inputCursorOffset(); got != 5 {
		t.Errorf("cursor offset=%d, want 5 (between the two words)", got)
	}

	// Out-of-range offsets clamp instead of panicking.
	m.setInputValueCursor("hi", 99)
	if got := m.inputCursorOffset(); got != 2 {
		t.Errorf("clamped cursor offset=%d, want 2", got)
	}

	// A multi-line draft positions onto the right logical row/column.
	m.setInputValueCursor("ab\ncde\nfg", 5) // "ab\ncd|e\nfg"
	if got := m.inputCursorOffset(); got != 5 {
		t.Errorf("multiline cursor offset=%d, want 5", got)
	}
}

func TestUndoPlacesCursorAtChangeNotEnd(t *testing.T) {
	var m Model
	ta := composerTextarea("", 40)
	ta.Focus()
	m.input = ta
	m.focus = focusInput
	m.lastInputHeight = ta.Height()
	m.grammar = newGrammarState()

	key := m.composerContextKey()
	// A space was removed in the middle: "hello world" -> "helloworld".
	m.history.checkpoint(key, "hello world")
	m.input.SetValue("helloworld")

	v, ok := m.history.undo(key, m.input.Value())
	if !ok {
		t.Fatal("undo should succeed")
	}
	m.applyComposerSnapshot(v, "helloworld")

	if got := m.input.Value(); got != "hello world" {
		t.Fatalf("after undo value=%q, want %q", got, "hello world")
	}
	if got := m.inputCursorOffset(); got != 6 {
		t.Errorf("after undo cursor offset=%d, want 6 (just past the restored space), not 11 (end)", got)
	}
}

func TestGrammarFixPlacesCursorAfterReplacement(t *testing.T) {
	// "som sentnce" with a fix on the first word; the cursor should land just
	// past the replacement ("some"), in the middle of the draft, not at the end.
	matches := []languagetool.Match{
		{Offset: 0, Length: 3, Replacements: []string{"some"}, IssueType: "misspelling"},
	}
	m := grammarModel("som sentnce", matches)
	m.grammar.popup = true
	m.grammar.popupIdx = 0

	m.applyGrammarSuggestion(0)
	if got := m.input.Value(); got != "some sentnce" {
		t.Fatalf("after fix value=%q, want %q", got, "some sentnce")
	}
	if got := m.inputCursorOffset(); got != 4 {
		t.Errorf("after fix cursor offset=%d, want 4 (just past %q), not 12 (end)", got, "some")
	}
}
