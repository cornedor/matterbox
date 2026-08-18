package ui

import (
	"encoding/json"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Saved messages are Mattermost's per-user "flagged_post" preferences: a post
// doesn't carry its saved state, so the set of saved post ids is loaded once
// (fetchSavedPostIDs, after the user is known) and kept in sync from the
// preferences_changed / preferences_deleted websocket events every session
// receives — including the echo of our own toggles.

// savedIDsLoadedMsg carries the initial saved-post id set.
type savedIDsLoadedMsg struct {
	ids []string
	err error
}

func (m Model) fetchSavedPostIDs(userID string) tea.Cmd {
	return func() tea.Msg {
		ids, err := m.client.SavedPostIDs(m.ctx, userID)
		return savedIDsLoadedMsg{ids: ids, err: err}
	}
}

// applySavedIDsLoaded seeds the set. A failed fetch keeps whatever we have
// (nothing at startup): the toggle then assumes "not saved", which is a
// harmless default — a second save is a no-op server-side.
func (m *Model) applySavedIDsLoaded(msg savedIDsLoadedMsg) {
	if msg.err != nil {
		m.status = "saved messages: " + oneLine(msg.err.Error())
		return
	}
	m.savedPostIDs = make(map[string]bool, len(msg.ids))
	for _, id := range msg.ids {
		m.savedPostIDs[id] = true
	}
	m.repaintPosts()
}

// isSaved reports whether the post is in the user's saved messages.
func (m *Model) isSaved(postID string) bool {
	return postID != "" && m.savedPostIDs[postID]
}

// applyPreferencesEvent folds a preferences_changed (added=true) or
// preferences_deleted (added=false) event into the saved set. The payload is
// the JSON-encoded preference list under "preferences"; only the flagged_post
// category matters here.
func (m *Model) applyPreferencesEvent(ev *model.WebSocketEvent, added bool) {
	raw, _ := ev.GetData()["preferences"].(string)
	if raw == "" {
		return
	}
	var prefs []model.Preference
	if err := json.Unmarshal([]byte(raw), &prefs); err != nil {
		return
	}
	changed := false
	for _, p := range prefs {
		if p.Category != model.PreferenceCategoryFlaggedPost || p.Name == "" {
			continue
		}
		if m.setSaved(p.Name, added) {
			changed = true
		}
	}
	if changed {
		m.repaintPosts()
	}
}

// setSaved records the post as saved / unsaved locally, reporting whether that
// changed anything. It also invalidates the post's cached rows so the header
// mark tracks the change; callers repaint.
func (m *Model) setSaved(postID string, saved bool) bool {
	if postID == "" || m.savedPostIDs[postID] == saved {
		return false
	}
	if m.savedPostIDs == nil {
		m.savedPostIDs = map[string]bool{}
	}
	if saved {
		m.savedPostIDs[postID] = true
	} else {
		delete(m.savedPostIDs, postID)
	}
	m.invalidatePostLines(postID)
	return true
}

// repaintPosts re-renders the channel list and the open thread after a
// saved-state change touched header marks.
func (m *Model) repaintPosts() {
	m.renderMessages()
	if m.threadOpen {
		m.renderThread()
	}
}

// saveCommand is the save/unsave toggle for the selected message; the label
// follows the saved set. Listed alongside the pin toggle (see pinCommands).
func (m *Model) saveCommand() switcherCommand {
	if p := m.selectedPost(); p != nil && m.isSaved(p.Id) {
		return switcherCommand{
			name: "Unsave message",
			desc: "remove the selected message from your saved messages",
			run:  runToggleSaved,
		}
	}
	return switcherCommand{
		name: "Save message",
		desc: "add the selected message to your saved messages",
		run:  runToggleSaved,
	}
}

// runToggleSaved flips the selected message's saved state: locally at once
// (so the mark and label update), then on the server; applySavedChanged
// reverts on failure.
func runToggleSaved(m *Model, _ string) tea.Cmd {
	if m.me == nil {
		m.status = "saved messages: user not loaded yet"
		return nil
	}
	p := m.selectedPost()
	if p == nil || p.Id == "" || p.DeleteAt != 0 {
		m.status = "no message selected"
		return nil
	}
	saved := !m.isSaved(p.Id)
	m.setSaved(p.Id, saved)
	m.repaintPosts()
	if saved {
		m.status = "saving message…"
	} else {
		m.status = "unsaving message…"
	}
	return m.setSavedCmd(p.Id, saved)
}

// setSavedCmd is the server half of a save/unsave.
func (m *Model) setSavedCmd(postID string, saved bool) tea.Cmd {
	userID := m.me.Id
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		var err error
		if saved {
			err = client.SavePost(ctx, userID, postID)
		} else {
			err = client.UnsavePost(ctx, userID, postID)
		}
		return savedChangedMsg{postID: postID, saved: saved, err: err}
	}
}

type savedChangedMsg struct {
	postID string
	saved  bool
	err    error
}

func (m Model) applySavedChanged(msg savedChangedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.setSaved(msg.postID, !msg.saved) {
			m.repaintPosts()
		}
		m.status = oneLine(msg.err.Error())
		return m, nil
	}
	if msg.saved {
		m.status = "message saved"
	} else {
		m.status = "message unsaved"
	}
	return m, nil
}

