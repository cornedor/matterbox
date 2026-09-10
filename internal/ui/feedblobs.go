package ui

import (
	"math"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The Feed tab's empty state is a slow field of shaded blobs — a lava-lamp
// drift with nothing to read into it. Nothing here is stored art: the field is
// a handful of metaballs, sampled once per character cell and quantised
// through blobRamp, so it fills any pane size exactly and owns no glyph
// anybody else drew.

// blobRamp is the shading table, ordered by ink coverage: index 0 is empty
// space, the last index the densest glyph. Picking an index out of it *is* the
// renderer — every cell is one shaded sample mapped onto this ramp.
//
// It stops at '#'. The classic ramp carries on through '%' and '@', but those
// two are so much inkier than everything below them that any cell reaching
// them reads as a hard edge — which is the opposite of what this is for.
const blobRamp = " .:-=+*#"

// The field runs at two frame rates, and which one is live is decided by the
// field itself rather than by a setting.
//
// Drifting, it moves less than a cell per frame at any rate worth having: the
// periods are minutes long, so at 60 fps a third of the frames come out
// byte-identical to the one before. Spending a repaint on them buys nothing —
// a live profile put a 60 fps idle field at 14% of a core, two thirds of it in
// bubbletea re-parsing a screen that had barely changed.
//
// A poke is the opposite: the swell spring peaks about 0.3s in, and that arc is
// the one thing here anybody looks at directly. It gets every frame the
// terminal can take.
//
// So: 60 fps while a spring is still moving, a twelfth of that when the field
// is only drifting — visually the same screen, at a fifth of the cost.
const (
	feedBlobIdleFPS = 12
	feedBlobLiveFPS = 60
)

const (
	// feedBlobIdleInterval is the drifting frame gap, and the one the tests
	// animate at.
	feedBlobIdleInterval = time.Second / feedBlobIdleFPS
	// feedBlobLiveInterval is the gap while a poke settles.
	feedBlobLiveInterval = time.Second / feedBlobLiveFPS
)

// feedBlobInterval is the gap until the next frame: fast while any spring is
// still carrying a poke, calm once they have all come to rest.
func (m *Model) feedBlobInterval() time.Duration {
	if m.feed.blobNudge.idle() {
		return feedBlobIdleInterval
	}
	return feedBlobLiveInterval
}

// cellAspect converts a column offset into row units: a terminal cell is about
// twice as tall as it is wide, so without this the blobs would be ellipses.
const cellAspect = 0.5

// blobPulse is how much a blob's radius breathes, as a fraction of it. Small
// on purpose: the mass should look like it's settling, not throbbing.
const blobPulse = 0.05

// A click in the field answers back — the reason this screen is worth leaving
// open. There are two gestures, and where the press lands picks between them:
// take hold of a blob and you can drag it around the tank and throw it (see
// grabFeedBlobs), press the goo *between* the blobs and you shove them.
//
// The shove is a nudge on a spring: each blob carries an offset from its drift
// path, a press gives that offset a velocity, and a critically damped spring
// walks it back to zero over several seconds. Nothing about the shove snaps or
// bounces — it is the calm half of the toy, and it is what the field does when
// you only brush past it.
const (
	// blobPushOmega is the spring's natural frequency, in rad/s — a period of
	// about fourteen seconds. Slow on purpose: the return takes the better part
	// of ten seconds, which is the difference between a lava lamp settling and a
	// button springing back.
	blobPushOmega = 0.45
	// blobPushZeta is the damping ratio: exactly critical. Underdamped adds a
	// wobble that reads as elastic, overdamped reads as sticky.
	blobPushZeta = 1.0
	// blobPushGain is this spring's peak response to a unit impulse, in units of
	// v₀/ω. For ζ = 1 that is exactly 1/e, at t = 1/ω.
	blobPushGain = 1 / math.E
	// blobPushSpan is the peak displacement of a best-placed click, as a
	// fraction of the pane's short side — about as far as the drift itself
	// travels, enough to read as the mass moving, not enough to throw it.
	blobPushSpan = 0.075
	// blobPushCap bounds the accumulated offset, as a fraction of the pane, so
	// a burst of clicking can't shove a blob off the canvas.
	blobPushCap = 0.22
)

// A poke also swells the blob it lands in, on a second spring. This one is
// deliberately underdamped: the size springs past its resting radius and
// settles back through one soft overshoot, which is what makes the mass read
// as something soft you pressed rather than a sprite that got scaled. The
// motion spring stays critically damped — a blob that both wobbled and bounced
// around the pane would be a fidget toy, not a lava lamp.
const (
	// blobSwellOmega is the swell spring's natural frequency, in rad/s. Much
	// faster than blobPushOmega, and that gap is the whole feel of a poke: the
	// blob squashes and recovers in well under a second while it is still
	// sliding slowly back into place.
	//
	// The rate also decides how the swell *reads*. A spring's impulse response
	// rises at a near-constant slope for the first third of its way to the peak,
	// so a slow one (this was 0.63, peaking 1.9 seconds in) animates as a
	// straight linear ramp with a flat top — visibly not physical. At 3.2 the
	// peak lands ~0.3s in and the whole arc sits inside the part of the curve
	// that eases.
	blobSwellOmega = 3.2
	// blobSwellZeta is just under half-critical damping — one soft overshoot
	// past the resting radius and out. Raise it and the elastic return
	// disappears; lower it and the blob rings like jelly.
	blobSwellZeta = 0.45
	// blobSwellSnap is how much of a poke's swell is applied as an instant
	// displacement rather than as a velocity kick, as a fraction of the drive.
	// Mouse-down has to *land*: a pure kick starts from zero with the spring's
	// near-linear opening slope, so the blob takes its time noticing it was
	// pressed. With this much snap the click frame lands about a sixth of the way
	// to the peak — enough that the press is felt on the frame it happened — and
	// the rest eases in over the next third of a second. Twice this read as a
	// pop: the blob was suddenly bigger rather than growing.
	//
	// Not all of it: a full step would be a pop with no growth to watch, and the
	// growth is the part that reads as goop.
	blobSwellSnap = 0.1
	// blobSizeSpan is the peak swell of a click on a blob's middle, as a
	// fraction of the blob's radius.
	blobSizeSpan = 0.4
	// blobSizeCap bounds the accumulated swell so a burst of clicking can't
	// inflate a blob over the whole pane.
	blobSizeCap = 2.6
)

// blobSwellGain is the peak of the swell spring's response to a unit drive
// (x₀ = blobSwellSnap, v₀ = (1−blobSwellSnap)·ω), in drive units. Derived from
// the closed form rather than tuned by hand, so blobSizeSpan keeps meaning the
// swell you actually see when the snap or the damping is changed.
var blobSwellGain = swellGain()

func swellGain() float64 {
	x0, v0 := blobSwellSnap, (1-blobSwellSnap)*blobSwellOmega
	lambda := blobSwellZeta * blobSwellOmega
	wd := blobSwellOmega * math.Sqrt(1-blobSwellZeta*blobSwellZeta)
	// x(t) = C·e^(−λt)·sin(ω_d·t + φ); it peaks where the sine's argument
	// reaches atan(ω_d/λ).
	b := (v0 + lambda*x0) / wd
	c, phi := math.Hypot(x0, b), math.Atan2(x0, b)
	tp := (math.Atan2(wd, lambda) - phi) / wd
	if tp <= 0 {
		return x0 // all snap: the click frame is the peak
	}
	return c * math.Exp(-lambda*tp) * wd / blobSwellOmega
}

// The field is a tank: the canvas's own edges are its walls, and a blob thrown
// at one hits it. Nothing is drawn there — the pane border is already a cell
// out, and a rim inside it just read as a second border. Only a blob you are
// playing with collides, too: a drifting one is on rails that never take it
// out of the pane, and clamping those against the edge would make the calm
// state jitter.
//
// The contact is a spring, not a reflection. v = −v is one frame long and
// reads as a bat hitting a ball: nothing in the shape has time to answer it.
// Here the blob is allowed *into* the wall and a damped spring in penetration
// space pushes it back out over about a tenth of a second, which is long
// enough to see the body slow, flatten, and leave. The rebound speed is not a
// number this file picks: for a damped spring it falls out of the damping
// ratio as e^(−πζ/√(1−ζ²)), so blobContactZeta *is* the restitution.
const (
	// blobContactDepth is the deepest a wall lets a blob in, as a fraction of
	// its own radius, at the fastest throw the field allows. The contact
	// spring's stiffness is derived from it (blobContactFreq) rather than fixed,
	// for two reasons: what the eye reads is how far the blob gives relative to
	// its size, not an absolute number of rows, and the canvas clips whatever
	// goes past the edge — a small blob on a stiff-enough-for-a-big-one spring
	// would sink most of itself into the border.
	blobContactDepth = 0.45
	// blobContactFloor / blobContactCeil bound that derived stiffness, in
	// rad/s: below the floor a contact outlasts the throw that caused it, above
	// the ceiling it is over inside a frame or two and we are back to a
	// reflection with extra steps.
	blobContactFloor = 10.0
	blobContactCeil  = 60.0
	// blobContactZeta is the contact damping, and so the restitution: 0.15 gives
	// e^(−π·0.15/√0.98) ≈ 0.62 in the abstract, and about half that on screen —
	// the blob is flattening while it is touching, and the give that goes into
	// the shape is speed that doesn't come back out.
	blobContactZeta = 0.15
	// blobContactSquash is how much of the penetration turns into flattening,
	// in strain per radius of penetration. At 1 the flattening exactly matches
	// how far the centre has gone in, so the blob's rim stays on the wall while
	// its centre carries on — which is what a soft body against a hard surface
	// does, and why the contact looks like a facet rather than a blob sinking
	// through glass.
	blobContactSquash = 1.0
	// blobContactFriction is how fast the *tangential* velocity bleeds off
	// while a blob is touching a wall, in 1/s. Without it a glancing hit
	// carries on at the same sideways speed, which is the other half of why
	// v = −v reads as a machine.
	blobContactFriction = 3.5
	// blobPenetrationCap bounds how far into a wall the centre may go, as a
	// fraction of the blob's radius. The canvas clips whatever goes past its
	// edge, so this is really a guard against a fast throw resolving a contact
	// from the far side of the wall.
	blobPenetrationCap = 0.5
	// blobHoldDepth is the same limit for a blob being *dragged* into a wall,
	// where there is no spring and no impact — the pointer can push as far as
	// it likes, and this is how far that gets. Tighter than an impact's, and
	// tight for a reason: the squash only hides so much of the penetration (a
	// blob flattened by f keeps a rim at r·√((1−f)/(1+f))), and what it doesn't
	// hide is a sliver of the blob clipped off at the canvas edge. At this
	// depth the two nearly cancel, and pressing a blob into the edge looks like
	// pressing a blob into the edge.
	blobHoldDepth = 0.22
	// blobGlideDrag is the free-flight drag coefficient, in 1/s: v decays as
	// e^(−kt), so a throw has spent about half its speed after a second. Goo
	// moving through goo — it is what stops a thrown blob orbiting the tank
	// forever, and it is set so a hard throw gets two or three wall hits out of
	// itself before the drift spring takes over.
	blobGlideDrag = 0.7
	// blobRestSpeed is the speed, in pane short-sides per second, below which a
	// flying blob is handed back to the drift spring. Position and velocity
	// carry over, so the handover is invisible.
	blobRestSpeed = 0.05
	// blobThrowMax caps the release speed, in short-sides per second, so a fast
	// flick across the pane can't tunnel a blob through a wall between frames.
	blobThrowMax = 2.0
	// blobThrowStale is how old the last pointer sample may be and still count
	// as a throw. Let go after holding the blob still and it should drop where
	// it is, not fly off on a velocity measured half a second ago.
	blobThrowStale = 150 * time.Millisecond
)

// The blob's *shape* answers to all this on two more sets of springs, and
// neither of them is a scale on the x and y axes — a blob whose only trick is
// scaleX/scaleY announces that it is a sprite with a transform on it.
//
// The first is strain: a symmetric, area-preserving deformation with a
// direction of its own, so a blob squashes along whatever line it was pushed
// on. It is held as the two independent numbers such a deformation has (ea, eb
// — the deviatoric part of the strain tensor), driven toward a target rather
// than kicked, so it *lags into* a contact and rings back out of it.
const (
	// blobStrainRatio is the shape spring's frequency as a multiple of the
	// blob's contact stiffness. A ratio rather than a number of rad/s, and
	// faster than the contact on purpose: the flattening has to arrive while
	// the blob is still against the wall, not on the way back out — a squash
	// that peaks after the blob has left reads as an unrelated wobble. Tying it
	// to the contact is also what makes a small blob deform as much as a big
	// one: its contacts are shorter (see blobContactFreq), and a fixed shape
	// spring could not keep up with them.
	blobStrainRatio = 1.6
	// blobStrainZeta is the shape damping — loose enough to ring its way back
	// to round after a contact. Well under the 0.6 floor the motion spec puts
	// on productivity UI, deliberately: that floor is there so controls don't
	// read as toys, and this is a toy.
	blobStrainZeta = 0.35
	// blobStrainCap bounds the deformation. The area factor is 1 − |e|², so
	// this also keeps the shape from turning inside out.
	blobStrainCap = 0.5
	// blobFlightStretch is how much a blob stretches along its own velocity in
	// flight, at blobStretchVRef and above (tanh, so there is no speed that
	// tears it apart). Small: it is what makes a thrown blob read as a soft
	// thing moving rather than a disc being translated.
	blobFlightStretch = 0.10
	blobStretchVRef   = 0.6
)

// The second is slosh: the blob is drawn as two half-weight lobes, and this is
// the offset between them, as a fraction of the radius. At rest they sit on
// top of each other and the field is exactly the single metaball it always was
// — no seam when a blob starts or stops being played with.
//
// What moves them apart is the thing an ellipse can't express. When the body's
// velocity changes, the material inside doesn't change with it: it keeps going
// and piles up on the leading side. So the slosh is driven by the body's
// acceleration — hit a wall and the goo heaps into the contact, come off it
// and the heap swings back through. That is what makes the silhouette go
// lopsided instead of staying a conic section.
const (
	blobSloshOmega = 13.0
	blobSloshZeta  = 0.20
	// blobSloshLag is the fraction of a velocity change the material fails to
	// take with it — how much of the body's Δv goes into the lobes' relative
	// motion instead.
	blobSloshLag = 0.7
	// blobSloshCap bounds the split, so the two lobes stay one blob instead of
	// pulling apart into two.
	blobSloshCap = 0.5
)

// blobGrabReach is how close to a blob's centre, in blob radii, a press has to
// land for a drag to be able to start there. Just inside the visible rim: the
// push kernel peaks outside it, so the two gestures don't fight over the same
// cells.
const blobGrabReach = 0.95

// blobDragThreshold is how far the pointer has to travel, in row units, before
// a press on a blob turns into a drag.
//
// Without it every press is a drag, and the shove — a click that leans on the
// goo and lets its spring carry it back, which is what this field did before
// anything could be picked up — becomes unreachable: you cannot click without
// also moving a cell or two. So a press stays a poke until the pointer has
// gone somewhere, and only then does the blob come with it. Two or three cells
// of travel: enough that a click is never mistaken for a drag, small enough
// that a drag never feels stuck to the glass.
const blobDragThreshold = 1.5

// blobPushReach is how far a click reaches, in blob radii. Deliberately way
// past the visible rim: the margins are wide and soft because this is a toy,
// not a hitbox — a click in the empty space near a blob should still lean on
// it, and there should be no edge you can feel the field switch off at.
const blobPushReach = 2.4

// blobPushWeight is how hard a click at u = distance/radius leans on a blob.
// Zero dead on the centre, strongest a little outside the rim, back to zero
// once you're well clear of it — pressing a goopy blob square in the middle
// pushes it nowhere, and shoving it from the side is the only way to move it.
// One arch of a sine is the vaguest shape that does all three.
func blobPushWeight(u float64) float64 {
	if u <= 0 || u >= blobPushReach {
		return 0
	}
	return math.Sin(math.Pi * u / blobPushReach)
}

// blobSizeReach is how far a click reaches to squash a blob, in blob radii.
// Shorter than blobPushReach — the swell says "this is the one you pressed",
// and that has to be the blob under the pointer, not its neighbour.
const blobSizeReach = 1.8

// blobSwellWeight is how much a click at u = distance/radius swells a blob.
// The mirror of blobPushWeight: strongest where the push is weakest, dead on
// the centre, fading to nothing at the reach. Pressing goop square in the
// middle doesn't move it anywhere — it just spreads.
//
// A quarter-cosine, so it is flat on top: the swell doesn't care exactly where
// near the middle you hit, which keeps the whole thing vague to the touch.
func blobSwellWeight(u float64) float64 {
	if u >= blobSizeReach {
		return 0
	}
	return math.Cos(math.Pi * u / (2 * blobSizeReach))
}

// blobMode says which of the three regimes a blob is in, and that is the only
// thing that changes how its offset is integrated: the offset and velocity are
// the same state in all three, so a handover carries both across without a
// seam (the motion spec's one hard rule — position and velocity are
// continuous, everything else is negotiable).
type blobMode uint8

const (
	// blobDrift is the resting regime: the offset is a deviation from the drift
	// path, on a critically damped spring back to it.
	blobDrift blobMode = iota
	// blobHeld is a blob under the pointer: the drag owns its offset outright,
	// tracking 1:1, and no spring pulls at it.
	blobHeld
	// blobFree is a thrown blob: ballistic with drag, bouncing off the walls,
	// until it has slowed enough to be handed back to blobDrift.
	blobFree
)

// blobNudge is everything one blob is doing that its drift path doesn't say:
// where it is relative to that path, and what shape it is in.
//
// ox/oy are the displacement and vx/vy the velocity carrying it, both as
// fractions of the pane (of width for x, height for y) so a resize doesn't
// teleport a mid-flight throw. os is the swell, as a fraction of the blob's
// radius, with vs carrying it. mode says how ox/oy are being integrated.
type blobNudge struct {
	ox, oy, vx, vy, os, vs float64
	// ea, eb are the area-preserving deformation (the deviatoric strain
	// tensor's two degrees of freedom: magnitude and direction, held as
	// components so they interpolate); vea, veb carry them.
	ea, eb, vea, veb float64
	// sx, sy are the slosh — the offset between the blob's two lobes, as a
	// fraction of its radius — with vsx, vsy carrying it.
	sx, sy, vsx, vsy float64
	mode             blobMode
}

// blobStrain is a deformation: what the shape spring is being driven toward
// this frame. The zero value is round.
type blobStrain struct{ a, b float64 }

// squashStrain is the deformation that squashes a blob by s along the unit
// direction (ux,uy), stretching it across by as much as area conservation
// needs. Negative s stretches along the direction instead.
//
// Any direction, not just the axes: this is the whole reason the deformation
// is a tensor and not a pair of scale factors. A hit on the corner of the tank
// squashes the blob on the diagonal it arrived on.
func squashStrain(s, ux, uy float64) blobStrain {
	return blobStrain{a: -s * (ux*ux - uy*uy), b: -2 * s * ux * uy}
}

// add composes two deformations. Contacts on two walls at once (a corner) sum,
// and cancel to the extent that they oppose — which is right: squeezed from
// both sides at once, a blob has nowhere to bulge.
func (t blobStrain) add(o blobStrain) blobStrain {
	return blobStrain{a: t.a + o.a, b: t.b + o.b}
}

// blobNudges is the push state for the whole cast. A fixed array, not a slice:
// it rides inside the Model, which is copied per event, and 128 bytes of value
// beats a heap allocation aliased across those copies.
type blobNudges [len(feedBlobs)]blobNudge

// advanceShape steps a blob's swell, deformation and slosh forward dt
// seconds. omega is its shape spring's frequency (see blobStrainFreq), target
// the deformation the frame's contacts (or its flight) are driving it toward,
// and dvx/dvy the body's velocity change over the frame, in radii per second —
// the material's failure to take that change with it is the slosh.
func (p *blobNudge) advanceShape(dt, omega float64, target blobStrain, dvx, dvy float64) {
	os, vs := springStep(p.os, p.vs, blobSwellOmega, blobSwellZeta, dt)
	p.os, p.vs = clampAbs(os, vs, blobSizeCap)

	// A spring *about the target*, not a kick: driven this way the shape lags
	// into a contact over the frames the contact lasts and rings out of it
	// afterwards, which is the difference between a bounce and a jolt.
	ea, vea := springStep(p.ea-target.a, p.vea, omega, blobStrainZeta, dt)
	eb, veb := springStep(p.eb-target.b, p.veb, omega, blobStrainZeta, dt)
	p.ea, p.vea = ea+target.a, vea
	p.eb, p.veb = eb+target.b, veb
	// Clamped as a magnitude rather than per component, so the limit is the
	// same in every direction (a per-axis clamp would make diagonal
	// deformations half again as large as axis-aligned ones).
	if mag := math.Hypot(p.ea, p.eb); mag > blobStrainCap {
		k := blobStrainCap / mag
		p.ea, p.eb = p.ea*k, p.eb*k
		p.vea, p.veb = p.vea*k, p.veb*k
	}

	p.vsx -= blobSloshLag * dvx
	p.vsy -= blobSloshLag * dvy
	sx, vsx := springStep(p.sx, p.vsx, blobSloshOmega, blobSloshZeta, dt)
	sy, vsy := springStep(p.sy, p.vsy, blobSloshOmega, blobSloshZeta, dt)
	p.sx, p.vsx = sx, vsx
	p.sy, p.vsy = sy, vsy
	if mag := math.Hypot(p.sx, p.sy); mag > blobSloshCap {
		k := blobSloshCap / mag
		p.sx, p.sy = p.sx*k, p.sy*k
		p.vsx, p.vsy = p.vsx*k, p.vsy*k
	}
	// And the rate is capped at what an oscillation of that amplitude actually
	// has, which is the bound on what any Δv can put in. A pointer that jumps
	// across the pane between two motion reports (they are not sampled at any
	// guaranteed rate) is a Δv nothing physical could produce, and without this
	// it heaves the lobes apart in one frame.
	if lim := blobSloshCap * blobSloshOmega; math.Hypot(p.vsx, p.vsy) > lim {
		k := lim / math.Hypot(p.vsx, p.vsy)
		p.vsx, p.vsy = p.vsx*k, p.vsy*k
	}
}

// advanceDrift walks a resting blob's offset back toward its drift path.
//
// Offsets are clamped to their cap, and a clamped axis drops its velocity: a
// blob held at the limit has nothing left to spend going further. The clamp
// only bites on a blob that was already inside the cap, so a thrown blob
// coming home from the far side of the tank eases in on its spring instead of
// being teleported to the cap the frame it stops flying.
func (p *blobNudge) advanceDrift(dt float64) {
	capped := math.Abs(p.ox) <= blobPushCap
	ox, vx := springStep(p.ox, p.vx, blobPushOmega, blobPushZeta, dt)
	if capped {
		ox, vx = clampAbs(ox, vx, blobPushCap)
	}
	p.ox, p.vx = ox, vx
	capped = math.Abs(p.oy) <= blobPushCap
	oy, vy := springStep(p.oy, p.vy, blobPushOmega, blobPushZeta, dt)
	if capped {
		oy, vy = clampAbs(oy, vy, blobPushCap)
	}
	p.oy, p.vy = oy, vy
}

// glideStep advances a free-flying blob dt seconds under linear drag
// (v̇ = −kv), in closed form for the same reason springStep is: the frame rate
// changes underneath this motion, and an Euler step would make a throw carry
// further at 12 fps than at 60.
func glideStep(x, v, dt float64) (float64, float64) {
	decay := math.Exp(-blobGlideDrag * dt)
	return x + v*(1-decay)/blobGlideDrag, v * decay
}

// springStep advances a damped spring's (offset, velocity) by dt seconds using
// the closed-form solution, not an Euler step. The frame rate changes while a
// poke is settling (see feedBlobInterval), and a stepper whose amplitude and
// settling time depend on dt would put a kink in the motion at the moment it
// dropped back to the calm rate. This way both rates run the same motion,
// sampled finely or coarsely.
func springStep(x, v, omega, zeta, dt float64) (float64, float64) {
	decay := math.Exp(-zeta * omega * dt)
	if zeta >= 1 { // critically damped: x(t) = (x₀ + (v₀ + ωx₀)t)·e^(−ωt)
		a := v + omega*x
		return (x + a*dt) * decay, (v - omega*a*dt) * decay
	}
	wd := omega * math.Sqrt(1-zeta*zeta) // damped frequency
	c, sn := math.Cos(wd*dt), math.Sin(wd*dt)
	lambda := zeta * omega
	return decay * (x*c + (v+lambda*x)/wd*sn),
		decay * (v*c - (omega*omega*x+lambda*v)/wd*sn)
}

// idle reports that every blob is back on its drift path, so the animation is
// pure drift again.
func (n *blobNudges) idle() bool {
	for _, p := range n {
		if p.mode != blobDrift {
			return false // held or in flight: something is happening every frame
		}
		if math.Abs(p.ox) > 1e-4 || math.Abs(p.oy) > 1e-4 || math.Abs(p.os) > 1e-4 ||
			math.Abs(p.vx) > 1e-5 || math.Abs(p.vy) > 1e-5 || math.Abs(p.vs) > 1e-5 {
			return false
		}
		if math.Abs(p.ea) > 1e-4 || math.Abs(p.eb) > 1e-4 || math.Abs(p.sx) > 1e-4 || math.Abs(p.sy) > 1e-4 ||
			math.Abs(p.vea) > 1e-5 || math.Abs(p.veb) > 1e-5 || math.Abs(p.vsx) > 1e-5 || math.Abs(p.vsy) > 1e-5 {
			return false // still changing shape
		}
	}
	return true
}

// clampAbs holds a spring's offset inside ±limit, zeroing the velocity that took
// it out of range: a blob pinned at the limit has nothing left to spend going
// further.
func clampAbs(o, v, limit float64) (float64, float64) {
	if o > limit {
		return limit, 0
	}
	if o < -limit {
		return -limit, 0
	}
	return o, v
}

// feedBlob is one metaball's motion: it drifts on two independent sines (a
// Lissajous path) and breathes on a third. All amplitudes are fractions of the
// pane, so the scene composes the same at any size.
type feedBlob struct {
	r          float64 // radius, as a fraction of the pane's short side
	ax, ay     float64 // drift amplitude, as a fraction of pane width / height
	wx, wy, wr float64 // angular speed (rad/s) of the x drift, y drift and radius pulse
	px, py, pr float64 // phase offsets, so no two blobs move in lockstep
}

// blobOmega converts a loop period into radians per second. Everything here is
// on a wall clock rather than a frame counter: the frame rate is configurable,
// and drift that sped up when you asked for smoother animation would be a bug.
func blobOmega(period time.Duration) float64 {
	return 2 * math.Pi / period.Seconds()
}

// feedBlobs is the cast. The drift amplitudes are small, so the blobs stay
// overlapped in the middle of the pane and read as one slowly kneading mass
// rather than four things chasing each other around it. The periods are
// mutually prime minutes: the combined field has no repeat a viewer could
// catch, and nothing in it moves fast enough to pull an eye off the composer.
var feedBlobs = [...]feedBlob{
	{r: 0.30, ax: 0.07, ay: 0.07, wx: blobOmega(181 * time.Second), wy: blobOmega(127 * time.Second), wr: blobOmega(149 * time.Second), px: 0.0, py: 1.3, pr: 0.4},
	{r: 0.24, ax: 0.09, ay: 0.06, wx: blobOmega(139 * time.Second), wy: blobOmega(167 * time.Second), wr: blobOmega(113 * time.Second), px: 2.1, py: 0.2, pr: 2.6},
	{r: 0.20, ax: 0.10, ay: 0.08, wx: blobOmega(211 * time.Second), wy: blobOmega(101 * time.Second), wr: blobOmega(173 * time.Second), px: 4.0, py: 3.4, pr: 1.1},
	{r: 0.16, ax: 0.11, ay: 0.07, wx: blobOmega(97 * time.Second), wy: blobOmega(229 * time.Second), wr: blobOmega(131 * time.Second), px: 5.2, py: 5.0, pr: 3.9},
}

// feedBlobPalette tints the ramp, one colour per shading step: near-neutral
// steel, cooling to a soft slate at the densest step so the core is the one
// thing with any colour in it.
//
// Both runs are short and stop well before the extremes. Neither end may
// approach the terminal's own foreground — a near-black step on a light
// background (or a near-white one on a dark background) puts the inkiest
// glyphs at full text contrast, and the whole thing starts reading as writing
// on the screen instead of a surface behind it. blobPaletteLimit in the tests
// holds that line. Index 0 is the blank step and is never rendered.
var feedBlobPalette = [len(blobRamp)]adaptiveColor{
	{light: lipgloss.Color("254"), dark: lipgloss.Color("236")}, // blank, never rendered
	{light: lipgloss.Color("253"), dark: lipgloss.Color("237")},
	{light: lipgloss.Color("251"), dark: lipgloss.Color("239")},
	{light: lipgloss.Color("249"), dark: lipgloss.Color("241")},
	{light: lipgloss.Color("247"), dark: lipgloss.Color("243")},
	{light: lipgloss.Color("245"), dark: lipgloss.Color("245")},
	{light: lipgloss.Color("243"), dark: lipgloss.Color("246")},
	{light: lipgloss.Color("60"), dark: lipgloss.Color("109")},
}

// feedBlobStyles is one style per ramp step, built once. adaptiveColor
// resolves against the terminal background at render time, so these need no
// rebuilding when the background is learned.
var feedBlobStyles = func() [len(blobRamp)]lipgloss.Style {
	var s [len(blobRamp)]lipgloss.Style
	for i, c := range feedBlobPalette {
		s[i] = lipgloss.NewStyle().Foreground(c)
	}
	return s
}()

// blobFalloff is Wyvill's kernel over q = (d/r)²: 1 at the centre, 0 at the
// edge, smooth in between. Finite support is the point — an inverse-square
// field would haze every cell in the pane up to ramp step 1 and there'd be no
// empty space left for the blobs to move through.
func blobFalloff(q float64) float64 {
	if q >= 1 {
		return 0
	}
	return 1 + q*(-22.0/9.0+q*(17.0/9.0-q*(4.0/9.0)))
}

// blobFrame is one blob resolved to pane coordinates for a single frame:
// where it is, how big, how it is deformed, and how its mass is split.
//
// The deformation is carried as the quadratic form of its outline rather than
// as a pair of radii, because the outline is an ellipse with an orientation of
// its own: q(dx,dy) = qa·dx² + 2qb·dx·dy + qc·dy² is 1 on the rim, and offsets
// are in row units (a column offset is worth cellAspect of one). det is
// qa·qc − qb², which the area-preserving construction keeps at 1/r⁴.
type blobFrame struct {
	cx, cy     float64 // centre, in (column, row)
	r          float64 // the radius the deformation is measured against, row units
	qa, qb, qc float64
	det        float64
	sx, sy     float64 // the lobes' offset from the centre, ±half of it, row units
}

// support is how far the outline reaches from the centre along the unit
// direction (nx,ny), in row units — the lobes included, so this is the extent
// the walls actually meet.
func (b blobFrame) support(nx, ny float64) float64 {
	// For {x : xᵗQx ≤ 1} the support along n is √(nᵗQ⁻¹n).
	h := math.Sqrt((b.qc*nx*nx - 2*b.qb*nx*ny + b.qa*ny*ny) / b.det)
	return h + math.Abs(b.sx*nx+b.sy*ny)/2
}

// radii is how far the point (dx,dy) — row units, from the centre — is out in
// this blob's own deformed radii: 1 on the rim, whatever the shape.
func (b blobFrame) radii(dx, dy float64) float64 {
	return math.Sqrt(b.qa*dx*dx + 2*b.qb*dx*dy + b.qc*dy*dy)
}

// blobNode is one metaball as the renderer sees it: a centre, the weight its
// field carries, and its outline's quadratic form. A blob is one node while
// its lobes are together and two when they are not, which is why a blob can
// stop being an ellipse at all.
type blobNode struct {
	cx, cy     float64
	weight     float64
	qa, qb, qc float64
	det        float64
	reach      float64 // rows this node's outline reaches from its centre
}

// feedBlobFrame places every blob t seconds into the animation. Positions are
// computed once per frame, not once per cell.
func feedBlobFrame(w, h int, t float64, nudge blobNudges) []blobFrame {
	fw, fh := float64(w), float64(h)
	// Radii key off the pane's short side, measured in row units, so a narrow
	// pane shrinks the blobs instead of pushing them off both edges.
	scale := math.Min(fh, fw*cellAspect)
	out := make([]blobFrame, len(feedBlobs))
	for i, b := range feedBlobs {
		p := nudge[i]
		// The swell cap allows a big negative excursion the springs never reach;
		// the floor is there so a pathological one can't invert the blob.
		r := b.r * scale * (1 + blobPulse*math.Sin(b.wr*t+b.pr)) * math.Max(1+p.os, 0.05)
		f := blobFrame{
			cx: fw/2 + b.ax*fw*math.Sin(b.wx*t+b.px) + p.ox*fw,
			cy: fh/2 + b.ay*fh*math.Sin(b.wy*t+b.py) + p.oy*fh,
			r:  r,
			sx: p.sx * r,
			sy: p.sy * r,
		}
		f.qa, f.qb, f.qc, f.det = strainForm(r, p.ea, p.eb)
		out[i] = f
	}
	return out
}

// strainForm turns a radius and a deviatoric strain (ea,eb) into the quadratic
// form of the deformed outline.
//
// The deformation is x ↦ Mx with M = k·[[1+ea, eb], [eb, 1−ea]], symmetric so
// it is a pure squash-and-stretch about some axis with no rotation in it, and
// k = 1/√(1−ea²−eb²) chosen so det M = 1 — area conserved exactly, which is
// Lasseter's volume constancy and the difference between a blob squashing and
// a blob shrinking. The outline is then M applied to a circle of radius r, and
// its quadratic form is (M⁻¹)ᵗM⁻¹/r².
func strainForm(r, ea, eb float64) (qa, qb, qc, det float64) {
	area := 1 - ea*ea - eb*eb
	if area < 0.05 { // the cap keeps this far away; a guard, not a limit
		area = 0.05
	}
	k2 := 1 / area // k², the area normalisation
	inv := k2 / (r * r)
	// M⁻¹ = k·[[1−ea, −eb], [−eb, 1+ea]]; the form is the sum of the squares of
	// its rows.
	qa = inv * ((1-ea)*(1-ea) + eb*eb)
	qc = inv * ((1+ea)*(1+ea) + eb*eb)
	qb = inv * -2 * eb
	return qa, qb, qc, 1 / (r * r * r * r)
}

// blobNodes resolves the cast into the metaballs to draw, appending into buf.
// A blob whose lobes are together is one node of full weight — bit for bit the
// field a single round metaball drew before any of this existed — and a
// sloshing one is two half-weight lobes, which merge into a lopsided shape
// where they overlap.
func blobNodes(blobs []blobFrame, buf []blobNode) []blobNode {
	out := buf[:0]
	for _, b := range blobs {
		n := blobNode{cx: b.cx, cy: b.cy, weight: 1, qa: b.qa, qb: b.qb, qc: b.qc, det: b.det}
		// The outline's row extent: |dy| ≤ √(qa/det) on the rim.
		n.reach = math.Sqrt(b.qa / b.det)
		if math.Abs(b.sx) < 1e-3 && math.Abs(b.sy) < 1e-3 {
			out = append(out, n)
			continue
		}
		n.weight = 0.5
		// The lobes straddle the centre, so the blob's centre of area doesn't
		// move — the slosh redistributes the mass, it doesn't carry it.
		dx, dy := b.sx/2/cellAspect, b.sy/2 // dx back into columns
		a, c := n, n
		a.cx, a.cy = n.cx+dx, n.cy+dy
		c.cx, c.cy = n.cx-dx, n.cy-dy
		out = append(out, a, c)
	}
	return out
}

// renderFeedBlobs composites the whole w×h empty-feed canvas at t seconds:
// every cell is the summed field of all the metaballs, quantised onto blobRamp
// and tinted by its step. Overlapping blobs add, so they merge into one shape
// where they meet instead of drawing over each other.
//
// The tank's walls are the edges of this canvas and are not drawn: the pane
// border is already right there, one cell out, and a rim inside it read as a
// second border rather than as glass. What a blob pressed into a wall loses to
// it is clipped by the canvas, which is the same thing the border implies.
//
// Every row is exactly w cells wide and there are exactly h of them — the feed
// viewport soft-wraps, and an overflowing row would reflow the whole scene.
func renderFeedBlobs(w, h int, t float64, nudge blobNudges) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	nodes := blobNodes(feedBlobFrame(w, h, t, nudge), make([]blobNode, 0, 2*len(feedBlobs)))
	field := make([]float64, w)
	level := make([]int, w)
	lines := make([]string, h)
	top := len(blobRamp) - 1
	for y := 0; y < h; y++ {
		fy := float64(y) + 0.5
		clear(field)
		// The kernel has finite support, so most cells are outside most blobs.
		// Walking each node's own span instead of testing every cell against
		// every node is what keeps a full-pane frame affordable at 60 fps; the
		// cells it skips are the ones the kernel returns 0 for, so the field —
		// summed in the same order — comes out bit-identical.
		for _, n := range nodes {
			dy := fy - n.cy
			if math.Abs(dy) >= n.reach {
				continue
			}
			// The row's slice of the outline: q ≤ 1 is a quadratic in dx, whose
			// two roots straddle a centre the deformation's shear puts off to one
			// side. In row units, then widened into columns.
			disc := n.qa - dy*dy*n.det
			if disc <= 0 {
				continue
			}
			mid := -n.qb * dy / n.qa
			half := math.Sqrt(disc) / n.qa
			x0 := max(int(math.Ceil((n.cx+(mid-half)/cellAspect)-0.5)), 0)
			x1 := min(int(math.Floor((n.cx+(mid+half)/cellAspect)-0.5)), w-1)
			// The form's dy terms are the same for every cell in the row.
			shear, flat := 2*n.qb*dy, n.qc*dy*dy
			for x := x0; x <= x1; x++ {
				dx := (float64(x) + 0.5 - n.cx) * cellAspect
				field[x] += n.weight * blobFalloff(dx*(n.qa*dx+shear)+flat)
			}
		}
		for x, f := range field {
			if f == 0 { // untouched by every blob: the blank step, no exp needed
				level[x] = 0
				continue
			}
			level[x] = min(max(int(blobShade(f)*float64(top)+0.5), 0), top)
		}
		lines[y] = styleBlobRow(level)
	}
	return strings.Join(lines, "\n")
}

