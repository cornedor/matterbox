package vapor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// keyframe is one control point on an animation track: the property reaches
// value V at time T (seconds), interpolated from the previous keyframe with the
// named easing (the ease is a property of the segment ending at this keyframe).
type keyframe struct {
	T    float64 `json:"t"`
	V    float64 `json:"v"`
	Ease string  `json:"ease,omitempty"`
}

// track is a time-ordered list of keyframes describing one scalar property.
// An empty track means "not animated" — the scene keeps its static value.
type track []keyframe

// vec3Track groups three independent scalar tracks for an x/y/z property.
type vec3Track struct {
	X track `json:"x,omitempty"`
	Y track `json:"y,omitempty"`
	Z track `json:"z,omitempty"`
}

// animFile is the on-disk JSON schema for an animation. Track values are
// absolute, per-property: a track replaces that property's static (flag) value,
// while a property with no track keeps the flag value. The schema reserves room
// for a future `texts` array (multiple words shown in sequence) without breaking
// existing files.
type animFile struct {
	Duration float64 `json:"duration"` // loop length in seconds (required when loop is true)
	Loop     bool    `json:"loop"`     // wrap time at duration and keep driving forward
	Sun      struct {
		Y track `json:"y,omitempty"` // vertical position multiplier (1 = default, >1 higher)
	} `json:"sun"`
	Mountain struct {
		Speed track `json:"speed,omitempty"` // scroll-speed multiplier (1 = default)
	} `json:"mountain"`
	Text struct {
		Pos vec3Track `json:"pos"` // world-space position x,y,z
		Rot vec3Track `json:"rot"` // rotation x,y,z in degrees (pitch, yaw, roll)
	} `json:"text"`
}

// Animation is a loaded, validated animation ready to sample per frame.
type Animation struct {
	f animFile
}

