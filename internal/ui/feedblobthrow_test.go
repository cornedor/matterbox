package ui

import (
	"math"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// grabBlobAt presses cell (col,row) and drags to (col+dcol,row+drow), which
// has to be far enough to cross blobDragThreshold. Reports the blob that ended
// up in hand, or -1.
func grabBlobAt(m *Model, col, row, dcol, drow int) int {
	m.pressFeedBlobs(col, row)
	m.pokeFeedBlobs(col, row)
	pointerDrag(m, col+dcol, row+drow, 16*time.Millisecond)
	return m.feed.heldBlob()
}

// pointerDrag moves the pointer to (col,row) as a motion report dt seconds
// after the last one — the velocity estimate is built from wall-clock gaps
// between reports, so a test that wants a known throw speed has to place its
// samples in time.
func pointerDrag(m *Model, col, row int, dt time.Duration) {
	m.feed.blobPtrAt = time.Now().Add(-dt)
	// Through the same gate the motion handler uses, so a drag in a test has to
	// cross the threshold exactly as one on screen does. The cell is turned
	// back into screen coordinates for it.
	top, _, yoff := m.feedGeom()
	m.dragFeedBlobMotion(col+1, row+top-yoff)
}

// TestFeedBlobGrabTracksPointer is the grab: once a press in a blob has
// travelled far enough to be a drag, the blob's centre follows the pointer
// one-to-one, keeping the point it was picked up by under the cursor. A press
// out in the open goo has nothing to pick up — that is still the shove.
func TestFeedBlobGrabTracksPointer(t *testing.T) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	// Off-centre, but inside the smallest blob: the grab point has to be
	// preserved, so a press that isn't dead on a middle is the interesting one.
	// Which blob it picks up is the field's business — they overlap, and the
	// nearest centre in its own radii wins.
	b := feedBlobFrame(w, h, 0, blobNudges{})[3]
	col, row := int(b.cx)+2, int(b.cy)+1
	idx := grabBlobAt(m, col, row, -4, 0)
	if idx < 0 {
		t.Fatalf("a press at (%d,%d) dragged 4 columns picked up nothing", col, row)
	}
	if m.feed.blobNudge[idx].mode != blobHeld {
		t.Fatal("the grabbed blob is not in the held regime")
	}
	if m.feed.pressedBlob() >= 0 {
		t.Error("the drag left the press candidate armed")
	}
	if m.feed.blobNudge.idle() {
		t.Error("a field with a blob in hand reports itself idle — the loop would stop mid-drag")
	}

	before := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[idx]
	const dcol, drow = -5, 2
	pointerDrag(m, col-4+dcol, row+drow, 16*time.Millisecond)
	after := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[idx]
	if dx, dy := after.cx-before.cx, after.cy-before.cy; math.Abs(dx-dcol) > 0.01 || math.Abs(dy-drow) > 0.01 {
		t.Errorf("the pointer moved (%d,%d) and the blob moved (%.2f,%.2f) — tracking is not 1:1", dcol, drow, dx, dy)
	}

	// Well clear of every blob: nothing to take hold of, however far it drags.
	far := pokeModel(w, h)
	if got := grabBlobAt(far, 1, 1, 9, 4); got >= 0 {
		t.Errorf("a drag from the empty corner picked up blob %d", got)
	}
	if far.feed.pressedBlob() >= 0 {
		t.Error("a press that landed in nothing still recorded a candidate")
	}
}

