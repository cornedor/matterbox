package ui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

func prefsEvent(t *testing.T, typ model.WebsocketEventType, prefs ...model.Preference) *model.WebSocketEvent {
	t.Helper()
	b, err := json.Marshal(prefs)
	if err != nil {
		t.Fatal(err)
	}
	ev := model.NewWebSocketEvent(typ, "", "", "u-me", nil, "")
	ev.Add("preferences", string(b))
	return ev
}

// The saved set is seeded from the flagged_post preferences and then follows
// preferences_changed / preferences_deleted for that category only — the same
// events the server echoes for our own toggles, and sends for saves made in
// another client.
func TestSavedSetFollowsPreferenceEvents(t *testing.T) {
	m := newRenderableModel()
	m.applySavedIDsLoaded(savedIDsLoadedMsg{ids: []string{"p1", "p2"}})
	if !m.isSaved("p1") || !m.isSaved("p2") || m.isSaved("p3") {
		t.Fatalf("seed: %v", m.savedPostIDs)
	}
	m.applyPreferencesEvent(prefsEvent(t, model.WebsocketEventPreferencesChanged,
		model.Preference{Category: model.PreferenceCategoryFlaggedPost, Name: "p3", Value: "true"},
		model.Preference{Category: "display_settings", Name: "p9", Value: "x"}, // other category: ignored
	), true)
	if !m.isSaved("p3") || m.isSaved("p9") {
		t.Fatalf("after changed: %v", m.savedPostIDs)
	}
	m.applyPreferencesEvent(prefsEvent(t, model.WebsocketEventPreferencesDeleted,
		model.Preference{Category: model.PreferenceCategoryFlaggedPost, Name: "p1"},
	), false)
	if m.isSaved("p1") || !m.isSaved("p2") || !m.isSaved("p3") {
		t.Fatalf("after deleted: %v", m.savedPostIDs)
	}
	// A failed seed keeps the set as it was (here: as loaded) rather than wiping it.
	m.applySavedIDsLoaded(savedIDsLoadedMsg{err: errors.New("boom")})
	if !m.isSaved("p2") {
		t.Fatal("a failed reload must not drop the known saved set")
	}
	// The events also arrive through the websocket dispatcher.
	m.savedPostIDs = nil
	m.handleWSEvent(prefsEvent(t, model.WebsocketEventPreferencesChanged,
		model.Preference{Category: model.PreferenceCategoryFlaggedPost, Name: "p5", Value: "true"}))
	if !m.isSaved("p5") {
		t.Fatal("preferences_changed via handleWSEvent should update the saved set")
	}
}

// The save toggle: label from the set, optimistic flip on run (mark, label
// and cached rows update before the round-trip), revert on error.
func TestSaveCommandToggles(t *testing.T) {
	m := newRenderableModel()
	m.me = &model.User{Id: "u-me"}
	p := &model.Post{Id: "p1", ChannelId: "c1", Message: "hi", UserId: "u1"}
	m.posts = []*model.Post{p}
	m.postIdx = 0
	m.focus = focusMessages
	if name := m.saveCommand().name; name != "Save message" {
		t.Fatalf("unsaved: %q", name)
	}
	if cmd := runToggleSaved(&m, ""); cmd == nil {
		t.Fatal("expected the server command")
	}
	if !m.isSaved("p1") || m.saveCommand().name != "Unsave message" || m.status != "saving message…" {
		t.Fatalf("after run: saved=%v label=%q status=%q", m.isSaved("p1"), m.saveCommand().name, m.status)
	}
	if fp := m.postLineFingerprint(p, 60, false, false, false); !strings.Contains(fp, "S") {
		t.Fatalf("saved state must be part of the row-cache fingerprint, got %q", fp)
	}
	next, _ := m.applySavedChanged(savedChangedMsg{postID: "p1", saved: true, err: errors.New("boom")})
	got := next.(Model)
	if got.isSaved("p1") || got.status != "boom" {
		t.Fatalf("failed save should revert: saved=%v status=%q", got.isSaved("p1"), got.status)
	}
	// Composer focus: still listed, but a run acts on nothing.
	got.focus = focusInput
	if cmd := runToggleSaved(&got, ""); cmd != nil || got.status != "no message selected" {
		t.Fatalf("no selection: cmd=%v status=%q", cmd != nil, got.status)
	}
}

