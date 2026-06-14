package listen

import (
	"encoding/json"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// postedEvent builds a "posted" websocket event carrying p, with optional extra
// data fields (mentions, channel_type, sender_name, channel_display_name) the
// server includes in the broadcast.
func postedEvent(t *testing.T, p *model.Post, data map[string]string) *model.WebSocketEvent {
	t.Helper()
	ev := model.NewWebSocketEvent(model.WebsocketEventPosted, "", p.ChannelId, "", nil, "")
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal post: %v", err)
	}
	ev.Add("post", string(b))
	for k, v := range data {
		ev.Add(k, v)
	}
	return ev
}

func mentionsData(t *testing.T, ids ...string) string {
	t.Helper()
	b, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("marshal mentions: %v", err)
	}
	return string(b)
}

func TestIsDirectMention(t *testing.T) {
	const meID, meName = "u-me", "corne"

	t.Run("dm from someone else triggers", func(t *testing.T) {
		p := &model.Post{Id: "p1", ChannelId: "d1", UserId: "u-bob", Message: "hey"}
		ev := postedEvent(t, p, map[string]string{"channel_type": "D"})
		if !isDirectMention(ev, p, meID, meName, false) {
			t.Fatal("want true for a DM from another user")
		}
	})

	t.Run("my own dm does not trigger", func(t *testing.T) {
		p := &model.Post{Id: "p2", ChannelId: "d1", UserId: meID, Message: "hey"}
		ev := postedEvent(t, p, map[string]string{"channel_type": "D"})
		if isDirectMention(ev, p, meID, meName, false) {
			t.Fatal("want false for my own post")
		}
	})

	t.Run("my own self-DM triggers with notifySelf (test affordance)", func(t *testing.T) {
		p := &model.Post{Id: "p2b", ChannelId: "d-self", UserId: meID, Message: "note to self"}
		ev := postedEvent(t, p, map[string]string{"channel_type": "D"})
		if !isDirectMention(ev, p, meID, meName, true) {
			t.Fatal("want true for my own self-DM when notifySelf is set")
		}
		// A system message still never triggers, even with notifySelf.
		sys := &model.Post{Id: "p2c", ChannelId: "d-self", UserId: meID, Message: "x", Type: model.PostTypeJoinChannel}
		evSys := postedEvent(t, sys, map[string]string{"channel_type": "D"})
		if isDirectMention(evSys, sys, meID, meName, true) {
			t.Fatal("want false for a system message even with notifySelf")
		}
	})

	t.Run("named @mention in a channel triggers", func(t *testing.T) {
		p := &model.Post{Id: "p3", ChannelId: "c1", UserId: "u-bob", Message: "@corne can you look?"}
		ev := postedEvent(t, p, map[string]string{
			"channel_type": "O",
			"mentions":     mentionsData(t, meID),
		})
		if !isDirectMention(ev, p, meID, meName, false) {
			t.Fatal("want true for an explicit @username mention")
		}
	})

	t.Run("@channel broadcast that resolves to me does not trigger", func(t *testing.T) {
		// The server lists me in mentions for @channel, but I'm not named.
		p := &model.Post{Id: "p4", ChannelId: "c1", UserId: "u-bob", Message: "@channel standup now"}
		ev := postedEvent(t, p, map[string]string{
			"channel_type": "O",
			"mentions":     mentionsData(t, meID, "u-x", "u-y"),
		})
		if isDirectMention(ev, p, meID, meName, false) {
			t.Fatal("want false for a broad @channel mention")
		}
	})

	t.Run("mention of someone else does not trigger", func(t *testing.T) {
		p := &model.Post{Id: "p5", ChannelId: "c1", UserId: "u-bob", Message: "@alice ping"}
		ev := postedEvent(t, p, map[string]string{
			"channel_type": "O",
			"mentions":     mentionsData(t, "u-alice"),
		})
		if isDirectMention(ev, p, meID, meName, false) {
			t.Fatal("want false when someone else is mentioned")
		}
	})

	t.Run("system / deleted / empty posts never trigger", func(t *testing.T) {
		base := map[string]string{"channel_type": "D"}
		cases := []*model.Post{
			{Id: "s1", ChannelId: "d1", UserId: "u-bob", Message: "joined", Type: model.PostTypeJoinChannel},
			{Id: "s2", ChannelId: "d1", UserId: "u-bob", Message: "x", DeleteAt: 123},
			{Id: "s3", ChannelId: "d1", UserId: "u-bob", Message: "   "},
		}
		for _, p := range cases {
			ev := postedEvent(t, p, base)
			if isDirectMention(ev, p, meID, meName, false) {
				t.Fatalf("want false for post %s", p.Id)
			}
		}
	})
}

