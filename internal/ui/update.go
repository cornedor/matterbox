package ui

import (
	"encoding/json"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
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
	if fetch := nm.fetchPendingEmoji(); fetch != nil {
		cmd = tea.Batch(cmd, fetch)
	}
	if fetch := nm.fetchPendingMRStatus(); fetch != nil {
		cmd = tea.Batch(cmd, fetch)
	}
	if anim := nm.maybeStartEmojiAnim(); anim != nil {
		cmd = tea.Batch(cmd, anim)
	}
	return nm, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.filter.SetWidth(channelsWidth - 4)
		// A resize *drag* fires a storm of these. Re-laying-out the panes is
		// cheap, so do it every frame — borders/scrollbars stay correct and
		// the viewports soft-wrap their existing content. But defer the
		// expensive content re-render (renderMessages walks every loaded post)
		// to a settle tick that fires only once the drag stops, coalescing the
		// storm into a single re-render. resizePreview is deferred with it so a
		// drag doesn't re-transmit the image preview on every frame.
		m.layoutPanes()
		m.resizeInput()
		m.resizeGen++
		return m, resizeSettleCmd(m.resizeGen)

	case resizeSettleMsg:
		// Ignore all but the latest scheduled settle — earlier ticks from the
		// same drag are stale (a newer WindowSizeMsg bumped resizeGen).
		if msg.gen != m.resizeGen {
			return m, nil
		}
		// Pane widths settled; every width-keyed postLineCache entry is stale.
		// Drop the map once here (not per drag frame) so it rebuilds at the
		// final width. The width-independent postMarkdownCache survives, so
		// this re-render re-wraps cached bodies instead of re-styling them.
		m.postLineCache = nil
		m.renderAllPanes()
		// A resize while the image preview is open re-fits + re-transmits it.
		return m, m.resizePreview()

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.PasteMsg:
		return m.handlePaste(msg)

	case tea.ColorProfileMsg:
		// The truecolor half of the custom-emoji image gate (the id rides in
		// the foreground, which a non-truecolor profile would quantise away).
		if m.emojiImg != nil {
			m.emojiImg.setColorProfile(msg.Profile == colorprofile.TrueColor)
		}
		return m, nil

	case uv.KittyGraphicsEvent:
		// Reply to the startup graphics-support probe (see emojiProbeCmd).
		// Transmits use q=2, so only the probe reply should reach here.
		if m.emojiImg != nil && msg.Options.ID == kittyProbeID {
			m.emojiImg.setProbeReply(string(msg.Payload))
			// A late OK overrides a prior timeout (setProbeOK); only a genuine
			// non-OK reply locks the probe as failed.
			if strings.HasPrefix(string(msg.Payload), "OK") {
				m.emojiImg.setProbeOK()
			} else {
				m.emojiImg.setProbeResult(false)
			}
		}
		return m, nil

	case emojiProbeTimeoutMsg:
		if m.emojiImg != nil {
			m.emojiImg.setProbeResult(false)
		}
		return m, nil

	case uv.CellSizeEvent:
		// Reply to the startup cell-size query (see requestCellSize): the
		// terminal's pixel-per-cell, used by the image-preview modal to avoid
		// upscaling a small image. Re-fit any open preview to the new figure (a
		// no-op unless one is up — e.g. after a mid-session font-size change).
		if msg.Width > 0 && msg.Height > 0 {
			m.cellPxW, m.cellPxH = msg.Width, msg.Height
		}
		return m, m.resizePreview()

	case customEmojiListMsg:
		if msg.err == nil {
			m.customEmojiNames = msg.names
		}
		return m, nil

	case emojiImagesFetchedMsg:
		return m.handleEmojiImagesFetched(msg)

	case emojiAnimTickMsg:
		return m, m.advanceEmojiAnim()

	case previewImageLoadedMsg:
		return m.handlePreviewLoaded(msg)

	case previewTickMsg:
		return m.handlePreviewTick(msg)

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
		for id, cs := range msg.customStatuses {
			m.customStatuses[id] = cs
		}
		m.bucketChannels(msg.channels)
		m.channelsLoaded = true
		m.applyUnreadFromMembers()
		cmds := []tea.Cmd{m.maybeFetchInitialPosts()}
		// Seed the :-picker's custom-emoji index once (images stay lazy).
		if m.customEmojiNames == nil {
			if c := m.fetchCustomEmojiList(); c != nil {
				cmds = append(cmds, c)
			}
		}
		// Start the single presence-poll chain now that DM partners are known.
		if !m.statusPollStarted {
			m.statusPollStarted = true
			cmds = append(cmds,
				m.fetchStatuses(),
				tea.Tick(statusPollInterval, func(time.Time) tea.Msg { return statusPollMsg{} }),
			)
		}
		return m, tea.Batch(cmds...)

	case groupDMResolvedMsg:
		return m.applyGroupDMResolved(msg)

	case statusesLoadedMsg:
		for id, st := range msg.statuses {
			m.statuses[id] = st
		}
		return m, nil

	case statusPollMsg:
		// The tick reschedules itself and drives each fetch — decoupled from
		// the fetch result so there's never more than one timer chain.
		return m, tea.Batch(
			m.fetchStatuses(),
			tea.Tick(statusPollInterval, func(time.Time) tea.Msg { return statusPollMsg{} }),
		)

	case membersLoadedMsg:
		m.members = msg.members
		m.membersLoaded = true
		m.applyUnreadFromMembers()
		return m, nil

	case postsLoadedMsg:
		if msg.channelID != m.openChannelID {
			// Stale to the UI, but still worth persisting so the cache
			// keeps growing for unfocused channels we briefly opened.
			return m, m.persistPosts(msg.posts...)
		}
		m.posts = msg.posts
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		m.loading = false
		m.status = ""
		m.postIdx = len(m.posts) - 1
		// If a search hit queued a jump, prefer that over the default
		// "select newest" position.
		m.jumpToPendingPost()
		m.renderMessages()
		// Defer the mark-read (and badge clear) until the channel has been
		// open for the configured dwell; a quick peek leaves it unread.
		return m, tea.Batch(
			m.scheduleMarkViewed(msg.channelID),
			m.persistPosts(msg.posts...),
		)

	case postsGapFilledMsg:
		// Always persist what we got — even if the user has since
		// switched channels, the rows belong in the cache.
		persistCmd := m.persistPosts(msg.posts...)
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		// Only mutate the visible posts slice if the gap-fill is for the
		// channel still open in the pane.
		if msg.channelID != m.openChannelID {
			return m, persistCmd
		}
		// Merge the fetched page into the visible slice by create_at,
		// deduping by Id. A plain append only handles posts newer than
		// everything loaded; the merge also inserts posts that fall
		// *between* loaded ones, which is what fills an interior cache gap
		// (a message posted while offline, hidden beneath a newer live
		// post). Preserve the selection across the re-sort: pin to the new
		// bottom if the user was following live, else keep the same post.
		selID := ""
		if m.postIdx >= 0 && m.postIdx < len(m.posts) {
			selID = m.posts[m.postIdx].Id
		}
		wasAtBottom := m.postIdx >= len(m.posts)-1
		m.posts = mergePostsByTime(m.posts, msg.posts)
		switch {
		case wasAtBottom:
			m.postIdx = len(m.posts) - 1
		case selID != "":
			for i, p := range m.posts {
				if p.Id == selID {
					m.postIdx = i
					break
				}
			}
		}
		if m.postIdx > len(m.posts)-1 {
			m.postIdx = len(m.posts) - 1
		}
		if m.postIdx < 0 {
			m.postIdx = 0
		}
		m.loading = false
		m.status = ""
		// Apply any queued search-result jump now that the gap is filled.
		m.jumpToPendingPost()
		m.renderMessages()
		return m, tea.Batch(
			m.scheduleMarkViewed(msg.channelID),
			persistCmd,
		)

	case olderPostsMsg:
		// Server-fetched page of older history (scroll-up past the loaded
		// window). Persist regardless of focus; only mutate the view if it's
		// still the open channel.
		persistCmd := m.persistPosts(msg.posts...)
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		if msg.channelID != m.openChannelID {
			return m, persistCmd
		}
		if len(msg.posts) == 0 {
			// Nothing older came back. Distinguish the true start of the
			// channel from a transient empty page so we don't cry "beginning"
			// prematurely.
			if msg.atChannelStart {
				m.status = "beginning of channel"
			} else {
				m.status = ""
			}
			return m, persistCmd
		}
		// Merge by create_at (mergePostsByTime inserts across any interior
		// hole the cache would skip) while keeping the selected post pinned
		// to the top of the viewport, so freshly-loaded older posts appear
		// above it and the screen doesn't jump.
		selID := ""
		if m.postIdx >= 0 && m.postIdx < len(m.posts) {
			selID = m.posts[m.postIdx].Id
		}
		m.posts = mergePostsByTime(m.posts, msg.posts)
		if selID != "" {
			for i, p := range m.posts {
				if p.Id == selID {
					m.postIdx = i
					break
				}
			}
		}
		if m.postIdx > len(m.posts)-1 {
			m.postIdx = len(m.posts) - 1
		}
		if m.postIdx < 0 {
			m.postIdx = 0
		}
		m.anchorMsgSelTop = true
		// Keep the loaded window bounded; selection sits near the top after a
		// scroll-up, so the trimmed tail is safely off-screen.
		m.trimPostWindowTail()
		m.status = ""
		m.renderMessages()
		return m, persistCmd

	case newerPostsMsg:
		// Forward mirror of olderPostsMsg (scroll-down past the loaded tail).
		persistCmd := m.persistPosts(msg.posts...)
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		if msg.channelID != m.openChannelID {
			return m, persistCmd
		}
		if len(msg.posts) == 0 {
			m.status = ""
			return m, persistCmd
		}
		selID := ""
		if m.postIdx >= 0 && m.postIdx < len(m.posts) {
			selID = m.posts[m.postIdx].Id
		}
		m.posts = mergePostsByTime(m.posts, msg.posts)
		if selID != "" {
			for i, p := range m.posts {
				if p.Id == selID {
					m.postIdx = i
					break
				}
			}
		}
		if m.postIdx > len(m.posts)-1 {
			m.postIdx = len(m.posts) - 1
		}
		if m.postIdx < 0 {
			m.postIdx = 0
		}
		// Selection sits near the bottom after a scroll-down; trim the head
		// and pin to the bottom so the stale viewport offset doesn't top-align.
		m.anchorMsgSelBottom = true
		m.trimPostWindowHead()
		m.status = ""
		m.renderMessages()
		return m, persistCmd

	case markViewedMsg:
		// The dwell elapsed. Ignore if the user switched away or refocused
		// the channel in the meantime (stale generation / different channel).
		if msg.gen != m.viewGen || msg.channelID != m.openChannelID {
			return m, nil
		}
		delete(m.unread, msg.channelID)
		delete(m.mentions, msg.channelID)
		m.viewSettled = true
		return m, m.markChannelViewed(msg.channelID)

	case mrFetchSettleMsg:
		if msg.gen != m.mrFetchGen {
			return m, nil // stale tick from an earlier nav burst
		}
		// Gen matched: scrolling has paused. Mark settled so the next outer
		// Update wrapper call to fetchPendingMRStatus drains the accumulated
		// pending sightings.
		m.mrFetchSettledGen = m.mrFetchGen
		return m, nil

	case mrStatusLoadedMsg:
		nm, cmd := m.handleMRStatusLoaded(msg)
		return nm, cmd

	case errMsg:
		m.loading = false
		m.status = "error: " + msg.err.Error()
		if isUnauthorized(msg.err) {
			m.status = "auth failed — run `matterbox login` to refresh the token"
		}
		return m, nil

	case wsConnectedMsg:
		m.ws = msg.ws
		m.wsRetry = 0
		if strings.HasPrefix(m.status, "websocket") || strings.HasPrefix(m.status, "reconnecting") {
			m.status = ""
		}
		// Resync presence after a (re)connect — a one-off fetch, no new tick.
		return m, tea.Batch(waitWSEvent(m.ws), m.fetchStatuses())

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
			// Animation frames are throwaway: persisting each one would
			// flood the local edit history and message cache.
			if !m.animatingPost(msg.post.Id) {
				cmd = m.persistPosts(msg.post)
			}
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

	case jiraLoadedMsg:
		return m.handleJiraLoaded(msg)

	case jiraPickerLoadedMsg:
		return m.handleJiraPickerLoaded(msg)

	case jiraAssigneeDebounceMsg:
		return m.handleJiraAssigneeDebounce(msg)

	case jiraMutatedMsg:
		return m.handleJiraMutated(msg)

	case gitlabLoadedMsg:
		return m.handleGitLabLoaded(msg)

	case gitlabMutatedMsg:
		return m.handleGitLabMutated(msg)

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
		// a thread (which may not be the open channel). Otherwise scope to
		// the open channel — what the composer actually targets — not the
		// sidebar cursor.
		var teamID, channelID string
		if m.threadOpen && m.threadChannelID != "" {
			channelID = m.threadChannelID
			teamID = m.threadTeamID()
		} else {
			ch := m.findChannel(m.openChannelID)
			if ch == nil {
				return m, nil
			}
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

	case giphyResolvedMsg:
		// Background Giphy title lookup finished. On error keep the instant
		// expansion that's already in the composer; just note it. On success
		// swap the instant markdown for the upgraded line — but only if it's
		// still present, so a paste the user has since edited isn't clobbered.
		if msg.err != nil {
			m.status = "Giphy title lookup failed: " + msg.err.Error()
			return m, nil
		}
		if msg.markdown == "" || m.focus != focusInput {
			return m, nil
		}
		val := m.input.Value()
		if !strings.Contains(val, msg.old) {
			return m, nil
		}
		m.input.SetValue(strings.Replace(val, msg.old, msg.markdown, 1))
		m.input.CursorEnd()
		m.updateMention()
		m.syncInputHeight()
		m.status = "expanded Giphy link"
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
		if m.aiSearch.phase == aiSearchRunning {
			sp, cmd := m.aiSearch.spinner.Update(msg)
			m.aiSearch.spinner = sp
			// The trace is baked into the viewport via SetContent, so re-render
			// it each tick to advance the spinner glyph between tool steps.
			m.renderSearchResults()
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)

	case indexResultMsg:
		return m, m.applyIndexResult(msg)

	case embedBatchMsg:
		return m, m.applyEmbedBatch(msg)

	case embedTickMsg:
		return m, m.applyEmbedTick(msg)

	case typingStartedMsg:
		return m, m.applyTypingStarted(msg)

	case typingTickMsg:
		return m, m.applyTypingTick(msg)

	case ballStartedMsg:
		return m, m.applyBallStarted(msg)

	case ballTickMsg:
		return m, m.applyBallTick(msg)

	case summaryGatheredMsg:
		return m, m.applySummaryGathered(msg)

	case summaryStreamOpenedMsg:
		return m, m.applySummaryStreamOpened(msg)

	case summaryChunkMsg:
		return m, m.applySummaryChunk(msg)

	case aiSearchOpenedMsg:
		return m, m.applyAISearchOpened(msg)

	case aiSearchUpdateMsg:
		return m, m.applyAISearchUpdate(msg)

	case searchDebounceMsg:
		return m.applySearchDebounce(msg)

	case searchResultsMsg:
		return m.applySearchResults(msg)

	case feedLoadedMsg:
		return m.applyFeedResults(msg)

	case feedWaveTickMsg:
		return m, m.applyFeedWaveTick()

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
	case model.WebsocketEventStatusChange:
		return m.applyStatusChange(ev)
	case model.WebsocketEventUserUpdated:
		return m.applyUserUpdated(ev)
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
	// sender_name is the override-aware label for *this* post; only fold it
	// into the per-user cache when it isn't a webhook/bot override, otherwise
	// a bot post would rename the human's historical messages (same UserId).
	if sn, ok := ev.GetData()["sender_name"].(string); ok && sn != "" {
		if ov, _ := p.GetProp("override_username").(string); ov == "" {
			m.userNames[p.UserId] = strings.TrimPrefix(sn, "@")
		}
	}
	// Keep the DM sidebar ordered by most recent conversation as messages
	// arrive live, not just at startup.
	m.touchChannelActivity(p.ChannelId, p.CreateAt)

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
				// Following live chat: bound the slice during a long session
				// in a busy channel (oldest posts stay in the store, paged
				// back in on scroll-up) and pin the new message to the
				// bottom, since the head drop leaves the viewport offset
				// stale. When the user is instead reading older history we
				// leave the window untrimmed so their view doesn't shift.
				m.trimPostWindowHead()
				m.anchorMsgSelBottom = true
			}
			m.renderMessages()
			// Only mark read immediately once the open channel's dwell has
			// elapsed. While it's still pending, the queued markViewedMsg
			// will cover this post too, so a freshly-opened channel that
			// receives a message within the dwell isn't marked read early.
			if m.viewSettled {
				cmds = append(cmds, m.markChannelViewed(p.ChannelId))
			}
			if needsFileInfoFetch(p) {
				cmds = append(cmds, m.fetchFileInfos(p.Id))
			}
		}
	} else if !m.isThreadPost(p) {
		// Not in the focused channel and not part of the open thread →
		// it's a background channel update. Only root posts bump the
		// channel's unread/mention badge: under collapsed reply threads
		// (which the unread seed in applyUnreadFromMembers assumes) replies
		// surface in the thread view, not as channel-unread. Counting them
		// here would drift the live badge away from the server's root count.
		if p.RootId == "" {
			m.unread[p.ChannelId]++
			if m.me != nil && wsMentions(ev)[m.me.Id] {
				m.mentions[p.ChannelId]++
			}
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
	// Skip persisting frames of a live animation (typing / bouncing
	// ball) — the per-frame churn would otherwise spam the local edit
	// history captured by the posts UPDATE trigger.
	var cmds []tea.Cmd
	if !m.animatingPost(p.Id) {
		cmds = append(cmds, m.persistPosts(p))
	}
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

// isCurrentChannel reports whether channelID is the channel the messages
// pane is showing (the open channel), not merely the sidebar selection.
// Navigation moves the cursor without opening, so optimistic append and
// live post events must track m.openChannelID to keep flowing into the
// visible transcript.
func (m *Model) isCurrentChannel(channelID string) bool {
	return channelID != "" && m.openChannelID == channelID
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

// applyStatusChange folds a presence update into m.statuses. Only the three
// "active" states are kept; offline/ooo (or anything else) clears the entry so
// the dot disappears. No command — the next render picks up the change.
func (m *Model) applyStatusChange(ev *model.WebSocketEvent) tea.Cmd {
	st, _ := ev.GetData()["status"].(string)
	id, _ := ev.GetData()["user_id"].(string)
	if id == "" {
		if b := ev.GetBroadcast(); b != nil {
			id = b.UserId
		}
	}
	if id == "" {
		return nil
	}
	switch st {
	case model.StatusOnline, model.StatusAway, model.StatusDnd:
		m.statuses[id] = st
	default:
		delete(m.statuses, id)
	}
	return nil
}

// applyUserUpdated refreshes a user's cached username and custom status from a
// user_updated event. It only touches users we already track (DM partners /
// seen senders) so the caches don't grow for unrelated profile churn.
func (m *Model) applyUserUpdated(ev *model.WebSocketEvent) tea.Cmd {
	u := userFromEvent(ev)
	if u == nil || u.Id == "" {
		return nil
	}
	if _, known := m.userNames[u.Id]; !known {
		return nil
	}
	if u.Username != "" {
		m.userNames[u.Id] = u.Username
	}
	if cs := u.GetCustomStatus(); cs != nil && (cs.Emoji != "" || cs.Text != "") {
		m.customStatuses[u.Id] = *cs
	} else {
		delete(m.customStatuses, u.Id)
	}
	return nil
}

// userFromEvent extracts the model.User carried in an event's "user" field.
// Mattermost may encode it either as a JSON string (like "post") or as a
// nested object depending on the broadcast path, so handle both.
func userFromEvent(ev *model.WebSocketEvent) *model.User {
	v, ok := ev.GetData()["user"]
	if !ok {
		return nil
	}
	var raw []byte
	switch t := v.(type) {
	case string:
		raw = []byte(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil
		}
		raw = b
	}
	var u model.User
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil
	}
	return &u
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
		// A pasted Giphy link becomes an inline ![alt](url) image instead of a
		// bare URL: expand it instantly (offline, from the GIF id) and, when a
		// Giphy API key is configured, kick off a background lookup that swaps
		// in the GIF's real title + the configured rendition.
		var giphyCmd tea.Cmd
		if md, id, ok := giphyExpand(strings.TrimSpace(msg.Content), m.giphyRendition); ok {
			msg.Content = md
			m.status = "expanded Giphy link"
			if m.giphyAPIKey != "" {
				giphyCmd = giphyLookup(m.ctx, m.giphyAPIKey, id, m.giphyRendition, md)
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		mentionCmd := m.updateMention()
		m.syncInputHeight()
		return m, tea.Batch(cmd, mentionCmd, giphyCmd)
	}
	if m.focus == focusSearch {
		// A finished AI run with the answer box selected pastes into the in-box
		// follow-up field, not the main search input.
		if m.aiSearch.phase == aiSearchDone && m.search.idx <= -1 && m.aiSearch.err == nil {
			var cmd tea.Cmd
			m.aiSearch.followup, cmd = m.aiSearch.followup.Update(msg)
			m.renderSearchResults()
			return m, cmd
		}
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
	// Key inspector is fully modal and checked first: it echoes every decoded
	// keystroke instead of acting on it (esc closes, ctrl+c still quits), so a
	// user can see exactly what the terminal sends for e.g. option+arrow.
	if m.keyDebugMode {
		return m.handleKeyDebugKey(msg)
	}
	// Delete-confirmation modal is fully modal: y/enter performs the
	// delete, n/esc cancels. Anything else is ignored.
	if m.deleteConfirmPostID != "" {
		return m.handleDeleteConfirmKey(msg)
	}
	// Reaction picker modal owns every keystroke while open.
	if m.reactionPickerPostID != "" {
		return m.handleReactionPickerKey(msg)
	}
	// Jira field editors (status/priority/assignee picker, points input) own
	// every keystroke while open.
	if m.jiraPicker.active {
		return m.handleJiraPickerKey(msg)
	}
	if m.jiraPointsActive {
		return m.handleJiraPointsKey(msg)
	}
	// GitLab approve/merge confirm owns every keystroke while open.
	if m.glConfirm.active {
		return m.handleGitLabConfirmKey(msg)
	}
	// Open-target picker modal owns every keystroke while open.
	if m.openPickerActive() {
		return m.handleOpenPickerKey(msg)
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
	// Keyboard cheatsheet popup ("> Keys") is fully modal: esc/q close it,
	// the rest scrolls the viewport. Opened from the switcher, which closes
	// itself first, so there's no overlap.
	if m.keysSheetMode {
		return m.handleKeysSheetKey(msg)
	}
	// Image-preview modal is fully modal: space/esc/q close it, ←/→ cycle the
	// post's images, everything else is swallowed.
	if m.preview.active {
		return m.handlePreviewKey(msg)
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
	// ctrl+p opens the switcher from anywhere — even inside the input or the
	// filter. (ctrl+k used to do this; it's now "prev channel" in the global
	// sidebar navigation below, so the switcher moved to ctrl+p.) The
	// @-mention / :emoji popups bind ctrl+p to "move selection up", so don't
	// steal it while one of those is open in the composer.
	popupOpen := m.focus == focusInput && (m.mention.active || m.emoji.active)
	if key.Matches(msg, m.keys.Switcher) && msg.String() != "ctrl+c" && !popupOpen {
		return m.openSwitcher()
	}
	// F1 opens the same switcher already in command mode (">" pre-filled). A
	// function key never collides with composing, so it needs no typing guard.
	if key.Matches(msg, m.keys.CommandPicker) {
		return m.openCommandPicker()
	}

	// Global sidebar navigation: the modifier-arrow aliases switch team (←/→)
	// and channel (↑/↓) and open the target immediately from ANY focus —
	// including while typing in the composer, filter, or search box — so a
	// draft survives the jump. The ctrl+vim keys do the same, but only here
	// (before the typing guards) when vim_nav is "global"; in "reading" mode
	// they're dispatched below, after the typing guards, so the composer keeps
	// ctrl+h/ctrl+k; "off" never navigates with them. See vimNavMode.
	if mm, cmd, ok := m.dispatchNav(msg, false); ok {
		return mm, cmd
	}
	if m.vimNav == vimNavGlobal {
		if mm, cmd, ok := m.dispatchNav(msg, true); ok {
			return mm, cmd
		}
	}

	// alt+1…9 jump straight to a team from ANY focus, including the composer.
	// Safe to dispatch before the typing guards because the textarea binds
	// alt+f/d/u/b/c/l but no alt+digit, so these never shadow an edit key — and
	// like the arrow nav above, the draft survives the jump. (alt+d / alt+u DO
	// collide with the composer's delete-word / uppercase-word, so those stay
	// below, after the typing guards.)
	if key.Matches(msg, m.keys.NavTeam) {
		return m.gotoTeam(teamDigit(msg))
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

	// Below here we're in a content focus (messages / thread / attachments /
	// teams / feed), so plain-character and alt-letter shortcuts are safe. The
	// alt+d DMs / alt+u Feed jumps, F / ctrl+shift+f (global search), and i
	// (compose) are dispatched here rather than globally so they never shadow
	// the composer's own alt/ctrl edit keys while the user is typing. (alt+1…9
	// team jumps are global — see above — because no alt+digit is an edit key.)

	// vim_nav "reading": the ctrl+vim keys navigate only out here, away from
	// the text inputs (which kept ctrl+h/ctrl+k for emacs editing above).
	if m.vimNav == vimNavReading {
		if mm, cmd, ok := m.dispatchNav(msg, true); ok {
			return mm, cmd
		}
	}

	if key.Matches(msg, m.keys.NavDM) { // alt+d → DMs tab
		return m.gotoDMTab()
	}
	if key.Matches(msg, m.keys.NavFeed) { // alt+u → Feed (unread bubbles)
		return m, m.openFeedTab()
	}
	if key.Matches(msg, m.keys.Search) { // F / ctrl+shift+f → global search, empty box
		return m, m.openSearchTab()
	}
	if key.Matches(msg, m.keys.Compose) { // i → focus the composer
		return m.focusComposer()
	}

	switch {
	case msg.String() == "ctrl+c":
		return m, tea.Quit
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.resizeMessagesViewport()
		return m, nil

	case key.Matches(msg, m.keys.Tab):
		return m.cycleFocus(1)
	case key.Matches(msg, m.keys.ShiftTab):
		return m.cycleFocus(-1)

	case key.Matches(msg, m.keys.SearchHere):
		// "/" searches the current channel's messages (prefilled scope).
		return m, m.openSearchHere()
	case key.Matches(msg, m.keys.Filter):
		// "f" filters the channel-list sidebar. The sidebar is no longer a
		// focus, so this works from any content pane on a channel/DM tab;
		// the Search/Feed tabs have no channel list to filter.
		if !m.onSearchTab() && !m.onFeedTab() {
			m.filterMode = true
			m.filter.SetValue(m.filterValue)
			m.filter.Focus()
			return m, nil
		}
	case key.Matches(msg, m.keys.MoveTeamLeft):
		// Team reorder is a sidebar action, now reachable from any content
		// focus (the sidebar can't be focused to host it anymore).
		if m.moveTeam(-1) {
			return m, m.persistTeamOrder()
		}
		return m, nil
	case key.Matches(msg, m.keys.MoveTeamRight):
		if m.moveTeam(1) {
			return m, m.persistTeamOrder()
		}
		return m, nil
	case msg.String() == "esc":
		// Close the open thread / reference panel first — whichever is the
		// visually dominant thing on screen, so esc dismisses it before touching
		// the sidebar filter.
		if m.threadOpen {
			m.closeThread()
			return m, nil
		}
		if m.refOpen {
			m.closeRef()
			return m, nil
		}
		if m.filterValue != "" {
			m.filterValue = ""
			m.filter.SetValue("")
			m.channelIdx = 0
			m.chanOff = 0
			return m, nil
		}
	}

	switch m.focus {
	case focusMessages:
		return m.handleMessagesKey(msg)
	case focusThread:
		return m.handleThreadKey(msg)
	case focusRef:
		return m.handleRefKey(msg)
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
		return m, openLocalPath(att.filename, att.localPath)
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
			return m, nil
		}
		if len(m.threadPosts) == 0 {
			return m, nil
		}
		// On the last thread reply: ↓ drops into the thread composer (the
		// inverse of ↑-on-the-first-composer-row selecting the last reply).
		return m.focusComposer()
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
		return m.openFromPost(m.threadPosts[m.threadIdx])
	case key.Matches(msg, m.keys.OpenRef):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			m.status = "no message selected"
			return m, nil
		}
		return m.openRefForPost(m.threadPosts[m.threadIdx])
	case key.Matches(msg, m.keys.Preview):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			m.status = "no message selected"
			return m, nil
		}
		return m.openImagePreview(m.threadPosts[m.threadIdx])
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
		return m, m.openReactionPicker(p.Id)
	}
	var cmd tea.Cmd
	m.threadView, cmd = m.threadView.Update(msg)
	return m, cmd
}

// teamDigit reads the team index (1-9) from a NavTeam keypress. The bound
// keys are alt+1…alt+9, so the trailing rune of the key string is the digit;
// returns 0 for anything without a 1-9 suffix (gotoTeam treats <1 as a no-op),
// which keeps a custom rebind that drops the digit from panicking.
func teamDigit(msg tea.KeyPressMsg) int {
	s := msg.String()
	if s == "" {
		return 0
	}
	c := s[len(s)-1]
	if c < '1' || c > '9' {
		return 0
	}
	return int(c - '0')
}

// focusComposer moves focus to the message input so the user can type. It
// is a no-op on the Search and Feed tabs, which have no composer. Bound to
// the bare "i" key in any navigation focus. (Entering a channel deliberately
// does NOT call this — the user opts in to typing rather than having the
// textarea swallow navigation shortcuts.)
func (m Model) focusComposer() (tea.Model, tea.Cmd) {
	if m.onSearchTab() || m.onFeedTab() {
		return m, nil
	}
	m.focus = focusInput
	m.renderMessages()
	return m, m.input.Focus()
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
	// Channel/DM tab: land in the messages pane (the sidebar is not a
	// focus) and open the preferred channel.
	m.focus = focusMessages
	m.chanOff = 0
	vis := m.visibleChannels()
	if len(vis) == 0 {
		m.channelIdx = 0
		m.posts = nil
		m.renderMessages()
		m.status = "no channels in this team"
		return m, nil
	}
	m.channelIdx = m.preferredChannelIdx(vis)
	ch := vis[m.channelIdx]
	return m, tea.Batch(m.openChannelLoadCmd(ch.Id), m.bumpChannelStat(ch.Id))
}

// switchTeamTab moves the active tab one step in `dir` (-1 left, +1 right) via
// gotoTab: a channel tab opens its preferred channel into the messages pane,
// while the Search/Feed tabs focus their own body. Clamped at the ends.
func (m Model) switchTeamTab(dir int) (tea.Model, tea.Cmd) {
	target := m.teamIdx + dir
	if target < 0 || target > m.maxTeamIdx() {
		return m, nil
	}
	return m.gotoTab(target)
}

// dispatchNav tries to handle msg as a sidebar-nav key. includeVim selects
// whether the ctrl+vim keys are considered (the always-global arrow aliases
// are considered regardless); handleKey calls it once with includeVim=false
// before the typing guards and again with includeVim=true on the schedule
// vim_nav dictates. Returns handled=false when msg isn't a nav key so the
// caller falls through to the next layer.
func (m Model) dispatchNav(msg tea.KeyPressMsg, includeVim bool) (tea.Model, tea.Cmd, bool) {
	for _, r := range m.keys.navRoutes {
		if key.Matches(msg, r.arrow) || (includeVim && key.Matches(msg, r.vim)) {
			mm, cmd := m.navMove(r)
			return mm, cmd, true
		}
	}
	return m, nil, false
}

// navMove performs a single nav route: a team switch or a channel switch in
// the route's direction.
func (m Model) navMove(r navRoute) (tea.Model, tea.Cmd) {
	if r.team {
		return m.navTeam(r.dir)
	}
	return m.navChannel(r.dir)
}

// navTeam is the global (any-focus) team switch driven by ctrl+←/→ and
// ctrl+h/l. It steps one tab in `dir`, opening the destination's preferred
// channel via gotoTab (which lands focus in the messages pane for channel
// tabs, or the body for Search/Feed). Clamped at the ends (no wrap).
func (m Model) navTeam(dir int) (tea.Model, tea.Cmd) {
	return m.switchTeamTab(dir)
}

// navChannel is the global (any-focus) channel switch driven by ctrl+↑/↓ and
// ctrl+k/j. It moves the sidebar selection one row in `dir` within the current
// tab's visible channels and opens that channel immediately, leaving focus
// untouched so the user keeps reading. chanOff self-corrects at render. No-op
// when the list is empty or the target is already open.
func (m Model) navChannel(dir int) (tea.Model, tea.Cmd) {
	vis := m.visibleChannels()
	if len(vis) == 0 {
		return m, nil
	}
	idx := m.channelIdx
	if idx >= len(vis) {
		idx = len(vis) - 1
	}
	if idx < 0 {
		idx = 0
	}
	idx += dir
	if idx < 0 {
		idx = 0
	}
	if idx > len(vis)-1 {
		idx = len(vis) - 1
	}
	m.channelIdx = idx
	ch := vis[idx]
	if ch.Id == m.openChannelID {
		return m, nil
	}
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
// the synthetic DM/Feed/Search tabs (",1".."9"). No-op when there
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
// while focused. The channel sidebar is no longer a Tab stop — teams and
// channels move with ctrl-nav from any focus — so focusChannels is always
// skipped. focusThread is skipped when the sidebar is closed; on the Search/
// Feed tabs focus is constrained to {Teams, Search/Feed}.
func (m Model) cycleFocus(step int) (tea.Model, tea.Cmd) {
	onSearch := m.onSearchTab()
	onFeed := m.onFeedTab()
	for i := 0; i < numFocus; i++ {
		m.focus = focus((int(m.focus) + step + numFocus) % numFocus)
		// The channel sidebar can't be focused anymore; it's driven by ctrl-nav.
		if m.focus == focusChannels {
			continue
		}
		if m.focus == focusThread && !m.threadOpen {
			continue
		}
		if m.focus == focusRef && !m.refOpen {
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
		// The team strip is its own Tab stop only on the Search/Feed tabs,
		// whose body panes can't host the ←/→ tab switch themselves.
		if m.focus == focusTeams && !onSearch && !onFeed {
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
	m.renderRef()
	return m, tea.Batch(cmd, buildCmd)
}

func (m Model) handleInputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// When the @-mention popup is open with at least one candidate, it
	// owns navigation/accept/dismiss keys before the normal input flow.
	if m.mention.active && len(m.mention.items) > 0 {
		switch {
		case key.Matches(msg, m.keys.InputUp):
			// The popup owns ctrl+p/ctrl+n (input_up/down) while open — the
			// popupOpen guard in handleKey keeps the global switcher off ctrl+p.
			if m.mention.idx > 0 {
				m.mention.idx--
			}
			return m, nil
		case key.Matches(msg, m.keys.InputDown):
			if m.mention.idx < len(m.mention.items)-1 {
				m.mention.idx++
			}
			return m, nil
		case key.Matches(msg, m.keys.Tab), key.Matches(msg, m.keys.Send):
			if m.acceptMention() {
				return m, nil
			}
		case msg.String() == "esc": // hardwired popup dismiss
			m.closeMention()
			return m, nil
		}
	}

	// The `:`-emoji picker owns the same navigation/accept/dismiss keys when
	// it's open, mirroring the @-mention popup above.
	if m.emoji.active && len(m.emoji.items) > 0 {
		switch {
		case key.Matches(msg, m.keys.InputUp):
			if m.emoji.idx > 0 {
				m.emoji.idx--
			}
			return m, nil
		case key.Matches(msg, m.keys.InputDown):
			if m.emoji.idx < len(m.emoji.items)-1 {
				m.emoji.idx++
			}
			return m, nil
		case key.Matches(msg, m.keys.Tab), key.Matches(msg, m.keys.Send):
			if m.acceptEmoji() {
				return m, nil
			}
		case msg.String() == "esc": // hardwired popup dismiss
			m.closeEmoji()
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
	case msg.String() == "up" && m.editingPostID == "" &&
		m.input.Line() == 0 && m.input.LineInfo().RowOffset == 0:
		// ↑ on the first visual row of the composer selects the absolute last
		// message — the inverse of ↓-on-the-last-message dropping into the
		// composer. The @-mention / :emoji popups consume ↑ above while open,
		// so reaching here means no autocomplete tooltip is showing. On a
		// multi-row draft ↑ still moves the cursor within the text (the case
		// falls through to the textarea below); only the top row escapes to
		// the transcript. Skipped while editing a post so an in-progress edit
		// isn't abandoned by a stray ↑.
		if m.threadOpen {
			if len(m.threadPosts) == 0 {
				break // nothing to select; let ↑ fall through to the textarea
			}
			m.input.Blur()
			m.focus = focusThread
			m.threadIdx = len(m.threadPosts) - 1
			m.renderMessages()
			m.renderThread()
			return m, nil
		}
		if len(m.posts) == 0 {
			break
		}
		m.input.Blur()
		m.focus = focusMessages
		m.selectLastMessage()
		m.renderMessages()
		return m, nil
	case key.Matches(msg, m.keys.ClearInput):
		// Wipe the whole draft in one keystroke. The textarea's emacs keys
		// only kill to the line start / end; this is the "start over" hatch
		// that also lets esc leave (esc is disabled while text is present,
		// see LeaveInput below). No-op on an empty input so a stray ctrl+g
		// doesn't flash a misleading status.
		if m.input.Value() == "" {
			return m, nil
		}
		m.input.Reset()
		m.syncInputHeight()
		m.closeMention()
		m.closeEmoji()
		m.status = "draft cleared"
		return m, nil
	case key.Matches(msg, m.keys.LeaveInput):
		// While composing a new message, esc only leaves the input when
		// the textarea is empty — a stray esc shouldn't yank focus away
		// from a half-typed draft. Edit mode is exempt: there esc cancels
		// the in-progress edit (dropping the prefilled text) and leaves,
		// which is the more useful escape hatch even with text present.
		editing := m.editingPostID != ""
		if !editing && strings.TrimSpace(m.input.Value()) != "" {
			// A stray esc shouldn't yank focus away from a half-typed draft
			// (and turn the next keystrokes into nav commands — q quits);
			// point at the clear shortcut instead of silently swallowing it.
			clearKey := "ctrl+g"
			if ks := m.keys.ClearInput.Keys(); len(ks) > 0 {
				clearKey = prettyKey(ks[0])
			}
			m.status = "esc disabled while composing — " + clearKey + " to clear, then esc to leave"
			return m, nil
		}
		m.closeMention()
		m.closeEmoji()
		if editing {
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
			m.closeEmoji()
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
			// Target the open channel (what the pane shows), not the
			// sidebar cursor — those diverge once you navigate the list
			// without opening a new channel.
			channelID = m.openChannelID
			if channelID == "" {
				return m, nil
			}
		}
		fileIDs := m.collectAttachmentFileIDs()
		m.input.Reset()
		m.syncInputHeight()
		m.closeMention()
		m.closeEmoji()
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
	m.updateEmoji()
	m.syncInputHeight()
	return m, tea.Batch(cmd, mentionCmd)
}

// handleFilterKey owns keystrokes while the channel filter is open (f). The
// filter is a one-shot channel finder: type to narrow, ↑/↓ to move the
// highlight, enter to open the selection (and drop the filter), esc to
// cancel. The sidebar is no longer a focus, so opening lands in the messages
// pane.
func (m Model) handleFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.CancelEdit):
		m.filterMode = false
		m.filterValue = ""
		m.filter.SetValue("")
		m.filter.Blur()
		m.channelIdx = 0
		m.chanOff = 0
		m.focus = focusMessages
		return m, nil
	case key.Matches(msg, m.keys.ApplyOpen):
		m.filterMode = false
		m.filter.Blur()
		m.focus = focusMessages
		m.input.Blur()
		vis := m.visibleChannels()
		if len(vis) == 0 || m.channelIdx >= len(vis) {
			m.filterValue = ""
			m.filter.SetValue("")
			return m, nil
		}
		ch := vis[m.channelIdx]
		// One-shot: drop the filter so the sidebar shows the full list again,
		// then re-point the selection at the channel we just opened (its index
		// in the filtered list no longer matches the unfiltered one).
		m.filterValue = ""
		m.filter.SetValue("")
		m.chanOff = 0
		for i, c := range m.visibleChannels() {
			if c.Id == ch.Id {
				m.channelIdx = i
				break
			}
		}
		return m, tea.Batch(m.openChannelLoadCmd(ch.Id), m.bumpChannelStat(ch.Id))
	case key.Matches(msg, m.keys.InputUp), key.Matches(msg, m.keys.InputDown):
		// Arrow through the filtered list while still typing (input_up/down:
		// ↑/ctrl+p, ↓/ctrl+n). We deliberately don't accept j/k here — the user
		// may be typing those into the filter. ctrl+p is the global switcher.
		vis := m.visibleChannels()
		if len(vis) > 0 {
			if m.channelIdx >= len(vis) {
				m.channelIdx = len(vis) - 1
			}
			if m.channelIdx < 0 {
				m.channelIdx = 0
			}
			if key.Matches(msg, m.keys.InputUp) && m.channelIdx > 0 {
				m.channelIdx--
			}
			if key.Matches(msg, m.keys.InputDown) && m.channelIdx < len(vis)-1 {
				m.channelIdx++
			}
		}
		return m, nil
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

// selectLastMessage moves the message selection to the channel's newest
// post. The loaded window may have slid up while scrolling back (older
// posts paged in, the newest trimmed off), so the loaded tail isn't
// necessarily the channel's newest message; if the store has posts newer
// than what's loaded, reload the most recent page first so the selection
// lands on the true newest rather than the bottom of the loaded slice. The
// caller renders. Shared by End/G and the ↑-in-composer jump, both of which
// target the absolute last message.
func (m *Model) selectLastMessage() {
	if len(m.posts) > 0 {
		last := m.posts[len(m.posts)-1]
		if newer := m.loadNewerFromStore(last.ChannelId, last.CreateAt); len(newer) > 0 {
			if latest := m.loadFromStore(last.ChannelId); len(latest) > 0 {
				m.posts = latest
			}
		}
	}
	m.postIdx = len(m.posts) - 1
	m.anchorMsgSelBottom = true
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
		settle := m.bumpMRFetch()
		if m.postIdx > 0 {
			m.postIdx--
			m.renderMessages()
			return m, settle
		}
		// At the top. Paint the next cached page immediately for instant
		// feedback, then ask the server for the page strictly older than the
		// current top and merge it (see the olderPostsMsg handler). The
		// server fetch is anchored on the OLD top — before the optimistic
		// prepend — so its page overlaps the cache page and reconciles it,
		// filling any interior hole the cache would silently skip and
		// continuing past the oldest cached post into history the cache
		// never held.
		if len(m.posts) == 0 {
			return m, settle
		}
		oldestID := m.posts[0].Id
		older := m.loadOlderFromStore(m.posts[0].ChannelId, m.posts[0].CreateAt)
		if len(older) > 0 {
			m.posts = append(older, m.posts...)
			m.postIdx = len(older) - 1
			m.status = ""
			m.anchorMsgSelTop = true
			m.trimPostWindowTail()
			m.renderMessages()
		} else {
			m.status = "loading older messages…"
		}
		if oldestID == "" {
			return m, settle
		}
		return m, tea.Batch(settle, m.fetchOlder(m.openChannelID, oldestID))
	case key.Matches(msg, m.keys.Down):
		settle := m.bumpMRFetch()
		if m.postIdx < len(m.posts)-1 {
			m.postIdx++
			m.renderMessages()
			return m, settle
		}
		// At the bottom of the loaded window.
		if len(m.posts) == 0 {
			return m, settle
		}
		last := m.posts[len(m.posts)-1]
		newer := m.loadNewerFromStore(last.ChannelId, last.CreateAt)
		if len(newer) == 0 {
			// On the channel's newest loaded post with nothing newer in the
			// cache — the absolute last message. ↓ here drops into the
			// composer (the inverse of ↑-on-the-first-composer-row selecting
			// it).
			nm, focusCmd := m.focusComposer()
			return nm, tea.Batch(settle, focusCmd)
		}
		// More cached history sits below the loaded window (e.g. reading
		// forward from a search hit centred on an old post): paint the next
		// cached page immediately, then fetch the page strictly newer than
		// the current tail from the server and merge it (see the
		// newerPostsMsg handler). Anchored on the OLD tail so it reconciles
		// the cache page and crosses any hole between the loaded tail and the
		// live tail.
		oldLen := len(m.posts)
		newestID := last.Id
		m.posts = append(m.posts, newer...)
		m.postIdx = oldLen
		m.status = ""
		m.trimPostWindowHead()
		m.anchorMsgSelBottom = true
		m.renderMessages()
		if newestID == "" {
			return m, settle
		}
		return m, tea.Batch(settle, m.fetchNewer(m.openChannelID, newestID))
	case key.Matches(msg, m.keys.Home):
		m.postIdx = 0
		m.renderMessages()
		return m, m.bumpMRFetch()
	case key.Matches(msg, m.keys.End):
		m.selectLastMessage()
		m.renderMessages()
		return m, m.bumpMRFetch()
	case key.Matches(msg, m.keys.PageDown):
		if len(m.posts) == 0 {
			return m, nil
		}
		m.postIdx += m.messagesPageStep()
		if m.postIdx > len(m.posts)-1 {
			m.postIdx = len(m.posts) - 1
		}
		m.renderMessages()
		return m, m.bumpMRFetch()
	case key.Matches(msg, m.keys.PageUp):
		if len(m.posts) == 0 {
			return m, nil
		}
		m.postIdx -= m.messagesPageStep()
		if m.postIdx < 0 {
			m.postIdx = 0
		}
		m.renderMessages()
		return m, m.bumpMRFetch()
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
		return m.openFromPost(m.posts[m.postIdx])
	case key.Matches(msg, m.keys.OpenRef):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			m.status = "no message selected"
			return m, nil
		}
		return m.openRefForPost(m.posts[m.postIdx])
	case key.Matches(msg, m.keys.Preview):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			m.status = "no message selected"
			return m, nil
		}
		return m.openImagePreview(m.posts[m.postIdx])
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
		return m, m.openReactionPicker(p.Id)
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
// is open. y confirms (fires the DeletePost call); n/esc/q cancels.
// enter is deliberately NOT a confirm — a delete is destructive, so it
// shouldn't ride on the same key that opens threads / sends messages.
func (m Model) handleDeleteConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c": // hardwired quit
		return m, tea.Quit
	case key.Matches(msg, m.keys.ConfirmYes):
		id := m.deleteConfirmPostID
		m.closeDeleteConfirm()
		// If we were editing the same post, drop the edit state — the
		// post is about to disappear from under us.
		if m.editingPostID == id {
			m.cancelEdit()
		}
		m.status = "deleting…"
		return m, m.deletePost(id)
	case key.Matches(msg, m.keys.ConfirmNo), msg.String() == "esc", msg.String() == "q":
		// esc / q are hardwired modal-cancel aliases alongside confirm_no (n).
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
	case key.Matches(msg, m.keys.Up), key.Matches(msg, m.keys.Down):
		// ↑/↓ drop into the current tab's body list (search hits / feed
		// bubbles), so the team strip and the list below it read as one
		// navigator. focusTeams only ever happens on the Search/Feed tabs —
		// channel tabs land directly in the messages pane.
		if m.onSearchTab() {
			m.focus = focusSearch
			cmd := m.search.input.Focus()
			next, c2 := m.handleSearchKey(msg)
			return next, tea.Batch(cmd, c2)
		}
		if m.onFeedTab() {
			m.focus = focusFeed
			return m.handleFeedKey(msg)
		}
		return m, nil
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
		m.focus = focusMessages
		m.chanOff = 0
		m.filterValue = ""
		m.filter.SetValue("")
		vis := m.visibleChannels()
		if len(vis) == 0 {
			m.channelIdx = 0
			m.posts = nil
			m.renderMessages()
			m.status = "no channels in this team"
			return m, nil
		}
		m.channelIdx = m.preferredChannelIdx(vis)
		ch := vis[m.channelIdx]
		return m, tea.Batch(m.openChannelLoadCmd(ch.Id), m.bumpChannelStat(ch.Id))
	}
	return m, nil
}