// --- the Saved messages sheet ("> Saved messages") ------------------------

// savedPostsState is the saved-messages browser: enter jumps to the message
// in its channel, d unsaves it. items is the server's page of saved posts;
// what the sheet shows is items filtered by the saved set (see visibleSaved),
// so a d, its failure revert, and an unsave echoed from another client all
// reach the sheet through the one set rather than a second copy of it.
type savedPostsState struct {
	active  bool
	loading bool
	gen     int // load generation; a stale fetch (closed + reopened) is dropped
	err     string
	items   []savedItem
	idx     int
}

// savedItem is one loaded saved post with the parts of its row that don't
// change while the sheet is open — channel label and one-line text — built
// once at load rather than on every frame. The author is looked up per frame:
// it may resolve (resolveUnknownSenders) while the sheet is up.
type savedItem struct {
	post    *model.Post
	channel string
	text    string
}

type savedPostsLoadedMsg struct {
	gen   int
	items []*model.Post
	err   error
}

func runOpenSavedMessages(m *Model, _ string) tea.Cmd {
	return m.openSavedPosts()
}

func (m *Model) openSavedPosts() tea.Cmd {
	if m.me == nil {
		m.status = "saved messages: user not loaded yet"
		return nil
	}
	gen := m.savedPosts.gen + 1
	m.savedPosts = savedPostsState{active: true, loading: true, gen: gen}
	userID := m.me.Id
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		items, err := client.SavedPosts(ctx, userID, 0, 200)
		return savedPostsLoadedMsg{gen: gen, items: items, err: err}
	}
}

func (m Model) applySavedPostsLoaded(msg savedPostsLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.savedPosts.active || msg.gen != m.savedPosts.gen {
		return m, nil // closed, or reopened since this fetch started
	}
	m.savedPosts.loading = false
	if msg.err != nil {
		m.savedPosts.err = oneLine(msg.err.Error())
		return m, nil
	}
	m.savedPosts.items = m.savedPosts.items[:0]
	m.savedPosts.idx = 0
	// Everything the server lists is saved by definition: seed the set from it
	// too, so the sheet's filter and the header marks agree even if the
	// preference fetch at startup failed.
	changed := false
	for _, p := range msg.items {
		if p == nil {
			continue
		}
		label := p.ChannelId
		if ch := m.findChannel(p.ChannelId); ch != nil {
			label = m.channelLabel(ch)
		}
		m.savedPosts.items = append(m.savedPosts.items, savedItem{
			post:    p,
			channel: label,
			text:    strings.Join(strings.Fields(p.Message), " "),
		})
		if m.setSaved(p.Id, true) {
			changed = true
		}
	}
	if changed {
		m.repaintPosts()
	}
	return m, nil
}

// visibleSaved is the sheet's row list: the loaded page minus anything
// unsaved since (locally or by another client).
func (m *Model) visibleSaved() []savedItem {
	out := make([]savedItem, 0, len(m.savedPosts.items))
	for _, it := range m.savedPosts.items {
		if m.isSaved(it.post.Id) {
			out = append(out, it)
		}
	}
	return out
}

func (m *Model) closeSavedPosts() {
	m.savedPosts = savedPostsState{gen: m.savedPosts.gen}
}

// handleSavedPostsKey owns every keystroke while the browser is open: esc/q
// close, ↑/↓ move, enter opens the message in its channel, d unsaves it.
func (m Model) handleSavedPostsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.closeSavedPosts()
		return m, nil
	}
	vis := m.visibleSaved()
	if m.savedPosts.idx >= len(vis) {
		m.savedPosts.idx = max(len(vis)-1, 0)
	}
	if m.listNav(msg, &m.savedPosts.idx, len(vis)) {
		return m, nil
	}
	if m.savedPosts.idx >= len(vis) {
		return m, nil
	}
	p := vis[m.savedPosts.idx].post
	switch {
	case key.Matches(msg, m.keys.SheetRemove):
		if m.me == nil {
			return m, nil
		}
		// The row goes as the set changes; the cursor stays on the same
		// position (now the next row), clamped by the next keystroke/render.
		m.setSaved(p.Id, false)
		m.repaintPosts()
		m.status = "unsaving message…"
		return m, m.setSavedCmd(p.Id, false)
	case key.Matches(msg, m.keys.OpenChannel):
		ch := m.findChannel(p.ChannelId)
		if ch == nil {
			m.status = "saved message's channel is not in the local list"
			return m, nil
		}
		m.closeSavedPosts()
		return m.openChannelAtPost(ch, p.Id)
	}
	return m, nil
}

func (m *Model) renderSavedPosts() string {
	if !m.savedPosts.active {
		return ""
	}
	vis := m.visibleSaved()
	body := "loading…"
	switch {
	case m.savedPosts.err != "":
		body = m.savedPosts.err
	case !m.savedPosts.loading && len(vis) == 0:
		body = "No saved messages yet."
	}
	idx := min(m.savedPosts.idx, max(len(vis)-1, 0))
	row := func(i int) string {
		return vis[i].channel + " · " + m.postAuthorName(vis[i].post) + ": " + vis[i].text
	}
	return m.renderListModal("Saved messages", helpKey(m.keys.OpenChannel)+" opens · "+helpKey(m.keys.SheetRemove)+" unsaves · esc closes", body, len(vis), idx, row)
}
