package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Collapsing a post's inline thumbnails with z: the chevron that says whether they
// are showing, and — the half that isn't cosmetic — that a collapsed image stops
// costing anything at all.

// thumbCollapseModel is a rendered transcript with thumbnails active: n posts, a
// viewport, and msgRowStarts, so the viewport-relative questions (what is on
// screen, what animates, what is worth fetching) have real answers.
func thumbCollapseModel(t *testing.T, n int) Model {
	t.Helper()
	m := benchViewModel(n)
	m.cellPxW, m.cellPxH = 10, 20
	m.inlineImg = newInlineImages("auto")
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)
	if !m.inlineImagesActive() {
		t.Fatal("precondition: thumbnails should be active")
	}
	return m
}

// withImageAttachment hangs an image on the selected (newest) post and re-renders.
func withImageAttachment(m *Model, fileID string) *model.Post {
	p := m.posts[len(m.posts)-1]
	p.Metadata = &model.PostMetadata{Files: []*model.FileInfo{{
		Id: fileID, Name: "graph.png", MimeType: "image/png", Width: 480, Height: 270, Size: 240000,
	}}}
	m.postIdx = len(m.posts) - 1
	m.postLineCache = nil
	m.renderMessages()
	return p
}

// animatedThumb installs a ready, resident, *animated* thumbnail — two frames, so
// it is a GIF as far as every consumer here is concerned.
func animatedThumb(m *Model, key string, id uint32) {
	m.inlineImg.markReady(key, readyInlineImg{
		id: id, rows: 10, cols: 36, box: 88,
		placeholder: kittyPlaceholder(id, 10, 36),
		frameSeqs:   []string{"<frame0>", "<frame1>"},
		delays:      []time.Duration{80 * time.Millisecond, 80 * time.Millisecond},
	})
}

// pressZ is the collapse key on the message pane.
func pressZ(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.toggleCollapse(focusMessages)
	out, ok := next.(Model)
	if !ok {
		t.Fatalf("toggleCollapse returned %T, want Model", next)
	}
	return out
}

// TestThumbChevronShowsWhetherThumbnailIsOpen: the attachment's own indicator line
// carries the disclosure chevron, pointing down at the image it is showing and right
// at the one it is hiding. This is the only affordance saying the image can come
// back, so a collapsed post must still draw the line it sits on.
func TestThumbChevronShowsWhetherThumbnailIsOpen(t *testing.T) {
	m := thumbModel()
	readyThumb(m, "f1", 10, 18, 78)
	p := thumbPost("f1")

	open := strings.Split(m.renderAttachments(p, 80), "\n")
	last := open[len(open)-1]
	if !strings.Contains(last, thumbOpenChevron) {
		t.Errorf("a showing thumbnail should mark its indicator with %q, got %q", thumbOpenChevron, last)
	}
	if !strings.Contains(last, "graph.png") {
		t.Errorf("the chevron must not displace the filename, got %q", last)
	}

	m.thumbsCollapsed = map[string]bool{p.Id: true}
	shut := strings.Split(m.renderAttachments(p, 80), "\n")
	if len(shut) != 1 {
		t.Fatalf("a collapsed post should draw the indicator line alone, got %d lines", len(shut))
	}
	if !strings.Contains(shut[0], thumbShutChevron) {
		t.Errorf("a collapsed thumbnail should mark its indicator with %q, got %q", thumbShutChevron, shut[0])
	}
	if emojiIsPlaceholder(shut[0]) {
		t.Error("a collapsed post drew image placeholder cells")
	}
}