// TestFeedBlobDragThreshold: a press is a poke until the pointer has gone
// somewhere. Below the threshold the blob does not follow the pointer at all —
// the old behaviour, a shove the spring carries back — and past it the blob
// comes with it, from where it is rather than from where the press was.
func TestFeedBlobDragThreshold(t *testing.T) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	b := feedBlobFrame(w, h, 0, blobNudges{})[3]
	col, row := int(b.cx), int(b.cy)
	m.pressFeedBlobs(col, row)
	m.pokeFeedBlobs(col, row)
	if m.feed.pressedBlob() < 0 {
		t.Fatal("a press in a blob recorded no candidate")
	}
	if m.feed.heldBlob() >= 0 {
		t.Fatal("the press picked the blob up before it had moved anywhere")
	}
	// A poke is what the press did, and it has to have done it: the swell and
	// the push spring are the whole of the old behaviour.
	if p := m.feed.blobNudge[3]; p.vs == 0 && p.vx == 0 && p.vy == 0 {
		t.Error("the press neither swelled nor shoved anything — it was not a poke")
	}

	// One cell sideways: half a row unit, well under the threshold.
	before := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[3]
	pointerDrag(m, col+1, row, 16*time.Millisecond)
	if m.feed.heldBlob() >= 0 {
		t.Errorf("one cell of travel started a drag — the threshold is %.1f row units", blobDragThreshold)
	}
	after := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[3]
	if after.cx != before.cx || after.cy != before.cy {
		t.Errorf("the blob moved (%.2f,%.2f) under the threshold — the press dragged it", after.cx-before.cx, after.cy-before.cy)
	}

	// Far enough, and now it is a drag — and the blob is where it was, not
	// snapped to the pointer's new position.
	pointerDrag(m, col+4, row, 16*time.Millisecond)
	if m.feed.heldBlob() != 3 {
		t.Fatalf("four cells of travel held blob %d, want 3", m.feed.heldBlob())
	}
	held := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[3]
	if math.Abs(held.cx-before.cx) > 0.01 || math.Abs(held.cy-before.cy) > 0.01 {
		t.Errorf("taking hold moved the blob by (%.2f,%.2f) — the grab should be measured where the pointer is",
			held.cx-before.cx, held.cy-before.cy)
	}
}

// TestFeedBlobPressWithoutDragIsAPoke: press and let go without moving, and
// the field behaves exactly as it did before any of this — a shove that
// settles, with nothing picked up and nothing thrown.
func TestFeedBlobPressWithoutDragIsAPoke(t *testing.T) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	b := feedBlobFrame(w, h, 0, blobNudges{})[0]
	m.clickFeedBlobs(rimCell(b))
	if m.feed.heldBlob() >= 0 {
		t.Fatal("a press alone picked a blob up")
	}
	m.dropFeedBlobs()
	if m.feed.pressedBlob() >= 0 {
		t.Error("the release left the press candidate armed")
	}
	for i, p := range m.feed.blobNudge {
		if p.mode != blobDrift {
			t.Errorf("blob %d is in mode %d after a plain click — a click must not throw anything", i, p.mode)
		}
	}
	if m.feed.blobNudge.idle() {
		t.Error("the click moved nothing at all")
	}
}

// TestFeedBlobThrowInheritsPointerVelocity: a release hands the blob the
// velocity the pointer had, so the throw continues the gesture instead of
// starting a new motion. Let go of a blob that was sitting still and it stays
// put, on the drift spring.
func TestFeedBlobThrowInheritsPointerVelocity(t *testing.T) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	b := feedBlobFrame(w, h, 0, blobNudges{})[1]
	m.pressFeedBlobs(int(b.cx), int(b.cy))
	col := int(b.cx)
	for i := 0; i < 5; i++ { // a steady 4 columns per 20ms report: 200 columns/s
		col += 4
		pointerDrag(m, col, int(b.cy), 20*time.Millisecond)
	}
	if m.feed.heldBlob() != 1 {
		t.Fatalf("the drag holds blob %d, want 1", m.feed.heldBlob())
	}
	m.releaseFeedBlobs()
	p := m.feed.blobNudge[1]
	if p.mode != blobFree {
		t.Fatalf("a moving release left the blob in mode %d, want free flight", p.mode)
	}
	if m.feed.heldBlob() >= 0 {
		t.Error("the release did not let go")
	}
	// 200 columns/s over an 80-column pane is 2.5 pane-widths/s, which the cap
	// pulls back; either way the throw must be fast and to the right.
	if want := 200.0 / float64(w); p.vx <= 0.2*want || p.vx > want*1.01 {
		t.Errorf("throw velocity %.3f panes/s, want a right-going throw up to %.3f", p.vx, want)
	}
	if speed := m.feedBlobSpeed(1); speed > blobThrowMax*1.01 {
		t.Errorf("throw speed %.2f short-sides/s exceeds the %.2f cap — a flick could tunnel a wall", speed, blobThrowMax)
	}

	// Held still, then released: a place, not a throw.
	m2 := pokeModel(w, h)
	m2.pressFeedBlobs(int(b.cx), int(b.cy))
	pointerDrag(m2, int(b.cx)+6, int(b.cy), 20*time.Millisecond)
	m2.feed.blobPtrAt = time.Now().Add(-2 * blobThrowStale) // the pointer stopped before letting go
	m2.releaseFeedBlobs()
	if p := m2.feed.blobNudge[1]; p.mode != blobDrift || p.vx != 0 {
		t.Errorf("letting go of a still blob threw it anyway: mode %d, vx %.3f", p.mode, p.vx)
	}
}

