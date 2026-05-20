package ui

import (
	"encoding/json"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Update is the bubbletea entry point. It runs the real handler, then —
// once state has settled — kicks off a background fetch for any on-screen
// sender we still can't name, so cached/WebSocket-delivered posts repaint
// with a real @name in place of a truncated raw id (see
// resolveUnknownSenders). The fetch is deduplicated at the client, so
// firing it after every event is cheap.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	nm, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	if resolve := nm.resolveUnknownSenders(); resolve != nil {
		cmd = tea.Batch(cmd, resolve)
	}
	return nm, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.filter.SetWidth(channelsWidth - 4)
		m.resizeMessagesViewport()
		m.resizeInput()
		// Pane widths just changed; every cached entry was fingerprinted
		// with the old width and will miss on lookup. Drop the map so
		// stale entries don't waste memory until cap eviction kicks in.
		m.postLineCache = nil
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.PasteMsg:
		return m.handlePaste(msg)

	case meLoadedMsg:
		m.me = msg.user
		m.status = "loading teams & channels…"
		return m, tea.Batch(
			m.fetchTeams(m.me.Id),
			m.fetchAllChannels(m.me.Id),
			m.fetchChannelMembers(m.me.Id),
		)

	case teamsLoadedMsg:
		m.teams = msg.teams
		m.applyTeamOrder()
		m.teamsLoaded = true
		return m, m.maybeFetchInitialPosts()

	case channelsLoadedMsg:
		for id, name := range msg.userNames {
			m.userNames[id] = name
		}
		m.bucketChannels(msg.channels)
		m.channelsLoaded = true
		m.applyUnreadFromMembers()
		return m, m.maybeFetchInitialPosts()

	case membersLoadedMsg:
		m.members = msg.members
		m.membersLoaded = true
		m.applyUnreadFromMembers()
		return m, nil

	case postsLoadedMsg:
		vis := m.visibleChannels()
		if m.channelIdx >= len(vis) || vis[m.channelIdx].Id != msg.channelID {
			// Stale to the UI, but still worth persisting so the cache
			// keeps growing for unfocused channels we briefly opened.
			return m, m.persistPosts(msg.posts...)
		}
		m.posts = msg.posts
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		delete(m.unread, msg.channelID)
		delete(m.mentions, msg.channelID)
		m.loading = false
		m.status = ""
		m.postIdx = len(m.posts) - 1
		// If a search hit queued a jump, prefer that over the default
		// "select newest" position.
		m.jumpToPendingPost()
		m.renderMessages()
		return m, tea.Batch(
			m.markChannelViewed(msg.channelID),
			m.persistPosts(msg.posts...),
		)

	case postsGapFilledMsg:
		// Always persist what we got — even if the user has since
		// switched channels, the rows belong in the cache.
		persistCmd := m.persistPosts(msg.posts...)
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		// Only mutate the visible posts slice if the user is still on
		// the same channel.
		vis := m.visibleChannels()
		stillFocused := m.channelIdx < len(vis) && vis[m.channelIdx].Id == msg.channelID
		if !stillFocused {
			return m, persistCmd
		}
		// Dedup by Id and append, mirroring applyPosted's selection logic.
		known := make(map[string]struct{}, len(m.posts))
		for _, p := range m.posts {
			if p.Id != "" {
				known[p.Id] = struct{}{}
			}
		}
		wasAtBottom := m.postIdx >= len(m.posts)-1
		for _, p := range msg.posts {
			if _, dup := known[p.Id]; dup {
				continue
			}
			known[p.Id] = struct{}{}
			m.posts = append(m.posts, p)
		}
		if wasAtBottom {
			m.postIdx = len(m.posts) - 1
		}
		m.loading = false
		m.status = ""
		// Apply any queued search-result jump now that the gap is filled.
		m.jumpToPendingPost()
		m.renderMessages()
		return m, tea.Batch(
			m.markChannelViewed(msg.channelID),
			persistCmd,
		)

	case errMsg:
		m.loading = false
		m.status = "error: " + msg.err.Error()
		if isUnauthorized(msg.err) {
			m.status = "auth failed — re-run `python mm_login.py` to refresh the token"
		}
		return m, nil

	case wsConnectedMsg:
		m.ws = msg.ws
		m.wsRetry = 0
		if strings.HasPrefix(m.status, "websocket") || strings.HasPrefix(m.status, "reconnecting") {
			m.status = ""
		}
		return m, waitWSEvent(m.ws)

	case wsEventMsg:
		cmd := m.handleWSEvent(msg.ev)
		return m, tea.Batch(cmd, waitWSEvent(m.ws))

	case wsClosedMsg:
		m.ws = nil
		m.wsRetry++
		delay := wsBackoff(m.wsRetry)
		if msg.err != nil {
			m.status = "websocket: " + msg.err.Error() + "; retry in " + delay.String()
		} else {
			m.status = "websocket closed; retry in " + delay.String()
		}
		return m, tea.Tick(delay, func(_ time.Time) tea.Msg { return wsReconnectMsg{} })

	case wsReconnectMsg:
		m.status = "reconnecting…"
		return m, m.connectWS()

	case postSentMsg:
		m.status = ""
		// Refetch to replace the optimistic stub with the real post and
		// catch anything that arrived between send and now. The WS-driven
		// refetch may double this up — harmless and idempotent.
		return m, m.fetchPosts(msg.channelID)

	case postEditedMsg:
		if msg.err != nil {
			m.status = "edit failed: " + msg.err.Error()
			return m, nil
		}
		m.status = "edit saved"
		// The WS `post_edited` broadcast (applyPostEdited) is the
		// authoritative source of the updated row; mirror its work
		// pre-emptively in case the broadcast is delayed so the UI
		// doesn't lag behind the user's action.
		var cmd tea.Cmd
		if msg.post != nil {
			cmd = m.persistPosts(msg.post)
			for i, ex := range m.posts {
				if ex.Id == msg.post.Id {
					m.posts[i] = msg.post
					break
				}
			}
			for i, ex := range m.threadPosts {
				if ex.Id == msg.post.Id {
					m.threadPosts[i] = msg.post
					break
				}
			}
			m.renderMessages()
			m.renderThread()
		}
		return m, cmd

	case threadLoadedMsg:
		if !m.threadOpen || msg.rootID != m.threadRootID {
			return m, nil // stale (closed or switched)
		}
		m.threadPosts = msg.posts
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		m.threadLoading = false
		m.threadIdx = len(m.threadPosts) - 1
		if m.threadIdx < 0 {
			m.threadIdx = 0
		}
		m.renderThread()
		return m, nil

	case fileInfosLoadedMsg:
		var persistCmd tea.Cmd
		for _, p := range m.posts {
			if p.Id != msg.postID {
				continue
			}
			if p.Metadata == nil {
				p.Metadata = &model.PostMetadata{}
			}
			p.Metadata.Files = msg.infos
			m.renderMessages()
			// The stored row's raw_json no longer matches what we're
			// showing; re-persist so a future reopen renders the same.
			persistCmd = m.persistPosts(p)
			break
		}
		return m, persistCmd

	case attachmentOpenedMsg:
		if msg.err != nil {
			m.status = "open " + msg.name + ": " + msg.err.Error()
		} else {
			m.status = "opened " + msg.name
		}
		return m, nil

	case copyClipboardMsg:
		m.status = "copied markdown to clipboard"
		return m, nil

	case mentionDebounceMsg:
		if !m.mention.active || msg.seq != m.mention.fetchSeq {
			return m, nil
		}
		// Scope autocomplete to the thread's channel/team when replying in
		// a thread (which may not be the channel currently selected in the
		// sidebar). Otherwise use the focused channel.
		var teamID, channelID string
		if m.threadOpen && m.threadChannelID != "" {
			channelID = m.threadChannelID
			teamID = m.threadTeamID()
		} else {
			vis := m.visibleChannels()
			if m.channelIdx >= len(vis) {
				return m, nil
			}
			ch := vis[m.channelIdx]
			teamID = ch.TeamId
			channelID = ch.Id
		}
		return m, m.fetchMentions(teamID, channelID, m.mention.query, msg.seq)

	case mentionUsersMsg:
		if !m.mention.active || msg.seq != m.mention.fetchSeq {
			return m, nil
		}
		if msg.err != nil {
			m.status = "mention: " + msg.err.Error()
			return m, nil
		}
		items := msg.users
		if len(items) > mentionLimit {
			items = items[:mentionLimit]
		}
		// Cache resolved usernames so future post rows label them without
		// another lookup.
		for _, u := range items {
			if u != nil && u.Username != "" {
				m.userNames[u.Id] = u.Username
			}
		}
		m.mention.items = items
		if m.mention.idx >= len(items) {
			m.mention.idx = 0
		}
		return m, nil

	case usersResolvedMsg:
		if msg.err != nil {
			// Leave the ids unknown so a later render retries them.
			m.status = "resolve usernames: " + msg.err.Error()
			return m, nil
		}
		changed := false
		for _, id := range msg.ids {
			if name := msg.users[id]; name != "" {
				if m.userNames[id] != name {
					m.userNames[id] = name
					changed = true
				}
				continue
			}
			// Negatively cache ids the server didn't return (deleted or
			// unknown users) so resolveUnknownSenders stops asking. Render
			// still falls back to the truncated id for these.
			if _, ok := m.userNames[id]; !ok {
				m.userNames[id] = ""
			}
		}
		if changed {
			m.renderMessages()
			m.renderThread()
			m.renderSearchResults()
		}
		return m, nil

	case clipboardReadMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		if len(msg.payloads) > 0 {
			return m, m.addAttachments(msg.payloads)
		}
		if msg.text != "" {
			// No file in clipboard but text is — route it as a paste so it
			// lands in whatever input is currently focused.
			return m.handlePaste(tea.PasteMsg{Content: msg.text})
		}
		m.status = "nothing to paste"
		return m, nil

	case attachmentUploadedMsg:
		m.applyUploadResult(msg)
		return m, nil

	case spinner.TickMsg:
		// spinner.Model.Update self-discriminates on TickMsg.ID, so it's
		// safe to broadcast the tick to every live spinner. The footer
		// reads m.indexer.spinner directly when active, so we don't need
		// to mirror it into m.status here (which would clobber any
		// transient status messages from elsewhere).
		cmds := []tea.Cmd{m.tickAttachmentSpinners(msg)}
		if m.indexer.active {
			sp, cmd := m.indexer.spinner.Update(msg)
			m.indexer.spinner = sp
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.summary.phase == summaryGathering || m.summary.phase == summaryStreaming {
			sp, cmd := m.summary.spinner.Update(msg)
			m.summary.spinner = sp
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)

	case indexResultMsg:
		return m, m.applyIndexResult(msg)

	case summaryGatheredMsg:
		return m, m.applySummaryGathered(msg)

	case summaryStreamOpenedMsg:
		return m, m.applySummaryStreamOpened(msg)

	case summaryChunkMsg:
		return m, m.applySummaryChunk(msg)

	case searchDebounceMsg:
		return m.applySearchDebounce(msg)

	case searchResultsMsg:
		return m.applySearchResults(msg)

	case feedLoadedMsg:
		return m.applyFeedResults(msg)

	case reactionErrMsg:
		m.status = "reaction: " + msg.err.Error()
		return m, nil

	case pollActionResultMsg:
		if msg.err != nil {
			m.status = "poll action: " + msg.err.Error()
		} else if !strings.HasPrefix(msg.actionID, pollVoteActionPrefix) && msg.actionID != pollAddOptionActionID {
			// Voting and addOption already set a status above; only
			// clear when an admin action (end/delete) succeeded.
			m.status = ""
		}
		return m, nil

	case pollDialogSubmittedMsg:
		m.applyPollDialogResult(msg)
		return m, nil
	}

	var cmd tea.Cmd
	m.msgsView, cmd = m.msgsView.Update(msg)
	return m, cmd
}

