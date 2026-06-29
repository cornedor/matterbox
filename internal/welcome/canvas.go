package welcome

import "matterbox/internal/vapor"

// cell aliases the renderer's cell type so the wizard can composite directly
// onto the background grid: it draws its panel by overwriting cells in place,
// then the whole grid is serialized once. Working in cells (rather than ANSI
// strings) sidesteps any escape-sequence cutting when the opaque-ish panel sits
// over the animated, fully-coloured background.
type cell = vapor.Cell

// Vaporwave panel palette, harmonised with the scene's neon sun/terrain.
var (
	panelBg    = vapor.RGB{R: 20, G: 13, B: 44}   // dark navy panel fill (blended translucently over the scene)
	borderC    = vapor.RGB{R: 255, G: 61, B: 127} // neon pink frame
	accentCyan = vapor.RGB{R: 84, G: 236, B: 224} // cyan accents / cursor
	titleC     = vapor.RGB{R: 255, G: 210, B: 70} // warm sun-yellow heading
	labelC     = vapor.RGB{R: 206, G: 200, B: 226}
	dimC       = vapor.RGB{R: 138, G: 132, B: 168}
	valueC     = vapor.RGB{R: 240, G: 244, B: 255}
	fieldBg    = vapor.RGB{R: 34, G: 24, B: 66}
	fieldFocus = vapor.RGB{R: 58, G: 40, B: 104}
	cursorBg   = vapor.RGB{R: 84, G: 236, B: 224}
	cursorFg   = vapor.RGB{R: 12, G: 8, B: 28}
	goodC      = vapor.RGB{R: 120, G: 240, B: 168}
	badC       = vapor.RGB{R: 255, G: 116, B: 116}
)

// blend mixes a toward b by t in [0,1].
func blend(a, b vapor.RGB, t float64) vapor.RGB {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	return vapor.RGB{
		R: a.R + (b.R-a.R)*t,
		G: a.G + (b.G-a.G)*t,
		B: a.B + (b.B-a.B)*t,
	}
}

// inBounds reports whether (x,y) is a valid cell coordinate.
func inBounds(grid [][]cell, x, y int) bool {
	return y >= 0 && y < len(grid) && x >= 0 && x < len(grid[y])
}

// setCell overwrites the cell at (x,y) if it is in bounds.
func setCell(grid [][]cell, x, y int, c cell) {
	if inBounds(grid, x, y) {
		grid[y][x] = c
	}
}

// fillPanel fills the rect (x,y,w,h) with bg blended over each cell's existing
// background by alpha (1 = fully opaque, lower = the animation glows through),
// clearing the runes so the panel reads as a solid surface.
func fillPanel(grid [][]cell, x, y, w, h int, bg vapor.RGB, alpha float64) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			cx, cy := x+i, y+j
			if !inBounds(grid, cx, cy) {
				continue
			}
			under := grid[cy][cx].Bg
			b := blend(under, bg, alpha)
			grid[cy][cx] = cell{R: ' ', Fg: b, Bg: b, HasBg: true}
		}
	}
}

// drawText writes the runes of s starting at (x,y), each in fg over bg. Runes
// past the grid edge are clipped. All wizard text is single-width (ASCII, box
// drawing, and a few symbols), so a rune maps to one column.
func drawText(grid [][]cell, x, y int, s string, fg, bg vapor.RGB) {
	i := 0
	for _, r := range s {
		setCell(grid, x+i, y, cell{R: r, Fg: fg, Bg: bg, HasBg: true})
		i++
	}
}

// drawTextOverlay writes s in fg, keeping each cell's existing background (used
// for the intro skip hint, which floats over the scene rather than a panel).
func drawTextOverlay(grid [][]cell, x, y int, s string, fg vapor.RGB) {
	i := 0
	for _, r := range s {
		cx := x + i
		if inBounds(grid, cx, y) {
			under := grid[y][cx]
			under.R = r
			under.Fg = fg
			grid[y][cx] = under
		}
		i++
	}
}

// roundedBorder draws a rounded-corner frame around the rect (x,y,w,h) in the
// border colour over bg.
func roundedBorder(grid [][]cell, x, y, w, h int, border, bg vapor.RGB) {
	if w < 2 || h < 2 {
		return
	}
	right, bottom := x+w-1, y+h-1
	hline := func(yy int) {
		for i := x + 1; i < right; i++ {
			setCell(grid, i, yy, cell{R: '─', Fg: border, Bg: bg, HasBg: true})
		}
	}
	vline := func(xx int) {
		for j := y + 1; j < bottom; j++ {
			setCell(grid, xx, j, cell{R: '│', Fg: border, Bg: bg, HasBg: true})
		}
	}
	hline(y)
	hline(bottom)
	vline(x)
	vline(right)
	setCell(grid, x, y, cell{R: '╭', Fg: border, Bg: bg, HasBg: true})
	setCell(grid, right, y, cell{R: '╮', Fg: border, Bg: bg, HasBg: true})
	setCell(grid, x, bottom, cell{R: '╰', Fg: border, Bg: bg, HasBg: true})
	setCell(grid, right, bottom, cell{R: '╯', Fg: border, Bg: bg, HasBg: true})
}
