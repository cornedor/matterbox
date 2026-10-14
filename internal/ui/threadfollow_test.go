package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// The follow toggle is listed only once the open thread's state is known, its
// label follows that state, and the pane title marks a followed thread.
func TestFollowThreadCommand(t *testing.T) {
	m := newRenderableModel()
	if _, ok := m.followThreadCommand(); ok {
		t.Fatal("listed with no thread open")
	}
	m.threadOpen, m.threadRootID = true, "r1"
	if _, ok := m.followThreadCommand(); ok {
		t.Fatal("listed before the follow state is known")
	}

	m.applyThreadFollow(threadFollowMsg{rootID: "r1", following: true})
	if c, ok := m.followThreadCommand(); !ok || c.name != "Unfollow thread" {
		t.Fatalf("followed: command = %q ok=%v", c.name, ok)
	}
	if title := m.renderThreadPane(30, 40); !strings.Contains(title, "following") {
		t.Error("followed thread: title has no following mark")
	}

	m.applyThreadFollow(threadFollowMsg{rootID: "r1", following: false})
	if c, _ := m.followThreadCommand(); c.name != "Follow thread" {
		t.Fatalf("not followed: command = %q", c.name)
	}
	if title := m.renderThreadPane(30, 40); strings.Contains(title, "following") {
		t.Error("unfollowed thread: title still marked following")
	}
}

// A lookup for a thread that is no longer open is dropped; a failed toggle
// reverts the optimistic flip; another client's follow change lands live.
func TestThreadFollowUpdates(t *testing.T) {
	m := newRenderableModel()
	m.threadOpen, m.threadRootID = true, "r1"

	m.applyThreadFollow(threadFollowMsg{rootID: "old", following: true})
	if m.threadFollowKnown {
		t.Fatal("stale lookup applied to the open thread")
	}

	m.applyThreadFollow(threadFollowMsg{rootID: "r1", following: true})
	m.threadFollowing = false // runToggleThreadFollow's optimistic unfollow
	m.applyThreadFollow(threadFollowMsg{rootID: "r1", following: false, toggled: true, err: errors.New("boom")})
	if !m.threadFollowing || m.status != "boom" {
		t.Fatalf("failed unfollow: following=%v status=%q, want reverted + error", m.threadFollowing, m.status)
	}

	ev := model.NewWebSocketEvent(model.WebsocketEventThreadFollowChanged, "t1", "", "me", nil, "")
	ev.Add("thread_id", "r1")
	ev.Add("state", false)
	m.handleWSEvent(ev)
	if m.threadFollowing {
		t.Error("thread_follow_changed false: still following")
	}
}
