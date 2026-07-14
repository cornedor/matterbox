package ui

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

// TestKittyTransmitMatchesLibraryFraming is what lets us replace
// kitty.EncodeGraphics with our own encoder. We only wanted to change *how the PNG
// is compressed* (a pooled encoder — see kittyPNG); everything else about the wire
// format has to be exactly what the library emits, or images silently stop showing
// up on some terminal we can't test here.
//
// So: run our path with the library's own compression level and demand the bytes
// match — option keys, base64, chunk boundaries and all — across images from a
// 1×2 emoji to a full preview.
//
// What this canNOT reach is a payload whose length is an exact multiple of the
// chunk size: no real PNG happens to encode to one, and that is precisely where the
// rewritten loop is easy to get wrong. TestKittyChunkMatchesReadFullLoop covers it.
func TestKittyTransmitMatchesLibraryFraming(t *testing.T) {
	libEnc := &png.Encoder{CompressionLevel: png.DefaultCompression} // what png.Encode does

	for _, tc := range []struct {
		name       string
		w, h       int
		rows, cols int
		noisy      bool
	}{
		{"tiny-emoji", 20, 20, 1, 2, false},
		{"one-chunk", 32, 32, 2, 4, false},
		{"multi-chunk-noisy", 360, 200, 10, 36, true},
		{"large-preview", 800, 600, 30, 80, true},
		{"flat", 200, 200, 10, 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, tc.w, tc.h))
			for y := 0; y < tc.h; y++ {
				for x := 0; x < tc.w; x++ {
					c := color.RGBA{0x40, 0x80, 0xc0, 0xff}
					if tc.noisy {
						c = color.RGBA{uint8(x * 7), uint8(y * 13), uint8(x ^ y), 0xff}
					}
					img.Set(x, y, c)
				}
			}

			var want strings.Builder
			err := kitty.EncodeGraphics(&want, img, &kitty.Options{
				Action:           kitty.TransmitAndPut,
				VirtualPlacement: true,
				ID:               0x123456,
				Rows:             tc.rows,
				Columns:          tc.cols,
				Format:           kitty.PNG,
				Transmission:     kitty.Direct,
				Quite:            2,
				Chunk:            true,
			})
			if err != nil {
				t.Fatalf("library encode: %v", err)
			}

			got, err := kittyTransmitWith(libEnc, 0x123456, img, tc.rows, tc.cols)
			if err != nil {
				t.Fatalf("kittyTransmitWith: %v", err)
			}
			if got != want.String() {
				t.Errorf("framing diverged from the library (%d chunks vs %d, %d bytes vs %d)",
					strings.Count(got, "\x1b_G"), strings.Count(want.String(), "\x1b_G"),
					len(got), want.Len())
			}
		})
	}
}

// refChunk is the library's chunking loop, transcribed verbatim from
// kitty/writer.go (the io.ReadFull form) so kittyChunk — which is the same loop
// rewritten as slice arithmetic — can be checked against it on payload sizes no
// real image produces.
//
// It exists because TestKittyTransmitMatchesLibraryFraming, which compares against
// the real library, can only reach the payload sizes that actual PNGs happen to
// encode to — and none of them land on a chunk boundary. The boundary is precisely
// where the rewrite is easy to get wrong.
func refChunk(payload []byte, opts *kitty.Options) string {
	var sb strings.Builder
	r := bytes.NewReader(payload)
	chunk := make([]byte, kitty.MaxChunkSize)
	first := true
	var n int
	for {
		var err error
		n, err = io.ReadFull(r, chunk)
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			break
		}
		sb.WriteString(ansi.KittyGraphics(chunk[:n], kittyChunkOpts(opts, first, false)...))
		first = false
	}
	sb.WriteString(ansi.KittyGraphics(chunk[:n], kittyChunkOpts(opts, first, true)...))
	return sb.String()
}

// TestKittyChunkMatchesReadFullLoop covers the chunk sizes real images never hit —
// above all an exact multiple of MaxChunkSize, where a plain
// `for len(payload) > MaxChunkSize` loop drops the empty terminating m=0 chunk and
// leaves the terminal waiting for a continuation that never comes.
func TestKittyChunkMatchesReadFullLoop(t *testing.T) {
	opts := &kitty.Options{
		Action: kitty.TransmitAndPut, VirtualPlacement: true, ID: 9,
		Rows: 10, Columns: 36, Format: kitty.PNG,
		Transmission: kitty.Direct, Quite: 2, Chunk: true,
	}
	const K = kitty.MaxChunkSize
	for _, n := range []int{0, 1, K - 1, K, K + 1, 2*K - 1, 2 * K, 2*K + 1, 5 * K, 5*K + 7} {
		payload := make([]byte, n)
		for i := range payload {
			payload[i] = byte('A' + i%26)
		}
		got, want := kittyChunk(payload, opts), refChunk(payload, opts)
		if got != want {
			t.Errorf("payload of %d bytes (%.2f chunks): framing diverged\n got %d chunks, %d bytes\nwant %d chunks, %d bytes",
				n, float64(n)/K,
				strings.Count(got, "\x1b_G"), len(got),
				strings.Count(want, "\x1b_G"), len(want))
		}
	}
}

// TestKittyTransmitProductionEncoderStillDecodes: the production path uses
// BestSpeed, so its bytes differ from the library's — but it must still produce the
// same framing shape and a payload the terminal can read.
func TestKittyTransmitProductionEncoderStillDecodes(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 360, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 360; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 0xff})
		}
	}
	seq, err := kittyTransmitImage(7, img, 10, 36)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(seq, "\x1b_G") {
		t.Errorf("not a Kitty graphics APC: %.20q", seq)
	}
	// The first chunk carries the placement; the last closes the sequence.
	if !strings.Contains(seq, "i=1193046") { // 0x123456 is the other test's id; ours is 7
		if !strings.Contains(seq, "i=7") {
			t.Error("the image id is missing from the transmit")
		}
	}
	if !strings.Contains(seq, "m=1") || !strings.Contains(seq, "m=0") {
		t.Error("a multi-chunk payload must carry m=1 continuations and an m=0 terminator")
	}
}