// The browser: enter jumps to the message in its channel; d unsaves the row
// (dropping it from the list and the set, cursor kept in range).
func TestSavedPostsBrowserKeys(t *testing.T) {
	ch := &model.Channel{Id: "c1", TeamId: "t1", DisplayName: "general", Name: "general", Type: model.ChannelTypeOpen}
	m := newRenderableModel()
	m.me = &model.User{Id: "u-me"}
	m.teams = []*model.Team{{Id: "t1", Name: "eng"}}
	m.channels = map[string][]*model.Channel{"t1": {ch}}
	m.teamIdx = m.firstTeamTabIdx()
	cmd := m.openSavedPosts()
	if cmd == nil || !m.savedPosts.active || !m.savedPosts.loading {
		t.Fatalf("openSavedPosts: cmd=%v state=%+v", cmd != nil, m.savedPosts)
	}
	// The loaded page seeds the saved set (the preference fetch may have
	// failed at startup); the sheet lists it.
	next, _ := m.applySavedPostsLoaded(savedPostsLoadedMsg{gen: m.savedPosts.gen, items: []*model.Post{
		{Id: "s1", ChannelId: "c1", Message: "first"},
		{Id: "s2", ChannelId: "c1", Message: "second"},
	}})
	m = next.(Model)
	if m.savedPosts.loading || len(m.visibleSaved()) != 2 || !m.isSaved("s1") || !m.isSaved("s2") {
		t.Fatalf("loaded: %+v set=%v", m.savedPosts, m.savedPostIDs)
	}
	if !strings.Contains(m.renderSavedPosts(), "first") {
		t.Fatal("render should list the saved messages")
	}

	// ↓ to s2, d unsaves it: gone from the sheet and the set.
	next, _ = m.handleSavedPostsKey(tea.KeyPressMsg{Code: tea.KeyDown})
	next, cmd = next.(Model).handleSavedPostsKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = next.(Model)
	if vis := m.visibleSaved(); cmd == nil || len(vis) != 1 || vis[0].post.Id != "s1" {
		t.Fatalf("after d: cmd=%v visible=%d", cmd != nil, len(vis))
	}
	if m.isSaved("s2") || !m.isSaved("s1") {
		t.Fatalf("set after d: %v", m.savedPostIDs)
	}
	if !strings.Contains(m.renderSavedPosts(), "first") || strings.Contains(m.renderSavedPosts(), "second") {
		t.Fatal("render should drop the unsaved row")
	}
	// The unsave failing brings the row back — the sheet reads the set.
	next, _ = m.applySavedChanged(savedChangedMsg{postID: "s2", saved: false, err: errors.New("boom")})
	m = next.(Model)
	if len(m.visibleSaved()) != 2 || !strings.Contains(m.renderSavedPosts(), "second") {
		t.Fatal("a failed unsave should restore the row in the sheet")
	}
	// …as does an unsave echoed from another client.
	m.applyPreferencesEvent(prefsEvent(t, model.WebsocketEventPreferencesDeleted,
		model.Preference{Category: model.PreferenceCategoryFlaggedPost, Name: "s2"}), false)
	if len(m.visibleSaved()) != 1 {
		t.Fatal("an unsave from another client should drop the row in the sheet")
	}

	// enter opens s1's channel at that post and closes the browser.
	next, _ = m.handleSavedPostsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if m.savedPosts.active {
		t.Fatal("enter should close the browser")
	}
	if m.openChannelID != "c1" || m.pendingJumpPostID != "s1" {
		t.Fatalf("enter should open c1 at s1: open=%q jump=%q", m.openChannelID, m.pendingJumpPostID)
	}
}

// A load that returns after the browser was closed must not reopen it, and
// one from a previous open (closed and reopened before it landed) must not
// overwrite the newer sheet.
func TestSavedPostsStaleLoads(t *testing.T) {
	m := newRenderableModel()
	next, _ := m.applySavedPostsLoaded(savedPostsLoadedMsg{gen: 0, items: []*model.Post{{Id: "s1"}}})
	if got := next.(Model); got.savedPosts.active || len(got.savedPosts.items) != 0 {
		t.Fatalf("stale load reopened the browser: %+v", got.savedPosts)
	}
	m.me = &model.User{Id: "u-me"}
	m.openSavedPosts()
	first := m.savedPosts.gen
	m.closeSavedPosts()
	m.openSavedPosts()
	next, _ = m.applySavedPostsLoaded(savedPostsLoadedMsg{gen: first, items: []*model.Post{{Id: "old"}}})
	if got := next.(Model); !got.savedPosts.loading || len(got.savedPosts.items) != 0 {
		t.Fatalf("first open's fetch landed on the second sheet: %+v", got.savedPosts)
	}
	next, _ = m.applySavedPostsLoaded(savedPostsLoadedMsg{gen: m.savedPosts.gen, items: []*model.Post{{Id: "new"}}})
	if got := next.(Model); got.savedPosts.loading || len(got.savedPosts.items) != 1 {
		t.Fatalf("current fetch should land: %+v", got.savedPosts)
	}
}
