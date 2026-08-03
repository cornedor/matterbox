package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// readyThumb installs a fake ready thumbnail for fileID at rows×cols, as if it
// had been fetched and transmitted, so the render path can be tested without a
// server or a terminal.
func readyThumb(m *Model, fileID string, rows, cols, box int) uint32 {
	const id = 0x123456
	m.inlineImg.markReady(fileID, readyInlineImg{
		id:          id,
		rows:        rows,
		cols:        cols,
		box:         box,
		placeholder: kittyPlaceholder(id, rows, cols),
		frameSeqs:   []string{"<transmit>"},
	})
	return id
}

// thumbModel is a Model with thumbnails switched on and the terminal probe
// already satisfied, so inlineImagesActive() is true.
func thumbModel() *Model {
	m := &Model{
		inlineImg: newInlineImages("auto"),
		emojiImg:  newEmojiImages("auto", true),
		cellPxW:   10,
		cellPxH:   20,
	}
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)
	return m
}

// takeAllPending drains every pending image regardless of where it sits relative to
// the viewport. The real fetch path takes only what's near the viewport
// (fetchPendingInlineImages → thumbKeysNearViewport); these unit tests carry no
// viewport geometry, so they ask for the lot.
func takeAllPending(ii *inlineImages) []previewItem {
	keys := ii.pendingKeys()
	want := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
	}
	return ii.takePending(want)
}

// blankRows reports whether every line is empty space — the holder a sighted but
// not-yet-built thumbnail reserves so its post is already at its final height.
func blankRows(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return len(lines) > 0
}

func thumbPost(fileID string) *model.Post {
	return &model.Post{
		Id: "post1",
		Metadata: &model.PostMetadata{
			Files: []*model.FileInfo{{
				Id:       fileID,
				Name:     "graph.png",
				MimeType: "image/png",
				Width:    1920,
				Height:   1080,
				Size:     240000,
			}},
		},
	}
}

// fileThumbLines draws f as the attachment of a post that owns it — the shape the
// transcript renders, and the one inlineFileThumbLines needs now that a post can
// have its thumbnails collapsed (z). These tests never collapse, so the post is
// just a carrier.
func fileThumbLines(m *Model, f *model.FileInfo, width int) []string {
	p := &model.Post{Id: "post1", Metadata: &model.PostMetadata{Files: []*model.FileInfo{f}}}
	return m.inlineFileThumbLines(p, f, width)
}

// TestInlineThumbLinesMeasureAsCells is the invariant the whole feature rests on:
// a thumbnail is rendered as ordinary text lines, and every one of them must
// measure exactly cols cells wide (plus the 2-cell gutter) to visualWidth — the
// function postVisualRows/visualRowsBefore use to build the scroll geometry. If a
// placeholder cell or its diacritics ever measured as anything but one cell, the
// row accounting behind msgRowStarts would drift and scrolling past an image
// would land on the wrong line.
func TestInlineThumbLinesMeasureAsCells(t *testing.T) {
	m := thumbModel()
	const cols, rows = 18, 10
	readyThumb(m, "f1", rows, cols, 78)

	lines := fileThumbLines(m, thumbPost("f1").Metadata.Files[0], 80)
	if len(lines) != rows {
		t.Fatalf("got %d thumbnail lines, want %d (one per image row)", len(lines), rows)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, "  ") {
			t.Errorf("line %d is not gutter-indented: %q", i, l)
		}
		if got, want := visualWidth(l), cols+2; got != want {
			t.Errorf("line %d: visualWidth = %d, want %d (%d cells + 2-cell gutter)", i, got, want, cols)
		}
		// visualWidth and lipgloss.Width must agree, or the viewport's own
		// soft-wrap math would disagree with ours.
		if got, want := lipgloss.Width(l), cols+2; got != want {
			t.Errorf("line %d: lipgloss.Width = %d, want %d", i, got, want)
		}
	}
}