// handleWSEvent reacts to a WebSocket event by parsing the embedded
// post and applying it locally — no refetch unless the payload is
// unparseable and the event concerns the current channel.
func (m *Model) handleWSEvent(ev *model.WebSocketEvent) tea.Cmd {
	switch ev.EventType() {
	case model.WebsocketEventPosted:
		return m.applyPosted(ev)
	case model.WebsocketEventPostEdited:
		return m.applyPostEdited(ev)
	case model.WebsocketEventPostDeleted:
		return m.applyPostDeleted(ev)
	case model.WebsocketEventReactionAdded:
		return m.applyReactionEvent(ev, true)
	case model.WebsocketEventReactionRemoved:
		return m.applyReactionEvent(ev, false)
	case model.WebsocketEventOpenDialog:
		m.applyOpenDialog(ev)
		return nil
	}
	return nil
}

// applyPosted appends a new post locally. If for a non-focused channel,
// just bumps the unread (and mention, if we're tagged) counter.
func (m *Model) applyPosted(ev *model.WebSocketEvent) tea.Cmd {
	p := parsePost(ev)
	if p == nil {
		// Fall back to refetch if we can't parse and it's the current
		// channel; also refresh the open thread so it doesn't fall
		// behind.
		var cmds []tea.Cmd
		if b := ev.GetBroadcast(); b != nil && m.isCurrentChannel(b.ChannelId) {
			cmds = append(cmds, m.fetchPosts(b.ChannelId))
		}
		if m.threadOpen {
			cmds = append(cmds, m.fetchThread(m.threadRootID))
		}
		if len(cmds) == 0 {
			return nil
		}
		return tea.Batch(cmds...)
	}
	if sn, ok := ev.GetData()["sender_name"].(string); ok && sn != "" {
		m.userNames[p.UserId] = strings.TrimPrefix(sn, "@")
	}

	var cmds []tea.Cmd
	// Persist every new post we can parse, even for unfocused channels —
	// this is the corpus a future local search reads from.
	cmds = append(cmds, m.persistPosts(p))

	if m.isCurrentChannel(p.ChannelId) {
		alreadyShown := false
		for _, ex := range m.posts {
			if ex.Id != "" && ex.Id == p.Id {
				alreadyShown = true
				break
			}
		}
		if !alreadyShown {
			// Drop any matching optimistic stub (own send, no Id yet).
			for i := len(m.posts) - 1; i >= 0; i-- {
				ex := m.posts[i]
				if ex.Id == "" && ex.UserId == p.UserId && ex.Message == p.Message {
					m.posts = append(m.posts[:i], m.posts[i+1:]...)
					break
				}
			}
			// If the user was viewing the last post, advance selection to the
			// new last so the incoming message comes into view. Otherwise keep
			// them where they are.
			wasAtBottom := m.postIdx >= len(m.posts)-1
			m.posts = append(m.posts, p)
			if wasAtBottom {
				m.postIdx = len(m.posts) - 1
			}
			m.renderMessages()
			cmds = append(cmds, m.markChannelViewed(p.ChannelId))
			if needsFileInfoFetch(p) {
				cmds = append(cmds, m.fetchFileInfos(p.Id))
			}
		}
	} else if !m.isThreadPost(p) {
		// Not in the focused channel and not part of the open thread →
		// it's a background channel update.
		m.unread[p.ChannelId]++
		if m.me != nil && wsMentions(ev)[m.me.Id] {
			m.mentions[p.ChannelId]++
		}
		// Keep the unread feed live without a manual refresh.
		m.feedAppendPosted(p)
	}

	if m.isThreadPost(p) {
		m.appendThreadPost(p)
		m.renderThread()
	}

	return tea.Batch(cmds...)
}

