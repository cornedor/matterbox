package ui

import (
	"strconv"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// TestAnimTickPreservesFrame is the invariant behind preservesFrame: a GIF
// animation tick re-transmits the next frame under the *same* image id, so the
// screen text is unchanged and View() must reuse the memoized frame. Without
// this, every tick rebuilt the whole screen — a ~1.5ms re-render 12-20×/s for as
// long as any GIF emoji or thumbnail was visible, which is what made an
// otherwise-idle UI feel sluggish.
func TestAnimTickPreservesFrame(t *testing.T) {
	m := benchViewModel(60)
	if m.vcache == nil {
		t.Fatal("precondition: expected an allocated vcache")
	}
	_ = m.viewContent()
	if !m.vcache.viewValid {
		t.Fatal("viewContent didn't populate the cache")
	}
	out, _ := m.update(imgAnimTickMsg{})
	m = out.(Model)
	if !m.vcache.viewValid {
		t.Fatal("imgAnimTickMsg invalidated the view cache: every GIF frame would force a full re-render")
	}
}

// TestVisibleThumbNeverEvicted is the invariant the terminal-memory cap rests on:
// whatever else is freed to stay under maxInlineTerminalBytes, an image that is on screen
// is not. Evicting one kittyDeletes it out from under the placeholder cells still
// displaying it, and since those cells live in the post's cached lines nothing
// re-sights it — so it stays blank for the rest of the session.
//
// The LRU stamp cannot carry this on its own, which is what the test drives: the
// visible post renders from postLineCache, so renderPostLines never reaches
// sight() and the stamp stays frozen at first paint. By the time a screenful of
// later images has arrived, the displayed one is the *oldest* thing in the map —
// the prime eviction candidate. evictLocked has to spare it explicitly.
func TestVisibleThumbNeverEvicted(t *testing.T) {
	m := benchViewModel(20)
	m.cellPxW, m.cellPxH = 10, 20
	m.inlineImg = newInlineImages("auto")
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)

	// An image on the newest post, i.e. on screen and staying there.
	const onScreen = "visible-file"
	p := m.posts[len(m.posts)-1]
	p.Metadata = &model.PostMetadata{Files: []*model.FileInfo{{
		Id: onScreen, Name: "graph.png", MimeType: "image/png", Width: 480, Height: 270,
	}}}
	m.postLineCache = nil
	m.renderMessages()
	readyThumb(&m, onScreen, 10, 36, 88)
	m.postLineCache = nil
	m.renderMessages()

	if _, ok := m.visibleInlineImageKeys()[onScreen]; !ok {
		t.Fatal("precondition: the thumbnail should be on screen")
	}
	stamp := m.inlineImg.entries[onScreen].seen

	// Steady state: the post is unchanged, so these renders are postLineCache hits
	// and never re-sight it. This is what leaves its stamp stale.
	for i := 0; i < 3; i++ {
		m.renderMessages()
	}
	if got := m.inlineImg.entries[onScreen].seen; got != stamp {
		t.Logf("note: the visible thumbnail was re-sighted (seen %d→%d); "+
			"the stale-stamp hazard this guards may have been fixed another way", stamp, got)
	}

	// Scrolling through an image-heavy channel: enough further thumbnails arrive to
	// blow the terminal-residency budget twice over. None of them belongs to a post
	// in view, so every one is a legitimate eviction candidate — and the displayed
	// one, with its frozen stamp, is the oldest thing in the map.
	perImage := 36 * m.cellPxW * 10 * m.cellPxH * 4
	n := 2 * maxInlineTerminalBytes / perImage
	ready := make(map[string]readyInlineImg, n)
	for i := 0; i < n; i++ {
		id := uint32(0x300000 + i)
		ready["other"+strconv.Itoa(i)] = readyInlineImg{
			id: id, rows: 10, cols: 36, box: 88,
			placeholder: kittyPlaceholder(id, 10, 36),
			frameSeqs:   []string{"<transmit>"},
		}
	}
	m, _ = m.handleInlineImagesFetched(inlineImagesFetchedMsg{ready: ready})
	m.renderMessages()
	m.flushInlineTransmits() // where the budget is actually enforced

	ent := m.inlineImg.entries[onScreen]
	if ent == nil {
		t.Fatalf("the on-screen thumbnail was evicted after %d further images arrived: "+
			"its placeholder cells are still displayed but the terminal no longer has the image, "+
			"and its cached post lines mean it is never re-sighted, so it goes permanently blank", n)
	}
	if !ent.resident {
		t.Fatal("the on-screen thumbnail was freed from terminal memory: its placeholder cells " +
			"still name an image the terminal no longer holds, so it renders blank")
	}
	if m.inlineImg.residentBytes > maxInlineTerminalBytes {
		t.Errorf("residency budget not enforced: %d bytes resident, budget %d",
			m.inlineImg.residentBytes, maxInlineTerminalBytes)
	}
}

// benchThumbKeystrokeModel is benchThumbViewModel focused on the composer, i.e.
// the typing path.
func benchThumbKeystrokeModel(b *testing.B, nPosts, kThumbs int) Model {
	m := benchThumbViewModel(b, nPosts, kThumbs)
	m.focus = focusInput
	return m
}

// BenchmarkThumbKeystroke measures a keypress while GIF thumbnails are on screen.
// A keystroke invalidates the frame either way, so the full View() rebuild is
// pre-existing; what this isolates is the *extra* per-event work the feature adds
// to every single event via the Update wrapper — fetchPendingInlineImages and
// maybeStartImageAnim → refreshAnimVisibility → viewportVisibleInlineImages,
// which rescans the visible posts for each ready animated GIF. thumbs=0 is the
// control.
func BenchmarkThumbKeystroke(b *testing.B) {
	for _, k := range []int{0, 3, 8} {
		b.Run("thumbs="+strconv.Itoa(k), func(b *testing.B) {
			var tm tea.Model = benchThumbKeystrokeModel(b, 400, k)
			_ = tm.(Model).View()
			key := tea.KeyPressMsg{Code: 'x', Text: "x"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tm, _ = tm.(Model).Update(key)
				_ = tm.(Model).View()
			}
		})
	}
}
