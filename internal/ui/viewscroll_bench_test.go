package ui

import (
	"strconv"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// benchViewModel builds a fully-renderable model sitting on a team tab with an
// open channel and n loaded posts, so viewContent() exercises the real
// per-message render path bubbletea runs after every event (including every
// wheel event during a scroll).
func benchViewModel(n int) Model {
	m := newTestModel()
	m.width, m.height = 120, 40
	posts, names := benchPosts(n)
	for k, v := range names {
		m.userNames[k] = v
	}
	m.teams = []*model.Team{{Id: "t1", Name: "team", DisplayName: "Team"}}
	// No DMs: tab order is Feed(0), Search(1), team(2). Land on the team tab so
	// viewContent renders the messages pane (not the feed/search bubble list).
	m.teamIdx = 2
	ch := &model.Channel{Id: "c1", Type: model.ChannelTypeOpen, Name: "general", DisplayName: "General"}
	m.channels = map[string][]*model.Channel{"t1": {ch}}
	m.openChannelID = ch.Id
	m.posts = posts
	m.postIdx = n - 1
	m.focus = focusMessages
	m.resizeMessagesViewport()
	m.resizeInput()
	m.renderMessages()
	return m
}

// BenchmarkViewContentScroll measures one full render (what bubbletea builds per
// message) while the message pane is scrolled by changing yOffset between calls —
// the exact work that drains per buffered wheel event. A trackpad's momentum
// flood drains at ~1/this, so this number is the scroll-lag ceiling.
func BenchmarkViewContentScroll(b *testing.B) {
	for _, n := range []int{200, 400} {
		m := benchViewModel(n)
		maxOff := m.msgRowStarts[len(m.msgRowStarts)-1]
		if maxOff < 1 {
			maxOff = 1
		}
		b.Run("posts="+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.msgsView.SetYOffset((i * 3) % maxOff)
				_ = m.renderViewContent()
			}
		})
	}
}

// benignMsg is an unrecognised message: update() falls through to the default
// case (a no-op viewport update), so it exercises the cache-invalidation default
// without side effects.
type benignMsg struct{}

// TestViewCacheInvalidatedExceptWheel guards the fail-safe invariant the trackpad
// fix relies on: a wheel event (which only accumulates, changing nothing on
// screen) preserves the cached frame, while every other message invalidates it —
// so the cache can speed up a flood without ever masking a real change.
func TestViewCacheInvalidatedExceptWheel(t *testing.T) {
	m := benchViewModel(60)
	if m.vcache == nil {
		t.Fatal("precondition: expected an allocated vcache")
	}
	_ = m.viewContent() // prime
	if !m.vcache.viewValid {
		t.Fatal("viewContent didn't populate the cache")
	}

	out, _ := m.update(wheel(tea.MouseWheelDown))
	m = out.(Model)
	if !m.vcache.viewValid {
		t.Fatal("wheel event invalidated the view cache (would defeat flood coalescing)")
	}

	out, _ = m.update(benignMsg{})
	m = out.(Model)
	if m.vcache.viewValid {
		t.Fatal("non-wheel msg left the view cache valid (risks a stale frame)")
	}
}

// BenchmarkWheelFloodRender measures the real per-event cost during a trackpad
// flood: each buffered MouseWheelMsg runs through update() then View(), exactly
// as bubbletea drives it. Between flush ticks the wheel only accumulates, so the
// memoized screen is returned instead of rebuilt — this is what stops the buffer
// from draining after the gesture ends. Contrast with BenchmarkViewContentScroll
// (the full rebuild this avoids).
func BenchmarkWheelFloodRender(b *testing.B) {
	var tm tea.Model = benchViewModel(400)
	_ = tm.(Model).View() // prime the cache
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tm, _ = tm.(Model).update(wheel(tea.MouseWheelDown))
		_ = tm.(Model).View()
	}
}
