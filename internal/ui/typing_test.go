package ui

import (
	"testing"
	"unicode/utf8"
)

// TestBuildTypingFramesEndsOnTarget runs the randomized frame builder
// many times to guard the core invariant: whatever typos get sprinkled
// in, the buffer always lands on exactly the requested message. It also
// checks that frames never overshoot the target length by more than the
// single extra char a typo can add, and that delays are positive.
func TestBuildTypingFramesEndsOnTarget(t *testing.T) {
	for _, target := range []string{"hi", "hello world", "performance test 123", "a"} {
		want := utf8.RuneCountInString(target)
		for i := 0; i < 200; i++ {
			frames := buildTypingFrames(target)
			if len(frames) == 0 {
				t.Fatalf("target %q: no frames produced", target)
			}
			last := frames[len(frames)-1]
			if last.text != target {
				t.Fatalf("target %q: final frame = %q, want %q", target, last.text, target)
			}
			for j, f := range frames {
				if got := utf8.RuneCountInString(f.text); got > want+1 {
					t.Fatalf("target %q frame %d %q: len %d exceeds target+1 (%d)", target, j, f.text, got, want+1)
				}
				if f.delay <= 0 {
					t.Fatalf("target %q frame %d: non-positive delay %v", target, j, f.delay)
				}
			}
		}
	}
}

// TestBuildTypingFramesEmpty makes sure an empty input degrades to no
// frames rather than panicking (the command guards against this, but
// the builder should be safe on its own).
func TestBuildTypingFramesEmpty(t *testing.T) {
	if frames := buildTypingFrames(""); len(frames) != 0 {
		t.Fatalf("empty target: got %d frames, want 0", len(frames))
	}
}
