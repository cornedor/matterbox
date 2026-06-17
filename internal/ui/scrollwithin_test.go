package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// tallPost builds a post whose body is `lines` short lines, so it renders
// taller than the test viewport (height 40) and exercises intra-message
// scrolling.
func tallPost(id string, createAt int64, lines int) *model.Post {
	return &model.Post{
		Id:       id,
		CreateAt: createAt,
		UserId:   "u",
		Message:  strings.TrimRight(strings.Repeat("line\n", lines), "\n"),
	}
}

// scrollModel is pagingModel with the keymap wired up so handleMessagesKey's
// key.Matches calls resolve.
func scrollModel(posts []*model.Post, postIdx int) Model {
	m := pagingModel(posts, postIdx)
	m.keys = newKeyMap("ctrl")
	return m
}

// TestScrollWithinTallPostDown: ↓ on a post taller than the pane scrolls inside
// it one row at a time and only steps to the next post once pinned to its
// bottom edge.
func TestScrollWithinTallPostDown(t *testing.T) {
	m := scrollModel([]*model.Post{p("a", 100), tallPost("big", 200, 80), p("c", 300)}, 1)
	m.anchorMsgSelTop = true // open the tall post at its top
	m.renderMessages()

	h := m.msgsView.Height()
	visStart := m.msgRowStarts[1]
	visEnd := m.msgRowStarts[2]
	if visEnd-visStart <= h {
		t.Fatalf("post not tall enough: %d rows, pane %d", visEnd-visStart, h)
	}
	if got := m.msgsView.YOffset(); got != visStart {
		t.Fatalf("anchor-top failed: YOffset=%d want %d", got, visStart)
	}

	maxOff := visEnd - h
	for off := visStart; off < maxOff; off++ {
		out, _ := m.handleMessagesKey(keyPress(tea.KeyDown))
		m = out.(Model)
		if m.postIdx != 1 {
			t.Fatalf("selection left the tall post early at YOffset %d (idx=%d)", off, m.postIdx)
		}
		if got := m.msgsView.YOffset(); got != off+1 {
			t.Fatalf("↓ scroll step: YOffset=%d want %d", got, off+1)
		}
	}
	if got := m.msgsView.YOffset(); got != maxOff {
		t.Fatalf("not pinned to bottom: YOffset=%d want %d", got, maxOff)
	}

	// At the bottom edge, ↓ finally advances the selection.
	out, _ := m.handleMessagesKey(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.postIdx != 2 {
		t.Fatalf("↓ at bottom edge didn't advance selection: idx=%d", m.postIdx)
	}
}

// TestScrollWithinTallPostUp is the mirror: ↑ scrolls up inside the post and
// only steps to the previous post once pinned to its top edge.
func TestScrollWithinTallPostUp(t *testing.T) {
	m := scrollModel([]*model.Post{p("a", 100), tallPost("big", 200, 80), p("c", 300)}, 1)
	m.anchorMsgSelBottom = true // open the tall post at its bottom
	m.renderMessages()

	h := m.msgsView.Height()
	visStart := m.msgRowStarts[1]
	visEnd := m.msgRowStarts[2]
	maxOff := visEnd - h
	if got := m.msgsView.YOffset(); got != maxOff {
		t.Fatalf("anchor-bottom failed: YOffset=%d want %d", got, maxOff)
	}

	for off := maxOff; off > visStart; off-- {
		out, _ := m.handleMessagesKey(keyPress(tea.KeyUp))
		m = out.(Model)
		if m.postIdx != 1 {
			t.Fatalf("selection left the tall post early at YOffset %d (idx=%d)", off, m.postIdx)
		}
		if got := m.msgsView.YOffset(); got != off-1 {
			t.Fatalf("↑ scroll step: YOffset=%d want %d", got, off-1)
		}
	}
	if got := m.msgsView.YOffset(); got != visStart {
		t.Fatalf("not pinned to top: YOffset=%d want %d", got, visStart)
	}

	// At the top edge, ↑ finally advances the selection to the previous post.
	out, _ := m.handleMessagesKey(keyPress(tea.KeyUp))
	m = out.(Model)
	if m.postIdx != 0 {
		t.Fatalf("↑ at top edge didn't advance selection: idx=%d", m.postIdx)
	}
}

// TestShortPostMovesSelection: a post that fits the pane is unchanged — ↓ moves
// the selection straight to the neighbour, no intra-scroll.
func TestShortPostMovesSelection(t *testing.T) {
	m := scrollModel([]*model.Post{p("a", 100), p("b", 200), p("c", 300)}, 0)
	m.renderMessages()

	out, _ := m.handleMessagesKey(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.postIdx != 1 {
		t.Fatalf("↓ on a short post: idx=%d want 1", m.postIdx)
	}
}

// TestLandOnTallPostAnchorsTop: stepping down onto a tall post opens it at its
// top (so it reads top-down) rather than the default bottom-align.
func TestLandOnTallPostAnchorsTop(t *testing.T) {
	m := scrollModel([]*model.Post{p("a", 100), tallPost("big", 200, 80), p("c", 300)}, 0)
	m.renderMessages()

	out, _ := m.handleMessagesKey(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.postIdx != 1 {
		t.Fatalf("↓ didn't select the tall post: idx=%d", m.postIdx)
	}
	if got, want := m.msgsView.YOffset(), m.msgRowStarts[1]; got != want {
		t.Fatalf("landed on tall post not anchored to top: YOffset=%d want %d", got, want)
	}
}

// TestLandOnTallPostAnchorsBottom: stepping up onto a tall post opens it at its
// bottom (the edge adjacent to where the cursor came from).
func TestLandOnTallPostAnchorsBottom(t *testing.T) {
	m := scrollModel([]*model.Post{p("a", 100), tallPost("big", 200, 80), p("c", 300)}, 2)
	m.renderMessages()

	out, _ := m.handleMessagesKey(keyPress(tea.KeyUp))
	m = out.(Model)
	if m.postIdx != 1 {
		t.Fatalf("↑ didn't select the tall post: idx=%d", m.postIdx)
	}
	h := m.msgsView.Height()
	if got, want := m.msgsView.YOffset(), m.msgRowStarts[2]-h; got != want {
		t.Fatalf("landed on tall post not anchored to bottom: YOffset=%d want %d", got, want)
	}
}
