package ui

import (
	"image/color"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// frame renders animation frame number phase with its styling removed — the
// ramp glyphs alone, which is what every assertion below is about. Frames, not
// seconds: the field's pace is a per-frame property, and these tests animate at
// the default frame rate.
func frame(w, h, phase int) []string {
	t := float64(phase) * feedBlobInterval.Seconds()
	return strings.Split(stripANSI(renderFeedBlobs(w, h, t, blobNudges{})), "\n")
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
	m.feed = newFeedState(false, 0)
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

// pokeModel is an empty Feed tab with a w×h field on it, ready to be clicked.
func pokeModel(w, h int) *Model {
	m := &Model{}
	m.feed = newFeedState(false, 0)
	m.feed.view.SetWidth(w)
	m.feed.view.SetHeight(h)
	return m
}

// step advances the field one animation frame, exactly as applyFeedBlobTick
// does, and returns the rendered frame.
func step(m *Model) string {
	dt := m.feed.blobInterval.Seconds()
	m.feed.blobPhase += dt
	m.feed.blobNudge.advance(dt)
	return stripANSI(renderFeedBlobs(m.feed.view.Width(), m.feed.view.Height(), m.feed.blobPhase, m.feed.blobNudge))
}

// rimCell is a cell one radius out to the side of a blob's centre — near the
// peak of blobPushWeight, which is where a poke does the most.
func rimCell(b blobFrame) (col, row int) {
	r := math.Sqrt(b.r2)
	return int(b.cx), int(b.cy + r)
}

// dist is the distance from cell (col,row) to a blob's centre, in row units —
// the space the push is isotropic in.
func dist(b blobFrame, col, row int) float64 {
	return math.Hypot((b.cx-(float64(col)+0.5))*cellAspect, b.cy-(float64(row)+0.5))
}

// TestFeedBlobPokePushesAway is the toy: a click inside a blob moves that blob
// away from the clicked cell, by a distance you can actually see, and never
// pulls it closer.
func TestFeedBlobPokePushesAway(t *testing.T) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	b0 := feedBlobFrame(w, h, 0, blobNudges{})[0]
	col, row := rimCell(b0)
	before := dist(b0, col, row)

	m.pokeFeedBlobs(col, row)
	var peak float64
	for f := 0; f < 60; f++ {
		step(m)
		b := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[0]
		drifted := feedBlobFrame(w, h, m.feed.blobPhase, blobNudges{})[0]
		// Measured against the same phase's undisturbed position, so the drift
		// itself can't be mistaken for the push.
		if d := dist(b, col, row) - dist(drifted, col, row); d < -0.01 {
			t.Fatalf("frame %d: the blob moved %.2f rows *towards* the click", f, -d)
		} else if d > peak {
			peak = d
		}
	}
	if peak < 1 {
		t.Errorf("peak push %.2f rows from %.2f rows out — too small to notice", peak, before)
	}
	if peak > 4 {
		t.Errorf("peak push %.2f rows — that's a throw, not a nudge", peak)
	}
}

// TestFeedBlobPokeSettles: the push is a critically damped spring, so the blob
// returns to its drift path without ever crossing it (a bounce would read as
// elastic, which is the opposite of soothing), stays inside the cap, and ends
// up exactly back on the path.
func TestFeedBlobPokeSettles(t *testing.T) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	b0 := feedBlobFrame(w, h, 0, blobNudges{})[0]
	m.pokeFeedBlobs(rimCell(b0))

	sign := func(v float64) float64 {
		if v < 0 {
			return -1
		}
		return 1
	}
	sx, sy := sign(m.feed.blobNudge[0].vx), sign(m.feed.blobNudge[0].vy)
	settled := -1
	for f := 1; f <= 300; f++ {
		step(m)
		for i, p := range m.feed.blobNudge {
			if math.Abs(p.ox) > blobPushCap+1e-9 || math.Abs(p.oy) > blobPushCap+1e-9 {
				t.Fatalf("frame %d: blob %d offset (%.3f,%.3f) escaped the cap %.2f", f, i, p.ox, p.oy, blobPushCap)
			}
		}
		p := m.feed.blobNudge[0]
		if p.ox*sx < -1e-6 || p.oy*sy < -1e-6 {
			t.Fatalf("frame %d: offset (%.4f,%.4f) overshot past the drift path — the spring bounces", f, p.ox, p.oy)
		}
		if settled < 0 && m.feed.blobNudge.idle() {
			settled = f
		}
	}
	if settled < 0 {
		t.Fatal("the push never settled — the field is left permanently displaced")
	}
	// Long enough to be a slow return, not so long that the field never
	// recovers its composed state.
	if lo, hi := 40, 200; settled < lo || settled > hi {
		t.Errorf("settled after %d frames (%.1fs), want %d..%d", settled,
			float64(settled)*feedBlobInterval.Seconds(), lo, hi)
	}
}

