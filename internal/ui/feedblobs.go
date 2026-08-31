package ui

import (
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The Feed tab's empty state is a slow field of shaded blobs — a lava-lamp
// drift with nothing to read into it. Nothing here is stored art: the field is
// a handful of metaballs, sampled once per character cell and quantised
// through blobRamp, so it fills any pane size exactly and owns no glyph
// anybody else drew.

// blobRamp is the shading table, ordered by ink coverage: index 0 is empty
// space, the last index the densest glyph. Picking an index out of it *is* the
// renderer — every cell is one shaded sample mapped onto this ramp.
//
// It stops at '#'. The classic ramp carries on through '%' and '@', but those
// two are so much inkier than everything below them that any cell reaching
// them reads as a hard edge — which is the opposite of what this is for.
const blobRamp = " .:-=+*#"

// defaultFeedBlobFPS is the animation's frame rate when the config doesn't say
// (animations.feed_blob_fps). The drift runs in minutes, so a frame only ever
// moves a handful of cells one ramp step — five a second is enough that those
// steps don't arrive in visible batches. A poke is livelier, which is the
// reason the knob exists: a fast terminal can spend more frames on it. A frame
// is O(cells × blobs) float ops (a few hundred microseconds on a full pane) and
// only runs while the empty feed is actually on screen.
const defaultFeedBlobFPS = 5

// feedBlobFPSMin / feedBlobFPSMax bound the configured rate. Below the minimum
// the field stutters instead of drifting; above the maximum a full pane of
// float sampling is being redrawn more often than anyone can see, for a screen
// that exists to be ignored.
const (
	feedBlobFPSMin = 1
	feedBlobFPSMax = 60
)

// feedBlobInterval is the default frame gap, and the one the tests animate at.
const feedBlobInterval = time.Second / defaultFeedBlobFPS

// feedBlobIntervalFor turns a configured frame rate into the tick gap, clamped
// to the supported range. 0 (or anything absurd) means "use the default".
func feedBlobIntervalFor(fps int) time.Duration {
	if fps <= 0 {
		fps = defaultFeedBlobFPS
	}
	fps = min(max(fps, feedBlobFPSMin), feedBlobFPSMax)
	return time.Second / time.Duration(fps)
}

// cellAspect converts a column offset into row units: a terminal cell is about
// twice as tall as it is wide, so without this the blobs would be ellipses.
const cellAspect = 0.5

// blobPulse is how much a blob's radius breathes, as a fraction of it. Small
// on purpose: the mass should look like it's settling, not throbbing.
const blobPulse = 0.05

// A click in the field pushes the blobs under it away from the cursor — the
// one thing on this screen that answers back, and the reason it's worth
// leaving open. The push is a nudge on a spring: each blob carries an offset
// from its drift path, a click gives that offset a velocity, and a critically
// damped spring walks it back to zero over several seconds. Nothing snaps and
// nothing bounces; the field stays something to stare at, not something to
// play with.
const (
	// blobPushOmega is the spring's natural frequency, in rad/s — a period of
	// about fourteen seconds. Slow on purpose: the return takes the better part
	// of ten seconds, which is the difference between a lava lamp settling and a
	// button springing back.
	blobPushOmega = 0.45
	// blobPushZeta is the damping ratio: exactly critical. Underdamped adds a
	// wobble that reads as elastic, overdamped reads as sticky.
	blobPushZeta = 1.0
	// blobPushGain is this spring's peak response to a unit impulse, in units of
	// v₀/ω. For ζ = 1 that is exactly 1/e, at t = 1/ω.
	blobPushGain = 1 / math.E
	// blobPushSpan is the peak displacement of a best-placed click, as a
	// fraction of the pane's short side — about as far as the drift itself
	// travels, enough to read as the mass moving, not enough to throw it.
	blobPushSpan = 0.075
	// blobPushCap bounds the accumulated offset, as a fraction of the pane, so
	// a burst of clicking can't shove a blob off the canvas.
	blobPushCap = 0.22
)

// A poke also swells the blob it lands in, on a second spring. This one is
// deliberately underdamped: the size springs past its resting radius and
// settles back through one soft overshoot, which is what makes the mass read
// as something soft you pressed rather than a sprite that got scaled. The
// motion spring stays critically damped — a blob that both wobbled and bounced
// around the pane would be a fidget toy, not a lava lamp.
const (
	// blobSwellOmega is the swell spring's natural frequency, in rad/s. Much
	// faster than blobPushOmega, and that gap is the whole feel of a poke: the
	// blob squashes and recovers in well under a second while it is still
	// sliding slowly back into place.
	//
	// The rate also decides how the swell *reads*. A spring's impulse response
	// rises at a near-constant slope for the first third of its way to the peak,
	// so a slow one (this was 0.63, peaking 1.9 seconds in) animates as a
	// straight linear ramp with a flat top — visibly not physical. At 3.2 the
	// peak lands ~0.3s in and the whole arc sits inside the part of the curve
	// that eases.
	blobSwellOmega = 3.2
	// blobSwellZeta is just under half-critical damping — one soft overshoot
	// past the resting radius and out. Raise it and the elastic return
	// disappears; lower it and the blob rings like jelly.
	blobSwellZeta = 0.45
	// blobSwellSnap is how much of a poke's swell is applied as an instant
	// displacement rather than as a velocity kick, as a fraction of the drive.
	// Mouse-down has to *land*: a pure kick starts from zero with the spring's
	// near-linear opening slope, so the blob takes its time noticing it was
	// pressed. With this much snap the click frame lands about a sixth of the way
	// to the peak — enough that the press is felt on the frame it happened — and
	// the rest eases in over the next third of a second. Twice this read as a
	// pop: the blob was suddenly bigger rather than growing.
	//
	// Not all of it: a full step would be a pop with no growth to watch, and the
	// growth is the part that reads as goop.
	blobSwellSnap = 0.1
	// blobSizeSpan is the peak swell of a click on a blob's middle, as a
	// fraction of the blob's radius.
	blobSizeSpan = 0.4
	// blobSizeCap bounds the accumulated swell so a burst of clicking can't
	// inflate a blob over the whole pane.
	blobSizeCap = 2.6
)

// blobSwellGain is the peak of the swell spring's response to a unit drive
// (x₀ = blobSwellSnap, v₀ = (1−blobSwellSnap)·ω), in drive units. Derived from
// the closed form rather than tuned by hand, so blobSizeSpan keeps meaning the
// swell you actually see when the snap or the damping is changed.
var blobSwellGain = swellGain()

func swellGain() float64 {
	x0, v0 := blobSwellSnap, (1-blobSwellSnap)*blobSwellOmega
	lambda := blobSwellZeta * blobSwellOmega
	wd := blobSwellOmega * math.Sqrt(1-blobSwellZeta*blobSwellZeta)
	// x(t) = C·e^(−λt)·sin(ω_d·t + φ); it peaks where the sine's argument
	// reaches atan(ω_d/λ).
	b := (v0 + lambda*x0) / wd
	c, phi := math.Hypot(x0, b), math.Atan2(x0, b)
	tp := (math.Atan2(wd, lambda) - phi) / wd
	if tp <= 0 {
		return x0 // all snap: the click frame is the peak
	}
	return c * math.Exp(-lambda*tp) * wd / blobSwellOmega
}

// blobPushReach is how far a click reaches, in blob radii. Deliberately way
// past the visible rim: the margins are wide and soft because this is a toy,
// not a hitbox — a click in the empty space near a blob should still lean on
// it, and there should be no edge you can feel the field switch off at.
const blobPushReach = 2.4

// blobPushWeight is how hard a click at u = distance/radius leans on a blob.
// Zero dead on the centre, strongest a little outside the rim, back to zero
// once you're well clear of it — pressing a goopy blob square in the middle
// pushes it nowhere, and shoving it from the side is the only way to move it.
// One arch of a sine is the vaguest shape that does all three.
func blobPushWeight(u float64) float64 {
	if u <= 0 || u >= blobPushReach {
		return 0
	}
	return math.Sin(math.Pi * u / blobPushReach)
}

// blobSizeReach is how far a click reaches to squash a blob, in blob radii.
// Shorter than blobPushReach — the swell says "this is the one you pressed",
// and that has to be the blob under the pointer, not its neighbour.
const blobSizeReach = 1.8

// blobSwellWeight is how much a click at u = distance/radius swells a blob.
// The mirror of blobPushWeight: strongest where the push is weakest, dead on
// the centre, fading to nothing at the reach. Pressing goop square in the
// middle doesn't move it anywhere — it just spreads.
//
// A quarter-cosine, so it is flat on top: the swell doesn't care exactly where
// near the middle you hit, which keeps the whole thing vague to the touch.
func blobSwellWeight(u float64) float64 {
	if u >= blobSizeReach {
		return 0
	}
	return math.Cos(math.Pi * u / (2 * blobSizeReach))
}

// blobNudge is one blob's displacement from its drift path and the velocity
// carrying it, both as fractions of the pane (of width for x, height for y) so
// a resize doesn't teleport a mid-flight push.
// os is the swell, as a fraction of the blob's radius, with vs carrying it.
type blobNudge struct{ ox, oy, vx, vy, os, vs float64 }

// blobNudges is the push state for the whole cast. A fixed array, not a slice:
// it rides inside the Model, which is copied per event, and 128 bytes of value
// beats a heap allocation aliased across those copies.
type blobNudges [len(feedBlobs)]blobNudge

// advance steps the springs forward dt seconds. Offsets are clamped to their
// caps, and a clamped axis drops its velocity — a blob held at the limit has
// nothing left to spend going further.
func (n *blobNudges) advance(dt float64) {
	for i := range n {
		p := &n[i]
		ox, vx := springStep(p.ox, p.vx, blobPushOmega, blobPushZeta, dt)
		oy, vy := springStep(p.oy, p.vy, blobPushOmega, blobPushZeta, dt)
		os, vs := springStep(p.os, p.vs, blobSwellOmega, blobSwellZeta, dt)
		p.ox, p.vx = clampAbs(ox, vx, blobPushCap)
		p.oy, p.vy = clampAbs(oy, vy, blobPushCap)
		p.os, p.vs = clampAbs(os, vs, blobSizeCap)
	}
}

// springStep advances a damped spring's (offset, velocity) by dt seconds using
// the closed-form solution, not an Euler step. The frame rate is configurable
// (animations.feed_blob_fps), and a stepper whose amplitude and settling time
// depend on dt would quietly change how a poke feels when you asked for a
// smoother one. This way 5 fps and 60 fps run the same motion, sampled coarsely
// or finely.
func springStep(x, v, omega, zeta, dt float64) (float64, float64) {
	decay := math.Exp(-zeta * omega * dt)
	if zeta >= 1 { // critically damped: x(t) = (x₀ + (v₀ + ωx₀)t)·e^(−ωt)
		a := v + omega*x
		return (x + a*dt) * decay, (v - omega*a*dt) * decay
	}
	wd := omega * math.Sqrt(1-zeta*zeta) // damped frequency
	c, sn := math.Cos(wd*dt), math.Sin(wd*dt)
	lambda := zeta * omega
	return decay * (x*c + (v+lambda*x)/wd*sn),
		decay * (v*c - (omega*omega*x+lambda*v)/wd*sn)
}

// idle reports that every blob is back on its drift path, so the animation is
// pure drift again.
func (n *blobNudges) idle() bool {
	for _, p := range n {
		if math.Abs(p.ox) > 1e-4 || math.Abs(p.oy) > 1e-4 || math.Abs(p.os) > 1e-4 ||
			math.Abs(p.vx) > 1e-5 || math.Abs(p.vy) > 1e-5 || math.Abs(p.vs) > 1e-5 {
			return false
		}
	}
	return true
}

// clampAbs holds a spring's offset inside ±limit, zeroing the velocity that took
// it out of range: a blob pinned at the limit has nothing left to spend going
// further.
func clampAbs(o, v, limit float64) (float64, float64) {
	if o > limit {
		return limit, 0
	}
	if o < -limit {
		return -limit, 0
	}
	return o, v
}

// feedBlob is one metaball's motion: it drifts on two independent sines (a
// Lissajous path) and breathes on a third. All amplitudes are fractions of the
// pane, so the scene composes the same at any size.
type feedBlob struct {
	r          float64 // radius, as a fraction of the pane's short side
	ax, ay     float64 // drift amplitude, as a fraction of pane width / height
	wx, wy, wr float64 // angular speed (rad/s) of the x drift, y drift and radius pulse
	px, py, pr float64 // phase offsets, so no two blobs move in lockstep
}

// blobOmega converts a loop period into radians per second. Everything here is
// on a wall clock rather than a frame counter: the frame rate is configurable,
// and drift that sped up when you asked for smoother animation would be a bug.
func blobOmega(period time.Duration) float64 {
	return 2 * math.Pi / period.Seconds()
}

// feedBlobs is the cast. The drift amplitudes are small, so the blobs stay
// overlapped in the middle of the pane and read as one slowly kneading mass
// rather than four things chasing each other around it. The periods are
// mutually prime minutes: the combined field has no repeat a viewer could
// catch, and nothing in it moves fast enough to pull an eye off the composer.
var feedBlobs = [...]feedBlob{
	{r: 0.30, ax: 0.07, ay: 0.07, wx: blobOmega(181 * time.Second), wy: blobOmega(127 * time.Second), wr: blobOmega(149 * time.Second), px: 0.0, py: 1.3, pr: 0.4},
	{r: 0.24, ax: 0.09, ay: 0.06, wx: blobOmega(139 * time.Second), wy: blobOmega(167 * time.Second), wr: blobOmega(113 * time.Second), px: 2.1, py: 0.2, pr: 2.6},
	{r: 0.20, ax: 0.10, ay: 0.08, wx: blobOmega(211 * time.Second), wy: blobOmega(101 * time.Second), wr: blobOmega(173 * time.Second), px: 4.0, py: 3.4, pr: 1.1},
	{r: 0.16, ax: 0.11, ay: 0.07, wx: blobOmega(97 * time.Second), wy: blobOmega(229 * time.Second), wr: blobOmega(131 * time.Second), px: 5.2, py: 5.0, pr: 3.9},
}

// feedBlobPalette tints the ramp, one colour per shading step: near-neutral
// steel, cooling to a soft slate at the densest step so the core is the one
// thing with any colour in it.
//
// Both runs are short and stop well before the extremes. Neither end may
// approach the terminal's own foreground — a near-black step on a light
// background (or a near-white one on a dark background) puts the inkiest
// glyphs at full text contrast, and the whole thing starts reading as writing
// on the screen instead of a surface behind it. blobPaletteLimit in the tests
// holds that line. Index 0 is the blank step and is never rendered.
var feedBlobPalette = [len(blobRamp)]adaptiveColor{
	{light: lipgloss.Color("254"), dark: lipgloss.Color("236")}, // blank, never rendered
	{light: lipgloss.Color("253"), dark: lipgloss.Color("237")},
	{light: lipgloss.Color("251"), dark: lipgloss.Color("239")},
	{light: lipgloss.Color("249"), dark: lipgloss.Color("241")},
	{light: lipgloss.Color("247"), dark: lipgloss.Color("243")},
	{light: lipgloss.Color("245"), dark: lipgloss.Color("245")},
	{light: lipgloss.Color("243"), dark: lipgloss.Color("246")},
	{light: lipgloss.Color("60"), dark: lipgloss.Color("109")},
}

// feedBlobStyles is one style per ramp step, built once. adaptiveColor
// resolves against the terminal background at render time, so these need no
// rebuilding when the background is learned.
var feedBlobStyles = func() [len(blobRamp)]lipgloss.Style {
	var s [len(blobRamp)]lipgloss.Style
	for i, c := range feedBlobPalette {
		s[i] = lipgloss.NewStyle().Foreground(c)
	}
	return s
}()

// blobFalloff is Wyvill's kernel over q = (d/r)²: 1 at the centre, 0 at the
// edge, smooth in between. Finite support is the point — an inverse-square
// field would haze every cell in the pane up to ramp step 1 and there'd be no
// empty space left for the blobs to move through.
func blobFalloff(q float64) float64 {
	if q >= 1 {
		return 0
	}
	return 1 + q*(-22.0/9.0+q*(17.0/9.0-q*(4.0/9.0)))
}

// blobFrame is one blob resolved to pane coordinates for a single frame:
// centre in (column, row) and squared radius in row units.
type blobFrame struct{ cx, cy, r2 float64 }

// feedBlobFrame places every blob t seconds into the animation. Positions are
// computed once per frame, not once per cell.
func feedBlobFrame(w, h int, t float64, nudge blobNudges) []blobFrame {
	fw, fh := float64(w), float64(h)
	// Radii key off the pane's short side, measured in row units, so a narrow
	// pane shrinks the blobs instead of pushing them off both edges.
	scale := math.Min(fh, fw*cellAspect)
	out := make([]blobFrame, len(feedBlobs))
	for i, b := range feedBlobs {
		r := b.r * scale * (1 + blobPulse*math.Sin(b.wr*t+b.pr)) * (1 + nudge[i].os)
		out[i] = blobFrame{
			cx: fw/2 + b.ax*fw*math.Sin(b.wx*t+b.px) + nudge[i].ox*fw,
			cy: fh/2 + b.ay*fh*math.Sin(b.wy*t+b.py) + nudge[i].oy*fh,
			r2: r * r,
		}
	}
	return out
}

// renderFeedBlobs composites the whole w×h empty-feed canvas at t seconds:
// every cell is the summed field of all blobs, quantised onto blobRamp and
// tinted by its step. Overlapping blobs add, so they merge into one shape
// where they meet instead of drawing over each other.
//
// Every row is exactly w cells wide and there are exactly h of them — the feed
// viewport soft-wraps, and an overflowing row would reflow the whole scene.
func renderFeedBlobs(w, h int, t float64, nudge blobNudges) string {
	blobs := feedBlobFrame(w, h, t, nudge)
	level := make([]int, w)
	lines := make([]string, h)
	top := len(blobRamp) - 1
	for y := 0; y < h; y++ {
		fy := float64(y) + 0.5
		for x := 0; x < w; x++ {
			fx := float64(x) + 0.5
			var f float64
			for _, b := range blobs {
				dx := (fx - b.cx) * cellAspect
				dy := fy - b.cy
				f += blobFalloff((dx*dx + dy*dy) / b.r2)
			}
			level[x] = min(max(int(blobShade(f)*float64(top)+0.5), 0), top)
		}
		lines[y] = styleBlobRow(level)
	}
	return strings.Join(lines, "\n")
}

// blobShade compresses summed density into the 0..1 the ramp spans. The blobs
// sit on top of each other, so a plain sum would saturate the whole overlap to
// the densest glyph and the mass would render as a flat slab with a shaded
// rim. This keeps giving ground as density piles up, so the interior still
// carries a gradient and only a genuine pile-up reaches the top of the ramp.
func blobShade(f float64) float64 { return 1 - math.Exp(-f) }

// styleBlobRow renders one row of ramp steps, coalescing equal-step runs into
// a single styled span so the output stays compact. Blank runs are written
// raw — an empty cell needs no colour.
func styleBlobRow(level []int) string {
	var b strings.Builder
	for i := 0; i < len(level); {
		j := i
		for j < len(level) && level[j] == level[i] {
			j++
		}
		run := strings.Repeat(blobRamp[level[i]:level[i]+1], j-i)
		if level[i] == 0 {
			b.WriteString(run)
		} else {
			b.WriteString(feedBlobStyles[level[i]].Render(run))
		}
		i = j
	}
	return b.String()
}

// feedBlobTickMsg drives the empty-feed animation. At most one is in flight,
// guarded by feedState.blobActive.
type feedBlobTickMsg struct{}

// feedBlobTickCmd schedules the next frame, at the configured frame rate.
func feedBlobTickCmd(interval time.Duration) tea.Cmd {
	if interval <= 0 {
		interval = feedBlobInterval
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return feedBlobTickMsg{} })
}

