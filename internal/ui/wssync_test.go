package ui

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// wsWithJSON builds an event carrying a marshalled object under key, the way
// the server sends channels, teams and channel members.
func wsWithJSON(t *testing.T, et model.WebsocketEventType, teamID, chanID, userID, key string, v any) *model.WebSocketEvent {
	t.Helper()
	ev := model.NewWebSocketEvent(et, teamID, chanID, userID, nil, "")
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	ev.Add(key, string(b))
	return ev
}

// A rename has to reach the row, the title and everything else holding the
// channel — which is why the update is copied over the existing struct rather
// than replacing the pointer.
func TestChannelUpdatedRenamesInPlace(t *testing.T) {
	m := resyncModel(t)
	before := m.findChannel("c1")

	ev := wsWithJSON(t, model.WebsocketEventChannelUpdated, "", "c1", "", "channel", &model.Channel{
		Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "renamed",
	})
	m.handleWSEvent(ev)

	if got := m.findChannel("c1").DisplayName; got != "renamed" {
		t.Errorf("DisplayName = %q; want renamed", got)
	}
	if before.DisplayName != "renamed" {
		t.Error("the pointer everything else holds still shows the old name")
	}
}

// A rename moves the row, since the bucket is sorted by label.
func TestChannelUpdatedResortsAndKeepsCursor(t *testing.T) {
	m := resyncModel(t) // cursor on c1, of c0/c1
	was := m.visibleChannels()[m.channelIdx].Id

	// "aaa" sorts c1 to the top of the bucket.
	ev := wsWithJSON(t, model.WebsocketEventChannelUpdated, "", "c1", "", "channel", &model.Channel{
		Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "aaa",
	})
	m.handleWSEvent(ev)

	if got := m.visibleChannels()[m.channelIdx].Id; got != was {
		t.Errorf("cursor slid from %s to %s across the re-sort", was, got)
	}
}

// A channel we aren't in has no row to correct, and inventing one would put a
// channel in the sidebar the user never joined.
func TestChannelUpdatedForUnknownChannelIsIgnored(t *testing.T) {
	m := resyncModel(t)

	ev := wsWithJSON(t, model.WebsocketEventChannelUpdated, "", "cx", "", "channel", &model.Channel{
		Id: "cx", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "elsewhere",
	})
	m.handleWSEvent(ev)

	if m.findChannel("cx") != nil {
		t.Error("an update for a channel we aren't in added it to the sidebar")
	}
}

// Public → private changes the row's prefix, which is part of its label.
func TestChannelConvertedChangesType(t *testing.T) {
	m := resyncModel(t)
	ev := model.NewWebSocketEvent(model.WebsocketEventChannelConverted, "t1", "", "", nil, "")
	ev.Add("channel_id", "c1")
	ev.Add("channel_type", string(model.ChannelTypePrivate))

	m.handleWSEvent(ev)

	if got := m.findChannel("c1").Type; got != model.ChannelTypePrivate {
		t.Errorf("Type = %q; want private", got)
	}
}

// Muting on another client has to reach m.mutedChannels, or the feed keeps
// showing a channel the user has silenced.
func TestChannelMemberUpdatedAppliesMute(t *testing.T) {
	m := resyncModel(t)
	m.members = model.ChannelMembersWithTeamData{{
		ChannelMember: model.ChannelMember{ChannelId: "c1", UserId: "me"},
	}}
	m.rebuildMutedChannels()
	if m.channelMuted("c1") {
		t.Fatal("c1 starts muted; the test proves nothing")
	}

	mb := model.ChannelMember{
		ChannelId:   "c1",
		UserId:      "me",
		NotifyProps: model.StringMap{model.MarkUnreadNotifyProp: model.ChannelMarkUnreadMention},
	}
	m.handleWSEvent(wsWithJSON(t, model.WebsocketEventChannelMemberUpdated, "", "", "me", "channelMember", &mb))

	if !m.channelMuted("c1") {
		t.Error("mute set on another client never reached mutedChannels")
	}
}

// The badges are left alone on purpose: deriving them needs a TotalMsgCount
// that only a channel fetch refreshes, while m.unread has been counting live
// posts since. Re-deriving here would throw those away.
func TestChannelMemberUpdatedKeepsLiveUnread(t *testing.T) {
	m := resyncModel(t)
	m.unread["c1"] = 4

	mb := model.ChannelMember{ChannelId: "c1", UserId: "me", MsgCount: 99, MsgCountRoot: 99}
	m.handleWSEvent(wsWithJSON(t, model.WebsocketEventChannelMemberUpdated, "", "", "me", "channelMember", &mb))

	if m.unread["c1"] != 4 {
		t.Errorf("unread = %d; want the 4 counted from live events", m.unread["c1"])
	}
}

// Somebody else's membership row is not ours to apply.
func TestChannelMemberUpdatedForAnotherUserIsIgnored(t *testing.T) {
	m := resyncModel(t)
	mb := model.ChannelMember{
		ChannelId:   "c1",
		UserId:      "someone",
		NotifyProps: model.StringMap{model.MarkUnreadNotifyProp: model.ChannelMarkUnreadMention},
	}

	m.handleWSEvent(wsWithJSON(t, model.WebsocketEventChannelMemberUpdated, "", "", "someone", "channelMember", &mb))

	if m.channelMuted("c1") {
		t.Error("somebody else muting the channel muted it here")
	}
}