// TestInlineThumbLinesSurviveWrapBodyLine checks the thumbnail rows pass through
// the body-wrap stage untouched. They are fitted to the pane width, so wrapping
// must never split one — a split row would tear the image in half.
func TestInlineThumbLinesSurviveWrapBodyLine(t *testing.T) {
	m := thumbModel()
	readyThumb(m, "f1", 10, 18, 78)

	for _, l := range fileThumbLines(m, thumbPost("f1").Metadata.Files[0], 80) {
		if got := wrapBodyLine(l, 80); len(got) != 1 {
			t.Fatalf("wrapBodyLine split a thumbnail row into %d lines; want 1", len(got))
		}
	}
}

// TestRenderAttachmentsThumbnailAboveChip: a ready thumbnail contributes its rows
// above the filename line, and the filename line is still there — the thumbnail
// adds to the chip rather than replacing it.
func TestRenderAttachmentsThumbnailAboveChip(t *testing.T) {
	m := thumbModel()
	const rows = 10
	readyThumb(m, "f1", rows, 18, 78)

	out := m.renderAttachments(thumbPost("f1"), 80)
	lines := strings.Split(out, "\n")
	if len(lines) != rows+1 {
		t.Fatalf("got %d lines, want %d (%d image rows + 1 filename line)", len(lines), rows+1, rows)
	}
	for i := 0; i < rows; i++ {
		if !emojiIsPlaceholder(lines[i]) {
			t.Errorf("line %d should be an image placeholder row, got %q", i, lines[i])
		}
	}
	if last := lines[rows]; !strings.Contains(last, "graph.png") {
		t.Errorf("last line should still be the filename chip, got %q", last)
	}
}

// TestRenderAttachmentsNoThumbnailWhenOff: with the feature off (the default),
// rendering is exactly the old filename-only line — no placeholder cells, and no
// sighting recorded, so nothing is ever fetched.
func TestRenderAttachmentsNoThumbnailWhenOff(t *testing.T) {
	m := &Model{inlineImg: newInlineImages("off"), emojiImg: newEmojiImages("auto", true)}
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)

	out := m.renderAttachments(thumbPost("f1"), 80)
	if strings.Contains(out, "\n") {
		t.Errorf("want a single filename line when thumbnails are off, got:\n%q", out)
	}
	if emojiIsPlaceholder(out) {
		t.Error("no placeholder cells should be emitted when thumbnails are off")
	}
	if got := takeAllPending(m.inlineImg); len(got) != 0 {
		t.Errorf("nothing should be queued for fetch when thumbnails are off, got %d", len(got))
	}
}

// TestInlineThumbNeedsGraphicsTerminal: the config toggle alone isn't enough —
// a terminal that failed (or hasn't answered) the graphics probe gets the plain
// filename line, never a placeholder that would render as garbage.
func TestInlineThumbNeedsGraphicsTerminal(t *testing.T) {
	m := &Model{inlineImg: newInlineImages("auto"), emojiImg: newEmojiImages("auto", true)}
	m.emojiImg.setColorProfile(true)
	m.emojiImg.markUnsupported() // probe came back not-OK

	if m.inlineImagesActive() {
		t.Fatal("thumbnails must not activate on a terminal without Kitty graphics")
	}
	if lines := fileThumbLines(m, thumbPost("f1").Metadata.Files[0], 80); lines != nil {
		t.Errorf("want no thumbnail rows without graphics support, got %d", len(lines))
	}
}

// TestInlineThumbnailsWorkWithEmojiImagesOff guards the combination that would
// be easy to break: thumbnails gate on the terminal probe, not on the custom-emoji
// feature, so `emoji_images: off` with `image_thumbnails: auto` must still draw.
func TestInlineThumbnailsWorkWithEmojiImagesOff(t *testing.T) {
	m := &Model{inlineImg: newInlineImages("auto"), emojiImg: newEmojiImages("off", true)}
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)

	if m.emojiImg.active() {
		t.Fatal("precondition: custom-emoji images should be off")
	}
	if !m.inlineImagesActive() {
		t.Fatal("thumbnails must still work when emoji_images is off")
	}
}

