package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// linkModel is a model with one team, one channel and one DM, enough for the
// link builders behind the "Copy link to …" palette commands.
func copyLinkModel() Model {
	return Model{
		serverURL: "https://chat.example.com/",
		teams:     []*model.Team{{Id: "t1", Name: "myteam", DisplayName: "My Team"}},
		channels: map[string][]*model.Channel{
			"t1": {{Id: "c1", TeamId: "t1", Name: "town-square", Type: model.ChannelTypeOpen}},
			dmTeamID: {{Id: "d1", Name: "aaaaaaaaaaaaaaaaaaaaaaaaaa__bbbbbbbbbbbbbbbbbbbbbbbbbb",
				Type: model.ChannelTypeDirect}},
		},
		openChannelID: "c1",
	}
}

// TestMessageLink builds a permalink our own parser reads back, so a copied
// link opens in-app for the next matterbox user who clicks it.
func TestMessageLink(t *testing.T) {
	m := copyLinkModel()
	got := m.messageLink(&model.Post{Id: pid, ChannelId: "c1"})
	want := "https://chat.example.com/myteam/pl/" + pid
	if got != want {
		t.Fatalf("messageLink = %q, want %q", got, want)
	}
	if id, ok := m.parsePermalinkPostID(got); !ok || id != pid {
		t.Errorf("own permalink didn't round-trip: (%q,%v)", id, ok)
	}
}

// A DM has no team of its own; the link hangs off a real one.
func TestMessageLinkDM(t *testing.T) {
	m := copyLinkModel()
	got := m.messageLink(&model.Post{Id: pid, ChannelId: "d1"})
	want := "https://chat.example.com/myteam/pl/" + pid
	if got != want {
		t.Errorf("messageLink(DM) = %q, want %q", got, want)
	}
}

func TestChannelLink(t *testing.T) {
	m := copyLinkModel()
	got := m.channelLink(m.findChannel("c1"))
	want := "https://chat.example.com/myteam/channels/town-square"
	if got != want {
		t.Errorf("channelLink = %q, want %q", got, want)
	}
}

// Without server_url there is nothing to build a link from — the commands
// report that rather than copying a broken URL.
func TestLinksNeedServerURL(t *testing.T) {
	m := copyLinkModel()
	m.serverURL = ""
	if got := m.messageLink(&model.Post{Id: pid, ChannelId: "c1"}); got != "" {
		t.Errorf("messageLink with no server_url = %q, want empty", got)
	}
	if got := m.channelLink(m.findChannel("c1")); got != "" {
		t.Errorf("channelLink with no server_url = %q, want empty", got)
	}
	if cmd := runCopyMessageLink(&m, ""); cmd != nil {
		t.Error("runCopyMessageLink returned a copy command with no server_url")
	}
}

// The channel command acts on the open channel, not the sidebar cursor, and
// says so when no channel is open.
func TestRunCopyChannelLinkNoChannel(t *testing.T) {
	m := copyLinkModel()
	m.openChannelID = ""
	if cmd := runCopyChannelLink(&m, ""); cmd != nil {
		t.Error("runCopyChannelLink returned a command with no channel open")
	}
	if m.status != "no channel open" {
		t.Errorf("status = %q, want %q", m.status, "no channel open")
	}
}
