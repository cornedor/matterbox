package vapor

// Glyph families for the adaptive ("glyph") presenter. Each family tiles a cell
// into a native grid of `rows` rows × 2 columns of sub-cells; a candidate glyph
// is a bit pattern marking which sub-cells are foreground. The bit for sub-cell
// (row, col) is `2*row + col`, with the top-left sub-cell as bit 0 — the same
// numbering Unicode uses for its block elements (BLOCK SEXTANT-1 = bit 0, BLOCK
// OCTANT-1 = bit 0, …), which is what lets us map a pattern straight to a rune.

// glyphFamily is a set of candidate glyphs at one native resolution.
type glyphFamily struct {
	name  string
	rows  int    // native sub-cell rows (columns are always 2)
	runes []rune // runes[pattern] is the glyph whose filled sub-cells == pattern
}

var (
	quadrantFamily = glyphFamily{name: "quadrant", rows: 2, runes: quadrantRunes()}
	sextantFamily  = glyphFamily{name: "sextant", rows: 3, runes: sextantRunes()}
	octantFamily   = glyphFamily{name: "octant", rows: 4, runes: octantRunes()}
)

// coverageFamilies returns the candidate families for a -coverage level, ordered
// most-compatible first. cellFit breaks ties toward earlier families, so common
// shapes resolve to widely-supported quadrant/half glyphs and only genuinely fine
// detail reaches the newer sextant/octant code points.
func coverageFamilies(coverage string) []glyphFamily {
	switch coverage {
	case "quad":
		return []glyphFamily{quadrantFamily}
	case "sextant":
		return []glyphFamily{quadrantFamily, sextantFamily}
	default: // "octant"
		return []glyphFamily{quadrantFamily, sextantFamily, octantFamily}
	}
}

// quadrantRunes maps each of the 16 patterns of a 2×2 grid to its rune. Bits:
// 0=upper-left, 1=upper-right, 2=lower-left, 3=lower-right.
func quadrantRunes() []rune {
	return []rune{
		0x0020, // 0000  (space)
		0x2598, // 0001  UL          QUADRANT UPPER LEFT
		0x259D, // 0010  UR          QUADRANT UPPER RIGHT
		0x2580, // 0011  UL UR       UPPER HALF BLOCK
		0x2596, // 0100  LL          QUADRANT LOWER LEFT
		0x258C, // 0101  UL LL       LEFT HALF BLOCK
		0x259E, // 0110  UR LL       QUADRANT UPPER RIGHT AND LOWER LEFT
		0x259B, // 0111  UL UR LL    QUADRANT UL+UR+LL
		0x2597, // 1000  LR          QUADRANT LOWER RIGHT
		0x259A, // 1001  UL LR       QUADRANT UPPER LEFT AND LOWER RIGHT
		0x2590, // 1010  UR LR       RIGHT HALF BLOCK
		0x259C, // 1011  UL UR LR    QUADRANT UL+UR+LR
		0x2584, // 1100  LL LR       LOWER HALF BLOCK
		0x2599, // 1101  UL LL LR    QUADRANT UL+LL+LR
		0x259F, // 1110  UR LL LR    QUADRANT UR+LL+LR
		0x2588, // 1111  (full)      FULL BLOCK
	}
}

// sextantRunes maps each of the 64 patterns of a 2×3 grid to its rune. The 60
// dedicated BLOCK SEXTANT glyphs occupy U+1FB00..U+1FB3B in increasing pattern
// order, skipping the four patterns that already have block glyphs.
func sextantRunes() []rune {
	reused := map[int]rune{
		0:  0x0020, // (space)
		21: 0x258C, // left column  -> LEFT HALF BLOCK
		42: 0x2590, // right column -> RIGHT HALF BLOCK
		63: 0x2588, // (full)       -> FULL BLOCK
	}
	return fillByRule(64, 0x1FB00, reused)
}

// octantRunes maps each of the 256 patterns of a 2×4 grid to its rune. The 230
// dedicated BLOCK OCTANT glyphs occupy U+1CD00..U+1CDE5 in increasing pattern
// order, skipping the 26 patterns that already have glyphs elsewhere. Verified
// to reproduce wezterm's OCTANT_PATTERNS table bit-for-bit.
func octantRunes() []rune {
	reused := map[int]rune{
		0:   0x0020,  // (space)
		1:   0x1CEA8, // LEFT HALF UPPER ONE QUARTER BLOCK
		2:   0x1CEAB, // RIGHT HALF UPPER ONE QUARTER BLOCK
		3:   0x1FB82, // UPPER ONE QUARTER BLOCK (top row)
		5:   0x2598,  // QUADRANT UPPER LEFT
		10:  0x259D,  // QUADRANT UPPER RIGHT
		15:  0x2580,  // UPPER HALF BLOCK
		20:  0x1FBE6, // MIDDLE LEFT ONE QUARTER BLOCK
		40:  0x1FBE7, // MIDDLE RIGHT ONE QUARTER BLOCK
		63:  0x1FB85, // UPPER THREE QUARTERS BLOCK (top three rows)
		64:  0x1CEA3, // LEFT HALF LOWER ONE QUARTER BLOCK
		80:  0x2596,  // QUADRANT LOWER LEFT
		85:  0x258C,  // LEFT HALF BLOCK
		90:  0x259E,  // QUADRANT UPPER RIGHT AND LOWER LEFT
		95:  0x259B,  // QUADRANT UL+UR+LL
		128: 0x1CEA0, // RIGHT HALF LOWER ONE QUARTER BLOCK
		160: 0x2597,  // QUADRANT LOWER RIGHT
		165: 0x259A,  // QUADRANT UPPER LEFT AND LOWER RIGHT
		170: 0x2590,  // RIGHT HALF BLOCK
		175: 0x259C,  // QUADRANT UL+UR+LR
		192: 0x2582,  // LOWER ONE QUARTER BLOCK (bottom row)
		240: 0x2584,  // LOWER HALF BLOCK
		245: 0x2599,  // QUADRANT UL+LL+LR
		250: 0x259F,  // QUADRANT UR+LL+LR
		252: 0x2586,  // LOWER THREE QUARTERS BLOCK (bottom three rows)
		255: 0x2588,  // FULL BLOCK
	}
	return fillByRule(256, 0x1CD00, reused)
}

// fillByRule builds an n-entry pattern→rune table: patterns present in `reused`
// take their listed rune; the rest are assigned `base`, base+1, … in increasing
// pattern order.
func fillByRule(n int, base rune, reused map[int]rune) []rune {
	r := make([]rune, n)
	next := base
	for p := 0; p < n; p++ {
		if c, ok := reused[p]; ok {
			r[p] = c
			continue
		}
		r[p] = next
		next++
	}
	return r
}
