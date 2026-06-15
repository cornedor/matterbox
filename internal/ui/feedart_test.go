package ui

import (
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// boatLayer returns the non-wave glyphs of a rendered frame: every '~'
// blanked to a space. This is the part of the splash that must never move.
func boatLayer(frame string) string {
	plain := stripANSI(frame)
	return strings.ReplaceAll(plain, "~", " ")
}

// TestFeedArtBoatIsStable verifies the ship/line-art stays pixel-identical
// across animation phases — only the waves drift.
func TestFeedArtBoatIsStable(t *testing.T) {
	want := boatLayer(renderFeedArt(0))
	for phase := 1; phase < 40; phase++ {
		if got := boatLayer(renderFeedArt(phase)); got != want {
			t.Fatalf("boat layer changed at phase %d:\n--- phase 0 ---\n%s\n--- phase %d ---\n%s",
				phase, want, phase, got)
		}
	}
}

// TestFeedArtWavesMove verifies the wave layer actually animates: at least
// one phase differs from the first frame within a full cycle.
func TestFeedArtWavesMove(t *testing.T) {
	base := stripANSI(renderFeedArt(0))
	moved := false
	for phase := 1; phase < 40; phase++ {
		if stripANSI(renderFeedArt(phase)) != base {
			moved = true
			break
		}
	}
	if !moved {
		t.Fatal("waves never moved across a full animation cycle")
	}
}

// TestFeedArtWaveCountPreserved verifies drifting never loses more than a
// couple of waves per frame (a wave that slides behind the hull is hidden,
// but the bulk of the water must remain on screen).
func TestFeedArtWaveCountPreserved(t *testing.T) {
	src := strings.Count(feedArtRaw, "~")
	for phase := 0; phase < 40; phase++ {
		got := strings.Count(stripANSI(renderFeedArt(phase)), "~")
		if diff := src - got; diff < 0 || diff > 8 {
			t.Fatalf("phase %d: %d/%d waves drawn (lost %d, want ≤8 hidden behind hull)",
				phase, got, src, diff)
		}
	}
}

// TestFeedArtBlockGeometry guards the rectangle the splash centers as: every
// rendered row is exactly feedArtBlockWidth columns, so the soft-wrapping
// feed viewport never reflows it.
func TestFeedArtBlockGeometry(t *testing.T) {
	for _, line := range strings.Split(stripANSI(renderFeedArt(7)), "\n") {
		if w := len([]rune(line)); w != feedArtBlockWidth {
			t.Fatalf("row width %d, want %d: %q", w, feedArtBlockWidth, line)
		}
	}
}

// TestFeedWaveLoopGuard checks the animation arms only while the splash is
// showing, never starts a second concurrent loop, and stops once the splash
// is replaced (here, by a refresh going in flight).
func TestFeedWaveLoopGuard(t *testing.T) {
	m := &Model{}
	m.feed = newFeedState()
	// teamIdx 0 with no DMs is the Feed tab (see tabAt); empty + not loading
	// means the splash is on screen, so the loop arms exactly once.
	if cmd := m.maybeStartFeedWaves(); cmd == nil || !m.feed.waveActive {
		t.Fatal("waves did not arm on the empty feed tab")
	}
	if cmd := m.maybeStartFeedWaves(); cmd != nil {
		t.Fatal("armed a second concurrent wave loop")
	}
	// A refresh in flight hides the splash → the next tick must stop the loop.
	m.feed.loading = true
	if cmd := m.applyFeedWaveTick(); cmd != nil || m.feed.waveActive {
		t.Fatal("wave loop kept ticking while the feed was loading")
	}
}
