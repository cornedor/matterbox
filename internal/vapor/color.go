package vapor

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// RGB holds a colour as floats (0..255) so blending stays smooth before the
// final quantisation to bytes for the terminal.
type RGB struct{ R, G, B float64 }

func (c RGB) u8() (uint8, uint8, uint8) {
	return clampByte(c.R), clampByte(c.G), clampByte(c.B)
}

func clampByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// lerp linearly interpolates between two colours.
func lerp(a, b RGB, t float64) RGB {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	return RGB{
		a.R + (b.R-a.R)*t,
		a.G + (b.G-a.G)*t,
		a.B + (b.B-a.B)*t,
	}
}

func scale(c RGB, s float64) RGB { return RGB{c.R * s, c.G * s, c.B * s} }

// gradientAt evaluates a multi-stop linear gradient at t in [0,1], with the
// stops spread evenly from t=0 (first) to t=1 (last).
func gradientAt(stops []RGB, t float64) RGB {
	switch len(stops) {
	case 0:
		return RGB{}
	case 1:
		return stops[0]
	}
	t = clamp01(t)
	seg := t * float64(len(stops)-1)
	i := int(seg)
	if i >= len(stops)-1 {
		return stops[len(stops)-1]
	}
	return lerp(stops[i], stops[i+1], seg-float64(i))
}

// parseHexColor parses a "#rgb" or "#rrggbb" colour (the leading '#' optional).
func parseHexColor(s string) (RGB, error) {
	orig := strings.TrimSpace(s)
	h := strings.TrimPrefix(orig, "#")
	if len(h) == 3 { // expand shorthand: f0a -> ff00aa
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return RGB{}, fmt.Errorf("invalid hex colour %q (want #rgb or #rrggbb)", orig)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return RGB{}, fmt.Errorf("invalid hex colour %q", orig)
	}
	return RGB{float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff)}, nil
}

// parseSunStops parses a comma-separated list of hex colours into gradient
// stops running from the top of the sun down to the bottom.
func parseSunStops(s string) ([]RGB, error) {
	var stops []RGB
	for _, p := range strings.Split(s, ",") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		c, err := parseHexColor(p)
		if err != nil {
			return nil, err
		}
		stops = append(stops, c)
	}
	if len(stops) == 0 {
		return nil, fmt.Errorf("no colours given")
	}
	return stops, nil
}

func (c RGB) lum() float64 { return 0.299*c.R + 0.587*c.G + 0.114*c.B }

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// hash2 returns a deterministic pseudo-random value in [0,1) for an (x,y) pair.
func hash2(x, y int) float64 {
	h := uint32(x)*374761393 + uint32(y)*668265263
	h = (h ^ (h >> 13)) * 1274126177
	h ^= h >> 16
	return float64(h&0xffffff) / float64(0x1000000)
}

func lerp1(a, b, t float64) float64 { return a + (b-a)*t }

func hash3(x, y, seed int) float64 {
	h := uint32(x)*374761393 ^ uint32(y)*668265263 ^ uint32(seed)*2246822519
	h = (h ^ (h >> 13)) * 1274126177
	h ^= h >> 16
	return float64(h&0xffffff) / float64(0x1000000)
}

// vnoise2 is smooth 2D value noise in [0,1).
func vnoise2(x, y float64, seed int) float64 {
	xi := int(math.Floor(x))
	yi := int(math.Floor(y))
	xf := x - float64(xi)
	yf := y - float64(yi)
	a := hash3(xi, yi, seed)
	b := hash3(xi+1, yi, seed)
	c := hash3(xi, yi+1, seed)
	d := hash3(xi+1, yi+1, seed)
	u := xf * xf * (3 - 2*xf)
	v := yf * yf * (3 - 2*yf)
	return lerp1(lerp1(a, b, u), lerp1(c, d, u), v)
}

// fbm2 is fractional Brownian motion of 2D value noise in [0,1).
func fbm2(x, y float64, seed int) float64 {
	amp, freq, sum, norm := 0.5, 1.0, 0.0, 0.0
	for o := 0; o < 4; o++ {
		sum += amp * vnoise2(x*freq, y*freq, seed+o*101)
		norm += amp
		amp *= 0.5
		freq *= 2
	}
	return sum / norm
}

// ridgedFbm2 sums octaves of ridged 2D noise for sharp mountain terrain.
func ridgedFbm2(x, y float64, seed int) float64 {
	amp, freq, sum, norm := 0.5, 1.0, 0.0, 0.0
	for o := 0; o < 5; o++ {
		v := vnoise2(x*freq, y*freq, seed+o*131)
		r := 1 - math.Abs(2*v-1)
		r *= r // sharpen ridges into peaks
		sum += amp * r
		norm += amp
		amp *= 0.5
		freq *= 2
	}
	return sum / norm
}
