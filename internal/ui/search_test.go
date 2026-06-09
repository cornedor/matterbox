package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestParseSearchQuery(t *testing.T) {
	cases := []struct {
		in   string
		text string
		team string
		ch   string
	}{
		{in: "hello world", text: "hello world"},
		{in: "team:foo hello", text: "hello", team: "foo"},
		{in: `team:"my team" hello`, text: "hello", team: "my team"},
		{in: "in:off-topic", text: "", ch: "off-topic"},
		{in: "TEAM:foo Hello", text: "Hello", team: "foo"},
		{in: "hello team:foo in:bar baz", text: "hello baz", team: "foo", ch: "bar"},
		{in: "", text: ""},
		{in: "hello: world", text: "hello: world"}, // not a modifier — only team/in count
		{in: "in:\"with spaces\"", text: "", ch: "with spaces"},
		// Last occurrence wins.
		{in: "team:a team:b hello", text: "hello", team: "b"},
	}
	for _, tc := range cases {
		got := parseSearchQuery(tc.in)
		if got.text != tc.text || got.team != tc.team || got.in != tc.ch {
			t.Errorf("parse(%q) = (text=%q, team=%q, in=%q); want (%q, %q, %q)",
				tc.in, got.text, got.team, got.in, tc.text, tc.team, tc.ch)
		}
	}
}

func TestSearchInValue(t *testing.T) {
	m := Model{
		me:        &model.User{Id: "meid"},
		userNames: map[string]string{"otherid": "alice"},
	}
	cases := []struct {
		name string
		ch   *model.Channel
		want string
	}{
		{"direct", &model.Channel{Type: model.ChannelTypeDirect, Name: "meid__otherid"}, "alice"},
		{"direct reversed", &model.Channel{Type: model.ChannelTypeDirect, Name: "otherid__meid"}, "alice"},
		{"group uses display name", &model.Channel{Type: model.ChannelTypeGroup, DisplayName: "alice, bob"}, "alice, bob"},
		{"open uses slug", &model.Channel{Type: model.ChannelTypeOpen, Name: "off-topic", DisplayName: "Off Topic"}, "off-topic"},
		{"private uses slug", &model.Channel{Type: model.ChannelTypePrivate, Name: "secret", DisplayName: "Secret"}, "secret"},
		{"unresolved dm partner", &model.Channel{Type: model.ChannelTypeDirect, Name: "meid__unknown"}, ""},
	}
	for _, tc := range cases {
		if got := m.searchInValue(tc.ch); got != tc.want {
			t.Errorf("%s: searchInValue = %q; want %q", tc.name, got, tc.want)
		}
	}
}

func TestQuoteModifier(t *testing.T) {
	cases := map[string]string{
		"engineering": "engineering",
		"off-topic":   "off-topic",
		"My Team":     `"My Team"`,
		"a\tb":        "\"a\tb\"",
	}
	for in, want := range cases {
		if got := quoteModifier(in); got != want {
			t.Errorf("quoteModifier(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestSearchScopePrefix(t *testing.T) {
	// With no DMs the tab order is Unread, Feed, Search, then teams — so
	// the first (only) team lands at teamIdx 3.
	t.Run("regular channel", func(t *testing.T) {
		m := Model{
			teams: []*model.Team{{Id: "t1", Name: "engineering", DisplayName: "Engineering"}},
			channels: map[string][]*model.Channel{
				"t1": {{Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "off-topic", DisplayName: "Off Topic"}},
			},
			teamIdx:       3,
			channelIdx:    0,
			openChannelID: "c1",
		}
		if got, want := m.searchScopePrefix(), "team:Engineering in:off-topic "; got != want {
			t.Errorf("searchScopePrefix = %q; want %q", got, want)
		}
	})

	t.Run("spaced team name is quoted", func(t *testing.T) {
		m := Model{
			teams: []*model.Team{{Id: "t1", Name: "eng", DisplayName: "My Team"}},
			channels: map[string][]*model.Channel{
				"t1": {{Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "general", DisplayName: "General"}},
			},
			teamIdx:       3,
			channelIdx:    0,
			openChannelID: "c1",
		}
		if got, want := m.searchScopePrefix(), `team:"My Team" in:general `; got != want {
			t.Errorf("searchScopePrefix = %q; want %q", got, want)
		}
	})

	t.Run("dm has no team", func(t *testing.T) {
		m := Model{
			me:        &model.User{Id: "meid"},
			userNames: map[string]string{"otherid": "alice"},
			hasDMs:    true, // DMs tab becomes index 0
			channels: map[string][]*model.Channel{
				dmTeamID: {{Id: "d1", Type: model.ChannelTypeDirect, Name: "meid__otherid"}},
			},
			teamIdx:       0,
			channelIdx:    0,
			openChannelID: "d1",
		}
		if got, want := m.searchScopePrefix(), "in:alice "; got != want {
			t.Errorf("searchScopePrefix = %q; want %q", got, want)
		}
	})

	t.Run("no current channel", func(t *testing.T) {
		m := Model{channels: map[string][]*model.Channel{}}
		if got := m.searchScopePrefix(); got != "" {
			t.Errorf("searchScopePrefix = %q; want empty", got)
		}
	})
}
