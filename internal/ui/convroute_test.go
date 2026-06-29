package ui

import (
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
)

// These tests probe the "open-conversation invariant": after any navigation
// that swaps the visible posts, the routing id (m.openChannelID), the visible
// posts, and the composer target must all agree on which channel the user is
// looking at. When they drift, a reply (and the title, gap-fill, read dwell)
// silently bind to the *previous* channel — the "I replied and it went to the
// wrong place" bug fixed for the search-jump path in 79e1de3.
//
// composerTarget() is the exact derivation the Enter-send handler uses for a
// non-thread message (update.go: channelID = m.openChannelID), so asserting on
// it is a faithful proxy for where a reply actually lands.

// openSeededStore opens a throwaway cache pre-loaded with posts.
func openSeededStore(t *testing.T, posts ...*model.Post) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "conv.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if len(posts) > 0 {
		if err := st.UpsertMany(posts); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}
	return st
}

// assertRoutesTo checks the open-conversation invariant: openChannelID, the
// visible posts, and the composer target all point at want.
func assertRoutesTo(t *testing.T, m Model, want string) {
	t.Helper()
	if m.openChannelID != want {
		t.Errorf("openChannelID = %q, want %q", m.openChannelID, want)
	}
	for _, p := range m.posts {
		if p.ChannelId != "" && p.ChannelId != want {
			t.Errorf("visible posts include channel %q, want all in %q", p.ChannelId, want)
			break
		}
	}
	if ch, _ := m.composerTarget(); ch != want {
		t.Errorf("composerTarget routes to %q, want %q — a reply would land in the wrong channel", ch, want)
	}
}

// mouseModel parks us on channel "c" (team t1, channels c + c2) with an editor
// composer; that's the "a channel is already open" starting point every
// navigation below jumps away from.

// TestConvRoute_SearchJumpCache: jumping to a search hit in another channel via
// the cache fast-path must repoint routing at the hit's channel. (The path the
// original bug lived in; guards the fix.)
func TestConvRoute_SearchJumpCache(t *testing.T) {
	st := openSeededStore(t, &model.Post{Id: "c2p", ChannelId: "c2", UserId: "u", CreateAt: 500, UpdateAt: 500, Message: "over there"})
	m := mouseModel([]*model.Post{{Id: "cp", ChannelId: "c", UserId: "u", CreateAt: 100, Message: "here"}})
	m.store = st
	m.me = &model.User{Id: "me"}

	out, _ := m.openHitChannel(store.SearchHit{Match: &model.Post{Id: "c2p", ChannelId: "c2"}})
	assertRoutesTo(t, out.(Model), "c2")
}

// TestConvRoute_SearchJumpFallback: when the cache can't satisfy the jump the
// fallback goes through openChannelLoadCmd, which repoints routing synchronously
// before returning its load command. (Posts are nil here; the openChannelID /
// composerTarget half of the invariant still must hold.)
func TestConvRoute_SearchJumpFallback(t *testing.T) {
	// Seed only the *current* channel, so PostsAround("c2", …) comes back empty
	// and openHitChannel takes the fallback rather than the cache fast-path.
	st := openSeededStore(t, &model.Post{Id: "cp", ChannelId: "c", UserId: "u", CreateAt: 100, UpdateAt: 100})
	m := mouseModel([]*model.Post{{Id: "cp", ChannelId: "c", UserId: "u", CreateAt: 100}})
	m.store = st
	m.me = &model.User{Id: "me"}

	out, _ := m.openHitChannel(store.SearchHit{Match: &model.Post{Id: "ghost", ChannelId: "c2"}})
	mm := out.(Model)
	if mm.openChannelID != "c2" {
		t.Fatalf("fallback openChannelID = %q, want c2", mm.openChannelID)
	}
	if ch, _ := mm.composerTarget(); ch != "c2" {
		t.Errorf("fallback composerTarget = %q, want c2 — a reply would mis-route", ch)
	}
}

// TestConvRoute_PermalinkCache: following an in-app permalink to a post in
// another channel (cache fast-path) must repoint routing at that channel.
func TestConvRoute_PermalinkCache(t *testing.T) {
	st := openSeededStore(t, &model.Post{Id: pid, ChannelId: "c2", UserId: "u", CreateAt: 500, UpdateAt: 500, Message: "over there"})
	m := mouseModel([]*model.Post{{Id: "x", ChannelId: "c", CreateAt: 100, UserId: "u"}})
	m.store = st
	m.serverURL = "https://chat.example.com"

	out, _ := m.followPermalink(pid, "https://chat.example.com/t1/pl/"+pid)
	assertRoutesTo(t, out.(Model), "c2")
}

// TestConvRoute_JumpToChannelPostForeign PROBES the latent landmine:
// jumpToChannelPost's cache branch swaps the visible posts for any channel id
// but never repoints openChannelID — it only half-trusts the "channelID is the
// open channel" precondition (the in-window branch checks it; the cache branch
// doesn't). Its sole foreign-capable caller (jumpToInfoPin) happens to pass
// infoChannelID, which openChannelInfo pins to openChannelID, so today it's
// reachable only by contract. The moment any caller jumps cross-channel, a
// reply mis-routes. This test asserts the *correct* behaviour and is expected
// to FAIL against current code — that's the signal we want.
func TestConvRoute_JumpToChannelPostForeign(t *testing.T) {
	st := openSeededStore(t, &model.Post{Id: "c2p", ChannelId: "c2", UserId: "u", CreateAt: 500, UpdateAt: 500, Message: "pinned"})
	m := mouseModel([]*model.Post{{Id: "cp", ChannelId: "c", UserId: "u", CreateAt: 100}})
	m.store = st

	out, _ := m.jumpToChannelPost("c2", "c2p") // jump to a post in a *different* channel
	mm := out.(Model)
	if len(mm.posts) == 0 || mm.posts[mm.postIdx].ChannelId != "c2" {
		t.Fatalf("did not land on the c2 post; posts=%v", mm.posts)
	}
	// The pane now shows c2, so routing must too.
	assertRoutesTo(t, mm, "c2")
}
