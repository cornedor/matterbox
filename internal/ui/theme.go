package ui

import (
	"image/color"
	"sync/atomic"

	"charm.land/lipgloss/v2"
)

// Background-sensitive surfaces: the subtle tints that are supposed to read as
// "one step off the page" — hover bars, chip fills, the jump-to-bottom pill. A
// grey that sits just above a dark background sits far *below* a light one,
// where it stops being a tint and becomes a black bar with unreadable text on
// it. Each of these colours therefore comes as a pair, and the pair is resolved
// against the terminal's own background rather than a fixed assumption.
//
// The background is learned at runtime: Init issues tea.RequestBackgroundColor
// and the answer arrives as a tea.BackgroundColorMsg (see update.go). Terminals
// that never answer keep the dark assumption the app has always made.

// lightBackground records the terminal's background as reported by the OSC 11
// query. The zero value (false = dark) is deliberately the fallback, so a
// terminal that stays silent renders exactly as it did before this existed.
// Atomic because it is written from Update while renders read it.
var lightBackground atomic.Bool

// adaptiveColor is one surface in both flavours. lipgloss resolves a style's
// colour at render time, so styles built from these re-tint on the next render
// once the background is known — none of them need rebuilding.
type adaptiveColor struct{ light, dark color.Color }

// RGBA satisfies color.Color.
func (c adaptiveColor) RGBA() (uint32, uint32, uint32, uint32) {
	if lightBackground.Load() {
		return c.light.RGBA()
	}
	return c.dark.RGBA()
}

var (
	// hoverRowBg tints the sidebar row under the pointer — quieter than the
	// selected row, which owns the brighter fill.
	hoverRowBg = adaptiveColor{light: lipgloss.Color("253"), dark: lipgloss.Color("237")}

	// panelHoverBg is the same "under the pointer" cue for the denser rows of
	// the channel-info / ref panels, and for a hovered hyperlink in a message.
	panelHoverBg = adaptiveColor{light: lipgloss.Color("253"), dark: lipgloss.Color("238")}

	// chipBg fills the little pills — reaction chips and inline MR badges. It
	// also paints their powerline caps (as a foreground), so the pill reads as
	// one rounded shape against the terminal background.
	chipBg = adaptiveColor{light: lipgloss.Color("253"), dark: lipgloss.Color("238")}

	// jumpPillBg / jumpPillHoverBg raise the jump-to-bottom pill off the
	// transcript, the hover state one step further from the page background in
	// whichever direction that background runs.
	jumpPillBg      = adaptiveColor{light: lipgloss.Color("252"), dark: lipgloss.Color("238")}
	jumpPillHoverBg = adaptiveColor{light: lipgloss.Color("247"), dark: lipgloss.Color("242")}
	// jumpPillFg is the pill's label: white on the dark fill, near-black on the
	// light one.
	jumpPillFg = adaptiveColor{light: lipgloss.Color("235"), dark: lipgloss.Color("15")}
)

// setLightBackground records a newly reported terminal background. It reports
// whether the value changed — the caller drops the render caches when it did,
// since those hold bytes that were styled under the old assumption.
func setLightBackground(light bool) bool {
	return lightBackground.Swap(light) != light
}
