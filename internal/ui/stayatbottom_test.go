package ui

import (
	"encoding/json"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// reactionEvent builds the `reaction_added` WS event the server broadcasts when
// someone reacts, payload and all, so these tests drive the real applier.
func reactionEvent(postID, userID, emoji string) *model.WebSocketEvent {
	ev := model.NewWebSocketEvent(model.WebsocketEventReactionAdded, "", "c", "", nil, "")
	raw, _ := json.Marshal(model.Reaction{UserId: userID, PostId: postID, EmojiName: emoji})
	ev.Add("reaction", string(raw))
	return ev
}

// bottomModel is a laid-out channel parked on its newest message.
func bottomModel(t *testing.T) Model {
	t.Helper()
	m := mouseModel(shortPosts(80))
	m.resizeMessagesViewport()
	m.selectLastMessage()
	m.renderMessages()
	if m.msgsMoreBelow() {
		t.Fatal("setup: transcript isn't at the bottom")
	}
	return m
}

// TestReactionOnLastPostKeepsBottomWheel: parked on the newest message after a
// wheel scroll, a reaction landing on that message grows it by its chip row —
// which must not slide the message under the fold and raise the jump pill.
func TestReactionOnLastPostKeepsBottomWheel(t *testing.T) {
	m := bottomModel(t)
	// Park there the way the wheel does: sticky free-scroll on an absolute row.
	m.msgScrollFree = true
	m.msgFreeOffset = m.msgsView.YOffset()
	m.renderMessages()

	before := m.msgRowStarts[len(m.msgRowStarts)-1]
	m.handleWSEvent(reactionEvent(m.posts[len(m.posts)-1].Id, "u2", "+1"))
	if grew := m.msgRowStarts[len(m.msgRowStarts)-1] - before; grew <= 0 {
		t.Fatalf("setup: the reaction didn't grow the transcript (%d rows)", grew)
	}
	if m.msgsMoreBelow() {
		t.Error("the jump pill appeared on a transcript the reader never left")
	}
	if m.msgFreeOffset != m.msgsView.YOffset() {
		t.Errorf("free-scroll offset %d left behind the pinned offset %d",
			m.msgFreeOffset, m.msgsView.YOffset())
	}
}

// TestReactionOnLastPostKeepsBottomSelectionAbove: same, with the selection left
// on an earlier message (a click, or the composer holding focus) so the
// keep-the-selection-visible rule has no reason to re-anchor.
func TestReactionOnLastPostKeepsBottomSelectionAbove(t *testing.T) {
	m := bottomModel(t)
	m.postIdx = len(m.posts) - 3
	m.renderMessages()
	if m.msgsMoreBelow() {
		t.Fatal("setup: selecting a visible post scrolled away from the bottom")
	}

	m.handleWSEvent(reactionEvent(m.posts[len(m.posts)-1].Id, "u2", "+1"))
	if m.msgsMoreBelow() {
		t.Error("the jump pill appeared on a transcript the reader never left")
	}
}

// TestStayAtBottomYieldsToKeyScroll: the bottom pin only covers a reader who
// hasn't moved — ↑ from the bottom still scrolls up, and a reaction arriving
// afterwards doesn't yank the view back down.
func TestStayAtBottomYieldsToKeyScroll(t *testing.T) {
	m := bottomModel(t)
	off := m.msgsView.YOffset()
	for range 40 { // past the top edge, so the offset has to follow
		out, _ := m.handleMessagesKey(keyPress(tea.KeyUp))
		m = out.(Model)
	}
	if m.msgsView.YOffset() >= off {
		t.Fatalf("↑ didn't scroll up: YOffset=%d, was %d", m.msgsView.YOffset(), off)
	}
	scrolled := m.msgsView.YOffset()

	m.handleWSEvent(reactionEvent(m.posts[len(m.posts)-1].Id, "u2", "+1"))
	if got := m.msgsView.YOffset(); got != scrolled {
		t.Errorf("a reaction moved a scrolled-up transcript: YOffset=%d want %d", got, scrolled)
	}
}

// TestStayAtBottomYieldsToWheelUp: the wheel's mirror of the above.
func TestStayAtBottomYieldsToWheelUp(t *testing.T) {
	m := bottomModel(t)
	off := m.msgsView.YOffset()
	m = wheelOnce(m, tea.MouseWheelUp)
	if m.msgsView.YOffset() >= off {
		t.Fatalf("wheel didn't scroll up: YOffset=%d, was %d", m.msgsView.YOffset(), off)
	}
	scrolled := m.msgsView.YOffset()

	m.handleWSEvent(reactionEvent(m.posts[len(m.posts)-1].Id, "u2", "+1"))
	if got := m.msgsView.YOffset(); got != scrolled {
		t.Errorf("a reaction moved a wheel-scrolled transcript: YOffset=%d want %d", got, scrolled)
	}
}
