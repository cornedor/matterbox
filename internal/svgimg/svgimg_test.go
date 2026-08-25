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
	res, err := Decode([]byte(src), Options{MaxW: 64, MaxH: 64})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	b := res.Image.Bounds()
	if want := 64 * Supersample; b.Dx() != want || b.Dy() != want {
		t.Errorf("raster is %dx%d, want %dx%d (the box, supersampled)", b.Dx(), b.Dy(), want, want)
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
	res, err := Decode([]byte(ring), Options{MaxW: 128, MaxH: 128})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// Coordinates are fractions of the raster, which is the box supersampled.
	b := res.Image.Bounds()
	cx, cy := b.Dx()/2, b.Dy()/2
	if got := alphaAt(t, res.Image, cx, cy); got != 0 {
		t.Errorf("centre of the ring is filled (alpha %d): the arcs were misparsed", got)
	}
	if got := alphaAt(t, res.Image, cx, b.Dy()*20/128); got == 0 {
		t.Error("the ring itself was not drawn")
	}
}

// TestDecodePackedArcFlags covers the other optimiser shorthand: flags written
// with no separator at all ("a7 7 0 100 14" is flags 1, 0 then x=0).
func TestDecodePackedArcFlags(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><path d="M8 1a7 7 0 100 14 7 7 0 000-14z" fill="black"/></svg>`
	res, err := Decode([]byte(src), Options{MaxW: 64, MaxH: 64})
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
	res, err := Decode([]byte(src), Options{MaxW: 40, MaxH: 40})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if res.W != 20 || res.H != 10 {
		t.Errorf("intrinsic size = %dx%d, want 20x10 from the viewBox", res.W, res.H)
	}
	b := res.Image.Bounds()
	if wantW, wantH := 40*Supersample, 20*Supersample; b.Dx() != wantW || b.Dy() != wantH {
		t.Errorf("raster is %dx%d, want %dx%d (aspect kept)", b.Dx(), b.Dy(), wantW, wantH)
	}
	if got := alphaAt(t, res.Image, b.Dx()/2, b.Dy()/2); got == 0 {
		t.Error("nothing was drawn")
	}
}

func TestDecodeCurrentColor(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="currentColor"/></svg>`
	res, err := Decode([]byte(src), Options{MaxW: 20, MaxH: 20, CurrentColor: "#ff0000"})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	r, g, b, a := res.Image.At(res.Image.Bounds().Dx()/2, res.Image.Bounds().Dy()/2).RGBA()
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
	res, err := Decode([]byte(src), Options{MaxW: 20, MaxH: 20})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := alphaAt(t, res.Image, 10, 10); got == 0 {
		t.Error("nothing drawn from a document with entity declarations")
	}
}

func TestDecodeReportsDroppedText(t *testing.T) {
	withText := `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20"><text x="1" y="15">hi</text></svg>`
	res, err := Decode([]byte(withText), Options{MaxW: 40, MaxH: 40})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !res.TextDropped {
		t.Error("TextDropped = false for a document whose only content is text")
	}
	plain := `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`
	res, err = Decode([]byte(plain), Options{MaxW: 10, MaxH: 10})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if res.TextDropped {
		t.Error("TextDropped = true for a document with no text")
	}
}

