package ui

import (
	"os"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// wideImageURL is a body-linked image, so the whole real build path (fetch →
// decode → downscale → encode) runs off the disk cache with no client.
const wideImageURL = "https://example.com/wide.png"

// seedWideImage puts a genuinely wide image — one that has to be shrunk to fit
// any pane — in the URL cache readThumbBytes reads from.
func seedWideImage(t *testing.T, wPx, hPx int) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // before cachedURLPath reads it
	path, err := cachedURLPath(wideImageURL)
	if err != nil {
		t.Fatalf("cachedURLPath: %v", err)
	}
	if err := os.WriteFile(path, noisyPNG(t, wPx, hPx), 0o644); err != nil {
		t.Fatalf("seed url cache: %v", err)
	}
}

// settleRealThumbs drives the real sight → fetch → build → install cycle the
// Update wrapper runs, with real decodes, and returns how many thumbnails were
// built. A settled layout must build each image once; more than once per image is
// the rebuild loop (see TestWideThumbConvergesWithThreadOpen).
func settleRealThumbs(t *testing.T, m *Model, rounds int) int {
	t.Helper()
	builds := 0
	for r := 0; r < rounds; r++ {
		m.renderMessages()
		m.renderThread()
		cmd := m.fetchPendingInlineImages()
		if cmd == nil {
			return builds
		}
		fetched, ok := cmd().(inlineImagesFetchedMsg)
		if !ok {
			t.Fatalf("the fetch returned %T, want inlineImagesFetchedMsg", cmd())
		}
		builds += len(fetched.ready)
		next, _ := m.handleInlineImagesFetched(fetched)
		*m = next
		m.flushInlineTransmits()
	}
	return builds
}

// wideImagePostModel is a renderable model whose one post links a wide image.
func wideImagePostModel(t *testing.T) *Model {
	t.Helper()
	seedWideImage(t, 1600, 400)
	m := New(nil, nil)
	m.width, m.height = 160, 45
	m.cellPxW, m.cellPxH = 10, 20
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)
	m.inlineImg.mode = "auto"
	onTeamTab(&m)
	m.posts = []*model.Post{{
		Id: "post1", UserId: "u1", CreateAt: 1_700_000_000_000,
		Message: "look at this " + wideImageURL,
	}}
	m.postIdx = 0
	m.resizeMessagesViewport()
	return &m
}

// TestWideThumbConvergesWithThreadOpen is the fixed point the fetch scheme
// depends on, in the one layout that used to have none: a thread root shows in
// the transcript and in the thread sidebar at once, and those panes are a couple
// of cells apart in width.
//
// The thumbnail was fitted to the messages pane, so the thread pane saw an image
// wider than its own box, marked it pending again, and the fetch — which fitted
// to the messages pane unconditionally — rebuilt exactly the same too-wide image.
// Every event: a fresh decode + downscale + PNG-encode, a kittyDelete of the old
// terminal id and a transmit of a new one, which is what made the image flash.
func TestWideThumbConvergesWithThreadOpen(t *testing.T) {
	m := wideImagePostModel(t)
	m.threadOpen = true
	m.threadPosts = m.posts
	m.resizeMessagesViewport()

	msgsBox := inlineThumbBox(m.msgsView.Width())
	threadBox := inlineThumbBox(m.threadView.Width())
	if msgsBox <= threadBox {
		t.Fatalf("precondition: the panes must differ in width, got msgs=%d thread=%d", msgsBox, threadBox)
	}

	if builds := settleRealThumbs(t, m, 6); builds != 1 {
		t.Errorf("the image was built %d times over 6 rounds, want 1: the transcript and the "+
			"thread sidebar are re-fitting it out from under each other, for ever", builds)
	}
	ent := m.inlineImg.entries[wideImageURL]
	if ent == nil {
		t.Fatal("the image was never sighted")
	}
	if ent.state != inlineImgReady {
		t.Fatalf("the image never settled as ready: state %d, still queued for another build", ent.state)
	}
	if ent.cols > threadBox {
		t.Errorf("the thumbnail is %d cells wide but the narrower pane drawing it holds %d: "+
			"it will be sent back to be re-fitted on the next render", ent.cols, threadBox)
	}
}

// TestWideThumbConvergesInTranscript: the same fixed point with no thread open —
// the case that always worked, kept honest now that the fit box is derived from
// the panes rather than handed in by the caller.
func TestWideThumbConvergesInTranscript(t *testing.T) {
	m := wideImagePostModel(t)
	if builds := settleRealThumbs(t, m, 6); builds != 1 {
		t.Errorf("the image was built %d times over 6 rounds, want 1", builds)
	}
	ent := m.inlineImg.entries[wideImageURL]
	if ent == nil {
		t.Fatal("the image was never sighted")
	}
	if box := inlineThumbBox(m.msgsView.Width()); ent.cols > box {
		t.Errorf("the thumbnail is %d cells wide but the pane drawing it holds %d", ent.cols, box)
	}
}
