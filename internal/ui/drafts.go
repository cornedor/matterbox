package ui

import (
	"encoding/json"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mattermost/mattermost/server/public/model"
)

// draftSaveDebounce is how long the composer must sit idle before an edited
// draft is flushed to the server. It mirrors the webapp's behaviour: drafts
// are saved a short beat after typing stops rather than on every keystroke,
// so a burst of typing costs one round-trip, but a draft still survives a
// quit (or syncs to other clients) without the user switching channels.
const draftSaveDebounce = 750 * time.Millisecond

// draftsLoadedMsg carries the user's server-side drafts, fetched once at
// startup: channel drafts keyed by channelID, and thread-reply drafts
// (RootId set) keyed by their thread root id.
type draftsLoadedMsg struct {
	drafts       map[string]string
	threadDrafts map[string]string
}

// draftSaveDebounceMsg fires after draftSaveDebounce. If seq still matches
// m.draftSaveSeq and the composer is still on the same target, the handler
// flushes the live draft to the server. channelID/rootID pin the save to the
// channel-or-thread that was open when the tick was armed, so a quick switch
// can't autosave one target's text under another.
type draftSaveDebounceMsg struct {
	seq       int
	channelID string
	rootID    string
}

// loadDrafts fetches the user's saved channel drafts across every team and
// merges them into a single channelID→text map. Drafts for DMs / group-DMs
// (empty TeamId) come back under whichever team is queried, so iterating the
// teams and deduping by ChannelId captures them too. Best-effort: a failed
// team query is skipped rather than aborting the whole load. Returns nil
// when there's nothing to query yet.
func (m Model) loadDrafts() tea.Cmd {
	if m.client == nil || m.me == nil || len(m.teams) == 0 {
		return nil
	}
	client, ctx, userID := m.client, m.ctx, m.me.Id
	teams := make([]string, 0, len(m.teams))
	for _, t := range m.teams {
		teams = append(teams, t.Id)
	}
	return func() tea.Msg {
		out := map[string]string{}
		threads := map[string]string{}
		for _, teamID := range teams {
			ds, err := client.GetDrafts(ctx, userID, teamID)
			if err != nil {
				continue
			}
			for _, d := range ds {
				if d == nil || strings.TrimSpace(d.Message) == "" {
					continue
				}
				if d.RootId != "" {
					threads[d.RootId] = d.Message // thread-reply draft
				} else {
					out[d.ChannelId] = d.Message // channel draft
				}
			}
		}
		return draftsLoadedMsg{drafts: out, threadDrafts: threads}
	}
}

// applyDraftsLoaded merges the fetched drafts into m.drafts / m.threadDrafts.
// If the composer's current target (open channel, or open thread) has a draft
// and the composer is empty, the draft is shown immediately; otherwise it just
// waits in the map for the next time that channel / thread is opened. The
// empty-composer guard keeps a slow draft fetch from clobbering text the user
// has already started typing.
func (m *Model) applyDraftsLoaded(msg draftsLoadedMsg) tea.Cmd {
	if m.drafts == nil {
		m.drafts = map[string]string{}
	}
	if m.threadDrafts == nil {
		m.threadDrafts = map[string]string{}
	}
	for id, text := range msg.drafts {
		m.drafts[id] = text
	}
	for id, text := range msg.threadDrafts {
		m.threadDrafts[id] = text
	}
	channelID, rootID, tracks := m.composerDraftTarget()
	if !tracks {
		return nil
	}
	if strings.TrimSpace(m.input.Value()) != "" {
		return nil
	}
	var text string
	var ok bool
	if rootID == "" {
		text, ok = m.drafts[channelID]
	} else {
		text, ok = m.threadDrafts[rootID]
	}
	if ok {
		m.setComposerDraft(text)
		// Check the just-seeded draft for grammar (no-op when off / empty).
		return m.scheduleGrammarCheck()
	}
	return nil
}

// composerDraftTarget reports the (channelID, rootID) pair the composer is
// currently drafting for, and whether that target tracks a draft at all. An
// in-progress edit owns the composer and isn't a draft, so it tracks nothing;
// an open thread drafts a reply (rootID set); otherwise it's the open channel.
func (m *Model) composerDraftTarget() (channelID, rootID string, tracks bool) {
	if m.editingPostID != "" {
		return "", "", false
	}
	if m.threadOpen {
		return m.threadChannelID, m.threadRootID, m.threadChannelID != "" && m.threadRootID != ""
	}
	return m.openChannelID, "", m.openChannelID != ""
}

// stashComposerDraft persists the composer's current text under whatever draft
// target it's composing for right now — the open channel, or the open thread.
// Returns the server-sync Cmd, or nil when there's no draft target (mid-edit).
func (m *Model) stashComposerDraft() tea.Cmd {
	channelID, rootID, tracks := m.composerDraftTarget()
	if !tracks {
		return nil
	}
	if rootID == "" {
		return m.stashDraft(channelID, m.input.Value())
	}
	return m.stashThreadDraft(channelID, rootID, m.input.Value())
}