// TestFeedBlobPokeStaysCalm holds the two properties a poke must not break,
// whatever the swell is dialled to: the field never fills the pane solid, and
// the liveliness *ends* — a few seconds after the last click the per-frame
// repaint is back under the ambient drift ceiling that TestFeedBlobsDrift pins.
// A percentage-of-cells ceiling during the burst is not the guard it looks
// like: a big blob crossing the pane changes most cells by one shading step,
// which reads as calm however many cells it touches.
func TestFeedBlobPokeStaysCalm(t *testing.T) {
	const w, h = 80, 24
	const fps = 60
	const clicks = 10

	m := pokeModel(w, h)
	m.feed.blobInterval = feedBlobIntervalFor(fps)
	prev := stripANSI(renderFeedBlobs(w, h, 0, blobNudges{}))
	diff := func(a, b string) int {
		n := 0
		for i := range a {
			if a[i] != b[i] {
				n++
			}
		}
		return n
	}
	var lastClick float64
	var quiet float64 // when the churn first drops back to drift level
	for f := 0; m.feed.blobPhase < float64(clicks)+8; f++ {
		if f%fps == 0 && f < clicks*fps { // a click a second, on a different blob each time
			b := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[(f/fps)%len(feedBlobs)]
			m.pokeFeedBlobs(rimCell(b))
			lastClick, quiet = m.feed.blobPhase, 0
		}
		cur := step(m)
		// Every row is still exactly w wide: pushed and swollen blobs must not
		// reflow the canvas any more than drifting ones do.
		for i, line := range strings.Split(cur, "\n") {
			if got := len([]rune(line)); got != w {
				t.Fatalf("%.2fs: row %d is %d columns wide, want %d", m.feed.blobPhase, i, got, w)
			}
		}
		if ink := len(cur) - strings.Count(cur, " ") - strings.Count(cur, "\n"); ink > w*h*9/10 {
			t.Fatalf("%.2fs: %d of %d cells inked — the swell swallowed the pane", m.feed.blobPhase, ink, w*h)
		}
		if d := diff(prev, cur); quiet == 0 && d <= w*h/50 {
			quiet = m.feed.blobPhase
		}
		prev = cur
	}
	if quiet == 0 {
		t.Fatal("the field never came back down to drift-level churn after the last click")
	}
	if settle := quiet - lastClick; settle > 4 {
		t.Errorf("still repainting above the drift ceiling %.1fs after the last click — a poke has to run out", settle)
	}
}