// isThreadPost reports whether p belongs to the currently-open thread.
func (m *Model) isThreadPost(p *model.Post) bool {
	if !m.threadOpen || p == nil {
		return false
	}
	return p.Id == m.threadRootID || p.RootId == m.threadRootID
}

// appendThreadPost inserts p into m.threadPosts, deduping by Id and
// replacing matching optimistic stubs (own-send echo).
func (m *Model) appendThreadPost(p *model.Post) {
	for _, ex := range m.threadPosts {
		if ex.Id != "" && ex.Id == p.Id {
			return
		}
	}
	for i := len(m.threadPosts) - 1; i >= 0; i-- {
		ex := m.threadPosts[i]
		if ex.Id == "" && ex.UserId == p.UserId && ex.Message == p.Message {
			m.threadPosts = append(m.threadPosts[:i], m.threadPosts[i+1:]...)
			break
		}
	}
	wasAtBottom := m.threadIdx >= len(m.threadPosts)-1
	m.threadPosts = append(m.threadPosts, p)
	if wasAtBottom {
		m.threadIdx = len(m.threadPosts) - 1
	}
}

func (m *Model) applyPostEdited(ev *model.WebSocketEvent) tea.Cmd {
	p := parsePost(ev)
	if p == nil {
		return nil
	}
	m.invalidatePostLines(p.Id)
	cmds := []tea.Cmd{m.persistPosts(p)}
	if m.isCurrentChannel(p.ChannelId) {
		for i, ex := range m.posts {
			if ex.Id == p.Id {
				m.posts[i] = p
				m.renderMessages()
				if needsFileInfoFetch(p) {
					cmds = append(cmds, m.fetchFileInfos(p.Id))
				}
				break
			}
		}
	}
	if m.isThreadPost(p) {
		for i, ex := range m.threadPosts {
			if ex.Id == p.Id {
				m.threadPosts[i] = p
				m.renderThread()
				break
			}
		}
	}
	return tea.Batch(cmds...)
}

// needsFileInfoFetch reports whether a post claims file attachments but
// arrived without resolved FileInfo metadata.
func needsFileInfoFetch(p *model.Post) bool {
	return len(p.FileIds) > 0 && (p.Metadata == nil || len(p.Metadata.Files) == 0)
}

func (m *Model) applyPostDeleted(ev *model.WebSocketEvent) tea.Cmd {
	p := parsePost(ev)
	if p == nil {
		return nil
	}
	m.invalidatePostLines(p.Id)
	// Drop it from the unread feed too, in case it's showing there.
	m.feedRemovePost(p.Id)
	// If the post we're currently editing just disappeared from under
	// us, drop edit-mode state so the textarea returns to its normal
	// prompt instead of staying stuck on "✎ ".
	if m.editingPostID != "" && m.editingPostID == p.Id {
		m.cancelEdit()
		m.status = "message was deleted; edit cancelled"
	}
	persistCmd := m.persistDelete(p.Id)
	if m.isCurrentChannel(p.ChannelId) {
		for i, ex := range m.posts {
			if ex.Id == p.Id {
				m.posts = append(m.posts[:i], m.posts[i+1:]...)
				if i < m.postIdx {
					m.postIdx--
				}
				m.renderMessages()
				break
			}
		}
	}
	if m.isThreadPost(p) {
		// If the root itself was deleted, drop the whole sidebar — there's
		// nothing left to show.
		if p.Id == m.threadRootID {
			m.closeThread()
			return persistCmd
		}
		for i, ex := range m.threadPosts {
			if ex.Id == p.Id {
				m.threadPosts = append(m.threadPosts[:i], m.threadPosts[i+1:]...)
				if i < m.threadIdx {
					m.threadIdx--
				}
				m.renderThread()
				break
			}
		}
	}
	return persistCmd
}

func (m *Model) isCurrentChannel(channelID string) bool {
	vis := m.visibleChannels()
	return m.channelIdx < len(vis) && vis[m.channelIdx].Id == channelID
}

// parsePost extracts and unmarshals the JSON-encoded post embedded in
// `posted` / `post_edited` / `post_deleted` event data.
func parsePost(ev *model.WebSocketEvent) *model.Post {
	raw, ok := ev.GetData()["post"].(string)
	if !ok || raw == "" {
		return nil
	}
	var p model.Post
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil
	}
	return &p
}

