package game

import "strings"

// RenderASCII draws the world as a plain-ASCII board, sized in terminal cells.
//
// This is the board that goes in the post's code fence — the part users on the
// official Mattermost clients actually see, and the only part of the game that
// has to survive an unknown font. It is deliberately plain ASCII: no block
// elements, no braille, nothing that renders as tofu on a phone.
//
// It is a downsample of the same occupancy bitmap the kitty renderer and the
// collision tests read, so the board can never show a building that a banana
// would fly through.
func RenderASCII(w *World, s *Shot, cols, rows int) []string {
	if cols < 8 || rows < 3 {
		return nil
	}
	grid := make([][]byte, rows)
	for y := range grid {
		grid[y] = make([]byte, cols)
		for x := range grid[y] {
			grid[y][x] = ' '
		}
	}

	// Cell (cx,cy) covers a block of field pixels; it is masonry if enough of
	// that block is solid. A bare majority would erode thin walls into nothing,
	// so any appreciable fill counts as wall.
	cw, ch := float64(FieldW)/float64(cols), float64(FieldH)/float64(rows)
	for cy := range rows {
		for cx := range cols {
			var solid, total int
			for sy := range 3 {
				for sx := range 3 {
					x := int((float64(cx) + (float64(sx)+0.5)/3) * cw)
					y := int((float64(cy) + (float64(sy)+0.5)/3) * ch)
					total++
					if w.Solid(x, y) {
						solid++
					}
				}
			}
			if solid*4 >= total { // ≥25% filled reads as wall
				grid[cy][cx] = '#'
			}
		}
	}

	put := func(fx, fy int, c byte) {
		cx, cy := int(float64(fx)/cw), int(float64(fy)/ch)
		if cx >= 0 && cx < cols && cy >= 0 && cy < rows {
			grid[cy][cx] = c
		}
	}
	for _, g := range w.Gorillas {
		put(g.X+gorillaW/2, g.Y+gorillaH/2, 'Y') // arms up: the pose everyone remembers
	}
	if s != nil {
		x, y := s.Pos()
		put(int(x), int(y), 'o')
	}

	out := make([]string, rows)
	for y, row := range grid {
		out[y] = strings.TrimRight(string(row), " ")
	}
	return out
}

// ASCIIBoard is RenderASCII wrapped in the code fence that goes into a post.
func ASCIIBoard(w *World, s *Shot, cols, rows int) string {
	var b strings.Builder
	b.WriteString("```\n")
	for _, line := range RenderASCII(w, s, cols, rows) {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("```")
	return b.String()
}