// TestNoChevronWhenThumbnailsOff: with the feature off — or on a terminal that
// can't paint images — nothing is collapsible, so the indicator keeps exactly the
// look it has always had. A chevron there would promise a thumbnail that never comes
// and a key that does nothing.
func TestNoChevronWhenThumbnailsOff(t *testing.T) {
	m := &Model{inlineImg: newInlineImages("off"), emojiImg: newEmojiImages("auto", true)}
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)

	out := m.renderAttachments(thumbPost("f1"), 80)
	for _, chev := range []string{thumbOpenChevron, thumbShutChevron} {
		if strings.Contains(out, chev) {
			t.Errorf("thumbnails are off but the attachment line carries %q: %q", chev, out)
		}
	}
	if body := m.markdownBody(giphyPost()); strings.Contains(body, thumbOpenChevron) {
		t.Errorf("thumbnails are off but the body indicator carries a chevron: %q", body)
	}
}

// TestNonImageAttachmentHasNoChevron: a PDF draws no thumbnail, so its chip gets no
// chevron — and neither does an image format we can't decode.
func TestNonImageAttachmentHasNoChevron(t *testing.T) {
	m := thumbModel()
	p := &model.Post{Id: "post1", Metadata: &model.PostMetadata{Files: []*model.FileInfo{
		{Id: "f2", Name: "spec.pdf", MimeType: "application/pdf", Size: 1000},
		{Id: "f3", Name: "logo.webp", MimeType: "image/webp", Size: 1000},
	}}}

	out := m.renderAttachments(p, 80)
	for _, chev := range []string{thumbOpenChevron, thumbShutChevron} {
		if strings.Contains(out, chev) {
			t.Errorf("a file with no thumbnail carries %q: %q", chev, out)
		}
	}
}

// TestBodyImageIndicatorCarriesChevron: a Giphy link is a *body* image — its
// indicator is the "🖼️ alt" the markdown renders, not an attachment chip — and it
// gets the same chevron, from the same state. Without this the common case (the GIF
// picker) would collapse with no sign it could be brought back.
func TestBodyImageIndicatorCarriesChevron(t *testing.T) {
	m := thumbModel()
	p := giphyPost()

	body := m.markdownBody(p)
	if !strings.Contains(body, thumbOpenChevron+" 🖼️") {
		t.Errorf("body-image indicator should lead with %q, got %q", thumbOpenChevron, body)
	}

	m.thumbsCollapsed = map[string]bool{p.Id: true}
	shut := m.markdownBody(p)
	if !strings.Contains(shut, thumbShutChevron+" 🖼️") {
		t.Errorf("a collapsed body image should lead with %q, got %q", thumbShutChevron, shut)
	}
	if lines := m.inlineBodyImageLines(p, 80); lines != nil {
		t.Errorf("a collapsed post drew %d body-image rows", len(lines))
	}
}

// TestImageLinkIndicatorCarriesChevron: a bare pasted image URL renders as a link,
// not a "🖼️" chip, but it still grows a thumbnail — so the chevron rides on the link
// text. A link we can't decode (a page, a .webp) gets none: it has no thumbnail to
// describe.
func TestImageLinkIndicatorCarriesChevron(t *testing.T) {
	m := thumbModel()

	img := &model.Post{Id: "p1", Message: "look at https://example.com/cat.gif"}
	if got := m.markdownBody(img); !strings.Contains(got, thumbOpenChevron) {
		t.Errorf("a bare image link should carry a chevron, got %q", got)
	}
	page := &model.Post{Id: "p2", Message: "see https://example.com/docs/index.html"}
	if got := m.markdownBody(page); strings.Contains(got, thumbOpenChevron) {
		t.Errorf("a link with no thumbnail should carry no chevron, got %q", got)
	}
}

