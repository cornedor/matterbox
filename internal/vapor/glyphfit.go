package vapor

import "math/bits"

// The glyph presenter samples every terminal cell on a common 2×12 sub-cell grid
// (the LCM of the 2×2 / 2×3 / 2×4 family grids), so candidates from different
// families are scored against the same ground truth. For each cell it picks the
// glyph + foreground/background colour pair that best reconstructs those samples.
const (
	glyphCols = 2                     // sub-cells across a cell (all families)
	glyphRows = 12                    // common sub-cell rows (LCM of 2, 3, 4)
	glyphSub  = glyphCols * glyphRows // 24 samples per cell
)

// lumW weights the squared-error metric by luminance so colour differences the
// eye notices count for more (same coefficients as RGB.lum).
var lumW = RGB{0.299, 0.587, 0.114}

// glyphPick is the outcome of fitting one cell.
type glyphPick struct {
	fam     *glyphFamily
	pattern int
	r       rune
	fg, bg  RGB
}

// cellFit chooses the best glyph for one cell. sub holds the 24 common sub-cell
// colours (row-major, index row*2+col, 12 rows); totSum is their sum. families
// are tried in order, and an equal score keeps the earlier (more compatible) one.
func cellFit(sub *[glyphSub]RGB, totSum RGB, families []glyphFamily) glyphPick {
	best := glyphPick{r: ' ', fg: meanRGB(totSum, glyphSub), bg: meanRGB(totSum, glyphSub)}
	bestScore := -1.0

	for fi := range families {
		fam := &families[fi]
		perNative := glyphRows / fam.rows // common cells per native sub-cell
		nCount := fam.rows * glyphCols

		// Sum each native sub-cell from its block of common cells.
		var nsum [8]RGB
		for nr := 0; nr < fam.rows; nr++ {
			for nc := 0; nc < glyphCols; nc++ {
				var s RGB
				for rr := 0; rr < perNative; rr++ {
					s = addRGB(s, sub[(nr*perNative+rr)*glyphCols+nc])
				}
				nsum[nr*glyphCols+nc] = s
			}
		}

		// Walk all patterns in Gray-code order so exactly one sub-cell flips
		// between steps: fgSum/fgCells update in O(1) instead of rescanning bits.
		var fgSum RGB
		fgCells, prev := 0, 0
		for g := 0; g < (1 << nCount); g++ {
			p := g ^ (g >> 1)
			if d := p ^ prev; d != 0 {
				k := bits.TrailingZeros(uint(d))
				if p&d != 0 {
					fgSum = addRGB(fgSum, nsum[k])
					fgCells++
				} else {
					fgSum = subRGB(fgSum, nsum[k])
					fgCells--
				}
				prev = p
			}

			fgN := fgCells * perNative
			bgN := glyphSub - fgN
			bgSum := subRGB(totSum, fgSum)

			// score = Σ|c|² − SSE (the Σ|c|² term is constant per cell and
			// cancels across candidates), so larger is a closer fit.
			score := 0.0
			if fgN > 0 {
				score += wdot(fgSum) / float64(fgN)
			}
			if bgN > 0 {
				score += wdot(bgSum) / float64(bgN)
			}
			// Strictly-better only, with a relative margin so floating-point
			// noise can't flip an exact tie. Genuine ties keep the earlier
			// (more compatible) candidate.
			if score > bestScore*(1+1e-9) {
				bestScore = score
				best.fam = fam
				best.pattern = p
				best.r = fam.runes[p]
				switch {
				case fgN == 0:
					best.bg = meanRGB(bgSum, bgN)
					best.fg = best.bg
				case bgN == 0:
					best.fg = meanRGB(fgSum, fgN)
					best.bg = best.fg
				default:
					best.fg = meanRGB(fgSum, fgN)
					best.bg = meanRGB(bgSum, bgN)
				}
			}
		}
	}
	return best
}

// presentGlyphCells renders the framebuffer (sampled at W = cols*2, glyphRows
// per cell) into a cell grid using the adaptive glyph fit, choosing the rune +
// foreground/background pair that best reconstructs each cell. It is
// single-threaded by design: the per-cell fit is cheap enough (Gray-code
// candidate walk plus a uniform-cell early-out) that fanning rows across
// goroutines every frame cost more in scheduling and GC than it saved.
func presentGlyphCells(grid [][]Cell, buf []RGB, W, sceneRows int, families []glyphFamily) {
	cols := W / glyphCols
	var sub [glyphSub]RGB

	for cr := 0; cr < sceneRows; cr++ {
		rowBase := cr * glyphRows
		dst := grid[cr]
		for cc := 0; cc < cols; cc++ {
			var totSum RGB
			uniform := true
			first := buf[rowBase*W+cc*glyphCols]
			for r := 0; r < glyphRows; r++ {
				src := (rowBase+r)*W + cc*glyphCols
				a, b := buf[src], buf[src+1]
				sub[r*glyphCols] = a
				sub[r*glyphCols+1] = b
				totSum = addRGB(addRGB(totSum, a), b)
				if uniform && (!nearEq(a, first) || !nearEq(b, first)) {
					uniform = false
				}
			}

			if uniform {
				// Flat cell (most of the sky): skip the search, paint a solid bg.
				bg := meanRGB(totSum, glyphSub)
				dst[cc] = Cell{R: ' ', Fg: bg, Bg: bg, HasBg: true}
				continue
			}
			pick := cellFit(&sub, totSum, families)
			dst[cc] = Cell{R: pick.r, Fg: pick.fg, Bg: pick.bg, HasBg: true}
		}
	}
}

func addRGB(a, b RGB) RGB { return RGB{a.R + b.R, a.G + b.G, a.B + b.B} }
func subRGB(a, b RGB) RGB { return RGB{a.R - b.R, a.G - b.G, a.B - b.B} }

func meanRGB(s RGB, n int) RGB {
	f := 1.0 / float64(n)
	return RGB{s.R * f, s.G * f, s.B * f}
}

// wdot is the luminance-weighted squared magnitude of a colour vector.
func wdot(c RGB) float64 {
	return lumW.R*c.R*c.R + lumW.G*c.G*c.G + lumW.B*c.B*c.B
}

// nearEq reports whether two colours are within ~1 quantisation step per channel.
func nearEq(a, b RGB) bool {
	const eps = 2.0
	return abs64(a.R-b.R) < eps && abs64(a.G-b.G) < eps && abs64(a.B-b.B) < eps
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
