package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestChannelType(t *testing.T) {
	cases := map[model.ChannelType]string{
		model.ChannelTypeOpen:    "public",
		model.ChannelTypePrivate: "private",
		model.ChannelTypeDirect:  "direct",
		model.ChannelTypeGroup:   "group",
	}
	for typ, want := range cases {
		if got := channelType(&model.Channel{Type: typ}); got != want {
			t.Errorf("channelType(%q) = %q, want %q", typ, got, want)
		}
	}
}

func TestChannelRow(t *testing.T) {
	teamSlug := map[string]string{"t1": "eng"}
	names := map[string]string{"alice": "alice"}

	t.Run("public channel gets team/channel address", func(t *testing.T) {
		r := channelRow(&model.Channel{Id: "c1", Type: model.ChannelTypeOpen, TeamId: "t1", Name: "general", DisplayName: "General"}, teamSlug, names, "me")
		if r.Address != "eng/general" || r.Team != "eng" || r.Type != "public" {
			t.Errorf("row = %+v", r)
		}
	})

	t.Run("unknown team falls back to bare channel name", func(t *testing.T) {
		r := channelRow(&model.Channel{Id: "c2", Type: model.ChannelTypePrivate, TeamId: "tX", Name: "secret"}, teamSlug, names, "me")
		if r.Address != "secret" || r.Team != "" {
			t.Errorf("row = %+v, want address=secret, no slug", r)
		}
	})

	t.Run("DM resolves to @partner", func(t *testing.T) {
		r := channelRow(&model.Channel{Id: "dm", Type: model.ChannelTypeDirect, Name: "me__alice"}, teamSlug, names, "me")
		if r.Address != "@alice" || r.Partner != "alice" {
			t.Errorf("row = %+v, want @alice", r)
		}
	})

	t.Run("unresolved DM partner leaves address empty", func(t *testing.T) {
		r := channelRow(&model.Channel{Id: "dm2", Type: model.ChannelTypeDirect, Name: "me__ghost"}, teamSlug, names, "me")
		if r.Address != "" {
			t.Errorf("row address = %q, want empty", r.Address)
		}
	})

	t.Run("group DM is not addressable", func(t *testing.T) {
		r := channelRow(&model.Channel{Id: "g", Type: model.ChannelTypeGroup, Name: "hash", DisplayName: "The Group"}, teamSlug, names, "me")
		if r.Address != "" || r.Type != "group" || r.DisplayName != "The Group" {
			t.Errorf("row = %+v", r)
		}
	})
}

func TestOrderChannels(t *testing.T) {
	teamName := map[string]string{"t1": "Engineering", "t2": "Marketing"}
	rows := []jsonChannel{
		{ID: "1", Address: "mkt/campaigns", Name: "campaigns", Type: "public", TeamID: "t2"},
		{ID: "2", Address: "@bob", Type: "direct"},
		{ID: "3", Address: "eng/zebra", Name: "zebra", Type: "public", TeamID: "t1"},
		{ID: "4", Address: "eng/alpha", Name: "alpha", Type: "public", TeamID: "t1"},
		{ID: "5", Address: "@alice", Type: "direct"},
		{ID: "6", Address: "", DisplayName: "Group X", Type: "group"},
	}
	ordered := orderChannels(rows, teamName)
	var gotIDs []string
	for _, r := range ordered {
		gotIDs = append(gotIDs, r.ID)
	}
	// Engineering (alpha, zebra) → Marketing (campaigns) → DMs (@alice, @bob, Group X).
	want := []string{"4", "3", "1", "5", "2", "6"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", gotIDs, want)
	}
}

func TestPrintChannels(t *testing.T) {
	teamName := map[string]string{"t1": "Engineering"}
	rows := []jsonChannel{
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaa", Address: "eng/general", Name: "general", DisplayName: "General", Type: "public", TeamID: "t1"},
		{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbb", Address: "@alice", DisplayName: "alice", Type: "direct", Partner: "alice"},
	}
	var buf bytes.Buffer
	printChannels(&buf, orderChannels(rows, teamName), teamName)
	out := buf.String()

	for _, want := range []string{
		"# Engineering\n",
		"# Direct messages\n",
		"eng/general",
		"@alice",
		"public",
		"direct",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The team header precedes the DM header.
	if i, j := strings.Index(out, "# Engineering"), strings.Index(out, "# Direct messages"); i < 0 || j < 0 || i > j {
		t.Errorf("team group should come before DMs:\n%s", out)
	}
	// id is the first column on each channel row.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, "eng/general") && !strings.HasPrefix(line, "aaaaaaaa") {
			t.Errorf("channel line should start with the id: %q", line)
		}
	}
}