// TestImgMarksNeverEscape: the chevron rides on an invisible marker planted in the
// cached markdown (imgIndicatorMark), so every consumer of a rendered body must
// either resolve it or strip it. One that forgets would ship a NUL sentinel to the
// terminal — and would only be found by eye, on a GIF, in a pane nobody was
// watching.
func TestImgMarksNeverEscape(t *testing.T) {
	m := thumbModel()
	m.msgsView.SetWidth(80)
	m.threadView.SetWidth(80)
	p := giphyPost()

	bodies := map[string]string{
		"markdownBody (transcript)": m.markdownBody(p),
		"markdownBodyPlain":         m.markdownBodyPlain(p),
		"markdownBodyRaw":           m.markdownBodyRaw(p),
		"meEmoteLine":               m.meEmoteLine(p),
	}
	for name, body := range bodies {
		if strings.Contains(body, imgIndicatorMark) {
			t.Errorf("%s leaked the image marker: %q", name, body)
		}
	}
	lines, _ := m.renderPostLines(p, false)
	if strings.Contains(strings.Join(lines, "\n"), imgIndicatorMark) {
		t.Errorf("renderPostLines leaked the image marker: %q", lines)
	}
	// The plain body is the same text minus the chevron — nothing else moves.
	if plain := m.markdownBodyPlain(p); strings.Contains(plain, thumbOpenChevron) {
		t.Errorf("a body rendered without thumbnails should carry no chevron: %q", plain)
	} else if !strings.Contains(plain, "🖼️") {
		t.Errorf("stripping the marker ate the indicator itself: %q", plain)
	}
}

// TestToggleCollapseHidesImageFirst is the z semantics on a post with an image. A
// thumbnail arrives *shown* while a long body arrives *folded*, so the two can't
// share one flag — and the image, being the thing the eye lands on, decides which
// way the first press goes: collapse. The second press expands both.
func TestToggleCollapseHidesImageFirst(t *testing.T) {
	m := thumbCollapseModel(t, 6)
	p := withImageAttachment(&m, "f1")
	if m.collapseRows <= 0 {
		t.Fatal("precondition: body collapsing should be enabled")
	}

	m = pressZ(t, m)
	if !m.thumbsCollapsed[p.Id] {
		t.Error("the first z on a post with an image should hide its thumbnail")
	}
	if m.expandedPosts[p.Id] {
		t.Error("collapsing the message should leave a long body folded, not expand it")
	}

	m = pressZ(t, m)
	if m.thumbsCollapsed[p.Id] {
		t.Error("the second z should bring the thumbnail back")
	}
	if !m.expandedPosts[p.Id] {
		t.Error("expanding the message should unfold its body too")
	}
}

// TestToggleCollapseWithoutImageUnchanged: on a post with no image, z is exactly
// what it always was — the long-body fold, expanding on the first press.
func TestToggleCollapseWithoutImageUnchanged(t *testing.T) {
	m := thumbCollapseModel(t, 6)
	m.postIdx = len(m.posts) - 1
	p := m.posts[m.postIdx]

	m = pressZ(t, m)
	if !m.expandedPosts[p.Id] {
		t.Error("the first z on a text post should expand its folded body")
	}
	if m.thumbsCollapsed[p.Id] {
		t.Error("a post with no image should never be marked thumb-collapsed")
	}
	m = pressZ(t, m)
	if m.expandedPosts[p.Id] {
		t.Error("the second z should re-fold the body")
	}
}

// TestCollapsedGIFStopsAnimating is the point of the whole exercise: a GIF that is
// collapsed must stop driving the animation loop. It is not enough that its rows
// aren't drawn — the loop asks which *posts* are on screen, and a collapsed post is
// as on screen as ever, so without unhooking it here the GIF would keep ticking
// (and re-transmitting a frame nobody can see) for the rest of the session.
func TestCollapsedGIFStopsAnimating(t *testing.T) {
	m := thumbCollapseModel(t, 6)
	withImageAttachment(&m, "f1")
	animatedThumb(&m, "f1", 0x777)

	if !m.refreshAnimVisibility() {
		t.Fatal("precondition: a visible animated thumbnail should drive the loop")
	}
	if !m.inlineImg.hasVisibleAnimated() {
		t.Fatal("precondition: the GIF should be visible")
	}

	m = pressZ(t, m)

	if m.refreshAnimVisibility() {
		t.Error("a collapsed GIF still reports as visible: it would keep ticking")
	}
	if m.inlineImg.hasVisibleAnimated() {
		t.Error("a collapsed GIF is still animating")
	}
	// And the loop, on its next tick, has nothing to do and stops itself.
	m.imgAnimating = true
	if cmd := m.advanceImageAnim(); cmd != nil {
		t.Error("the animation loop should stop once the only GIF on screen is collapsed")
	}
	if m.imgAnimating {
		t.Error("imgAnimating should be cleared when nothing animates")
	}
	if cmd := m.maybeStartImageAnim(); cmd != nil {
		t.Error("a collapsed GIF must not re-arm the animation loop")
	}
	if _, ok := m.visibleInlineImageKeys()["f1"]; ok {
		t.Error("a collapsed thumbnail is still counted as on screen")
	}
}

