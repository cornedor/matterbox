package ui

import (
	"bytes"
	"context"
	"image"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/config"
)

// activeEmojiImages returns an emojiImages manager gated fully on (probe OK +
// truecolor), so emojiImg.active() is true — the precondition for the preview
// modal to open.
func activeEmojiImages() *emojiImages {
	e := newEmojiImages("auto", true)
	e.setProbeResult(true)
	e.setColorProfile(true)
	return e
}

func imagePost(mimes ...string) *model.Post {
	files := make([]*model.FileInfo, 0, len(mimes))
	for i, mime := range mimes {
		files = append(files, &model.FileInfo{
			Id:       "f" + string(rune('0'+i)),
			Name:     "pic.png",
			Size:     1234,
			MimeType: mime,
		})
	}
	return &model.Post{Id: "p1", Metadata: &model.PostMetadata{Files: files}}
}

func TestPreviewableMIME(t *testing.T) {
	for _, mime := range []string{"image/png", "image/jpeg", "image/jpg", "image/gif"} {
		if !previewableMIME(mime) {
			t.Errorf("previewableMIME(%q) = false, want true", mime)
		}
	}
	for _, mime := range []string{"image/webp", "image/svg+xml", "application/pdf", "text/plain", ""} {
		if previewableMIME(mime) {
			t.Errorf("previewableMIME(%q) = true, want false", mime)
		}
	}
}

func TestPreviewImagesAttachmentFilter(t *testing.T) {
	p := imagePost("image/png", "application/pdf", "image/gif")
	got := previewImages(p)
	if len(got) != 2 {
		t.Fatalf("previewImages returned %d items, want 2 (png+gif, pdf dropped)", len(got))
	}
	for _, it := range got {
		if it.file == nil {
			t.Errorf("attachment item has no file: %+v", it)
		}
	}
	if previewImages(nil) != nil {
		t.Error("previewImages(nil) should be nil")
	}
	if previewImages(&model.Post{}) != nil {
		t.Error("previewImages on an empty post should be nil")
	}
}

func TestFitImageCells(t *testing.T) {
	tests := []struct {
		name               string
		w, h, maxC, maxR   int
		cellPxW, cellPxH   int
		wantCols, wantRows int
	}{
		// --- Cell size unknown (0,0): box-filling fallback, ~1:2 cell aspect. ---
		// Square image: cols/rows = 2·w/h = 2, so the box is twice as wide as
		// tall in cells (≈ square on screen at a 1:2 cell aspect).
		{"square, col-bound", 100, 100, 40, 40, 0, 0, 40, 20},
		// Wide image is column-bound.
		{"wide", 200, 100, 40, 40, 0, 0, 40, 10},
		// Tall image is row-bound: ratio 1, cols would be 40 but rows cap at 10.
		{"tall, row-bound", 100, 200, 40, 10, 0, 0, 10, 10},
		// Degenerate dims fall back to the full box.
		{"zero dims", 0, 0, 30, 12, 0, 0, 30, 12},

		// --- Cell size known: never upscale past native, fill when larger. ---
		// Image smaller than the box stays native: 40×20px at a 10×20px cell is
		// 4×1 cells, not blown up to fill the 40×40 box.
		{"small, no upscale", 40, 20, 40, 40, 10, 20, 4, 1},
		// A large image is scaled down to fit, preserving aspect: 800×400px at a
		// 10×20px cell wants 80×20 cells but is capped to fill the 40-col box.
		{"large, scaled down", 800, 400, 40, 40, 10, 20, 40, 10},
		// Exactly box-sized: native fit, no scaling either way.
		{"exact fit", 400, 800, 40, 40, 10, 20, 40, 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cols, rows := fitImageCells(tt.w, tt.h, tt.maxC, tt.maxR, tt.cellPxW, tt.cellPxH)
			if cols != tt.wantCols || rows != tt.wantRows {
				t.Errorf("fitImageCells(%d,%d,%d,%d,%d,%d) = (%d,%d), want (%d,%d)",
					tt.w, tt.h, tt.maxC, tt.maxR, tt.cellPxW, tt.cellPxH, cols, rows, tt.wantCols, tt.wantRows)
			}
			if cols > tt.maxC || rows > tt.maxR {
				t.Errorf("result (%d,%d) exceeds box (%d,%d)", cols, rows, tt.maxC, tt.maxR)
			}
			// With a known cell size the placement must never exceed the image's
			// native pixels (the whole point: no upscaling).
			if tt.cellPxW > 0 && tt.cellPxH > 0 {
				if cols*tt.cellPxW > tt.w || rows*tt.cellPxH > tt.h {
					t.Errorf("placement %dx%d cells = %dx%d px upsizes past native %dx%d",
						cols, rows, cols*tt.cellPxW, rows*tt.cellPxH, tt.w, tt.h)
				}
			}
		})
	}
}

