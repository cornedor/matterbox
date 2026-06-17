package ui

import (
	_ "embed"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// feedArtRaw is the ship-on-calm-water splash drawn on the Feed tab when
// nothing is unread. Embedded from a sibling .txt so the backticks and
// backslashes in the art need no Go-string escaping.
//
//go:embed feedart.txt
var feedArtRaw string

// birdArtRaw holds the gull's wing-flap frames, separated by "===" lines and
// embedded (like feedArtRaw) so the backticks/backslashes in the art need no
// Go-string escaping. The gull glides left→right across the empty-feed sky.
//
//go:embed birdart.txt
var birdArtRaw string

// feedWaveInterval is the gap between wave-animation frames. Deliberately
// slow — the empty feed is a calm "all caught up" moment, not a busy
// animation, and a lazy redraw cadence keeps the cost negligible.
const feedWaveInterval = 480 * time.Millisecond

var (
	// feedShipStyle / feedWaveStyle two-tone the splash: the boat is a quiet
	// grey so it reads as line-art, the ~ water a soft blue so the motion
	// catches the eye without shouting.
	feedShipStyle = lipgloss.NewStyle().Foreground(dimColor)
	feedWaveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))  // deep-sky-blue water
	feedBirdStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252")) // soft near-white gull
)

// Bird-flight tuning. The gull is stepped once per wave frame (feedWaveInterval),
// so one column-step is one frame and gaps measured in time convert to frames
// by dividing by feedWaveInterval.
const (
	birdSpeed   = 2 // columns advanced per wave frame — a slow, distant glide
	birdTopRow  = 1 // rows below the splash's top where the gull's base sky row sits
	birdYJitter = 8 // each fly-by's height is randomized ± this many rows

	// A fly-by happens a few times an hour: after one crosses, the gull idles a
	// random gap in [birdGapMin, birdGapMax] before the next. The first one after
	// the splash appears uses the shorter birdGapFirst so it isn't a long wait.
	birdGapMin   = 11 * time.Minute
	birdGapMax   = 21 * time.Minute
	birdGapFirst = 30 * time.Second
)

// randBirdGap picks the idle gap (in wave frames) before the next fly-by,
// uniformly within [birdGapMin, birdGapMax]. Averages ~16 min → ~4 birds/hour.
func randBirdGap() int {
	lo := int(birdGapMin / feedWaveInterval)
	hi := int(birdGapMax / feedWaveInterval)
	return lo + rand.IntN(hi-lo+1)
}

// randBirdYOff picks a fly-by's vertical offset from the base sky row,
// uniformly within [-birdYJitter, +birdYJitter].
func randBirdYOff() int { return rand.IntN(2*birdYJitter+1) - birdYJitter }

// birdArt is one parsed flap cycle: every frame padded to the same w×h grid
// (space = transparent) so a frame can be stamped over the splash at any
// position without disturbing the cells its blanks leave alone.
type birdArt struct {
	frames [][][]rune
	w, h   int
}

var gull = parseBirdFrames(birdArtRaw)

// birdTravel is how many frames a full left→right crossing of a screenW-wide
// scene takes: the gull enters fully offscreen-left (x = -w) and leaves once x
// clears the right edge.
func birdTravel(screenW int) int {
	return (screenW + gull.w + birdSpeed - 1) / birdSpeed
}

// parseBirdFrames splits the "==="-delimited art into frames and pads each to a
// common w×h rectangle (blanks fill short/missing rows). The frames are drawn
// pre-aligned (each body line at the same row), so across the cycle the body
// holds level and only the wings flap — no big vertical bob.
func parseBirdFrames(s string) birdArt {
	blocks := strings.Split(strings.TrimRight(s, "\n"), "\n===\n")
	raw := make([][]string, len(blocks))
	var w, h int
	for i, blk := range blocks {
		lines := strings.Split(blk, "\n")
		raw[i] = lines
		if len(lines) > h {
			h = len(lines)
		}
		for _, ln := range lines {
			if n := len([]rune(ln)); n > w {
				w = n
			}
		}
	}
	frames := make([][][]rune, len(raw))
	for i, lines := range raw {
		grid := make([][]rune, h)
		for r := range grid {
			row := make([]rune, w)
			for c := range row {
				row[c] = ' '
			}
			if r < len(lines) {
				for c, g := range []rune(lines[r]) {
					if c < w {
						row[c] = g
					}
				}
			}
			grid[r] = row
		}
		frames[i] = grid
	}
	return birdArt{frames: frames, w: w, h: h}
}

// advanceFeedBird steps the gull's fly-by state machine one wave frame: it
// counts down the idle gap, launches a fly-by (at a fresh random sky height)
// when the gap elapses, advances an in-progress crossing, and on completion
// schedules the next random gap. screenW sets how far the crossing runs.
// Called once per wave frame from applyFeedWaveTick.
func (m *Model) advanceFeedBird(screenW int) {
	f := &m.feed
	switch {
	case f.birdActive:
		f.birdStep++
		if f.birdStep >= birdTravel(screenW) { // crossed the full width — done
			f.birdActive = false
			f.birdWait = randBirdGap()
		}
	case f.birdWait > 0:
		f.birdWait--
	default:
		f.birdActive = true
		f.birdStep = 0
		f.birdYOff = randBirdYOff()
	}
}

