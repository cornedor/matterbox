package editor

import (
	"image/color"
	"math"
)

// Command-highlight support: a single span (the leading "/command" token the
// composer recognises) drawn bold with an animated orange gradient that loops
// across it — the same shimmer a skeleton-loader uses to read as "live". The
// editor only draws what it is told; recognising the command and driving the
// frame loop live in the ui layer.

// SetCommandSpan marks [start, end) (rune offsets into Value) as a recognised
// command, drawn bold with the animated shimmer. A zero-width or inverted span
// clears the highlight instead.
func (m *Model) SetCommandSpan(start, end int) {
	if end <= start || start < 0 {
		m.ClearCommandSpan()
		return
	}
	m.cmdActive, m.cmdStart, m.cmdEnd = true, start, end
}

// ClearCommandSpan removes the command highlight.
func (m *Model) ClearCommandSpan() {
	m.cmdActive, m.cmdStart, m.cmdEnd = false, 0, 0
}

// CommandSpan reports the current command span and whether one is set.
func (m *Model) CommandSpan() (start, end int, ok bool) {
	return m.cmdStart, m.cmdEnd, m.cmdActive
}

// SetCommandPhase sets the shimmer phase, wrapped into [0,1). Advancing it once
// per animation frame slides the gradient band across the span.
func (m *Model) SetCommandPhase(p float64) {
	m.cmdPhase = p - math.Floor(p)
}

// commandSpanAt reports whether rune offset off falls inside the active command
// span, and its position relative to the span start (for the gradient).
func (m *Model) commandSpanAt(off int) (posInSpan int, ok bool) {
	if !m.cmdActive || off < m.cmdStart || off >= m.cmdEnd {
		return 0, false
	}
	return off - m.cmdStart, true
}

// shimmer gradient endpoints: a resting deep orange and a bright warm highlight
// that the moving band lifts the text to. Truecolor terminals get a smooth
// fade; lipgloss degrades the interpolated colours to the 256-palette
// elsewhere.
var (
	shimmerDim    = color.RGBA{R: 0xc8, G: 0x64, B: 0x14, A: 0xff} // #c86414 deep orange
	shimmerBright = color.RGBA{R: 0xff, G: 0xdd, B: 0xa0, A: 0xff} // #ffdda0 light warm
)

// commandShimmerSpread is the half-width, in cells, of the bright band that
// sweeps across the command. Wide enough to read as a soft glow rather than a
// single lit cell.
const commandShimmerSpread = 3.0

// shimmerColor returns the foreground colour for the cell at position pos within
// a span of n cells, given the current phase in [0,1). A bright band sweeps from
// before the span to past its end and loops, interpolating dim→bright→dim — the
// skeleton-loader shimmer. The band fully exits at both ends of its travel, so
// the colour at the phase wrap is identical on either side (no visible seam).
func shimmerColor(pos, n int, phase float64) color.Color {
	travel := float64(n) + 2*commandShimmerSpread
	centre := phase*travel - commandShimmerSpread
	d := math.Abs(float64(pos) - centre)
	t := 1 - d/commandShimmerSpread
	if t < 0 {
		t = 0
	}
	t = t * t * (3 - 2*t) // smoothstep — a rounded peak, not a linear ramp
	return color.RGBA{
		R: lerpByte(shimmerDim.R, shimmerBright.R, t),
		G: lerpByte(shimmerDim.G, shimmerBright.G, t),
		B: lerpByte(shimmerDim.B, shimmerBright.B, t),
		A: 0xff,
	}
}

// lerpByte linearly interpolates between two 8-bit channels at t in [0,1].
func lerpByte(a, b uint8, t float64) uint8 {
	return uint8(float64(a) + (float64(b)-float64(a))*t + 0.5)
}