// feedEmptyArtVisible reports whether the Feed tab is currently showing the
// animated empty-state art: on the tab, built, nothing unread, no error.
func (m *Model) feedEmptyArtVisible() bool {
	return m.onFeedTab() && !m.feed.loading && m.feed.err == "" && len(m.feed.entries) == 0
}

// maybeStartFeedBlobs arms the animation loop when the empty-state art is on
// screen and the loop isn't already running. Idempotent; returns nil when
// there's nothing to animate.
func (m *Model) maybeStartFeedBlobs() tea.Cmd {
	if m.feed.blobActive || !m.feedEmptyArtVisible() {
		return nil
	}
	m.feed.blobActive = true
	return feedBlobTickCmd(m.feed.blobInterval)
}

// applyFeedBlobTick advances one frame, redraws the field, and reschedules —
// stopping (and clearing the guard) the moment the art is no longer shown.
func (m *Model) applyFeedBlobTick() tea.Cmd {
	if !m.feedEmptyArtVisible() {
		m.feed.blobActive = false
		return nil
	}
	dt := m.feed.blobInterval
	if dt <= 0 {
		dt = feedBlobInterval
	}
	m.feed.blobPhase += dt.Seconds()
	m.feed.blobNudge.advance(dt.Seconds())
	m.renderFeedResults()
	return feedBlobTickCmd(dt)
}