func TestDecodeNoIntrinsicSize(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" width="100%" height="100%"><rect width="10" height="10"/></svg>`
	res, err := Decode([]byte(src), Options{MaxW: 60, MaxH: 60})
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
			if _, err := Decode([]byte(src), Options{MaxW: 32, MaxH: 32}); err != nil {
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
		res, err := Decode([]byte(src), Options{MaxW: 32, MaxH: 32})
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

func TestFitFillsBoxKeepingAspect(t *testing.T) {
	// Width-bound: a wide drawing in a square box.
	if w, h := fit(200, 100, 50, 50); w != 50 || h != 25 {
		t.Errorf("fit(200,100 -> 50x50) = %dx%d, want 50x25", w, h)
	}
	// Height-bound: the thumbnail case, a wide box only ten rows tall.
	if w, h := fit(100, 100, 800, 160); w != 160 || h != 160 {
		t.Errorf("fit(100,100 -> 800x160) = %dx%d, want 160x160", w, h)
	}
	// A small drawing is scaled up: it is vector art and the placement will not
	// enlarge it later.
	if w, h := fit(16, 16, 512, 512); w != 512 || h != 512 {
		t.Errorf("fit(16,16 -> 512x512) = %dx%d, want 512x512", w, h)
	}
	// An extreme aspect ratio stays inside the pixel cap.
	if w, h := fit(1, 100000, 100000, 100000); w*h > maxPixels {
		t.Errorf("fit produced %dx%d = %d pixels, over the %d cap", w, h, w*h, maxPixels)
	}
	// A degenerate document cannot produce a zero-size raster.
	if w, h := fit(0, 0, 100, 100); w < 1 || h < 1 {
		t.Errorf("fit(0,0) = %dx%d, want at least 1x1", w, h)
	}
}

// TestDecodeUniformScale is the second rasteriser bug worked around here: it
// reads scale(s) as scale(s, 0), flattening everything onto one line. The
// Ghostscript tiger is drawn through translate → matrix → scale(.1), so it came
// out as a band at the bottom of the frame.
func TestDecodeUniformScale(t *testing.T) {
	// A 100x100 box scaled by .5 should fill the top-left quarter and nothing else.
	src := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><g transform="scale(.5)"><rect width="100" height="100" fill="black"/></g></svg>`
	res, err := Decode([]byte(src), Options{MaxW: 100, MaxH: 100})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// In quarters of the raster, whatever size supersampling made it.
	b := res.Image.Bounds()
	q := func(fx, fy float64) uint32 {
		return alphaAt(t, res.Image, int(float64(b.Dx())*fx), int(float64(b.Dy())*fy))
	}
	if got := q(0.25, 0.25); got == 0 {
		t.Error("nothing inside the scaled box: the Y axis was flattened")
	}
	// Middle of the frame is outside the scaled box.
	if got := q(0.75, 0.75); got != 0 {
		t.Errorf("paint outside the scaled box (alpha %d): scale was not applied", got)
	}
	// The bottom row is where a flattened Y axis piles everything up.
	if got := q(0.25, 0.99); got != 0 {
		t.Errorf("paint on the bottom row (alpha %d): the Y axis collapsed", got)
	}
}

// TestDecodeRefusesTooMuchDrawing pins the work guard: a document asking for more
// rasterising than the budget allows is turned away before any of it is paid, so
// a pathological drawing cannot hold the preview for ten seconds. The same
// document may still be fine in a smaller box, which is why the check is on the
// shape count against the target size rather than on the file.
func TestDecodeRefusesTooMuchDrawing(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">`)
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&b, `<path d="M%d 0h1v1h-1z" fill="black"/>`, i%100)
	}
	b.WriteString(`</svg>`)
	raw := []byte(b.String())

	if _, err := Decode(raw, Options{MaxW: 2048, MaxH: 2048}); err == nil {
		t.Error("a 4000-shape drawing at 2048px was accepted; the budget did not bind")
	}
	// Small enough a box and the same document is cheap, so it must go through.
	if _, err := Decode(raw, Options{MaxW: 64, MaxH: 64}); err != nil {
		t.Errorf("the same document in a small box was refused: %v", err)
	}
}

// TestDecodeSupersamples guards the antialiasing: the raster must come back
// larger than the destination box so the placement's downscale has coverage to
// work with. Drawn at 1:1 the rasteriser turns a hairline into a blob, which is
// what a thumbnail of a detailed drawing looks like without this.
func TestDecodeSupersamples(t *testing.T) {
	res, err := Decode([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`),
		Options{MaxW: 100, MaxH: 100})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := res.Image.Bounds().Dx(); got != 100*Supersample {
		t.Errorf("raster is %dpx for a 100px box, want %dpx (%d× supersampled)", got, 100*Supersample, Supersample)
	}
}

// TestDecodeDegradesBeforeRefusing pins the order of retreat for a drawing too
// detailed to supersample: drop to the destination box (a quarter of the work)
// rather than refuse, since a refusal costs the user the picture entirely.
func TestDecodeDegradesBeforeRefusing(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">`)
	for i := 0; i < 900; i++ {
		fmt.Fprintf(&b, `<path d="M%d 0h1v1h-1z" fill="black"/>`, i%100)
	}
	b.WriteString(`</svg>`)
	raw := []byte(b.String())

	// 900 shapes at 700² supersampled is 1.76G — over budget — but 900 at 700² is
	// 441M, which is not, so it must come back at the box rather than as an error.
	res, err := Decode(raw, Options{MaxW: 700, MaxH: 700})
	if err != nil {
		t.Fatalf("degraded case was refused instead: %v", err)
	}
	if got := res.Image.Bounds().Dx(); got != 700 {
		t.Errorf("raster is %dpx, want the un-supersampled 700px", got)
	}
}

// inkPixels counts how much of a raster got painted, for comparing one drawing
// against a variant of itself.
func inkPixels(t *testing.T, img image.Image) int {
	t.Helper()
	b := img.Bounds()
	var n int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				n++
			}
		}
	}
	return n
}