// wsMentions returns the set of user IDs explicitly mentioned in the
// event (Mattermost JSON-encodes the list into data["mentions"]).
func wsMentions(ev *model.WebSocketEvent) map[string]bool {
	out := map[string]bool{}
	raw, ok := ev.GetData()["mentions"].(string)
	if !ok || raw == "" {
		return out
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// wsBackoff returns the backoff delay for the n-th consecutive failure
// (1 → 1s, 2 → 2s, …, capped at 32s).
func wsBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	shift := n - 1
	if shift > 5 {
		shift = 5
	}
	return time.Second << shift
}

func isUnauthorized(err error) bool {
	s := err.Error()
	return strings.Contains(s, "401") || strings.Contains(strings.ToLower(s), "unauthorized")
}

// maybeFetchInitialPosts kicks off the first post fetch once both teams
// and channels have arrived. Returns nil if either is still pending.
func (m *Model) maybeFetchInitialPosts() tea.Cmd {
	if !m.teamsLoaded || !m.channelsLoaded {
		return nil
	}
	m.ensureSelection()
	vis := m.visibleChannels()
	if len(vis) == 0 {
		m.loading = false
		m.status = "no channels"
		return nil
	}
	if m.posts != nil {
		return nil
	}
	return m.openChannelLoadCmd(vis[m.channelIdx].Id)
}

// ensureSelection clamps teamIdx/channelIdx to valid values given current
// teams + channels state. If a last-active channel was recorded from a
// previous session, it is restored first; otherwise the first team with
// channels is selected. Falls back to DMs if no team channels exist.
func (m *Model) ensureSelection() {
	if len(m.teams) == 0 && !m.hasDMs {
		return
	}
	m.restoreLastActive()
	if m.teamIdx > m.maxTeamIdx() {
		m.teamIdx = 0
	}
	for tries := 0; tries <= m.maxTeamIdx(); tries++ {
		if len(m.channels[m.currentTeamID()]) > 0 {
			break
		}
		m.teamIdx++
		if m.teamIdx > m.maxTeamIdx() {
			m.teamIdx = 0
			break
		}
	}
	if m.channelIdx >= len(m.visibleChannels()) {
		m.channelIdx = 0
		m.chanOff = 0
	}
}

// maxTeamIdx returns the highest valid teamIdx, accounting for the
// synthetic DM tab (when present) and the always-present Unread + Feed +
// Search tabs.
func (m *Model) maxTeamIdx() int {
	n := len(m.teams)
	n++ // Unread is always present
	n++ // Feed is always present
	n++ // Search is always present
	if m.hasDMs {
		n++
	}
	n--
	if n < 0 {
		n = 0
	}
	return n
}

// handlePaste routes bracketed-paste (terminal right-click / shift-insert /
// terminal-level paste) into whichever text component is currently focused.
// Without this the PasteMsg falls through to the messages viewport and the
// pasted text is dropped on the floor.
func (m Model) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if m.switcherMode {
		old := m.switcher.Value()
		var cmd tea.Cmd
		m.switcher, cmd = m.switcher.Update(msg)
		if m.switcher.Value() != old {
			m.switcherIdx = 0
		}
		return m, cmd
	}
	if m.filterMode {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.filterValue = m.filter.Value()
		m.channelIdx = 0
		m.chanOff = 0
		return m, cmd
	}
	if m.focus == focusInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		mentionCmd := m.updateMention()
		m.syncInputHeight()
		return m, tea.Batch(cmd, mentionCmd)
	}
	if m.focus == focusSearch {
		old := m.search.input.Value()
		var cmd tea.Cmd
		m.search.input, cmd = m.search.input.Update(msg)
		if m.search.input.Value() != old {
			debounceCmd := m.scheduleSearch()
			return m, tea.Batch(cmd, debounceCmd)
		}
		return m, cmd
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Delete-confirmation modal is fully modal: y/enter performs the
	// delete, n/esc cancels. Anything else is ignored.
	if m.deleteConfirmPostID != "" {
		return m.handleDeleteConfirmKey(msg)
	}
	// Reaction picker modal owns every keystroke while open.
	if m.reactionPickerPostID != "" {
		return m.handleReactionPickerKey(msg)
	}
	// Poll-dialog modal (e.g. matterpoll "Add Option") owns every
	// keystroke while open.
	if m.pollDialog.open {
		return m.handlePollDialogKey(msg)
	}
	// History popup is fully modal: it owns every keystroke while open
	// so esc/arrows route to the popup viewport, not the underlying pane.
	if m.historyMode {
		return m.handleHistoryKey(msg)
	}
	// Summary modal (duration picker / running / result) owns every
	// keystroke while open. Opened from the switcher's "> Summarize"
	// command, which closes the switcher first, so there's no overlap.
	if m.summary.active() {
		return m.handleSummaryKey(msg)
	}
	// Switcher is fully modal: it owns every keystroke while open. Check
	// before any other mode so escape/enter/etc. don't leak through.
	if m.switcherMode {
		return m.handleSwitcherKey(msg)
	}
	// ctrl+k opens the switcher from anywhere — even inside the input or
	// the filter, where the regular handlers below would otherwise eat it
	// (textarea binds ctrl+k to delete-to-end-of-line by default).
	if key.Matches(msg, m.keys.Switcher) && msg.String() != "ctrl+c" {
		return m.openSwitcher()
	}
	// Filter mode and input mode each own most keys while active; check
	// before the navigation shortcuts so plain letters ("," / "f" / "F" /
	// "U" / "q" / "/") don't leak through while the user is typing.
	if m.filterMode {
		return m.handleFilterKey(msg)
	}
	if m.focus == focusInput {
		return m.handleInputKey(msg)
	}
	// Search input owns every keystroke while focused — otherwise the
	// navigation shortcuts below would fire mid-typing.
	if m.focus == focusSearch {
		return m.handleSearchKey(msg)
	}

	// Below here we're in a navigation focus (channels / messages / thread
	// / attachments / teams / feed), so plain-character shortcuts are safe.
	// The "," leader chord, F (global search), and U (unread feed) are
	// dispatched here rather than globally for exactly that reason.
	if m.leaderPending {
		return m.handleLeaderKey(msg)
	}
	if key.Matches(msg, m.keys.Leader) {
		m.leaderPending = true
		return m, nil
	}
	if key.Matches(msg, m.keys.Search) { // F → global search, empty box
		return m, m.openSearchTab()
	}
	if key.Matches(msg, m.keys.Feed) { // U → combined unread feed
		return m, m.openFeedTab()
	}

	switch {
	case msg.String() == "ctrl+c":
		return m, tea.Quit
	case key.Matches(msg, m.keys.Quit):
		if m.focus == focusChannels && m.filterValue != "" {
			// Don't quit while a filter is applied; let user clear with esc.
			return m, nil
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.resizeMessagesViewport()
		return m, nil

	case key.Matches(msg, m.keys.Unread):
		return m.jumpToUnread()

	case key.Matches(msg, m.keys.Tab):
		return m.cycleFocus(1)
	case key.Matches(msg, m.keys.ShiftTab):
		return m.cycleFocus(-1)

	case key.Matches(msg, m.keys.SearchHere):
		// "/" searches the current channel's messages (prefilled scope).
		return m, m.openSearchHere()
	case key.Matches(msg, m.keys.Filter):
		// "f" filters the channel-list sidebar (the relocated old "/").
		if m.focus == focusChannels {
			m.filterMode = true
			m.filter.SetValue(m.filterValue)
			m.filter.Focus()
			return m, nil
		}
	case msg.String() == "esc":
		if m.filterValue != "" {
			m.filterValue = ""
			m.filter.SetValue("")
			m.channelIdx = 0
			m.chanOff = 0
			return m, nil
		}
		if m.threadOpen {
			m.closeThread()
			return m, nil
		}
	}

	switch m.focus {
	case focusChannels:
		return m.handleChannelsKey(msg)
	case focusMessages:
		return m.handleMessagesKey(msg)
	case focusThread:
		return m.handleThreadKey(msg)
	case focusAttachments:
		return m.handleAttachmentsKey(msg)
	case focusTeams:
		return m.handleTeamsKey(msg)
	case focusSearch:
		return m.handleSearchKey(msg)
	case focusFeed:
		return m.handleFeedKey(msg)
	}
	return m, nil
}

func (m Model) handleAttachmentsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if len(m.attachments) == 0 {
		m.focus = focusInput
		cmd := m.input.Focus()
		return m, cmd
	}
	switch {
	case key.Matches(msg, m.keys.Left):
		if m.attachmentIdx > 0 {
			m.attachmentIdx--
		}
		return m, nil
	case key.Matches(msg, m.keys.Right):
		if m.attachmentIdx < len(m.attachments)-1 {
			m.attachmentIdx++
		}
		return m, nil
	case key.Matches(msg, m.keys.Home):
		m.attachmentIdx = 0
		return m, nil
	case key.Matches(msg, m.keys.End):
		m.attachmentIdx = len(m.attachments) - 1
		return m, nil
	case key.Matches(msg, m.keys.OpenAttach):
		att := m.attachments[m.attachmentIdx]
		m.status = "opening " + att.filename + "…"
		return m, xdgOpenPath(att.filename, att.localPath)
	case key.Matches(msg, m.keys.AttachRemove):
		id := m.attachments[m.attachmentIdx].id
		m.removeAttachment(id)
		return m, nil
	}
	return m, nil
}

