//go:build video

package ui

import (
	_ "embed"
	"testing"
)

// stillJXL is a 48x32 JPEG XL still (naked codestream, ff 0a). 1.4KB.
//
//go:embed testdata/still.jxl
var stillJXL []byte

// animJXL is a 5-frame animated JPEG XL (48x32) — the jpegxl_anim codec, which is
// a different codec id from a still one and hence the second half of the probe.
//
//go:embed testdata/anim.jxl
var animJXL []byte

// TestDecodeJXLStill proves the whole route on a real file: sniff, probe, demux
// (jpegxl_pipe, chosen by content — our temp file has no extension), decode, scale.
func TestDecodeJXLStill(t *testing.T) {
	if !jxlDecodable() {
		t.Skip("this ffmpeg was built without libjxl")
	}
	if !looksLikeJXL(stillJXL) {
		t.Fatal("looksLikeJXL = false: the bytes would never reach libav")
	}
	if !routesToLibav(stillJXL) {
		t.Fatal("routesToLibav = false: decodeImageFrames would hand this to the Go decoders")
	}
	frames, _, err := decodeImageFrames(stillJXL, true)
	if err != nil {
		t.Fatalf("decodeImageFrames(JXL): %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1 (a still)", len(frames))
	}
	if b := frames[0].Bounds(); b.Dx() != 48 || b.Dy() != 32 {
		t.Errorf("frame bounds %v, want 48x32", b)
	}
}

// TestDecodeAnimatedJXL: JPEG XL carries animation, and it arrives under a
// separate codec id (jpegxl_anim) — which is why jxlOK requires both decoders
// rather than either.
func TestDecodeAnimatedJXL(t *testing.T) {
	if !jxlDecodable() {
		t.Skip("this ffmpeg was built without libjxl")
	}
	frames, delays, err := decodeImageFrames(animJXL, true)
	if err != nil {
		t.Fatalf("decodeImageFrames(animated JXL): %v", err)
	}
	// Every frame, not a decimated subset: the jpegxl_anim demuxer reports no
	// average frame rate and a 100Hz timebase, which used to make decimation keep
	// every 7th of 5 frames — i.e. turn every animated JXL into a still.
	if len(frames) != 5 {
		t.Fatalf("got %d frames, want all 5 — decimation thinned an image sequence", len(frames))
	}
	if len(delays) != len(frames) {
		t.Errorf("%d delays for %d frames", len(delays), len(frames))
	}
	still, _, err := decodeImageFrames(animJXL, false)
	if err != nil {
		t.Fatalf("decodeImageFrames(animated JXL, still): %v", err)
	}
	if len(still) != 1 {
		t.Errorf("the poster decode returned %d frames, want 1", len(still))
	}
}
