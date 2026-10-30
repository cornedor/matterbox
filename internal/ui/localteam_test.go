package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/config"
)

const deploysID = localTeamIDPrefix + "Deploys"

// localTeamModel has two real teams (emico, acme), DMs with jira, gitlab and
// alice, a group DM of jira+gitlab and one of jira+alice, a combined channel
// "bots", and a local team "Deploys" over jira, gitlab and nobody (no DM),
// emico's deploy channel (renamed "prod") and the combined channel.
func localTeamModel(t *testing.T, hide bool, order ...string) Model {
	t.Helper()
	m := newTestModel()
	m.me = &model.User{Id: "me", Username: "me"}
	m.userNames = map[string]string{"ujira": "jira", "ugitlab": "gitlab", "ualice": "alice"}
	m.teamOrder = order
	m.teams = []*model.Team{
		{Id: "temico", Name: "emico", DisplayName: "Emico"},
		{Id: "tacme", Name: "acme", DisplayName: "Acme"},
	}
	m.combined = newCombinedChannels([]config.CombinedChannelConfig{{Name: "bots", Members: []string{"jira"}}})
	m.localTeams = newLocalTeams([]config.LocalTeamConfig{{
		Name:     "Deploys",
		Hide:     hide,
		Members:  []string{"@jira", "gitlab", "nobody"},
		Channels: []config.LocalTeamChannelConfig{{Channel: "emico/deploy", Name: "prod"}},
		Combined: []string{"bots"},
	}})
	m.applyTeamOrder()
	m.bucketChannels([]*model.Channel{
		{Id: "dmjira", Name: "me__ujira", Type: model.ChannelTypeDirect, LastPostAt: 300},
		{Id: "dmgitlab", Name: "ugitlab__me", Type: model.ChannelTypeDirect, LastPostAt: 200},
		{Id: "dmalice", Name: "me__ualice", Type: model.ChannelTypeDirect, LastPostAt: 100},
		{Id: "gmbots", Name: "h1", DisplayName: "gitlab, jira, me", Type: model.ChannelTypeGroup, LastPostAt: 50},
		{Id: "gmmixed", Name: "h2", DisplayName: "alice, jira, me", Type: model.ChannelTypeGroup, LastPostAt: 40},
		{Id: "cdeploy", TeamId: "temico", Name: "deploy", DisplayName: "Deploy", Type: model.ChannelTypeOpen},
		{Id: "cgeneral", TeamId: "temico", Name: "general", DisplayName: "General", Type: model.ChannelTypeOpen},
	})
	return m
}

func tabIDs(m *Model) []string {
	var out []string
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, id, _ := m.tabAt(i); kind == tabTeam {
			out = append(out, id)
		}
	}
	return out
}

func TestLocalTeamSortsAmongTeams(t *testing.T) {
	m := localTeamModel(t, false, "acme", "Deploys", "emico")
	if got, want := tabIDs(&m), []string{"tacme", deploysID, "temico"}; !equalStrings(got, want) {
		t.Fatalf("team tabs = %v, want %v (team_order)", got, want)
	}
	// Moving the local team writes its name into team_order like a real one.
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if _, id, _ := m.tabAt(i); id == deploysID {
			m.teamIdx = i
		}
	}
	if !m.moveTeam(-1) {
		t.Fatal("moveTeam(-1) on the local team did nothing")
	}
	if want := []string{"Deploys", "acme", "emico"}; !equalStrings(m.teamOrder, want) {
		t.Errorf("team_order = %v, want %v", m.teamOrder, want)
	}
}

func TestLocalTeamLists(t *testing.T) {
	m := localTeamModel(t, false)
	got := channelIDs(m.tabChannels(deploysID))
	want := []string{botsID, "dmjira", "dmgitlab", "gmbots", "cdeploy", localDMIDPrefix + "nobody"}
	if !equalStrings(got, want) {
		t.Errorf("Deploys = %v, want %v (no alice DM, no mixed group DM)", got, want)
	}
	if got := m.channelLabel(m.findChannel("cdeploy")); got != "#prod" {
		t.Errorf("deploy label = %q, want the local name #prod", got)
	}
	// Without hide everything stays in its own tab too, and that tab stays home.
	if !contains(channelIDs(m.tabChannels(dmTeamID)), "dmjira") || !contains(channelIDs(m.tabChannels("temico")), "cdeploy") {
		t.Error("without hide the channels must stay in their own tabs")
	}
	if got := m.homeTabOf(m.findChannel("dmjira")); got != dmTeamID {
		t.Errorf("home of dmjira = %q, want the DMs tab", got)
	}
}

func TestLocalTeamHideMovesHome(t *testing.T) {
	m := localTeamModel(t, true)
	dms := channelIDs(m.tabChannels(dmTeamID))
	for _, id := range []string{botsID, "dmjira", "dmgitlab", "gmbots"} {
		if contains(dms, id) {
			t.Errorf("DMs tab still lists %s", id)
		}
	}
	if !contains(dms, "dmalice") || !contains(dms, "gmmixed") {
		t.Errorf("DMs tab = %v, lost a non-member conversation", dms)
	}
	if contains(channelIDs(m.tabChannels("temico")), "cdeploy") {
		t.Error("emico still lists the deploy channel")
	}

	// ctrl+p on a member's DM lands on the local team, cursor on its row.
	m.switchToChannelHomeTeam(m.findChannel("dmgitlab"))
	if got := m.currentTeamID(); got != deploysID {
		t.Fatalf("landed on %q, want %q", got, deploysID)
	}
	if vis := m.visibleChannels(); vis[m.channelIdx].Id != "dmgitlab" {
		t.Errorf("cursor on %q, want dmgitlab", vis[m.channelIdx].Id)
	}
}

func TestLocalTeamDMlessMemberCreatesDM(t *testing.T) {
	m := localTeamModel(t, true)
	m.openChannelID = "dmalice"
	if cmd := m.openChannelLoadCmd(localDMIDPrefix+"nobody", "sidebar_key"); cmd == nil {
		t.Fatal("opening a DM-less member returned no command")
	}
	if m.openChannelID != "dmalice" {
		t.Errorf("openChannelID = %q; the placeholder must never become the open channel", m.openChannelID)
	}
	if got := m.channelLabel(m.tabChannels(deploysID)[5]); got != "@nobody" {
		t.Errorf("placeholder label = %q, want @nobody", got)
	}
}
