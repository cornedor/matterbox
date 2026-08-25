package svgimg

import (
	"fmt"
	"image"
	"strings"
	"testing"
)

func TestLooks(t *testing.T) {
	yes := []struct{ name, in string }{
		{"bare root", `<svg xmlns="http://www.w3.org/2000/svg"></svg>`},
		{"xml declaration first", `<?xml version="1.0"?><svg></svg>`},
		{"doctype first", `<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" ""><svg></svg>`},
		{"comment first", "<!-- drawn by hand -->\n<svg></svg>"},
		{"leading whitespace and BOM", "\xef\xbb\xbf\n  <svg></svg>"},
		{"namespaced root", `<svg:svg xmlns:svg="http://www.w3.org/2000/svg"></svg:svg>`},
		{"internal dtd", `<!DOCTYPE svg [<!ENTITY ns_svg "x">]><svg></svg>`},
	}
	for _, tc := range yes {
		if !Looks([]byte(tc.in)) {
			t.Errorf("Looks(%s) = false, want true", tc.name)
		}
	}
	no := []struct{ name, in string }{
		{"empty", ""},
		{"png magic", "\x89PNG\r\n\x1a\n"},
		{"html", `<html><body><svg></svg></body></html>`},
		{"plain text naming svg", "this is an svg file, honest"},
		{"json", `{"svg":true}`},
		{"unterminated comment", "<!-- <svg>"},
	}
	for _, tc := range no {
		if Looks([]byte(tc.in)) {
			t.Errorf("Looks(%s) = true, want false", tc.name)
		}
	}
}

// alphaAt reports the alpha of one pixel of a rasterised drawing.
func alphaAt(t *testing.T, img image.Image, x, y int) uint32 {
	t.Helper()
	_, _, _, a := img.At(x, y).RGBA()
	return a
}

func TestDecodeDrawsShapes(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100"><rect width="100" height="100" fill="red"/></svg>`
	res, err := Decode([]byte(src), Options{MaxSide: 64})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	b := res.Image.Bounds()
	if b.Dx() != 64 || b.Dy() != 64 {
		t.Errorf("raster is %dx%d, want 64x64", b.Dx(), b.Dy())
	}
	if res.W != 100 || res.H != 100 {
		t.Errorf("intrinsic size = %dx%d, want 100x100", res.W, res.H)
	}
	if got := alphaAt(t, res.Image, 32, 32); got == 0 {
		t.Error("centre of a filled rect is transparent")
	}
}

// TestDecodeCompactArcKeepsTheHole is the regression that motivated normalising
// path data: this ring is one `a` command carrying several parameter sets, the
// form every SVG optimiser emits. Handed to the rasteriser as-is it comes out a
// filled blob, so the assertion is that the middle is still empty.
func TestDecodeCompactArcKeepsTheHole(t *testing.T) {
	ring := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><path d="m8 2a6 6 0 0 0-6 6 6 6 0 0 0 6 6 6 6 0 0 0 6-6 6 6 0 0 0-6-6zm0 1a5 5 0 0 1 5 5 5 5 0 0 1-5 5 5 5 0 0 1-5-5 5 5 0 0 1 5-5z" fill="black"/></svg>`
	res, err := Decode([]byte(ring), Options{MaxSide: 128})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := alphaAt(t, res.Image, 64, 64); got != 0 {
		t.Errorf("centre of the ring is filled (alpha %d): the arcs were misparsed", got)
	}
	if got := alphaAt(t, res.Image, 64, 20); got == 0 {
		t.Error("the ring itself was not drawn")
	}
}

// TestDecodePackedArcFlags covers the other optimiser shorthand: flags written
// with no separator at all ("a7 7 0 100 14" is flags 1, 0 then x=0).
func TestDecodePackedArcFlags(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><path d="M8 1a7 7 0 100 14 7 7 0 000-14z" fill="black"/></svg>`
	res, err := Decode([]byte(src), Options{MaxSide: 64})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := alphaAt(t, res.Image, 32, 32); got == 0 {
		t.Error("a circle built from packed-flag arcs drew nothing")
	}
}

// TestDecodePercentageSizeUsesViewBox pins the other real-world trap: the
// rasteriser abandons the root element when width="100%" will not parse, losing
// the viewBox that came after it.
func TestDecodePercentageSizeUsesViewBox(t *testing.T) {
	src := `<svg xmlns='http://www.w3.org/2000/svg' width='100%' height='100%' viewBox='0 0 20 10'><rect width='20' height='10' fill='black'/></svg>`
	res, err := Decode([]byte(src), Options{MaxSide: 40})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if res.W != 20 || res.H != 10 {
		t.Errorf("intrinsic size = %dx%d, want 20x10 from the viewBox", res.W, res.H)
	}
	if b := res.Image.Bounds(); b.Dx() != 40 || b.Dy() != 20 {
		t.Errorf("raster is %dx%d, want 40x20 (aspect kept)", b.Dx(), b.Dy())
	}
	if got := alphaAt(t, res.Image, 20, 10); got == 0 {
		t.Error("nothing was drawn")
	}
}

