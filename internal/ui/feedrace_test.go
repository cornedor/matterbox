package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// A feed rebuild is a round trip: buildFeed snapshots the unread set on the UI
// goroutine, then a worker fetches every channel's posts. A `posted` event that
// lands in between has to survive the snapshot arriving on top of it — the live
// append is the only record of it. Losing it leaves the tab badge counting a
// channel the pane doesn't show, which is the reported bug: a DM notification
// fired, the Feed had other bubbles, that DM was missing.

// feedRaceModel is a painted Feed tab with one bubble already in it (c0), a DM
// nothing knows about yet, and a rebuild in flight.
func feedRaceModel() Model {
	m := feedMouseModel(1)
	m.channels[dmTeamID] = []*model.Channel{{Id: "dm", Type: model.ChannelTypeDirect, Name: "me__u2"}}
	m.userNames["u2"] = "frank"
	m.buildFeed() // takes the snapshot; the returned fetch cmd is never run
	return m
}

// entriesFor counts the bubbles the feed holds for one channel. More than one
// is as wrong as none.
func entriesFor(m Model, channelID string) int {
	n := 0
	for _, e := range m.feed.entries {
		if e.channelID == channelID {
			n++
		}
	}
	return n
}

func dmPost(id string, at int64) *model.Post {
	return &model.Post{Id: id, ChannelId: "dm", UserId: "u2", CreateAt: at, Message: "yo"}
}

// The headline case: a DM arrives while the rebuild is fetching. The build's
// snapshot predates it, so installing that snapshot must not drop the bubble
// the live event added.
func TestFeedRebuildKeepsLivePost(t *testing.T) {
	m := feedRaceModel()
	snapshot := append([]feedEntry(nil), m.feed.entries...)

	m.applyPosted(postedEvent(dmPost("p-dm", 900)))
	if m.unread["dm"] != 1 {
		t.Fatalf("setup: live post did not bump the badge (unread = %d)", m.unread["dm"])
	}

	out, _ := m.applyFeedResults(feedLoadedMsg{seq: m.feed.seq, entries: snapshot})
	got := out.(Model)

	if n := entriesFor(got, "dm"); n != 1 {
		t.Errorf("dm bubbles = %d; want 1 (the badge says %d unread, so the pane has to show it)", n, got.unread["dm"])
	}
	if n := entriesFor(got, "c0"); n != 1 {
		t.Errorf("c0 bubbles = %d; want 1 (the rebuild lost what it did fetch)", n)
	}
}

// The same race on the very first build, when there is no feed to append to
// yet: the post arrives before feed.built, so nothing records it at all.
func TestFeedRebuildKeepsLivePostBeforeFirstBuild(t *testing.T) {
	m := feedRaceModel()
	m.feed.built = false // this session's first build, still in flight
	m.feed.entries = nil

	m.applyPosted(postedEvent(dmPost("p-dm", 900)))

	out, _ := m.applyFeedResults(feedLoadedMsg{seq: m.feed.seq})
	got := out.(Model)

	if n := entriesFor(got, "dm"); n != 1 {
		t.Errorf("dm bubbles = %d; want 1 (a post that beat the first build is still unread)", n)
	}
}

// A post that is deleted again before the build lands must not come back with
// it — replaying a live append cannot resurrect a message.
func TestFeedRebuildDropsDeletedPendingPost(t *testing.T) {
	m := feedRaceModel()
	snapshot := append([]feedEntry(nil), m.feed.entries...)

	m.applyPosted(postedEvent(dmPost("p-dm", 900)))
	m.feedRemovePost("p-dm") // sender deleted it while the build was fetching

	out, _ := m.applyFeedResults(feedLoadedMsg{seq: m.feed.seq, entries: snapshot})
	got := out.(Model)

	if n := entriesFor(got, "dm"); n != 0 {
		t.Errorf("dm bubbles = %d; want 0 (the post was deleted before the build landed)", n)
	}
}

// The usual outcome of the race is that the worker's PostsSince already
// returned the post: the snapshot and the live append describe the same
// message, and the feed must show it once.
func TestFeedRebuildNoDuplicateWhenSnapshotIncludesPost(t *testing.T) {
	m := feedRaceModel()
	p := dmPost("p-dm", 900)
	snapshot := append([]feedEntry(nil), m.feed.entries...)

	m.applyPosted(postedEvent(p))
	snapshot = append(snapshot, feedEntry{channelID: "dm", unread: []*model.Post{p}})

	out, _ := m.applyFeedResults(feedLoadedMsg{seq: m.feed.seq, entries: snapshot})
	got := out.(Model)

	if n := entriesFor(got, "dm"); n != 1 {
		t.Fatalf("dm bubbles = %d; want exactly 1", n)
	}
	for _, e := range got.feed.entries {
		if e.channelID == "dm" && len(e.unread) != 1 {
			t.Errorf("dm bubble holds %d unread posts; want 1 (the post was counted twice)", len(e.unread))
		}
	}
}

// A build whose seq no longer matches is discarded wholesale, so the live
// append it would have clobbered stays where it is.
func TestStaleFeedBuildLeavesLivePostAlone(t *testing.T) {
	m := feedRaceModel()
	m.applyPosted(postedEvent(dmPost("p-dm", 900)))

	out, _ := m.applyFeedResults(feedLoadedMsg{seq: m.feed.seq - 1})
	got := out.(Model)

	if n := entriesFor(got, "dm"); n != 1 {
		t.Errorf("dm bubbles = %d; want 1 (a stale build must change nothing)", n)
	}
}

// The reported flow, end to end: you send something in a DM, jump to the Feed
// (which starts a build), and the reply lands while that build is fetching. The
// DM is still openChannelID — but it is behind the Feed tab, so the reply is
// unread, and the bubble has to be there when the build lands next to the
// channels the build did fetch.
func TestReplyArrivingOnFeedAfterLeavingDMShowsUp(t *testing.T) {
	m := feedMouseModel(1)
	m.channels[dmTeamID] = []*model.Channel{{Id: "dm", Type: model.ChannelTypeDirect, Name: "me__u2"}}
	m.userNames["u2"] = "frank"
	m.openChannelID = "dm" // read a moment ago, then we jumped to the Feed
	m.viewSettled = true
	if !m.onFeedTab() || m.isCurrentChannel("dm") {
		t.Fatal("setup: the DM should linger as openChannelID behind the Feed tab")
	}
	m.buildFeed() // arriving on the Feed tab kicks a rebuild
	snapshot := append([]feedEntry(nil), m.feed.entries...)

	m.applyPosted(postedEvent(dmPost("p-reply", 900)))
	out, _ := m.applyFeedResults(feedLoadedMsg{seq: m.feed.seq, entries: snapshot})
	got := out.(Model)

	if got.unread["dm"] != 1 {
		t.Errorf("unread = %d; want 1", got.unread["dm"])
	}
	if n := entriesFor(got, "dm"); n != 1 {
		t.Errorf("dm bubbles = %d; want 1 — the notification fired, the Feed showed nothing", n)
	}
}
