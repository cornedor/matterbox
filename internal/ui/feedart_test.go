package ui

import (
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// boatRunes joins the unstyled boat+wave compositor rows for a phase — the
// static layer, before the gull is overlaid at scene scale.
func boatRunes(phase int) string {
	rows := make([]string, len(feedArtRows))
	for r := range feedArtRows {
		rows[r] = string(composeFeedArtRow(r, phase))
	}
	return strings.Join(rows, "\n")
}

// TestFeedArtBoatIsStable verifies the ship/line-art stays pixel-identical
// across animation phases — only the waves drift. Checked on the boat+wave
// layer; the gull is a separate moving layer (see TestFeedArtBirdFliesFullWidth).
func TestFeedArtBoatIsStable(t *testing.T) {
	want := strings.ReplaceAll(boatRunes(0), "~", " ")
	for phase := 1; phase < 40; phase++ {
		if got := strings.ReplaceAll(boatRunes(phase), "~", " "); got != want {
			t.Fatalf("boat layer changed at phase %d:\n--- phase 0 ---\n%s\n--- phase %d ---\n%s",
				phase, want, phase, got)
		}
	}
}

// TestFeedArtWavesMove verifies the wave layer actually animates: at least
// one phase differs from the first frame within a full cycle.
func TestFeedArtWavesMove(t *testing.T) {
	base := boatRunes(0)
	moved := false
	for phase := 1; phase < 40; phase++ {
		if boatRunes(phase) != base {
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
		got := strings.Count(boatRunes(phase), "~")
		if diff := src - got; diff < 0 || diff > 8 {
			t.Fatalf("phase %d: %d/%d waves drawn (lost %d, want ≤8 hidden behind hull)",
				phase, got, src, diff)
		}
	}
}

// TestFeedSceneGeometry guards the canvas the feed viewport renders: every row
// is exactly w columns (the gull is stamped in place and must never widen one)
// and there are exactly h rows, so the soft-wrapping viewport never reflows it.
// Swept across the whole crossing at the extremes of the height jitter.
func TestFeedSceneGeometry(t *testing.T) {
	const w, h = 80, 30
	for _, yOff := range []int{-birdYJitter, 0, birdYJitter} {
		for step := 0; step < birdTravel(w); step++ {
			lines := strings.Split(stripANSI(renderFeedScene(w, h, step, true, step, yOff)), "\n")
			if len(lines) != h {
				t.Fatalf("step %d yOff %d: %d rows, want %d", step, yOff, len(lines), h)
			}
			for _, line := range lines {
				if got := len([]rune(line)); got != w {
					t.Fatalf("step %d yOff %d: row width %d, want %d: %q", step, yOff, got, w, line)
				}
			}
		}
	}
}

// birdDiffRows reports the screen rows the gull overlay touches and the
// smallest/largest column it touches, isolated by diffing a with-bird render
// against a bird-free one (the water is identical between the two, so every
// difference is a gull cell). minCol/maxCol are -1 when the gull draws nothing.
func birdDiffRows(w, h, step, yOff int) (rows []int, minCol, maxCol int) {
	withBird := strings.Split(stripANSI(renderFeedScene(w, h, 0, true, step, yOff)), "\n")
	base := strings.Split(stripANSI(renderFeedScene(w, h, 0, false, 0, 0)), "\n")
	minCol, maxCol = -1, -1
	for r := range withBird {
		a, b := []rune(withBird[r]), []rune(base[r])
		hit := false
		for c := range a {
			if c < len(b) && a[c] != b[c] {
				hit = true
				if minCol < 0 || c < minCol {
					minCol = c
				}
				if c > maxCol {
					maxCol = c
				}
			}
		}
		if hit {
			rows = append(rows, r)
		}
	}
	return rows, minCol, maxCol
}

// TestFeedBirdFliesFullWidth verifies the gull glides rightward across the whole
// screen — reaching the blank margins on both sides, beyond the centered art
// block, not just inside it.
func TestFeedBirdFliesFullWidth(t *testing.T) {
	const w, h = 80, 30
	travel := birdTravel(w)

	// Direction: the left edge advances between a quarter and three-quarters in.
	_, earlyMin, _ := birdDiffRows(w, h, travel/4, 0)
	_, lateMin, _ := birdDiffRows(w, h, travel*3/4, 0)
	if earlyMin < 0 || lateMin < 0 {
		t.Fatalf("gull not drawn mid-crossing (early=%d late=%d)", earlyMin, lateMin)
	}
	if lateMin <= earlyMin {
		t.Fatalf("gull did not move right: left edge %d → %d", earlyMin, lateMin)
	}

	// Full screen: it reaches both margins, outside the centered art block.
	leftPad := (w - feedArtBlockWidth) / 2
	minCol, maxCol := w, -1
	for step := 0; step < travel; step++ {
		_, lo, hi := birdDiffRows(w, h, step, 0)
		if lo < 0 {
			continue
		}
		if lo < minCol {
			minCol = lo
		}
		if hi > maxCol {
			maxCol = hi
		}
	}
	if minCol >= leftPad {
		t.Fatalf("gull never reached the left margin (min col %d, art block starts at col %d)", minCol, leftPad)
	}
	if maxCol < leftPad+feedArtBlockWidth {
		t.Fatalf("gull never reached the right margin (max col %d, art block ends at col %d)", maxCol, leftPad+feedArtBlockWidth)
	}
}

// TestFeedBirdHeightJitter verifies birdYOff shifts the gull vertically by
// exactly that many rows. Uses a wide screen so the gull sits over blank left
// margin (every glyph differs from the background) and a tall one so ±jitter
// doesn't hit the on-screen clamp.
func TestFeedBirdHeightJitter(t *testing.T) {
	const w, h = 200, 40
	const step = 20 // mid-left margin: x = -gull.w + step*birdSpeed, all blank behind
	top := func(yOff int) int {
		rows, _, _ := birdDiffRows(w, h, step, yOff)
		if len(rows) == 0 {
			t.Fatalf("gull not drawn at yOff %d", yOff)
		}
		return rows[0]
	}
	base := top(0)
	for _, d := range []int{-birdYJitter, -3, 3, birdYJitter} {
		if got := top(d); got != base+d {
			t.Fatalf("yOff %d: top row %d, want %d (base %d)", d, got, base+d, base)
		}
	}
}

// TestFeedBirdSchedule exercises the fly-by state machine: it idles through the
// gap without launching early, launches a crossing at a fresh in-range height,
// runs exactly birdTravel frames, then schedules the next random in-range gap.
func TestFeedBirdSchedule(t *testing.T) {
	const w = 80
	m := &Model{}
	m.feed = newFeedState(false)
	m.feed.birdWait = 3 // short idle for the test

	for i := 0; i < 3; i++ {
		m.advanceFeedBird(w)
		if m.feed.birdActive {
			t.Fatalf("gull launched during the idle gap (after %d frames)", i+1)
		}
	}
	m.advanceFeedBird(w) // gap elapsed → launch
	if !m.feed.birdActive || m.feed.birdStep != 0 {
		t.Fatalf("gull did not launch after the gap (active=%v step=%d)", m.feed.birdActive, m.feed.birdStep)
	}
	if y := m.feed.birdYOff; y < -birdYJitter || y > birdYJitter {
		t.Fatalf("fly-by height %d out of range [%d,%d]", y, -birdYJitter, birdYJitter)
	}

	steps := 0
	for m.feed.birdActive && steps < birdTravel(w)+5 {
		m.advanceFeedBird(w)
		steps++
	}
	if m.feed.birdActive {
		t.Fatal("crossing never ended")
	}
	if steps != birdTravel(w) {
		t.Fatalf("crossing took %d frames, want %d", steps, birdTravel(w))
	}
	lo, hi := int(birdGapMin/feedWaveInterval), int(birdGapMax/feedWaveInterval)
	if g := m.feed.birdWait; g < lo || g > hi {
		t.Fatalf("next gap %d frames, want [%d,%d] (a few birds/hour)", g, lo, hi)
	}
}

// TestFeedWaveLoopGuard checks the animation arms only while the splash is
// showing, never starts a second concurrent loop, and stops once the splash
// is replaced (here, by a refresh going in flight).
func TestFeedWaveLoopGuard(t *testing.T) {
	m := &Model{}
	m.feed = newFeedState(false)
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