// throwAt puts blob i in flight from the middle of the tank with velocity
// (vx,vy) in pane fractions per second.
func throwAt(m *Model, i int, vx, vy float64) {
	p := &m.feed.blobNudge[i]
	p.mode, p.vx, p.vy = blobFree, vx, vy
}

// TestFeedBlobThrowBouncesOffWalls: a thrown blob stays in the tank, comes
// back off the wall it hit with less speed than it arrived with, and is handed
// back to the drift spring once it has run out — the field has to recover its
// composed state on its own.
//
// The contact is also checked for *not being a reflection*: the turnaround has
// to take frames, because a velocity that flips between two frames is a
// mirror, not a collision, and no amount of shape animation hides it.
func TestFeedBlobThrowBouncesOffWalls(t *testing.T) {
	const w, h = 80, 24
	for _, tc := range []struct {
		name   string
		vx, vy float64
	}{
		{"left", -1.2, 0}, {"right", 1.2, 0}, {"top", 0, -1.0}, {"bottom", 0, 1.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := pokeModel(w, h)
			throwAt(m, 3, tc.vx, tc.vy)
			dt := feedBlobLiveInterval.Seconds()
			var approach, rebound float64
			touched, flipped, left, settled := -1, -1, -1, -1
			for f := 0; f < 20*feedBlobLiveFPS; f++ {
				pv := m.feed.blobNudge[3]
				prev := m.feedBlobSpeed(3)
				m.feed.blobPhase += dt
				m.stepFeedBlobs(dt)
				b := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[3]
				// The centre stays in the tank, and the outline may press into a
				// wall only as far as the contact allows — that overlap is the
				// facet the canvas clips, and it is bounded so a throw can never
				// end up on the far side of the glass. Measured against the blob's
				// own radius, which is what the walls are, plus the give: the
				// flattened outline is inside that by construction.
				give := blobPenetrationCap * b.r
				if b.cx < 0 || b.cx > float64(w) || b.cy < 0 || b.cy > float64(h) {
					t.Fatalf("frame %d: the blob's centre is at (%.2f,%.2f) — outside the tank", f, b.cx, b.cy)
				}
				if lo := b.cx - b.r/cellAspect; lo < -give/cellAspect-0.01 {
					t.Fatalf("frame %d: the blob reaches column %.2f, %.2f past the left wall", f, lo, -lo)
				}
				if hi := b.cx + b.r/cellAspect; hi > float64(w)+give/cellAspect+0.01 {
					t.Fatalf("frame %d: the blob reaches column %.2f, past the right wall at %d", f, hi, w)
				}
				if lo := b.cy - b.r; lo < -give-0.01 {
					t.Fatalf("frame %d: the blob reaches row %.2f, %.2f past the ceiling", f, lo, -lo)
				}
				if hi := b.cy + b.r; hi > float64(h)+give+0.01 {
					t.Fatalf("frame %d: the blob reaches row %.2f, past the floor at %d", f, hi, h)
				}
				now := m.feed.blobNudge[3]
				switch {
				case touched < 0:
					if m.feedBlobTouching(3) {
						touched, approach = f, prev
					}
				case flipped < 0:
					// Still being compressed: the frames between touching the wall
					// and heading the other way. This is the whole difference between
					// a contact and a mirror.
					if now.vx*pv.vx < 0 || now.vy*pv.vy < 0 {
						flipped = f
					}
				case left < 0:
					if !m.feedBlobTouching(3) {
						left, rebound = f, m.feedBlobSpeed(3)
					}
				}
				if settled < 0 && now.mode == blobDrift {
					settled = f
				}
			}
			if touched < 0 || flipped < 0 || left < 0 {
				t.Fatalf("the throw never completed a bounce (touched at %d, turned at %d, left at %d)", touched, flipped, left)
			}
			if flipped-touched < 3 {
				t.Errorf("the blob turned around %d frames after touching the wall — that is a reflection, not a contact", flipped-touched)
			}
			if left-touched < 6 {
				t.Errorf("the contact lasted %d frames — too short to see the blob give", left-touched)
			}
			if rebound >= approach {
				t.Errorf("bounced off at %.2f short-sides/s, faster than the %.2f it arrived at", rebound, approach)
			}
			if rebound < 0.1*approach {
				t.Errorf("bounced off at %.2f, a tenth of the %.2f it arrived at — the wall ate the throw", rebound, approach)
			}
			if settled < 0 {
				t.Error("the blob never came to rest — it is left flying forever")
			}
		})
	}
}

