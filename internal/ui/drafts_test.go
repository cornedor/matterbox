package ui

import (
	"encoding/json"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/languagetool"
)

// grammarNavModel is navModel with grammar enabled (a fake LanguageTool
// endpoint), so the transient composer state can be checked across channel
// switches without a live server.
func grammarNavModel() Model {
	m := navModel()
	m.grammar = newGrammarState()
	m.ltClient = languagetool.New("http://localhost:8010/v2", "auto", false, 0)
	return m
}

// TestChannelSwitchClearsStaleGrammar: leaving a channel drops the grammar
// findings (and any open suggestion popup) bound to its draft, so they can't
// paint over the next channel's text at the wrong offsets.
func TestChannelSwitchClearsStaleGrammar(t *testing.T) {
	m := grammarNavModel() // on c1
	m.input.SetValue("som sentnce")
	m.setGrammarMatches("som sentnce", []languagetool.Match{
		{Offset: 0, Length: 3, IssueType: "misspelling"},
	})
	m.grammar.popup = true

	m.swapChannelDraft("c2") // c2 has no draft → composer clears
	m.openChannelID = "c2"

	if m.grammar.matches != nil {
		t.Fatalf("stale grammar matches survived the switch: %+v", m.grammar.matches)
	}
	if m.grammar.checkedText != "" {
		t.Fatalf("grammar checkedText not reset: %q", m.grammar.checkedText)
	}
	if m.grammar.popup {
		t.Fatalf("grammar popup left open after switch")
	}
}

// TestChannelSwitchRechecksRestoredDraft: switching to a channel that has a
// saved draft restores it and arms a fresh grammar check for that text —
// rather than carrying the previous channel's findings or leaving the restored
// draft unchecked.
func TestChannelSwitchRechecksRestoredDraft(t *testing.T) {
	m := grammarNavModel() // on c1, empty composer
	m.drafts = map[string]string{"c2": "som draft"}
	// Pretend the old channel had findings, to prove they don't linger.
	m.setGrammarMatches("stale", []languagetool.Match{{Offset: 0, Length: 5}})
	seqBefore := m.grammar.seq

	cmd := m.swapChannelDraft("c2")
	m.openChannelID = "c2"

	if got := m.input.Value(); got != "som draft" {
		t.Fatalf("draft not restored: composer = %q", got)
	}
	if m.grammar.checkedText != "" || m.grammar.matches != nil {
		t.Fatalf("restored draft kept stale findings: checkedText=%q matches=%+v",
			m.grammar.checkedText, m.grammar.matches)
	}
	if m.grammar.seq == seqBefore {
		t.Fatalf("no grammar check armed for the restored draft (seq still %d)", m.grammar.seq)
	}
	if cmd == nil {
		t.Fatalf("swap returned no cmd; expected a grammar-check tick")
	}
}

// TestNavKeyClearsGrammar: the same clearing happens end-to-end through a real
// ctrl+j nav keypress, not just the helper.
func TestNavKeyClearsGrammar(t *testing.T) {
	m := grammarNavModel()
	m.focus = focusInput
	m.input.Focus()
	m.input.SetValue("som sentnce")
	m.setGrammarMatches("som sentnce", []languagetool.Match{
		{Offset: 0, Length: 3, IssueType: "misspelling"},
	})

	out, _ := m.handleKey(ctrlKey('j'))
	got := out.(Model)
	if got.openChannelID != "c2" {
		t.Fatalf("nav didn't switch: openChannelID = %q", got.openChannelID)
	}
	if got.grammar.matches != nil {
		t.Fatalf("grammar findings survived the nav keypress: %+v", got.grammar.matches)
	}
}

// TestDraftsLoadedRechecksOpenDraft: a draft arriving from the server for the
// open channel seeds the empty composer and arms a grammar check for it.
func TestDraftsLoadedRechecksOpenDraft(t *testing.T) {
	m := grammarNavModel() // on c1, empty composer
	seqBefore := m.grammar.seq

	cmd := m.applyDraftsLoaded(draftsLoadedMsg{drafts: map[string]string{"c1": "som loaded"}})

	if got := m.input.Value(); got != "som loaded" {
		t.Fatalf("loaded draft not seeded into composer: %q", got)
	}
	if m.grammar.seq == seqBefore || cmd == nil {
		t.Fatalf("no grammar check armed for the loaded draft (seq %d→%d, cmd nil=%v)",
			seqBefore, m.grammar.seq, cmd == nil)
	}
}

// TestChannelSwitchClosesMentionPopup: an open @-mention dropdown belongs to
// the draft being left, so a channel switch dismisses it.
func TestChannelSwitchClosesMentionPopup(t *testing.T) {
	m := navModel()
	m.input.SetValue("hey @al")
	m.mention.active = true
	m.mention.query = "al"

	m.swapChannelDraft("c2")
	m.openChannelID = "c2"

	if m.mention.active {
		t.Fatalf("mention popup stayed open across the channel switch")
	}
}

// TestChannelSwitchResetsUndoHistory: undo history is reset on switch so an
// undo in the new channel can't resurrect the previous channel's text.
func TestChannelSwitchResetsUndoHistory(t *testing.T) {
	m := navModel()
	m.input.SetValue("hello")
	m.history.note("chan:c1", "", "hello")

	m.swapChannelDraft("c2")
	m.openChannelID = "c2"

	if v, ok := m.history.undo("chan:c2", m.input.Value()); ok {
		t.Fatalf("undo after switch resurrected previous text: %q", v)
	}
}

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