// TestDecodeStrokeFollowsTransform is the fix that mattered most: a stroke inside
// a scaled group has to be scaled with it. Both documents draw the same line in
// the same place at the same device size; only the second reaches it through a
// scale, so its stroke must come out four times as wide. Unfixed, the two are
// indistinguishable.
func TestDecodeStrokeFollowsTransform(t *testing.T) {
	plain := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><line x1="0" y1="50" x2="100" y2="50" stroke="black" stroke-width="1"/></svg>`
	scaled := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><g transform="scale(4)"><line x1="0" y1="12.5" x2="25" y2="12.5" stroke="black" stroke-width="1"/></g></svg>`

	a, err := Decode([]byte(plain), Options{MaxW: 200, MaxH: 200})
	if err != nil {
		t.Fatalf("Decode plain: %v", err)
	}
	b, err := Decode([]byte(scaled), Options{MaxW: 200, MaxH: 200})
	if err != nil {
		t.Fatalf("Decode scaled: %v", err)
	}
	thin, thick := inkPixels(t, a.Image), inkPixels(t, b.Image)
	if thin == 0 {
		t.Fatal("the plain line drew nothing")
	}
	ratio := float64(thick) / float64(thin)
	if ratio < 3 || ratio > 5 {
		t.Errorf("stroke through scale(4) is %.2f× the ink, want about 4× — the transform is not reaching the stroke width", ratio)
	}
}

// TestDecodeStrokeSurvivesFlip covers the case a mean-of-the-diagonal scale gets
// wrong: a matrix that flips Y has diagonal terms that cancel, so averaging them
// yields zero and the stroke disappears. The geometric mean does not.
func TestDecodeStrokeSurvivesFlip(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><g transform="matrix(1 0 0 -1 0 100)"><line x1="10" y1="50" x2="90" y2="50" stroke="black" stroke-width="4"/></g></svg>`
	res, err := Decode([]byte(src), Options{MaxW: 200, MaxH: 200})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if n := inkPixels(t, res.Image); n == 0 {
		t.Error("a stroke under a Y flip drew nothing: its width was scaled to zero")
	}
}

// TestDecodeImplicitRepetition covers the parameter-set handling for the commands
// that carry several sets under one letter. Arcs are the pair that were broken;
// the rest are here so a future edit to the vendored parser cannot quietly take
// them with it.
func TestDecodeImplicitRepetition(t *testing.T) {
	for _, tc := range []struct{ name, d string }{
		{"lineto", "M10 10 L90 10 L90 90 L10 90 Z"},
		{"implicit lineto", "M10 10 90 10 90 90 10 90 Z"},
		{"implicit cubic", "M10 50c10-40 30-40 40 0 10 40 30 40 40 0"},
		{"implicit arc", "m50 10a40 40 0 0 0-40 40 40 40 0 0 0 40 40 40 40 0 0 0 40-40 40 40 0 0 0-40-40z"},
		{"packed arc flags", "M50 10a40 40 0 100 80 40 40 0 000-80z"},
		{"exponent", "M1e1 1e1L9e1 9e1"},
		{"abutting decimals", "M10.5.5L90.5 90.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><path d="` + tc.d + `" fill="black" stroke="black" stroke-width="2"/></svg>`
			res, err := Decode([]byte(src), Options{MaxW: 100, MaxH: 100})
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if inkPixels(t, res.Image) == 0 {
				t.Error("nothing was drawn: the parameter sets were not read")
			}
		})
	}
}

// TestDecodeClampsOutOfRangeRGB pins the narrowing in oksvg's parseColorValue:
// an rgb() component is parsed as an int and then cut down to a uint8, so an
// out-of-range one used to wrap — -1 came out full red, 300% came out 253.
func TestDecodeClampsOutOfRangeRGB(t *testing.T) {
	cases := []struct {
		fill  string
		wantR uint32
	}{
		{"rgb(-1,0,0)", 0},
		{"rgb(-20%,0,0)", 0},
		{"rgb(300,0,0)", 0xFFFF},
		{"rgb(300%,0,0)", 0xFFFF},
		{"rgb(255,0,0)", 0xFFFF},
		{"rgb(100%,0,0)", 0xFFFF},
		{"rgb(50%,0,0)", 0x7F7F},
	}
	for _, tc := range cases {
		src := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="%s"/></svg>`, tc.fill)
		res, err := Decode([]byte(src), Options{MaxW: 20, MaxH: 20})
		if err != nil {
			t.Errorf("Decode(%s): %v", tc.fill, err)
			continue
		}
		b := res.Image.Bounds()
		r, _, _, a := res.Image.At(b.Dx()/2, b.Dy()/2).RGBA()
		if a == 0 {
			t.Errorf("%s drew nothing", tc.fill)
			continue
		}
		if r != tc.wantR {
			t.Errorf("%s: red = %d, want %d", tc.fill, r, tc.wantR)
		}
	}
}