// TestFeedBlobPokeShape is the feel: pressing a blob square in the middle does
// (near enough) nothing, pressing just off its side does the most, and a click
// well clear of it does nothing again — two goopy blobs you can only shove
// sideways. The margins are wide on purpose, so "well clear" is measured in
// blob radii, not cells.
func TestFeedBlobPokeShape(t *testing.T) {
	const w, h = 80, 24
	b := feedBlobFrame(w, h, 0, blobNudges{})[0]
	r := math.Sqrt(b.r2)

	// Push, in cells travelled, from a click r rows off the blob's centre.
	push := func(mult float64) float64 {
		m := pokeModel(w, h)
		m.pokeFeedBlobs(int(b.cx), int(b.cy+mult*r))
		p := m.feed.blobNudge[0]
		return math.Hypot(p.vx*float64(w), p.vy*float64(h))
	}

	centre, rim, far := push(0), push(1), push(blobPushReach+0.2)
	if rim <= 0 {
		t.Fatal("a click just off the blob's side moved nothing")
	}
	if centre > rim/10 {
		t.Errorf("a click on the centre pushes %.4f, %.0f%% of the %.4f a side click does — pressing the middle should do nothing",
			centre, 100*centre/rim, rim)
	}
	if far != 0 {
		t.Errorf("a click %.1f radii out still pushes %.4f — the reach has no end", blobPushReach+0.2, far)
	}
	// The kernel peaks near the rim, and the peak is broad — no cliff a user
	// could feel the field switch on or off at.
	for _, u := range []float64{0.6, 1.0, 1.6} {
		if got := push(u); got < rim/2 {
			t.Errorf("a click %.1f radii out pushes %.4f, under half the rim's %.4f — the sweet spot is too narrow",
				u, got, rim)
		}
	}
	// Wide margins: a click a full radius outside the visible rim still leans on
	// the blob, which is what makes the field feel soft rather than clickable.
	if push(2.0) <= 0 {
		t.Error("a click two radii out does nothing — the margins are not wide")
	}

	// The swell runs the other way round: it marks the blob you pressed, so it
	// peaks on the centre and dies before the reach.
	swell := func(mult float64) float64 {
		m := pokeModel(w, h)
		m.pokeFeedBlobs(int(b.cx), int(b.cy+mult*r))
		return m.feed.blobNudge[0].vs
	}
	if swell(0) <= swell(1) {
		t.Errorf("a side click swells the blob %.4f, at least as much as the %.4f a centre press does — the swell has to name the blob under the pointer",
			swell(1), swell(0))
	}
	if got := swell(blobSizeReach + 0.1); got != 0 {
		t.Errorf("a click %.1f radii out still swells the blob %.4f", blobSizeReach+0.1, got)
	}
}

// TestFeedBlobSwellPicksTheClickedBlob: press one blob's middle and it is that
// blob that spreads, not a neighbour whose rim the click happened to reach.
// The blobs overlap by design, so this is the assertion that keeps the swell
// legible as "you pressed this one".
func TestFeedBlobSwellPicksTheClickedBlob(t *testing.T) {
	const w, h = 80, 24
	blobs := feedBlobFrame(w, h, 0, blobNudges{})
	for i, b := range blobs {
		m := pokeModel(w, h)
		m.pokeFeedBlobs(int(b.cx), int(b.cy))
		for j := range blobs {
			if j != i && m.feed.blobNudge[j].vs > m.feed.blobNudge[i].vs {
				t.Errorf("pressing blob %d's middle swelled blob %d harder (%.4f vs %.4f)",
					i, j, m.feed.blobNudge[j].vs, m.feed.blobNudge[i].vs)
			}
		}
	}
}

// TestFeedBlobPokeSwells is the second half of the toy: a poke swells the blob
// it lands in, then lets the size settle back through one soft overshoot —
// elastic, not a bounce. Pins blobSizeSpan as the swell that actually reaches
// the screen (the gain constant that makes it so is measured, not derived).
func TestFeedBlobPokeSwells(t *testing.T) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	b0 := feedBlobFrame(w, h, 0, blobNudges{})[0]
	// The middle of the blob: where a press spreads it most.
	m.pokeFeedBlobs(int(b0.cx), int(b0.cy))

	var peak, dip float64
	var trace []float64
	for f := 1; f <= 300; f++ {
		step(m)
		os := m.feed.blobNudge[0].os
		peak = math.Max(peak, os)
		dip = math.Min(dip, os)
		trace = append(trace, os)
	}
	// Crossings of the resting radius, counted against a band scaled to the
	// swell — an absolute threshold would read the numerical tail as ringing
	// once blobSizeSpan is turned up.
	band := peak / 100
	crossings, prev := 0, trace[0]
	for _, os := range trace[1:] {
		if (prev > band && os < -band) || (prev < -band && os > band) {
			crossings++
		}
		if math.Abs(os) > band {
			prev = os
		}
	}
	// The swell has to be several times the ambient radius pulse to read as a
	// response at all, and stay well inside its cap.
	if lo := 3 * blobPulse; peak < lo || peak > blobSizeCap {
		t.Errorf("peak swell %.3f, want %.3f..%.2f", peak, lo, blobSizeCap)
	}
	if peak < blobSizeSpan*0.8 || peak > blobSizeSpan*1.2 {
		t.Errorf("peak swell %.3f is not the %.2f blobSizeSpan promises — retune blobSizeSwellGain",
			peak, blobSizeSpan)
	}
	// Elastic: it must dip below the resting radius on the way back (that's the
	// overshoot), but only the once — more is ringing.
	if dip > -0.005 {
		t.Errorf("the swell never sank past the resting radius (min %.4f) — no elastic return", dip)
	}
	if dip < -peak/2 {
		t.Errorf("undershoot %.3f is half the %.3f swell — that's a bounce, not a settle", dip, peak)
	}
	if crossings > 2 {
		t.Errorf("the size crossed its resting radius %d times — the blob rings", crossings)
	}
	if !m.feed.blobNudge.idle() {
		t.Error("the swell never settled")
	}
}

