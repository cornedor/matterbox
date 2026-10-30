package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/config"
)

// combinedModel has three DMs (jira, gitlab, alice) and one combined channel
// "bots" over jira + gitlab, bucketed the way startup does it.
func combinedModel(t *testing.T, posts ...*model.Post) Model {
	t.Helper()
	m := newTestModel()
	m.me = &model.User{Id: "me"}
	m.store = openSeededStore(t, posts...)
	m.userNames = map[string]string{"ujira": "jira", "ugitlab": "GitLab", "ualice": "alice"}
	m.combined = newCombinedChannels([]config.CombinedChannelConfig{
		{Name: "bots", Members: []string{"@jira", "gitlab"}},
	})
	m.bucketChannels([]*model.Channel{
		{Id: "dmalice", Name: "me__ualice", Type: model.ChannelTypeDirect, LastPostAt: 300},
		{Id: "dmjira", Name: "me__ujira", Type: model.ChannelTypeDirect, LastPostAt: 200},
		{Id: "dmgitlab", Name: "ugitlab__me", Type: model.ChannelTypeDirect, LastPostAt: 100},
	})
	return m
}

const botsID = combinedIDPrefix + "bots"

func TestCombinedPinnedAtTopOfDMs(t *testing.T) {
	m := combinedModel(t)
	dms := m.channels[dmTeamID]
	if len(dms) != 4 || dms[0].Id != botsID {
		t.Fatalf("DM bucket = %v, want bots first", channelIDs(dms))
	}
	// A live post re-sorts the DMs; the combined row must stay on top.
	m.touchChannelActivity("dmgitlab", 999)
	if got := m.channels[dmTeamID][0].Id; got != botsID {
		t.Errorf("after re-sort first DM = %q, want %q", got, botsID)
	}
	if got := m.combinedMemberIDs(botsID); len(got) != 2 || !contains(got, "dmjira") || !contains(got, "dmgitlab") {
		t.Errorf("members = %v, want dmjira + dmgitlab (username match is case-insensitive, @ optional)", got)
	}
}

func TestCombinedOpenMergesMemberDMs(t *testing.T) {
	m := combinedModel(t,
		&model.Post{Id: "j1", ChannelId: "dmjira", UserId: "ujira", CreateAt: 100, Message: "jira 1"},
		&model.Post{Id: "a1", ChannelId: "dmalice", UserId: "ualice", CreateAt: 150, Message: "not a member"},
		&model.Post{Id: "g1", ChannelId: "dmgitlab", UserId: "ugitlab", CreateAt: 200, Message: "gitlab 1"},
		&model.Post{Id: "j2", ChannelId: "dmjira", UserId: "ujira", CreateAt: 300, Message: "jira 2"},
	)
	m.openChannelLoadCmd(botsID, "sidebar_key")

	var got []string
	for _, p := range m.posts {
		got = append(got, p.Id)
	}
	if want := []string{"j1", "g1", "j2"}; !equalStrings(got, want) {
		t.Errorf("posts = %v, want %v", got, want)
	}
	if ch, _, tracks := m.composerDraftTarget(); tracks {
		t.Errorf("composer drafts for %q; a combined channel has no draft target", ch)
	}
	if !m.combinedSendBlocked() {
		t.Error("top-level send must be blocked in a combined channel")
	}
}

func TestCombinedLivePostRouting(t *testing.T) {
	m := combinedModel(t, &model.Post{Id: "j1", ChannelId: "dmjira", UserId: "ujira", CreateAt: 100, Message: "jira 1"})
	m.openChannelLoadCmd(botsID, "sidebar_key")

	m.applyPosted(postedEvent(&model.Post{Id: "g2", ChannelId: "dmgitlab", UserId: "ugitlab", CreateAt: 400, Message: "pipeline"}))
	m.applyPosted(postedEvent(&model.Post{Id: "a2", ChannelId: "dmalice", UserId: "ualice", CreateAt: 500, Message: "hi"}))

	if last := m.posts[len(m.posts)-1]; last.Id != "g2" {
		t.Errorf("last post = %q, want the member's live post g2", last.Id)
	}
	if m.unread["dmalice"] != 1 {
		t.Errorf("dmalice unread = %d, want 1 (not a member, so a background post)", m.unread["dmalice"])
	}
	if m.unread["dmgitlab"] != 0 {
		t.Errorf("dmgitlab unread = %d, want 0 (shown in the open combined channel)", m.unread["dmgitlab"])
	}
}

