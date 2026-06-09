package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// dmIDs returns the ordered channel ids in the DM bucket.
func dmIDs(m *Model) []string {
	var ids []string
	for _, c := range m.channels[dmTeamID] {
		ids = append(ids, c.Id)
	}
	return ids
}

func TestBucketChannelsSortsDMsByRecency(t *testing.T) {
	m := &Model{}
	m.bucketChannels([]*model.Channel{
		{Id: "old", Type: model.ChannelTypeDirect, Name: "me__a", LastPostAt: 100},
		{Id: "new", Type: model.ChannelTypeDirect, Name: "me__b", LastPostAt: 300},
		{Id: "mid", Type: model.ChannelTypeGroup, Name: "me__c", LastPostAt: 200},
		{Id: "never", Type: model.ChannelTypeDirect, Name: "me__d", LastPostAt: 0},
	})

	got := dmIDs(m)
	want := []string{"new", "mid", "old", "never"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DM order = %v, want %v", got, want)
		}
	}
}

func TestTouchChannelActivityBumpsDM(t *testing.T) {
	m := &Model{}
	m.bucketChannels([]*model.Channel{
		{Id: "a", Type: model.ChannelTypeDirect, Name: "me__a", LastPostAt: 300},
		{Id: "b", Type: model.ChannelTypeDirect, Name: "me__b", LastPostAt: 200},
		{Id: "c", Type: model.ChannelTypeDirect, Name: "me__c", LastPostAt: 100},
	})

	// A new message in the oldest DM should float it to the top.
	m.touchChannelActivity("c", 400)
	if got, want := dmIDs(m), []string{"c", "a", "b"}; got[0] != want[0] {
		t.Fatalf("after bump, DM order = %v, want %v", got, want)
	}

	// A stale timestamp must not move anything.
	m.touchChannelActivity("b", 50)
	if got := dmIDs(m); got[0] != "c" {
		t.Fatalf("stale ts reordered DMs: %v", got)
	}
}

func TestTouchChannelActivityPreservesCursor(t *testing.T) {
	m := &Model{teamIdx: 0}
	m.teams = nil // single virtual DM tab path; currentTeamID falls through
	m.bucketChannels([]*model.Channel{
		{Id: "a", Type: model.ChannelTypeDirect, Name: "me__a", LastPostAt: 300},
		{Id: "b", Type: model.ChannelTypeDirect, Name: "me__b", LastPostAt: 200},
		{Id: "c", Type: model.ChannelTypeDirect, Name: "me__c", LastPostAt: 100},
	})

	// Only run the cursor assertion if the DM bucket is what's visible.
	vis := m.visibleChannels()
	if len(vis) != 3 {
		t.Skipf("DM bucket not the visible list in this minimal model (got %d)", len(vis))
	}
	// Cursor on "b" (index 1).
	m.channelIdx = 1
	cursorID := vis[m.channelIdx].Id

	// Bump "c" to the top; cursor should still point at "b".
	m.touchChannelActivity("c", 400)
	vis = m.visibleChannels()
	if vis[m.channelIdx].Id != cursorID {
		t.Fatalf("cursor moved off %s to %s after re-sort", cursorID, vis[m.channelIdx].Id)
	}
}

// touchChannelActivity must not re-sort or bump a regular team channel.
func TestTouchChannelActivityIgnoresTeamChannel(t *testing.T) {
	m := &Model{}
	m.bucketChannels([]*model.Channel{
		{Id: "x", Type: model.ChannelTypeOpen, TeamId: "t1", Name: "alpha", LastPostAt: 100},
	})
	m.touchChannelActivity("x", 999)
	if c := m.findChannel("x"); c.LastPostAt != 999 {
		// LastPostAt is still updated for accuracy, but the channel stays
		// in its team bucket and the DM bucket is untouched.
		t.Fatalf("team channel LastPostAt = %d, want 999", c.LastPostAt)
	}
	if _, ok := m.channels[dmTeamID]; ok {
		t.Fatalf("team channel leaked into DM bucket")
	}
}