// swapToThreadDraft stashes whatever the composer is currently drafting (the
// open channel, or a previously-open thread) and loads rootID's thread draft
// into the composer in its place. Callers invoke this *before* repointing the
// thread state, so composerDraftTarget still names the outgoing target.
func (m *Model) swapToThreadDraft(channelID, rootID string) tea.Cmd {
	stashCmd := m.stashComposerDraft()
	if m.threadDrafts == nil {
		m.threadDrafts = map[string]string{}
	}
	m.setComposerDraft(m.threadDrafts[rootID])
	// setComposerDraft dropped the outgoing draft's grammar findings; arm a
	// fresh check so the restored thread draft (if any) gets underlined too.
	return tea.Batch(stashCmd, m.scheduleGrammarCheck())
}

// swapChannelDraft persists the composer's current text as the draft for the
// open channel and loads newChannelID's draft into the composer in its place.
// It's a no-op when an edit or thread reply owns the composer (those aren't
// channel drafts) or when the channel isn't actually changing. Returns a Cmd
// that syncs the stashed draft to the server, or nil. Callers invoke this
// *before* repointing m.openChannelID.
func (m *Model) swapChannelDraft(newChannelID string) tea.Cmd {
	if m.editingPostID != "" || m.threadOpen {
		return nil
	}
	old := m.openChannelID
	if newChannelID == old {
		return nil
	}
	var cmd tea.Cmd
	if old != "" {
		cmd = m.stashDraft(old, m.input.Value())
	}
	m.setComposerDraft(m.drafts[newChannelID])
	// setComposerDraft dropped the previous channel's grammar findings; arm a
	// fresh check so the restored draft (if any) gets underlined too. No-op
	// when grammar is off or the restored draft is empty.
	return tea.Batch(cmd, m.scheduleGrammarCheck())
}

// setComposerDraft replaces the composer contents with text and resets the
// transient composer state that's bound to the previous draft (undo history,
// grammar findings, open mention/emoji popups, height). Used when restoring a
// channel's draft on open and when seeding a freshly-fetched draft.
func (m *Model) setComposerDraft(text string) {
	if text == m.input.Value() {
		return // composer already holds this — nothing to reset
	}
	m.input.SetValue(text)
	m.input.CursorEnd()
	m.history.reset()
	m.closeMention()
	m.closeEmoji()
	m.closeSlash()
	m.closeLang()
	// Drop any command shimmer from the previous channel's draft. SetValue
	// keeps the span (so the /dm mention-accept path doesn't flicker), so a
	// channel hop must clear it explicitly; it re-lights on the first keystroke
	// if the restored draft is itself a command.
	m.input.ClearCommandSpan()
	m.clearGrammar()
	m.syncInputHeight()
}

// putDraft records text under key in store and returns the server-sync Cmd for
// the (channelID, rootID) draft: an upsert when there's real content, a delete
// when it's been emptied, nothing when unchanged. stashDraft / stashThreadDraft
// wrap it for the channel and thread maps respectively.
func (m *Model) putDraft(store map[string]string, key, channelID, rootID, text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		if _, had := store[key]; !had {
			return nil // already empty — nothing to delete
		}
		delete(store, key)
		return m.deleteDraftCmd(channelID, rootID)
	}
	if store[key] == text {
		return nil // unchanged
	}
	store[key] = text
	return m.upsertDraftCmd(channelID, rootID, text)
}

// stashDraft records text as channelID's draft and returns a Cmd to sync the
// change to the server. It short-circuits when nothing changed so a channel hop
// without an edit costs no round-trip.
func (m *Model) stashDraft(channelID, text string) tea.Cmd {
	if channelID == "" {
		return nil
	}
	if m.drafts == nil {
		m.drafts = map[string]string{}
	}
	return m.putDraft(m.drafts, channelID, channelID, "", text)
}

// stashThreadDraft records text as the draft for the (channelID, rootID) thread
// reply and returns the server-sync Cmd. The thread-reply analogue of
// stashDraft, keyed by the thread root id.
func (m *Model) stashThreadDraft(channelID, rootID, text string) tea.Cmd {
	if channelID == "" || rootID == "" {
		return nil
	}
	if m.threadDrafts == nil {
		m.threadDrafts = map[string]string{}
	}
	return m.putDraft(m.threadDrafts, rootID, channelID, rootID, text)
}

// clearDraft drops the channel's draft locally and on the server. Used after
// a successful send, when the composed text has become a real post.
func (m *Model) clearDraft(channelID string) tea.Cmd {
	if channelID == "" {
		return nil
	}
	_, had := m.drafts[channelID]
	delete(m.drafts, channelID)
	if !had {
		return nil
	}
	return m.deleteDraftCmd(channelID, "")
}

