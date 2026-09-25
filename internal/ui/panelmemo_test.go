package ui

import (
	"strconv"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/jira"
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

// TestParkedRefKeepsContent: a loaded reference panel comes back without
// refetching.
func TestParkedRefKeepsContent(t *testing.T) {
	m := mouseModel(nil)
	issue := &jira.Issue{Key: "ABC-1"}
	m.refOpen, m.refChannelID = true, "c"
	m.refs = []reference{{kind: refJira, jiraKey: "ABC-1"}}
	m.jiraIssue = issue

	m.enterChannel("c2", "sidebar_key")
	if m.refOpen {
		t.Fatal("ref panel still open on c2")
	}
	m.enterChannel("c", "sidebar_key")
	if !m.refOpen || m.jiraIssue != issue || m.refLoading {
		t.Fatalf("ref not restored as loaded: open=%v issue=%v loading=%v", m.refOpen, m.jiraIssue, m.refLoading)
	}
}

// TestParkedPanelsCapped: past maxParkedPanels the oldest is forgotten.
func TestParkedPanelsCapped(t *testing.T) {
	m := mouseModel(nil)
	for i := range maxParkedPanels + 1 {
		m.rememberPanel("ch"+strconv.Itoa(i), panelMemo{kind: panelInfo})
	}
	if len(m.channelPanels) != maxParkedPanels {
		t.Fatalf("parked %d, want %d", len(m.channelPanels), maxParkedPanels)
	}
	if _, ok := m.channelPanels["ch0"]; ok {
		t.Error("oldest parked panel kept")
	}
	// Re-parking refreshes recency.
	m.rememberPanel("ch1", panelMemo{kind: panelInfo})
	m.rememberPanel("new", panelMemo{kind: panelInfo})
	if _, ok := m.channelPanels["ch1"]; !ok {
		t.Error("recently re-parked panel dropped")
	}
}
