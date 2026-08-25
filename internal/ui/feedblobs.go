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

// feedBlobInterval is the gap between animation frames. The drift runs in
// minutes, so a frame only ever moves a handful of cells one ramp step — this
// just has to be often enough that those steps don't arrive in visible
// batches. A frame is O(cells × blobs) float ops (a few hundred microseconds
// on a full pane) and only runs while the empty feed is actually on screen.
const feedBlobInterval = 200 * time.Millisecond

// cellAspect converts a column offset into row units: a terminal cell is about
// twice as tall as it is wide, so without this the blobs would be ellipses.
const cellAspect = 0.5

// blobPulse is how much a blob's radius breathes, as a fraction of it. Small
// on purpose: the mass should look like it's settling, not throbbing.
const blobPulse = 0.05

// feedBlob is one metaball's motion: it drifts on two independent sines (a
// Lissajous path) and breathes on a third. All amplitudes are fractions of the
// pane, so the scene composes the same at any size.
type feedBlob struct {
	r          float64 // radius, as a fraction of the pane's short side
	ax, ay     float64 // drift amplitude, as a fraction of pane width / height
	wx, wy, wr float64 // angular speed of the x drift, y drift and radius pulse
	px, py, pr float64 // phase offsets, so no two blobs move in lockstep
}

// blobOmega converts a loop period into radians per animation frame.
func blobOmega(period time.Duration) float64 {
	return 2 * math.Pi * float64(feedBlobInterval) / float64(period)
}

// feedBlobs is the cast. The drift amplitudes are small, so the blobs stay
// overlapped in the middle of the pane and read as one slowly kneading mass
// rather than four things chasing each other around it. The periods are
// mutually prime minutes: the combined field has no repeat a viewer could
// catch, and nothing in it moves fast enough to pull an eye off the composer.
var feedBlobs = []feedBlob{
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

// feedBlobFrame places every blob for the given frame. Positions are computed
// once per frame, not once per cell.
func feedBlobFrame(w, h, phase int) []blobFrame {
	t := float64(phase)
	fw, fh := float64(w), float64(h)
	// Radii key off the pane's short side, measured in row units, so a narrow
	// pane shrinks the blobs instead of pushing them off both edges.
	scale := math.Min(fh, fw*cellAspect)
	out := make([]blobFrame, len(feedBlobs))
	for i, b := range feedBlobs {
		r := b.r * scale * (1 + blobPulse*math.Sin(b.wr*t+b.pr))
		out[i] = blobFrame{
			cx: fw/2 + b.ax*fw*math.Sin(b.wx*t+b.px),
			cy: fh/2 + b.ay*fh*math.Sin(b.wy*t+b.py),
			r2: r * r,
		}
	}
	return out
}

// renderFeedBlobs composites the whole w×h empty-feed canvas for one frame:
// every cell is the summed field of all blobs, quantised onto blobRamp and
// tinted by its step. Overlapping blobs add, so they merge into one shape
// where they meet instead of drawing over each other.
//
// Every row is exactly w cells wide and there are exactly h of them — the feed
// viewport soft-wraps, and an overflowing row would reflow the whole scene.
func renderFeedBlobs(w, h, phase int) string {
	blobs := feedBlobFrame(w, h, phase)
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

// feedBlobTickCmd schedules the next frame.
func feedBlobTickCmd() tea.Cmd {
	return tea.Tick(feedBlobInterval, func(time.Time) tea.Msg { return feedBlobTickMsg{} })
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
	return feedBlobTickCmd()
}

// applyFeedBlobTick advances one frame, redraws the field, and reschedules —
// stopping (and clearing the guard) the moment the art is no longer shown.
func (m *Model) applyFeedBlobTick() tea.Cmd {
	if !m.feedEmptyArtVisible() {
		m.feed.blobActive = false
		return nil
	}
	m.feed.blobPhase++
	m.renderFeedResults()
	return feedBlobTickCmd()
}

// feedBlobMinW / feedBlobMinH are the smallest pane the field is worth drawing
// in. Below that the blobs have no room to move and it reads as noise, so a
// one-line hint takes over.
const (
	feedBlobMinW = 24
	feedBlobMinH = 8
)

// feedEmptyContent is the body shown when the feed has no entries: the
// animated blob field when there's room for it, otherwise a one-line hint.
func (m *Model) feedEmptyContent() string {
	w, h := m.feed.view.Width(), m.feed.view.Height()
	if w < feedBlobMinW || h < feedBlobMinH {
		return lipgloss.NewStyle().Foreground(dimColor).Render("  all caught up — nothing unread")
	}
	return renderFeedBlobs(w, h, m.feed.blobPhase)
}