// blobShade compresses summed density into the 0..1 the ramp spans. The blobs
// sit on top of each other, so a plain sum would saturate the whole overlap to
// the densest glyph and the mass would render as a flat slab with a shaded
// rim. This keeps giving ground as density piles up, so the interior still
// carries a gradient and only a genuine pile-up reaches the top of the ramp.
func blobShade(f float64) float64 { return 1 - math.Exp(-f) }

// blobSGR is one ramp step's escape sequences: what lipgloss wraps a span in
// for that step's style, taken apart once so a run can be written as
// prefix + glyphs + suffix.
type blobSGR struct{ prefix, suffix string }

// blobSGRCache holds those sequences for the whole ramp, together with the
// background they were resolved against — the palette is adaptive, so a
// terminal that reports its background late invalidates them.
var blobSGRCache struct {
	sync.Mutex
	light bool
	ok    bool
	steps [len(blobRamp)]blobSGR
}

// blobSGRSteps returns the per-step escapes, building them on first use (and
// again after the terminal background changes). They come out of lipgloss
// itself rather than being written by hand: the exact bytes a Style emits
// depend on the colour profile, and a row that guessed them would render
// differently from every other styled span on the screen.
func blobSGRSteps() *[len(blobRamp)]blobSGR {
	light := lightBackground.Load()
	blobSGRCache.Lock()
	defer blobSGRCache.Unlock()
	if !blobSGRCache.ok || blobSGRCache.light != light {
		for i, st := range feedBlobStyles {
			// A one-cell span, split around its glyph: whatever precedes it opens
			// the style and whatever follows closes it.
			const cell = "\x00"
			pre, suf, _ := strings.Cut(st.Render(cell), cell)
			blobSGRCache.steps[i] = blobSGR{prefix: pre, suffix: suf}
		}
		blobSGRCache.light, blobSGRCache.ok = light, true
	}
	return &blobSGRCache.steps
}

