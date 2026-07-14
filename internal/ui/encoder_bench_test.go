package ui

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"testing"
)

// Why kittyPNG is a pooled BestSpeed encoder, in numbers. Every image matterbox
// shows — custom emoji, inline thumbnails, the preview modal — is PNG-encoded and
// base64'd into a Kitty APC, and a live pprof of scrolling an image-heavy channel
// put ~70% of all CPU in that build. These are the alternatives that were measured
// before settling on one. Run: make bench BENCH=Enc
//
//	EncPNGDefault          15.2 ms   140 KB   856 KB/op, 28 allocs   ← stdlib png.Encode
//	EncPNGBestSpeed        13.4 ms   140 KB   1.2 MB/op, 29 allocs
//	EncPNGDefaultPooled     7.7 ms   140 KB   1.4 KB/op,  0 allocs   ← the pool is the win
//	EncPNGBestSpeedPooled   5.6 ms   140 KB   1.0 KB/op,  0 allocs   ← chosen
//	EncZlibRaw              3.6 ms   224 KB                          ← no filter search: 60% more bytes
//
// The headline: stdlib png.Encode allocates a fresh zlib writer on *every call* —
// ~860KB of garbage per frame, which costs more than the compression itself. The
// compression level barely moves the output of a thumbnail-sized frame, so BestSpeed
// is free speed on top.
//
// EncZlibRaw is the bound on what removing png's filter search (which is ~75% of the
// remaining encode) could buy: not enough to justify a hand-rolled PNG encoder or a
// raw-RGBA wire format, at 60% more bytes down a tty that re-sends GIF frames live.

// thumbFrame is a thumbnail-sized frame with photo-like content: smooth regions
// plus noise, so neither trivially compressible nor pathological.
func thumbFrame() *image.RGBA {
	const w, h = 360, 200
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			n := func(v int) uint8 {
				v += rnd.Intn(24) - 12
				if v < 0 {
					v = 0
				}
				if v > 255 {
					v = 255
				}
				return uint8(v)
			}
			img.Set(x, y, color.RGBA{n(x * 255 / w), n(y * 255 / h), n(128), 255})
		}
	}
	return img
}

func benchEncode(b *testing.B, enc *png.Encoder) {
	img := thumbFrame()
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := enc.Encode(&buf, img); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(buf.Len()), "B/frame")
}

func BenchmarkEncPNGDefault(b *testing.B) {
	benchEncode(b, &png.Encoder{CompressionLevel: png.DefaultCompression})
}
func BenchmarkEncPNGDefaultPooled(b *testing.B) {
	benchEncode(b, &png.Encoder{CompressionLevel: png.DefaultCompression, BufferPool: &pngBufferPool{}})
}
func BenchmarkEncPNGBestSpeedPooled(b *testing.B) {
	benchEncode(b, &kittyPNG) // the production encoder
}

// BenchmarkEncZlibRaw is the "no filter search at all" bound: zlib straight over the
// RGBA pixel bytes, which is what a filter-None PNG (or kitty's f=32 + o=z) would
// cost. The size column is what it costs.
func BenchmarkEncZlibRaw(b *testing.B) {
	img := thumbFrame()
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		zw, _ := zlib.NewWriterLevel(&buf, flate.BestSpeed)
		_, _ = zw.Write(img.Pix)
		_ = zw.Close()
	}
	b.StopTimer()
	b.ReportMetric(float64(buf.Len()), "B/frame")
}

// BenchmarkEncKittyTransmit is the whole real path: encode → base64 → 4KB APC
// chunks. This is the number that shows up in a profile as kittyTransmitImage, once
// per frame of every image we display.
func BenchmarkEncKittyTransmit(b *testing.B) {
	img := thumbFrame()
	b.ReportAllocs()
	b.ResetTimer()
	var n int
	for i := 0; i < b.N; i++ {
		seq, err := kittyTransmitImage(1, img, 10, 36)
		if err != nil {
			b.Fatal(err)
		}
		n = len(seq)
	}
	b.StopTimer()
	b.ReportMetric(float64(n), "B/apc")
}
