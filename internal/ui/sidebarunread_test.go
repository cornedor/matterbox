package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func newUnreadSidebarModel() Model {
	m := newRenderableModel()
	m.teams = []*model.Team{{Id: "t1"}}
	m.channels = map[string][]*model.Channel{"t1": {
		{Id: "c1", TeamId: "t1", DisplayName: "alpha"},
		{Id: "c2", TeamId: "t1", DisplayName: "beta"},
		{Id: "c3", TeamId: "t1", DisplayName: "gamma"},
		{Id: "c4", TeamId: "t1", DisplayName: "delta"},
	}}
	m.unread = map[string]int{"c1": 1, "c4": 2}
	m.mentions = map[string]int{}
	m.teamIdx = m.firstTeamTabIdx()
	return m
}

func visibleIDs(m *Model) string {
	vis := m.visibleChannels()
	ids := make([]string, len(vis))
	for i, c := range vis {
		ids[i] = c.Id
	}
	return strings.Join(ids, ",")
}

// Unread-only shows channels with unread activity plus the open one (which
// is read, and stays), in bucket order; the f filter still applies on top.
func TestSidebarUnreadOnlyList(t *testing.T) {
	m := newUnreadSidebarModel()
	m.openChannelID = "c3"
	if got := visibleIDs(&m); got != "c1,c2,c3,c4" {
		t.Fatalf("mode off: %s", got)
	}
	m.sidebarUnreadOnly = true
	if got := visibleIDs(&m); got != "c1,c3,c4" {
		t.Fatalf("mode on: %s, want unread c1,c4 plus open c3", got)
	}
	m.filterValue = "a"
	if got := visibleIDs(&m); got != "c1,c3,c4" { // alpha, gamma, delta all contain "a"
		t.Fatalf("mode on + filter a: %s", got)
	}
	m.filterValue = "gam"
	if got := visibleIDs(&m); got != "c3" {
		t.Fatalf("mode on + filter gam: %s", got)
	}
}

// Toggling re-points the cursor at the open channel's (moved) row, both ways;
// with the cursor past the end of the shrunken list nothing paints empty.
func TestSidebarUnreadOnlyToggleKeepsCursorOnOpen(t *testing.T) {
	m := newUnreadSidebarModel()
	m.openChannelID = "c3"
	m.channelIdx = 3 // on c4, below the open channel
	m.setSidebarUnreadOnly(true)
	if m.channelIdx != 1 { // c1,c3,c4 → c3 is row 1
		t.Fatalf("toggle on: channelIdx = %d, want 1 (c3)", m.channelIdx)
	}
	m.setSidebarUnreadOnly(false)
	if m.channelIdx != 2 { // c1,c2,c3,c4 → c3 is row 2
		t.Fatalf("toggle off: channelIdx = %d, want 2 (c3)", m.channelIdx)
	}
	// Open channel not in this tab's list at all → clamped, not out of range.
	m.openChannelID = "elsewhere"
	m.channelIdx = 3
	m.setSidebarUnreadOnly(true)
	if vis := m.visibleChannels(); m.channelIdx >= len(vis) {
		t.Fatalf("toggle on with foreign open channel: channelIdx %d past %d rows", m.channelIdx, len(vis))
	}
}

// Opening a channel while unread-only is on drops the previously open (read)
// channel from the list, so the rows above the cursor shift: enterChannel
// re-points the cursor at the newly open channel rather than leaving it one
// row off — a stale row is exactly how a keystroke lands on the wrong channel.
func TestSidebarUnreadOnlyEnterChannelSnapsCursor(t *testing.T) {
	m := newUnreadSidebarModel()
	m.openChannelID = "c3"
	m.setSidebarUnreadOnly(true) // list c1,c3,c4; cursor 1
	m.channelIdx = 2             // user moves ↓ to c4 and opens it
	m.enterChannel("c4", "sidebar_key")
	if got := visibleIDs(&m); got != "c1,c4" {
		t.Fatalf("after open: %s (c3 is read and no longer open)", got)
	}
	if m.channelIdx != 1 {
		t.Fatalf("channelIdx = %d, want 1 (c4's new row)", m.channelIdx)
	}
	// With the mode off enterChannel leaves the cursor alone (the plain
	// sidebar never re-derives its list).
	m.setSidebarUnreadOnly(false)
	m.channelIdx = 0
	m.enterChannel("c2", "sidebar_key")
	if m.channelIdx != 0 {
		t.Fatalf("mode off: enterChannel moved the cursor to %d", m.channelIdx)
	}
}