func (m Model) handleThreadKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.threadIdx >= 0 && m.threadIdx < len(m.threadPosts) {
		if mm, cmd, handled := m.handlePollKey(m.threadPosts[m.threadIdx], msg); handled {
			return mm, cmd
		}
	}
	switch {
	case key.Matches(msg, m.keys.CloseThread):
		m.closeThread()
		return m, nil
	case key.Matches(msg, m.keys.Up):
		if m.threadIdx > 0 {
			m.threadIdx--
			m.renderThread()
		}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if m.threadIdx < len(m.threadPosts)-1 {
			m.threadIdx++
			m.renderThread()
		}
		return m, nil
	case key.Matches(msg, m.keys.Home):
		m.threadIdx = 0
		m.renderThread()
		return m, nil
	case key.Matches(msg, m.keys.End):
		m.threadIdx = len(m.threadPosts) - 1
		m.renderThread()
		return m, nil
	case key.Matches(msg, m.keys.OpenAttach):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			m.status = "no message selected"
			return m, nil
		}
		opens := collectOpenables(m.threadPosts[m.threadIdx])
		if len(opens) == 0 {
			m.status = "nothing to open on this message"
			return m, nil
		}
		o := opens[0]
		m.status = "opening " + o.name + "…"
		return m, m.openOpenable(o)
	case key.Matches(msg, m.keys.CopyMD):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			m.status = "no message selected"
			return m, nil
		}
		return m, m.copyPostMarkdown(m.threadPosts[m.threadIdx])
	case key.Matches(msg, m.keys.ShowHistory):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			return m, nil
		}
		p := m.threadPosts[m.threadIdx]
		if p.EditAt == 0 {
			m.status = "message has not been edited"
			return m, nil
		}
		m.openHistory(p)
		return m, nil
	case key.Matches(msg, m.keys.EditPost):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			return m, nil
		}
		p := m.threadPosts[m.threadIdx]
		if !m.canMutatePost(p) {
			m.status = "can only edit your own messages"
			return m, nil
		}
		return m, m.beginEditPost(p)
	case key.Matches(msg, m.keys.DeletePost):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			return m, nil
		}
		p := m.threadPosts[m.threadIdx]
		if !m.canMutatePost(p) {
			m.status = "can only delete your own messages"
			return m, nil
		}
		m.openDeleteConfirm(p)
		return m, nil
	case key.Matches(msg, m.keys.React):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			return m, nil
		}
		p := m.threadPosts[m.threadIdx]
		if p.Id == "" {
			m.status = "message hasn't landed yet"
			return m, nil
		}
		m.openReactionPicker(p.Id)
		return m, nil
	}
	var cmd tea.Cmd
	m.threadView, cmd = m.threadView.Update(msg)
	return m, cmd
}

// jumpToUnread switches to the synthetic Unread tab and selects its
// first channel (loading its messages). If nothing is unread, it leaves
// the tab focused with an "all caught up" status so the user still gets
// confirmation that `u` did something.
func (m Model) jumpToUnread() (tea.Model, tea.Cmd) {
	target := -1
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, _, _ := m.tabAt(i); kind == tabUnread {
			target = i
			break
		}
	}
	if target < 0 {
		return m, nil
	}
	m.teamIdx = target
	m.focus = focusChannels
	m.channelIdx = 0
	m.chanOff = 0
	m.filterMode = false
	m.filterValue = ""
	m.filter.SetValue("")
	m.filter.Blur()
	m.input.Blur()
	vis := m.visibleChannels()
	if len(vis) == 0 {
		m.posts = nil
		m.renderMessages()
		m.status = "all caught up"
		return m, nil
	}
	return m, m.openChannelLoadCmd(vis[0].Id)
}

// handleLeaderKey resolves the second key of a "," leader chord. It is
// only reached when m.leaderPending is set (in handleKey, after the ","
// leader key in a navigation focus); the flag is always cleared here.
// Unrecognized second keys — including esc — simply cancel the chord.
func (m Model) handleLeaderKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.leaderPending = false
	switch msg.String() {
	case "t":
		m.focus = focusTeams
		m.input.Blur()
		m.renderMessages()
		return m, nil
	case "c":
		if m.onSearchTab() || m.onFeedTab() {
			return m, nil // no channel sidebar on these tabs
		}
		m.focus = focusChannels
		m.input.Blur()
		m.renderMessages()
		return m, nil
	case "m":
		if m.onSearchTab() || m.onFeedTab() {
			return m, nil // no messages pane on these tabs
		}
		m.focus = focusMessages
		m.input.Blur()
		m.renderMessages()
		return m, nil
	case "i":
		if m.onSearchTab() || m.onFeedTab() {
			return m, nil // no composer on these tabs
		}
		m.focus = focusInput
		m.renderMessages()
		return m, m.input.Focus()
	case "d":
		return m.gotoDMTab()
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return m.gotoTeam(int(msg.String()[0] - '0'))
	}
	return m, nil
}