// LoadAnimationJSON parses and validates a JSON animation from raw bytes (e.g.
// an embedded file). The schema matches vaporascii's -anim files.
func LoadAnimationJSON(b []byte) (*Animation, error) {
	var af animFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // surface typos like "speeed" instead of ignoring them
	if err := dec.Decode(&af); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	a := &Animation{f: af}
	if err := a.validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// eachTrack visits every track in the file by pointer so callers can sort and
// validate them in place.
func (a *Animation) eachTrack(fn func(name string, tr *track)) {
	fn("sun.y", &a.f.Sun.Y)
	fn("mountain.speed", &a.f.Mountain.Speed)
	fn("text.pos.x", &a.f.Text.Pos.X)
	fn("text.pos.y", &a.f.Text.Pos.Y)
	fn("text.pos.z", &a.f.Text.Pos.Z)
	fn("text.rot.x", &a.f.Text.Rot.X)
	fn("text.rot.y", &a.f.Text.Rot.Y)
	fn("text.rot.z", &a.f.Text.Rot.Z)
}

// validate sorts each track by time and rejects nonsensical input.
func (a *Animation) validate() error {
	if a.f.Duration < 0 {
		return fmt.Errorf("duration must be >= 0")
	}
	if a.f.Loop && a.f.Duration <= 0 {
		return fmt.Errorf("loop requires a positive duration")
	}
	var verr error
	a.eachTrack(func(name string, tr *track) {
		if verr != nil || len(*tr) == 0 {
			return
		}
		sort.SliceStable(*tr, func(i, j int) bool { return (*tr)[i].T < (*tr)[j].T })
		for _, k := range *tr {
			if math.IsNaN(k.T) || math.IsNaN(k.V) || math.IsInf(k.T, 0) || math.IsInf(k.V, 0) {
				verr = fmt.Errorf("%s: keyframe has non-finite t or v", name)
				return
			}
			if !validEase(k.Ease) {
				verr = fmt.Errorf("%s: unknown ease %q", name, k.Ease)
				return
			}
		}
	})
	return verr
}

// localTime folds an absolute time into the loop window when looping is enabled.
func (a *Animation) localTime(t float64) float64 {
	if a.f.Loop && a.f.Duration > 0 {
		t = math.Mod(t, a.f.Duration)
		if t < 0 {
			t += a.f.Duration
		}
	}
	return t
}

// speedDistance returns the integrated speed multiplier ∫₀ᵗ speed(τ)dτ over the
// mountain.speed track. The camera position is this integral (not t·speed), so a
// changing speed never teleports the terrain. When looping, full-period integrals
// accumulate per completed loop so the drive keeps moving forward. ok is false
// when there is no speed track (the caller falls back to the static speed).
func (a *Animation) speedDistance(t float64) (float64, bool) {
	tr := a.f.Mountain.Speed
	if len(tr) == 0 {
		return 0, false
	}
	if a.f.Loop && a.f.Duration > 0 && t > 0 {
		p := a.f.Duration
		loops := math.Floor(t / p)
		local := t - loops*p
		return loops*integrateTrack(tr, p) + integrateTrack(tr, local), true
	}
	return integrateTrack(tr, t), true
}

// evalTrack samples a track at time t, holding the first/last value before/after
// the keyframe range. ok is false when the track is empty.
func evalTrack(tr track, t float64) (float64, bool) {
	if len(tr) == 0 {
		return 0, false
	}
	n := len(tr)
	if t <= tr[0].T {
		return tr[0].V, true
	}
	if t >= tr[n-1].T {
		return tr[n-1].V, true
	}
	for i := 1; i < n; i++ {
		if t <= tr[i].T {
			a, b := tr[i-1], tr[i]
			span := b.T - a.T
			if span <= 0 {
				return b.V, true
			}
			u := ease(b.Ease, (t-a.T)/span)
			return a.V + (b.V-a.V)*u, true
		}
	}
	return tr[n-1].V, true
}

// integrateTrack returns ∫₀ᵗ track(τ)dτ for t >= 0, treating the value as held
// constant (clamped) before the first and after the last keyframe. The eased
// segments are integrated in closed form so the result is exact and continuous.
func integrateTrack(tr track, t float64) float64 {
	if len(tr) == 0 || t <= 0 {
		return 0
	}
	if t <= tr[0].T {
		return tr[0].V * t // constant hold before the first keyframe
	}
	sum := tr[0].V * tr[0].T
	n := len(tr)
	for i := 1; i < n; i++ {
		a, b := tr[i-1], tr[i]
		if t <= a.T {
			break
		}
		span := b.T - a.T
		if span <= 0 {
			continue
		}
		segEnd := math.Min(t, b.T)
		u := (segEnd - a.T) / span
		sum += span * segIntegral(b.Ease, a.V, b.V, u)
		if t <= b.T {
			return sum
		}
	}
	return sum + tr[n-1].V*(t-tr[n-1].T) // constant hold after the last keyframe
}

// segIntegral returns ∫₀ᵘ (v0 + (v1-v0)·ease(s)) ds in u-units (the caller scales
// by the segment's time span).
func segIntegral(easeName string, v0, v1, u float64) float64 {
	return v0*u + (v1-v0)*easeIntegral(easeName, u)
}

func validEase(name string) bool {
	switch name {
	case "", "linear", "smooth", "smoothstep", "in", "ease-in", "out", "ease-out":
		return true
	}
	return false
}

// ease remaps a normalized 0..1 progress with the named curve.
func ease(name string, u float64) float64 {
	if u < 0 {
		u = 0
	} else if u > 1 {
		u = 1
	}
	switch name {
	case "smooth", "smoothstep":
		return u * u * (3 - 2*u)
	case "in", "ease-in":
		return u * u
	case "out", "ease-out":
		return u * (2 - u)
	default: // "", "linear"
		return u
	}
}

// easeIntegral returns ∫₀ᵘ ease(s) ds, the closed-form antiderivative of ease.
func easeIntegral(name string, u float64) float64 {
	switch name {
	case "smooth", "smoothstep":
		return u*u*u - u*u*u*u/2
	case "in", "ease-in":
		return u * u * u / 3
	case "out", "ease-out":
		return u*u - u*u*u/3
	default: // "", "linear"
		return u * u / 2
	}
}