// clearThreadDraft drops a thread's reply draft locally and on the server.
// Used after a successful thread reply, when the text has become a real post.
func (m *Model) clearThreadDraft(channelID, rootID string) tea.Cmd {
	if channelID == "" || rootID == "" {
		return nil
	}
	_, had := m.threadDrafts[rootID]
	delete(m.threadDrafts, rootID)
	if !had {
		return nil
	}
	return m.deleteDraftCmd(channelID, rootID)
}

// scheduleDraftSave arms (or re-arms) the debounced autosave after the
// composer changes. It saves whichever draft the composer is on — channel or
// thread — and is a no-op while editing a post (an edit isn't a draft). Each
// call supersedes the previous tick via draftSaveSeq.
func (m *Model) scheduleDraftSave() tea.Cmd {
	channelID, rootID, tracks := m.composerDraftTarget()
	if !tracks {
		return nil
	}
	m.draftSaveSeq++
	seq := m.draftSaveSeq
	return tea.Tick(draftSaveDebounce, func(time.Time) tea.Msg {
		return draftSaveDebounceMsg{seq: seq, channelID: channelID, rootID: rootID}
	})
}

// applyDraftSaveDebounce flushes the live draft when a debounce tick matures,
// provided it's still the latest tick and the composer is still on the same
// target the tick was armed for. Returns the server-sync Cmd or nil.
func (m *Model) applyDraftSaveDebounce(msg draftSaveDebounceMsg) tea.Cmd {
	if msg.seq != m.draftSaveSeq {
		return nil
	}
	channelID, rootID, tracks := m.composerDraftTarget()
	if !tracks || channelID != msg.channelID || rootID != msg.rootID {
		return nil
	}
	if rootID == "" {
		return m.stashDraft(channelID, m.input.Value())
	}
	return m.stashThreadDraft(channelID, rootID, m.input.Value())
}

// upsertDraftCmd persists a draft to the server in the background. Draft sync
// is best-effort: a failure is swallowed rather than flashed on the status
// line, since saves fire while the user types.
func (m Model) upsertDraftCmd(channelID, rootID, message string) tea.Cmd {
	if m.client == nil || m.me == nil {
		return nil
	}
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		_, _ = client.UpsertDraft(ctx, channelID, rootID, message, nil)
		return nil
	}
}

// parseDraft pulls the model.Draft out of a draft_* WebSocket event (the
// server JSON-encodes it under the "draft" key, mirroring "post" on posted
// events). Returns nil when the payload is missing or unparseable.
func parseDraft(ev *model.WebSocketEvent) *model.Draft {
	raw, ok := ev.GetData()["draft"].(string)
	if !ok || raw == "" {
		return nil
	}
	var d model.Draft
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil
	}
	return &d
}

// applyDraftUpserted folds a draft create/update broadcast from another of the
// user's sessions (the webapp, mobile, a second matterbox) into m.drafts (or
// m.threadDrafts for a thread reply). It leaves the composer's *current* target
// alone so a live broadcast — including the echo of this client's own autosave —
// can't clobber what the user is actively typing; the open channel when no
// thread is up, or the open thread when one is. Switching to the updated target
// later picks up the fresh text from the map.
func (m *Model) applyDraftUpserted(ev *model.WebSocketEvent) {
	d := parseDraft(ev)
	if d == nil || d.ChannelId == "" {
		return
	}
	if d.RootId != "" {
		if m.threadOpen && d.RootId == m.threadRootID {
			return // local composer is the source of truth for the open thread
		}
		if m.threadDrafts == nil {
			m.threadDrafts = map[string]string{}
		}
		if strings.TrimSpace(d.Message) == "" {
			delete(m.threadDrafts, d.RootId)
			return
		}
		m.threadDrafts[d.RootId] = d.Message
		return
	}
	if !m.threadOpen && d.ChannelId == m.openChannelID {
		return // local composer is the source of truth for the open channel
	}
	if m.drafts == nil {
		m.drafts = map[string]string{}
	}
	if strings.TrimSpace(d.Message) == "" {
		delete(m.drafts, d.ChannelId)
		return
	}
	m.drafts[d.ChannelId] = d.Message
}

// applyDraftDeleted drops a draft removed from another session, with the same
// active-target guards as applyDraftUpserted.
func (m *Model) applyDraftDeleted(ev *model.WebSocketEvent) {
	d := parseDraft(ev)
	if d == nil || d.ChannelId == "" {
		return
	}
	if d.RootId != "" {
		if m.threadOpen && d.RootId == m.threadRootID {
			return
		}
		delete(m.threadDrafts, d.RootId)
		return
	}
	if !m.threadOpen && d.ChannelId == m.openChannelID {
		return
	}
	delete(m.drafts, d.ChannelId)
}

// deleteDraftCmd removes a draft from the server in the background, swallowing
// errors for the same reason as upsertDraftCmd.
func (m Model) deleteDraftCmd(channelID, rootID string) tea.Cmd {
	if m.client == nil || m.me == nil {
		return nil
	}
	client, ctx, userID := m.client, m.ctx, m.me.Id
	return func() tea.Msg {
		_ = client.DeleteDraft(ctx, userID, channelID, rootID)
		return nil
	}
}
