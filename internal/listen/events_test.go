package listen

import (
	"encoding/json"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/telegram"
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

func TestParseQuietHours(t *testing.T) {
	cases := []struct {
		in         string
		start, end int
		ok         bool
	}{
		{"22:00-08:00", 1320, 480, true},
		{"08:00-22:00", 480, 1320, true},
		{" 9:5 - 10:30 ", 545, 630, true},
		{"", 0, 0, false},
		{"22:00", 0, 0, false},
		{"25:00-08:00", 0, 0, false},
		{"22:60-08:00", 0, 0, false},
		{"garbage", 0, 0, false},
	}
	for _, c := range cases {
		s, e, ok := parseQuietHours(c.in)
		if ok != c.ok || (ok && (s != c.start || e != c.end)) {
			t.Errorf("parseQuietHours(%q)=(%d,%d,%v) want (%d,%d,%v)", c.in, s, e, ok, c.start, c.end, c.ok)
		}
	}
}

func TestInQuietHours(t *testing.T) {
	// Wrapping window 22:00–08:00.
	wrap := func(m int) bool { return inQuietHours(m, 1320, 480) }
	if !wrap(1380) || !wrap(300) || !wrap(1320) { // 23:00, 05:00, 22:00 (start incl)
		t.Error("wrap window should include late-night and pre-dawn")
	}
	if wrap(600) || wrap(480) { // 10:00, 08:00 (end excl)
		t.Error("wrap window should exclude daytime and the end boundary")
	}
	// Non-wrapping window 08:00–22:00.
	day := func(m int) bool { return inQuietHours(m, 480, 1320) }
	if !day(600) || !day(480) || day(400) || day(1320) {
		t.Error("day window boundaries wrong")
	}
	// Empty window is never quiet.
	if inQuietHours(600, 0, 0) {
		t.Error("zero-length window should never be quiet")
	}
}

func TestDecodeCallback(t *testing.T) {
	cases := map[string][2]string{
		"k:abc":     {"k", "abc"},
		"r:chan123": {"r", "chan123"},
		"noColon":   {"noColon", ""},
		"a:b:c":     {"a", "b:c"},
	}
	for in, want := range cases {
		a, arg := decodeCallback(in)
		if a != want[0] || arg != want[1] {
			t.Errorf("decodeCallback(%q)=(%q,%q) want (%q,%q)", in, a, arg, want[0], want[1])
		}
	}
}

func TestParseCommand(t *testing.T) {
	cases := []struct{ in, cmd, args string }{
		{"/search foo bar", "search", "foo bar"},
		{"/unread", "unread", ""},
		{"/Digest", "digest", ""},
		{"/help@matterbox_bridge_bot", "help", ""},
		{"  /s   x  ", "s", "x"},
		{"hello", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		cmd, args := parseCommand(c.in)
		if cmd != c.cmd || args != c.args {
			t.Errorf("parseCommand(%q)=(%q,%q) want (%q,%q)", c.in, cmd, args, c.cmd, c.args)
		}
	}
}

func TestMattermostEmojiName(t *testing.T) {
	want := map[string]string{
		"👍":  "+1",
		"❤️": "heart", // with variation selector
		"❤":  "heart", // without
		"🔥":  "fire",
		"🎉":  "tada",
	}
	for in, exp := range want {
		if got, ok := mattermostEmojiName(in); !ok || got != exp {
			t.Errorf("mattermostEmojiName(%q)=(%q,%v) want %q", in, got, ok, exp)
		}
	}
	if got, ok := mattermostEmojiName("not-an-emoji"); ok {
		t.Errorf("expected no mapping for a non-emoji, got %q", got)
	}
}

func TestReactionEmojiDiff(t *testing.T) {
	em := func(e string) telegram.ReactionType { return telegram.ReactionType{Type: "emoji", Emoji: e} }

	added, removed := reactionEmojiDiff([]telegram.ReactionType{em("👍")}, []telegram.ReactionType{em("👍"), em("🔥")})
	if len(added) != 1 || added[0] != "🔥" || len(removed) != 0 {
		t.Errorf("add case: added=%v removed=%v", added, removed)
	}

	added, removed = reactionEmojiDiff([]telegram.ReactionType{em("👍")}, nil)
	if len(removed) != 1 || removed[0] != "👍" || len(added) != 0 {
		t.Errorf("remove case: added=%v removed=%v", added, removed)
	}

	// Non-emoji reaction types are ignored.
	added, _ = reactionEmojiDiff(nil, []telegram.ReactionType{{Type: "custom_emoji"}})
	if len(added) != 0 {
		t.Errorf("custom_emoji should be ignored, got %v", added)
	}
}

func TestInboundFile(t *testing.T) {
	// Plain text: nothing to forward.
	if _, _, ok := inboundFile(&telegram.Message{Text: "hi"}); ok {
		t.Error("text-only message should have no file")
	}
	// Photo: highest-resolution size, no filename (derived later from the path).
	id, name, ok := inboundFile(&telegram.Message{Photo: []telegram.PhotoSize{
		{FileID: "small", FileSize: 100},
		{FileID: "big", FileSize: 9000},
	}})
	if !ok || id != "big" || name != "" {
		t.Errorf("photo: id=%q name=%q ok=%v", id, name, ok)
	}
	// Document: its own file id and filename.
	id, name, ok = inboundFile(&telegram.Message{Document: &telegram.Document{FileID: "doc1", FileName: "report.pdf"}})
	if !ok || id != "doc1" || name != "report.pdf" {
		t.Errorf("document: id=%q name=%q ok=%v", id, name, ok)
	}
	// Document takes precedence over a photo (an image sent "as a file").
	id, _, ok = inboundFile(&telegram.Message{
		Document: &telegram.Document{FileID: "doc1"},
		Photo:    []telegram.PhotoSize{{FileID: "p1", FileSize: 1}},
	})
	if !ok || id != "doc1" {
		t.Errorf("document should win over photo, got id=%q", id)
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

func TestHandleStatusChange(t *testing.T) {
	e := &Engine{
		me:   &model.User{Id: "u-me"},
		opts: Options{RespectDND: true},
	}
	ev := model.NewWebSocketEvent(model.WebsocketEventStatusChange, "", "", "u-me", nil, "")
	ev.Add("user_id", "u-me")
	ev.Add("status", model.StatusDnd)
	e.handle(t.Context(), ev)
	if e.myStatus != model.StatusDnd {
		t.Fatalf("own status_change: myStatus = %q, want dnd", e.myStatus)
	}

	other := model.NewWebSocketEvent(model.WebsocketEventStatusChange, "", "", "u-bob", nil, "")
	other.Add("user_id", "u-bob")
	other.Add("status", model.StatusOnline)
	e.handle(t.Context(), other)
	if e.myStatus != model.StatusDnd {
		t.Fatalf("other user's status_change overwrote myStatus = %q", e.myStatus)
	}

	e.opts.RespectDND = false
	e.myStatus = ""
	e.handle(t.Context(), ev)
	if e.myStatus != "" {
		t.Fatalf("respect_dnd off should ignore status_change, myStatus = %q", e.myStatus)
	}
}