// TestFeedBlobBounceBlubbers is the bounce itself. Four things have to be true
// for it to read as goo rather than as a sprite with a transform: the
// flattening builds over the frames the blob is touching the wall instead of
// appearing whole, it is on the axis the blob hit, the mass goes lopsided
// (the slosh — an ellipse cannot do this), and all of it rings out and
// settles.
func TestFeedBlobBounceBlubbers(t *testing.T) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	throwAt(m, 3, 0, 1.1) // straight at the floor
	dt := feedBlobLiveInterval.Seconds()

	var peak, peakSlosh float64
	var growth int // frames over which the flattening built up
	rings, prev := 0, 0.0
	hit, spent := -1, false
	for f := 0; f < 12*feedBlobLiveFPS; f++ {
		m.feed.blobPhase += dt
		m.stepFeedBlobs(dt)
		p := m.feed.blobNudge[3]
		e := math.Hypot(p.ea, p.eb)
		if hit < 0 && e > 0.01 {
			hit = f
		}
		if e > peak {
			peak, growth = e, f-hit+1
		}
		peakSlosh = math.Max(peakSlosh, math.Hypot(p.sx, p.sy))
		// Rings of the deformation about round, counted on the component the
		// floor drives.
		if band := peak / 20; band > 0 && ((prev > band && p.ea < -band) || (prev < -band && p.ea > band)) {
			rings++
		}
		if math.Abs(p.ea) > peak/20 {
			prev = p.ea
		}
		if hit >= 0 && f > hit && e < peak/50 && math.Hypot(p.vea, p.veb) < blobStrainFreq(0, 0)*peak/50 {
			// The blob settles onto the floor over a couple of smaller contacts,
			// so this is the whole landing, not one impact.
			if secs := float64(f-hit) * dt; secs > 5 {
				t.Errorf("the blob was still changing shape %.1fs after landing", secs)
			}
			spent = true
			break
		}
	}
	if hit < 0 {
		t.Fatal("hitting the floor deformed the blob not at all")
	}
	if !spent {
		t.Error("the wobble never died down — the blob is left permanently deformed")
	}
	if growth < 3 {
		t.Errorf("the flattening reached its peak in %d frames — a soft body gives way over the contact, it does not snap to a shape", growth)
	}
	if peak < 0.1 {
		t.Errorf("peak deformation %.3f — too small to read as a squash", peak)
	}
	// A floor hit flattens the blob: wider than it is tall, which is ea > 0
	// (see strainForm). In flight it is the other way round — stretched along
	// the direction it is falling — so this also pins that the contact reverses
	// the deformation rather than just deepening it.
	if flat, flight := peakStrainA(); flat <= 0.1 || flight >= 0 {
		t.Errorf("the floor hit flattened the blob by %+.3f and it flew at %+.3f — want a pancake on the floor and a stretch in the air", flat, flight)
	}
	if peakSlosh < 0.05 {
		t.Errorf("peak slosh %.3f — the mass never went lopsided, so the shape stayed an ellipse", peakSlosh)
	}
	if rings < 2 {
		t.Errorf("the deformation crossed back through round %d times — a bounce that doesn't blubber", rings)
	}
	if rings > 6 {
		t.Errorf("%d crossings — the blob rings like a bell", rings)
	}
}

// peakStrainA replays a throw at the floor and reports the deformation's
// extremes: the flattest it got (on the floor) and the most stretched (in the
// air on the way down).
func peakStrainA() (flat, flight float64) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	throwAt(m, 3, 0, 1.1)
	dt := feedBlobLiveInterval.Seconds()
	for f := 0; f < 3*feedBlobLiveFPS; f++ {
		m.feed.blobPhase += dt
		m.stepFeedBlobs(dt)
		flat = math.Max(flat, m.feed.blobNudge[3].ea)
		flight = math.Min(flight, m.feed.blobNudge[3].ea)
	}
	return flat, flight
}