// styleBlobRow renders one row of ramp steps, coalescing equal-step runs into
// a single styled span so the output stays compact. Blank runs are written
// raw — an empty cell needs no colour.
//
// The spans are pasted from cached escapes rather than rendered: at 60 fps a
// full pane is thousands of runs a second, and lipgloss re-measures the span
// it is styling every time.
func styleBlobRow(level []int) string {
	steps := blobSGRSteps()
	var b strings.Builder
	b.Grow(len(level) + 16)
	for i := 0; i < len(level); {
		j := i
		for j < len(level) && level[j] == level[i] {
			j++
		}
		if level[i] != 0 {
			b.WriteString(steps[level[i]].prefix)
		}
		for k := i; k < j; k++ {
			b.WriteByte(blobRamp[level[i]])
		}
		if level[i] != 0 {
			b.WriteString(steps[level[i]].suffix)
		}
		i = j
	}
	return b.String()
}

// feedBlobTickMsg drives the empty-feed animation. At most one is in flight,
// guarded by feedState.blobActive. gen is the loop it belongs to: a poke
// starts a new, faster loop at once rather than waiting out the calm frame gap
// already in flight, and the tick left over from the old loop is dropped by its
// stale gen.
type feedBlobTickMsg struct{ gen uint64 }

// feedBlobTickCmd schedules the next frame of loop gen.
func feedBlobTickCmd(interval time.Duration, gen uint64) tea.Cmd {
	if interval <= 0 {
		interval = feedBlobIdleInterval
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return feedBlobTickMsg{gen: gen} })
}

