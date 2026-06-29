package vapor

import (
	"math"
	"testing"
)

const introAnim = `{
  "duration": 6,
  "sun":      {"y":    [{"t":0,"v":0},{"t":6,"v":1}]},
  "mountain": {"speed": [{"t":5,"v":1},{"t":6,"v":3}]},
  "text":     {"pos": {"z": [{"t":0,"v":40},{"t":5,"v":22}]}}
}`

// TestDelayByShiftsChoreographyNotDrift is the contract the demo intro relies on:
// shifting the keyframes delays the sun rise and title fly-in, but the terrain
// keeps drifting from t=0 because the speed track holds its first value before
// its first keyframe.
func TestDelayByShiftsChoreographyNotDrift(t *testing.T) {
	const d = 2.0

	base, err := LoadAnimationJSON([]byte(introAnim))
	if err != nil {
		t.Fatal(err)
	}
	delayed, err := LoadAnimationJSON([]byte(introAnim))
	if err != nil {
		t.Fatal(err)
	}
	delayed.DelayBy(d)

	// Title fly-in (text.pos.z) is delayed: at t=2 the delayed clip is still at
	// its start value (40), matching the un-delayed clip at t=0.
	if v, _ := evalTrack(delayed.f.Text.Pos.Z, d); v != 40 {
		t.Errorf("delayed pos.z at t=%v = %v, want 40 (held at start)", d, v)
	}
	v0, _ := evalTrack(base.f.Text.Pos.Z, 3)
	vd, _ := evalTrack(delayed.f.Text.Pos.Z, 3+d)
	if math.Abs(v0-vd) > 1e-9 {
		t.Errorf("fly-in not shifted by %v: base(3)=%v delayed(%v)=%v", d, v0, 3+d, vd)
	}

	// Terrain drift (integrated mountain.speed) is unchanged before the first
	// speed keyframe — the mountains move from t=0 either way.
	for _, at := range []float64{0.5, 2, 4} {
		b, _ := base.speedDistance(at)
		dd, _ := delayed.speedDistance(at)
		if math.Abs(b-dd) > 1e-9 {
			t.Errorf("drift changed at t=%v: base=%v delayed=%v", at, b, dd)
		}
	}

	// DelayBy(<=0) is a no-op.
	noop, _ := LoadAnimationJSON([]byte(introAnim))
	noop.DelayBy(0)
	if v, _ := evalTrack(noop.f.Text.Pos.Z, 0); v != 40 {
		t.Errorf("DelayBy(0) altered track: pos.z at 0 = %v", v)
	}
}
