package ui

import (
	"fmt"
	"testing"
)

// A live message must show up without a scroll whenever the reader could see
// the bottom of the transcript as it arrived — whether the selection sits on
// the newest message, a few posts above it, or the wheel parked the view at the
// bottom. A reader who scrolled up is left where they are.

// liveTailModel is blurredDMModel with n read posts tall enough to overflow the
// 40-row pane, laid out once so the viewport geometry is real, parked at the
// bottom with the newest post selected.
func liveTailModel(n int) Model {
	m := blurredDMModel()
	for i := 0; i < n; i++ {
		p := tallPost(fmt.Sprintf("p%d", i), int64(1000+i), 3)
		p.ChannelId, p.UserId = "dm", "u2"
		m.posts = append(m.posts, p)
	}
	m.postIdx = n - 1
	m.anchorMsgSelBottom = true
	m.renderMessages()
	return m
}

func (m *Model) msgsTotalRows() int { return m.msgRowStarts[len(m.msgRowStarts)-1] }

func TestLivePostFollowsWithSelectionAboveBottom(t *testing.T) {
	m := liveTailModel(30)
	h := m.msgsView.Height()
	// Select a message three up: the bottom is still on screen.
	m.postIdx -= 3
	m.renderMessages()
	if m.msgsView.YOffset() != m.msgsTotalRows()-h {
		t.Fatal("setup: the bottom should be on screen")
	}
	sel := m.posts[m.postIdx].Id
	m.applyPosted(postedEvent(dmPost("new", 5000)))
	if got, want := m.msgsView.YOffset(), m.msgsTotalRows()-h; got != want {
		t.Fatalf("YOffset = %d, want %d: the new message must be on screen", got, want)
	}
	if m.posts[m.postIdx].Id != sel {
		t.Fatalf("selection moved to %q, want it left on %q", m.posts[m.postIdx].Id, sel)
	}
	if m.msgsFollowTail {
		t.Fatal("the follow flag is one-shot")
	}
}

func TestLivePostLeavesScrolledUpReader(t *testing.T) {
	m := liveTailModel(30)
	m.postIdx = 2
	m.renderMessages()
	before := m.msgsView.YOffset()
	if before >= m.msgsTotalRows()-m.msgsView.Height() {
		t.Fatal("setup: the reader should be scrolled up")
	}
	m.applyPosted(postedEvent(dmPost("new", 5000)))
	if got := m.msgsView.YOffset(); got != before {
		t.Fatalf("YOffset = %d, want %d: a reader scrolled up must not be yanked", got, before)
	}
}

// The selection sits on the first fully visible row and the bottom is on screen;
// a tall arrival can't be shown whole without scrolling the selection off, so
// the view stops with the selection at the top edge (and the pill takes over).
func TestLivePostNeverScrollsSelectionOff(t *testing.T) {
	m := liveTailModel(30)
	h := m.msgsView.Height()
	off := m.msgsView.YOffset()
	for i := range m.posts {
		if m.msgRowStarts[i] >= off {
			m.postIdx = i
			break
		}
	}
	m.renderMessages()
	if m.msgsView.YOffset() != off {
		t.Fatal("setup: selecting the top visible post must not move the view")
	}
	big := dmPost("new", 5000)
	big.Message = tallPost("", 0, 30).Message
	m.applyPosted(postedEvent(big))
	if got, want := m.msgsView.YOffset(), m.msgRowStarts[m.postIdx]; got != want {
		t.Fatalf("YOffset = %d, want %d (the selection's top row)", got, want)
	}
	if m.msgsView.YOffset()+h >= m.msgsTotalRows() {
		t.Fatal("the tall arrival should be under the fold, not the selection")
	}
}

func TestLivePostFollowsWheelParkedAtBottom(t *testing.T) {
	m := liveTailModel(30)
	h := m.msgsView.Height()
	m.msgScrollFree = true
	m.msgFreeOffset = m.msgsView.YOffset()
	m.applyPosted(postedEvent(dmPost("new", 5000)))
	want := m.msgsTotalRows() - h
	if got := m.msgsView.YOffset(); got != want {
		t.Fatalf("YOffset = %d, want %d: a wheel parked at the bottom follows new posts", got, want)
	}
	if m.msgFreeOffset != want {
		t.Fatalf("msgFreeOffset = %d, want %d so the next render keeps the bottom", m.msgFreeOffset, want)
	}
}

func TestLivePostLeavesWheelScrolledUp(t *testing.T) {
	m := liveTailModel(30)
	m.msgScrollFree = true
	m.msgFreeOffset = 5
	m.msgsView.SetYOffset(5)
	m.applyPosted(postedEvent(dmPost("new", 5000)))
	if got := m.msgsView.YOffset(); got != 5 {
		t.Fatalf("YOffset = %d, want 5: a wheel scrolled up must not be yanked", got)
	}
}
