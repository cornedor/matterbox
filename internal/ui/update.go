package ui

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
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
	if fetch := nm.fetchPendingInlineImages(); fetch != nil {
		cmd = tea.Batch(cmd, fetch)
	}
	// After the render: re-transmit any thumbnail that was drawn but is no longer in
	// terminal memory, and free whatever is now over a cap. Must follow the fetch, so
	// an image installed this event is on screen before the caps are enforced.
	if raw := nm.flushInlineTransmits(); raw != nil {
		cmd = tea.Batch(cmd, raw)
	}
	// Encode the animation frames of any GIF that has come on screen — they are left
	// out of the first build (see buildInlineThumb). Must follow flushInlineTransmits,
	// which is what refreshes "on screen".
	if frames := nm.buildVisibleThumbFrames(); frames != nil {
		cmd = tea.Batch(cmd, frames)
	}
	if fetch := nm.fetchPendingMRStatus(); fetch != nil {
		cmd = tea.Batch(cmd, fetch)
	}
	if anim := nm.maybeStartImageAnim(); anim != nil {
		cmd = tea.Batch(cmd, anim)
	}
	// Same shape for the text effects: refresh the viewport gate (so an effect
	// scrolled into view is painted on this very event) and arm the frame loop if
	// it isn't running. Free when nothing on screen carries effects.
	if fx := nm.maybeStartEffectsAnim(); fx != nil {
		cmd = tea.Batch(cmd, fx)
	}
	// Reconcile the composer's cursor with m.focus *after* the handler ran, so no
	// focus-changing path can leave the editor visibly focused (or dark) by
	// forgetting to blur/focus it. Every event funnels through here, so this is
	// the one place the invariant is guaranteed (see syncComposerFocus).
	nm.syncComposerFocus()
	return nm, cmd
}

// syncComposerFocus reconciles the composer's bubble-level focus with m.focus,
// the single source of truth for which pane is active. The editor draws its
// cursor from its own focus flag, so any path that moves focus off the composer
// without blurring it — `matterbox open`, a permalink jump, switching to the
// Feed/Search/SQL tab, the channel sidebar nav — would otherwise leave a stale
// cursor that makes the composer look focused while you're reading; the reverse
// leaves the composer dark when it should be accepting input. Enforcing
// input.Focused() == (focus == focusInput) on every Update means new
// focus-changing code can't desync the cursor. Focus()/Blur() are side-effect
// only (no blink cmd), so this needs no command, and the guards make it a no-op
// unless the state actually crossed the composer boundary (so Blur's
// selection-drop only fires on a real focus-out). The Search/SQL inputs are left
// out on purpose: the Search tab hands its cursor to an AI follow-up box
// independently of m.focus, so they own their own focus state.
func (m *Model) syncComposerFocus() {
	switch {
	case m.focus == focusInput && !m.input.Focused():
		m.input.Focus()
	case m.focus != focusInput && m.input.Focused():
		m.input.Blur()
	}
}

