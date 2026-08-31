package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"testing"
	"time"
)

// A GIF carries no size or frame-count ceiling, and compositing one costs a
// full-resolution RGBA frame per step — 178 MB to build a 10-row thumbnail of a
// 3 MB GIF, and 128 MB held for as long as a preview of a 0.4 MB one stayed
// open. The fix was to stream: composite one frame at a time off a shared
// canvas, fit and encode it inside the callback, and keep only the small result
// (PERF_NOTES §12). These tests pin that the streamed frames are still the same
// frames, and that the paths that consume them still stream.

// disposalGIF encodes a GIF that exercises both disposal methods that mutate the
// canvas *after* a frame is handed out — the two cases a shared canvas has to get
// right, and the ones a per-frame clone made trivially correct.
func disposalGIF(t *testing.T, wPx, hPx int) []byte {
	t.Helper()
	pal := color.Palette{
		color.RGBA{0, 0, 0, 0},
		color.RGBA{200, 30, 40, 255},
		color.RGBA{20, 90, 220, 255},
		color.RGBA{40, 200, 90, 255},
	}
	paint := func(r image.Rectangle, idx uint8) *image.Paletted {
		p := image.NewPaletted(r, pal)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				p.SetColorIndex(x, y, idx)
			}
		}
		return p
	}
	g := &gif.GIF{
		Config: image.Config{Width: wPx, Height: hPx},
		Image: []*image.Paletted{
			paint(image.Rect(0, 0, wPx, hPx), 1),
			paint(image.Rect(wPx/4, hPx/4, wPx*3/4, hPx*3/4), 2),
			paint(image.Rect(0, hPx/2, wPx, hPx), 3),
			paint(image.Rect(wPx/8, hPx/8, wPx/2, hPx/2), 2),
		},
		Delay: []int{5, 7, 3, 9},
		Disposal: []byte{
			gif.DisposalNone,
			gif.DisposalPrevious, // rolls the canvas back after this frame
			gif.DisposalBackground,
			gif.DisposalPrevious, // a second one, to catch a reused restore buffer
		},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

// The streaming compositor must produce exactly the frames the batch one did —
// same count, same bounds, same pixels, same delays. Everything downstream (the
// cell box a thumbnail is sized to, the frames a preview animates) was built on
// compositeGIF's output, so a divergence here is a divergence everywhere.
func TestEachCompositeGIFFrameMatchesCompositeGIF(t *testing.T) {
	for name, raw := range map[string][]byte{
		"plain":              benchAnimGIF(64, 48, 5),
		"offset first frame": offsetFirstFrameGIF(t, 64, 48),
		"disposal methods":   disposalGIF(t, 64, 48),
	} {
		t.Run(name, func(t *testing.T) {
			g, err := gif.DecodeAll(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("DecodeAll: %v", err)
			}
			want, wantDelays, err := compositeGIF(g)
			if err != nil {
				t.Fatalf("compositeGIF: %v", err)
			}

			g2, err := gif.DecodeAll(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("DecodeAll: %v", err)
			}
			var got []image.Image
			var gotDelays []time.Duration
			err = eachCompositeGIFFrame(g2, func(fr image.Image, d time.Duration, shared bool) error {
				if !shared {
					t.Error("a streamed GIF frame must be flagged shared")
				}
				got = append(got, cloneFrame(fr)) // the contract: copy to keep
				gotDelays = append(gotDelays, d)
				return nil
			})
			if err != nil {
				t.Fatalf("eachCompositeGIFFrame: %v", err)
			}

			if len(got) != len(want) {
				t.Fatalf("frame count = %d, want %d", len(got), len(want))
			}
			for i := range want {
				w, ok := want[i].(*image.RGBA)
				if !ok {
					t.Fatalf("frame %d: compositeGIF returned %T", i, want[i])
				}
				gi := got[i].(*image.RGBA)
				if gi.Rect != w.Rect {
					t.Fatalf("frame %d bounds = %v, want %v", i, gi.Rect, w.Rect)
				}
				if !bytes.Equal(gi.Pix, w.Pix) {
					t.Fatalf("frame %d pixels differ from the batch compositor", i)
				}
				if gotDelays[i] != wantDelays[i] {
					t.Fatalf("frame %d delay = %v, want %v", i, gotDelays[i], wantDelays[i])
				}
			}
		})
	}
}

// The point of the exercise: every frame comes off one canvas. Collecting them
// instead — the shape this replaced — would hand out a distinct image each time,
// and the memory would be back.
func TestEachCompositeGIFFrameReusesOneCanvas(t *testing.T) {
	g, err := gif.DecodeAll(bytes.NewReader(benchAnimGIF(64, 48, 6)))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	var first *image.RGBA
	n := 0
	err = eachCompositeGIFFrame(g, func(fr image.Image, _ time.Duration, _ bool) error {
		rgba, ok := fr.(*image.RGBA)
		if !ok {
			t.Fatalf("frame %d is %T, want *image.RGBA", n, fr)
		}
		if n == 0 {
			first = rgba
		} else if rgba != first {
			t.Fatalf("frame %d came off a different canvas — the decode is not streaming", n)
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("eachCompositeGIFFrame: %v", err)
	}
	if n != 6 {
		t.Fatalf("streamed %d frames, want 6", n)
	}
}

// An error from the callback stops the decode where it is rather than
// compositing the rest for nothing.
func TestEachCompositeGIFFrameStopsOnError(t *testing.T) {
	g, err := gif.DecodeAll(bytes.NewReader(benchAnimGIF(32, 32, 8)))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	boom := fmt.Errorf("stop here")
	n := 0
	if err := eachCompositeGIFFrame(g, func(image.Image, time.Duration, bool) error {
		n++
		if n == 3 {
			return boom
		}
		return nil
	}); err != boom {
		t.Fatalf("err = %v, want the callback's own error", err)
	}
	if n != 3 {
		t.Fatalf("composited %d frames after the callback failed at 3", n)
	}
}

// eachFrame routes a still image (and an animation-disabled GIF) through the
// batch decoder, where the frames are the caller's to keep — shared must be
// false, or a consumer would copy for nothing.
func TestEachFrameStillIsNotShared(t *testing.T) {
	raw := benchAnimGIF(32, 32, 4)
	n := 0
	err := eachFrame(raw, false, decodeImageFrames, func(_ image.Image, _ time.Duration, shared bool) error {
		if shared {
			t.Error("a batch-decoded frame must not be flagged shared")
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("eachFrame: %v", err)
	}
	if n != 1 {
		t.Fatalf("animate=false yielded %d frames, want 1 (the still)", n)
	}
}

// buildThumbFrames must land on the same cell box the still already occupies —
// markFramesBuilt drops the whole build otherwise — and produce one transmit per
// frame, with delays parallel to them.
func TestBuildThumbFramesMatchesTheStill(t *testing.T) {
	raw := benchAnimGIF(96, 64, 5)
	m := Model{cellPxW: 10, cellPxH: 20}

	still, err := decodeFirstGIFFrame(raw)
	if err != nil {
		t.Fatalf("decodeFirstGIFFrame: %v", err)
	}
	want, err := m.encodeInlineThumb(0x4242, []image.Image{still}, 88)
	if err != nil {
		t.Fatalf("encodeInlineThumb: %v", err)
	}

	got, err := m.buildThumbFrames(thumbFramesJob{
		key: "k", id: 0x4242, box: 88, rows: want.rows, cols: want.cols,
	}, raw)
	if err != nil {
		t.Fatalf("buildThumbFrames: %v", err)
	}
	if got.rows != want.rows || got.cols != want.cols {
		t.Fatalf("placement = %dx%d, want %dx%d (markFramesBuilt would reject this)",
			got.cols, got.rows, want.cols, want.rows)
	}
	if len(got.seqs) != 5 {
		t.Fatalf("seqs = %d, want one per frame (5)", len(got.seqs))
	}
	if len(got.delays) != len(got.seqs) {
		t.Fatalf("delays = %d, seqs = %d — they must stay parallel", len(got.delays), len(got.seqs))
	}
	if got.native != "" {
		t.Error("the manual path must not build a native setup")
	}

	// The native path never re-sends frame 0 (it is the root, already on screen),
	// so it carries a setup and no per-frame sequences at all.
	nm := m
	nm.nativeAnim = true
	nat, err := nm.buildThumbFrames(thumbFramesJob{
		key: "k", id: 0x4242, box: 88, rows: want.rows, cols: want.cols,
	}, raw)
	if err != nil {
		t.Fatalf("buildThumbFrames (native): %v", err)
	}
	if nat.native == "" {
		t.Fatal("the native path must build a setup")
	}
	if len(nat.seqs) != 0 {
		t.Fatalf("native seqs = %d, want 0", len(nat.seqs))
	}
}

// A single-frame GIF has nothing to add: the still already is the whole image,
// so the build returns nothing rather than a one-frame "animation".
func TestBuildThumbFramesSkipsSingleFrame(t *testing.T) {
	m := Model{cellPxW: 10, cellPxH: 20}
	got, err := m.buildThumbFrames(thumbFramesJob{key: "k", id: 1, box: 88}, benchAnimGIF(32, 32, 1))
	if err != nil {
		t.Fatalf("buildThumbFrames: %v", err)
	}
	if len(got.seqs) != 0 || got.native != "" {
		t.Fatalf("a single-frame GIF produced %d seqs / native %q", len(got.seqs), got.native)
	}
}

// The preview keeps frame 0 and nothing else — that retention was 91 MB of the
// 128 MB an open GIF preview used to hold.
func TestBuildPreviewFramesKeepsOnlyFirstFrame(t *testing.T) {
	raw := benchAnimGIF(96, 64, 7)
	m := Model{width: 100, height: 40, cellPxW: 10, cellPxH: 20, animatePreview: true}

	built, err := m.buildPreviewFrames(raw, 0x77, true)
	if err != nil {
		t.Fatalf("buildPreviewFrames: %v", err)
	}
	if built.count != 7 {
		t.Fatalf("count = %d, want 7", built.count)
	}
	if len(built.seqs) != 7 || len(built.delays) != 7 {
		t.Fatalf("seqs = %d, delays = %d, want 7 each", len(built.seqs), len(built.delays))
	}
	if built.first == nil {
		t.Fatal("frame 0 must be kept — the placement aspect and the caption read it")
	}

	// And it must be the real frame 0, not a later one off the shared canvas.
	want, err := decodeFirstGIFFrame(raw)
	if err != nil {
		t.Fatalf("decodeFirstGIFFrame: %v", err)
	}
	got, ok := built.first.(*image.RGBA)
	if !ok {
		t.Fatalf("first is %T", built.first)
	}
	if got.Rect != want.Bounds() || !bytes.Equal(got.Pix, want.(*image.RGBA).Pix) {
		t.Error("the kept frame is not the GIF's first composited frame")
	}

	// keepFirst=false is the resize path: it wants the sequences rebuilt at the
	// new size and already has frame 0 in preview.img.
	re, err := m.buildPreviewFrames(raw, 0x77, false)
	if err != nil {
		t.Fatalf("buildPreviewFrames (reencode): %v", err)
	}
	if re.first != nil {
		t.Error("a re-encode must not keep a frame")
	}
	if len(re.seqs) != len(built.seqs) {
		t.Fatalf("re-encode seqs = %d, want %d", len(re.seqs), len(built.seqs))
	}
}

// A still image is one root transmit whether or not native animation is on —
// there is no animation to hand the terminal.
func TestBuildPreviewFramesStillIsNeverNative(t *testing.T) {
	m := Model{width: 100, height: 40, cellPxW: 10, cellPxH: 20, animatePreview: true, nativeAnim: true}
	built, err := m.buildPreviewFrames(benchAnimGIF(64, 48, 1), 0x88, true)
	if err != nil {
		t.Fatalf("buildPreviewFrames: %v", err)
	}
	if built.native || built.setup != "" {
		t.Error("a single-frame image must not become a native animation")
	}
	if len(built.seqs) != 1 {
		t.Fatalf("seqs = %d, want 1", len(built.seqs))
	}
}

// frameFitter must build its scaler and its destination once for a run of
// same-sized frames, not per frame. x/image/draw's Kernel.Scale throws away a
// kernelScaler — weight tables plus a dw×sh [4]float64 scratch — on every call,
// which a live profile of scrolling an image-heavy channel put at 355 MB in six
// minutes, the largest single allocation site in the process.
func TestFrameFitterReusesItsScaler(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 480, 480))
	var fitter frameFitter
	fitter.fit(src, 36, 10, 10, 20) // the first call builds both
	n := testing.AllocsPerRun(20, func() {
		fitter.fit(src, 36, 10, 10, 20)
	})
	if n > 2 {
		t.Errorf("fit allocates %.0f times per frame after warm-up — the scaler is being rebuilt", n)
	}
	// A different source size must still be handled, just not for free.
	other := image.NewRGBA(image.Rect(0, 0, 640, 360))
	if got := fitter.fit(other, 36, 10, 10, 20).Bounds(); got.Empty() {
		t.Error("a size change produced an empty frame")
	}
}