// overlayBird stamps the gull's frame row that falls on art row r (given its
// top at btop) into buf, skipping transparent (space) cells and anything that
// would fall outside the block. It returns the columns it touched so the
// styler can tint them as the bird rather than as water/line-art, or nil when
// the gull doesn't reach this row.
func overlayBird(buf []rune, r, top, frame, x int) []bool {
	fr := r - top
	if fr < 0 || fr >= gull.h {
		return nil
	}
	var mask []bool
	for c, g := range gull.frames[frame][fr] {
		if g == ' ' {
			continue
		}
		col := x + c
		if col < 0 || col >= len(buf) {
			continue
		}
		if mask == nil {
			mask = make([]bool, len(buf))
		}
		buf[col] = g
		mask[col] = true
	}
	return mask
}

// feedArtRow splits one row of the splash into its two layers: the fixed
// boat/line-art glyphs (boat, with the ~ cells blanked to spaces) and the
// columns that originally held a wave. The wave layer is the only thing
// that moves; the boat is rendered identically every frame.
type feedArtRow struct {
	boat  []rune // fixed glyphs, width-padded, ~ cells blanked to ' '
	waves []int  // columns that held a '~' in the source art
}

var (
	feedArtLines = splitFeedArt(feedArtRaw)
	// feedArtBlockWidth pads two columns past the widest line so a wave near
	// the right edge has room to drift outward instead of being clipped. It's
	// also the rendered block's true width, so the fit check below keys on it
	// (the feed viewport soft-wraps, which would mangle an overflowing block).
	feedArtBlockWidth = artMaxWidth(feedArtLines) + 2
	feedArtRows       = buildFeedArtRows(feedArtLines, feedArtBlockWidth)
)