// TestInlineThumbSightingQueuesFetch: the first render of an unseen image records
// it for the background fetch and, in the meantime, *reserves* the rows the
// thumbnail will occupy. Reserving is what makes lazy fetching safe: the post is
// already its final height, so the image arriving later never reflows the
// transcript (a wheel scroll anchors on an absolute row offset and would jump).
func TestInlineThumbSightingQueuesFetch(t *testing.T) {
	m := thumbModel()
	f := thumbPost("f1").Metadata.Files[0] // 1920×1080 → the height cap, 10 rows

	lines := fileThumbLines(m, f, 80)
	if len(lines) != inlineThumbRows {
		t.Fatalf("an unfetched image should hold the %d rows its thumbnail will fill, got %d",
			inlineThumbRows, len(lines))
	}
	if !blankRows(lines) {
		t.Errorf("the reserved rows should be blank until the image arrives, got %q", lines)
	}
	pending := takeAllPending(m.inlineImg)
	if len(pending) != 1 || thumbKey(pending[0]) != "f1" {
		t.Fatalf("first sighting should queue exactly f1 for fetch, got %v", pending)
	}
	// Drained once: a second drain is empty, so Update doesn't refetch every event.
	if got := takeAllPending(m.inlineImg); len(got) != 0 {
		t.Errorf("takePending should drain; second call returned %d", len(got))
	}
}

// TestInlineThumbReservedRowsMatchTheImage: the reservation is only worth anything
// if it is the *same height* as the thumbnail that replaces it — otherwise the post
// resizes on load and we've reflowed anyway, just later.
func TestInlineThumbReservedRowsMatchTheImage(t *testing.T) {
	m := thumbModel()
	f := thumbPost("f1").Metadata.Files[0]

	reserved := fileThumbLines(m, f, 80)

	// The real build sizes from the decoded frame; with the same dimensions it must
	// land on the same cell box the reservation predicted.
	cols, rows := inlineThumbCells(f.Width, f.Height, inlineThumbBox(80), m.cellPxW, m.cellPxH)
	readyThumb(m, "f1", rows, cols, inlineThumbBox(80))

	drawn := fileThumbLines(m, f, 80)
	if len(drawn) != len(reserved) {
		t.Errorf("the image landed on %d rows but %d were reserved: the post would jump on load",
			len(drawn), len(reserved))
	}
	if !emojiIsPlaceholder(drawn[0]) {
		t.Error("once built, the reserved rows should become real placeholder cells")
	}
}

// TestInlineThumbNonImageIgnored: a PDF (or anything we can't decode) never
// queues a fetch, and reserves no space — there is no image coming.
func TestInlineThumbNonImageIgnored(t *testing.T) {
	m := thumbModel()
	f := &model.FileInfo{Id: "f2", Name: "spec.pdf", MimeType: "application/pdf"}

	if lines := fileThumbLines(m, f, 80); lines != nil {
		t.Errorf("a PDF should not draw or reserve image rows, got %d", len(lines))
	}
	if got := takeAllPending(m.inlineImg); len(got) != 0 {
		t.Errorf("a PDF should not be queued for fetch, got %d", len(got))
	}
}

// TestInlineThumbNarrowPaneFallsBack: below inlineThumbMinCols the thumbnail is
// dropped for the plain filename line rather than drawn as an unreadable smear.
func TestInlineThumbNarrowPaneFallsBack(t *testing.T) {
	m := thumbModel()
	readyThumb(m, "f1", 10, 18, 78)

	if lines := fileThumbLines(m, thumbPost("f1").Metadata.Files[0], 6); lines != nil {
		t.Errorf("want no thumbnail in a 6-column pane, got %d rows", len(lines))
	}
}

// TestInlineThumbRefitsWhenPaneNarrows: a thumbnail wider than the pane now allows
// must not be drawn at its old size (it would wrap and tear); it goes back to
// pending so the fetch re-encodes it to fit.
func TestInlineThumbRefitsWhenPaneNarrows(t *testing.T) {
	m := thumbModel()
	readyThumb(m, "f1", 10, 40, 78) // fitted to a wide pane
	f := thumbPost("f1").Metadata.Files[0]

	// Same wide pane: drawn as-is.
	if lines := fileThumbLines(m, f, 80); len(lines) != 10 {
		t.Fatalf("wide pane should draw the thumbnail, got %d rows", len(lines))
	}
	// Pane narrows to 30 cols → box 28 < the thumbnail's 40 cols. It must not be
	// drawn at its old size (it would wrap and tear); it holds blank space instead
	// while the re-fit runs.
	lines := fileThumbLines(m, f, 30)
	if !blankRows(lines) {
		t.Errorf("an over-wide thumbnail must not be drawn; want reserved blanks, got %q", lines)
	}
	pending := takeAllPending(m.inlineImg)
	if len(pending) != 1 || thumbKey(pending[0]) != "f1" {
		t.Fatalf("the narrowed pane should queue f1 for a re-fit, got %v", pending)
	}
}

