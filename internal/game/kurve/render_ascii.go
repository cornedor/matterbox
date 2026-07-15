package kurve

import (
	"math"
	"strings"
)

// asciiTrail is one distinct glyph per player, in index order, for the plain
// board other clients watch. Sized to MaxPlayers so a six-way game still reads.
var asciiTrail = [MaxPlayers]byte{'o', 'x', '*', '#', '~', '='}

// RenderASCII draws the sim as a plain-ASCII board, sized in terminal cells —
// the half of the game users on the official Mattermost clients can actually
// watch. Deliberately plain: no block elements or braille, nothing that renders
// as tofu on a phone.
//
// It downsamples the same owner grid the kitty renderer and the collision tests
// read, so it can never show a trail a head would pass through. Each player's
// trail is its own glyph from asciiTrail, live heads are `@`, dead ones `+`.
func RenderASCII(s *Sim, cols, rows int) []string {
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

	cw, ch := float64(FieldW)/float64(cols), float64(FieldH)/float64(rows)
	count := make([]int, len(s.Curves)+1) // index 0 is empty; reused per cell
	for cy := range rows {
		for cx := range cols {
			// A cell reads as a player's trail if that player owns the majority of
			// the field block it covers; ties and empties stay blank.
			for i := range count {
				count[i] = 0
			}
			for sy := range 3 {
				for sx := range 3 {
					x := int((float64(cx) + (float64(sx)+0.5)/3) * cw)
					y := int((float64(cy) + (float64(sy)+0.5)/3) * ch)
					count[s.Owner(x, y)]++
				}
			}
			best, bestN := 0, 1 // a cell needs ≥2 of 9 samples to claim
			for o := 1; o < len(count); o++ {
				if count[o] > bestN {
					best, bestN = o, count[o]
				}
			}
			if best > 0 {
				grid[cy][cx] = asciiTrail[best-1]
			}
		}
	}

	put := func(fx, fy float64, c byte) {
		cx, cy := int(fx/cw), int(fy/ch)
		if cx >= 0 && cx < cols && cy >= 0 && cy < rows {
			grid[cy][cx] = c
		}
	}
	for i := range s.Curves {
		c := &s.Curves[i]
		head := byte('@')
		if c.Dead {
			head = '+'
		}
		put(math.Round(c.X), math.Round(c.Y), head)
	}

	out := make([]string, rows)
	for y, row := range grid {
		out[y] = strings.TrimRight(string(row), " ")
	}
	return out
}

// ASCIIBoard is RenderASCII wrapped in the code fence that goes into a post.
func ASCIIBoard(s *Sim, cols, rows int) string {
	var b strings.Builder
	b.WriteString("```\n")
	for _, line := range RenderASCII(s, cols, rows) {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("```")
	return b.String()
}
