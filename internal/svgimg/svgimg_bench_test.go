package svgimg

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// icon is the shape most drawings posted in chat take: a small symbolic icon,
// one path, driven from currentColor.
var icon = []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><path d="m8 2a6 6 0 0 0-6 6 6 6 0 0 0 6 6 6 6 0 0 0 6-6 6 6 0 0 0-6-6zm0 1a5 5 0 0 1 5 5 5 5 0 0 1-5 5 5 5 0 0 1-5-5 5 5 0 0 1 5-5z" fill="currentColor"/></svg>`)

// BenchmarkLooks is the sniff every image decode now pays before reaching the
// stdlib decoders, so the PNG case — bytes that are *not* a drawing — is the one
// that matters: it is pure tax on the existing thumbnail pipeline.
func BenchmarkLooks(b *testing.B) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x42}, 8<<10)...)
	jpeg := append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte{0x42}, 8<<10)...)
	// Whitespace before the root is the slowest accept: it has to be trimmed.
	padded := append(bytes.Repeat([]byte(" \n\t"), 200), icon...)

	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"png_reject", png},
		{"jpeg_reject", jpeg},
		{"svg_accept", icon},
		{"svg_padded", padded},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = Looks(tc.raw)
			}
		})
	}
}

// drawing builds a document of n filled paths, each an arc pair written the way
// an optimiser emits them, under the nested transforms real exports carry. Stands
// in for a detailed illustration — the Ghostscript tiger is ~300 paths — without
// committing somebody else's artwork as a fixture.
func drawing(n int) []byte {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 200">`)
	b.WriteString(`<g transform="translate(-8.8 -48.6)"><g transform="matrix(.3528 0 0 -.3528 8.8 249)"><g transform="scale(.1)">`)
	for i := 0; i < n; i++ {
		x, y := (i*37)%5000, (i*71)%5000
		fmt.Fprintf(&b, `<path d="m%d %da60 60 0 0 0-60 60 60 60 0 0 0 60 60 60 60 0 0 0 60-60 60 60 0 0 0-60-60zm0 10a50 50 0 0 1 50 50 50 50 0 0 1-50 50 50 50 0 0 1-50-50 50 50 0 0 1 50-50z" fill="#%02x%02x80"/>`,
			x, y, i%256, (i*7)%256)
	}
	b.WriteString(`</g></g></g></svg>`)
	return []byte(b.String())
}

// BenchmarkDecode is the whole cost of turning a drawing into pixels, at both
// sizes the app asks for: the inline-thumbnail raster and the preview modal's.
func BenchmarkDecode(b *testing.B) {
	for _, tc := range []struct {
		name string
		raw  []byte
	}{{"icon", icon}, {"detailed_300paths", drawing(300)}} {
		// The two boxes the app actually asks for: an inline thumbnail (a pane
		// wide, ten rows tall) and the preview modal.
		for _, box := range []struct {
			name string
			w, h int
		}{{"thumb", 640, 160}, {"preview", 1408, 624}} {
			b.Run(tc.name+"_"+box.name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := Decode(tc.raw, Options{MaxW: box.w, MaxH: box.h, CurrentColor: "#d7d7d7"}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
