package ui

import (
	"encoding/json"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// draftEvent builds a draft_* WebSocket event carrying the given draft, the
// way the server broadcasts it (JSON under the "draft" data key).
func draftEvent(evType model.WebsocketEventType, d *model.Draft) *model.WebSocketEvent {
	ev := model.NewWebSocketEvent(evType, "", "", "me", nil, "")
	b, _ := json.Marshal(d)
	ev.Add("draft", string(b))
	return ev
}

// TestApplyDraftWSSyncsBackgroundChannels: a draft broadcast for a non-open
// channel updates the map; a delete clears it. The open channel is left alone
// so a live echo can't clobber active typing.
func TestApplyDraftWSSyncsBackgroundChannels(t *testing.T) {
	m := navModel() // openChannelID = c1

	// Update for a background channel lands in the map.
	m.applyDraftUpserted(draftEvent(model.WebsocketEventDraftUpdated,
		&model.Draft{ChannelId: "c2", Message: "from phone"}))
	if got := m.drafts["c2"]; got != "from phone" {
		t.Fatalf("background draft not synced: drafts[c2] = %q", got)
	}

	// Update for the open channel is ignored (local composer wins).
	m.input.SetValue("typing here")
	m.applyDraftUpserted(draftEvent(model.WebsocketEventDraftUpdated,
		&model.Draft{ChannelId: "c1", Message: "echo"}))
	if got := m.input.Value(); got != "typing here" {
		t.Fatalf("open-channel draft event clobbered the composer: %q", got)
	}
	if _, ok := m.drafts["c1"]; ok {
		t.Fatalf("open-channel draft event wrote to the map, want it skipped")
	}

	// Thread drafts (RootId set) are not channel drafts — ignored.
	m.applyDraftUpserted(draftEvent(model.WebsocketEventDraftCreated,
		&model.Draft{ChannelId: "c3", RootId: "r1", Message: "reply"}))
	if _, ok := m.drafts["c3"]; ok {
		t.Fatalf("thread draft event was stored as a channel draft")
	}

	// Delete clears a background channel's draft.
	m.applyDraftDeleted(draftEvent(model.WebsocketEventDraftDeleted,
		&model.Draft{ChannelId: "c2"}))
	if _, ok := m.drafts["c2"]; ok {
		t.Fatalf("draft delete event didn't clear drafts[c2]")
	}
}

// TestSwapChannelDraftRoundTrip: leaving a channel with text stashes it as
// that channel's draft and clears the composer for the next channel; returning
// restores it. This is the core per-channel behaviour.
func TestSwapChannelDraftRoundTrip(t *testing.T) {
	m := navModel()
	m.focus = focusInput
	m.input.SetValue("hello c1")

	// Leave c1 for c2.
	m.swapChannelDraft("c2")
	m.openChannelID = "c2"
	if got := m.input.Value(); got != "" {
		t.Fatalf("after leaving c1, composer = %q, want empty (c2 has no draft)", got)
	}
	if got := m.drafts["c1"]; got != "hello c1" {
		t.Fatalf("c1 draft not stashed: drafts[c1] = %q", got)
	}

	// Type something in c2, then go back to c1.
	m.input.SetValue("hello c2")
	m.swapChannelDraft("c1")
	m.openChannelID = "c1"
	if got := m.input.Value(); got != "hello c1" {
		t.Fatalf("returning to c1 didn't restore its draft: composer = %q", got)
	}
	if got := m.drafts["c2"]; got != "hello c2" {
		t.Fatalf("c2 draft not stashed: drafts[c2] = %q", got)
	}
}

// TestSwapChannelDraftSameChannelNoop: reopening the open channel doesn't
// disturb the composer or the draft map.
func TestSwapChannelDraftSameChannelNoop(t *testing.T) {
	m := navModel()
	m.input.SetValue("typing")
	if cmd := m.swapChannelDraft("c1"); cmd != nil {
		t.Fatalf("swap to the already-open channel returned a cmd, want nil")
	}
	if got := m.input.Value(); got != "typing" {
		t.Fatalf("same-channel swap touched the composer: %q", got)
	}
	if _, ok := m.drafts["c1"]; ok {
		t.Fatalf("same-channel swap stashed a draft, want none")
	}
}

// TestSwapChannelDraftSkipsThreadAndEdit: a thread reply or an in-progress
// edit owns the composer, so a channel switch must not stash/restore over it.
func TestSwapChannelDraftSkipsThreadAndEdit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(m *Model)
	}{
		{"thread", func(m *Model) { m.threadOpen = true }},
		{"edit", func(m *Model) { m.editingPostID = "p1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := navModel()
			m.input.SetValue("reply text")
			tc.setup(&m)
			if cmd := m.swapChannelDraft("c2"); cmd != nil {
				t.Fatalf("%s: swap returned a cmd, want nil (no channel draft)", tc.name)
			}
			if got := m.input.Value(); got != "reply text" {
				t.Fatalf("%s: swap clobbered the composer: %q", tc.name, got)
			}
			if len(m.drafts) != 0 {
				t.Fatalf("%s: swap stashed a draft, want none", tc.name)
			}
		})
	}
}

// TestStashDraftDeletesWhenEmptied: emptying a draft drops the stored copy so
// the map only ever holds channels with real pending text.
func TestStashDraftDeletesWhenEmptied(t *testing.T) {
	m := navModel()
	m.stashDraft("c1", "something")
	if m.drafts["c1"] != "something" {
		t.Fatalf("stash didn't record the draft")
	}
	m.stashDraft("c1", "   ") // whitespace-only counts as empty
	if _, ok := m.drafts["c1"]; ok {
		t.Fatalf("emptying the draft left it in the map")
	}
}

// TestClearDraftOnSend: a channel send drops the saved draft.
func TestClearDraftOnSend(t *testing.T) {
	m := navModel()
	m.drafts = map[string]string{"c1": "draft"}
	m.clearDraft("c1")
	if _, ok := m.drafts["c1"]; ok {
		t.Fatalf("clearDraft left the draft in the map")
	}
}

// TestApplyDraftsLoadedSeedsOpenComposer: a freshly-fetched draft for the open
// channel populates an empty composer, but never clobbers in-progress typing.
func TestApplyDraftsLoadedSeedsOpenComposer(t *testing.T) {
	// Empty composer: the loaded draft is shown.
	m := navModel() // openChannelID = c1
	m.applyDraftsLoaded(draftsLoadedMsg{drafts: map[string]string{"c1": "saved", "c2": "other"}})
	if got := m.input.Value(); got != "saved" {
		t.Fatalf("loaded draft not shown in empty composer: %q", got)
	}
	if got := m.drafts["c2"]; got != "other" {
		t.Fatalf("background draft not stored: drafts[c2] = %q", got)
	}

	// Non-empty composer: don't clobber what the user already typed.
	m2 := navModel()
	m2.input.SetValue("mine")
	m2.applyDraftsLoaded(draftsLoadedMsg{drafts: map[string]string{"c1": "saved"}})
	if got := m2.input.Value(); got != "mine" {
		t.Fatalf("loaded draft clobbered in-progress text: %q", got)
	}
}
