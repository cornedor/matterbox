package cli

import (
	"bytes"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestUnreadFromList(t *testing.T) {
	const boundary = int64(100)
	pl := &model.PostList{
		Order: []string{"new2", "new1", "sys", "del", "old"},
		Posts: map[string]*model.Post{
			"new1": {Id: "new1", Message: "a", CreateAt: 110},
			"new2": {Id: "new2", Message: "b", CreateAt: 120},
			"sys":  {Id: "sys", Message: "joined", CreateAt: 130, Type: model.PostTypeJoinChannel},
			"del":  {Id: "del", Message: "gone", CreateAt: 140, DeleteAt: 141},
			"old":  {Id: "old", Message: "read", CreateAt: 90}, // before boundary
		},
	}
	got := unreadFromList(pl, boundary)
	want := []string{"a", "b"} // chronological, only unread non-system non-deleted
	if len(got) != len(want) {
		t.Fatalf("got %d posts, want %d: %+v", len(got), len(want), got)
	}
	for i, p := range got {
		if p.Message != want[i] {
			t.Errorf("post[%d] = %q, want %q", i, p.Message, want[i])
		}
	}
}

func TestDmPartnerID(t *testing.T) {
	cases := []struct {
		name, channelName, want string
	}{
		{"me first", "me__other", "other"},
		{"me second", "other__me", "other"},
		{"dm with self", "me__me", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dmPartnerID(&model.Channel{Name: c.channelName}, "me")
			if got != c.want {
				t.Errorf("dmPartnerID(%q) = %q, want %q", c.channelName, got, c.want)
			}
		})
	}
}

func TestLabelerHeader(t *testing.T) {
	chans := map[string]*model.Channel{
		"open":    {Id: "open", Type: model.ChannelTypeOpen, TeamId: "t1", Name: "general"},
		"noteam":  {Id: "noteam", Type: model.ChannelTypeOpen, TeamId: "tX", Name: "lonely"},
		"dm":      {Id: "dm", Type: model.ChannelTypeDirect, Name: "me__u2"},
		"dmgone":  {Id: "dmgone", Type: model.ChannelTypeDirect, Name: "me__u9"},
		"groupdm": {Id: "groupdm", Type: model.ChannelTypeGroup, DisplayName: "alice, bob"},
	}
	lbl := labeler{
		meID:     "me",
		teamSlug: map[string]string{"t1": "eng"},
		channels: chans,
		names:    map[string]string{"u2": "alice"},
	}
	cases := []struct{ id, want string }{
		{"open", "eng/general"},
		{"noteam", "lonely"}, // team slug unknown → bare channel slug
		{"dm", "@alice"},     // resolved DM partner
		{"dmgone", "@?"},     // unresolved DM partner
		{"groupdm", "alice, bob"},
		{"missing", "missing"}, // unknown channel id → echoed back
	}
	for _, c := range cases {
		if got := lbl.header(c.id); got != c.want {
			t.Errorf("header(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

func TestUnreadUserIDs(t *testing.T) {
	chans := map[string]*model.Channel{
		"c1": {Id: "c1", Type: model.ChannelTypeOpen},
		"dm": {Id: "dm", Type: model.ChannelTypeDirect, Name: "me__partner"},
	}
	groups := []unreadGroup{
		{channelID: "c1", posts: []*model.Post{{UserId: "a"}, {UserId: "b"}, {UserId: "a"}}},
		{channelID: "dm", posts: []*model.Post{{UserId: "partner"}}},
	}
	got := unreadUserIDs(groups, chans, "me")
	// authors a, b, partner — deduped, first-seen order; the DM partner is
	// already present as an author so it isn't added twice.
	want := []string{"a", "b", "partner"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestPrintUnread(t *testing.T) {
	chans := map[string]*model.Channel{
		"c1": {Id: "c1", Type: model.ChannelTypeOpen, TeamId: "t1", Name: "general"},
		"dm": {Id: "dm", Type: model.ChannelTypeDirect, Name: "me__u2"},
	}
	lbl := labeler{
		meID:     "me",
		teamSlug: map[string]string{"t1": "eng"},
		channels: chans,
		names:    map[string]string{"u1": "alice", "u2": "bob"},
	}
	groups := []unreadGroup{
		{channelID: "c1", posts: []*model.Post{{UserId: "u1", Message: "hi", CreateAt: ms(t, "09:00")}}, total: 1},
		{channelID: "dm", posts: []*model.Post{{UserId: "u2", Message: "yo", CreateAt: ms(t, "09:01")}}, total: 3, mention: 1, truncated: 2},
	}
	names := map[string]string{"u1": "alice", "u2": "bob"}

	var buf bytes.Buffer
	printUnread(&buf, lbl, groups, names)

	want := "" +
		"eng/general · 1 new\n" +
		"[09:00] @alice  hi\n" +
		"\n" +
		"@bob · 3 new · 1 mention\n" +
		"  … +2 earlier unread\n" +
		"[09:01] @bob  yo\n"
	if buf.String() != want {
		t.Errorf("printUnread mismatch:\n got:\n%q\nwant:\n%q", buf.String(), want)
	}
}