func TestDecodeCurrentColor(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="currentColor"/></svg>`
	res, err := Decode([]byte(src), Options{MaxSide: 20, CurrentColor: "#ff0000"})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	r, g, b, a := res.Image.At(10, 10).RGBA()
	if a == 0 {
		t.Fatal("currentColor shape drew nothing")
	}
	if r < 0x8000 || g > 0x4000 || b > 0x4000 {
		t.Errorf("currentColor resolved to rgba(%d,%d,%d,%d), want red", r, g, b, a)
	}
}

func TestDecodeEntityDeclarations(t *testing.T) {
	// Illustrator exports declare their own entities, which Go's strict XML
	// parser rejects outright unless they are stripped first.
	src := `<?xml version="1.0"?>
<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "svg11.dtd" [<!ENTITY ns_svg "http://www.w3.org/2000/svg">]>
<svg xmlns="&ns_svg;" width="10" height="10"><rect width="10" height="10" fill="black"/></svg>`
	res, err := Decode([]byte(src), Options{MaxSide: 20})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := alphaAt(t, res.Image, 10, 10); got == 0 {
		t.Error("nothing drawn from a document with entity declarations")
	}
}

func TestDecodeReportsDroppedText(t *testing.T) {
	withText := `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20"><text x="1" y="15">hi</text></svg>`
	res, err := Decode([]byte(withText), Options{MaxSide: 40})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !res.TextDropped {
		t.Error("TextDropped = false for a document whose only content is text")
	}
	plain := `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`
	res, err = Decode([]byte(plain), Options{MaxSide: 10})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if res.TextDropped {
		t.Error("TextDropped = true for a document with no text")
	}
}

func TestDecodeNoIntrinsicSize(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" width="100%" height="100%"><rect width="10" height="10"/></svg>`
	res, err := Decode([]byte(src), Options{MaxSide: 60})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if res.W != int(defaultW) || res.H != int(defaultH) {
		t.Errorf("intrinsic size = %dx%d, want the %vx%v default", res.W, res.H, defaultW, defaultH)
	}
}

func TestDecodeRejects(t *testing.T) {
	if _, err := Decode(nil, Options{}); err == nil {
		t.Error("Decode(nil) succeeded, want an error")
	}
	big := []byte(`<svg xmlns="http://www.w3.org/2000/svg">` + strings.Repeat(" ", MaxBytes) + `</svg>`)
	if _, err := Decode(big, Options{}); err == nil {
		t.Error("Decode of an over-size document succeeded, want an error")
	}
}

// TestDecodeSurvivesGarbage is the guard that matters operationally: this runs on
// a background goroutine, so neither a parse failure nor a panic inside the
// rasteriser may escape as anything but an error.
func TestDecodeSurvivesGarbage(t *testing.T) {
	cases := []string{
		`<svg`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path d="MMMM"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0a"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 -1 -1"><rect width="1" height="1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="a b c d"><circle r="NaN"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0L1e999999 1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path transform="matrix(0,0,0,0,0,0)" d="M0 0h1v1h-1z"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" width="0" height="0"></svg>`,
		"<svg xmlns=\"http://www.w3.org/2000/svg\"><rect width=\"\x00\x01\"/></svg>",
	}
	for i, src := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			// Any outcome is fine except a panic escaping.
			if _, err := Decode([]byte(src), Options{MaxSide: 32}); err != nil {
				t.Logf("rejected: %v", err)
			}
		})
	}
}

func TestDescribeMatchesDecode(t *testing.T) {
	for _, src := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" width="8mm" height="4mm"><rect width="1" height="1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" width="100%" height="100%"><rect width="1" height="1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20"><text x="0" y="10">hi</text></svg>`,
	} {
		w, h, dropped := Describe([]byte(src))
		res, err := Decode([]byte(src), Options{MaxSide: 32})
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if w != res.W || h != res.H {
			t.Errorf("Describe = %dx%d, Decode = %dx%d for %s", w, h, res.W, res.H, src)
		}
		if dropped != res.TextDropped {
			t.Errorf("Describe text=%v, Decode text=%v", dropped, res.TextDropped)
		}
	}
}

func TestParseLength(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
	}{
		{"12", 12}, {"12px", 12}, {" 24 ", 24}, {"100%", 0}, {"", 0}, {"1in", 96},
		{"72pt", 96}, {"1pc", 16}, {"abc", 0}, {"1em", 0},
	} {
		if got := parseLength(tc.in); got != tc.want {
			t.Errorf("parseLength(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFitKeepsAspectAndBounds(t *testing.T) {
	w, h := fit(200, 100, 50)
	if w != 50 || h != 25 {
		t.Errorf("fit(200,100,50) = %dx%d, want 50x25", w, h)
	}
	// A small drawing is scaled up: downstream only ever scales down.
	if w, h := fit(16, 16, 512); w != 512 || h != 512 {
		t.Errorf("fit(16,16,512) = %dx%d, want 512x512", w, h)
	}
	// An extreme aspect ratio stays inside the pixel cap.
	if w, h := fit(1, 100000, 100000); w*h > maxPixels {
		t.Errorf("fit produced %dx%d = %d pixels, over the %d cap", w, h, w*h, maxPixels)
	}
}