// feedEmptyArtVisible reports whether the Feed tab is currently showing the
// animated empty-state art: on the tab, built, nothing unread, no error.
func (m *Model) feedEmptyArtVisible() bool {
	return m.onFeedTab() && !m.feed.loading && m.feed.err == "" && len(m.feed.entries) == 0
}

// maybeStartFeedBlobs arms the animation loop when the empty-state art is on
// screen and the loop isn't already running. Idempotent; returns nil when
// there's nothing to animate.
func (m *Model) maybeStartFeedBlobs() tea.Cmd {
	if m.feed.blobActive || !m.feedEmptyArtVisible() {
		return nil
	}
	m.feed.blobActive = true
	return feedBlobTickCmd(m.feedBlobInterval(), m.feed.blobGen)
}

// restartFeedBlobs arms the loop again from this instant, superseding whatever
// tick is in flight. A poke lands while the field is drifting at the calm rate,
// with up to a whole idle frame gap still to run: waiting it out would put a
// visible hitch between the press and the swell, which is the one moment here
// that has to feel immediate.
func (m *Model) restartFeedBlobs() tea.Cmd {
	if !m.feedEmptyArtVisible() {
		return nil
	}
	m.feed.blobGen++
	m.feed.blobActive = true
	return feedBlobTickCmd(m.feedBlobInterval(), m.feed.blobGen)
}

