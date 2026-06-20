package ui

import "testing"

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
