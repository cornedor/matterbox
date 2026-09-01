package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// unreadModel is a renderable channel "c" with three posts, fully read, plus
// the channel/member state the mark-unread path rewinds.
func unreadModel() Model {
	posts := []*model.Post{
		pPost("a", 1000, "other"),
		pPost("b", 2000, "other"),
		pPost("c", 3000, "other"),
	}
	m := pagingModel(posts, len(posts)-1)
	m.me = &model.User{Id: "me"}
	m.viewGen = 1
	m.viewSettled = true
	m.markReadDelay = 5 * time.Second
	m.hasDMs = true
	m.unread = map[string]int{}
	m.mentions = map[string]int{}
	m.channels = map[string][]*model.Channel{
		"t1": {{Id: "c", Name: "general", DisplayName: "General", TotalMsgCount: 3, TotalMsgCountRoot: 3}},
	}
	m.members = []model.ChannelMemberWithTeamData{
		{ChannelMember: model.ChannelMember{ChannelId: "c", UserId: "me", MsgCount: 3, MsgCountRoot: 3, LastViewedAt: 3000}},
	}
	return m
}

// Marking the selected message unread rewinds the badge, the member's counters
// and the divider boundary, and holds the channel unread while it stays open.
func TestMarkPostUnreadRewindsLocalState(t *testing.T) {
	m := unreadModel()
	m.postIdx = 1 // the "b" post

	runMarkPostUnread(&m, "")

	if got := m.unread["c"]; got != 2 {
		t.Errorf("unread badge = %d, want 2 (posts b and c)", got)
	}
	if m.unreadBoundary != 1999 {
		t.Errorf("unreadBoundary = %d, want 1999", m.unreadBoundary)
	}
	if m.members[0].LastViewedAt != 1999 {
		t.Errorf("member LastViewedAt = %d, want 1999", m.members[0].LastViewedAt)
	}
	if m.members[0].MsgCount != 1 {
		t.Errorf("member MsgCount = %d, want 1 (3 total - 2 unread)", m.members[0].MsgCount)
	}
	if !m.markReadHeld("c") {
		t.Error("the channel must be held unread while it's open")
	}
	if m.viewSettled {
		t.Error("viewSettled must be cleared so a live post can't mark it read")
	}
	// The re-derive from m.members must agree with the badge we set.
	m.channelsLoaded, m.membersLoaded = true, true
	m.openChannelID = "" // not the exempt "currently reading" channel
	m.applyUnreadFromMembers()
	if got := m.unread["c"]; got != 2 {
		t.Errorf("after re-derive from members: unread = %d, want 2", got)
	}
}

// The hold is the whole point: neither the dwell, nor a live post, nor the
// terminal regaining focus may mark a hand-unread channel read again.
func TestMarkUnreadHoldSurvivesTheMarkReadPaths(t *testing.T) {
	m := unreadModel()
	runMarkPostUnread(&m, "")

	if cmd := m.scheduleMarkViewed("c"); cmd != nil {
		t.Error("scheduleMarkViewed must not re-arm the dwell for a held channel")
	}
	m.viewSettled = true // pretend the dwell had already completed
	if cmd := m.liveMarkRead("c"); cmd != nil {
		t.Error("a live post must not mark a held channel read")
	}
	m.termFocusKnown, m.termFocused = true, false
	if cmd := m.applyTerminalFocus(true); cmd != nil {
		t.Error("refocusing must not mark a held channel read")
	}
	if !isUnread(m, "c") {
		t.Error("the badge must still be there")
	}
	// A pending dwell tick queued before the mark-unread is stale by generation.
	next, cmd := m.update(markViewedMsg{channelID: "c", gen: 1})
	if cmd != nil || !isUnread(next.(Model), "c") {
		t.Error("an in-flight dwell tick must not clear a held channel")
	}
}

// Entering the channel again drops the hold, so a deliberate revisit reads it
// the normal way.
func TestEnterChannelDropsMarkUnreadHold(t *testing.T) {
	m := unreadModel()
	runMarkPostUnread(&m, "")
	m.enterChannel("c", "test")
	if m.markReadHeld("c") {
		t.Error("re-entering the channel must drop the hold")
	}
}

// The entry is offered while a conversation is open, carries a catalogued
// telemetry id (the palette counter drops anything else), and is absent with no
// channel open.
func TestMarkUnreadCommandListed(t *testing.T) {
	m := unreadModel()
	cmd, ok := m.markUnreadCommand()
	if !ok {
		t.Fatal("expected the mark-unread entry with a channel open")
	}
	if !strings.Contains(cmd.name, "message") {
		t.Errorf("entry name = %q, want it to name the message", cmd.name)
	}
	if cmd.tid == "" {
		t.Error("entry has no telemetry id")
	}
	m.openChannelID = ""
	if _, ok := m.markUnreadCommand(); ok {
		t.Error("no entry expected with no channel open")
	}
}

// Running the per-message entry with no selection reports it rather than
// touching the read state.
func TestMarkPostUnreadWithoutSelection(t *testing.T) {
	m := unreadModel()
	m.focus = focusInput // selectedPost() follows focus: nothing selected here
	if cmd := runMarkPostUnread(&m, ""); cmd != nil {
		t.Error("expected no command with no message selected")
	}
	if isUnread(m, "c") {
		t.Error("read state must be untouched")
	}
	if m.status != "no message selected" {
		t.Errorf("status = %q, want %q", m.status, "no message selected")
	}
}