// gotoTab switches the active tab to index target, focuses its content,
// and loads the first channel. Search/Feed tabs focus their body instead
// of a channel list. Mirrors the LoadTeam (enter-on-tab) behaviour so the
// "," tab jumps land somewhere useful.
func (m Model) gotoTab(target int) (tea.Model, tea.Cmd) {
	m.teamIdx = target
	m.filterMode = false
	m.filterValue = ""
	m.filter.SetValue("")
	m.filter.Blur()
	m.input.Blur()
	if m.onSearchTab() {
		m.focus = focusSearch
		m.search.input.Focus()
		return m, nil
	}
	if m.onFeedTab() {
		m.focus = focusFeed
		return m, m.buildFeed()
	}
	m.focus = focusChannels
	m.channelIdx = 0
	m.chanOff = 0
	vis := m.visibleChannels()
	if len(vis) == 0 {
		m.posts = nil
		m.renderMessages()
		if m.currentTeamID() == unreadTeamID {
			m.status = "all caught up"
		} else {
			m.status = "no channels in this team"
		}
		return m, nil
	}
	ch := vis[0]
	return m, tea.Batch(m.openChannelLoadCmd(ch.Id), m.bumpChannelStat(ch.Id))
}

// gotoDMTab jumps to the synthetic DMs tab (",d"). No-op with a hint when
// the user has no direct messages.
func (m Model) gotoDMTab() (tea.Model, tea.Cmd) {
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, _, _ := m.tabAt(i); kind == tabDM {
			return m.gotoTab(i)
		}
	}
	m.status = "no direct messages"
	return m, nil
}

// gotoTeam jumps to the n-th real team (1-based) in the tab bar, skipping
// the synthetic DM/Unread/Feed/Search tabs (",1".."9"). No-op when there
// is no n-th team.
func (m Model) gotoTeam(n int) (tea.Model, tea.Cmd) {
	if n < 1 {
		return m, nil
	}
	count := 0
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, _, _ := m.tabAt(i); kind == tabTeam {
			count++
			if count == n {
				return m.gotoTab(i)
			}
		}
	}
	return m, nil
}

// cycleFocus advances the active focus by `step` (typically +1 / -1)
// and syncs the input's bubble-level focus so its cursor blinks only
// while focused. focusThread is skipped when the sidebar is closed.
// On the synthetic Search tab focus is constrained to {Teams, Search};
// otherwise focusSearch is skipped.
func (m Model) cycleFocus(step int) (tea.Model, tea.Cmd) {
	onSearch := m.onSearchTab()
	onFeed := m.onFeedTab()
	for i := 0; i < numFocus; i++ {
		m.focus = focus((int(m.focus) + step + numFocus) % numFocus)
		if m.focus == focusThread && !m.threadOpen {
			continue
		}
		if m.focus == focusAttachments && len(m.attachments) == 0 {
			continue
		}
		if m.focus == focusSearch && !onSearch {
			continue
		}
		if m.focus == focusFeed && !onFeed {
			continue
		}
		if onSearch && m.focus != focusTeams && m.focus != focusSearch {
			continue
		}
		if onFeed && m.focus != focusTeams && m.focus != focusFeed {
			continue
		}
		break
	}
	var cmd tea.Cmd
	if m.focus == focusInput {
		cmd = m.input.Focus()
	} else {
		m.input.Blur()
	}
	if m.focus == focusSearch {
		m.search.input.Focus()
	} else {
		m.search.input.Blur()
	}
	// Entering the Feed pane for the first time this session builds it
	// lazily so arrowing onto the tab and tab-ing in shows fresh unreads.
	var buildCmd tea.Cmd
	if m.focus == focusFeed && !m.feed.built && !m.feed.loading {
		buildCmd = m.buildFeed()
	}
	// Bar visibility depends on whether messages pane has focus.
	m.renderMessages()
	m.renderThread()
	return m, tea.Batch(cmd, buildCmd)
}

func (m Model) handleInputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// When the @-mention popup is open with at least one candidate, it
	// owns navigation/accept/dismiss keys before the normal input flow.
	if m.mention.active && len(m.mention.items) > 0 {
		switch msg.String() {
		case "up", "ctrl+p":
			if m.mention.idx > 0 {
				m.mention.idx--
			}
			return m, nil
		case "down", "ctrl+n":
			if m.mention.idx < len(m.mention.items)-1 {
				m.mention.idx++
			}
			return m, nil
		case "tab", "enter":
			if m.acceptMention() {
				return m, nil
			}
		case "esc":
			m.closeMention()
			return m, nil
		}
	}

	switch {
	case msg.String() == "ctrl+c":
		return m, tea.Quit
	case key.Matches(msg, m.keys.Paste):
		return m, readClipboard()
	case key.Matches(msg, m.keys.Tab):
		return m.cycleFocus(1)
	case key.Matches(msg, m.keys.ShiftTab):
		return m.cycleFocus(-1)
	case key.Matches(msg, m.keys.LeaveInput):
		m.closeMention()
		// Cancelling an in-progress edit is the more useful semantic
		// when in edit mode — drop the prefilled text and prompt
		// before leaving the input.
		if m.editingPostID != "" {
			m.cancelEdit()
		}
		m.input.Blur()
		// When the thread sidebar is open, the input lives inside it —
		// escape should return focus to that pane rather than jumping
		// over to the messages list.
		if m.threadOpen {
			m.focus = focusThread
			m.renderMessages()
			m.renderThread()
			return m, nil
		}
		m.focus = focusMessages
		m.renderMessages()
		return m, nil
	case key.Matches(msg, m.keys.Send):
		text := strings.TrimSpace(m.input.Value())
		// Editing branches off here: empty text isn't allowed (mattermost
		// rejects a patch that would blank the message), but attachments
		// are irrelevant — edits only touch the body.
		if m.editingPostID != "" {
			if text == "" {
				m.status = "edited message can't be empty"
				return m, nil
			}
			id := m.editingPostID
			m.editingPostID = ""
			m.input.Reset()
			m.syncInputHeight()
			m.closeMention()
			m.restoreInputPrompt()
			m.status = "saving edit…"
			return m, m.editPost(id, text)
		}
		if text == "" && len(m.attachments) == 0 {
			return m, nil
		}
		if m.hasUploadingAttachments() {
			m.status = "waiting for upload…"
			return m, nil
		}
		// Replying inside an open thread targets the thread's channel
		// regardless of which channel is selected in the sidebar list —
		// otherwise switching channels while the thread is up would
		// silently send to the wrong place.
		var channelID, rootID string
		if m.threadOpen {
			channelID = m.threadChannelID
			rootID = m.threadRootID
		} else {
			vis := m.visibleChannels()
			if m.channelIdx >= len(vis) {
				return m, nil
			}
			channelID = vis[m.channelIdx].Id
		}
		fileIDs := m.collectAttachmentFileIDs()
		m.input.Reset()
		m.syncInputHeight()
		m.closeMention()
		m.appendOptimistic(channelID, rootID, text, fileIDs)
		m.clearAttachments()
		m.resizeMessagesViewport()
		if !m.threadOpen {
			m.postIdx = len(m.posts) - 1
		}
		m.renderMessages()
		m.renderThread()
		m.status = "sending…"
		return m, m.sendMessage(channelID, rootID, text, fileIDs)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// After the textarea has consumed the keystroke, recompute mention
	// state and reflow the input/messages split so newlines from
	// shift+enter (or alt+enter / ctrl+j) make the input grow.
	mentionCmd := m.updateMention()
	m.syncInputHeight()
	return m, tea.Batch(cmd, mentionCmd)
}