// TestFeedBlobDeformConservesArea: the deformation is a squash on one axis and
// exactly the matching stretch across it, at every deformation the springs can
// reach. A blob that squashed without spreading would read as shrinking
// instead of as hitting something.
//
// The area of {x : xᵗQx ≤ 1} is π/√det Q, so conserving it means qa·qc − qb²
// stays at 1/r⁴ whatever the strain — checked from the form's own
// coefficients, not from the value strainForm caches.
func TestFeedBlobDeformConservesArea(t *testing.T) {
	const r = 6.0
	for _, e := range [][2]float64{{0, 0}, {0.3, 0}, {-0.4, 0}, {0, 0.35}, {0.25, -0.3}, {0.35, 0.35}} {
		qa, qb, qc, det := strainForm(r, e[0], e[1])
		if got, want := qa*qc-qb*qb, 1/(r*r*r*r); math.Abs(got-want) > 1e-9*want {
			t.Errorf("strain (%.2f,%.2f): det %.9g, want %.9g — the deformation changes the blob's area", e[0], e[1], got, want)
		}
		if math.Abs(det-1/(r*r*r*r)) > 1e-12 {
			t.Errorf("strain (%.2f,%.2f): cached det %.9g disagrees with the form", e[0], e[1], det)
		}
	}
}

// TestFeedBlobSloshBreaksTheEllipse: a sloshing blob is drawn as two lobes, so
// its silhouette stops being a conic section — the widest row of it is not the
// row through its centre, which is the one thing an ellipse can never manage.
// With the lobes together it is exactly the single metaball it always was.
func TestFeedBlobSloshBreaksTheEllipse(t *testing.T) {
	const w, h = 80, 24
	var nudge blobNudges
	if got := len(blobNodes(feedBlobFrame(w, h, 0, nudge), nil)); got != len(feedBlobs) {
		t.Errorf("a resting field draws %d metaballs, want the %d it always drew", got, len(feedBlobs))
	}
	nudge[0].sy = 0.4
	if got := len(blobNodes(feedBlobFrame(w, h, 0, nudge), nil)); got != len(feedBlobs)+1 {
		t.Errorf("a sloshing blob draws %d metaballs, want %d — its lobes should have split", got, len(feedBlobs)+1)
	}

	// Only blob 0, alone in a pane, so the profile below is its own.
	widest, centre := -1, h/2
	rows := strings.Split(stripANSI(renderFeedBlobs(w, h, 0, nudge)), "\n")
	widestInk := -1
	for y, line := range rows {
		if ink := len(line) - strings.Count(line, " "); ink > widestInk {
			widestInk, widest = ink, y
		}
	}
	if widest == centre {
		t.Errorf("the widest row of a sloshing blob is still the one through its centre (row %d) — the shape is an ellipse", widest)
	}
}

// TestFeedBlobDragStopsAtWall: dragged into a wall the blob stops against it —
// it is the blob that is confined, not the pointer, and a drag does not
// rebound (letting go of it there and it just sits there).
func TestFeedBlobDragStopsAtWall(t *testing.T) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	b := feedBlobFrame(w, h, 0, blobNudges{})[2]
	if got := grabBlobAt(m, int(b.cx), int(b.cy), -4, 0); got != 2 {
		t.Fatalf("the drag holds blob %d, want 2", got)
	}
	// Off the canvas and well past the left wall, a few cells per report the
	// way a real drag arrives.
	for col := int(b.cx) - 4; col > -40; col -= 4 {
		pointerDrag(m, col, int(b.cy), 16*time.Millisecond)
	}
	// A few frames of the shape spring, so the blob gives against the glass the
	// way it would on screen.
	for f := 0; f < 12; f++ {
		m.feed.blobPhase += feedBlobLiveInterval.Seconds()
		m.stepFeedBlobs(feedBlobLiveInterval.Seconds())
	}
	got := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[2]
	// What the eye sees is the drawn outline, and the flattening is what keeps
	// it on the wall while the centre presses past where an undeformed blob
	// would have stopped: a sliver is clipped by the canvas, not a lobe.
	if lo := got.cx - got.support(1, 0)/cellAspect; lo < -1.5 {
		t.Errorf("the blob is drawn %.2f columns past the wall — it is being pushed through the glass, not squashing on it", -lo)
	}
	if lo := got.cx - got.support(1, 0)/cellAspect; lo > 1.5 {
		t.Errorf("the blob stopped %.2f columns short of the wall it was dragged into", lo)
	}
	// Pressed against the glass it spreads: squashed on the axis it is being
	// pushed on (ea < 0 is narrow and tall — see strainForm), not stopped dead
	// with its shape intact.
	if p := m.feed.blobNudge[2]; p.ea >= -0.05 {
		t.Errorf("held against the left wall the blob's strain is %+.3f — it stopped dead instead of spreading on the glass", p.ea)
	}
	if m.feed.blobNudge[2].mode != blobHeld {
		t.Error("dragging into a wall let go of the blob")
	}
}

