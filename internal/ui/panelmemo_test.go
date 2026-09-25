package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestPanelFollowsChannel: a thread open in one channel goes away when another
// channel opens and comes back, on the same reply, when the first reopens.
func TestPanelFollowsChannel(t *testing.T) {
	root := &model.Post{Id: "r", ChannelId: "c", UserId: "u", CreateAt: 100, Message: "root"}
	reply := &model.Post{Id: "x", ChannelId: "c", RootId: "r", UserId: "u", CreateAt: 200, Message: "reply"}
	m := mouseModel([]*model.Post{root})
	m.showThread("c", "r", "", "", []*model.Post{root, reply})
	m.threadIdx = 0

	m.enterChannel("c2", "sidebar_key")
	if m.threadOpen {
		t.Fatal("thread still open after switching to another channel")
	}
	if ch, _ := m.composerTarget(); ch != "c2" {
		t.Errorf("composer targets %q, want c2", ch)
	}

	m.enterChannel("c", "sidebar_key")
	if !m.threadOpen || m.threadRootID != "r" || m.threadChannelID != "c" {
		t.Fatalf("thread not restored: open=%v root=%q ch=%q", m.threadOpen, m.threadRootID, m.threadChannelID)
	}
	if m.threadIdx != 0 {
		t.Errorf("threadIdx = %d, want 0 (the reply the cursor was on)", m.threadIdx)
	}

	// Closed by hand, it stays closed.
	m.closeThread()
	m.enterChannel("c2", "sidebar_key")
	m.enterChannel("c", "sidebar_key")
	if m.threadOpen {
		t.Error("closed thread came back")
	}
}

// TestPanelSwapsBetweenChannels: each channel keeps its own panel.
func TestPanelSwapsBetweenChannels(t *testing.T) {
	m := mouseModel(nil)
	m.showThread("c", "r", "", "", nil)
	m.enterChannel("c2", "sidebar_key")
	m.raiseChannelInfo()
	if !m.infoOpen || m.infoChannelID != "c2" {
		t.Fatal("info panel did not open on c2")
	}

	m.enterChannel("c", "sidebar_key")
	if m.infoOpen || !m.threadOpen {
		t.Fatalf("back on c: info=%v thread=%v, want thread only", m.infoOpen, m.threadOpen)
	}
	m.enterChannel("c2", "sidebar_key")
	if m.threadOpen || !m.infoOpen || m.infoChannelID != "c2" {
		t.Fatalf("back on c2: info=%v thread=%v, want info only", m.infoOpen, m.threadOpen)
	}
}