// preservesFrame reports whether msg leaves the rendered screen byte-identical,
// so View() can hand back the memoized frame (viewCache.view) instead of
// rebuilding it. Everything else invalidates.
//
//   - A wheel event only accumulates wheelPending; nothing moves until the flush
//     tick (a wheelFlushMsg, not a MouseWheelMsg, so that still invalidates).
//     This is what lets a trackpad flood reuse the cached frame.
//   - A GIF animation tick re-transmits the next frame *under the same image id*
//     (advanceImageAnim → tea.Raw). The placeholder cells on screen are unchanged
//     — same id, same rows×cols — and the terminal repaints the image itself. The
//     frame text is therefore identical, and rebuilding it was pure waste: it put
//     a full ~1.5ms re-render behind every tick, 12–20×/s for as long as any GIF
//     emoji or thumbnail was visible.
//   - Finished GIF frames (inlineThumbFramesMsg) only fill in an existing
//     thumbnail's animation frames, under the id and cell box the still on screen
//     already uses. Nothing about the *text* of the screen changes — that is the
//     whole premise of building them late (see buildInlineThumb).
func preservesFrame(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.MouseWheelMsg, imgAnimTickMsg, inlineThumbFramesMsg:
		return true
	}
	return false
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// User input that acts on the scroll position must see any coalesced wheel
	// delta applied first (see handleMouseWheel). Background msgs and further
	// wheel events deliberately don't flush — that would defeat the coalescing.
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg, tea.PasteMsg:
		m.applyPendingWheel()
	}
	// Invalidate the memoized screen (viewCache.view) by default; see
	// preservesFrame for the two messages that don't change it.
	if m.vcache != nil && !preservesFrame(msg) {
		m.vcache.viewValid = false
	}
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
		// A resize while the image preview or a game is open re-fits + re-transmits it.
		return m, tea.Batch(m.resizePreview(), m.resizeGorillas(), m.resizeKurve())

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		return m.handleMouseWheel(msg)

	case wheelFlushMsg:
		// One frame elapsed since the burst started; apply the accumulated delta
		// and disarm. New events re-arm a fresh tick, so a continuous gesture
		// moves once per frame and a stopped one settles within a frame.
		m.wheelTicking = false
		// A flick on the transcript that lands at an edge pages in more history
		// (the keyboard's ↑-at-first-post / ↓-at-last-post paths, here offset-
		// driven). Note the direction before applyPendingWheel zeroes the delta.
		onMsgs := m.wheelTarget == wheelMsgs
		up := onMsgs && m.wheelPending < 0
		down := onMsgs && m.wheelPending > 0
		m.applyPendingWheel()
		switch {
		case up:
			return m, m.paginateMsgsOnWheelTop()
		case down:
			return m, m.paginateMsgsOnWheelBottom()
		}
		return m, nil

	case tea.MouseClickMsg:
		return m.handleMouseClick(msg)

	case tea.MouseReleaseMsg:
		return m.handleMouseRelease(msg)

	case tea.MouseMotionMsg:
		return m.handleMouseMotion(msg)

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

	case openChannelRequestMsg:
		return m.openChannelExternal(msg.channelID)

	case followPermalinkMsg:
		return m.followPermalink(msg.postID, msg.url)

	case permalinkResolvedMsg:
		return m.handlePermalinkResolved(msg)

	case customEmojiListMsg:
		if msg.err == nil {
			m.customEmojiNames = msg.names
		}
		return m, nil

	case serverCommandsMsg:
		if msg.err == nil {
			m.serverCmds[msg.teamID] = msg.cmds
			// If the "/" popup is open for this team, fold the freshly-cached
			// commands in without waiting for the next keystroke.
			if m.slash.active {
				if ch, _ := m.composerTarget(); m.commandTeamID(ch) == msg.teamID {
					m.slash.items = m.slashMatches(m.slash.query, msg.teamID)
					if m.slash.idx >= len(m.slash.items) {
						m.slash.idx = 0
					}
				}
			}
		}
		return m, nil

	case emojiImagesFetchedMsg:
		return m.handleEmojiImagesFetched(msg)

	case inlineImagesFetchedMsg:
		return m.handleInlineImagesFetched(msg)

	case inlineThumbFramesMsg:
		return m.handleInlineThumbFrames(msg)

	case imgAnimTickMsg:
		return m, m.advanceImageAnim()

	case effectsAnimTickMsg:
		return m, m.applyEffectsTick()

	case typingIndicatorTickMsg:
		return m, m.applyTypingIndicatorTick()

	case previewImageLoadedMsg:
		return m.handlePreviewLoaded(msg)

	case previewReencodedMsg:
		return m.handlePreviewReencoded(msg)

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
		return m, tea.Batch(m.maybeFetchInitialPosts(), m.loadDrafts())

	case draftsLoadedMsg:
		return m, m.applyDraftsLoaded(msg)

	case draftSaveDebounceMsg:
		return m, m.applyDraftSaveDebounce(msg)

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

	case channelMembersAddedMsg:
		return m.applyMembersAdded(msg)

	case channelCreatedMsg:
		return m.applyChannelCreated(msg)

	case channelPatchedMsg:
		return m.applyChannelPatched(msg)

	case channelActionDoneMsg:
		return m.applyChannelActionDone(msg)

	case publicChannelsMsg:
		return m.applyPublicChannels(msg)

	case channelJoinedMsg:
		return m.applyChannelJoined(msg)

	case slashExecMsg:
		if msg.err != nil {
			m.status = "command failed: " + msg.err.Error()
			return m, nil
		}
		// An in-channel command (e.g. /me) posts server-side and arrives over
		// the WebSocket; just clear the "sending…" status. An ephemeral reply
		// (e.g. /away → "You are now away") is shown in the footer.
		if msg.resp != nil && msg.resp.ResponseType != model.CommandResponseTypeInChannel {
			if t := firstLine(msg.resp.Text); t != "" {
				return m, m.flashStatus(t)
			}
		}
		m.status = ""
		return m, nil

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
		m.setMembers(msg.members)
		m.membersLoaded = true
		m.applyUnreadFromMembers()
		return m, nil

	case postsLoadedMsg:
		if msg.channelID != m.openChannelID {
			// Stale to the UI, but still worth persisting so the cache
			// keeps growing for unfocused channels we briefly opened.
			return m, m.persistPosts(msg.posts...)
		}
		// A server fetch never returns deleted posts; carry over any tombstones
		// already on screen so a full reload (notably the post-send refetch)
		// doesn't make removed messages vanish. The cache keeps them too, so a
		// later reopen reloads them — this just covers the in-session replace.
		m.posts = mergeTombstones(m.posts, msg.posts)
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

	case deletionsSyncedMsg:
		// Tombstones are already persisted (syncChannelDeletions). If the synced
		// channel is still open, flip any matching live post in the transcript so
		// the "message deleted" marker shows without waiting for a reopen — and
		// mirror applyPostDeleted's handling of an open thread sidebar so the two
		// delete paths (live WS vs offline sweep) render the same.
		if msg.channelID != m.openChannelID {
			return m, nil
		}
		changed, threadChanged := false, false
		var threadCmd tea.Cmd
		for _, d := range msg.deleted {
			for _, ex := range m.posts {
				if ex.Id == d.Id && ex.DeleteAt == 0 {
					markPostDeleted(ex, d.DeleteAt)
					m.invalidatePostLines(ex.Id)
					m.feedRemovePost(ex.Id)
					changed = true
					break
				}
			}
			if m.threadOpen {
				// A removed thread root leaves nothing to anchor the sidebar on.
				if d.Id == m.threadRootID {
					threadCmd = m.closeThread()
					threadChanged = false // closeThread already tore the pane down
					continue
				}
				for _, ex := range m.threadPosts {
					if ex.Id == d.Id && ex.DeleteAt == 0 {
						markPostDeleted(ex, d.DeleteAt)
						m.invalidatePostLines(ex.Id)
						threadChanged = true
						break
					}
				}
			}
		}
		if changed {
			m.renderMessages()
		}
		if threadChanged {
			m.renderThread()
		}
		return m, threadCmd

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
			// Mirror the live WS new-post path: a reconcile that arrives while the
			// user is at the bottom (e.g. the fetchRecent every channel-open kicks
			// off) must keep them pinned there. Without this, renderMessages'
			// default keep-visible branch top-anchors a newest post taller than the
			// pane, knocking the just-opened channel off the bottom — so ↓ then
			// scrolls inside that post instead of dropping into the composer.
			// Skip it when a search/permalink jump is pending so that jump wins.
			if m.pendingJumpPostID == "" {
				m.anchorMsgSelBottom = true
			}
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
		m.loadingOlder = false // in-flight wheel fetch resolved; allow the next
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
		if m.msgScrollFree {
			// Wheel-driven scroll-up: the offset, not the selection, owns the
			// view. Pin the post at the viewport top in place so the merged
			// older history lands above it without the screen jumping.
			anchorID, within := m.msgFreeAnchor()
			m.posts = mergePostsByTime(m.posts, msg.posts)
			// Follow the top post with the selection so the tail can be trimmed.
			if idx := m.postIndexByID(anchorID); idx >= 0 {
				m.postIdx = idx
			}
			if m.postIdx > len(m.posts)-1 {
				m.postIdx = len(m.posts) - 1
			}
			if m.postIdx < 0 {
				m.postIdx = 0
			}
			m.trimPostWindowTail()
			m.status = ""
			m.renderMessages()
			if idx := m.postIndexByID(anchorID); idx >= 0 {
				m.msgFreeOffset = m.msgRowStarts[idx] + within
				m.msgsView.SetYOffset(m.msgFreeOffset)
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
		m.loadingNewer = false // in-flight wheel fetch resolved; allow the next
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
		if m.msgScrollFree {
			// Wheel-driven scroll-down: pin the post at the viewport top in place
			// so the merged newer history lands below it (and the head-trim above
			// it) without the screen jumping.
			anchorID, within := m.msgFreeAnchor()
			m.posts = mergePostsByTime(m.posts, msg.posts)
			if idx := m.postIndexByID(anchorID); idx >= 0 {
				m.postIdx = idx
			}
			if m.postIdx > len(m.posts)-1 {
				m.postIdx = len(m.posts) - 1
			}
			if m.postIdx < 0 {
				m.postIdx = 0
			}
			m.trimPostWindowHead()
			m.status = ""
			m.renderMessages()
			if idx := m.postIndexByID(anchorID); idx >= 0 {
				m.msgFreeOffset = m.msgRowStarts[idx] + within
				m.msgsView.SetYOffset(m.msgFreeOffset)
			}
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
		// The dwell elapsed. Ignore if the user switched away or refocused the
		// channel in the meantime (stale generation / different channel), or if
		// it's off screen behind a Feed/Search/SQL tab — isCurrentChannel covers
		// the latter so a dwell armed for a now-backgrounded conversation can't
		// complete.
		if msg.gen != m.viewGen || !m.isCurrentChannel(msg.channelID) {
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
		m.loadingOlder = false // a failed fetch must not wedge the wheel guards
		m.loadingNewer = false
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

	case infoMembersLoadedMsg:
		if !m.infoOpen || msg.channelID != m.infoChannelID {
			return m, nil // stale (closed or switched)
		}
		m.infoMembersLoaded = true
		m.infoMembersErr = msg.err
		m.infoMembers = msg.members
		m.renderInfo()
		return m, nil

	case infoPinnedLoadedMsg:
		if !m.infoOpen || msg.channelID != m.infoChannelID {
			return m, nil // stale (closed or switched)
		}
		m.infoPinnedLoaded = true
		m.infoPinnedErr = msg.err
		m.infoPinned = msg.posts
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		m.renderInfo()
		return m, nil

	case infoMediaLoadedMsg:
		if !m.infoOpen || msg.channelID != m.infoChannelID {
			return m, nil // stale (closed or switched)
		}
		m.infoMediaLoaded = true
		m.infoMediaErr = msg.err
		m.infoMedia = msg.files
		m.infoMediaTruncated = msg.truncated
		for id, name := range msg.users {
			m.userNames[id] = name
		}
		m.renderInfo()
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
			// Errors stay until the next interaction — worth reading, and rare.
			m.status = "open " + msg.name + ": " + msg.err.Error()
			return m, nil
		}
		return m, m.flashStatus("opened " + msg.name)

	case attachmentsDownloadedMsg:
		return m, m.applyDownloadResult(msg)

	case statusFlashClearMsg:
		if m.status == msg.text {
			m.status = ""
		}
		return m, nil

	case copyClipboardMsg:
		return m, m.flashStatus("copied " + msg.what + " to clipboard")

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
		// Float the people you mention most to the top of the server's
		// relevance-ordered results — a stable sort keeps the server order
		// for everyone you've never picked.
		sort.SliceStable(items, func(i, j int) bool {
			if items[i] == nil || items[j] == nil {
				return items[j] == nil && items[i] != nil
			}
			return m.mentionUsage[items[i].Username] > m.mentionUsage[items[j].Username]
		})
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
		m.history.checkpoint(m.composerContextKey(), val)
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

	case gorillasPostedMsg:
		return m, m.applyGorillasPosted(msg)
	case gorillasJoinedMsg:
		return m, m.applyGorillasJoined(msg)
	case gorillasTickMsg:
		return m, m.applyGorillasTick(msg)
	case gorillasHeartbeatMsg:
		return m, m.applyGorillasHeartbeat(msg)
	case gorillasResumedMsg:
		return m, m.applyGorillasResumed(msg)
	case gorillasFrameMsg:
		return m, m.applyGorillasFrame(msg)

	case kurvePostedMsg:
		return m, m.applyKurvePosted(msg)
	case kurveJoinedMsg:
		return m, m.applyKurveJoined(msg)
	case kurveTickMsg:
		return m, m.applyKurveTick(msg)
	case kurveResumedMsg:
		return m, m.applyKurveResumed(msg)
	case kurveFrameMsg:
		return m, m.applyKurveFrame(msg)
	case ballStartedMsg:
		return m, m.applyBallStarted(msg)

	case ballTickMsg:
		return m, m.applyBallTick(msg)

	case cmdShimmerTickMsg:
		return m, m.applyCmdShimmerTick()

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

	case grammarDebounceMsg:
		return m, m.applyGrammarDebounce(msg)

	case grammarResultMsg:
		return m, m.applyGrammarResult(msg)

	case searchResultsMsg:
		return m.applySearchResults(msg)

	case sqlResultsMsg:
		return m.applySQLResults(msg)

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
	case model.WebsocketEventTyping:
		return m.applyTypingEvent(ev)
	case model.WebsocketEventMultipleChannelsViewed:
		return m.applyMultipleChannelsViewed(ev)
	case model.WebsocketEventDraftCreated, model.WebsocketEventDraftUpdated:
		m.applyDraftUpserted(ev)
		return nil
	case model.WebsocketEventDraftDeleted:
		m.applyDraftDeleted(ev)
		return nil
	}
	return nil
}

// applyMultipleChannelsViewed reconciles the live unread/mention badges with
// read state advanced by another session — a second matterbox instance, the
// web client, or mobile. The server broadcasts this to all of a user's
// sessions whenever any of them views a channel. Without it the count-based
// m.unread badge (bumped here on every posted event) stays stale while the
// authoritative feed — which re-fetches LastViewedAt in fetchFeed — shows
// nothing: the "badge says 1, feed empty" drift.
//
// We only clear a channel whose view caught up to its newest known post. A
// strictly-newer local LastPostAt means a message landed that the viewing
// session hadn't seen, so genuine unread remains and the badge stays.
func (m *Model) applyMultipleChannelsViewed(ev *model.WebSocketEvent) tea.Cmd {
	times := wsChannelTimes(ev)
	if len(times) == 0 {
		return nil
	}
	feedChanged := false
	for id, viewedAt := range times {
		if m.unread[id] == 0 && m.mentions[id] == 0 {
			continue // nothing to clear (commonly our own view echoed back)
		}
		if c := m.findChannel(id); c != nil && c.LastPostAt > viewedAt {
			continue // a post newer than the view exists locally — keep it unread
		}
		delete(m.unread, id)
		delete(m.mentions, id)
		if m.feed.built {
			m.removeFeedEntry(id)
			feedChanged = true
		}
	}
	if feedChanged {
		m.renderFeedResults()
	}
	return nil
}

// applyPosted appends a new post locally. If for a non-focused channel,
// just bumps the unread (and mention, if we're tagged) counter.
func (m *Model) applyPosted(ev *model.WebSocketEvent) tea.Cmd {
	p := parsePost(ev)
	if p != nil {
		if cmd := m.gorillasWSPosted(p); cmd != nil {
			return cmd
		}
		if cmd := m.kurveWSPosted(p); cmd != nil {
			return cmd
		}
	}
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
		// it's a background channel update. Thread replies count too:
		// matterbox renders them inline, and the unread seed
		// (applyUnreadFromMembers) combines the all-posts counters, so
		// counting replies here keeps the live badge consistent with it.
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
	if cmd := m.gorillasWSEdited(p); cmd != nil {
		return cmd
	}
	if cmd := m.kurveWSEdited(p); cmd != nil {
		return cmd
	}
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
	// Drop the content from the local cache so deleted text doesn't linger on
	// disk, but leave a tombstone in the live transcript so the message doesn't
	// silently vanish from under the reader (see deletedPostLines).
	persistCmd := m.persistDelete(p)
	if m.isCurrentChannel(p.ChannelId) {
		for _, ex := range m.posts {
			if ex.Id == p.Id {
				markPostDeleted(ex, p.DeleteAt)
				m.renderMessages()
				break
			}
		}
	}
	if m.isThreadPost(p) {
		// If the root itself was deleted, drop the whole sidebar — there's
		// nothing left to anchor it on.
		if p.Id == m.threadRootID {
			return tea.Batch(persistCmd, m.closeThread())
		}
		for _, ex := range m.threadPosts {
			if ex.Id == p.Id {
				markPostDeleted(ex, p.DeleteAt)
				m.renderThread()
				break
			}
		}
	}
	return persistCmd
}

// markPostDeleted flags p as removed so the render path shows a tombstone in
// place of its content. The post_deleted event's own DeleteAt isn't reliably
// populated, so we fall back to any nonzero stamp — only the nonzero-ness
// matters to the renderer and to postLineFingerprint. We also clear the
// content from memory: a tombstone never renders the original message, and we
// don't want the deleted text sitting around in the model.
func markPostDeleted(p *model.Post, at int64) {
	if at == 0 {
		at = p.UpdateAt
	}
	if at == 0 {
		at = p.CreateAt
	}
	if at == 0 {
		at = 1
	}
	p.DeleteAt = at
	// Strip the content the tombstone must never expose (message text, file
	// metadata, …). Shared with the persisted-tombstone path so the two can't
	// drift; Props is deliberately kept for the webhook override_username.
	store.StripTombstoneContent(p)
}

// isCurrentChannel reports whether channelID is the channel the messages
// pane is showing (the open channel), not merely the sidebar selection.
// Navigation moves the cursor without opening, so optimistic append and
// live post events must track m.openChannelID to keep flowing into the
// visible transcript.
//
// The Feed/Search/SQL tabs replace the messages pane with their own, so the
// open channel is off screen even though m.openChannelID still points at it.
// Treat it as not-current there: a live post for it must fall through to the
// background path (bump the unread badge, surface in the feed) rather than be
// appended to a hidden transcript and silently marked read. Reopening the
// channel repaints it from the cache.
func (m *Model) isCurrentChannel(channelID string) bool {
	if channelID == "" || m.openChannelID != channelID {
		return false
	}
	return !m.onFeedTab() && !m.onSearchTab() && !m.onSQLTab()
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

// wsChannelTimes parses the channel_times payload of a
// multiple_channels_viewed event into channelID → last-viewed timestamp
// (ms). The server sends it as a nested JSON object, so after the event
// round-trips through JSON the numbers arrive as float64; a JSON-string
// encoding is tolerated as a fallback. A failed parse yields an empty map,
// which the caller treats as "clear nothing".
func wsChannelTimes(ev *model.WebSocketEvent) map[string]int64 {
	out := map[string]int64{}
	switch v := ev.GetData()["channel_times"].(type) {
	case map[string]any:
		for id, t := range v {
			out[id] = wsInt64(t)
		}
	case string:
		if v != "" {
			_ = json.Unmarshal([]byte(v), &out)
		}
	}
	return out
}

// wsInt64 coerces a JSON-decoded number (float64, or json.Number under a
// UseNumber decoder) to int64. Non-numeric values yield 0.
func wsInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
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
// synthetic DM tab (when present), the always-present Feed + Search tabs,
// and the optional SQL tab (config sql_tab).
func (m *Model) maxTeamIdx() int {
	n := len(m.teams)
	n++ // Feed is always present
	n++ // Search is always present
	if m.showSQL {
		n++ // SQL is optional (config sql_tab)
	}
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
		*m.switcher, cmd = m.switcher.Update(msg)
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
	// A file dragged onto the terminal arrives here: the emulator writes the
	// dropped path into the pty as a bracketed paste. When the whole paste is
	// existing files, attach them instead of typing their paths out.
	if m.focus == focusInput || m.focus == focusMessages || m.focus == focusAttachments {
		if cmd, ok := m.attachDroppedFiles(msg.Content); ok {
			return m, cmd
		}
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
		} else if !m.input.InCodeBlock() {
			// A pasted box-drawing / ASCII table becomes a Markdown pipe table,
			// unless the caret sits inside a code block (keep the drawing as-is).
			if md, ok := convertPastedBoxTables(msg.Content); ok {
				msg.Content = md
				m.status = "converted pasted table to Markdown"
			}
		}
		before := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		// A paste lands as a single, discrete undo step.
		if m.input.Value() != before {
			m.history.checkpoint(m.composerContextKey(), before)
		}
		mentionCmd := m.updateMention()
		m.updateEmoji()
		slashCmd := m.updateSlash()
		m.updateLang()
		m.updateEffectPopup()
		m.syncComposerDecorations()
		cmdHlCmd := m.updateCommandHighlight()
		m.syncInputHeight()
		return m, tea.Batch(cmd, mentionCmd, slashCmd, cmdHlCmd, giphyCmd)
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
	if m.focus == focusSQL {
		old := m.sql.input.Value()
		var cmd tea.Cmd
		m.sql.input, cmd = m.sql.input.Update(msg)
		if m.sql.input.Value() != old {
			m.layoutPanes()
		}
		return m, cmd
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// A keypress ends mouse free-scroll: re-anchor the selection to the post the
	// wheel left on screen (so the key acts on a visible message and the view
	// doesn't jump back to the pre-scroll selection), then resume normal
	// selection-following on the render this key triggers.
	if m.msgScrollFree {
		m.syncMsgSelToViewport()
		m.msgScrollFree = false
	}
	if m.threadScrollFree {
		m.syncThreadSelToViewport()
		m.threadScrollFree = false
	}
	// A keypress also dismisses a mouse text selection so its highlight doesn't
	// linger over content the keyboard is now acting on.
	if m.textSel.active || m.textSel.dragging {
		m.clearTextSel()
	}
	// Key inspector is fully modal and checked first: it echoes every decoded
	// keystroke instead of acting on it (esc closes, ctrl+c still quits), so a
	// user can see exactly what the terminal sends for e.g. option+arrow.
	if m.keyDebugMode {
		return m.handleKeyDebugKey(msg)
	}
	// An open game is fully modal: it is a game, and every key belongs to it.
	if m.gorillas.active {
		return m.handleGorillasKey(msg)
	}
	if m.kurve.active {
		return m.handleKurveKey(msg)
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
	// Jira comment composer (add / reply) owns every keystroke while open.
	if m.jiraCommentActive {
		return m.handleJiraCommentKey(msg)
	}
	// GitLab approve/merge confirm owns every keystroke while open.
	if m.glConfirm.active {
		return m.handleGitLabConfirmKey(msg)
	}
	// Link warning (clicked a non-web link) owns every keystroke while open.
	if m.linkConfirm.active {
		return m.handleLinkConfirmKey(msg)
	}
	// Open-target picker modal owns every keystroke while open.
	if m.openPickerActive() {
		return m.handleOpenPickerKey(msg)
	}
	// Code-block picker modal owns every keystroke while open.
	if m.codePickerActive() {
		return m.handleCodePickerKey(msg)
	}
	// Poll-dialog modal (e.g. matterpoll "Add Option") owns every
	// keystroke while open.
	if m.pollDialog.open {
		return m.handlePollDialogKey(msg)
	}
	// Create-channel form owns every keystroke while open. Opened from the
	// switcher's "> Create channel" command, which closes itself first.
	if m.createChan != nil {
		return m.handleCreateChannelKey(msg)
	}
	// The other channel modals, likewise raised from a > command that closed
	// the switcher first: the edit form (rename / purpose / header), the
	// archive/leave/privacy confirm, and the join-a-channel catalogue.
	if m.chanEdit != nil {
		return m.handleEditChannelKey(msg)
	}
	if m.chanConfirm != nil {
		return m.handleChannelConfirmKey(msg)
	}
	if m.joinChan != nil {
		return m.handleJoinChannelKey(msg)
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
	popupOpen := m.focus == focusInput && (m.mention.active || m.emoji.active || m.slash.active || m.lang.active || m.effectPopup.active)
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
	// The SQL editor likewise owns every keystroke while focused so its
	// multi-line typing isn't intercepted by the nav shortcuts below.
	if m.focus == focusSQL {
		return m.handleSQLKey(msg)
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
		// the Search/Feed/SQL tabs have no channel list to filter.
		if !m.onSearchTab() && !m.onFeedTab() && !m.onSQLTab() {
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
			return m, m.closeThread()
		}
		if m.refOpen {
			m.closeRef()
			return m, nil
		}
		if m.infoOpen {
			// Inside the media listing, esc backs out one level rather than
			// closing the panel outright.
			if m.infoMode == infoModeMedia {
				m.closeInfoMedia()
				return m, nil
			}
			m.closeInfo()
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
	case focusInfo:
		return m.handleInfoKey(msg)
	case focusAttachments:
		return m.handleAttachmentsKey(msg)
	case focusTeams:
		return m.handleTeamsKey(msg)
	case focusSearch:
		return m.handleSearchKey(msg)
	case focusFeed:
		return m.handleFeedKey(msg)
	case focusSQL:
		return m.handleSQLKey(msg)
	case focusSQLResults:
		return m.handleSQLResultsKey(msg)
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
		return m, m.closeThread()
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
	case key.Matches(msg, m.keys.Download):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			m.status = "no message selected"
			return m, nil
		}
		return m.downloadFromPost(m.threadPosts[m.threadIdx])
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
	case key.Matches(msg, m.keys.CopyCode):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			m.status = "no message selected"
			return m, nil
		}
		return m.copyCodeFromPost(m.threadPosts[m.threadIdx])
	case key.Matches(msg, m.keys.ShowHistory):
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			return m, nil
		}
		p := m.threadPosts[m.threadIdx]
		if p.DeleteAt != 0 {
			m.status = "message was deleted"
			return m, nil
		}
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
		if p.DeleteAt != 0 {
			m.status = "message was deleted"
			return m, nil
		}
		return m, m.openReactionPicker(p.Id)
	case key.Matches(msg, m.keys.Collapse):
		return m.toggleCollapse(focusThread)
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
	if m.onSQLTab() {
		// No composer on the SQL tab, but i is a natural "give me the editor"
		// key when the cursor is down in the results.
		if m.focus == focusSQLResults {
			m.focus = focusSQL
			m.renderSQLResults()
			return m, m.sql.input.Focus()
		}
		return m, nil
	}
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
	if m.onSQLTab() {
		m.focus = focusSQL
		return m, m.sql.input.Focus()
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
	onSQL := m.onSQLTab()
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
		if m.focus == focusInfo && !m.infoOpen {
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
		if (m.focus == focusSQL || m.focus == focusSQLResults) && !onSQL {
			continue
		}
		// The SQL results focus only exists while there are rows to act on.
		if m.focus == focusSQLResults && len(m.sql.posts) == 0 {
			continue
		}
		// The team strip is its own Tab stop only on the Search/Feed/SQL tabs,
		// whose body panes can't host the ←/→ tab switch themselves.
		if m.focus == focusTeams && !onSearch && !onFeed && !onSQL {
			continue
		}
		if onSearch && m.focus != focusTeams && m.focus != focusSearch {
			continue
		}
		if onFeed && m.focus != focusTeams && m.focus != focusFeed {
			continue
		}
		if onSQL && m.focus != focusTeams && m.focus != focusSQL && m.focus != focusSQLResults {
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
	if m.focus == focusSQL {
		cmd = tea.Batch(cmd, m.sql.input.Focus())
	} else {
		m.sql.input.Blur()
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
	m.renderInfo()
	if onSQL {
		m.renderSQLResults() // toggle the result-list selection bar with focus
	}
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
			if cmd, ok := m.acceptMention(); ok {
				return m, cmd
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
			if cmd, ok := m.acceptEmoji(); ok {
				return m, cmd
			}
		case msg.String() == "esc": // hardwired popup dismiss
			m.closeEmoji()
			return m, nil
		}
	}

	// The "/" command picker owns the same navigation/accept/dismiss keys when
	// it's open, mirroring the @-mention / :emoji popups above. (It can't be
	// active at the same time as those two — it only fires on a "/" at line
	// start with no whitespace before the cursor, where neither '@' nor ':'
	// can have opened a picker.)
	if m.slash.active && len(m.slash.items) > 0 {
		switch {
		case key.Matches(msg, m.keys.InputUp):
			if m.slash.idx > 0 {
				m.slash.idx--
			}
			return m, nil
		case key.Matches(msg, m.keys.InputDown):
			if m.slash.idx < len(m.slash.items)-1 {
				m.slash.idx++
			}
			return m, nil
		case key.Matches(msg, m.keys.Tab), key.Matches(msg, m.keys.Send):
			if cmd, ok := m.acceptSlash(); ok {
				return m, cmd
			}
		case msg.String() == "esc": // hardwired popup dismiss
			m.closeSlash()
			return m, nil
		}
	}

	// The "\" effect picker owns the same navigation/accept/dismiss keys when it's
	// open, mirroring the popups above. It only fires on a backslash followed by
	// letters, where none of the other triggers can have opened one.
	if m.effectPopup.active && len(m.effectPopup.items) > 0 {
		switch {
		case key.Matches(msg, m.keys.InputUp):
			if m.effectPopup.idx > 0 {
				m.effectPopup.idx--
			}
			return m, nil
		case key.Matches(msg, m.keys.InputDown):
			if m.effectPopup.idx < len(m.effectPopup.items)-1 {
				m.effectPopup.idx++
			}
			return m, nil
		case key.Matches(msg, m.keys.Tab), key.Matches(msg, m.keys.Send):
			if m.acceptEffectPopup() {
				return m, nil
			}
		case msg.String() == "esc": // hardwired popup dismiss
			m.closeEffectPopup()
			return m, nil
		}
	}

	// The ```-fence language picker owns the same navigation/accept/dismiss keys
	// when it's open, mirroring the popups above. It only opens on an opening
	// code fence's info string, where '@', ':' or a leading '/' can't have
	// triggered, so it's mutually exclusive with the three above.
	if m.lang.active && len(m.lang.items) > 0 {
		switch {
		case key.Matches(msg, m.keys.InputUp):
			if m.lang.idx > 0 {
				m.lang.idx--
			}
			return m, nil
		case key.Matches(msg, m.keys.InputDown):
			if m.lang.idx < len(m.lang.items)-1 {
				m.lang.idx++
			}
			return m, nil
		case key.Matches(msg, m.keys.Tab), key.Matches(msg, m.keys.Send):
			if cmd, ok := m.acceptLang(); ok {
				return m, cmd
			}
		case msg.String() == "esc": // hardwired popup dismiss
			m.closeLang()
			return m, nil
		}
	}

	// Grammar/spell suggestions: alt+g opens the popup on the mistake at the
	// cursor (or cycles to the next while open). When the popup is up it owns
	// the digit accelerators, tab navigation and esc; any other key dismisses
	// it and is handled normally below. (Keys hardwired, like the popups above,
	// rather than going through the configurable keymap.) Suppressed while an
	// @-mention / :emoji / ```-language popup owns the slot so they never fight
	// over it.
	if m.grammarEnabled() && !m.mention.active && !m.emoji.active && !m.slash.active && !m.lang.active && !m.effectPopup.active {
		if msg.String() == "alt+g" && len(m.grammar.matches) > 0 {
			m.openOrCycleGrammarPopup()
			return m, nil
		}
		if m.grammar.popup {
			s := msg.String()
			switch {
			case s == "esc":
				m.closeGrammarPopup()
				return m, nil
			case key.Matches(msg, m.keys.Tab):
				m.cycleGrammarPopup(1)
				return m, nil
			case key.Matches(msg, m.keys.ShiftTab):
				m.cycleGrammarPopup(-1)
				return m, nil
			case len(s) == 1 && s[0] >= '1' && s[0] <= '9':
				return m, m.applyGrammarSuggestion(int(s[0] - '1'))
			default:
				m.closeGrammarPopup()
			}
		} else if key.Matches(msg, m.keys.Tab) && !m.input.InTableRow() {
			// With no popup open, Tab applies the top suggestion for the mistake
			// the cursor is on (the one the footer hint is showing). When the
			// cursor isn't on a fixable mistake it falls through to focus-cycle.
			// Inside a table Tab means "next cell", so it never fixes a typo there.
			if idx := m.matchAtCursor(); idx >= 0 && len(m.grammar.matches[idx].Replacements) > 0 {
				m.grammar.popupIdx = idx
				return m, m.applyGrammarSuggestion(0)
			}
		}
	}

	switch {
	case msg.String() == "ctrl+c":
		return m, tea.Quit
	case key.Matches(msg, m.keys.Paste):
		return m, readClipboard()
	// Inside a pipe table tab steps from cell to cell instead of cycling focus:
	// the key falls through to the textarea below, whose own binding moves the
	// caret (and opens a new row off the last cell), so the edit picks up the
	// undo/draft/typing hooks there like any other.
	case key.Matches(msg, m.keys.Tab) && !m.input.InTableRow():
		return m.cycleFocus(1)
	case key.Matches(msg, m.keys.ShiftTab) && !m.input.InTableRow():
		return m.cycleFocus(-1)
	case msg.String() == "up" && m.editingPostID == "" &&
		m.input.CursorVisualRow() == 0:
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
	case key.Matches(msg, m.keys.Undo):
		// Silent no-op at the stack boundary — a sticky "nothing to undo"
		// status would linger until the next action replaced it.
		if v, ok := m.history.undo(m.composerContextKey(), m.input.Value()); ok {
			return m, m.applyComposerSnapshot(v)
		}
		return m, nil
	case key.Matches(msg, m.keys.Redo):
		if v, ok := m.history.redo(m.composerContextKey(), m.input.Value()); ok {
			return m, m.applyComposerSnapshot(v)
		}
		return m, nil
	case key.Matches(msg, m.keys.ClearInput):
		// Wipe the whole draft in one keystroke. The textarea's emacs keys
		// only kill to the line start / end; this is the "start over" hatch.
		// No-op on an empty input so a stray ctrl+g doesn't flash a
		// misleading status.
		if m.input.Value() == "" {
			return m, nil
		}
		m.input.Reset()
		m.history.reset()
		m.syncInputHeight()
		m.closeMention()
		m.closeEmoji()
		m.closeSlash()
		m.closeLang()
		m.closeEffectPopup()
		m.clearGrammar()
		m.status = "draft cleared"
		// Wiping a draft also drops its server copy — for whichever target the
		// composer is on (channel or thread). An in-progress edit isn't a draft,
		// so leave the server alone then.
		var clearCmd tea.Cmd
		if channelID, rootID, tracks := m.composerDraftTarget(); tracks {
			if rootID == "" {
				clearCmd = m.clearDraft(channelID)
			} else {
				clearCmd = m.clearThreadDraft(channelID, rootID)
			}
		}
		return m, clearCmd
	case key.Matches(msg, m.keys.LeaveInput):
		// esc leaves the composer, keeping any half-typed text as a draft:
		// it stays in the input and autosaves to the server, so focus can
		// jump back to the reading pane without losing work. Edit mode
		// cancels the in-progress edit (dropping the prefilled text) on the
		// way out.
		editing := m.editingPostID != ""
		m.closeMention()
		m.closeEmoji()
		m.closeSlash()
		m.closeLang()
		m.closeEffectPopup()
		m.clearGrammar()
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
			m.history.reset()
			m.syncInputHeight()
			m.closeMention()
			m.closeEmoji()
			m.closeSlash()
			m.closeLang()
			m.closeEffectPopup()
			m.clearGrammar()
			m.restoreInputPrompt()
			m.status = "saving edit…"
			// Same compile as a fresh send: the composer holds markup (see
			// beginEditPost), the wire gets visible text + payload.
			return m, m.editPost(id, compileEffects(text))
		}
		// A leading "/" + letter is a slash command, not a message: handle it
		// (or forward it to the server) instead of posting the raw text.
		if name, args, ok := parseSlash(text); ok {
			return m.runSlashCommand(name, args)
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
		m.history.reset()
		m.syncInputHeight()
		m.closeMention()
		m.closeEmoji()
		m.closeSlash()
		m.closeLang()
		m.closeEffectPopup()
		m.clearGrammar()
		wire := compileEffects(text)
		m.appendOptimistic(channelID, rootID, wire, fileIDs)
		m.clearAttachments()
		m.resizeMessagesViewport()
		if !m.threadOpen {
			m.postIdx = len(m.posts) - 1
		}
		m.renderMessages()
		m.renderThread()
		m.status = "sending…"
		// A send consumes its draft; drop the saved copy locally and on the
		// server, for the channel or the thread depending on where it went.
		var draftCmd tea.Cmd
		if rootID == "" {
			draftCmd = m.clearDraft(channelID)
		} else {
			draftCmd = m.clearThreadDraft(channelID, rootID)
		}
		return m, tea.Batch(m.sendMessage(channelID, rootID, wire, fileIDs), draftCmd)
	}
	var cmd tea.Cmd
	before := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	// Announce typing only when the keystroke actually changed the draft,
	// so pure navigation (arrows, ctrl+a/e) doesn't ping the channel and an
	// empty composer never claims someone's typing. The send is throttled.
	// A real edit also (re)arms the debounced grammar check.
	var typingCmd, grammarCmd, draftCmd tea.Cmd
	if v := m.input.Value(); v != before {
		// Snapshot the pre-keystroke draft for undo. Single-character edits
		// coalesce into word-sized steps inside note.
		m.history.note(m.composerContextKey(), before, v)
		if v != "" {
			typingCmd = m.maybeSendTyping(time.Now())
		}
		grammarCmd = m.scheduleGrammarCheck()
		// Persist the channel draft a beat after typing stops (no-op while
		// editing or replying in a thread).
		draftCmd = m.scheduleDraftSave()
	}
	// After the textarea has consumed the keystroke, recompute mention
	// state and reflow the input/messages split so newlines from
	// shift+enter (or alt+enter / ctrl+j) make the input grow.
	mentionCmd := m.updateMention()
	m.updateEmoji()
	slashCmd := m.updateSlash()
	m.updateLang()
	m.updateEffectPopup()
	m.syncComposerDecorations()
	cmdHlCmd := m.updateCommandHighlight()
	m.syncInputHeight()
	return m, tea.Batch(cmd, mentionCmd, slashCmd, cmdHlCmd, typingCmd, grammarCmd, draftCmd)
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

// wheelTarget identifies which scrollable a wheel gesture is driving. It's
// captured per gesture so a coalesced flush applies to the right viewport.
type wheelTarget int

const (
	wheelNone wheelTarget = iota
	wheelMsgs
	wheelThread
	wheelRef
	wheelInfo
	wheelSearch
	wheelFeed
	wheelSQL
)

// wheelCoalesceDelay is how long accumulated wheel delta waits before being
// applied — one frame, matching the renderer's ~60fps flush. Long enough to
// collapse a trackpad's momentum flood into one viewport move per frame, short
// enough that the scroll still tracks the gesture.
const wheelCoalesceDelay = 16 * time.Millisecond

// wheelFlushMsg fires wheelCoalesceDelay after the first wheel event of a burst;
// its handler applies the delta accumulated since (see handleMouseWheel).
type wheelFlushMsg struct{}

func wheelFlushCmd() tea.Cmd {
	return tea.Tick(wheelCoalesceDelay, func(time.Time) tea.Msg { return wheelFlushMsg{} })
}

// wheelTargetForFocus resolves which scrollable the wheel drives right now. The
// message feed and open thread free-scroll (decoupled from the selection); the
// synthetic Search / Feed tabs scroll their bubble list even when focus rests on
// the tab strip. The composer (and anything else) ignores the wheel.
func (m *Model) wheelTargetForFocus() wheelTarget {
	switch m.focus {
	case focusMessages:
		return wheelMsgs
	case focusThread:
		if m.threadOpen {
			return wheelThread
		}
		return wheelNone
	case focusRef:
		if m.refOpen {
			return wheelRef
		}
		return wheelNone
	case focusInfo:
		if m.infoOpen {
			return wheelInfo
		}
		return wheelNone
	default:
		switch {
		case m.onSearchTab():
			return wheelSearch
		case m.onFeedTab():
			return wheelFeed
		case m.onSQLTab():
			return wheelSQL
		}
		return wheelNone
	}
}

// wheelStep is the lines-per-event the target viewport scrolls (its
// MouseWheelDelta), so accumulating ±wheelStep per event reproduces the
// viewport's own per-event movement.
func (m *Model) wheelStep(t wheelTarget) int {
	switch t {
	case wheelMsgs:
		return m.msgsView.MouseWheelDelta
	case wheelThread:
		return m.threadView.MouseWheelDelta
	case wheelRef:
		return m.refView.MouseWheelDelta
	case wheelInfo:
		return m.infoView.MouseWheelDelta
	case wheelSearch:
		return m.search.view.MouseWheelDelta
	case wheelFeed:
		return m.feed.view.MouseWheelDelta
	case wheelSQL:
		return m.sql.view.MouseWheelDelta
	default:
		return 0
	}
}

// handleMouseWheel coalesces wheel events instead of moving the viewport per
// event. A MacBook trackpad floods MouseWheelMsg (and keeps firing momentum
// after the fingers lift); applying each one — an O(content) viewport clamp plus
// a full re-render — lets the msg queue back up so the buffered events drain
// after the gesture ends (the "keeps scrolling" feel). Here we only accumulate
// the net delta (O(1)) and arm one frame tick; applyWheel moves the viewport
// once per frame. The sticky msgScrollFree / threadScrollFree flag is set
// immediately so a background re-render mid-burst keeps the wheel offset rather
// than snapping back to the selection. Horizontal wheels, and wheels on the
// composer, are ignored.
func (m Model) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	var dir int
	switch msg.Button {
	case tea.MouseWheelUp:
		dir = -1
	case tea.MouseWheelDown:
		dir = 1
	default:
		return m, nil // ignore horizontal wheel
	}
	tgt := m.wheelTargetForFocus()
	if tgt == wheelNone {
		return m, nil
	}
	// A focus/tab change mid-burst would retarget the pending delta; apply what's
	// queued for the old target first so it lands where the user aimed it.
	if m.wheelPending != 0 && tgt != m.wheelTarget {
		m.applyWheel(m.wheelTarget, m.wheelPending)
		m.wheelPending = 0
	}
	m.wheelTarget = tgt
	m.wheelPending += dir * m.wheelStep(tgt)
	// Enter free-scroll now (not at flush) so any re-render before the tick keeps
	// the offset; applyWheel updates the free offset once the move lands.
	switch tgt {
	case wheelMsgs:
		m.msgScrollFree = true
	case wheelThread:
		m.threadScrollFree = true
	case wheelInfo:
		m.infoScrollFree = true
	}
	if m.wheelTicking {
		return m, nil
	}
	m.wheelTicking = true
	return m, wheelFlushCmd()
}

// applyWheel moves a target viewport by delta lines (SetYOffset clamps to
// content), mirroring the per-event behaviour the wheel used to have inline. For
// the feed / thread it also pins the free-scroll offset and refreshes which
// animated emoji are on screen.
func (m *Model) applyWheel(t wheelTarget, delta int) {
	if delta == 0 {
		return
	}
	switch t {
	case wheelMsgs:
		m.msgsView.SetYOffset(m.msgsView.YOffset() + delta)
		m.msgFreeOffset = m.msgsView.YOffset()
		m.msgScrollFree = true
		m.refreshAnimVisibility()
	case wheelThread:
		if !m.threadOpen {
			return
		}
		m.threadView.SetYOffset(m.threadView.YOffset() + delta)
		m.threadFreeOffset = m.threadView.YOffset()
		m.threadScrollFree = true
		m.refreshAnimVisibility()
	case wheelRef:
		if !m.refOpen {
			return
		}
		m.refView.SetYOffset(m.refView.YOffset() + delta)
	case wheelInfo:
		if !m.infoOpen {
			return
		}
		m.infoView.SetYOffset(m.infoView.YOffset() + delta)
		m.infoFreeOffset = m.infoView.YOffset()
		m.infoScrollFree = true
	case wheelSearch:
		m.search.view.SetYOffset(m.search.view.YOffset() + delta)
	case wheelFeed:
		m.feed.view.SetYOffset(m.feed.view.YOffset() + delta)
	case wheelSQL:
		m.sql.view.SetYOffset(m.sql.view.YOffset() + delta)
	}
}

// applyPendingWheel flushes any coalesced wheel delta immediately. Called before
// handling user input that acts on the scroll position (a keypress, a click) so
// it sees the final offset rather than one up to a frame stale.
func (m *Model) applyPendingWheel() {
	if m.wheelPending == 0 {
		return
	}
	m.applyWheel(m.wheelTarget, m.wheelPending)
	m.wheelPending = 0
}

// syncMsgSelToViewport moves the message selection to the first post still
// visible at the top of the viewport. Called when a keypress ends mouse
// free-scroll so the key acts on an on-screen post and the view doesn't jump
// back to the pre-scroll selection.
func (m *Model) syncMsgSelToViewport() {
	off := m.msgFreeOffset
	for i := 0; i+1 < len(m.msgRowStarts); i++ {
		if m.msgRowStarts[i+1] > off {
			m.postIdx = i
			return
		}
	}
}

// syncThreadSelToViewport is the thread-pane mirror of syncMsgSelToViewport.
func (m *Model) syncThreadSelToViewport() {
	off := m.threadFreeOffset
	for i := 0; i+1 < len(m.threadRowStarts); i++ {
		if m.threadRowStarts[i+1] > off {
			m.threadIdx = i
			return
		}
	}
}

// paginateMsgsOnWheelTop loads older history when the mouse wheel reaches the
// top of the loaded window, the offset-driven counterpart to ↑-at-the-first-
// post. It paints the next cached page immediately (pinning the previously-top
// post in place so the view doesn't jump) and asks the server for anything
// older. A no-op unless the viewport is actually pegged at the top. The
// loadingOlder guard keeps a fast flick from stacking a server fetch per
// momentum frame: the cache prepend self-limits (it moves the offset off the
// top), so only the network fetch is gated.
func (m *Model) paginateMsgsOnWheelTop() tea.Cmd {
	if m.msgsView.YOffset() > 0 || len(m.posts) == 0 {
		return nil // not at the top of the loaded window
	}
	oldestID := m.posts[0].Id
	if oldestID == "" {
		return nil // optimistic stub at the head; nothing to fetch before it
	}
	// Free-scroll leaves the selection wherever it was (often the bottom). Move
	// it to the post at the viewport top so trimPostWindowTail can shed the now-
	// offscreen tail and keep renderMessages bounded.
	m.syncMsgSelToViewport()
	older := m.loadOlderFromStore(m.posts[0].ChannelId, m.posts[0].CreateAt)
	if n := len(older); n > 0 {
		m.posts = append(older, m.posts...)
		m.postIdx += n
		m.trimPostWindowTail()
		m.renderMessages()
		// The cache prepend never trims the head, so the previously-top post now
		// starts n posts in; pin the viewport there so it stays put.
		if n < len(m.msgRowStarts) {
			m.msgFreeOffset = m.msgRowStarts[n]
			m.msgsView.SetYOffset(m.msgFreeOffset)
		}
		m.status = ""
	}
	if m.loadingOlder {
		return nil // a server page is already in flight; don't stack another
	}
	if len(older) == 0 {
		m.status = "loading older messages…"
	}
	m.loadingOlder = true
	return m.fetchOlder(m.openChannelID, oldestID)
}

// paginateMsgsOnWheelBottom is the downward mirror of paginateMsgsOnWheelTop:
// when the wheel reaches the bottom of the loaded window it loads newer history
// (the ↓-at-the-last-post path), which only has anything to do when the loaded
// tail sits below the live tail — e.g. reading forward from a search hit centred
// on an old post. Unlike the keyboard ↓, hitting the bottom with the wheel never
// drops into the composer; at the live tail it's simply a no-op. The post at the
// viewport top is pinned across the head-trim so the view doesn't jump, and
// loadingNewer keeps a momentum flood to one fetch at a time.
func (m *Model) paginateMsgsOnWheelBottom() tea.Cmd {
	if len(m.posts) == 0 || len(m.msgRowStarts) == 0 {
		return nil
	}
	total := m.msgRowStarts[len(m.msgRowStarts)-1]
	maxOff := total - m.msgsView.Height()
	if maxOff < 0 {
		maxOff = 0
	}
	if m.msgsView.YOffset() < maxOff {
		return nil // not at the bottom of the loaded window
	}
	last := m.posts[len(m.posts)-1]
	newer := m.loadNewerFromStore(last.ChannelId, last.CreateAt)
	if len(newer) == 0 {
		return nil // at the live tail; nothing newer to page in
	}
	newestID := last.Id // anchor the server fetch on the OLD tail (pre-append)
	// Appending grows content below the viewport (no shift), but trimPostWindowHead
	// drops posts above it; pin the viewport-top post so the head-trim is invisible.
	anchorID, within := m.msgFreeAnchor()
	m.posts = append(m.posts, newer...)
	if idx := m.postIndexByID(anchorID); idx >= 0 {
		m.postIdx = idx // let the trim shed everything above the viewport top
	}
	m.trimPostWindowHead()
	m.renderMessages()
	if idx := m.postIndexByID(anchorID); idx >= 0 {
		m.msgFreeOffset = m.msgRowStarts[idx] + within
		m.msgsView.SetYOffset(m.msgFreeOffset)
	}
	m.status = ""
	if m.loadingNewer {
		return nil // a server page is already in flight; don't stack another
	}
	if newestID == "" {
		return nil
	}
	m.loadingNewer = true
	return m.fetchNewer(m.openChannelID, newestID)
}

// msgFreeAnchor returns the id of the post at the top of the free-scroll
// viewport and how many of its rows sit above the top edge, so the exact pixel
// position can be restored after posts are inserted above it (a server page of
// older history merged in mid wheel-scroll).
func (m Model) msgFreeAnchor() (id string, within int) {
	off := m.msgFreeOffset
	for i := 0; i+1 < len(m.msgRowStarts) && i < len(m.posts); i++ {
		if m.msgRowStarts[i+1] > off {
			return m.posts[i].Id, off - m.msgRowStarts[i]
		}
	}
	return "", 0
}

// postIndexByID returns the index of the post with the given id in m.posts, or
// -1 if it isn't loaded.
func (m Model) postIndexByID(id string) int {
	if id == "" {
		return -1
	}
	for i, p := range m.posts {
		if p.Id == id {
			return i
		}
	}
	return -1
}

// viewportPageStep is the number of visual rows a PageUp/PageDown moves the
// view when scrolling inside a tall post: one screenful minus a row of overlap
// so the reader keeps their place. At least one so the keys always move.
func (m Model) viewportPageStep() int {
	if s := m.msgsView.Height() - 1; s > 1 {
		return s
	}
	return 1
}

// scrollSelWithin scrolls the viewport inside the selected post when that post
// is taller than the pane, so its hidden interior is reachable without the
// selection jumping to a neighbour. dir is -1 (up) or +1 (down); step is the
// number of visual rows to move. It returns true when it consumed the key (the
// caller should re-render and stop); false means the post isn't tall or the
// view is already pinned to that edge, so the caller should move the selection
// as usual. Relies on m.msgRowStarts / the live YOffset from the last render —
// valid here because the selection (and thus every post's height) is unchanged.
func (m *Model) scrollSelWithin(dir, step int) bool {
	h := m.msgsView.Height()
	if h <= 0 || m.postIdx < 0 || m.postIdx+1 >= len(m.msgRowStarts) {
		return false
	}
	visStart := m.msgRowStarts[m.postIdx]
	visEnd := m.msgRowStarts[m.postIdx+1]
	if visEnd-visStart <= h {
		return false // post fits; let the caller move the selection
	}
	off := m.msgsView.YOffset()
	switch {
	case dir < 0: // up: reveal rows hidden above the view
		if off <= visStart {
			return false
		}
		off -= step
		if off < visStart {
			off = visStart
		}
	default: // down: reveal rows hidden below the view
		if off+h >= visEnd {
			return false
		}
		off += step
		if off > visEnd-h {
			off = visEnd - h
		}
	}
	m.pendingMsgOffset = off
	m.keepMsgOffset = true
	return true
}

// anchorSelOnLand sets the one-shot anchor for a selection that just moved onto
// a post taller than the pane, so the post opens at its natural reading edge:
// moving down lands on its top (read it top-down); moving up lands on its
// bottom (the edge adjacent to where the cursor came from). Posts that fit the
// pane are left to renderMessages' default "ensure visible" handling.
func (m *Model) anchorSelOnLand(dir int) {
	h := m.msgsView.Height()
	if h <= 0 || m.postIdx < 0 || m.postIdx+1 >= len(m.msgRowStarts) {
		return
	}
	if m.msgRowStarts[m.postIdx+1]-m.msgRowStarts[m.postIdx] <= h {
		return
	}
	if dir < 0 {
		m.anchorMsgSelBottom = true
	} else {
		m.anchorMsgSelTop = true
	}
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

// gotoPrevOwnMessage moves the selection to the nearest message authored by the
// logged-in user strictly above the current selection, so repeated presses walk
// back through the user's own messages. It scans the loaded window first and,
// when no earlier own message is loaded, pages older cached posts in (the same
// way ↑-at-top does) and keeps scanning until one is found or the store is
// exhausted. Tombstones are skipped — there's nothing to revisit on a deleted
// message. Reports whether the selection moved; the caller renders.
func (m *Model) gotoPrevOwnMessage() bool {
	if m.me == nil || m.me.Id == "" || len(m.posts) == 0 {
		return false
	}
	start := m.postIdx
	if start > len(m.posts) {
		start = len(m.posts)
	}
	for {
		for i := start - 1; i >= 0; i-- {
			p := m.posts[i]
			if p.DeleteAt == 0 && p.UserId == m.me.Id {
				m.postIdx = i
				m.anchorSelOnLand(-1)
				return true
			}
		}
		// Nothing above in the loaded window — page in the next older cached
		// chunk and resume scanning from where it was prepended. The trim caps
		// the window, so a channel the user never posted in costs a bounded
		// store walk, not unbounded growth.
		older := m.loadOlderFromStore(m.posts[0].ChannelId, m.posts[0].CreateAt)
		if len(older) == 0 {
			return false
		}
		m.posts = append(older, m.posts...)
		m.postIdx += len(older)
		start = len(older)
		m.trimPostWindowTail()
	}
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
		// Tall selected post with content hidden above: scroll within it
		// before moving the selection to the previous post.
		if m.scrollSelWithin(-1, 1) {
			m.renderMessages()
			return m, settle
		}
		if m.postIdx > 0 {
			m.postIdx--
			m.anchorSelOnLand(-1)
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
		// Mark the fetch in flight so a wheel flick onto the top doesn't stack a
		// second PostsBefore on top of this keyboard-triggered one.
		m.loadingOlder = true
		return m, tea.Batch(settle, m.fetchOlder(m.openChannelID, oldestID))
	case key.Matches(msg, m.keys.Down):
		settle := m.bumpMRFetch()
		// Tall selected post with content hidden below: scroll within it
		// before moving the selection to the next post.
		if m.scrollSelWithin(1, 1) {
			m.renderMessages()
			return m, settle
		}
		if m.postIdx < len(m.posts)-1 {
			m.postIdx++
			m.anchorSelOnLand(1)
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
		// Mark the fetch in flight so a wheel flick onto the bottom doesn't stack
		// a second PostsAfter on top of this keyboard-triggered one.
		m.loadingNewer = true
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
		// Page through a tall selected post before advancing the selection.
		if m.scrollSelWithin(1, m.viewportPageStep()) {
			m.renderMessages()
			return m, m.bumpMRFetch()
		}
		m.postIdx += m.messagesPageStep()
		if m.postIdx > len(m.posts)-1 {
			m.postIdx = len(m.posts) - 1
		}
		m.anchorSelOnLand(1)
		m.renderMessages()
		return m, m.bumpMRFetch()
	case key.Matches(msg, m.keys.PageUp):
		if len(m.posts) == 0 {
			return m, nil
		}
		if m.scrollSelWithin(-1, m.viewportPageStep()) {
			m.renderMessages()
			return m, m.bumpMRFetch()
		}
		m.postIdx -= m.messagesPageStep()
		if m.postIdx < 0 {
			m.postIdx = 0
		}
		m.anchorSelOnLand(-1)
		m.renderMessages()
		return m, m.bumpMRFetch()
	case key.Matches(msg, m.keys.NextHit):
		return m.gotoSearchHit(1)
	case key.Matches(msg, m.keys.PrevHit):
		return m.gotoSearchHit(-1)
	case key.Matches(msg, m.keys.PrevOwnMsg):
		if !m.gotoPrevOwnMessage() {
			m.status = "no earlier message from you"
			return m, nil
		}
		m.renderMessages()
		return m, m.bumpMRFetch()
	case key.Matches(msg, m.keys.OpenThread), key.Matches(msg, m.keys.ReplyInThread):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			return m, nil
		}
		if m.posts[m.postIdx].DeleteAt != 0 {
			// A tombstone keeps its Id/RootId, so without this guard the user
			// could open a thread on (or reply into) a removed message.
			m.status = "message was deleted"
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
	case key.Matches(msg, m.keys.Download):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			m.status = "no message selected"
			return m, nil
		}
		return m.downloadFromPost(m.posts[m.postIdx])
	case key.Matches(msg, m.keys.OpenRef):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			m.status = "no message selected"
			return m, nil
		}
		return m.openRefForPost(m.posts[m.postIdx])
	case key.Matches(msg, m.keys.ChannelInfo):
		return m.openChannelInfo()
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
		if m.posts[m.postIdx].DeleteAt != 0 {
			// Content is stripped on a tombstone; copying would silently put an
			// empty string on the clipboard and still report success.
			m.status = "message was deleted"
			return m, nil
		}
		return m, m.copyPostMarkdown(m.posts[m.postIdx])
	case key.Matches(msg, m.keys.CopyCode):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			m.status = "no message selected"
			return m, nil
		}
		if m.posts[m.postIdx].DeleteAt != 0 {
			m.status = "message was deleted"
			return m, nil
		}
		return m.copyCodeFromPost(m.posts[m.postIdx])
	case key.Matches(msg, m.keys.ShowHistory):
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			return m, nil
		}
		p := m.posts[m.postIdx]
		if p.DeleteAt != 0 {
			m.status = "message was deleted"
			return m, nil
		}
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
		if p.DeleteAt != 0 {
			m.status = "message was deleted"
			return m, nil
		}
		return m, m.openReactionPicker(p.Id)
	case key.Matches(msg, m.keys.Collapse):
		return m.toggleCollapse(focusMessages)
	}
	// Anything else (pgup/pgdn, half-page, etc.) falls through to viewport.
	var cmd tea.Cmd
	m.msgsView, cmd = m.msgsView.Update(msg)
	return m, cmd
}

// toggleCollapse collapses or expands the selected post in the given pane (the
// messages transcript or the open thread) — everything about it that can fold: a
// long body, and the inline thumbnails of any image it carries.
//
// The two have mirrored defaults — a long body arrives folded, a thumbnail arrives
// shown — so they are tracked in mirrored sets (expandedPosts, thumbsCollapsed) and
// what one z press means depends on which of them the post has:
//
//   - No image: exactly as before. z expands the folded body, z again re-folds it.
//   - An image: the post starts collapsible, so the first z *collapses* — the
//     thumbnails go, and a long body folds back with them — and the second expands
//     both. The image is the thing the eye is drawn to, so it decides the direction.
//
// Collapsing a post is not merely visual: its thumbnails stop being fetched,
// animated and held in terminal memory (see releaseThumbs and thumbKeysInRows), so
// a channel of GIFs can be quietened one message at a time.
//
// No-ops when nothing is selected, or the post hasn't landed on the server yet (an
// optimistic stub has no stable id to key on). Body collapsing can be switched off
// (collapse_long_messages: 0) — thumbnails still collapse, since that hides an image
// rather than text.
func (m Model) toggleCollapse(pane focus) (tea.Model, tea.Cmd) {
	var p *model.Post
	if pane == focusThread {
		if m.threadIdx < 0 || m.threadIdx >= len(m.threadPosts) {
			return m, nil
		}
		p = m.threadPosts[m.threadIdx]
	} else {
		if m.postIdx < 0 || m.postIdx >= len(m.posts) {
			return m, nil
		}
		p = m.posts[m.postIdx]
	}
	hasThumbs := m.inlineImagesActive() && len(m.postThumbKeys(p)) > 0
	if m.collapseRows <= 0 && !hasThumbs {
		m.status = "message collapsing is disabled"
		return m, nil
	}
	if p.Id == "" {
		m.status = "message hasn't landed yet"
		return m, nil
	}
	if m.expandedPosts == nil {
		m.expandedPosts = map[string]bool{}
	}
	if hasThumbs {
		if m.thumbsCollapsed == nil {
			m.thumbsCollapsed = map[string]bool{}
		}
		collapsed := !m.thumbsCollapsed[p.Id]
		m.thumbsCollapsed[p.Id] = collapsed
		// The body follows the same intent: collapsing the message folds it back,
		// expanding the message opens it up.
		m.expandedPosts[p.Id] = !collapsed
		if collapsed {
			m.releaseThumbs(p)
		}
	} else {
		m.expandedPosts[p.Id] = !m.expandedPosts[p.Id]
	}
	if pane == focusThread {
		m.renderThread()
	} else {
		m.renderMessages()
	}
	return m, nil
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
	if p.DeleteAt != 0 {
		return false // already a tombstone — nothing left to edit or delete
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
	// A thread has its own draft, separate from its channel's. Stash whatever
	// the composer is currently drafting (the open channel, or a different
	// thread) and load this thread's draft in its place — so the channel draft
	// isn't carried into the reply. Skipped mid-edit: beginEditPost owns the
	// composer then, and an edit isn't a draft.
	var draftCmd tea.Cmd
	if m.editingPostID == "" {
		draftCmd = m.swapToThreadDraft(channelID, rootID)
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
	return m, tea.Batch(m.fetchThread(rootID), focusCmd, draftCmd)
}

// closeThread tears down the sidebar and returns focus to the messages pane.
// It stashes the thread's reply text as that thread's draft and restores the
// open channel's draft into the composer, the mirror of openThreadForPost's
// swap. Returns the server-sync Cmd for the stashed draft (plus a grammar
// recheck for the restored one), or nil.
func (m *Model) closeThread() tea.Cmd {
	if !m.threadOpen {
		return nil
	}
	// Stash the thread's composer text under its own draft before the thread
	// state is cleared (stashThreadDraft reads threadChannelID/threadRootID).
	// Skipped mid-edit — an edit owns the composer and isn't a draft.
	var stashCmd tea.Cmd
	if m.editingPostID == "" {
		stashCmd = m.stashThreadDraft(m.threadChannelID, m.threadRootID, m.input.Value())
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
	// Same as openThreadForPost — leave the composer alone if an edit is in
	// progress so the user keeps the "✎ " mode indicator and prefilled text.
	var grammarCmd tea.Cmd
	if m.editingPostID == "" {
		m.input.SetPromptFunc(2, inputPromptFunc("> "))
		m.setComposerDraft(m.drafts[m.openChannelID])
		grammarCmd = m.scheduleGrammarCheck()
	}
	m.resizeMessagesViewport()
	m.resizeInput()
	m.renderMessages()
	return tea.Batch(stashCmd, grammarCmd)
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
		if m.onSQLTab() {
			m.focus = focusSQL
			return m, m.sql.input.Focus()
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
		if m.onSQLTab() {
			m.focus = focusSQL
			return m, m.sql.input.Focus()
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