// TestCollapsedThumbNotFetched: collapsing before the image arrives cancels the work
// entirely — it is never fetched, decoded or encoded. The image stays pending (it is
// still in the render window), so this is checking the *fetch reach*, which is what
// decides whether ~10ms/frame gets spent.
func TestCollapsedThumbNotFetched(t *testing.T) {
	m := thumbCollapseModel(t, 6)
	p := withImageAttachment(&m, "f1")

	pending := m.inlineImg.pendingKeys()
	if len(pending) != 1 || pending[0] != "f1" {
		t.Fatalf("precondition: rendering the post should sight f1, got %v", pending)
	}
	if _, ok := m.thumbKeysNearViewport(pending)["f1"]; !ok {
		t.Fatal("precondition: an on-screen image should be within fetching reach")
	}

	m.thumbsCollapsed = map[string]bool{p.Id: true}
	m.renderMessages()

	if got := m.thumbKeysNearViewport(m.inlineImg.pendingKeys()); len(got) != 0 {
		t.Errorf("a collapsed post's image is still being fetched: %v", got)
	}
	if cmd := m.fetchPendingInlineImages(); cmd != nil {
		t.Error("collapsing should leave nothing to fetch")
	}
}

// TestCollapsedThumbFreedFromTerminalMemory: collapsing hands the image's terminal
// memory back rather than leaving it parked until the LRU happens to notice. The
// built frames stay in *our* memory, so expanding again re-transmits a string we
// already have instead of decoding the image a second time.
func TestCollapsedThumbFreedFromTerminalMemory(t *testing.T) {
	m := thumbCollapseModel(t, 6)
	withImageAttachment(&m, "f1")
	const id uint32 = 0x777
	// The real order: the sighting is drained by the fetch, which then installs the
	// built image. Draining *after* markReady would put the ready entry back into
	// fetching and quietly defeat the rest of this test.
	takeAllPending(m.inlineImg)
	animatedThumb(&m, "f1", id)
	m.renderMessages()
	m.flushInlineTransmits() // as the Update wrapper does: settles what is on screen

	if !m.inlineImg.entries["f1"].resident {
		t.Fatal("precondition: the thumbnail should be in terminal memory")
	}
	frames := len(m.inlineImg.entries["f1"].frameSeqs)

	m = pressZ(t, m)
	cmd := m.flushInlineTransmits()
	if cmd == nil {
		t.Fatal("collapsing a thumbnail should free it from terminal memory")
	}
	raw, ok := cmd().(tea.RawMsg)
	if !ok {
		t.Fatalf("expected tea.RawMsg, got %T", cmd())
	}
	seq, _ := raw.Msg.(string)
	if !strings.Contains(seq, kittyDelete(id)) {
		t.Errorf("the collapsed image was not deleted from the terminal: %q", seq)
	}
	ent := m.inlineImg.entries["f1"]
	if ent.resident {
		t.Error("the entry still claims to be in terminal memory")
	}
	if len(ent.frameSeqs) != frames {
		t.Errorf("the built frames were thrown away (%d → %d): expanding would pay for a full rebuild",
			frames, len(ent.frameSeqs))
	}

	// Expanding re-transmits what we kept, rather than rebuilding it.
	m = pressZ(t, m)
	m.postLineCache = nil
	m.renderMessages()
	if cmd := m.flushInlineTransmits(); cmd == nil {
		t.Error("expanding should re-transmit the image the terminal no longer holds")
	}
	if !m.inlineImg.entries["f1"].resident {
		t.Error("the expanded thumbnail is not back in terminal memory")
	}
	if got := m.inlineImg.pendingKeys(); len(got) != 0 {
		t.Errorf("expanding queued a rebuild (%v); the frames were still in memory", got)
	}
}