// TestInlineThumbCellsPreserveAspect: the cell box tracks the image's real aspect
// ratio (accounting for the terminal's non-square cells), capped at
// inlineThumbRows tall, and a small image is not blown up to fill it.
func TestInlineThumbCellsPreserveAspect(t *testing.T) {
	const cellW, cellH = 8, 16

	// A 16:9 landscape image, big enough to be downscaled into the box.
	cols, rows := inlineThumbCells(1920, 1080, 100, cellW, cellH)
	if rows != inlineThumbRows {
		t.Errorf("a large image should fill the %d-row box, got %d rows", inlineThumbRows, rows)
	}
	// rows*cellH px tall ⇒ (rows*cellH)*(16/9) px wide ⇒ /cellW cols ≈ 35.
	wantCols := (rows * cellH * 16 / 9) / cellW
	if diff := cols - wantCols; diff > 1 || diff < -1 {
		t.Errorf("aspect not preserved: got %d cols, want ~%d", cols, wantCols)
	}

	// A portrait image is narrower than a landscape one at the same height.
	pCols, pRows := inlineThumbCells(1080, 1920, 100, cellW, cellH)
	if pRows != inlineThumbRows {
		t.Errorf("portrait should also fill the row box, got %d", pRows)
	}
	if pCols >= cols {
		t.Errorf("portrait (%d cols) should be narrower than landscape (%d cols)", pCols, cols)
	}

	// A tiny image is not upscaled to ten rows.
	if _, smallRows := inlineThumbCells(16, 16, 100, cellW, cellH); smallRows >= inlineThumbRows {
		t.Errorf("a 16px image should not be blown up to %d rows, got %d", inlineThumbRows, smallRows)
	}

	// Never wider than the box it was given.
	if c, _ := inlineThumbCells(4000, 100, 20, cellW, cellH); c > 20 {
		t.Errorf("a very wide image must be clamped to the box: got %d cols, want ≤20", c)
	}
}

// sightAt is sight() with the reservation a Model would have computed, for tests
// that only care about the ready/not-ready answer.
func sightAt(ii *inlineImages, key string, box int) sightResult {
	return ii.sight(previewItem{file: &model.FileInfo{Id: key}}, box, inlineThumbRows, 4)
}

// TestInlineImagesEvictLRU: past the terminal-memory cap the least-recently-seen
// thumbnail is freed from the *terminal* and its id handed back for a kittyDelete,
// while the most-recently-seen (i.e. on-screen) ones stay resident.
//
// Crucially, freeing it does NOT discard the built thumbnail. Terminal memory and
// the decoded/downscaled/PNG-encoded frames are two different resources, and
// conflating them is what made an image-heavy channel rebuild forever
// (TestThumbFetchConverges): the evicted entry vanished, the next render re-sighted
// it as brand new, and it was decoded and re-encoded from scratch.
func TestInlineImagesEvictLRU(t *testing.T) {
	ii := newInlineImages("auto")
	for i := 0; i < maxInlineImages; i++ {
		ii.markReady(fileIDf(i), readyInlineImg{id: uint32(i + 1), rows: 4, cols: 4, box: 78})
	}
	// Re-sight the first one so it is no longer the oldest.
	sightAt(ii, fileIDf(0), 78)

	ii.markReady("overflow", readyInlineImg{id: 9999, rows: 4, cols: 4, box: 78})
	if seq := ii.takeTransmits(); seq == "" { // enforces the caps
		t.Fatal("going over the cap should have freed an image from terminal memory")
	}

	// file 1 is the oldest (file 0 was just re-sighted), so it is the one freed.
	old := ii.entries[fileIDf(1)]
	if old == nil {
		t.Fatal("eviction discarded the built thumbnail: a later sighting would have to " +
			"decode and re-encode it from scratch, which is the rebuild loop")
	}
	if old.resident {
		t.Error("the least-recently-seen image should no longer be resident in the terminal")
	}
	if fresh := ii.entries[fileIDf(0)]; fresh == nil || !fresh.resident {
		t.Error("the re-sighted image should have stayed resident")
	}
}

