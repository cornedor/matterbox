package game

import (
	"image"
	"image/color"
	"math"
)

// The explosions.
//
// Both of the original's are, at heart, the same trick, and it is not the one a
// modern engine would use: the fireball is *drawn* onto the screen, and then
// *erased* by redrawing it in the background colour — from the outside in. The
// erase does not stop at the fireball. It eats the masonry underneath too, and
// that is where the crater comes from. There is no separate destruction step
// anywhere in GORILLA.BAS; the hole in the skyline is a side effect of cleaning
// up the animation.
//
// Reproducing that is what makes the impact read correctly, and it dictates the
// shape of this code:
//
//   - The crater is not carved when the banana lands. It is carved when the
//     fireball finishes collapsing (see Match.Step), because until then the
//     buildings the fireball will eventually eat are still standing, and you can
//     see them around it.
//   - The renderer paints sky over the ring the collapse has already swallowed.
//     So the hole opens from the outside in, chasing the shrinking fireball, and
//     the world underneath does not change until it is over.
//
// Get that backwards — carve on impact, then animate a fireball over the hole —
// and a gorilla hit looks especially wrong, because its crater is far bigger than
// its fireball ever gets: the skyline would vanish a beat before the blast that
// was supposed to have destroyed it.

// ExplosionKind is which of the two the original plays.
type ExplosionKind uint8

const (
	// BoomBanana is DoExplosion: the banana hit masonry.
	BoomBanana ExplosionKind = iota
	// BoomGorilla is ExplodeGorilla: a direct hit, and the end of the round.
	BoomGorilla
)

// The geometry, in field units — which are the original's pixels, so these are
// its numbers.
const (
	// DoExplosion: Radius = ScrHeight / 50, drawn with CIRCLE's default aspect. So
	// a banana's crater is not round in pixel space; it is a squat ellipse that
	// only looks round on the 4:3 monitor the game was written for.
	craterRX = 7
	craterRY = craterRX * defAspect // ≈ 5

	// ExplodeGorilla passes aspect -1.57 to every CIRCLE it draws, which makes the
	// blast markedly taller than it is wide. Its rings reach 32…
	gorillaAspect  = 1.57
	gorillaBlastRX = 32
	// …but the collapse erases out to 48, so the hole it leaves is half again
	// wider than the fireball that made it — and a good deal taller.
	gorillaCraterRX = 48
	gorillaCraterRY = gorillaCraterRX * gorillaAspect // ≈ 75

	// The rising column of phase one, in which the gorilla is engulfed from the
	// feet up before the blast proper.
	gorillaRiseR = 16
)

// The three loops of ExplodeGorilla, and DoExplosion's one, paced onto frames.
//
// The original's timing is a property of a 1990 PC: the loops that only draw run
// flat out and read as instantaneous, and the one loop that erases has a delay in
// it, so what you actually perceive is a flash followed by a collapse. That is
// what is reproduced — the ratios, not the iteration counts.
const (
	bananaBoomFrames = 14 // Radius 7 down to 0 in halves, the only loop DoExplosion delays

	gorillaRiseFrames     = 3
	gorillaRingFrames     = 6
	gorillaCollapseFrames = 15
	gorillaBoomFrames     = gorillaRiseFrames + gorillaRingFrames + gorillaCollapseFrames
)

// Explosion is a fireball in progress. It is part of the game state rather than a
// decoration on top of it: it holds the round open, it blocks both players from
// firing, and when it burns out it is what carves the crater.
type Explosion struct {
	// X, Y is the centre. For a banana that is where it landed; for a gorilla it
	// is the ape's chest, which is where ExplodeGorilla centres its blast
	// regardless of where the banana actually struck.
	X, Y  int
	Kind  ExplosionKind
	Frame int
}

// NewExplosion lights one.
func NewExplosion(kind ExplosionKind, x, y int) *Explosion {
	return &Explosion{X: x, Y: y, Kind: kind}
}

// Frames is how long this explosion burns.
func (e *Explosion) Frames() int {
	if e.Kind == BoomGorilla {
		return gorillaBoomFrames
	}
	return bananaBoomFrames
}

// Done reports that the fireball has fully collapsed. The crater is carved now.
func (e *Explosion) Done() bool { return e.Frame >= e.Frames() }

// Crater is the hole this explosion will have eaten by the time it is done.
func (e *Explosion) Crater() (rx, ry float64) {
	if e.Kind == BoomGorilla {
		return gorillaCraterRX, gorillaCraterRY
	}
	return craterRX, craterRY
}

