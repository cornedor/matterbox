package ui

import (
	_ "embed"
	"math"
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

// feedWaveInterval is the gap between wave-animation frames. Deliberately
// slow — the empty feed is a calm "all caught up" moment, not a busy
// animation, and a lazy redraw cadence keeps the cost negligible.
const feedWaveInterval = 480 * time.Millisecond

var (
	// feedShipStyle / feedWaveStyle two-tone the splash: the boat is a quiet
	// grey so it reads as line-art, the ~ water a soft blue so the motion
	// catches the eye without shouting.
	feedShipStyle = lipgloss.NewStyle().Foreground(dimColor)
	feedWaveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")) // deep-sky-blue water
)

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

// renderFeedArt composites the splash for one animation phase: the fixed
// boat layer with each row's waves drifted by waveOffset. A wave that drifts
// onto a boat glyph is dropped for that frame (the water passes behind the
// hull); a wave that lands on open water is drawn.
func renderFeedArt(phase int) string {
	out := make([]string, len(feedArtRows))
	for r, row := range feedArtRows {
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
		out[r] = styleFeedArtLine(buf)
	}
	return strings.Join(out, "\n")
}

// styleFeedArtLine colours one composited row: ~ runs blue, line-art runs
// grey, and blank gaps are written raw so the styled output stays compact.
func styleFeedArtLine(runes []rune) string {
	var b strings.Builder
	for i := 0; i < len(runes); {
		switch {
		case runes[i] == '~':
			j := i
			for j < len(runes) && runes[j] == '~' {
				j++
			}
			b.WriteString(feedWaveStyle.Render(string(runes[i:j])))
			i = j
		case runes[i] == ' ':
			j := i
			for j < len(runes) && runes[j] == ' ' {
				j++
			}
			b.WriteString(string(runes[i:j]))
			i = j
		default:
			j := i
			for j < len(runes) && runes[j] != '~' && runes[j] != ' ' {
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
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, renderFeedArt(m.feed.wavePhase))
}