// TestFreedImageRetransmitsNotRebuilds: once an image has been freed from terminal
// memory, drawing it again must re-transmit the frames we still hold — not send it
// back to the fetcher to be decoded and re-encoded.
func TestFreedImageRetransmitsNotRebuilds(t *testing.T) {
	ii := newInlineImages("auto")
	ii.markReady("f1", readyInlineImg{
		id: 11, rows: 4, cols: 4, box: 78,
		placeholder: kittyPlaceholder(11, 4, 4),
		frameSeqs:   []string{"<frame0>"},
	})
	ii.entries["f1"].resident = false // as if freed to stay under the cap

	r := sightAt(ii, "f1", 78)
	if !r.ready {
		t.Fatal("a freed-but-built image should still draw its placeholder")
	}
	if got := takeAllPending(ii); len(got) != 0 {
		t.Errorf("a freed image must not be re-queued for a rebuild, got %v", got)
	}
	seq := ii.takeTransmits()
	if !strings.Contains(seq, "<frame0>") {
		t.Errorf("drawing a freed image should re-transmit its cached frame, got %q", seq)
	}
	if !ii.entries["f1"].resident {
		t.Error("re-transmitting should mark the image resident again")
	}
}

// TestInlineImagesRefitFreesOldImage: re-encoding a thumbnail at a new size must
// hand back the previous terminal id, or the old image would sit in the
// terminal's memory with nothing pointing at it.
func TestInlineImagesRefitFreesOldImage(t *testing.T) {
	ii := newInlineImages("auto")
	ii.markReady("f1", readyInlineImg{id: 11, rows: 10, cols: 40, box: 78})

	replaced := ii.markReady("f1", readyInlineImg{id: 22, rows: 6, cols: 24, box: 40})
	if len(replaced) != 1 || replaced[0] != 11 {
		t.Fatalf("a re-fit should free the old image id 11, got %v", replaced)
	}
}

func fileIDf(i int) string { return fmt.Sprintf("file%d", i) }

// giphyURL is the shape a GIF picker posts: a markdown image whose URL carries a
// query string. Nothing is uploaded, so the post has no attachments at all.
const giphyURL = "https://media2.giphy.com/media/v1.abc123/28GHfhGFWpFgsQB4wR/200.gif"

func giphyPost() *model.Post {
	return &model.Post{
		Id:      "post2",
		Message: "![Robin Williams Hello GIF](" + giphyURL + ")",
	}
}

// TestInlineBodyImageDrawsGiphyLink is the regression guard for the gap this
// feature shipped with: a Giphy link is a body image URL, not an attachment, so a
// file-only implementation drew nothing for it — while space-to-preview happily
// opened it. Thumbnails must cover exactly what the preview modal covers.
func TestInlineBodyImageDrawsGiphyLink(t *testing.T) {
	m := thumbModel()
	p := giphyPost()

	if p.Metadata != nil && len(p.Metadata.Files) != 0 {
		t.Fatal("precondition: a Giphy link post has no attachments")
	}
	// Nothing drawn yet — but the space is held (a body URL carries no dimensions,
	// so the reservation assumes nominalBodyImage) and the sighting is queued by URL.
	if lines := m.inlineBodyImageLines(p, 80); !blankRows(lines) {
		t.Errorf("an unfetched body image should reserve blank rows, got %q", lines)
	}
	pending := takeAllPending(m.inlineImg)
	if len(pending) != 1 || thumbKey(pending[0]) != giphyURL {
		t.Fatalf("the Giphy URL should be queued for fetch, got %v", pending)
	}
	if pending[0].file != nil {
		t.Error("a body image should carry no FileInfo")
	}

	// Once fetched, it draws its rows.
	const rows, cols = 10, 20
	readyThumb(m, giphyURL, rows, cols, 78)
	lines := m.inlineBodyImageLines(p, 80)
	if len(lines) != rows {
		t.Fatalf("got %d rows for the Giphy thumbnail, want %d", len(lines), rows)
	}
	for i, l := range lines {
		if !emojiIsPlaceholder(l) {
			t.Errorf("row %d should be an image placeholder, got %q", i, l)
		}
		if got, want := visualWidth(l), cols+2; got != want {
			t.Errorf("row %d: visualWidth = %d, want %d", i, got, want)
		}
	}
}

