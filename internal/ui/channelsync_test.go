package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// A channel the user was added to while the app couldn't hear about it — asleep
// through the add, or simply running before this existed — used to stay
// invisible until a restart, along with every message posted in it. These tests
// pin the two paths that now learn of it: the reconnect catch-up and the live
// `user_added` event.

// resyncModel is wsModel with the pieces a channel resync touches: the sidebar
// cursor parked on a known row, and the team tab holding the channels.
func resyncModel(t *testing.T) Model {
	t.Helper()
	m := wsModel(t)
	// feedMouseModel parks teamIdx on the Feed tab; move it to the tab holding
	// the channels so the sidebar cursor means something here.
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if _, id, _ := m.tabAt(i); id == "t1" {
			m.teamIdx = i
			break
		}
	}
	m.channelIdx = 1
	m.openChannelID = "c1"
	return m
}

// channelSet is the server's answer to fetchAllChannels: the existing bucket
// plus whatever extra ids the test says the user has since been added to.
func channelSet(m *Model, extra ...string) []*model.Channel {
	out := append([]*model.Channel(nil), m.channels["t1"]...)
	for _, id := range extra {
		out = append(out, &model.Channel{
			Id: id, TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: id,
		})
	}
	return out
}

// The headline case: the add happened while we were deaf, so nothing but a
// fresh channel list can surface the row.
func TestResyncAdoptsChannelAddedWhileDisconnected(t *testing.T) {
	m := resyncModel(t)
	if m.findChannel("cnew") != nil {
		t.Fatal("cnew is supposed to be unknown before the resync")
	}

	m.applyChannelsResynced(channelsLoadedMsg{channels: channelSet(&m, "cnew"), resync: true})

	if m.findChannel("cnew") == nil {
		t.Fatal("channel added during the outage is still missing from the sidebar")
	}
}

// The sidebar cursor is an index, so a channel landing above it would silently
// move the selection to a different conversation.
func TestResyncKeepsSidebarCursorOnItsChannel(t *testing.T) {
	m := resyncModel(t)
	was := m.visibleChannels()[m.channelIdx].Id

	// "aaa" sorts above every c<n>, so it shifts every row down by one.
	m.applyChannelsResynced(channelsLoadedMsg{channels: channelSet(&m, "aaa"), resync: true})

	if got := m.visibleChannels()[m.channelIdx].Id; got != was {
		t.Errorf("cursor moved from %s to %s across the resync", was, got)
	}
}

// A channel that arrived with a backlog gets no `posted` events to count, so
// its badge has to come from the member row the same resync fetched.
func TestResyncBadgesChannelAddedWhileDisconnected(t *testing.T) {
	m := resyncModel(t)
	m.applyChannelsResynced(channelsLoadedMsg{channels: channelSet(&m, "cnew"), resync: true})
	seedUnread(&m, "cnew", 4, 1, 2)

	m.applyUnreadFromMembers()

	if m.unread["cnew"] != 3 {
		t.Errorf("unread = %d; want the 3 messages waiting in the new channel", m.unread["cnew"])
	}
	if m.mentions["cnew"] != 2 {
		t.Errorf("mentions = %d; want 2", m.mentions["cnew"])
	}
}

// The resync reuses channelsLoadedMsg, which on startup also opens the restored
// conversation and starts the presence poll. Neither may fire again mid-session.
func TestResyncSkipsStartupWork(t *testing.T) {
	m := resyncModel(t)
	m.posts = nil // startup's "nothing open yet" state, the one that would re-open

	out, cmd := m.update(channelsLoadedMsg{channels: channelSet(&m), resync: true})
	got := out.(Model)

	if cmd != nil {
		t.Errorf("resync issued startup commands; want none with no feed to rebuild")
	}
	if got.statusPollStarted {
		t.Error("resync started the presence poll a second time")
	}
}

// Being added while the app is awake and connected: the event says so, and it
// is the only notice we get.
func TestUserAddedForMeSchedulesResync(t *testing.T) {
	m := resyncModel(t)
	ev := model.NewWebSocketEvent(model.WebsocketEventUserAdded, "", "cnew", "", nil, "")
	ev.Add("user_id", "me")

	cmd := m.handleWSEvent(ev)

	if cmd == nil {
		t.Fatal("being added to a channel scheduled no resync")
	}
	if !m.channelResyncQueued {
		t.Error("channelResyncQueued not set")
	}
}

// user_added is broadcast to the whole channel, so most of them are somebody
// else joining a channel we are already in — a full channel-list refetch each
// time would be a request per join.
func TestUserAddedForSomeoneElseIsIgnored(t *testing.T) {
	m := resyncModel(t)
	ev := model.NewWebSocketEvent(model.WebsocketEventUserAdded, "", "c1", "", nil, "")
	ev.Add("user_id", "someone")

	if cmd := m.handleWSEvent(ev); cmd != nil {
		t.Error("somebody else joining triggered a channel resync")
	}
	if m.channelResyncQueued {
		t.Error("channelResyncQueued set for another user's join")
	}
}

// A team join arrives as several adds at once; the refetch is a full list, so
// one covers them all.
func TestChannelResyncDebouncesBurst(t *testing.T) {
	m := resyncModel(t)

	scheduled := 0
	for i := 0; i < 3; i++ {
		ev := model.NewWebSocketEvent(model.WebsocketEventUserAdded, "", "cnew", "", nil, "")
		ev.Add("user_id", "me")
		if m.handleWSEvent(ev) != nil {
			scheduled++
		}
	}

	if scheduled != 1 {
		t.Errorf("%d refetches scheduled for one burst; want 1", scheduled)
	}
}

// The debounce reopens once its window closes, or a second add later in the
// session would be swallowed by the first one's spent flag.
func TestChannelResyncRearmsAfterFiring(t *testing.T) {
	m := resyncModel(t)
	m.channelResyncQueued = true

	if cmd := m.applyChannelResyncDue(); cmd == nil {
		t.Fatal("the due resync fetched nothing")
	}
	if m.channelResyncQueued {
		t.Fatal("still queued after firing; a later add would never schedule")
	}
	if m.scheduleChannelResync() == nil {
		t.Error("a later add scheduled nothing")
	}
}

// A DM someone else opens with us is a channel we have never seen; direct_added
// only ever reaches its participants, so there is no sender to filter on.
func TestDirectAddedSchedulesResync(t *testing.T) {
	for _, et := range []model.WebsocketEventType{
		model.WebsocketEventDirectAdded, model.WebsocketEventGroupAdded,
	} {
		m := resyncModel(t)
		ev := model.NewWebSocketEvent(et, "", "dnew", "me", nil, "")
		if m.handleWSEvent(ev) == nil {
			t.Errorf("%s scheduled no resync", et)
		}
	}
}

// Before /users/me lands there is no id to fetch a channel list for.
func TestChannelResyncBeforeUserLoaded(t *testing.T) {
	m := resyncModel(t)
	m.me = nil

	if cmd := m.scheduleChannelResync(); cmd != nil {
		t.Error("scheduled a resync with no account loaded")
	}
	m.channelResyncQueued = true
	if cmd := m.applyChannelResyncDue(); cmd != nil {
		t.Error("fetched a channel list with no account loaded")
	}
}