func TestCombinedBadgesAndMarkRead(t *testing.T) {
	m := combinedModel(t)
	m.unread["dmjira"], m.unread["dmgitlab"], m.unread["dmalice"] = 2, 3, 5
	m.mentions["dmjira"] = 1

	bots := m.findChannel(botsID)
	if unread, mentions := m.channelBadges(bots); unread != 5 || mentions != 1 {
		t.Errorf("badges = %d/%d, want 5 unread / 1 mention (sum of members)", unread, mentions)
	}
	m.clearUnread(botsID)
	if m.unread["dmjira"] != 0 || m.unread["dmgitlab"] != 0 || m.mentions["dmjira"] != 0 {
		t.Errorf("members not cleared: %v %v", m.unread, m.mentions)
	}
	if m.unread["dmalice"] != 5 {
		t.Errorf("dmalice unread = %d, want 5 untouched", m.unread["dmalice"])
	}
}

func channelIDs(cs []*model.Channel) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Id)
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCombinedTeamChannels(t *testing.T) {
	m := newTestModel()
	m.me = &model.User{Id: "me"}
	m.store = openSeededStore(t,
		&model.Post{Id: "j1", ChannelId: "dmjira", UserId: "ujira", CreateAt: 100, Message: "jira 1"},
		&model.Post{Id: "d1", ChannelId: "cdeploy", UserId: "ubot", CreateAt: 200, Message: "deployed"},
		&model.Post{Id: "g1", ChannelId: "cgeneral", UserId: "ualice", CreateAt: 300, Message: "not merged"},
	)
	m.userNames = map[string]string{"ujira": "jira"}
	m.teams = []*model.Team{{Id: "temico", Name: "emico"}}
	m.combined = newCombinedChannels([]config.CombinedChannelConfig{
		{Name: "bots", Members: []string{"jira"}, Channels: []string{"Emico/deploy", "emico/missing", "nonsense"}},
		{Name: "deploys", Channels: []string{"emico/deploy"}},
	})
	m.bucketChannels([]*model.Channel{
		{Id: "dmjira", Name: "me__ujira", Type: model.ChannelTypeDirect},
		{Id: "cdeploy", TeamId: "temico", Name: "deploy", Type: model.ChannelTypeOpen},
		{Id: "cgeneral", TeamId: "temico", Name: "general", Type: model.ChannelTypeOpen},
	})

	if got := m.combinedMemberIDs(botsID); !equalStrings(got, []string{"dmjira", "cdeploy"}) {
		t.Fatalf("members = %v, want dmjira + cdeploy (team/channel is case-insensitive)", got)
	}
	if got := m.combinedMemberIDs(combinedIDPrefix + "deploys"); !equalStrings(got, []string{"cdeploy"}) {
		t.Errorf("channels-only members = %v, want cdeploy", got)
	}

	m.openChannelLoadCmd(botsID, "sidebar_key")
	var got []string
	for _, p := range m.posts {
		got = append(got, p.Id)
	}
	if want := []string{"j1", "d1"}; !equalStrings(got, want) {
		t.Errorf("posts = %v, want %v", got, want)
	}

	m.unread["cdeploy"], m.unread["cgeneral"] = 4, 2
	if unread, _ := m.channelBadges(m.findChannel(botsID)); unread != 4 {
		t.Errorf("unread = %d, want 4 from the team channel", unread)
	}
	m.clearUnread(botsID)
	if m.unread["cdeploy"] != 0 || m.unread["cgeneral"] != 2 {
		t.Errorf("unread after clear = %v, want only cdeploy cleared", m.unread)
	}

	// Joining a channel later adds it without a DM-bucket change.
	m.combined[0].channels = append(m.combined[0].channels, teamChannelRef{team: "emico", channel: "ops"})
	m.channels["temico"] = append(m.channels["temico"], &model.Channel{Id: "cops", TeamId: "temico", Name: "ops", Type: model.ChannelTypeOpen})
	if got := m.combinedMemberIDs(botsID); !contains(got, "cops") {
		t.Errorf("members = %v, want the joined channel cops", got)
	}
}