// TestCollapsedGIFStillOwesItsFrames: a GIF is built as a still and only earns its
// animation frames once it reaches the screen (buildInlineThumb). Collapse it before
// that happens and the note saying the frames are owed — its ii.deferred entry — must
// survive, or expanding it again would hand back a GIF that is frozen for the rest of
// the session, with nothing left to say why.
func TestCollapsedGIFStillOwesItsFrames(t *testing.T) {
	m := thumbCollapseModel(t, 6)
	p := m.posts[len(m.posts)-1]
	p.Message = giphyPost().Message
	m.postIdx = len(m.posts) - 1
	m.postLineCache = nil
	m.renderMessages()
	takeAllPending(m.inlineImg)

	const id uint32 = 0x999
	m.inlineImg.markReady(giphyURL, readyInlineImg{
		id: id, rows: 10, cols: 36, box: 88,
		placeholder:    kittyPlaceholder(id, 10, 36),
		frameSeqs:      []string{"<still>"}, // frame 0 only: the rest are still owed
		deferredFrames: true,
		item:           previewItem{url: giphyURL},
	})
	m.renderMessages()
	m.flushInlineTransmits()

	m = pressZ(t, m) // collapsed before the frames were ever encoded
	m.flushInlineTransmits()
	if jobs := m.inlineImg.takeDeferredFrames(); len(jobs) != 0 {
		t.Fatal("a collapsed GIF must not buy its animation frames")
	}

	m = pressZ(t, m) // and back
	m.postLineCache = nil
	m.renderMessages()
	m.flushInlineTransmits()

	jobs := m.inlineImg.takeDeferredFrames()
	if len(jobs) != 1 || jobs[0].key != giphyURL {
		t.Fatalf("an expanded GIF never gets its frames built (%v): it would never animate again", jobs)
	}
	if jobs[0].id != id || jobs[0].box != 88 {
		t.Errorf("the frames must complete the still already on screen (id %#x, box 88), got id %#x box %d",
			id, jobs[0].id, jobs[0].box)
	}
}

// TestReleaseSparesImageShownElsewhere: the same Giphy URL posted twice is one image
// in terminal memory. Collapsing one copy must not kittyDelete it out from under the
// other — whose placeholder cells live in cached lines, so nothing would re-sight it
// and it would stay a blank hole.
func TestReleaseSparesImageShownElsewhere(t *testing.T) {
	m := thumbCollapseModel(t, 6)
	first, second := m.posts[len(m.posts)-2], m.posts[len(m.posts)-1]
	first.Message = giphyPost().Message
	second.Message = giphyPost().Message
	m.postIdx = len(m.posts) - 1
	m.postLineCache = nil
	m.renderMessages()

	const id uint32 = 0x888
	animatedThumb(&m, giphyURL, id)
	m.renderMessages()
	m.flushInlineTransmits()

	// Collapse only the second post; the first still shows the same image.
	m = pressZ(t, m)
	if !m.thumbsCollapsed[second.Id] {
		t.Fatal("precondition: the selected post should be collapsed")
	}
	cmd := m.flushInlineTransmits()
	if cmd != nil {
		if raw, ok := cmd().(tea.RawMsg); ok {
			if seq, _ := raw.Msg.(string); strings.Contains(seq, kittyDelete(id)) {
				t.Fatal("freed an image another post is still displaying: it would go blank")
			}
		}
	}
	if !m.inlineImg.entries[giphyURL].resident {
		t.Error("the image is still on screen in the other post; it must stay resident")
	}
	if !m.inlineImg.hasVisibleAnimated() {
		t.Error("the other post's copy should still be animating")
	}
}