// feedBlobMinW / feedBlobMinH are the smallest pane the field is worth drawing
// in. Below that the blobs have no room to move and it reads as noise, so a
// one-line hint takes over.
const (
	feedBlobMinW = 24
	feedBlobMinH = 8
)

// feedBlobFieldDrawn reports whether the animated field itself is on screen —
// the empty state *and* a pane big enough for it (below that, feedEmptyContent
// draws a one-line hint instead, and there is nothing to poke).
func (m *Model) feedBlobFieldDrawn() bool {
	return m.feedEmptyArtVisible() &&
		m.feed.view.Width() >= feedBlobMinW && m.feed.view.Height() >= feedBlobMinH
}

// pokeFeedBlobs presses cell (col,row) into the field. Two kernels, pulling
// opposite ways: blobPushWeight shoves a blob away — nothing on its centre,
// most just off its side — while blobSwellWeight swells it, hardest on the
// centre. So a middle press spreads the blob you hit, a side press slides it,
// and a click well clear of everything does neither.
func (m *Model) pokeFeedBlobs(col, row int) {
	w, h := m.feed.view.Width(), m.feed.view.Height()
	if w <= 0 || h <= 0 {
		return
	}
	fw, fh := float64(w), float64(h)
	// Impulses (per second) that a best-placed click turns into exactly
	// blobPushSpan of travel and blobSizeSpan of swell: a spring kicked at v₀
	// peaks at gain·v₀/ω.
	imp := blobPushSpan * math.Min(fh, fw*cellAspect) * blobPushOmega / blobPushGain
	swell := blobSizeSpan / blobSwellGain
	px, py := float64(col)+0.5, float64(row)+0.5
	for i, b := range feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge) {
		// Row units, so the push is as far up as it is sideways on screen.
		dx := (b.cx - px) * cellAspect
		dy := b.cy - py
		d := math.Hypot(dx, dy)
		u := d / math.Sqrt(b.r2)
		// The swell drive is split: part lands as displacement on this very frame
		// (mouse-down is felt at once), the rest as velocity the spring eases in.
		if k := swell * blobSwellWeight(u); k > 0 {
			n := &m.feed.blobNudge[i]
			n.os, n.vs = clampAbs(n.os+blobSwellSnap*k, n.vs+(1-blobSwellSnap)*k*blobSwellOmega, blobSizeCap)
		}
		weight := blobPushWeight(u)
		if weight <= 0 || d < 1e-9 {
			continue // on the centre, or well clear of the blob: nowhere to push it
		}
		v := imp * weight / d // magnitude on the unit vector away from the cell
		m.feed.blobNudge[i].vx += v * dx / (cellAspect * fw)
		m.feed.blobNudge[i].vy += v * dy / fh
	}
}

// clickFeedBlobs is the mouse entry point: poke the field, repaint it at once
// (waiting for the next tick would put a frame of lag on the one thing here
// that responds to you), and make sure the animation loop is running.
func (m *Model) clickFeedBlobs(col, row int) tea.Cmd {
	m.pokeFeedBlobs(col, row)
	m.renderFeedResults()
	return m.maybeStartFeedBlobs()
}

// feedEmptyContent is the body shown when the feed has no entries: the
// animated blob field when there's room for it, otherwise a one-line hint.
func (m *Model) feedEmptyContent() string {
	w, h := m.feed.view.Width(), m.feed.view.Height()
	if w < feedBlobMinW || h < feedBlobMinH {
		return lipgloss.NewStyle().Foreground(dimColor).Render("  all caught up — nothing unread")
	}
	return renderFeedBlobs(w, h, m.feed.blobPhase, m.feed.blobNudge)
}
