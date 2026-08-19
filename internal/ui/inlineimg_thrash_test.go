package ui

import (
	"strconv"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// fakeReady is a built thumbnail, standing in for what buildInlineThumb returns
// after the decode + downscale + PNG-encode of every frame.
func fakeReady(i int) readyInlineImg {
	id := uint32(0x400000 + i)
	return readyInlineImg{
		id: id, rows: 10, cols: 36, box: 88,
		placeholder: kittyPlaceholder(id, 10, 36),
		frameSeqs:   []string{"<transmit>"},
	}
}

// imagePosts builds n posts each carrying one image attachment.
func imagePosts(n int) []*model.Post {
	posts := make([]*model.Post, n)
	for i := 0; i < n; i++ {
		posts[i] = &model.Post{
			Id:       "img-post-" + strconv.Itoa(i),
			UserId:   "u1",
			Message:  "look at this",
			CreateAt: int64(1_700_000_000_000 + i*60_000),
			UpdateAt: int64(1_700_000_000_000 + i*60_000),
			Metadata: &model.PostMetadata{Files: []*model.FileInfo{{
				Id: "img" + strconv.Itoa(i), Name: "shot.png", MimeType: "image/png",
				Width: 480, Height: 270, Size: 90000,
			}}},
		}
	}
	return posts
}

// settleThumbs drives the real sight → fetch → build → install loop to a fixed
// point, exactly as the app does: renderMessages sights every post in the render
// window, the Update wrapper drains what was sighted, the background build
// returns, handleInlineImagesFetched installs it (evicting to stay under the cap)
// and re-renders — which sights again. Returns how many thumbnails were *built*
// in total. Steady state should be zero further builds; anything else is a loop.
func settleThumbs(t *testing.T, m Model, rounds int) (builds int, lastPending int) {
	t.Helper()
	for r := 0; r < rounds; r++ {
		m.renderMessages()
		// Exactly what the Update wrapper drains: only the images near the viewport.
		items := m.inlineImg.takePending(m.thumbKeysNearViewport(m.inlineImg.pendingKeys()))
		lastPending = len(items)
		if len(items) == 0 {
			return builds, 0
		}
		ready := make(map[string]readyInlineImg, len(items))
		for i, it := range items {
			builds++ // one decode + downscale + PNG-encode per frame, off the render loop
			ready[thumbKey(it)] = fakeReady(r*1000 + i)
		}
		m, _ = m.handleInlineImagesFetched(inlineImagesFetchedMsg{ready: ready})
		m.flushInlineTransmits() // enforces the caps, as the Update wrapper does
	}
	return builds, lastPending
}

// BenchmarkChannelOpenThumbs measures what the pprof of channel-switching actually
// captured: how many thumbnails have to be *built* — decode + downscale +
// PNG-encode every frame — to display an image-heavy channel. It counts builds
// rather than timing them, because the build runs on a Cmd goroutine: the number is
// the cost, and it was unbounded.
//
// Switching away and back must cost nothing the second time. Before, eviction
// discarded the built frames, so every revisit rebuilt every image from scratch —
// which is the 70%-of-all-CPU the profile shows in buildInlineThumb, with
// readThumbBytes at ~0 because the bytes were on disk all along.
func BenchmarkChannelOpenThumbs(b *testing.B) {
	base := benchViewModel(4)
	base.cellPxW, base.cellPxH = 10, 20
	base.inlineImg = newInlineImages("auto")
	base.emojiImg.setProbeOK()
	base.emojiImg.setColorProfile(true)
	chA, chB := imagePosts(80), imagePosts(80)
	for i, p := range chB { // distinct images, so the two channels can't share
		p.Id = "b-" + p.Id
		p.Metadata.Files[0].Id = "b-img" + strconv.Itoa(i)
	}

	builds := 0
	open := func(m Model, posts []*model.Post) Model {
		m.posts = posts
		m.postIdx = len(posts) - 1
		m.postLineCache = nil
		for {
			m.renderMessages()
			items := m.inlineImg.takePending(m.thumbKeysNearViewport(m.inlineImg.pendingKeys()))
			if len(items) == 0 {
				return m
			}
			ready := make(map[string]readyInlineImg, len(items))
			for _, it := range items {
				builds++
				ready[thumbKey(it)] = fakeReady(builds)
			}
			m, _ = m.handleInlineImagesFetched(inlineImagesFetchedMsg{ready: ready})
			m.flushInlineTransmits()
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	m := base
	for i := 0; i < b.N; i++ {
		m = open(m, chA)
		m = open(m, chB)
	}
	b.StopTimer()
	b.ReportMetric(float64(builds)/float64(b.N), "builds/switch-pair")
}

// TestFailedThumbReleasesItsReservedRows: reserving space for an image that never
// arrives would leave a blank hole in the transcript for the rest of the session.
// A sighted image reserves its rows on first render (so lazy loading never
// reflows), and the post's lines are then cached — so when the image turns out to
// be undecodable, the post must be re-rendered to give those rows back. Nothing else
// would ever re-render it.
func TestFailedThumbReleasesItsReservedRows(t *testing.T) {
	m := benchViewModel(4)
	m.cellPxW, m.cellPxH = 10, 20
	m.inlineImg = newInlineImages("auto")
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)
	m.posts = imagePosts(1)
	m.postIdx = 0
	m.postLineCache = nil

	p := m.posts[0]
	m.renderMessages()
	reserved, _ := m.renderPostLines(p, false, nestInfo{})
	if len(reserved) == 0 {
		t.Fatal("precondition: the sighted image should have reserved rows")
	}

	m, _ = m.handleInlineImagesFetched(inlineImagesFetchedMsg{failed: []string{"img0"}})

	after, _ := m.renderPostLines(p, false, nestInfo{})
	if len(after) >= len(reserved) {
		t.Errorf("an undecodable image kept its %d reserved rows (post went %d → %d lines): "+
			"they stay on screen as a blank hole forever, because the post's cached lines "+
			"mean it is never re-rendered again", len(reserved)-len(after), len(reserved), len(after))
	}
}

// TestThumbFetchConverges is the invariant the whole background-fetch scheme
// depends on: an unchanging channel must reach a fixed point where nothing more
// is sighted, so no image is ever rebuilt.
//
// It doesn't, once a channel holds more images than maxInlineImages. renderMessages
// renders *every* post in the render window (up to maxLoadedPosts, currently 400) —
// not just the handful inside the viewport — so sight() is called for every image
// in the window. The terminal-memory cap is 64. The moment the window holds more
// than 64 images the two disagree, and this cycle spins forever:
//
//	render sights all N ─→ fetch builds them ─→ markReady evicts N−64 to stay
//	under the cap, *deleting their entries* ─→ invalidatePostsForThumbs drops
//	those posts' cached lines ─→ re-render sights the evicted ones afresh ─→ …
//
// Each turn re-runs a full decode + downscale + PNG-encode per frame, on bytes
// already sitting in the disk cache. It is what a live pprof of channel-switching
// shows as ~70% of all CPU in buildInlineThumb, with readThumbBytes at ~0.
func TestThumbFetchConverges(t *testing.T) {
	for _, n := range []int{maxInlineImages / 2, maxInlineImages + 16} {
		t.Run("images="+strconv.Itoa(n), func(t *testing.T) {
			m := benchViewModel(4)
			m.cellPxW, m.cellPxH = 10, 20
			m.inlineImg = newInlineImages("auto")
			m.emojiImg.setProbeOK()
			m.emojiImg.setColorProfile(true)
			m.posts = imagePosts(n)
			m.postIdx = n - 1
			m.postLineCache = nil

			builds, pending := settleThumbs(t, m, 12)

			if pending != 0 {
				t.Errorf("after 12 render/fetch rounds the channel still wants %d more images built: "+
					"it never settles. %d thumbnails were built for %d images — each rebuild is a full "+
					"decode + downscale + PNG-encode of bytes already on disk. The render window sights "+
					"every image it holds, but only %d fit in terminal memory, so the overflow was evicted "+
					"and immediately re-sighted, forever.", pending, builds, n, maxInlineImages)
			}
			if builds > n {
				t.Errorf("built %d thumbnails for %d images: %d were rebuilt", builds, n, builds-n)
			}
			// The real win: only the images near the viewport are built at all. The
			// window holds n of them but the terminal only ever shows a screenful, so
			// building all n would be paying for images nobody can see.
			if builds >= n {
				t.Errorf("built %d thumbnails for a %d-image window: the fetch is not viewport-gated, "+
					"so every image in the render window is being decoded and PNG-encoded to display "+
					"the handful that are actually on screen", builds, n)
			}
			t.Logf("%d images in the window → %d built (only what's within %d screens of the viewport)",
				n, builds, inlineFetchMarginScreens)
		})
	}
}