// applyFeedBlobTick advances one frame, redraws the field, and reschedules —
// stopping (and clearing the guard) the moment the art is no longer shown.
func (m *Model) applyFeedBlobTick(gen uint64) tea.Cmd {
	// A tick from a loop a poke has already superseded: dropping it is what
	// keeps exactly one loop running (see restartFeedBlobs).
	if gen != m.feed.blobGen {
		return nil
	}
	if !m.feedEmptyArtVisible() {
		m.feed.blobActive = false
		// The field went away under the pointer.
		m.feed.holdBlob(-1)
		m.feed.pressBlob(-1)
		return nil
	}
	dt := m.feedBlobInterval()
	m.feed.blobPhase += dt.Seconds()
	m.stepFeedBlobs(dt.Seconds())
	painted := m.feed.blobPainted
	m.renderFeedResults()
	// preservesFrame keeps the memoized screen for this tick by default, since
	// most frames at a high frame rate redraw the field as it already is. A
	// frame that did move something drops the memo here.
	if m.vcache != nil && m.feed.blobPainted != painted {
		m.vcache.viewValid = false
	}
	// The rate is re-read rather than reused: the spring this frame advanced may
	// have been the last one still moving.
	return feedBlobTickCmd(m.feedBlobInterval(), gen)
}