// TestFeedBlobClickRoute walks the real mouse path: a click in the empty feed
// hit-tests to the field, lands on the field's own cell coordinates, pokes it
// and keeps the animation armed. With bubbles in the pane there is no field, so
// the same click must go back to being a bubble click.
func TestFeedBlobClickRoute(t *testing.T) {
	m := feedButtonModel(t)
	m.feed.entries = nil
	m.renderFeedResults()

	if !m.feedBlobFieldDrawn() {
		t.Fatalf("no field on a %dx%d empty feed", m.feed.view.Width(), m.feed.view.Height())
	}
	top, _, _ := m.feedGeom()
	col, row := m.feed.view.Width()/2, m.feed.view.Height()/2
	x, y := col+1, row+top // the pane's left border owns column 0

	h := m.hitTest(x, y)
	if h.zone != hitFeedBlobs || h.col != col || h.line != row {
		t.Fatalf("hitTest(%d,%d) = %v col=%d line=%d, want hitFeedBlobs col=%d line=%d",
			x, y, h.zone, h.col, h.line, col, row)
	}
	next, cmd := m.handleMouseClick(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	got := next.(Model)
	if got.feed.blobNudge.idle() {
		t.Error("a click in the middle of the field pushed nothing")
	}
	if cmd == nil || !got.feed.blobActive {
		t.Error("the click left the animation loop unarmed — the push would never move")
	}

	// Bubbles back in the pane: the field is gone and so is its hit zone.
	m2 := feedButtonModel(t)
	if h := m2.hitTest(x, y); h.zone == hitFeedBlobs {
		t.Error("the field claimed a click over a feed bubble")
	}
}

// TestFeedBlobFPSIndependent is what makes animations.feed_blob_fps a
// smoothness knob and nothing else: the same poke, replayed at 1, 5 and 60 fps,
// reaches the same displacement and the same swell at the same wall-clock
// second. The springs are stepped in closed form for exactly this reason — an
// Euler stepper would quietly change the feel along with the frame rate.
func TestFeedBlobFPSIndependent(t *testing.T) {
	const w, h = 80, 24

	// State exactly `secs` seconds after a rim poke, animated at fps frames a
	// second (a whole number of frames, so every rate samples the same instant).
	at := func(fps int, secs float64) blobNudge {
		m := pokeModel(w, h)
		m.feed.blobInterval = feedBlobIntervalFor(fps)
		b := feedBlobFrame(w, h, 0, blobNudges{})[0]
		m.pokeFeedBlobs(rimCell(b))
		for f := 0; f < int(secs*float64(fps)); f++ {
			step(m)
		}
		return m.feed.blobNudge[0]
	}

	// Both near the peak and out on the settling tail.
	for _, secs := range []float64{1, 6} {
		ref := at(defaultFeedBlobFPS, secs)
		for _, fps := range []int{1, 2, 12, 30, 60} {
			got := at(fps, secs)
			for _, f := range []struct {
				name          string
				got, ref, amp float64
			}{
				// Tolerances are against each spring's own amplitude, not against
				// the reference value: on the tail the values are near zero, where
				// a relative comparison means nothing.
				{"offset x", got.ox, ref.ox, blobPushSpan},
				{"offset y", got.oy, ref.oy, blobPushSpan},
				{"swell", got.os, ref.os, blobSizeSpan},
			} {
				if d := math.Abs(f.got - f.ref); d > 0.02*f.amp {
					t.Errorf("%d fps: %s at %.0fs = %.4f, want %.4f (the %d fps value) — the frame rate changed the motion",
						fps, f.name, secs, f.got, f.ref, defaultFeedBlobFPS)
				}
			}
		}
	}
}

// TestFeedBlobIntervalFor: the config value is clamped, and 0 means default.
func TestFeedBlobIntervalFor(t *testing.T) {
	for _, tc := range []struct {
		fps  int
		want time.Duration
	}{
		{0, feedBlobInterval},
		{-4, feedBlobInterval},
		{defaultFeedBlobFPS, feedBlobInterval},
		{20, 50 * time.Millisecond},
		{600, time.Second / feedBlobFPSMax},
	} {
		if got := feedBlobIntervalFor(tc.fps); got != tc.want {
			t.Errorf("feedBlobIntervalFor(%d) = %v, want %v", tc.fps, got, tc.want)
		}
	}
}

// TestFeedBlobPeakMatchesSpan pins the two span knobs as the amplitudes they
// claim to be: a best-placed click travels blobPushSpan of the pane's short
// side, and a centre press swells the blob by blobSizeSpan of its radius. Both
// come out of the gain constants, so a wrong gain shows up here rather than as
// a field that feels off.
func TestFeedBlobPeakMatchesSpan(t *testing.T) {
	const w, h = 80, 24
	scale := math.Min(float64(h), float64(w)*cellAspect)

	// Travel, in row units, of a rim poke.
	m := pokeModel(w, h)
	b := feedBlobFrame(w, h, 0, blobNudges{})[0]
	m.pokeFeedBlobs(rimCell(b))
	var travel, swell float64
	for f := 0; f < 300; f++ {
		step(m)
		p := m.feed.blobNudge[0]
		travel = math.Max(travel, math.Hypot(p.ox*float64(w)*cellAspect, p.oy*float64(h)))
	}
	if want := blobPushSpan * scale; math.Abs(travel-want) > 0.15*want {
		t.Errorf("peak travel %.2f rows, want blobPushSpan's %.2f", travel, want)
	}

	m = pokeModel(w, h)
	m.pokeFeedBlobs(int(b.cx), int(b.cy))
	for f := 0; f < 300; f++ {
		step(m)
		swell = math.Max(swell, m.feed.blobNudge[0].os)
	}
	if math.Abs(swell-blobSizeSpan) > 0.15*blobSizeSpan {
		t.Errorf("peak swell %.2f, want blobSizeSpan's %.2f", swell, blobSizeSpan)
	}
}

// TestFeedBlobTickKeepsFrameWhenStill: at a high frame rate most ticks redraw
// the field exactly as it already is. Those must leave the memoized screen (and
// the viewport) alone — repainting the same bytes 60 times a second is the
// whole cost this animation could have had.
func TestFeedBlobTickKeepsFrameWhenStill(t *testing.T) {
	m := benchBlobModel(160, 48)
	m.vcache.viewValid = true

	m.feed.blobInterval = time.Microsecond // a frame gap nothing moves in
	if cmd := m.applyFeedBlobTick(); cmd == nil {
		t.Fatal("the tick must reschedule itself")
	}
	if !m.vcache.viewValid {
		t.Error("an unchanged frame dropped the memoized screen")
	}

	m.feed.blobInterval = 2 * time.Second // long enough that the drift shows
	m.applyFeedBlobTick()
	if m.vcache.viewValid {
		t.Error("a frame that moved kept the memoized screen — the drift would freeze")
	}
}

// TestFeedBlobPaintedClearedOffEmptyState: the "already on screen" memo speaks
// only for the empty-state art. As soon as the feed has something to list, it
// has to be cleared, or a later empty frame that happens to match it would be
// skipped over content that is no longer the blob field.
func TestFeedBlobPaintedClearedOffEmptyState(t *testing.T) {
	m := benchBlobModel(160, 48)
	m.renderFeedResults()
	if m.feed.blobPainted == "" {
		t.Fatal("the empty state should have recorded what it painted")
	}
	m.feed.entries = []feedEntry{{channelID: "c1", unread: []*model.Post{{Id: "p1", Message: "hi"}}}}
	m.renderFeedResults()
	if m.feed.blobPainted != "" {
		t.Error("a listed feed left the blob-field memo armed")
	}
}
