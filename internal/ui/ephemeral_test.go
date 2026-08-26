package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// wsEphemeral builds the event the server sends for a post only we can see.
func wsEphemeral(t *testing.T, channelID, rootID, text string) *model.WebSocketEvent {
	t.Helper()
	ev := model.NewWebSocketEvent(model.WebsocketEventEphemeralMessage, "", channelID, "me", nil, "")
	p := &model.Post{
		Id:        "eph1",
		Type:      model.PostTypeEphemeral,
		ChannelId: channelID,
		RootId:    rootID,
		UserId:    "bot",
		Message:   text,
		CreateAt:  9999,
	}
	raw, err := p.ToJSON()
	if err != nil {
		t.Fatalf("encode post: %v", err)
	}
	ev.Add("post", raw)
	return ev
}

// The regression: a plugin answers, and the user used to get silence.
func TestEphemeralPostShowsInTheOpenChannel(t *testing.T) {
	m := resyncModel(t) // open on c1
	before := len(m.posts)

	m.handleWSEvent(wsEphemeral(t, "c1", "", "only you can see this"))

	if len(m.posts) != before+1 {
		t.Fatalf("posts = %d; want one more than %d", len(m.posts), before)
	}
	if got := m.posts[len(m.posts)-1].Message; !strings.Contains(got, "only you can see this") {
		t.Errorf("message = %q; want the ephemeral text", got)
	}
}

// Nothing else in the transcript is private, so say so.
func TestEphemeralPostIsMarkedPrivate(t *testing.T) {
	m := resyncModel(t)

	m.handleWSEvent(wsEphemeral(t, "c1", "", "psst"))

	if got := m.posts[len(m.posts)-1].Message; !strings.Contains(got, "only visible to you") {
		t.Errorf("message = %q; want the visibility note", got)
	}
}

// It exists nowhere but this transcript. Caching it would resurrect it on every
// warm open and in search, with no refetch able to remove it again.
func TestEphemeralPostIsNeverPersisted(t *testing.T) {
	m := resyncModel(t)
	m.store = openSeededStore(t)

	if cmd := m.persistPosts(&model.Post{
		Id: "eph1", Type: model.PostTypeEphemeral, ChannelId: "c1", Message: "psst",
	}); cmd != nil {
		t.Error("persistPosts accepted an ephemeral post")
	}
}

// A real post alongside an ephemeral still has to be cached.
func TestPersistKeepsRealPostsBesideAnEphemeral(t *testing.T) {
	m := resyncModel(t)
	m.store = openSeededStore(t)

	cmd := m.persistPosts(
		&model.Post{Id: "eph1", Type: model.PostTypeEphemeral, ChannelId: "c1", Message: "psst"},
		&model.Post{Id: "p1", ChannelId: "c1", UserId: "u", Message: "real", CreateAt: 100},
	)
	if cmd == nil {
		t.Fatal("the real post was dropped along with the ephemeral")
	}
	cmd()

	got, err := m.store.RecentForChannel("c1", 10, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, p := range got {
		if p.Id == "eph1" {
			t.Error("ephemeral reached the cache")
		}
	}
	if len(got) != 1 || got[0].Id != "p1" {
		t.Errorf("cached %d posts; want just the real one", len(got))
	}
}

// There is no read state behind an ephemeral — it was never on the server — so
// it must not move a badge.
func TestEphemeralPostDoesNotCountUnread(t *testing.T) {
	m := resyncModel(t)
	m.openChannelID = "c0" // c1 is a background channel now
	delete(m.unread, "c1")

	m.handleWSEvent(wsEphemeral(t, "c1", "", "psst"))

	if n := m.unread["c1"]; n != 0 {
		t.Errorf("unread = %d; want the ephemeral not to count", n)
	}
}

// matterbox runs commands in the open channel, so a reply for anywhere else has
// no home. Dropping it beats grafting it onto the wrong conversation.
func TestEphemeralPostForAnotherChannelIsDropped(t *testing.T) {
	m := resyncModel(t)
	before := len(m.posts)

	m.handleWSEvent(wsEphemeral(t, "c0", "", "psst"))

	if len(m.posts) != before {
		t.Errorf("posts = %d; want the open channel's %d untouched", len(m.posts), before)
	}
}