// feedBlobMinW / feedBlobMinH are the smallest pane the field is worth drawing
// in. Below that the blobs have no room to move and it reads as noise, so a
// one-line hint takes over.
const (
	feedBlobMinW = 24
	feedBlobMinH = 8
)

// feedBlobFieldDrawn reports whether the animated field itself is on screen —
// the empty state *and* a pane big enough for it (below that, feedEmptyContent
// draws a one-line hint instead, and there is nothing to poke).
func (m *Model) feedBlobFieldDrawn() bool {
	return m.feedEmptyArtVisible() &&
		m.feed.view.Width() >= feedBlobMinW && m.feed.view.Height() >= feedBlobMinH
}

// pokeFeedBlobs presses cell (col,row) into the field. Two kernels, pulling
// opposite ways: blobPushWeight shoves a blob away — nothing on its centre,
// most just off its side — while blobSwellWeight swells it, hardest on the
// centre. So a middle press spreads the blob you hit, a side press slides it,
// and a click well clear of everything does neither.
func (m *Model) pokeFeedBlobs(col, row int) {
	w, h := m.feed.view.Width(), m.feed.view.Height()
	if w <= 0 || h <= 0 {
		return
	}
	fw, fh := float64(w), float64(h)
	// Impulses (per second) that a best-placed click turns into exactly
	// blobPushSpan of travel and blobSizeSpan of swell: a spring kicked at v₀
	// peaks at gain·v₀/ω.
	imp := blobPushSpan * math.Min(fh, fw*cellAspect) * blobPushOmega / blobPushGain
	swell := blobSizeSpan / blobSwellGain
	px, py := float64(col)+0.5, float64(row)+0.5
	for i, b := range feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge) {
		// Row units, so the push is as far up as it is sideways on screen.
		dx := (b.cx - px) * cellAspect
		dy := b.cy - py
		d := math.Hypot(dx, dy)
		u := b.radii(dx, dy)
		// The swell drive is split: part lands as displacement on this very frame
		// (mouse-down is felt at once), the rest as velocity the spring eases in.
		if k := swell * blobSwellWeight(u); k > 0 {
			n := &m.feed.blobNudge[i]
			n.os, n.vs = clampAbs(n.os+blobSwellSnap*k, n.vs+(1-blobSwellSnap)*k*blobSwellOmega, blobSizeCap)
		}
		weight := blobPushWeight(u)
		if weight <= 0 || d < 1e-9 {
			continue // on the centre, or well clear of the blob: nowhere to push it
		}
		v := imp * weight / d // magnitude on the unit vector away from the cell
		m.feed.blobNudge[i].vx += v * dx / (cellAspect * fw)
		m.feed.blobNudge[i].vy += v * dy / fh
	}
}

// pressFeedBlobs records which blob a press at cell (col,row) landed in — the
// nearest one whose middle it is inside, measured in its own radii — as the
// candidate for a drag. Reports whether it found one.
//
// It does not pick the blob up: that is blobDragThreshold's business. The
// press itself is still the poke it always was, and pokeFeedBlobs runs on
// every press whether this found a candidate or not.
func (m *Model) pressFeedBlobs(col, row int) bool {
	w, h := m.feed.view.Width(), m.feed.view.Height()
	if w <= 0 || h <= 0 {
		return false
	}
	px, py := float64(col)+0.5, float64(row)+0.5
	best, bestU := -1, blobGrabReach
	for i, b := range feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge) {
		if u := b.radii((b.cx-px)*cellAspect, b.cy-py); u < bestU {
			best, bestU = i, u
		}
	}
	m.feed.pressBlob(best)
	if best < 0 {
		return false
	}
	m.feed.blobPressX, m.feed.blobPressY = px, py
	// Pointer tracking starts at the press, not at the moment the drag does, so
	// the velocity a throw inherits is measured over the whole gesture.
	m.feed.blobPtrX, m.feed.blobPtrY = px, py
	m.feed.blobPtrAt = time.Now()
	m.feed.blobPtrVX, m.feed.blobPtrVY = 0, 0
	return true
}

// beginFeedBlobDrag picks the pressed blob up, with the pointer now at cell
// (col,row).
//
// The grab point is measured here rather than at the press, so the blob does
// not jump the two or three cells the pointer has travelled getting past the
// threshold — position stays continuous, which is the one rule everything in
// this file obeys. Whatever the press's own shove had it doing is dropped:
// from here the pointer is the only thing moving it.
func (m *Model) beginFeedBlobDrag(col, row int) {
	i := m.feed.pressedBlob()
	w, h := m.feed.view.Width(), m.feed.view.Height()
	if i < 0 || w <= 0 || h <= 0 {
		return
	}
	b := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[i]
	px, py := float64(col)+0.5, float64(row)+0.5
	m.feed.holdBlob(i)
	m.feed.pressBlob(-1)
	m.feed.blobGrabX, m.feed.blobGrabY = b.cx-px, b.cy-py
	p := &m.feed.blobNudge[i]
	p.mode = blobHeld
	p.vx, p.vy = 0, 0
}

// dragFeedBlobs moves the held blob to cell (col,row), keeping the point it
// was grabbed by under the pointer: tracking is 1:1 and unfiltered, because a
// dragged thing that lags the pointer stops reading as held.
//
// It also keeps a running estimate of the pointer's velocity, which is what
// the throw is made of. A single frame's delta is noise, so it is smoothed —
// an exponential average, since terminal motion reports arrive at whatever
// rate they arrive at.
func (m *Model) dragFeedBlobs(col, row int) {
	i := m.feed.heldBlob()
	w, h := m.feed.view.Width(), m.feed.view.Height()
	if i < 0 || i >= len(m.feed.blobNudge) || w <= 0 || h <= 0 {
		return
	}
	fw, fh := float64(w), float64(h)
	px, py := float64(col)+0.5, float64(row)+0.5
	p := &m.feed.blobNudge[i]
	was := [2]float64{m.feed.blobPtrVX, m.feed.blobPtrVY}
	m.sampleFeedBlobPointer(px, py)
	// A held blob's velocity *is* the pointer's, so a change of direction under
	// the hand sloshes the goo exactly the way a wall does. Without this a
	// dragged blob is a rigid thing on a string.
	if r := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[i].r; r > 0 {
		p.vsx -= blobSloshLag * (m.feed.blobPtrVX - was[0]) * fw * cellAspect / r
		p.vsy -= blobSloshLag * (m.feed.blobPtrVY - was[1]) * fh / r
	}
	// Where the blob's centre wants to be, turned back into an offset from its
	// drift path — the drift is what the offset is measured against, so the grab
	// has to be resolved against this frame's drift position.
	drift := feedBlobFrame(w, h, m.feed.blobPhase, blobNudges{})[i]
	p.mode = blobHeld
	p.ox = (px + m.feed.blobGrabX - drift.cx) / fw
	p.oy = (py + m.feed.blobGrabY - drift.cy) / fh
	// Dragged into a wall, the blob stops at it rather than sinking through —
	// the same clamp a bounce uses, minus the rebound.
	m.confineFeedBlob(i, feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[i])
}

