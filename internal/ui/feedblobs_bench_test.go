package ui

import (
	"fmt"
	"strconv"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// benchBlobModel is an empty Feed tab at a terminal size, animating at the top
// configurable frame rate — the state the drifting blob field is in when it
// costs the most.
func benchBlobModel(w, h int) Model {
	m := newTestModel()
	m.vcache = &viewCache{}
	m.width, m.height = w, h
	fs := newFeedState(false, feedBlobFPSMax)
	fs.built = true
	m.feed = fs
	gotoTab(&m, tabFeed)
	m.focus = focusFeed
	m.layoutPanes()
	m.renderFeedResults()
	m.viewContent()
	return m
}

func BenchmarkFeedBlobField(b *testing.B) {
	for _, sz := range [][2]int{{96, 36}, {160, 48}, {240, 64}} {
		m := benchBlobModel(sz[0], sz[1])
		w, h := m.feed.view.Width(), m.feed.view.Height()
		b.Run(fmt.Sprintf("%dx%d", w, h), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				sink = renderFeedBlobs(w, h, float64(i)*0.2, blobNudges{})
			}
		})
	}
}

// BenchmarkFeedBlobTick: one animation frame's model-side work (advance the
// springs, repaint the field into the viewport).
func BenchmarkFeedBlobTick(b *testing.B) {
	m := benchBlobModel(160, 48)
	b.ReportAllocs()
	for b.Loop() {
		m.applyFeedBlobTick()
	}
}

// BenchmarkFeedBlobFrame: the whole per-frame cost bubbletea pays — Update on
// the tick message plus the full View() re-render that follows it.
func BenchmarkFeedBlobFrame(b *testing.B) {
	for _, sz := range [][2]int{{96, 36}, {160, 48}, {240, 64}} {
		b.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(b *testing.B) {
			m := benchBlobModel(sz[0], sz[1])
			b.ReportAllocs()
			for b.Loop() {
				nm, _ := m.Update(feedBlobTickMsg{})
				mm := nm.(Model)
				sinkV = mm.View()
				m = mm
			}
		})
	}
}

// BenchmarkFeedIdleFrame: the same View() without the blob repaint — the
// baseline any other event (a keystroke) costs on this screen.
func BenchmarkFeedIdleFrame(b *testing.B) {
	m := benchBlobModel(160, 48)
	b.ReportAllocs()
	for b.Loop() {
		sinkV = m.View()
	}
}

var sink string

var sinkV tea.View

// BenchmarkFeedBlobFrameLoaded is the same frame with a realistic heap behind
// it — a live client holds thousands of cached posts, so every GC cycle the
// frame's garbage triggers has far more to scan than an empty test model does.
func BenchmarkFeedBlobFrameLoaded(b *testing.B) {
	for _, n := range []int{0, 2000, 10000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			m := benchBlobModel(160, 40)
			if n > 0 {
				posts, names := benchPosts(n)
				m.posts = posts
				for k, v := range names {
					m.userNames[k] = v
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				nm, _ := m.Update(feedBlobTickMsg{})
				mm := nm.(Model)
				sinkV = mm.View()
				m = mm
			}
		})
	}
}
