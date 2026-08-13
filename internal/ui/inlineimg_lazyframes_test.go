package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"os"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// A GIF thumbnail is built in two halves: its first frame when the image is fetched,
// the rest only if it ever actually appears on screen (buildInlineThumb →
// buildVisibleThumbFrames). Encoding a frame costs ~10ms, and the fetch margin
// builds several screens' worth of images to display one screenful, so the frames of
// a GIF you merely scroll past are pure waste — a 90-frame GIF is ~700ms of it.
//
// The scheme rests entirely on the still being *frame 0 of the full decode*, so that
// completing it later cannot move the placement by a cell. These tests pin that.

// seedURLCacheGIF writes an n-frame GIF into the disk cache the thumbnail build
// reads body-image URLs from, so buildInlineThumb runs its real decode → downscale →
// PNG-encode path with no server and no network.
func seedURLCacheGIF(t *testing.T, url string, wPx, hPx, n int) []byte {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // before cachedURLPath reads it
	raw := benchAnimGIF(wPx, hPx, n)
	path, err := cachedURLPath(url)
	if err != nil {
		t.Fatalf("cachedURLPath: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("seed url cache: %v", err)
	}
	return raw
}

// offsetFirstFrameGIF encodes a GIF whose first frame covers only part of the
// logical screen — legal, and the case where "the first frame" and "the first
// composited frame" are not the same image at all.
func offsetFirstFrameGIF(t *testing.T, wPx, hPx int) []byte {
	t.Helper()
	pal := color.Palette{color.RGBA{0, 0, 0, 0}, color.RGBA{200, 30, 40, 255}, color.RGBA{20, 90, 220, 255}}
	sub := image.NewPaletted(image.Rect(wPx/8, hPx/6, wPx/2, hPx/2), pal)
	for y := sub.Rect.Min.Y; y < sub.Rect.Max.Y; y++ {
		for x := sub.Rect.Min.X; x < sub.Rect.Max.X; x++ {
			sub.SetColorIndex(x, y, 1)
		}
	}
	full := image.NewPaletted(image.Rect(0, 0, wPx, hPx), pal)
	for y := 0; y < hPx; y++ {
		for x := 0; x < wPx; x++ {
			full.SetColorIndex(x, y, 2)
		}
	}
	g := &gif.GIF{
		Config:   image.Config{Width: wPx, Height: hPx},
		Image:    []*image.Paletted{sub, full},
		Delay:    []int{8, 8},
		Disposal: []byte{gif.DisposalNone, gif.DisposalNone},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

// TestFirstGIFFrameMatchesComposite is the load-bearing identity: decodeFirstGIFFrame
// must produce exactly what a full gif.DecodeAll + compositeGIF produces as its
// frames[0] — same bounds, same pixels. The thumbnail's cell box is derived from
// those bounds, so any divergence would size the still differently from the frames
// that complete it later, and the post would silently change height under the reader.
//
// The offset-first-frame case is where a naive gif.Decode (which hands back the raw
// first sub-rectangle, not the logical screen) would get it wrong.
func TestFirstGIFFrameMatchesComposite(t *testing.T) {
	cases := map[string][]byte{
		"full canvas":        benchAnimGIF(64, 48, 5),
		"offset first frame": offsetFirstFrameGIF(t, 64, 48),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			g, err := gif.DecodeAll(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("DecodeAll: %v", err)
			}
			frames, _, err := compositeGIF(g)
			if err != nil {
				t.Fatalf("compositeGIF: %v", err)
			}
			first, err := decodeFirstGIFFrame(raw)
			if err != nil {
				t.Fatalf("decodeFirstGIFFrame: %v", err)
			}
			want := frames[0]
			if first.Bounds() != want.Bounds() {
				t.Fatalf("frame 0 bounds %v, full decode's frame 0 is %v: the thumbnail would be "+
					"sized from different bounds than the frames completing it, and the post would "+
					"change height when they arrived", first.Bounds(), want.Bounds())
			}
			for y := want.Bounds().Min.Y; y < want.Bounds().Max.Y; y++ {
				for x := want.Bounds().Min.X; x < want.Bounds().Max.X; x++ {
					if first.At(x, y) != want.At(x, y) {
						t.Fatalf("pixel (%d,%d): %v, full decode has %v", x, y, first.At(x, y), want.At(x, y))
					}
				}
			}
		})
	}
}

// TestGIFThumbBuildsStillFirst: fetching an animated GIF must encode one frame, not
// ninety — and that one frame must be byte-for-byte the transmit the full build would
// have produced for frame 0, at the same placement.
func TestGIFThumbBuildsStillFirst(t *testing.T) {
	m := thumbModel()
	m.animateInline = true
	raw := seedURLCacheGIF(t, giphyURL, 480, 270, 12)
	const box = 78

	still, err := m.buildInlineThumb(previewItem{url: giphyURL, name: "200.gif"}, box)
	if err != nil {
		t.Fatalf("buildInlineThumb: %v", err)
	}
	if len(still.frameSeqs) != 1 {
		t.Errorf("a 12-frame GIF encoded %d frames at fetch time: only the first is needed until "+
			"the thumbnail is actually on screen", len(still.frameSeqs))
	}
	if !still.deferredFrames {
		t.Error("the GIF's remaining frames were not recorded as owed, so it would never animate")
	}

	// What the full build would have made, under the same id.
	frames, _, err := decodeImageFrames(raw, true)
	if err != nil {
		t.Fatalf("decodeImageFrames: %v", err)
	}
	full, err := m.encodeInlineThumb(still.id, frames, box)
	if err != nil {
		t.Fatalf("encodeInlineThumb: %v", err)
	}
	if full.rows != still.rows || full.cols != still.cols {
		t.Fatalf("the still is %d×%d cells but its frames want %d×%d: completing the thumbnail "+
			"would reflow the transcript", still.rows, still.cols, full.rows, full.cols)
	}
	if full.placeholder != still.placeholder {
		t.Error("the placeholder block changes when the frames arrive: the post's cached lines would be stale")
	}
	if full.frameSeqs[0] != still.frameSeqs[0] {
		t.Error("the still is not frame 0 of the animation: the GIF would visibly jump when it starts playing")
	}
}

// TestGIFFramesBuiltOnlyWhenOnScreen drives the whole lazy path against a real GIF:
// an off-screen GIF stays a still forever, and coming on screen — and only that —
// buys its frames. Installing them must change nothing that is drawn.
func TestGIFFramesBuiltOnlyWhenOnScreen(t *testing.T) {
	const nFrames = 12
	m := thumbModel()
	m.animateInline = true
	onTeamTab(m) // the transcript has to be the frame for anything in it to animate
	seedURLCacheGIF(t, giphyURL, 480, 270, nFrames)

	m.posts = []*model.Post{giphyPost()}
	m.msgsView.SetWidth(80)
	m.msgsView.SetHeight(20)
	m.msgRowStarts = []int{100, 112} // fetched (the margin reaches it) but below the fold

	it := previewItem{url: giphyURL, name: "200.gif"}
	still, err := m.buildInlineThumb(it, inlineThumbBox(80))
	if err != nil {
		t.Fatalf("buildInlineThumb: %v", err)
	}
	m.inlineImg.markReady(giphyURL, still)
	before := m.inlineThumbLines(it, 80)

	m.flushInlineTransmits() // this is what refreshes "on screen"
	if jobs := m.inlineImg.takeDeferredFrames(); len(jobs) != 0 {
		t.Fatalf("an off-screen GIF encoded its %d frames anyway — the entire point is not to pay "+
			"~10ms/frame for images nobody is looking at", len(jobs))
	}
	if cmd := m.buildVisibleThumbFrames(); cmd != nil {
		t.Fatal("no GIF is on screen, so no frame build should be scheduled")
	}

	// Scroll it into view.
	m.msgRowStarts = []int{0, 12}
	m.flushInlineTransmits()
	cmd := m.buildVisibleThumbFrames()
	if cmd == nil {
		t.Fatal("an on-screen GIF must get its frames built, or it would never animate")
	}
	msg, ok := cmd().(inlineThumbFramesMsg)
	if !ok {
		t.Fatalf("frame build returned %T, want inlineThumbFramesMsg", msg)
	}
	next, _ := m.handleInlineThumbFrames(msg)
	m = &next

	ent := m.inlineImg.entries[giphyURL]
	if len(ent.frameSeqs) != nFrames {
		t.Fatalf("the on-screen GIF has %d frames, want %d", len(ent.frameSeqs), nFrames)
	}
	if len(ent.delays) != nFrames {
		t.Fatalf("the GIF has %d frames but %d delays: advanceFrame indexes delays by frameIdx", len(ent.frameSeqs), len(ent.delays))
	}
	if ent.id != still.id || ent.rows != still.rows || ent.cols != still.cols {
		t.Errorf("completing the thumbnail moved it: id/rows/cols went %d/%d×%d → %d/%d×%d",
			still.id, still.rows, still.cols, ent.id, ent.rows, ent.cols)
	}
	if ent.frameIdx != 0 {
		t.Errorf("frameIdx is %d: the frame already transmitted (and on screen) is frame 0", ent.frameIdx)
	}
	if after := m.inlineThumbLines(it, 80); !equalLines(before, after) {
		t.Error("the thumbnail's rendered lines changed when its frames arrived: the transcript would " +
			"reflow under the reader, which is exactly what building the still first is meant to avoid")
	}
	if got, want := m.inlineImg.builtBytes, ent.builtSize(); got != want {
		t.Errorf("builtBytes is %d but the entry holds %d: the memory budget (maxInlineBuiltBytes) has drifted", got, want)
	}

	// It animates now, and it is not rebuilt again.
	if !m.refreshAnimVisibility() || !m.inlineImg.hasVisibleAnimated() {
		t.Error("the completed GIF should arm the animation loop")
	}
	if jobs := m.inlineImg.takeDeferredFrames(); len(jobs) != 0 {
		t.Errorf("the GIF is queued for a second frame build: %v", jobs)
	}
}

// TestStaleGIFFramesDropped: the frames come back on a background goroutine, and by
// then the thumbnail they were built for may be gone — a narrower pane re-fits it
// under a fresh id. Installing them anyway would transmit an image under an id that
// now belongs to a different placement.
func TestStaleGIFFramesDropped(t *testing.T) {
	ii := newInlineImages("auto")
	deferredGIF := func(id uint32, rows, cols, box int) readyInlineImg {
		return readyInlineImg{
			id: id, rows: rows, cols: cols, box: box,
			placeholder:    kittyPlaceholder(id, rows, cols),
			frameSeqs:      []string{"<still>"},
			deferredFrames: true,
			item:           previewItem{url: giphyURL},
		}
	}
	ii.markReady(giphyURL, deferredGIF(7, 10, 36, 88))
	ii.entries[giphyURL].onScreen = true

	// The pane narrowed: sight() re-fits, and the rebuild lands under a new id.
	ii.markReady(giphyURL, deferredGIF(9, 6, 20, 40))

	frames := []string{"<f0>", "<f1>"}
	delays := []time.Duration{80 * time.Millisecond, 80 * time.Millisecond}
	if ii.markFramesBuilt(builtThumbFrames{key: giphyURL, id: 7, rows: 10, cols: 36, seqs: frames, delays: delays}) {
		t.Error("frames built for a superseded id were installed: they would paint the old placement's image")
	}
	if got := len(ii.entries[giphyURL].frameSeqs); got != 1 {
		t.Errorf("the re-fitted thumbnail now has %d frames, want the 1 it was built with", got)
	}
	if !ii.markFramesBuilt(builtThumbFrames{key: giphyURL, id: 9, rows: 6, cols: 20, seqs: frames, delays: delays}) {
		t.Fatal("frames built for the current id should install")
	}
	if got := len(ii.entries[giphyURL].frameSeqs); got != len(frames) {
		t.Errorf("the current thumbnail has %d frames, want %d", got, len(frames))
	}
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