// When another session reads a channel above the cursor the narrowed list
// shrinks under it; the sidebar render clamps the cursor to the last row
// instead of scrolling an empty pane. Off, the render never touches it.
func TestSidebarUnreadOnlyRenderClampsCursor(t *testing.T) {
	m := newUnreadSidebarModel()
	m.openChannelID = "c3"
	m.width, m.height = 100, 30
	m.layoutPanes()
	m.channelIdx = 9
	m.renderChannelsPane(20)
	if m.channelIdx != 9 {
		t.Fatalf("mode off: render moved the cursor to %d", m.channelIdx)
	}
	m.sidebarUnreadOnly = true // list c1,c3,c4
	m.channelIdx = 2
	delete(m.unread, "c4") // read elsewhere → c1,c3
	pane := m.renderChannelsPane(20)
	if m.channelIdx != 1 {
		t.Fatalf("after c4 read: channelIdx = %d, want 1 (last row)", m.channelIdx)
	}
	if !strings.Contains(pane, "alpha") || !strings.Contains(pane, "gamma") {
		t.Fatalf("pane should still list the remaining rows:\n%s", pane)
	}
}

// Switching tabs while narrowed lands on the team's remembered channel even
// though it is read (and so not in the narrowed list), and the cursor ends
// up on that channel's row — never a blank pane with a stale openChannelID.
func TestSidebarUnreadOnlyTabLanding(t *testing.T) {
	m := newUnreadSidebarModel()
	m.teams = []*model.Team{{Id: "t1"}, {Id: "t2"}}
	m.channels["t2"] = []*model.Channel{
		{Id: "d1", TeamId: "t2", DisplayName: "one"},
		{Id: "d2", TeamId: "t2", DisplayName: "two"},
	}
	m.lastChannelByTeam = map[string]string{"t2": "d2"}
	m.openChannelID = "c3"
	m.setSidebarUnreadOnly(true)
	next, _ := m.gotoTab(m.firstTeamTabIdx() + 1)
	got := next.(Model)
	if got.openChannelID != "d2" {
		t.Fatalf("landed on %q, want the remembered d2 (read, but still the tab's channel)", got.openChannelID)
	}
	if vis := got.visibleChannels(); len(vis) != 1 || vis[0].Id != "d2" || got.channelIdx != 0 {
		t.Fatalf("after landing: visible=%v channelIdx=%d, want [d2] 0", visibleIDs(&got), got.channelIdx)
	}
	if got.status == "no channels in this team" {
		t.Fatal("a team with channels must not report itself empty because they are read")
	}
}

// The header says the list is filtered, in the f <filter> convention.
func TestSidebarUnreadOnlyHeader(t *testing.T) {
	m := newUnreadSidebarModel()
	m.openChannelID = "c3"
	m.width, m.height = 100, 30
	m.layoutPanes()
	if pane := m.renderChannelsPane(20); !strings.Contains(pane, "Channels") || strings.Contains(pane, "f unread") {
		t.Fatalf("mode off header: %q", firstLine(pane))
	}
	m.sidebarUnreadOnly = true
	if pane := m.renderChannelsPane(20); !strings.Contains(pane, "f unread") {
		t.Fatalf("mode on header: %q", firstLine(pane))
	}
	m.filterValue = "gam"
	if pane := m.renderChannelsPane(20); !strings.Contains(pane, "f unread gam") {
		t.Fatalf("mode on + filter header: %q", firstLine(pane))
	}
}