// A renamed team relabels its tab, and the strip is ordered by that label.
func TestTeamUpdatedRenamesAndKeepsTab(t *testing.T) {
	m := resyncModel(t)
	m.teams = append(m.teams, &model.Team{Id: "t2", DisplayName: "T2", Name: "t2"})
	kindWas, idWas := m.teamTabAnchor()

	ev := wsWithJSON(t, model.WebsocketEventUpdateTeam, "t1", "", "", "team", &model.Team{
		Id: "t1", DisplayName: "ZZZ last", Name: "t1",
	})
	m.handleWSEvent(ev)

	var found bool
	for _, tm := range m.teams {
		if tm.Id == "t1" && tm.DisplayName == "ZZZ last" {
			found = true
		}
	}
	if !found {
		t.Fatal("team rename never applied")
	}
	kind, id := m.teamTabAnchor()
	if kind != kindWas || id != idWas {
		t.Errorf("focused tab moved to (%v,%s) across the rename", kind, id)
	}
}

// A team going away or coming back takes all of its channels with it, so it
// needs the full refetch rather than a patch.
func TestTeamDeletedAndRestoredResync(t *testing.T) {
	for _, et := range []model.WebsocketEventType{
		model.WebsocketEventDeleteTeam, model.WebsocketEventRestoreTeam,
	} {
		m := resyncModel(t)
		ev := wsWithJSON(t, et, "t1", "", "", "team", &model.Team{Id: "t1", DisplayName: "T1", Name: "t1"})
		if m.handleWSEvent(ev) == nil {
			t.Errorf("%s scheduled no resync", et)
		}
	}
}

// Marking a message unread elsewhere has to light the badge here.
func TestPostUnreadRestoresBadge(t *testing.T) {
	m := resyncModel(t)
	delete(m.unread, "c1")
	ch := m.findChannel("c1")
	ch.TotalMsgCount, ch.TotalMsgCountRoot = 10, 10

	ev := model.NewWebSocketEvent(model.WebsocketEventPostUnread, "t1", "c1", "me", nil, "")
	ev.Add("msg_count", float64(7))
	ev.Add("msg_count_root", float64(7))
	ev.Add("mention_count", float64(2))
	ev.Add("mention_count_root", float64(2))
	ev.Add("last_viewed_at", float64(1234))
	m.handleWSEvent(ev)

	if m.unread["c1"] != 3 {
		t.Errorf("unread = %d; want 3 (10 total − 7 seen)", m.unread["c1"])
	}
	if m.mentions["c1"] != 2 {
		t.Errorf("mentions = %d; want 2", m.mentions["c1"])
	}
}

// TotalMsgCount only moves on a channel fetch, so it can sit behind the post
// that was just marked unread and compute zero. Reporting the channel as read
// is the one answer that cannot be right — the user just said otherwise.
func TestPostUnreadNeverComputesRead(t *testing.T) {
	m := resyncModel(t)
	ch := m.findChannel("c1")
	ch.TotalMsgCount, ch.TotalMsgCountRoot = 5, 5

	ev := model.NewWebSocketEvent(model.WebsocketEventPostUnread, "t1", "c1", "me", nil, "")
	ev.Add("msg_count", float64(5)) // stale total says nothing is unread
	ev.Add("msg_count_root", float64(5))
	m.handleWSEvent(ev)

	if m.unread["c1"] < 1 {
		t.Errorf("unread = %d; want at least 1 after a deliberate mark-unread", m.unread["c1"])
	}
}

// The read boundary rides along, so the feed's next build asks the server for
// the right window.
func TestPostUnreadStoresLastViewedAt(t *testing.T) {
	m := resyncModel(t)
	ev := model.NewWebSocketEvent(model.WebsocketEventPostUnread, "t1", "c1", "me", nil, "")
	ev.Add("last_viewed_at", float64(4242))
	m.handleWSEvent(ev)

	for _, mb := range m.members {
		if mb.ChannelId == "c1" {
			if mb.LastViewedAt != 4242 {
				t.Errorf("LastViewedAt = %d; want 4242", mb.LastViewedAt)
			}
			return
		}
	}
	t.Error("no member row stored for c1")
}

// A ping timeout and a dropped link both classify as "network", so without a
// cause of its own the one failure that can be ours — a reader that stalled and
// stopped producing — is indistinguishable from the user's wifi.
func TestWSDropCauseSeparatesPingTimeout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		pingTimeout bool
		want        string
	}{
		{"ping timeout", errors.New("ping timeout"), true, "ping_timeout"},
		{"read error", errors.New("connection reset by peer"), false, "read_error"},
		{"clean close", nil, false, "closed"},
	} {
		if got := wsDropCause(tc.err, tc.pingTimeout); got != tc.want {
			t.Errorf("%s: cause = %q; want %q", tc.name, got, tc.want)
		}
	}
}

// The flag has to survive the trip from the watchdog to the telemetry, which is
// the whole point of carrying it rather than re-reading the error text.
func TestWSClosedCarriesPingTimeoutToTheDropCause(t *testing.T) {
	m := resyncModel(t)

	out, _ := m.update(wsClosedMsg{err: errors.New("ping timeout"), pingTimeout: true})
	if got := out.(Model).wsRetry; got != 1 {
		t.Fatalf("wsRetry = %d; want the drop to have been handled", got)
	}
	if got := wsDropCause(errors.New("ping timeout"), true); got != "ping_timeout" {
		t.Errorf("cause = %q; want ping_timeout", got)
	}
}
