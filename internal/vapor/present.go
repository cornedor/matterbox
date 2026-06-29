package vapor

import (
	"bytes"
	"strconv"
	"sync"
)

// u8dec is the decimal text of every byte value, so writeSGR emits colour
// channels without strconv.Itoa allocating a string per channel per cell.
var u8dec = func() [256]string {
	var t [256]string
	for i := range t {
		t[i] = strconv.Itoa(i)
	}
	return t
}()

// serializeBufPool reuses the output buffer across frames: the animation
// serializes a fresh grid ~12–30×/s, and a grown buffer kept its capacity, so
// only the final String() copy allocates (down from thousands of allocs/frame).
var serializeBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

const (
	fgPrefix = "\x1b[38;2;" // 24-bit foreground SGR introducer
	bgPrefix = "\x1b[48;2;" // 24-bit background SGR introducer
)

// Cell is one terminal character cell of a rendered frame: a rune drawn in a
// 24-bit foreground colour over an optional 24-bit background. The scene fills
// every cell with a background (HasBg true); HasBg is false only for cells an
// overlay leaves on the terminal's default background.
type Cell struct {
	R     rune
	Fg    RGB
	Bg    RGB
	HasBg bool
}

func packC(c RGB) int32 {
	r, g, b := c.u8()
	return int32(r)<<16 | int32(g)<<8 | int32(b)
}

// writeSGR appends a 24-bit colour escape; prefix is fgPrefix or bgPrefix.
func writeSGR(o *bytes.Buffer, prefix string, c RGB) {
	r, g, b := c.u8()
	o.WriteString(prefix)
	o.WriteString(u8dec[r])
	o.WriteByte(';')
	o.WriteString(u8dec[g])
	o.WriteByte(';')
	o.WriteString(u8dec[b])
	o.WriteByte('m')
}

// presentBlocksCells renders the framebuffer into a cell grid with half-block
// glyphs: each cell shows two stacked pixels (top = foreground, bottom =
// background), doubling vertical resolution for the smoothest colour.
func presentBlocksCells(grid [][]Cell, buf []RGB, W, sceneRows int) {
	for r := 0; r < sceneRows; r++ {
		top := 2 * r * W
		bot := (2*r + 1) * W
		dst := grid[r]
		for x := 0; x < W; x++ {
			dst[x] = Cell{R: '▀', Fg: buf[top+x], Bg: buf[bot+x], HasBg: true}
		}
	}
}

const asciiRamp = " .:-=+*#%@"

// presentAsciiCells renders the framebuffer into a cell grid as classic ASCII
// art: one ramp glyph per cell, tinted with the pixel colour over black so the
// serialized output composites the same way as the block/glyph modes.
func presentAsciiCells(grid [][]Cell, buf []RGB, W, sceneRows int) {
	for r := 0; r < sceneRows; r++ {
		base := r * W
		dst := grid[r]
		for x := 0; x < W; x++ {
			c := buf[base+x]
			l := c.lum() / 255
			idx := min(max(int(l*float64(len(asciiRamp)-1)+0.5), 0), len(asciiRamp)-1)
			dst[x] = Cell{R: rune(asciiRamp[idx]), Fg: scale(c, 1.25), Bg: RGB{}, HasBg: true}
		}
	}
}

// serialize turns a cell grid into a single newline-joined string of 24-bit
// SGR escapes. Colour state is tracked within a line (fewer escapes) and reset
// at every line break so each line is self-contained for the terminal renderer.
func serialize(grid [][]Cell) string {
	b := serializeBufPool.Get().(*bytes.Buffer)
	b.Reset()
	for y, row := range grid {
		if y > 0 {
			b.WriteByte('\n')
		}
		lastFg, lastBg := int32(-1), int32(-1)
		hadBg := true
		for _, c := range row {
			if fg := packC(c.Fg); fg != lastFg {
				writeSGR(b, fgPrefix, c.Fg)
				lastFg = fg
			}
			if c.HasBg {
				if bg := packC(c.Bg); bg != lastBg || !hadBg {
					writeSGR(b, bgPrefix, c.Bg)
					lastBg = bg
					hadBg = true
				}
			} else if hadBg {
				b.WriteString("\x1b[49m")
				hadBg = false
			}
			b.WriteRune(c.R)
		}
		b.WriteString("\x1b[0m")
	}
	s := b.String()
	serializeBufPool.Put(b)
	return s
}
