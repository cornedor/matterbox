package game

import "image/color"

// The palette, as GORILLA.BAS sets it — decoded rather than eyeballed.
//
// SCREEN 9 attributes index a 64-entry DAC whose 6-bit values are laid out
// rgbRGB: each channel's level is 2*high + low, so 0–3, which map onto
// 0/85/170/255. SetScreen's PALETTE statements are therefore the actual colours
// of the game, and several of them are not what a modern eye guesses: the
// explosion (PALETTE 2, 44 = 101100b) is a hot red-magenta, not orange, and the
// gorillas (PALETTE 1, 46) are salmon.
var (
	colSky       = ega(1)  // PALETTE 0, 1  — BACKATTR, and what an erase paints
	colGorilla   = ega(46) // PALETTE 1, 46 — OBJECTCOLOR
	colExplosion = ega(44) // PALETTE 2, 44 — ExplosionColor
	colSun       = ega(54) // PALETTE 3, 54 — SUNATTR

	// MakeCityScape picks BuildingColor = FnRan(3) + 4, so a building is always
	// attribute 5, 6 or 7 — grey, dark red, teal.
	colBuilding = [3]color.RGBA{
		ega(7), // PALETTE 5, 7
		ega(4), // PALETTE 6, 4
		ega(3), // PALETTE 7, 3
	}

	// Windows and the banana are attribute 14, which SetScreen never repalettes —
	// so they keep the default EGA yellow, and a lit window is *exactly* the colour
	// of the banana. So does 8, the grey of a window whose occupant has gone home.
	colWindowLit  = egaDefault(14) // WINDOWCOLOR
	colWindowDark = egaDefault(8)
	colBanana     = egaDefault(14)
)

// ega decodes one 6-bit EGA DAC value. The bits are rgbRGB: bit 5 is low red,
// bit 2 is high red, and so on down.
func ega(v int) color.RGBA {
	level := func(high, low uint) uint8 {
		return 85 * uint8(2*(v>>high&1)+(v>>low&1))
	}
	return color.RGBA{level(2, 5), level(1, 4), level(0, 3), 0xff}
}

// egaDefault decodes one of the sixteen default text attributes, for the two the
// game leaves alone. The table is the standard EGA default — including attribute
// 6's jump to 20, the fix that keeps it brown instead of dark yellow.
func egaDefault(attr int) color.RGBA {
	dac := [16]int{0, 1, 2, 3, 4, 5, 20, 7, 56, 57, 58, 59, 60, 61, 62, 63}
	return ega(dac[attr])
}