// TestFeedBlobThrowRoute walks the real mouse path: press in a blob, drag,
// release — and the blob is in flight with the loop armed to animate it.
func TestFeedBlobThrowRoute(t *testing.T) {
	m := feedButtonModel(t)
	m.feed.entries = nil
	m.renderFeedResults()
	if !m.feedBlobFieldDrawn() {
		t.Fatalf("no field on a %dx%d empty feed", m.feed.view.Width(), m.feed.view.Height())
	}
	top, _, _ := m.feedGeom()
	w, h := m.feed.view.Width(), m.feed.view.Height()
	b := feedBlobFrame(w, h, m.feed.blobPhase, m.feed.blobNudge)[0]
	x, y := int(b.cx)+1, int(b.cy)+top // the pane's left border owns column 0

	next, _ := m.handleMouseClick(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	got := next.(Model)
	if got.feed.pressedBlob() != 0 {
		t.Fatalf("a press in blob 0 recorded candidate %d", got.feed.pressedBlob())
	}
	if got.feed.heldBlob() >= 0 {
		t.Fatal("the press picked the blob up before the pointer had moved")
	}
	for i := 0; i < 4; i++ {
		x += 3
		got.feed.blobPtrAt = time.Now().Add(-16 * time.Millisecond)
		next, _ = got.handleMouseMotion(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
		got = next.(Model)
	}
	if got.feed.blobNudge[0].mode != blobHeld {
		t.Fatal("the drag lost hold of the blob")
	}
	next, cmd := got.handleMouseRelease(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	got = next.(Model)
	if got.feed.heldBlob() >= 0 {
		t.Error("the release kept hold of the blob")
	}
	if p := got.feed.blobNudge[0]; p.mode != blobFree || p.vx <= 0 {
		t.Errorf("release left the blob in mode %d with vx %.3f, want a right-going throw", p.mode, p.vx)
	}
	if cmd == nil || !got.feed.blobActive {
		t.Error("the throw left the animation loop unarmed — the blob would hang in the air")
	}
}

// TestFeedBlobFlightRateIndependent: free flight is integrated in closed form,
// so a throw travels the same distance whatever frame rate the loop happens to
// be running at. Measured short of any wall — a bounce is resolved on a frame
// boundary and is quantised by the rate on purpose.
func TestFeedBlobFlightRateIndependent(t *testing.T) {
	const w, h = 80, 24
	at := func(fps int) blobNudge {
		m := pokeModel(w, h)
		throwAt(m, 3, 0.18, 0)
		dt := 1 / float64(fps)
		for f := 0; f < fps; f++ { // one second of flight
			m.feed.blobPhase += dt
			m.stepFeedBlobs(dt)
		}
		return m.feed.blobNudge[3]
	}
	ref := at(feedBlobLiveFPS)
	for _, fps := range []int{6, feedBlobIdleFPS, 30} {
		got := at(fps)
		if d := math.Abs(got.ox - ref.ox); d > 0.002 {
			t.Errorf("%d fps: the throw landed at %.4f, %.4f from the %d fps %.4f — the frame rate changed the flight",
				fps, got.ox, d, feedBlobLiveFPS, ref.ox)
		}
	}
}

// TestFeedBlobThrowGoesQuiet: a throw has to end. Everything it sets moving —
// the body, the contacts, the deformation, the slosh — has to come to rest, or
// the field is left asking for 60 fps forever on an idle screen.
func TestFeedBlobThrowGoesQuiet(t *testing.T) {
	const w, h = 80, 24
	m := pokeModel(w, h)
	throwAt(m, 2, 1.4, 0.9) // hard, into a corner
	dt := feedBlobLiveInterval.Seconds()
	for f := 0; f < 90*feedBlobLiveFPS; f++ {
		m.feed.blobPhase += dt
		m.stepFeedBlobs(dt)
		if m.feed.blobNudge.idle() {
			if secs := float64(f) * dt; secs > 60 {
				t.Errorf("the field took %.0fs to go quiet after one throw", secs)
			}
			if got := m.feedBlobInterval(); got != feedBlobIdleInterval {
				t.Errorf("a quiet field still asks for %v", got)
			}
			return
		}
	}
	p := m.feed.blobNudge[2]
	t.Errorf("the field never went quiet: mode %d, offset (%.4f,%.4f), strain (%.4f,%.4f), slosh (%.4f,%.4f)",
		p.mode, p.ox, p.oy, p.ea, p.eb, p.sx, p.sy)
}