func TestOpenImagePreviewNoImage(t *testing.T) {
	m := Model{emojiImg: activeEmojiImages()}
	mm, cmd := m.openImagePreview(&model.Post{Id: "x"})
	got := mm.(Model)
	if got.preview.active {
		t.Error("preview should not open for a post with no image")
	}
	if cmd != nil {
		t.Error("no load command expected when there's nothing to preview")
	}
	if !strings.Contains(got.status, "no image") {
		t.Errorf("status = %q, want a 'no image' hint", got.status)
	}
}

func TestOpenImagePreviewNoGraphics(t *testing.T) {
	// emojiImg nil → Kitty graphics unavailable → modal declines, points to `o`.
	m := Model{}
	mm, cmd := m.openImagePreview(imagePost("image/png"))
	got := mm.(Model)
	if got.preview.active {
		t.Error("preview should not open without terminal graphics support")
	}
	if cmd != nil {
		t.Error("no load command expected without graphics support")
	}
	if !strings.Contains(got.status, "unavailable") || !strings.Contains(got.status, "press o") {
		t.Errorf("status = %q, want an 'unavailable … press o' hint", got.status)
	}
}

func TestEmojiImagesStatusReason(t *testing.T) {
	tests := []struct {
		name string
		set  func(e *emojiImages)
		want string // substring the reason must contain
	}{
		{"nil", nil, "disabled in this build"},
		{"off", func(e *emojiImages) { e.mode = "off" }, "off in config"},
		{"probing", func(e *emojiImages) {}, "still probing"},
		{"silent probe", func(e *emojiImages) { e.setColorProfile(true); e.setProbeResult(false) }, "didn't answer"},
		{"non-ok reply", func(e *emojiImages) {
			e.setColorProfile(true)
			e.setProbeReply("ENOTSUPPORTED")
			e.setProbeResult(false)
		}, "not with OK"},
		{"no truecolor", func(e *emojiImages) { e.setProbeReply("OK"); e.setProbeResult(true); e.setColorProfile(false) }, "not detected as truecolor"},
		{"active", func(e *emojiImages) { e.setProbeReply("OK"); e.setProbeResult(true); e.setColorProfile(true) }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var e *emojiImages
			if tc.set != nil {
				e = newEmojiImages("auto", true)
				tc.set(e)
			}
			got := e.statusReason()
			if tc.want == "" {
				if got != "" {
					t.Errorf("statusReason() = %q, want empty (active)", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("statusReason() = %q, want substring %q", got, tc.want)
			}
		})
	}
}

func TestOpenImagePreviewStartsLoad(t *testing.T) {
	m := Model{emojiImg: activeEmojiImages()}
	mm, cmd := m.openImagePreview(imagePost("image/png", "image/gif"))
	got := mm.(Model)
	if !got.preview.active || !got.preview.loading {
		t.Fatalf("expected an active, loading preview; got active=%v loading=%v",
			got.preview.active, got.preview.loading)
	}
	if len(got.preview.items) != 2 {
		t.Errorf("preview.items = %d, want 2", len(got.preview.items))
	}
	if got.previewGen == 0 {
		t.Error("previewGen should be bumped on open")
	}
	if cmd == nil {
		t.Error("expected a background load command")
	}
}

func TestHandlePreviewLoadedTransmits(t *testing.T) {
	m := Model{
		width: 100, height: 40,
		emojiImg: activeEmojiImages(),
	}
	m.preview = previewState{active: true, items: previewImages(imagePost("image/png")), loading: true}
	m.previewGen = 1

	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	mm, cmd := m.handlePreviewLoaded(previewImageLoadedMsg{gen: 1, frames: []image.Image{img}, caption: "pic.png"})
	got := mm.(Model)
	if got.preview.loading {
		t.Error("loading should clear once the decode lands")
	}
	if got.preview.img == nil {
		t.Error("decoded image should be stored")
	}
	if got.preview.id == 0 {
		t.Error("an image id should be allocated")
	}
	if got.preview.rows <= 0 || got.preview.cols <= 0 {
		t.Errorf("placement not sized: rows=%d cols=%d", got.preview.rows, got.preview.cols)
	}
	if cmd == nil {
		t.Error("expected a tea.Raw transmit command")
	}
}

func TestHandlePreviewLoadedStaleDropped(t *testing.T) {
	m := Model{width: 100, height: 40, emojiImg: activeEmojiImages()}
	m.preview = previewState{active: true, loading: true}
	m.previewGen = 5 // live generation

	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	mm, cmd := m.handlePreviewLoaded(previewImageLoadedMsg{gen: 2, frames: []image.Image{img}}) // stale
	got := mm.(Model)
	if got.preview.img != nil || cmd != nil {
		t.Error("a stale (gen-mismatched) load result must be ignored")
	}
}

func twoFrames() ([]image.Image, []time.Duration) {
	frames := []image.Image{
		image.NewRGBA(image.Rect(0, 0, 8, 8)),
		image.NewRGBA(image.Rect(0, 0, 8, 8)),
		image.NewRGBA(image.Rect(0, 0, 8, 8)),
	}
	const d = 50 * time.Millisecond
	return frames, []time.Duration{d, d, d}
}

// A multi-frame decode anchors the animation clock and arms the tick; a still
// image (single frame) does not.
func TestHandlePreviewLoadedAnimatedArmsTick(t *testing.T) {
	m := Model{width: 100, height: 40, emojiImg: activeEmojiImages(), animatePreview: true}
	m.preview = previewState{active: true, items: previewImages(imagePost("image/gif")), loading: true}
	m.previewGen = 1

	frames, delays := twoFrames()
	mm, cmd := m.handlePreviewLoaded(previewImageLoadedMsg{gen: 1, frames: frames, delays: delays, caption: "a.gif"})
	got := mm.(Model)
	if len(got.preview.frames) != 3 {
		t.Fatalf("frames stored = %d, want 3", len(got.preview.frames))
	}
	if got.preview.frameStart.IsZero() {
		t.Error("animated preview should anchor frameStart")
	}
	if cmd == nil {
		t.Fatal("expected a transmit + tick command")
	}

	// Still image: one frame, no tick armed (frameStart stays zero).
	still := Model{width: 100, height: 40, emojiImg: activeEmojiImages(), animatePreview: true}
	still.preview = previewState{active: true, items: previewImages(imagePost("image/png")), loading: true}
	still.previewGen = 1
	smm, _ := still.handlePreviewLoaded(previewImageLoadedMsg{gen: 1, frames: []image.Image{frames[0]}})
	if !smm.(Model).preview.frameStart.IsZero() {
		t.Error("a still preview should not anchor an animation clock")
	}
}

// handlePreviewTick advances the frame (catching up overdue frames) and
// reschedules; a tick from a cycled/closed preview or a still image is dropped.
func TestHandlePreviewTick(t *testing.T) {
	frames, delays := twoFrames()
	m := Model{width: 100, height: 40, emojiImg: activeEmojiImages(), animatePreview: true}
	m.preview = previewState{
		active: true, id: 9, rows: 4, cols: 8,
		frames: frames, delays: delays,
		frameStart: time.Now().Add(-2 * delays[0]), // ~2 frames overdue
	}
	m.previewGen = 1

	mm, cmd := m.handlePreviewTick(previewTickMsg{gen: 1})
	got := mm.(Model)
	if got.preview.frameIdx == 0 {
		t.Errorf("frameIdx did not advance past 0 on an overdue tick")
	}
	if cmd == nil {
		t.Fatal("expected a transmit + reschedule command")
	}

	// A stale-generation tick is a no-op.
	if _, c := got.handlePreviewTick(previewTickMsg{gen: 999}); c != nil {
		t.Error("stale (gen-mismatched) preview tick should be dropped")
	}

	// A still image (one frame) never ticks.
	still := Model{emojiImg: activeEmojiImages()}
	still.preview = previewState{active: true, id: 1, frames: frames[:1], frameStart: time.Now()}
	still.previewGen = 1
	if _, c := still.handlePreviewTick(previewTickMsg{gen: 1}); c != nil {
		t.Error("a still-image preview should not schedule animation ticks")
	}
}

func TestHandlePreviewKeyClose(t *testing.T) {
	for _, key := range []string{"space", "esc", "q"} {
		m := Model{keys: newKeyMap("ctrl"), emojiImg: activeEmojiImages()}
		m.preview = previewState{active: true, items: previewImages(imagePost("image/png")), id: 7}
		m.previewGen = 1
		mm, _ := m.handlePreviewKey(prevKey(key))
		if mm.(Model).preview.active {
			t.Errorf("%q should close the preview modal", key)
		}
	}
}

func TestHandlePreviewKeyCycle(t *testing.T) {
	m := Model{keys: newKeyMap("ctrl"), emojiImg: activeEmojiImages()}
	m.preview = previewState{active: true, items: previewImages(imagePost("image/png", "image/gif")), idx: 0}
	m.previewGen = 1

	mm, cmd := m.handlePreviewKey(prevKey("right"))
	got := mm.(Model)
	if got.preview.idx != 1 {
		t.Errorf("right should advance idx to 1, got %d", got.preview.idx)
	}
	if !got.preview.loading || cmd == nil {
		t.Error("cycling should reload the new image")
	}
	// Wrap-around: left from idx 1 → 0, and again → last.
	mm2, _ := got.handlePreviewKey(prevKey("left"))
	if mm2.(Model).preview.idx != 0 {
		t.Errorf("left should move idx to 0, got %d", mm2.(Model).preview.idx)
	}
}

func TestCyclePreviewSingleImageNoop(t *testing.T) {
	m := Model{emojiImg: activeEmojiImages()}
	m.preview = previewState{active: true, items: previewImages(imagePost("image/png")), idx: 0}
	mm, cmd := m.cyclePreview(1)
	if mm.(Model).preview.idx != 0 || cmd != nil {
		t.Error("cycling a single-image preview should be a no-op")
	}
}

// TestPreviewKeyConfigurable proves the preview trigger goes through the action
// registry: a `preview_image` override rebinds it everywhere — the keymap field
// and the modal's own toggle-close both honor the new key.
func TestPreviewKeyConfigurable(t *testing.T) {
	km, err := applyKeyOverrides(newKeyMap("ctrl"), map[string]config.StringOrList{
		"preview_image": {"p"},
	})
	if err != nil {
		t.Fatalf("applyKeyOverrides: %v", err)
	}
	if got := km.Preview.Keys(); len(got) != 1 || got[0] != "p" {
		t.Fatalf("preview_image override not applied: Preview.Keys() = %v", got)
	}
	m := Model{keys: km, emojiImg: activeEmojiImages()}
	m.preview = previewState{active: true, items: previewImages(imagePost("image/png")), id: 3}
	m.previewGen = 1
	if mm, _ := m.handlePreviewKey(prevKey("p")); mm.(Model).preview.active {
		t.Error("the rebound preview key should close the modal")
	}
}

func TestIsPreviewableImageURL(t *testing.T) {
	yes := []string{
		// A GIF-picker URL: extension on the path, cache keys in the query.
		"https://media2.giphy.com/media/GRk3GLfzduq1NtfGt5/200.gif?cid=2475d0be&ep=v1_gifs_search&rid=200.gif&ct=g",
		"https://example.com/cat.PNG", // case-insensitive
		"http://host/a/b/photo.jpeg",
		"https://host/x.jpg",
	}
	for _, u := range yes {
		if !isPreviewableImageURL(u) {
			t.Errorf("isPreviewableImageURL(%q) = false, want true", u)
		}
	}
	no := []string{
		"https://example.com/page",       // no extension
		"https://example.com/video.mp4",  // not an image
		"https://example.com/image.webp", // stdlib can't decode
		"",
	}
	for _, u := range no {
		if isPreviewableImageURL(u) {
			t.Errorf("isPreviewableImageURL(%q) = true, want false", u)
		}
	}
}

// previewImages pulls a GIF-picker image link out of the message body (the
// exact ![alt](url) form Mattermost's picker posts) as a URL item, with the alt
// text as its name.
func TestPreviewImagesFindsBodyURL(t *testing.T) {
	p := &model.Post{
		Id:      "p1",
		Message: "haha ![Cat Fun GIF by Black Roses Playing Cards](https://media2.giphy.com/media/GRk3GLfzduq1NtfGt5/200.gif?cid=2475d0be&rid=200.gif&ct=g) lol",
	}
	got := previewImages(p)
	if len(got) != 1 {
		t.Fatalf("previewImages = %d items, want 1", len(got))
	}
	if got[0].file != nil || !strings.Contains(got[0].url, "200.gif") {
		t.Errorf("item = %+v, want a giphy URL item", got[0])
	}
	if got[0].name != "Cat Fun GIF by Black Roses Playing Cards" {
		t.Errorf("name = %q, want the alt text", got[0].name)
	}

	// A non-image link in the body is not previewable.
	if n := len(previewImages(&model.Post{Id: "p2", Message: "see https://example.com/page"})); n != 0 {
		t.Errorf("previewImages on a non-image link = %d items, want 0", n)
	}
}

// readOrDownloadURL fetches an external image, caches it to disk, and serves the
// cached copy on the next read; an HTTP error surfaces.
func TestReadOrDownloadURL(t *testing.T) {
	want := noisyPNG(t, 8, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(want)
	}))
	defer srv.Close()

	m := Model{ctx: context.Background()}
	cachePath := filepath.Join(t.TempDir(), "img")

	got, err := m.readOrDownloadURL(cachePath, srv.URL+"/x.png")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("downloaded bytes don't match the served image")
	}
	// Second read is served from the cache: a bogus URL is never contacted.
	cached, err := m.readOrDownloadURL(cachePath, "http://127.0.0.1:0/never")
	if err != nil {
		t.Fatalf("cached read: %v", err)
	}
	if !bytes.Equal(cached, want) {
		t.Error("second read did not use the on-disk cache")
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer errSrv.Close()
	if _, err := m.readOrDownloadURL(filepath.Join(t.TempDir(), "y"), errSrv.URL+"/missing.png"); err == nil {
		t.Error("expected an error on HTTP 404")
	}
}

// prevKey builds a KeyPressMsg whose String() matches the given key string for
// the named keys used by the preview modal (space/esc/q/left/right). Special
// keys set only their Code constant (String() derives the name); single runes
// set Text so String() round-trips — mirroring keyStr in phase1_test.go.
func prevKey(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeySpace})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	case "left":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft})
	case "right":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyRight})
	default: // single rune like "q"
		r := []rune(s)
		return tea.KeyPressMsg(tea.Key{Code: r[0], Text: s})
	}
}