// alive is the outer radius the collapse has not yet swallowed: everything beyond
// it is already background, and everything within it is still whatever it was.
// During the phases that only draw, nothing has been erased and this is the full
// crater.
func (e *Explosion) alive() float64 {
	switch e.Kind {
	case BoomGorilla:
		burn := e.Frame - gorillaRiseFrames - gorillaRingFrames
		if burn < 0 {
			return gorillaCraterRX
		}
		return gorillaCraterRX * (1 - float64(burn+1)/gorillaCollapseFrames)
	default:
		// DoExplosion draws its disc whole and then erases it in half-unit steps,
		// so the fireball is at full size on the first frame and gone on the last.
		return craterRX - float64(e.Frame)*0.5
	}
}

// drawExplosion paints one frame of a fireball over the world.
func drawExplosion(img *image.RGBA, e *Explosion, sx, sy float64) {
	rx, ry := e.Crater()
	alive := e.alive()
	aspect := defAspect
	if e.Kind == BoomGorilla {
		aspect = gorillaAspect
	}

	// The collapse has already eaten everything from `alive` out to the crater's
	// rim, so that ring is sky — this is the hole opening up, and the reason the
	// world itself is not touched until the explosion is over.
	//
	// Both loops run over the crater's bounding box rather than the fireball's,
	// because the hole is the larger of the two.
	for dy := -int(math.Ceil(ry)); dy <= int(math.Ceil(ry)); dy++ {
		for dx := -int(math.Ceil(rx)); dx <= int(math.Ceil(rx)); dx++ {
			if ellipseDist(float64(dx), float64(dy), rx, ry) > 1 {
				continue
			}
			// How far out this pixel sits, measured in the blast's own ellipse — so
			// d is exactly the radius the CIRCLE that drew or erased it was given.
			d := math.Sqrt(ellipseDist(float64(dx), float64(dy), 1, aspect))
			if d > alive {
				fillField(img, e.X+dx, e.Y+dy, colSky, sx, sy)
				continue
			}
			if c, ok := e.body(dx, dy, d); ok {
				fillField(img, e.X+dx, e.Y+dy, c, sx, sy)
			}
		}
	}
}

// body is the colour of the fireball itself at an offset from its centre, and
// whether there is any fireball there at all.
func (e *Explosion) body(dx, dy int, d float64) (col color.RGBA, ok bool) {
	if e.Kind == BoomBanana {
		// A flat disc of ExplosionColor. That is the whole of it — no gradient, no
		// hot core, no ragged rim; those are modern habits and the original has
		// none of them.
		return colExplosion, true
	}

	// ExplodeGorilla, phase two: concentric ellipses of radius 1..32 in alternating
	// colours — `i MOD 2 + 1`, so ExplosionColor and OBJECTCOLOR — which land as
	// red and salmon stripes across the fireball.
	if ring := e.rings(); d <= ring {
		if int(math.Round(d))%2 == 1 {
			return colExplosion, true
		}
		return colGorilla, true
	}

	// Phase one: the column that climbs the ape before the blast, and the ellipse
	// at its foot. Phase two erases these as its rings grow over them.
	if r := e.rise(); r > 0 {
		// The lower ellipse, twelve units below the blast's centre.
		if ellipseDist(float64(dx), float64(dy-12), r, r*gorillaAspect) <= 1 {
			return colExplosion, true
		}
		// The line ExplodeGorilla sweeps upward across the gorilla's chest, one row
		// per iteration, which leaves a filled column behind it.
		if dx >= -12 && dx <= 2 && float64(dy) >= 4-r && dy <= 4 {
			return colExplosion, true
		}
	}
	return col, false
}

// rings is how far phase two's striped ellipses have grown.
func (e *Explosion) rings() float64 {
	grown := e.Frame - gorillaRiseFrames
	switch {
	case grown < 0:
		return 0
	case grown >= gorillaRingFrames:
		return gorillaBlastRX
	}
	return gorillaBlastRX * float64(grown+1) / gorillaRingFrames
}

// rise is the radius of phase one's ellipse: it grows while the blast climbs the
// gorilla, then shrinks away as phase two erases it from under the rings.
func (e *Explosion) rise() float64 {
	switch {
	case e.Frame < gorillaRiseFrames:
		return gorillaRiseR * float64(e.Frame+1) / gorillaRiseFrames
	case e.Frame < gorillaRiseFrames+gorillaRingFrames:
		burn := float64(e.Frame-gorillaRiseFrames+1) / gorillaRingFrames
		return gorillaRiseR * (1 - burn)
	}
	return 0
}
