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

// draftsLoadedMsg carries the user's server-side channel drafts, keyed by
// channelID, fetched once at startup. Thread-reply drafts (RootId set) are
// excluded — only the main composer is synced per channel.
type draftsLoadedMsg struct{ drafts map[string]string }

// draftSaveDebounceMsg fires after draftSaveDebounce. If seq still matches
// m.draftSaveSeq and the composer is still on channelID, the handler flushes
// the live draft to the server. channelID pins the save to the channel that
// was open when the tick was armed, so a quick switch can't autosave one
// channel's text under another.
type draftSaveDebounceMsg struct {
	seq       int
	channelID string
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
		for _, teamID := range teams {
			ds, err := client.GetDrafts(ctx, userID, teamID)
			if err != nil {
				continue
			}
			for _, d := range ds {
				if d == nil || d.RootId != "" {
					continue // channel drafts only
				}
				if strings.TrimSpace(d.Message) == "" {
					continue
				}
				out[d.ChannelId] = d.Message
			}
		}
		return draftsLoadedMsg{drafts: out}
	}
}

// applyDraftsLoaded merges the fetched drafts into m.drafts. If the open
// channel has a draft and its composer is empty (and not mid-edit / mid-
// thread-reply), the draft is shown immediately; otherwise it just waits in
// the map for the next time that channel is opened. The empty-composer guard
// keeps a slow draft fetch from clobbering text the user has already started
// typing.
func (m *Model) applyDraftsLoaded(msg draftsLoadedMsg) {
	if m.drafts == nil {
		m.drafts = map[string]string{}
	}
	for id, text := range msg.drafts {
		m.drafts[id] = text
	}
	if m.openChannelID == "" || m.threadOpen || m.editingPostID != "" {
		return
	}
	if strings.TrimSpace(m.input.Value()) != "" {
		return
	}
	if text, ok := m.drafts[m.openChannelID]; ok {
		m.setComposerDraft(text)
	}
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
	return cmd
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
	m.clearGrammar()
	m.syncInputHeight()
}

// stashDraft records text as channelID's draft and returns a Cmd to sync the
// change to the server: an upsert when there's real content, a delete when
// the draft has been emptied. It short-circuits when nothing changed so a
// channel hop without an edit costs no round-trip.
func (m *Model) stashDraft(channelID, text string) tea.Cmd {
	if channelID == "" {
		return nil
	}
	if m.drafts == nil {
		m.drafts = map[string]string{}
	}
	if strings.TrimSpace(text) == "" {
		if _, had := m.drafts[channelID]; !had {
			return nil // already empty — nothing to delete
		}
		delete(m.drafts, channelID)
		return m.deleteDraftCmd(channelID, "")
	}
	if m.drafts[channelID] == text {
		return nil // unchanged
	}
	m.drafts[channelID] = text
	return m.upsertDraftCmd(channelID, "", text)
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

// scheduleDraftSave arms (or re-arms) the debounced autosave after the
// composer changes. It's a no-op while editing a post or replying in a
// thread — only channel drafts autosave. Each call supersedes the previous
// tick via draftSaveSeq.
func (m *Model) scheduleDraftSave() tea.Cmd {
	if m.editingPostID != "" || m.threadOpen || m.openChannelID == "" {
		return nil
	}
	m.draftSaveSeq++
	seq, channelID := m.draftSaveSeq, m.openChannelID
	return tea.Tick(draftSaveDebounce, func(time.Time) tea.Msg {
		return draftSaveDebounceMsg{seq: seq, channelID: channelID}
	})
}

// applyDraftSaveDebounce flushes the live draft when a debounce tick matures,
// provided it's still the latest tick and the composer is still on the same
// channel composing a channel draft. Returns the server-sync Cmd or nil.
func (m *Model) applyDraftSaveDebounce(msg draftSaveDebounceMsg) tea.Cmd {
	if msg.seq != m.draftSaveSeq {
		return nil
	}
	if m.editingPostID != "" || m.threadOpen || m.openChannelID != msg.channelID {
		return nil
	}
	return m.stashDraft(msg.channelID, m.input.Value())
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
// user's sessions (the webapp, mobile, a second matterbox) into m.drafts. It
// only touches background channels' channel drafts: the currently-open channel
// is left alone so a live broadcast — including the echo of this client's own
// autosave — can't clobber what the user is actively typing, and thread/edit
// composers aren't channel drafts. Switching to the updated channel later picks
// up the fresh text from the map.
func (m *Model) applyDraftUpserted(ev *model.WebSocketEvent) {
	d := parseDraft(ev)
	if d == nil || d.RootId != "" || d.ChannelId == "" {
		return
	}
	if d.ChannelId == m.openChannelID {
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
// open-channel guard as applyDraftUpserted.
func (m *Model) applyDraftDeleted(ev *model.WebSocketEvent) {
	d := parseDraft(ev)
	if d == nil || d.RootId != "" || d.ChannelId == "" {
		return
	}
	if d.ChannelId == m.openChannelID {
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
