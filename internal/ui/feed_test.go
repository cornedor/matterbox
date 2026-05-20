package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func newBreadcrumbModel() Model {
	return Model{
		teams:     []*model.Team{{Id: "t1", DisplayName: "Engineering", Name: "eng"}},
		userNames: map[string]string{"u2": "alice"},
		me:        &model.User{Id: "u1", Username: "me"},
	}
}

func TestChannelBreadcrumb(t *testing.T) {
	m := newBreadcrumbModel()
	cases := []struct {
		name string
		ch   *model.Channel
		want string
	}{
		{
			name: "public channel prefixes its team",
			ch:   &model.Channel{Type: model.ChannelTypeOpen, TeamId: "t1", DisplayName: "General"},
			want: "Engineering › #General",
		},
		{
			name: "private channel prefixes its team",
			ch:   &model.Channel{Type: model.ChannelTypePrivate, TeamId: "t1", DisplayName: "Secrets"},
			want: "Engineering › 🔒Secrets",
		},
		{
			name: "direct message is under DMs",
			ch:   &model.Channel{Type: model.ChannelTypeDirect, Name: "u1__u2"},
			want: "DMs › @alice",
		},
		{
			name: "group message is under DMs",
			ch:   &model.Channel{Type: model.ChannelTypeGroup, DisplayName: "Group Chat"},
			want: "DMs › ·Group Chat",
		},
		{
			name: "unknown team falls back to the bare label",
			ch:   &model.Channel{Type: model.ChannelTypeOpen, TeamId: "gone", DisplayName: "Orphan"},
			want: "#Orphan",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.channelBreadcrumb(tc.ch); got != tc.want {
				t.Errorf("channelBreadcrumb = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestUnreadFromPostList(t *testing.T) {
	// Order is newest-first as the server returns it.
	pl := &model.PostList{
		Order: []string{"p5", "p4", "p3", "p2", "p1"},
		Posts: map[string]*model.Post{
			"p1": {Id: "p1", CreateAt: 100, Message: "already read"},
			"p2": {Id: "p2", CreateAt: 200, Message: "first unread"},
			"p3": {Id: "p3", CreateAt: 250, Message: "system", Type: "system_join_channel"},
			"p4": {Id: "p4", CreateAt: 300, Message: "deleted", DeleteAt: 999},
			"p5": {Id: "p5", CreateAt: 350, Message: "second unread"},
		},
	}
	got := unreadFromPostList(pl, 150)
	if len(got) != 2 {
		t.Fatalf("got %d posts; want 2 (%v)", len(got), got)
	}
	// Oldest → newest, system + deleted + already-read filtered out.
	if got[0].Id != "p2" || got[1].Id != "p5" {
		t.Errorf("got order [%s %s]; want [p2 p5]", got[0].Id, got[1].Id)
	}
}

func TestUnreadFromPostListNoBoundary(t *testing.T) {
	// A non-positive boundary keeps every non-system, non-deleted post
	// (the caller already limited the page).
	pl := &model.PostList{
		Order: []string{"p2", "p1"},
		Posts: map[string]*model.Post{
			"p1": {Id: "p1", CreateAt: 100, Message: "a"},
			"p2": {Id: "p2", CreateAt: 200, Message: "b"},
		},
	}
	got := unreadFromPostList(pl, 0)
	if len(got) != 2 || got[0].Id != "p1" || got[1].Id != "p2" {
		t.Fatalf("got %v; want [p1 p2]", got)
	}
}

func TestFeedEntryLastActivity(t *testing.T) {
	e := feedEntry{unread: []*model.Post{
		{Id: "a", CreateAt: 10},
		{Id: "b", CreateAt: 42},
	}}
	if got := e.lastActivity(); got != 42 {
		t.Errorf("lastActivity = %d; want 42", got)
	}
	if got := (feedEntry{}).lastActivity(); got != 0 {
		t.Errorf("empty lastActivity = %d; want 0", got)
	}
}
