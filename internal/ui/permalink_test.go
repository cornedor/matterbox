package ui

import (
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"matterbox/internal/store"
)

// A pair of syntactically valid (26-char, alphanumeric) Mattermost post ids.
const (
	pid  = "abcdefghijklmnopqrstuvwxyz"
	pid2 = "zyxwvutsrqponmlkjihgfedcba"
)

// TestParsePermalinkPostID recognises a /<team>/pl/<postID> link on our own
// server and rejects everything else.
func TestParsePermalinkPostID(t *testing.T) {
	m := mouseModel(nil)
	m.serverURL = "https://chat.example.com"

	cases := []struct {
		name string
		url  string
		want string // "" means "not a permalink"
	}{
		{"plain", "https://chat.example.com/myteam/pl/" + pid, pid},
		{"with query", "https://chat.example.com/myteam/pl/" + pid + "?a=b", pid},
		{"server subpath", "https://chat.example.com/mattermost/myteam/pl/" + pid, pid},
		{"host case-insensitive", "https://CHAT.example.com/t/pl/" + pid, pid},
		{"wrong host", "https://evil.example.com/t/pl/" + pid, ""},
		{"channel link, not a post", "https://chat.example.com/t/channels/town-square", ""},
		{"invalid id", "https://chat.example.com/t/pl/short", ""},
		{"not a url", "::::not a url", ""},
	}
	for _, c := range cases {
		got, ok := m.parsePermalinkPostID(c.url)
		if c.want == "" {
			if ok {
				t.Errorf("%s: parsed %q, want not-a-permalink", c.name, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s: got (%q,%v), want (%q,true)", c.name, got, ok, c.want)
		}
	}
}

// TestParsePermalinkNoServerURL: with no server configured the interception is
// disabled, so even a well-formed permalink is left for the browser.
func TestParsePermalinkNoServerURL(t *testing.T) {
	m := mouseModel(nil)
	m.serverURL = ""
	if _, ok := m.parsePermalinkPostID("https://chat.example.com/t/pl/" + pid); ok {
		t.Error("parsed a permalink with no server_url configured")
	}
}

// TestOpenTargetRoutesPermalink: openTarget — the single open path shared by the
// mouse click and the keyboard `o` / picker — turns an in-app permalink into a
// followPermalinkMsg (and leaves an ordinary link to the browser path).
func TestOpenTargetRoutesPermalink(t *testing.T) {
	m := mouseModel(nil)
	m.serverURL = "https://chat.example.com"

	// A permalink: emitted as a follow message, no browser status set.
	url := "https://chat.example.com/t1/pl/" + pid
	cmd := m.openTarget(openable{name: url, url: url})
	if cmd == nil {
		t.Fatal("permalink openTarget returned no command")
	}
	msg, ok := cmd().(followPermalinkMsg)
	if !ok {
		t.Fatalf("permalink openTarget emitted %T, want followPermalinkMsg", cmd())
	}
	if msg.postID != pid {
		t.Errorf("followPermalinkMsg.postID = %q, want %q", msg.postID, pid)
	}
	if m.status != "" {
		t.Errorf("permalink openTarget set status %q, want none", m.status)
	}

	// An ordinary external link takes the browser open path instead.
	ext := "https://example.org/page"
	if c := m.openTarget(openable{name: ext, url: ext}); c == nil {
		t.Error("external openTarget returned no command")
	}
	if m.status != "opening "+ext+"…" {
		t.Errorf("external openTarget status = %q, want %q", m.status, "opening "+ext+"…")
	}
}

// TestFollowPermalinkLoaded: a permalink to a post already in the open channel
// selects it in place — no team/channel switch, no browser command.
func TestFollowPermalinkLoaded(t *testing.T) {
	m := mouseModel([]*model.Post{
		{Id: "x", ChannelId: "c", CreateAt: 100, UserId: "u", Message: "first"},
		{Id: pid, ChannelId: "c", CreateAt: 200, UserId: "u", Message: "target"},
	})
	m.serverURL = "https://chat.example.com"

	out, cmd := m.followPermalink(pid, "https://chat.example.com/t1/pl/"+pid)
	mm := out.(Model)
	if cmd != nil {
		t.Error("expected no command for an in-place jump (would have opened the browser)")
	}
	if mm.openChannelID != "c" {
		t.Errorf("openChannelID = %q, want unchanged %q", mm.openChannelID, "c")
	}
	if mm.posts[mm.postIdx].Id != pid {
		t.Errorf("selected post %q, want %q", mm.posts[mm.postIdx].Id, pid)
	}
}

// TestFollowPermalinkCachedOtherChannel: a permalink to a post in another
// channel found in the local cache switches to that channel and centres on it,
// without going to the browser.
func TestFollowPermalinkCachedOtherChannel(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "pl.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertMany([]*model.Post{
		{Id: pid, ChannelId: "c2", UserId: "u", CreateAt: 500, UpdateAt: 500, Message: "over there"},
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	m := mouseModel([]*model.Post{{Id: "x", ChannelId: "c", CreateAt: 100, UserId: "u"}})
	m.store = st
	m.serverURL = "https://chat.example.com"

	out, _ := m.followPermalink(pid, "https://chat.example.com/t1/pl/"+pid)
	mm := out.(Model)
	if mm.openChannelID != "c2" {
		t.Errorf("openChannelID = %q, want %q", mm.openChannelID, "c2")
	}
	if len(mm.posts) == 0 || mm.posts[mm.postIdx].Id != pid {
		t.Errorf("did not centre on the target post; postIdx=%d posts=%v", mm.postIdx, mm.posts)
	}
	if mm.focus != focusMessages {
		t.Errorf("focus = %v, want focusMessages", mm.focus)
	}
}

// TestFollowPermalinkUnresolved: a permalink whose post is neither loaded nor
// cached defers to a server lookup (a command), not the browser.
func TestFollowPermalinkUnresolved(t *testing.T) {
	m := mouseModel([]*model.Post{{Id: "x", ChannelId: "c", CreateAt: 100, UserId: "u"}})
	m.serverURL = "https://chat.example.com"

	out, cmd := m.followPermalink(pid2, "https://chat.example.com/t1/pl/"+pid2)
	mm := out.(Model)
	if cmd == nil {
		t.Fatal("expected a resolve command for an unknown post")
	}
	if mm.status != "opening message…" {
		t.Errorf("status = %q, want %q", mm.status, "opening message…")
	}
}