func (m Model) handleFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.CancelEdit):
		m.filterMode = false
		m.filterValue = ""
		m.filter.SetValue("")
		m.filter.Blur()
		m.channelIdx = 0
		m.chanOff = 0
		return m, nil
	case key.Matches(msg, m.keys.ApplyOpen):
		m.filterMode = false
		m.filter.Blur()
		// Keep current filter applied. Selecting the highlighted channel:
		vis := m.visibleChannels()
		if len(vis) > 0 && m.channelIdx < len(vis) {
			ch := vis[m.channelIdx]
			return m, tea.Batch(m.openChannelLoadCmd(ch.Id), m.bumpChannelStat(ch.Id))
		}
		return m, nil
	case msg.String() == "up", msg.String() == "down":
		// Allow arrow-key navigation of the filtered list while still
		// typing. We deliberately don't accept j/k here — the user may be
		// typing those characters into the filter.
		return m.handleChannelsKey(msg)
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	if v := m.filter.Value(); v != m.filterValue {
		m.filterValue = v
		m.channelIdx = 0
		m.chanOff = 0
	}
	return m, cmd
}

func (m Model) handleChannelsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	vis := m.visibleChannels()
	if len(vis) == 0 {
		return m, nil
	}
	// visibleChannels can shrink under a stale channelIdx — e.g. opening
	// the last entry on the Unread tab clears that channel's unread, and
	// arrow-navigating back to Unread doesn't reset the selection.
	if m.channelIdx >= len(vis) {
		m.channelIdx = len(vis) - 1
	}
	if m.channelIdx < 0 {
		m.channelIdx = 0
	}
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.channelIdx > 0 {
			m.channelIdx--
		}
	case key.Matches(msg, m.keys.Down):
		if m.channelIdx < len(vis)-1 {
			m.channelIdx++
		}
	case key.Matches(msg, m.keys.Home):
		m.channelIdx = 0
	case key.Matches(msg, m.keys.End):
		m.channelIdx = len(vis) - 1
	case key.Matches(msg, m.keys.OpenChannel):
		ch := vis[m.channelIdx]
		// When opening from the virtual Unread tab, hop to the channel's
		// home team so isCurrentChannel keeps tracking the open channel
		// after its unread count clears and it leaves the Unread list.
		if m.currentTeamID() == unreadTeamID {
			m.switchToChannelHomeTeam(ch)
			m.filterValue = ""
			m.filter.SetValue("")
		}
		m.focus = focusInput
		loadCmd := m.openChannelLoadCmd(ch.Id)
		saveCmd := m.bumpChannelStat(ch.Id)
		return m, tea.Batch(m.input.Focus(), loadCmd, saveCmd)
	}
	return m, nil
}

// messagesPageStep is how many posts PageDown / PageUp move the selection.
// Posts are variable-height, so this is a heuristic derived from the
// viewport height (a fraction of the visible lines) rather than an exact
// screenful; at least one so the keys always move.
func (m Model) messagesPageStep() int {
	if s := m.msgsView.Height() / 3; s > 1 {
		return s
	}
	return 1
}

func (m Model) handleMessagesKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Poll accelerators (digits / a / E / X) act on a poll post under
	// the cursor, before the regular messages-pane handler picks them
	// up. The handler is a no-op when the selected post isn't a poll,
	// so plain letters still fall through to their normal binding.
	if m.postIdx >= 0 && m.postIdx < len(m.posts) {
		if mm, cmd, handled := m.handlePollKey(m.posts[m.postIdx], msg); handled {
			return mm, cmd
		}
	}
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.postIdx > 0 {
			m.postIdx--
			m.renderMessages()
			return m, nil
		}
		// At the top — try to page further back through the cache so the
		// user can keep scrolling into older history rather than hitting
		// a wall at the initial render limit.
		if len(m.posts) > 0 {
			older := m.loadOlderFromStore(m.posts[0].ChannelId, m.posts[0].CreateAt)
			if len(older) > 0 {
				m.posts = append(older, m.posts...)
				m.postIdx = len(older) - 1
				m.status = ""
				// Pin the new selection to the top of the viewport so
				// the previously-visible content stays where it was on
				// screen, with the just-loaded older posts appearing
				// above it (reachable by continuing to scroll up).
				m.anchorMsgSelTop = true
				m.renderMessages()
			} else {
				m.status = "no more cached messages"
			}
		}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if m.postIdx < len(m.posts)-1 {
			m.postIdx++
			m.renderMessages()
			return m, nil
		}
		// At the bottom — try to page forward through the cache.
		// Mirror of the Up-at-top load-older path. Especially useful
		// after opening a search hit, where m.posts is centred on the
		// matched message and there may be plenty of cached history
		// after it the user wants to read forward into.
		if len(m.posts) > 0 {
			last := m.posts[len(m.posts)-1]
			newer := m.loadNewerFromStore(last.ChannelId, last.CreateAt)
			if len(newer) > 0 {
				oldLen := len(m.posts)
				m.posts = append(m.posts, newer...)
				m.postIdx = oldLen
				m.status = ""
				m.renderMessages()
			} else {
				m.status = "no more cached messages"
			}
		}
		return m, nil
	case key.Matches(msg, m.keys.Home):
		m.postIdx = 0
		m.renderMessages()
		return m, nil
	case key.Matches(msg, m.keys.End):
		m.postIdx = len(m.posts) - 1
		m.renderMessages()
		return m, nil
	case key.Matches(msg, m.keys.PageDown):
		if len(m.posts) == 0 {
			return m, nil
		}
		m.postIdx += m.messagesPageStep()
		if m.postIdx > len(m.posts)-1 {
			m.postIdx = len(m.posts) - 1
		}
		m.renderMessages()
		return m, nil
	case key.Matches(msg, m.keys.PageUp):
		if len(m.posts) == 0 {
			return m, nil
		}
		m.postIdx -= m.messagesPageStep()
		if m.postIdx < 0 {
			m.postIdx = 0
		}
		m.renderMessages()
		return m, nil
	case key.Matches(msg, m.keys.NextHit):
		return m.gotoSearchHit(1)
	case key.Matches(msg, m.keys.PrevHit):
		return m.gotoSearchHit(-1)
	case key.Matches(msg, m.keys.OpenThread), key.Matches(msg, m.keys.ReplyInThread):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			return m, nil
		}
		return m.openThreadForPost(m.posts[m.postIdx])
	case key.Matches(msg, m.keys.EditPost):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			return m, nil
		}
		p := m.posts[m.postIdx]
		if !m.canMutatePost(p) {
			m.status = "can only edit your own messages"
			return m, nil
		}
		return m, m.beginEditPost(p)
	case key.Matches(msg, m.keys.DeletePost):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			return m, nil
		}
		p := m.posts[m.postIdx]
		if !m.canMutatePost(p) {
			m.status = "can only delete your own messages"
			return m, nil
		}
		m.openDeleteConfirm(p)
		return m, nil
	case key.Matches(msg, m.keys.OpenAttach):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			m.status = "no message selected"
			return m, nil
		}
		opens := collectOpenables(m.posts[m.postIdx])
		if len(opens) == 0 {
			m.status = "nothing to open on this message"
			return m, nil
		}
		o := opens[0]
		m.status = "opening " + o.name + "…"
		return m, m.openOpenable(o)
	case key.Matches(msg, m.keys.CopyMD):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			m.status = "no message selected"
			return m, nil
		}
		return m, m.copyPostMarkdown(m.posts[m.postIdx])
	case key.Matches(msg, m.keys.ShowHistory):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			return m, nil
		}
		p := m.posts[m.postIdx]
		if p.EditAt == 0 {
			m.status = "message has not been edited"
			return m, nil
		}
		m.openHistory(p)
		return m, nil
	case key.Matches(msg, m.keys.React):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			return m, nil
		}
		p := m.posts[m.postIdx]
		if p.Id == "" {
			m.status = "message hasn't landed yet"
			return m, nil
		}
		m.openReactionPicker(p.Id)
		return m, nil
	}
	// Anything else (pgup/pgdn, half-page, etc.) falls through to viewport.
	var cmd tea.Cmd
	m.msgsView, cmd = m.msgsView.Update(msg)
	return m, cmd
}