// splitFeedArt breaks the embedded art into rows, dropping only the trailing
// newline(s) so interior spacing (and the art's own trailing spaces) survive.
func splitFeedArt(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// artMaxWidth returns the rune-width of the widest row.
func artMaxWidth(lines []string) int {
	w := 0
	for _, ln := range lines {
		if n := len([]rune(ln)); n > w {
			w = n
		}
	}
	return w
}

// buildFeedArtRows pre-splits every line into its boat and wave layers,
// padding the boat layer to w so the block is a clean rectangle (which keeps
// lipgloss centering stable and gives edge waves room to drift).
func buildFeedArtRows(lines []string, w int) []feedArtRow {
	rows := make([]feedArtRow, len(lines))
	for i, ln := range lines {
		boat := make([]rune, w)
		for c := range boat {
			boat[c] = ' '
		}
		var waves []int
		for c, r := range []rune(ln) {
			if c >= w {
				break
			}
			if r == '~' {
				waves = append(waves, c) // boat[c] stays a blank
			} else {
				boat[c] = r
			}
		}
		rows[i] = feedArtRow{boat: boat, waves: waves}
	}
	return rows
}

// waveOffset is the horizontal drift, in columns, applied to a row's waves
// at the given animation phase. A small-amplitude sine gives a gentle
// side-to-side sway; the per-row phase term keeps the rows from sliding as
// one rigid sheet, so the water reads as lapping rather than scrolling.
func waveOffset(row, phase int) int {
	return int(math.Round(1.8 * math.Sin(float64(phase)*0.18+float64(row)*0.6)))
}

// composeFeedArtRow builds one art row's runes for the given phase: the fixed
// boat layer with that row's waves drifted by waveOffset. A wave that drifts
// onto a boat glyph is dropped (the water passes behind the hull); a wave that
// lands on open water is drawn. Width is feedArtBlockWidth; the gull is not
// drawn here — it's overlaid at scene scale so it can fly the full screen.
func composeFeedArtRow(r, phase int) []rune {
	row := feedArtRows[r]
	buf := make([]rune, len(row.boat))
	copy(buf, row.boat)
	off := waveOffset(r, phase)
	for _, c := range row.waves {
		nc := c + off
		if nc < 0 || nc >= len(buf) {
			continue
		}
		if buf[nc] == ' ' {
			buf[nc] = '~'
		}
	}
	return buf
}

// renderFeedScene composites the whole w×h empty-feed canvas: the ship-on-water
// splash centered in the viewport (waves drifted by phase), and — when birdShow
// — the gull gliding the full width of the screen on top. birdStep is how far
// into its crossing the gull is (it enters offscreen-left and exits past the
// right edge, not just inside the centered art block); birdYOff jitters its
// flight height. The gull is stamped last, so it passes in front of the ship.
//
// birdShow gates the moving gull so the static, centered splash — whose only
// animation is the lapping water — stays testable on its own.
func renderFeedScene(w, h, phase int, birdShow bool, birdStep, birdYOff int) string {
	artH := len(feedArtLines)
	leftPad := (w - feedArtBlockWidth) / 2
	if leftPad < 0 {
		leftPad = 0
	}
	topPad := (h - artH) / 2
	if topPad < 0 {
		topPad = 0
	}

	bx := -gull.w + birdStep*birdSpeed
	frame := birdStep % len(gull.frames)
	// Base sky row plus this fly-by's jitter, clamped so the gull stays fully
	// on screen even at the extremes of birdYJitter.
	birdTop := topPad + birdTopRow + birdYOff
	if max := h - gull.h; birdTop > max {
		birdTop = max
	}
	if birdTop < 0 {
		birdTop = 0
	}

	lines := make([]string, h)
	for y := 0; y < h; y++ {
		buf := make([]rune, w)
		for x := range buf {
			buf[x] = ' '
		}
		if ar := y - topPad; ar >= 0 && ar < artH {
			for i, g := range composeFeedArtRow(ar, phase) {
				if x := leftPad + i; x >= 0 && x < w {
					buf[x] = g
				}
			}
		}
		var bird []bool
		if birdShow {
			bird = overlayBird(buf, y, birdTop, frame, bx)
		}
		lines[y] = styleFeedArtLine(buf, bird)
	}
	return strings.Join(lines, "\n")
}

// styleFeedArtLine colours one composited row: gull cells (bird[i]) near-white,
// ~ runs blue, line-art runs grey, and blank gaps written raw so the styled
// output stays compact. bird may be nil (no gull on this row). Bird cells are
// tested first and break every run, so a gull glyph that happens to be '~'
// still reads as bird, not water.
func styleFeedArtLine(runes []rune, bird []bool) string {
	isBird := func(i int) bool { return i < len(bird) && bird[i] }
	var b strings.Builder
	for i := 0; i < len(runes); {
		switch {
		case isBird(i):
			j := i
			for j < len(runes) && isBird(j) {
				j++
			}
			b.WriteString(feedBirdStyle.Render(string(runes[i:j])))
			i = j
		case runes[i] == '~':
			j := i
			for j < len(runes) && runes[j] == '~' && !isBird(j) {
				j++
			}
			b.WriteString(feedWaveStyle.Render(string(runes[i:j])))
			i = j
		case runes[i] == ' ':
			j := i
			for j < len(runes) && runes[j] == ' ' && !isBird(j) {
				j++
			}
			b.WriteString(string(runes[i:j]))
			i = j
		default:
			j := i
			for j < len(runes) && runes[j] != '~' && runes[j] != ' ' && !isBird(j) {
				j++
			}
			b.WriteString(feedShipStyle.Render(string(runes[i:j])))
			i = j
		}
	}
	return b.String()
}

// feedWaveTickMsg drives the empty-feed wave animation. At most one is in
// flight, guarded by feedState.waveActive.
type feedWaveTickMsg struct{}

// feedWaveTickCmd schedules the next wave frame.
func feedWaveTickCmd() tea.Cmd {
	return tea.Tick(feedWaveInterval, func(time.Time) tea.Msg { return feedWaveTickMsg{} })
}

// feedEmptyArtVisible reports whether the Feed tab is currently showing the
// animated empty-state art: on the tab, built, nothing unread, no error.
func (m *Model) feedEmptyArtVisible() bool {
	return m.onFeedTab() && !m.feed.loading && m.feed.err == "" && len(m.feed.entries) == 0
}

// maybeStartFeedWaves arms the wave-animation loop when the empty-state art
// is on screen and the loop isn't already running. Idempotent; returns nil
// when there's nothing to animate.
func (m *Model) maybeStartFeedWaves() tea.Cmd {
	if m.feed.waveActive || !m.feedEmptyArtVisible() {
		return nil
	}
	m.feed.waveActive = true
	return feedWaveTickCmd()
}

// applyFeedWaveTick advances one frame, redraws the splash, and reschedules
// — stopping (and clearing the guard) the moment the art is no longer shown.
func (m *Model) applyFeedWaveTick() tea.Cmd {
	if !m.feedEmptyArtVisible() {
		m.feed.waveActive = false
		return nil
	}
	m.feed.wavePhase++
	m.advanceFeedBird(m.feed.view.Width())
	m.renderFeedResults()
	return feedWaveTickCmd()
}

// feedEmptyContent is the body shown when the feed has no entries: the
// centered, animated splash when it fits, otherwise a one-line hint for
// terminals too small to hold the art.
func (m *Model) feedEmptyContent() string {
	w, h := m.feed.view.Width(), m.feed.view.Height()
	if w < feedArtBlockWidth || h < len(feedArtLines) {
		return lipgloss.NewStyle().Foreground(dimColor).Render("  all caught up — nothing unread")
	}
	return renderFeedScene(w, h, m.feed.wavePhase, m.feed.birdActive, m.feed.birdStep, m.feed.birdYOff)
}
