package ui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
)

// Marking read follows terminal focus: a blurred terminal must not bump
// LastViewedAt, because that would tell every other client (and the listen
// daemon) you read a message you never saw. But the message is then unread on
// the server, so it has to be unread here too. Nothing else re-derives the open
// channel's badge — applyUnreadFromMembers skips it and buildFeed reads
// m.unread — so a post dropped here is invisible until the app restarts.

// blurredDMModel has the DM open and on screen (a channel tab, not the Feed),
// its dwell long completed, and the terminal blurred: the user is in another
// window when the message lands.
func blurredDMModel() Model {
	mvp := viewport.New()
	mvp.SoftWrap = true
	mvp.SetWidth(80)
	mvp.SetHeight(40)
	fp := newFeedState(false, 0)
	fp.view.SetWidth(76)
	fp.view.SetHeight(30)
	return Model{
		keys:           newKeyMap("ctrl"),
		me:             &model.User{Id: "me"},
		userNames:      map[string]string{"me": "me", "u2": "frank"},
		drafts:         map[string]string{},
		unread:         map[string]int{},
		mentions:       map[string]int{},
		markReadDelay:  time.Second,
		openChannelID:  "dm",
		viewSettled:    true, // its dwell completed while the user was here
		termFocusKnown: true,
		termFocused:    false, // ... and then they switched windows
		hasDMs:         true,  // tab 0 is the DMs tab, so the DM is on screen
		feed:           fp,
		msgsView:       mvp,
		channels: map[string][]*model.Channel{
			dmTeamID: {{Id: "dm", Type: model.ChannelTypeDirect, Name: "me__u2"}},
		},
		width:  100,
		height: 44,
	}
}

func mentionEvent(p *model.Post, userIDs ...string) *model.WebSocketEvent {
	ev := postedEvent(p)
	ids, _ := json.Marshal(userIDs)
	ev.Add("mentions", string(ids))
	return ev
}

// The headline case. The post is on screen but nothing told the server we read
// it, so the badge has to say so.
func TestBlurredOpenChannelCountsUnread(t *testing.T) {
	m := blurredDMModel()
	if !m.isCurrentChannel("dm") {
		t.Fatal("setup: the DM should be the on-screen channel")
	}
	if m.liveMarkRead("dm") != nil {
		t.Fatal("setup: a blurred terminal must not mark read")
	}

	m.applyPosted(postedEvent(dmPost("p1", 2000)))

	if m.unread["dm"] != 1 {
		t.Errorf("unread = %d; want 1 (unread on the server, so unread here)", m.unread["dm"])
	}
	if len(m.posts) != 1 {
		t.Errorf("transcript holds %d posts; want 1 (the message is still on screen)", len(m.posts))
	}
}

// Mentions travel the same path and drive the same surfaces (feed tint, sidebar
// colour, the switcher's attention rank), so they must be counted too.
func TestBlurredOpenChannelCountsMention(t *testing.T) {
	m := blurredDMModel()

	m.applyPosted(mentionEvent(dmPost("p1", 2000), "me"))

	if m.mentions["dm"] != 1 {
		t.Errorf("mentions = %d; want 1", m.mentions["dm"])
	}
}

// The badge and the feed are two views of the same fact, so a post counted as
// unread belongs in the feed as well — otherwise the Feed tab badge counts a
// channel with no bubble, which is the mismatch this whole class of bug shows up
// as.
func TestBlurredOpenChannelGetsFeedBubble(t *testing.T) {
	m := blurredDMModel()
	m.feed.built = true

	m.applyPosted(postedEvent(dmPost("p1", 2000)))

	if n := entriesFor(m, "dm"); n != 1 {
		t.Errorf("dm bubbles = %d; want 1 (badge says %d unread)", n, m.unread["dm"])
	}
}

// Coming back to the terminal marks the channel read, so the badge and bubble
// the blurred arrival created have to go with it — the local half of the same
// decision, and the only thing that clears them for an on-screen channel.
func TestFocusReturnClearsBadgeAndBubble(t *testing.T) {
	m := blurredDMModel()
	m.feed.built = true
	m.applyPosted(postedEvent(dmPost("p1", 2000)))
	if m.unread["dm"] == 0 {
		t.Fatal("setup: the blurred arrival should have left a badge to clear")
	}

	cmd := m.applyTerminalFocus(true)

	if cmd == nil {
		t.Error("focus returned without marking the channel read")
	}
	if n, ok := m.unread["dm"]; ok {
		t.Errorf("unread = %d after refocus; want the badge cleared", n)
	}
	if n, ok := m.mentions["dm"]; ok {
		t.Errorf("mentions = %d after refocus; want cleared", n)
	}
	if n := entriesFor(m, "dm"); n != 0 {
		t.Errorf("dm bubbles = %d after refocus; want 0", n)
	}
}

// The counterpart: while the terminal has focus the post IS marked read, so no
// badge should appear for the conversation being read. This is the behaviour the
// blurred case must not break.
func TestFocusedOpenChannelStaysRead(t *testing.T) {
	m := blurredDMModel()
	m.termFocused = true
	m.feed.built = true
	if m.liveMarkRead("dm") == nil {
		t.Fatal("setup: a focused terminal marks read")
	}

	m.applyPosted(postedEvent(dmPost("p1", 2000)))

	if n, ok := m.unread["dm"]; ok {
		t.Errorf("unread = %d; want no badge on the channel being read", n)
	}
	if n := entriesFor(m, "dm"); n != 0 {
		t.Errorf("dm bubbles = %d; want 0 for a channel being read", n)
	}
}

// A terminal that never reports focus counts as focused (the pre-focus-report
// behaviour), so it keeps marking read and must not start collecting badges for
// the channel on screen.
func TestSilentTerminalStaysRead(t *testing.T) {
	m := blurredDMModel()
	m.termFocusKnown = false // e.g. tmux without focus-events
	m.termFocused = false

	m.applyPosted(postedEvent(dmPost("p1", 2000)))

	if n, ok := m.unread["dm"]; ok {
		t.Errorf("unread = %d; want no badge when focus is unknowable", n)
	}
}

// A dwell that hasn't elapsed yet is not the same as a suppressed mark-read:
// the queued markViewedMsg will cover this post as well, so flashing a badge
// (and a feed bubble) for the second in between is noise.
func TestPendingDwellOnFocusedChannelStaysRead(t *testing.T) {
	m := blurredDMModel()
	m.termFocused = true
	m.viewSettled = false // opened a moment ago; the dwell is still armed
	m.feed.built = true

	m.applyPosted(postedEvent(dmPost("p1", 2000)))

	if n, ok := m.unread["dm"]; ok {
		t.Errorf("unread = %d; want no badge while the dwell is pending", n)
	}
	if n := entriesFor(m, "dm"); n != 0 {
		t.Errorf("dm bubbles = %d; want 0 while the dwell is pending", n)
	}
}
