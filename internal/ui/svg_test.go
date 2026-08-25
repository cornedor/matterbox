package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestIsSVGAttachment(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *model.FileInfo
		want bool
	}{
		{"nil", nil, false},
		{"by mime", &model.FileInfo{MimeType: "image/svg+xml"}, true},
		{"mime with parameters", &model.FileInfo{MimeType: "image/svg+xml; charset=utf-8"}, true},
		{"legacy mime", &model.FileInfo{MimeType: "image/svg"}, true},
		{"by extension when mime is empty", &model.FileInfo{Name: "diagram.svg", Extension: "svg"}, true},
		// Mattermost leaves both mime_type and extension unset often enough that the
		// filename has to be enough on its own.
		{"by filename alone", &model.FileInfo{Name: "logo.SVG"}, true},
		{"octet-stream with an svg name", &model.FileInfo{Name: "a.svg", MimeType: "application/octet-stream"}, true},
		{"png", &model.FileInfo{Name: "a.png", MimeType: "image/png", Extension: "png"}, false},
		{"no extension at all", &model.FileInfo{Name: "README", MimeType: ""}, false},
		{"svg in the middle of a name", &model.FileInfo{Name: "svg-notes.txt", Extension: "txt"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSVGAttachment(tc.f); got != tc.want {
				t.Errorf("isSVGAttachment = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPreviewImagesIncludesSVG pins that a drawing joins the gallery the space
// key opens, including when the server told us nothing about its type.
func TestPreviewImagesIncludesSVG(t *testing.T) {
	p := &model.Post{Id: "p1", Metadata: &model.PostMetadata{Files: []*model.FileInfo{
		{Id: "1", Name: "a.png", MimeType: "image/png"},
		{Id: "2", Name: "b.svg", MimeType: "image/svg+xml"},
		{Id: "3", Name: "c.svg", MimeType: ""},
		{Id: "4", Name: "d.pdf", MimeType: "application/pdf"},
	}}}
	got := previewImages(p, false)
	if len(got) != 3 {
		t.Fatalf("previewImages returned %d items, want 3 (png + both svg, pdf dropped)", len(got))
	}
	for _, want := range []string{"a.png", "b.svg", "c.svg"} {
		var found bool
		for _, it := range got {
			if it.name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing from the preview gallery", want)
		}
	}
}

func TestIsPreviewableImageURLSVG(t *testing.T) {
	for _, u := range []string{
		"https://example.com/logo.svg",
		"https://example.com/logo.SVG",
		"https://example.com/logo.svg?v=2",
	} {
		if !isPreviewableImageURL(u) {
			t.Errorf("isPreviewableImageURL(%q) = false, want true", u)
		}
	}
	if isPreviewableImageURL("https://example.com/page.svgz") {
		t.Error("svgz is not something we render, want false")
	}
}

func TestFilePreviewableSVG(t *testing.T) {
	var m Model
	if !m.filePreviewable(&model.FileInfo{Name: "a.svg", Extension: "svg"}) {
		t.Error("an svg attachment is not previewable, so it would get no thumbnail")
	}
}

// TestDecodeImageFramesRoutesSVG pins that the shared thumbnail/emoji decode
// path recognises a drawing rather than handing it to the image decoders.
func TestDecodeImageFramesRoutesSVG(t *testing.T) {
	raw := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="20" height="10"><rect width="20" height="10" fill="black"/></svg>`)
	frames, delays, err := decodeImageFrames(raw, true)
	if err != nil {
		t.Fatalf("decodeImageFrames: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1 (a drawing is never animated)", len(frames))
	}
	if delays != nil {
		t.Error("delays should be nil for a still drawing")
	}
	// This path has no destination box, so it renders to the emoji box.
	b := frames[0].Bounds()
	if b.Dx() != svgEmojiBox || b.Dy() != svgEmojiBox/2 {
		t.Errorf("raster is %dx%d, want %dx%d", b.Dx(), b.Dy(), svgEmojiBox, svgEmojiBox/2)
	}
}

func TestSVGCaptionReportsDocumentSize(t *testing.T) {
	// The raster is 512 across; the caption must describe the document instead.
	raw := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24"/></svg>`)
	got := svgCaption("icon.svg", raw, 1024)
	if !strings.Contains(got, "24×24") {
		t.Errorf("caption %q does not report the document size", got)
	}
	if strings.Contains(got, "text not rendered") {
		t.Errorf("caption %q warns about text the document does not have", got)
	}

	withText := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 80 40"><text x="0" y="20">hi</text></svg>`)
	got = svgCaption("diagram.svg", withText, 2048)
	if !strings.Contains(got, "text not rendered") {
		t.Errorf("caption %q does not warn that text was dropped", got)
	}
}

// TestSVGThumbBoxMatchesPlacement pins the sizing that keeps a drawing cheap:
// the raster is the pixel box the thumbnail actually occupies — as wide as the
// pane, at most inlineThumbRows tall — not a fixed oversized square.
func TestSVGThumbBoxMatchesPlacement(t *testing.T) {
	m := Model{cellPxW: 8, cellPxH: 16}
	w, h := m.svgThumbBox(60)
	if w != 60*8 {
		t.Errorf("thumb box width = %d, want the pane's %d px", w, 60*8)
	}
	if h != inlineThumbRows*16 {
		t.Errorf("thumb box height = %d, want %d px (%d rows)", h, inlineThumbRows*16, inlineThumbRows)
	}
	// Unknown cell metrics fall back to the same assumption the placement makes.
	m = Model{}
	w, h = m.svgThumbBox(60)
	if w != 60*svgFallbackCellW || h != inlineThumbRows*svgFallbackCellH {
		t.Errorf("fallback thumb box = %dx%d, want %dx%d", w, h, 60*svgFallbackCellW, inlineThumbRows*svgFallbackCellH)
	}
}

func TestSVGPreviewBoxBounds(t *testing.T) {
	m := Model{width: 100, height: 40, cellPxW: 8, cellPxH: 16}
	cols, rows := m.previewMaxBox()
	w, h := m.svgPreviewBox()
	if w != cols*8 || h != rows*16 {
		t.Errorf("preview box = %dx%d, want %dx%d from the modal's cell box", w, h, cols*8, rows*16)
	}
	// A huge terminal is capped rather than asked for.
	m = Model{width: 10000, height: 10000, cellPxW: 10, cellPxH: 20}
	w, h = m.svgPreviewBox()
	if w != svgMaxBox || h != svgMaxBox {
		t.Errorf("preview box = %dx%d, want both capped at %d", w, h, svgMaxBox)
	}
}

// TestSVGCurrentColorFollowsBackground pins the reason this exists: a symbolic
// icon drawn in currentColor must not come out invisible.
func TestSVGCurrentColorFollowsBackground(t *testing.T) {
	was := lightBackground.Load()
	defer lightBackground.Store(was)

	lightBackground.Store(false)
	dark := svgCurrentColor()
	lightBackground.Store(true)
	light := svgCurrentColor()
	if dark == light {
		t.Fatalf("currentColor is %q on both backgrounds", dark)
	}
}

// TestSVGThumbnailByteCap pins that a drawing too expensive to render unasked
// keeps its icon while still being one keypress from the preview modal.
func TestSVGThumbnailByteCap(t *testing.T) {
	small := &model.FileInfo{Id: "1", Name: "a.svg", MimeType: "image/svg+xml", Size: 4 << 10}
	big := &model.FileInfo{Id: "2", Name: "b.svg", MimeType: "image/svg+xml", Size: svgThumbMaxBytes + 1}

	if !svgThumbnailable(small) {
		t.Error("a small drawing should get a thumbnail")
	}
	if svgThumbnailable(big) {
		t.Error("an oversize drawing should not be rendered unasked")
	}

	var m Model
	if !m.drawsFileThumb(small) {
		t.Error("small drawing draws no thumbnail")
	}
	if m.drawsFileThumb(big) {
		t.Error("oversize drawing draws a thumbnail (and so gets a chevron)")
	}
	// Both stay previewable: the cap is about unasked work, not about what space opens.
	if !m.filePreviewable(big) {
		t.Error("an oversize drawing should still open in the preview modal")
	}

	p := &model.Post{Id: "p1", Metadata: &model.PostMetadata{Files: []*model.FileInfo{small, big}}}
	if got := len(previewImages(p, false)); got != 2 {
		t.Errorf("previewImages returned %d, want both drawings", got)
	}
	items := m.thumbItems(p)
	if len(items) != 1 || items[0].name != "a.svg" {
		t.Errorf("thumbItems = %v, want only the small drawing", items)
	}
}

// TestSVGThumbEncodesToKitty walks a drawing the whole way an inline thumbnail
// travels — render, fit to a cell box, build the transmit sequence and the
// placeholder — so a break anywhere along that chain shows up here rather than
// as a blank patch in a terminal.
func TestSVGThumbEncodesToKitty(t *testing.T) {
	raw := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 16"><rect width="32" height="16" fill="#3daee9"/></svg>`)
	frames, _, err := decodeSVGFrames(raw, 480, 160)
	if err != nil {
		t.Fatalf("decodeSVGFrames: %v", err)
	}

	m := Model{emojiImg: newEmojiImages("auto", true), cellPxW: 8, cellPxH: 16}
	ready, err := m.encodeInlineThumb(0, frames, 20)
	if err != nil {
		t.Fatalf("encodeInlineThumb: %v", err)
	}
	if ready.id == 0 {
		t.Error("no image id was allocated")
	}
	if ready.cols <= 0 || ready.rows <= 0 {
		t.Fatalf("placement is %dx%d cells", ready.cols, ready.rows)
	}
	// 2:1 document in 8x16 cells: twice as wide as tall in pixels means the same
	// cell count each way, give or take rounding.
	if ready.cols < ready.rows {
		t.Errorf("placement %dx%d cells does not keep the 2:1 aspect", ready.cols, ready.rows)
	}
	if len(ready.frameSeqs) != 1 || ready.frameSeqs[0] == "" {
		t.Fatalf("got %d transmit sequences, want 1 non-empty", len(ready.frameSeqs))
	}
	if ready.placeholder == "" {
		t.Error("no placeholder cells were built, so nothing would be drawn")
	}
}