// sampleFeedBlobPointer folds one pointer position into the velocity estimate
// a throw is made of. A single motion report's delta is noise — terminals send
// them at whatever rate they send them at — so it is smoothed exponentially,
// and stamped so a release can tell a throw from letting go of a blob that had
// been held still.
func (m *Model) sampleFeedBlobPointer(px, py float64) {
	fw, fh := float64(m.feed.view.Width()), float64(m.feed.view.Height())
	now := time.Now()
	if dt := now.Sub(m.feed.blobPtrAt).Seconds(); dt > 1e-3 && fw > 0 && fh > 0 {
		const smooth = 0.4 // weight on the newest sample
		vx := (px - m.feed.blobPtrX) / dt / fw
		vy := (py - m.feed.blobPtrY) / dt / fh
		m.feed.blobPtrVX = smooth*vx + (1-smooth)*m.feed.blobPtrVX
		m.feed.blobPtrVY = smooth*vy + (1-smooth)*m.feed.blobPtrVY
	}
	m.feed.blobPtrX, m.feed.blobPtrY, m.feed.blobPtrAt = px, py, now
}

// releaseFeedBlobs lets go. With the pointer still moving the blob is thrown:
// it inherits the pointer's measured velocity and flies (blobFree) until drag
// and the walls have taken it. Let go of a blob held still and it just stays
// where it was put, and the drift spring walks it home.
func (m *Model) releaseFeedBlobs() {
	i := m.feed.heldBlob()
	m.feed.holdBlob(-1)
	if i < 0 || i >= len(m.feed.blobNudge) {
		return
	}
	p := &m.feed.blobNudge[i]
	p.mode = blobDrift
	if time.Since(m.feed.blobPtrAt) > blobThrowStale {
		return // the pointer had already stopped: this is a place, not a throw
	}
	w, h := m.feed.view.Width(), m.feed.view.Height()
	fw, fh := float64(w), float64(h)
	scale := math.Min(fh, fw*cellAspect)
	if scale <= 0 {
		return
	}
	vx, vy := m.feed.blobPtrVX, m.feed.blobPtrVY
	// Speed in pane short-sides per second, so the cap and the rest threshold
	// mean the same thing on any pane.
	speed := math.Hypot(vx*fw*cellAspect, vy*fh) / scale
	if speed < blobRestSpeed {
		return
	}
	if speed > blobThrowMax {
		vx, vy = vx*blobThrowMax/speed, vy*blobThrowMax/speed
	}
	p.mode = blobFree
	p.vx, p.vy = vx, vy
}

// stepFeedBlobs advances the whole field dt seconds: every blob's body first
// — a drift spring, a drag, or a wall it is in the middle of hitting — and
// then the shape springs, which answer to what the body just did.
func (m *Model) stepFeedBlobs(dt float64) {
	w, h := m.feed.view.Width(), m.feed.view.Height()
	var blobs []blobFrame
	if w > 0 && h > 0 {
		blobs = feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)
	}
	fw, fh := float64(w), float64(h)
	for i := range m.feed.blobNudge {
		p := &m.feed.blobNudge[i]
		var target blobStrain
		var dvx, dvy float64 // the frame's velocity change, in row units per second
		switch {
		case blobs == nil:
			p.advanceDrift(dt)
		case p.mode == blobHeld:
			// The drag owns the body, but not the walls: a held blob still has to
			// be kept in the tank every frame, not only on the frames the pointer
			// moves — the swell from the press that picked it up carries on
			// growing, and a blob that swelled while held against the glass would
			// grow straight through it.
			m.confineFeedBlob(i, blobs[i])
			// The shape answers to the wall it is being pressed into, and to the
			// pointer's own accelerations, which dragFeedBlobs has already fed in.
			target = m.contactStrain(i, blobs[i])
		case p.mode == blobFree:
			vx0, vy0 := p.vx, p.vy
			target = m.flyFeedBlob(i, blobs[i], dt)
			dvx, dvy = (p.vx-vx0)*fw*cellAspect, (p.vy-vy0)*fh
			if m.feedBlobSpeed(i) < blobRestSpeed && !m.feedBlobTouching(i) {
				// Out of momentum and clear of the walls: hand it back to the drift
				// spring, which takes it from exactly this position and velocity.
				p.mode = blobDrift
			}
		default:
			p.advanceDrift(dt)
		}
		r, omega := 1.0, blobStrainFreq(0, 0)
		if blobs != nil {
			r = blobs[i].r
			omega = blobStrainFreq(r, m.feedBlobScale())
		}
		p.advanceShape(dt, omega, target, dvx/r, dvy/r)
	}
}

// blobContactFreq is the contact spring's frequency for a blob of radius r in
// a pane whose short side is scale, both in row units.
//
// A spring struck at v peaks at v/ω, so pinning the peak penetration to a
// fraction of the radius at the fastest throw pins ω. Everything slower than
// that goes less deep and comes back out sooner, which is what a soft body
// does: the harder you throw it, the more of it gives.
func blobStrainFreq(r, scale float64) float64 {
	return blobStrainRatio * blobContactFreq(r, scale)
}

func blobContactFreq(r, scale float64) float64 {
	if r <= 0 {
		return blobContactCeil
	}
	return min(max(blobThrowMax*scale/(blobContactDepth*r), blobContactFloor), blobContactCeil)
}

// blobWallGap is how far blob i, placed as b, is from each wall along each
// axis: the room left on the low and high side, in canvas units (columns for x,
// rows for y). Negative means it is inside the wall.
//
// Measured against the blob's *undeformed* radius, not its flattened outline.
// That matters: the flattening is driven by this penetration, so reading the
// penetration back off the flattened shape would be a feedback loop, and one
// that lets a blob squash itself free of the wall it is still being pushed out
// of. Taken this way the two stay consistent — with blobContactSquash at 1 the
// centre goes in by exactly what the blob flattens by, so the rim sits on the
// wall and what you see is a facet.
func blobWallGap(b blobFrame, fw, fh float64) (loX, hiX, loY, hiY float64) {
	// The slosh is left out for the same reason: it moves mass around inside
	// the body, and letting it move the body's collision extent would make the
	// penetration jitter frame to frame, which a spring reads as energy.
	halfX := b.r / cellAspect // row units → columns
	halfY := b.r
	return b.cx - halfX, fw - (b.cx + halfX), b.cy - halfY, fh - (b.cy + halfY)
}

// flyFeedBlob advances a free blob's body one frame and returns the
// deformation its contacts (or its flight) are driving.
//
// Each axis is either in contact or not. In contact it is a damped spring in
// penetration space — the blob goes *into* the wall, slows, stops and is
// pushed back out over the six or seven frames the contact lasts, with the
// rebound speed falling out of blobContactZeta rather than being multiplied on
// by hand. Clear of the walls it is plain drag.
func (m *Model) flyFeedBlob(i int, b blobFrame, dt float64) blobStrain {
	p := &m.feed.blobNudge[i]
	fw, fh := float64(m.feed.view.Width()), float64(m.feed.view.Height())
	loX, hiX, loY, hiY := blobWallGap(b, fw, fh)
	omega := blobContactFreq(b.r, m.feedBlobScale())
	penX := axisStep(&p.ox, &p.vx, loX, hiX, fw, b.r/cellAspect, omega, dt)
	penY := axisStep(&p.oy, &p.vy, loY, hiY, fh, b.r, omega, dt)
	// Friction: a blob scrubbing along a wall loses sideways speed to it, so a
	// glancing hit comes off at an angle instead of mirroring.
	if penX != 0 {
		p.vy *= math.Exp(-blobContactFriction * dt)
	}
	if penY != 0 {
		p.vx *= math.Exp(-blobContactFriction * dt)
	}
	if penX == 0 && penY == 0 {
		return m.flightStrain(i)
	}
	// The contacts, summed: penetration in row units, flattening against each
	// wall's own normal.
	var t blobStrain
	if penX != 0 {
		t = t.add(squashStrain(blobContactSquash*math.Abs(penX)*cellAspect/b.r, math.Copysign(1, penX), 0))
	}
	if penY != 0 {
		t = t.add(squashStrain(blobContactSquash*math.Abs(penY)/b.r, 0, math.Copysign(1, penY)))
	}
	return t
}

