package ui

import (
	"image/color"
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// frame renders one animation frame with its styling removed — the ramp
// glyphs alone, which is what every assertion below is about.
func frame(w, h, phase int) []string {
	return strings.Split(stripANSI(renderFeedBlobs(w, h, phase)), "\n")
}

// TestFeedBlobsGeometry guards the canvas the feed viewport renders: exactly h
// rows of exactly w columns. The viewport soft-wraps, so a single overlong row
// would reflow the whole scene.
func TestFeedBlobsGeometry(t *testing.T) {
	for _, sz := range [][2]int{{feedBlobMinW, feedBlobMinH}, {80, 24}, {200, 60}, {31, 47}} {
		w, h := sz[0], sz[1]
		for phase := 0; phase < 200; phase += 7 {
			lines := frame(w, h, phase)
			if len(lines) != h {
				t.Fatalf("%dx%d phase %d: %d rows, want %d", w, h, phase, len(lines), h)
			}
			for _, line := range lines {
				if got := len([]rune(line)); got != w {
					t.Fatalf("%dx%d phase %d: row width %d, want %d: %q", w, h, phase, got, w, line)
				}
			}
		}
	}
}

// TestFeedBlobsOnlyRampGlyphs verifies every cell comes out of the shading
// table — nothing else can reach the canvas.
func TestFeedBlobsOnlyRampGlyphs(t *testing.T) {
	for phase := 0; phase < 200; phase += 11 {
		for _, line := range frame(80, 24, phase) {
			for _, r := range line {
				if !strings.ContainsRune(blobRamp, r) {
					t.Fatalf("phase %d: glyph %q is not in the ramp %q", phase, r, blobRamp)
				}
			}
		}
	}
}

// TestFeedBlobsDrift pins the pace, which is the whole point of this thing:
// it has to be calm enough to leave open on a second screen. No frame may
// repaint more than 2% of the pane (measured peak is under 1%, so this is a
// ceiling with headroom, not a snapshot), and the field must still be moving —
// checked over a window, since at this speed a single frame can land
// unchanged.
func TestFeedBlobsDrift(t *testing.T) {
	const w, h = 80, 24
	const window = 10 // frames — two seconds at feedBlobInterval

	frames := make([]string, 200)
	for phase := range frames {
		frames[phase] = strings.Join(frame(w, h, phase), "")
	}
	diff := func(a, b string) int {
		n := 0
		for i := range a {
			if a[i] != b[i] {
				n++
			}
		}
		return n
	}
	for phase := 1; phase < len(frames); phase++ {
		if d, max := diff(frames[phase-1], frames[phase]), w*h/50; d > max {
			t.Fatalf("phase %d repainted %d of %d cells (want ≤%d) — too busy to sit next to",
				phase, d, w*h, max)
		}
	}
	for phase := window; phase < len(frames); phase++ {
		if diff(frames[phase-window], frames[phase]) == 0 {
			t.Fatalf("phases %d..%d are identical — the field froze", phase-window, phase)
		}
	}
}

// TestFeedBlobsShade verifies the blobs are shaded rather than flat. The blobs
// overlap by design, so this is really a guard on blobShade: drop the density
// compression and the overlap saturates to the top glyph, the middle of the
// ramp goes unused, and the mass renders as a slab.
func TestFeedBlobsShade(t *testing.T) {
	const w, h = 80, 24
	seen := map[rune]bool{}
	// Ten minutes of animation: the densest glyph only shows up when the drift
	// happens to stack the blobs, which is minutes apart.
	for phase := 0; phase < 3000; phase += 7 {
		for _, line := range frame(w, h, phase) {
			for _, r := range line {
				seen[r] = true
			}
		}
	}
	for _, r := range blobRamp {
		if !seen[r] {
			t.Fatalf("ramp step %q never rendered — the field never spans the full shading table", r)
		}
	}
}

// TestFeedBlobsBreathe guards the empty-vs-ink balance over a long run: the
// field always has something in it, and never swallows the whole pane.
func TestFeedBlobsBreathe(t *testing.T) {
	const w, h = 80, 24
	for phase := 0; phase < 2000; phase += 13 {
		ink := 0
		for _, line := range frame(w, h, phase) {
			ink += len(line) - strings.Count(line, " ")
		}
		if ink == 0 {
			t.Fatalf("phase %d: the pane is empty — every blob drifted off screen", phase)
		}
		if max := w * h * 9 / 10; ink > max {
			t.Fatalf("phase %d: %d of %d cells inked (want ≤%d) — no space left to drift in",
				phase, ink, w*h, max)
		}
	}
}

// TestFeedBlobLoopGuard checks the animation arms only while the empty state is
// showing, never starts a second concurrent loop, and stops once the field is
// replaced (here, by a refresh going in flight).
func TestFeedBlobLoopGuard(t *testing.T) {
	m := &Model{}
	m.feed = newFeedState(false)
	// teamIdx 0 with no DMs is the Feed tab (see tabAt); empty + not loading
	// means the field is on screen, so the loop arms exactly once.
	if cmd := m.maybeStartFeedBlobs(); cmd == nil || !m.feed.blobActive {
		t.Fatal("the blob loop did not arm on the empty feed tab")
	}
	if cmd := m.maybeStartFeedBlobs(); cmd != nil {
		t.Fatal("armed a second concurrent blob loop")
	}
	// A refresh in flight hides the field → the next tick must stop the loop.
	m.feed.loading = true
	if cmd := m.applyFeedBlobTick(); cmd != nil || m.feed.blobActive {
		t.Fatal("blob loop kept ticking while the feed was loading")
	}
}

// blobPaletteLimit is how far the densest end of the ramp may travel from the
// terminal's background. The faint end is deliberately unconstrained — it is
// meant to sink into the page — but the inky end running all the way to the
// foreground is what turns the field into text on the screen rather than a
// surface behind it, which is exactly what a near-black step on a light
// terminal did.
const blobPaletteLimit = 0.80

// TestFeedBlobPaletteStaysSoft checks both palettes stop short of that limit
// and stay monotonic — each step has to be a step, in one direction, or the
// ramp stops reading as shading.
func TestFeedBlobPaletteStaysSoft(t *testing.T) {
	lum := func(c color.Color) float64 {
		r, g, b, _ := c.RGBA()
		return (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 65535
	}
	for _, tc := range []struct {
		name string
		pick func(adaptiveColor) color.Color
		sign float64 // +1 if the ramp brightens with density, -1 if it darkens
	}{
		{"dark background", func(c adaptiveColor) color.Color { return c.dark }, +1},
		{"light background", func(c adaptiveColor) color.Color { return c.light }, -1},
	} {
		prev := 0.0
		for i := 1; i < len(blobRamp); i++ {
			l := lum(tc.pick(feedBlobPalette[i]))
			// Distance from the background this palette assumes: a dark
			// terminal's background is 0, a light one's is 1.
			d := l
			if tc.sign < 0 {
				d = 1 - l
			}
			if d > blobPaletteLimit {
				t.Errorf("%s: step %d (%q) sits %.2f from the background (limit %.2f) — too close to the terminal's own text",
					tc.name, i, blobRamp[i], d, blobPaletteLimit)
			}
			if i > 1 && tc.sign*(l-prev) <= 0 {
				t.Errorf("%s: step %d (%q) at %.2f does not continue the ramp from %.2f",
					tc.name, i, blobRamp[i], l, prev)
			}
			prev = l
		}
	}
}
