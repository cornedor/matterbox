package ui

import (
	"testing"

	"matterbox/internal/editor"
)

// typeRunes feeds text into the history one rune at a time, the way the
// composer's keystroke path calls note (before → after per keystroke). It
// returns the resulting draft.
func typeRunes(h *composerHistory, key, start, text string) string {
	cur := start
	for _, r := range text {
		before := cur
		cur += string(r)
		h.note(key, before, cur)
	}
	return cur
}

func TestComposerHistoryWordGranularUndo(t *testing.T) {
	var h composerHistory
	live := typeRunes(&h, "chan:A", "", "hello world")

	// Undo peels off a word at a time rather than a keystroke at a time.
	want := []string{"hello ", ""}
	for i, w := range want {
		v, ok := h.undo("chan:A", live)
		if !ok {
			t.Fatalf("undo %d: ok=false, want a value", i)
		}
		if v != w {
			t.Fatalf("undo %d: got %q, want %q", i, v, w)
		}
		live = v
	}
	if _, ok := h.undo("chan:A", live); ok {
		t.Fatalf("undo past the start should report nothing to undo")
	}

	// Redo replays the same states back to the full draft.
	for _, w := range []string{"hello ", "hello world"} {
		v, ok := h.redo("chan:A", live)
		if !ok {
			t.Fatalf("redo: ok=false, want %q", w)
		}
		if v != w {
			t.Fatalf("redo: got %q, want %q", v, w)
		}
		live = v
	}
	if _, ok := h.redo("chan:A", live); ok {
		t.Fatalf("redo past the end should report nothing to redo")
	}
}

func TestComposerHistoryInsertDeleteFlipIsABoundary(t *testing.T) {
	var h composerHistory
	typeRunes(&h, "k", "", "ab") // two inserts, one coalesced step

	// A backspace flips insert→delete and must start a fresh undo step so the
	// pre-delete draft is recoverable.
	h.note("k", "ab", "a")
	live := "a"

	v, ok := h.undo("k", live)
	if !ok || v != "ab" {
		t.Fatalf("undo after delete: got %q ok=%v, want \"ab\" true", v, ok)
	}
}

func TestComposerHistoryCheckpointIsDiscreteStep(t *testing.T) {
	var h composerHistory
	h.checkpoint("k", "before")
	v, ok := h.undo("k", "after")
	if !ok || v != "before" {
		t.Fatalf("undo of checkpoint: got %q ok=%v, want \"before\" true", v, ok)
	}
}

// TestComposerHistoryDropsOnContextSwitch is the channel-isolation guarantee:
// an undo issued under a different context must never resurrect the previous
// context's draft.
func TestComposerHistoryDropsOnContextSwitch(t *testing.T) {
	var h composerHistory
	typeRunes(&h, "chan:A", "", "secret")

	if v, ok := h.undo("chan:B", "fresh draft"); ok {
		t.Fatalf("undo across contexts returned %q; must report nothing to undo", v)
	}
	// And the new context starts clean.
	if v, ok := h.redo("chan:B", "fresh draft"); ok {
		t.Fatalf("redo in a fresh context returned %q; must be empty", v)
	}
}

func TestComposerHistoryResetClears(t *testing.T) {
	var h composerHistory
	typeRunes(&h, "k", "", "draft")
	h.reset()
	if v, ok := h.undo("k", "draft"); ok {
		t.Fatalf("undo after reset returned %q; want nothing", v)
	}
}

func TestChangeEndOffset(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		want          int
	}{
		// Undo an insertion: "hello world" → "hello " lands the caret right
		// after the restored text (end of common prefix), not at the draft end.
		{"undo insertion", "hello world", "hello ", 6},
		// Undo a deletion: "hi" → "hi there" lands after the re-inserted tail.
		{"undo deletion", "hi", "hi there", 8},
		// A change in the middle lands after the changed region, before the
		// shared suffix.
		{"middle change", "the cat sat", "the dog sat", 7},
		// Identical strings degrade to the end of the draft (harmless).
		{"identical", "same", "same", 4},
		// Multibyte runes are counted as runes, not bytes.
		{"unicode", "café au", "café", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := changeEndOffset(tt.before, tt.after); got != tt.want {
				t.Fatalf("changeEndOffset(%q, %q) = %d, want %d", tt.before, tt.after, got, tt.want)
			}
		})
	}
}

// inputModel builds a Model with a usable textarea for cursor-placement tests.
func inputModel() Model {
	m := Model{keys: newKeyMap("ctrl")}
	ta := editor.New()
	ta.SetWidth(80)
	ta.Focus()
	m.input = ta
	return m
}

// TestApplyComposerSnapshotCursorAtEdit: restoring an undo snapshot lands the
// cursor at the edit site, not blindly at the end of the draft.
func TestApplyComposerSnapshotCursorAtEdit(t *testing.T) {
	m := inputModel()
	m.input.SetValue("hello world")
	// Undo back to "hello " — the caret should sit just after "hello ".
	m.applyComposerSnapshot("hello ")
	if got := m.input.CursorOffset(); got != 6 {
		t.Fatalf("cursor offset after undo = %d, want 6", got)
	}
	if got := m.input.Value(); got != "hello " {
		t.Fatalf("value after undo = %q, want %q", got, "hello ")
	}
}

// TestSetInputCursorOffsetMultiline: the caret lands on the right logical row
// and column for a multi-line draft.
func TestSetInputCursorOffsetMultiline(t *testing.T) {
	m := inputModel()
	m.input.SetValue("one\ntwo\nthree")
	m.input.SetCursorOffset(5) // row 1 ("two"), col 1 → offset 5
	if row, _ := m.input.CursorRowCol(); row != 1 {
		t.Fatalf("cursor row = %d, want 1", row)
	}
	if got := m.input.CursorOffset(); got != 5 {
		t.Fatalf("cursor offset = %d, want 5", got)
	}
}

func TestComposerHistoryCaps(t *testing.T) {
	var h composerHistory
	// Each checkpoint is a discrete push; drive well past the cap.
	for i := 0; i < maxComposerHistory+50; i++ {
		h.checkpoint("k", string(rune('a'+i%26))+itoa(i))
	}
	if len(h.past) != maxComposerHistory {
		t.Fatalf("past length = %d, want capped at %d", len(h.past), maxComposerHistory)
	}
}