// canMutatePost reports whether the current user is allowed (per local
// state) to edit/delete this post. The server will reject anything we
// missed; this gate is just for UX so we don't open prompts that will
// definitely fail. Optimistic stubs (empty Id) are excluded — they
// haven't landed on the server yet.
func (m Model) canMutatePost(p *model.Post) bool {
	if p == nil || p.Id == "" || m.me == nil {
		return false
	}
	return p.UserId == m.me.Id
}

// handleDeleteConfirmKey owns every keystroke while the delete modal
// is open. y/enter confirms (fires the DeletePost call); n/esc cancels.
func (m Model) handleDeleteConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "y", "Y", "enter":
		id := m.deleteConfirmPostID
		m.closeDeleteConfirm()
		// If we were editing the same post, drop the edit state — the
		// post is about to disappear from under us.
		if m.editingPostID == id {
			m.cancelEdit()
		}
		m.status = "deleting…"
		return m, m.deletePost(id)
	case "n", "N", "esc", "q":
		m.closeDeleteConfirm()
		return m, nil
	}
	return m, nil
}

// openThreadForPost figures out which thread the selected post belongs
// to (its own Id if it's a root, otherwise its RootId) and opens the
// thread sidebar. Optimistic stubs (empty Id) are ignored. The compose
// textarea takes focus immediately so the user can start typing a reply
// without a separate keystroke.
func (m Model) openThreadForPost(p *model.Post) (tea.Model, tea.Cmd) {
	rootID := p.RootId
	if rootID == "" {
		rootID = p.Id
	}
	if rootID == "" {
		return m, nil // optimistic stub, no canonical Id yet
	}
	channelID := p.ChannelId
	// Same thread already open? Just refocus the input.
	if m.threadOpen && m.threadRootID == rootID {
		m.focus = focusInput
		cmd := m.input.Focus()
		m.renderMessages()
		m.renderThread()
		return m, cmd
	}
	m.threadOpen = true
	m.threadRootID = rootID
	m.threadChannelID = channelID
	m.threadPosts = nil
	m.threadIdx = 0
	m.threadLoading = true
	// Don't clobber a "✎ " prompt the user is mid-edit on — beginEditPost
	// owns the prompt while editingPostID is set, and the patch will
	// fire on the original post regardless of which pane is open.
	if m.editingPostID == "" {
		m.input.SetPromptFunc(2, inputPromptFunc("↳ "))
	}
	m.focus = focusInput
	focusCmd := m.input.Focus()
	m.resizeMessagesViewport()
	m.resizeInput()
	m.renderMessages()
	m.renderThread()
	return m, tea.Batch(m.fetchThread(rootID), focusCmd)
}

// closeThread tears down the sidebar and returns focus to the messages pane.
func (m *Model) closeThread() {
	if !m.threadOpen {
		return
	}
	m.threadOpen = false
	m.threadRootID = ""
	m.threadChannelID = ""
	m.threadPosts = nil
	m.threadIdx = 0
	m.threadLoading = false
	if m.focus == focusThread {
		m.focus = focusMessages
	}
	// Same as openThreadForPost — leave the prompt alone if an edit is
	// in progress so the user keeps the "✎ " mode indicator.
	if m.editingPostID == "" {
		m.input.SetPromptFunc(2, inputPromptFunc("> "))
	}
	m.resizeMessagesViewport()
	m.resizeInput()
	m.renderMessages()
}

func (m Model) handleTeamsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	max := m.maxTeamIdx()
	if max == 0 && len(m.teams) == 0 && !m.hasDMs {
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.MoveTeamLeft):
		if m.moveTeam(-1) {
			return m, m.persistTeamOrder()
		}
		return m, nil
	case key.Matches(msg, m.keys.MoveTeamRight):
		if m.moveTeam(1) {
			return m, m.persistTeamOrder()
		}
		return m, nil
	case key.Matches(msg, m.keys.Left):
		if m.teamIdx > 0 {
			m.teamIdx--
		}
		return m, m.maybeBuildFeedOnLand()
	case key.Matches(msg, m.keys.Right):
		if m.teamIdx < max {
			m.teamIdx++
		}
		return m, m.maybeBuildFeedOnLand()
	case key.Matches(msg, m.keys.LoadTeam):
		if m.onSearchTab() {
			m.focus = focusSearch
			m.search.input.Focus()
			return m, nil
		}
		if m.onFeedTab() {
			m.focus = focusFeed
			return m, m.buildFeed()
		}
		m.focus = focusChannels
		m.channelIdx = 0
		m.chanOff = 0
		m.filterValue = ""
		m.filter.SetValue("")
		vis := m.visibleChannels()
		if len(vis) == 0 {
			m.posts = nil
			m.renderMessages()
			if m.currentTeamID() == unreadTeamID {
				m.status = "all caught up"
			} else {
				m.status = "no channels in this team"
			}
			return m, nil
		}
		ch := vis[m.channelIdx]
		return m, tea.Batch(m.openChannelLoadCmd(ch.Id), m.bumpChannelStat(ch.Id))
	}
	return m, nil
}