// axisStep advances one axis of a free blob. lo and hi are how much room the
// blob has on the low and high side of the tank (negative: it is in the wall),
// extent the pane's size on this axis in canvas units, reach the blob's own
// radius in those units — what the penetration cap is measured in — and omega
// the stiffness of its contact.
// It returns the penetration left at the end of the frame, signed by which
// wall: positive at the low wall, negative at the high one, zero when clear.
func axisStep(o, v *float64, lo, hi, extent, reach, omega, dt float64) float64 {
	var pen, dir float64
	switch {
	case lo < 0 && hi < 0:
		return 0 // the blob is wider than the tank on this axis: nothing to push against
	case lo < 0:
		pen, dir = -lo, +1
	case hi < 0:
		pen, dir = -hi, -1
	default:
		*o, *v = glideStep(*o, *v, dt)
		return 0
	}
	if cap := blobPenetrationCap * reach; pen > cap {
		// Deeper than a contact should ever get (a throw fast enough to cross the
		// wall in one frame): put the blob on the surface and let the spring
		// below take it from there.
		*o += dir * (pen - cap) / extent
		pen = cap
	}
	// In penetration space p, with ṗ = −dir·(the velocity): p̈ = −2ζωṗ − ω²p.
	// Stepped in closed form like every other spring here, so the contact runs
	// the same motion whatever the frame rate samples it at.
	pdot0 := -dir * *v * extent
	p, pdot := springStep(pen, pdot0, omega, blobContactZeta, dt)
	if p < 0 {
		// The blob came off the wall part way through this frame. The rest of the
		// frame belongs to the flight, and it has to be given to it: leaving the
		// blob parked on the surface with its rebound velocity intact is how a
		// contact turns into flypaper — the position never moves, the velocity
		// bleeds away, and the blob sits there vibrating in place.
		frac := pen / (pen - p) // where in the frame it crossed the surface
		_, exit := springStep(pen, pdot0, omega, blobContactZeta, dt*frac)
		*o += dir * pen / extent
		*v = -dir * exit / extent
		*o, *v = glideStep(*o, *v, dt*(1-frac))
		return 0
	}
	*o += dir * (pen - p) / extent
	*v = -dir * pdot / extent
	return dir * p
}

// flightStrain is the deformation of a blob in mid-air: stretched along its
// own velocity, by more the faster it is going. Goo in flight is not a disc
// being translated.
func (m *Model) flightStrain(i int) blobStrain {
	p := m.feed.blobNudge[i]
	fw, fh := float64(m.feed.view.Width()), float64(m.feed.view.Height())
	// Row units, so the direction is the one seen on screen rather than the one
	// in the offsets.
	vx, vy := p.vx*fw*cellAspect, p.vy*fh
	speed := math.Hypot(vx, vy)
	if speed < 1e-9 {
		return blobStrain{}
	}
	scale := m.feedBlobScale()
	if scale <= 0 {
		return blobStrain{}
	}
	// Negative: a stretch along the direction of travel, not a squash.
	s := -blobFlightStretch * math.Tanh(speed/scale/blobStretchVRef)
	return squashStrain(s, vx/speed, vy/speed)
}

// contactStrain is the flattening of a blob that is being *held* against a
// wall: the same penetration-driven squash a bounce uses, with no rebound —
// dragged into the glass, the blob spreads on it.
func (m *Model) contactStrain(i int, b blobFrame) blobStrain {
	fw, fh := float64(m.feed.view.Width()), float64(m.feed.view.Height())
	loX, hiX, loY, hiY := blobWallGap(b, fw, fh)
	var t blobStrain
	if pen := math.Min(loX, hiX); pen < 0 {
		t = t.add(squashStrain(blobContactSquash*-pen*cellAspect/b.r, 1, 0))
	}
	if pen := math.Min(loY, hiY); pen < 0 {
		t = t.add(squashStrain(blobContactSquash*-pen/b.r, 0, 1))
	}
	return t
}

// feedBlobTouching reports whether blob i's outline is in a wall — a blob is
// not handed back to the drift spring in the middle of a contact, or it would
// be left resting inside the glass.
func (m *Model) feedBlobTouching(i int) bool {
	w, h := m.feed.view.Width(), m.feed.view.Height()
	if w <= 0 || h <= 0 {
		return false
	}
	b := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[i]
	loX, hiX, loY, hiY := blobWallGap(b, float64(w), float64(h))
	return loX < 0 || hiX < 0 || loY < 0 || hiY < 0
}

// confineFeedBlob holds a dragged blob inside the tank: the pointer can go
// where it likes, the blob stops at the glass. The give is the strain
// contactStrain reads off the same penetration, so it spreads against the wall
// rather than stopping dead at it.
func (m *Model) confineFeedBlob(i int, b blobFrame) {
	fw, fh := float64(m.feed.view.Width()), float64(m.feed.view.Height())
	if fw <= 0 || fh <= 0 {
		return
	}
	p := &m.feed.blobNudge[i]
	loX, hiX, loY, hiY := blobWallGap(b, fw, fh)
	// A blob wider than the tank on an axis is left alone on it — there is no
	// inside to put it in. Otherwise it may press in as far as the cap, which
	// is what draws the facet.
	if loX >= 0 || hiX >= 0 {
		if give := blobHoldDepth * b.r / cellAspect; loX < -give {
			p.ox += (-give - loX) / fw
		} else if hiX < -give {
			p.ox -= (-give - hiX) / fw
		}
	}
	if loY >= 0 || hiY >= 0 {
		if give := blobHoldDepth * b.r; loY < -give {
			p.oy += (-give - loY) / fh
		} else if hiY < -give {
			p.oy -= (-give - hiY) / fh
		}
	}
}

// feedBlobScale is the pane's short side in row units — what every speed here
// is measured against, so the physics feels the same on any pane.
func (m *Model) feedBlobScale() float64 {
	fw, fh := float64(m.feed.view.Width()), float64(m.feed.view.Height())
	return math.Min(fh, fw*cellAspect)
}

// feedBlobSpeed is blob i's speed in pane short-sides per second.
func (m *Model) feedBlobSpeed(i int) float64 {
	scale := m.feedBlobScale()
	if scale <= 0 {
		return 0
	}
	p := m.feed.blobNudge[i]
	return math.Hypot(p.vx*float64(m.feed.view.Width())*cellAspect, p.vy*float64(m.feed.view.Height())) / scale
}

// clickFeedBlobs is the mouse entry point for a press: shove the field, note
// which blob (if any) the press landed in so a drag can start from it, then
// repaint at once — waiting for the next tick would put a frame of lag on the
// one thing here that responds to you — and make sure the loop is running.
func (m *Model) clickFeedBlobs(col, row int) tea.Cmd {
	m.pressFeedBlobs(col, row)
	m.pokeFeedBlobs(col, row)
	m.renderFeedResults()
	// restart, not maybeStart: the loop is normally already running, at the calm
	// drifting rate, and the poke needs it at the live one now.
	return m.restartFeedBlobs()
}

// dragFeedBlobMotion is the motion entry point while the button is down over
// the field. It reports whether it took the event: a press that has not gone
// anywhere yet is still just a press, and the pointer belongs to whatever the
// motion handler does with it otherwise.
//
// The pointer may have left the field — a drag doesn't stop at the pane
// border — so the cell is not hit-tested. It is taken raw, and it is the blob
// that gets confined.
func (m *Model) dragFeedBlobMotion(x, y int) (tea.Cmd, bool) {
	top, _, yoff := m.feedGeom()
	col, row := x-1, yoff+y-top
	switch {
	case m.feed.heldBlob() >= 0:
	case m.feed.pressedBlob() >= 0:
		px, py := float64(col)+0.5, float64(row)+0.5
		dx := (px - m.feed.blobPressX) * cellAspect
		dy := py - m.feed.blobPressY
		if math.Hypot(dx, dy) < blobDragThreshold {
			// Still a press. The sample is kept anyway, so the throw's velocity
			// covers the whole gesture rather than starting cold at the threshold.
			m.sampleFeedBlobPointer(px, py)
			return nil, true
		}
		m.beginFeedBlobDrag(col, row)
	default:
		return nil, false
	}
	m.dragFeedBlobs(col, row)
	m.renderFeedResults()
	return m.restartFeedBlobs(), true
}

// dropFeedBlobs is the release entry point: throw or place what was being
// dragged, forget the press either way, and keep the loop live so the flight
// is animated.
func (m *Model) dropFeedBlobs() tea.Cmd {
	m.feed.pressBlob(-1)
	if m.feed.heldBlob() < 0 {
		return nil // a press that never became a drag: the poke was the whole of it
	}
	m.releaseFeedBlobs()
	m.renderFeedResults()
	return m.restartFeedBlobs()
}

// feedEmptyContent is the body shown when the feed has no entries: the
// animated blob field when there's room for it, otherwise a one-line hint.
func (m *Model) feedEmptyContent() string {
	w, h := m.feed.view.Width(), m.feed.view.Height()
	if w < feedBlobMinW || h < feedBlobMinH {
		return lipgloss.NewStyle().Foreground(dimColor).Render("  all caught up — nothing unread")
	}
	return renderFeedBlobs(w, h, m.feed.blobPhase, m.feed.blobNudge)
}