func TestMentionsName(t *testing.T) {
	cases := []struct {
		msg, name string
		want      bool
	}{
		{"@corne hi", "corne", true},
		{"hey @Corne", "corne", true}, // case-insensitive
		{"cc @corne, thanks", "corne", true},
		{"@corner is not me", "corne", false}, // word boundary
		{"mailto corne@example.com", "corne", false},
		{"no mention here", "corne", false},
		{"@corne", "", false}, // empty username never matches
	}
	for _, c := range cases {
		if got := mentionsName(c.msg, c.name); got != c.want {
			t.Errorf("mentionsName(%q,%q)=%v want %v", c.msg, c.name, got, c.want)
		}
	}
}

func TestChannelLabel(t *testing.T) {
	dm := postedEvent(t, &model.Post{Id: "p", ChannelId: "d"}, map[string]string{
		"channel_type": "D",
		"sender_name":  "@bob",
	})
	if got := channelLabel(dm, ""); got != "DM from @bob" {
		t.Errorf("dm label = %q", got)
	}

	ch := postedEvent(t, &model.Post{Id: "p", ChannelId: "c"}, map[string]string{
		"channel_type":         "O",
		"sender_name":          "@bob",
		"channel_display_name": "Engineering",
	})
	if got := channelLabel(ch, ""); got != "@bob in Engineering" {
		t.Errorf("channel label = %q", got)
	}

	// Falls back to the resolved sender when the event omits sender_name.
	noSender := postedEvent(t, &model.Post{Id: "p", ChannelId: "d"}, map[string]string{"channel_type": "D"})
	if got := channelLabel(noSender, "@alice"); got != "DM from @alice" {
		t.Errorf("fallback label = %q", got)
	}
}

func TestTranscriptAndOrdering(t *testing.T) {
	names := map[string]string{"u1": "alice", "u2": "bob"}
	pl := &model.PostList{
		Posts: map[string]*model.Post{
			"b": {Id: "b", UserId: "u2", Message: "second", CreateAt: 2000},
			"a": {Id: "a", UserId: "u1", Message: "first", CreateAt: 1000},
			"s": {Id: "s", UserId: "u1", Message: "joined", CreateAt: 1500, Type: model.PostTypeJoinChannel},
		},
	}
	posts := postsByCreateAt(pl)
	if len(posts) != 3 || posts[0].Id != "a" || posts[2].Id != "b" {
		t.Fatalf("expected oldest-first a,s,b; got %v", ids(posts))
	}
	got := transcript(posts, names)
	want := "[" + tsOf(posts[0]) + "] @alice: first\n[" + tsOf(posts[2]) + "] @bob: second"
	if got != want {
		t.Errorf("transcript:\n got=%q\nwant=%q", got, want)
	}
}

func TestEnsureContains(t *testing.T) {
	posts := []*model.Post{
		{Id: "a", CreateAt: 1000},
		{Id: "b", CreateAt: 2000},
	}
	// Already present: unchanged.
	if out := ensureContains(posts, posts[0]); len(out) != 2 {
		t.Fatalf("present post should not be re-added, got %d", len(out))
	}
	// Newer live post: appended and sorted last.
	live := &model.Post{Id: "c", CreateAt: 3000}
	out := ensureContains(posts, live)
	if len(out) != 3 || out[2].Id != "c" {
		t.Fatalf("live post should sort last, got %v", ids(out))
	}
	// Older straggler: inserted in time order.
	older := &model.Post{Id: "z", CreateAt: 500}
	out2 := ensureContains([]*model.Post{{Id: "a", CreateAt: 1000}}, older)
	if len(out2) != 2 || out2[0].Id != "z" {
		t.Fatalf("older post should sort first, got %v", ids(out2))
	}
}

func ids(posts []*model.Post) []string {
	out := make([]string, len(posts))
	for i, p := range posts {
		out[i] = p.Id
	}
	return out
}

func tsOf(p *model.Post) string {
	return postLine(p, map[string]string{p.UserId: "x"})[1:6]
}