// TestInlineBodyImageMatchesPreviewItems: whatever space-to-preview would open for
// a post, inline thumbnails should draw. Anything less and the feature fails at its
// own premise ("see it without pressing space").
func TestInlineBodyImageMatchesPreviewItems(t *testing.T) {
	m := thumbModel()
	p := giphyPost()

	items := previewImages(p, false)
	if len(items) != 1 || items[0].url != giphyURL {
		t.Fatalf("precondition: space-to-preview should offer the Giphy URL, got %v", items)
	}
	// The same item is what the thumbnail path queues.
	m.inlineBodyImageLines(p, 80)
	pending := takeAllPending(m.inlineImg)
	if len(pending) != 1 || thumbKey(pending[0]) != thumbKey(items[0]) {
		t.Errorf("thumbnails should cover the same item preview does: %v vs %v", pending, items)
	}
}

// TestPostOwnsThumb: invalidation and the animation visibility scan both ask "is
// this thumbnail drawn in this post?". A URL key matches the body that links it; a
// file-id key matches an attachment — and never a body substring.
func TestPostOwnsThumb(t *testing.T) {
	giphy := giphyPost()
	attach := thumbPost("f1")

	if !postOwnsThumb(giphy, giphyURL) {
		t.Error("the Giphy post should own its body-image URL")
	}
	if postOwnsThumb(attach, giphyURL) {
		t.Error("an attachment post should not own an unrelated URL")
	}
	if !postOwnsThumb(attach, "f1") {
		t.Error("the attachment post should own its file id")
	}
	if postOwnsThumb(giphy, "f1") {
		t.Error("the Giphy post should not own a file id it doesn't have")
	}
}

// TestInlineBodyImageAnimatesWhenVisible: a GIF thumbnail keyed by URL must be
// found by the viewport visibility scan, or the animation loop would never run for
// the single most common animated image — a Giphy link.
func TestInlineBodyImageAnimatesWhenVisible(t *testing.T) {
	m := thumbModel()
	p := giphyPost()
	m.posts = []*model.Post{p}
	m.msgRowStarts = []int{0, 12} // the post occupies rows 0..11
	m.msgsView.SetWidth(80)
	m.msgsView.SetHeight(20)

	// A ready, *animated* thumbnail (two frames) keyed by the URL.
	m.inlineImg.markReady(giphyURL, readyInlineImg{
		id: 7, rows: 10, cols: 20, box: 78,
		placeholder: kittyPlaceholder(7, 10, 20),
		frameSeqs:   []string{"<f0>", "<f1>"},
		delays:      []time.Duration{80 * time.Millisecond, 80 * time.Millisecond},
	})

	visible := m.viewportVisibleInlineImages()
	if _, ok := visible[giphyURL]; !ok {
		t.Fatalf("the on-screen Giphy thumbnail should be visible to the animation loop, got %v", visible)
	}
	m.inlineImg.setVisibleAnimated(visible)
	if !m.inlineImg.hasVisibleAnimated() {
		t.Error("an on-screen animated GIF should arm the animation loop")
	}

	// Scrolled far past it → no longer visible, so the loop can stop.
	m.msgsView.SetYOffset(0)
	m.msgRowStarts = []int{100, 112} // the post now sits below the viewport
	if v := m.viewportVisibleInlineImages(); len(v) != 0 {
		t.Errorf("an off-screen GIF should not animate, got %v", v)
	}
}
