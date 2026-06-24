package ui

import (
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// feedContextLines is how many already-read messages we show above the
// "new" divider in each bubble, for context. Two matches the spec (and
// the search tab's before-context).
const feedContextLines = 2

// feedUnreadCap bounds how many unread messages each bubble renders. A
// very busy channel collapses the overflow into a "+N earlier unread"
// row so one channel can't swamp the whole feed.
const feedUnreadCap = 8

// feedUnreadMax bounds how many unread posts we keep in memory / persist
// per channel, regardless of how many the server returns. The header
// count still reflects the server's true unread total (m.unread).
const feedUnreadMax = 50

// feedEntry is one bubble in the unread feed: a channel with unread
// messages, plus a little already-read context above them. Posts are
// oldest→newest. The channel itself is looked up by id at render time so
// label/team changes are always reflected.
type feedEntry struct {
	channelID string
	context   []*model.Post // already-read context (≤ feedContextLines)
	unread    []*model.Post // unread posts, oldest→newest (≤ feedUnreadMax)
	mention   bool          // had a mention at build time (border tint hint)
	// phantom marks a channel the server still counts as unread but with no
	// genuine message to show — the unread post was deleted (or is a system
	// post), leaving an off-by-one in the server's root counter. The bubble
	// renders an explanatory line instead of messages; opening or marking it
	// read clears the stale count. See fetchFeed.
	phantom bool
}

// lastActivity returns the create time of the newest unread post, used
// to sort the most recently active channels to the top of the feed.
func (e feedEntry) lastActivity() int64 {
	if n := len(e.unread); n > 0 {
		return e.unread[n-1].CreateAt
	}
	return 0
}

// feedState owns the combined-unread-feed UI on the synthetic Feed tab.
type feedState struct {
	view    viewport.Model
	entries []feedEntry
	idx     int  // selected bubble
	loading bool // a build/refresh is in flight
	built   bool // we've assembled the feed at least once this session
	err     string
	seq     int // bumps on every build; stale feedLoadedMsg are dropped

	// Empty-state splash animation. wavePhase advances each frame to drift
	// the water; waveActive guards against running more than one tick loop.
	// Both only matter while the feed has no entries (see feedart.go).
	wavePhase  int
	waveActive bool

	// Gull fly-bys, advanced once per wave frame (see advanceFeedBird). A bird
	// crosses a few times an hour: birdWait counts down the random idle frames
	// until the next fly-by; while birdActive, birdStep advances the crossing
	// and birdYOff holds that fly-by's random sky height.
	birdActive bool
	birdStep   int
	birdWait   int
	birdYOff   int

	// zones maps viewport visual rows to feed-entry indices for mouse
	// hit-testing; zonesTotal is the rendered list's full height. Both are
	// rebuilt by renderFeedResults on every repaint and cleared in its
	// non-list states (empty splash / loading / error). See mouse.go.
	zones      []bubbleZone
	zonesTotal int
}

// newFeedState constructs the viewport used by the Feed tab. Called once
// at startup from New().
func newFeedState() feedState {
	vp := viewport.New()
	vp.SoftWrap = true
	// Seed a short initial idle so the first gull appears soon after the splash
	// is first viewed, then settles into the rare random cadence.
	return feedState{view: vp, birdWait: int(birdGapFirst / feedWaveInterval)}
}

// feedTarget is a snapshot of one unread channel taken on the UI
// goroutine, handed to the worker so it never touches UI state.
type feedTarget struct {
	channelID    string
	lastViewedAt int64
	unreadCount  int // count-based "N new" total; caps how many posts the bubble shows
	mention      bool
}

// onFeedTab reports whether the synthetic Feed tab is currently selected.
func (m *Model) onFeedTab() bool {
	kind, _, _ := m.tabAt(m.teamIdx)
	return kind == tabFeed
}

// openFeedTab switches to the synthetic Feed tab and kicks off a build.
// Idempotent — calling it while already on Feed just refreshes.
func (m *Model) openFeedTab() tea.Cmd {
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, _, _ := m.tabAt(i); kind == tabFeed {
			m.teamIdx = i
			break
		}
	}
	m.filterMode = false
	m.filter.SetValue("")
	m.filter.Blur()
	m.input.Blur()
	m.search.input.Blur()
	m.focus = focusFeed
	return m.buildFeed()
}

// lastViewedByChannel maps each known channel to its server-side
// last-viewed timestamp, the boundary between read and unread posts.
func (m Model) lastViewedByChannel() map[string]int64 {
	out := make(map[string]int64, len(m.members))
	for _, mb := range m.members {
		out[mb.ChannelId] = mb.LastViewedAt
	}
	return out
}

// channelMuted reports whether the given channel is muted for the current
// user, per the cached channel-member notify props (mark_unread == mention).
// Muted channels are deliberately kept out of the unread feed. The lookup is
// O(1) against the mutedChannels set setMembers maintains; a nil set (no
// members loaded yet) reports nothing muted.
func (m *Model) channelMuted(channelID string) bool {
	return m.mutedChannels[channelID]
}

// setMembers installs a fresh channel-member snapshot and rebuilds the derived
// mutedChannels set. Every assignment to m.members must go through here so the
// muted set never drifts from the members it summarises.
func (m *Model) setMembers(ms model.ChannelMembersWithTeamData) {
	m.members = ms
	m.rebuildMutedChannels()
}

// rebuildMutedChannels recomputes the muted-channel set from m.members.
func (m *Model) rebuildMutedChannels() {
	muted := make(map[string]bool, len(m.members))
	for _, mb := range m.members {
		if mb.IsChannelMuted() {
			muted[mb.ChannelId] = true
		}
	}
	m.mutedChannels = muted
}

// feedBadgeCounts returns the number of distinct channels with unread
// messages and with mentions for the Feed tab badge. Muted channels are
// skipped to match buildFeed: they keep their unread/mention counts for the
// sidebar, but the feed is the "things to read" list they're opted out of.
func (m *Model) feedBadgeCounts() (unread, mention int) {
	for id, n := range m.unread {
		if n > 0 && !m.channelMuted(id) {
			unread++
		}
	}
	for id, n := range m.mentions {
		if n > 0 && !m.channelMuted(id) {
			mention++
		}
	}
	return unread, mention
}

// buildFeed snapshots the current unread channels and fires the worker
// that fetches each channel's unread posts. Bumps the seq so any earlier
// in-flight build is ignored when it lands. Muted channels are skipped —
// they still carry an unread count, but the feed is the "things to read"
// list and muted channels are explicitly opted out of that.
func (m *Model) buildFeed() tea.Cmd {
	m.feed.seq++
	m.feed.loading = true
	m.feed.err = ""
	m.renderFeedResults()

	lastViewed := m.lastViewedByChannel()
	chans := m.unreadChannels()
	targets := make([]feedTarget, 0, len(chans))
	for _, c := range chans {
		if m.channelMuted(c.Id) {
			continue
		}
		targets = append(targets, feedTarget{
			channelID:    c.Id,
			lastViewedAt: lastViewed[c.Id],
			unreadCount:  m.unread[c.Id],
			mention:      m.mentions[c.Id] > 0,
		})
	}
	return m.fetchFeed(m.feed.seq, targets)
}

// fetchFeed pulls each target channel's unread posts (and a little cached
// context) on a worker goroutine and returns them as a feedLoadedMsg.
// Value receiver so the closure captures a copy of m, mirroring fetchPosts.
// `known` is a snapshot of m.userNames taken here on the UI goroutine: the
// closure runs on a Bubble Tea worker, so it must not read the live map while
// the Update loop writes it (issue #2). m.store is a pointer set once at
// startup, so reading it inside the closure is safe.
func (m Model) fetchFeed(seq int, targets []feedTarget) tea.Cmd {
	known := snapshotNames(m.userNames)
	return func() tea.Msg {
		// Refresh the read boundary from the server. m.members is captured
		// once at startup and never updated, so its LastViewedAt drifts
		// stale as channels are read this session — which would otherwise
		// drag already-read messages into the bubbles. ViewChannel keeps the
		// server side current, so a re-fetch here is authoritative.
		var freshMembers model.ChannelMembersWithTeamData
		lastViewed := map[string]int64{}
		if m.me != nil {
			if ms, err := m.client.ChannelMembers(m.ctx, m.me.Id); err == nil {
				freshMembers = ms
				for _, mb := range ms {
					lastViewed[mb.ChannelId] = mb.LastViewedAt
				}
			}
		}

		entries := make([]feedEntry, 0, len(targets))
		need := map[string]struct{}{}
		var toPersist []*model.Post
		for _, t := range targets {
			lv := t.lastViewedAt
			if v, ok := lastViewed[t.channelID]; ok {
				lv = v
			}
			var (
				pl  *model.PostList
				err error
			)
			if lv > 0 {
				pl, err = m.client.PostsSince(m.ctx, t.channelID, lv)
			} else {
				// Unknown read boundary — show the most recent page instead
				// of pulling the channel's entire history.
				pl, err = m.client.Posts(m.ctx, t.channelID, feedUnreadCap)
			}
			if err != nil || pl == nil {
				continue
			}
			full := unreadFromPostList(pl, lv)
			if len(full) == 0 {
				// The server still counts this channel as unread, but nothing
				// past the boundary is a genuine message — the unread post was
				// deleted or is a system post, leaving a stale root counter.
				// Surface a labelled "phantom" bubble (with no messages) so the
				// feed and the tab badge agree and the user can clear the count
				// by opening or marking it read, rather than silently dropping
				// it and leaving an unexplained badge.
				if t.unreadCount > 0 || t.mention {
					entries = append(entries, feedEntry{
						channelID: t.channelID,
						mention:   t.mention,
						phantom:   true,
					})
				}
				continue
			}
			// Persist the whole since-boundary page, not just the capped
			// slice the bubble shows. Dropping the older rows we already
			// fetched is exactly what leaves an interior cache gap for a
			// busy unread channel — the messages exist on screen briefly
			// but never reach the store, so a later channel-open can't
			// repaint them and search can't find them.
			toPersist = append(toPersist, full...)
			unread := capUnread(full, t.unreadCount)
			var ctxPosts []*model.Post
			if m.store != nil {
				ctxPosts, _ = m.store.BeforeInChannel(t.channelID, unread[0].CreateAt, feedContextLines, false)
			}
			entries = append(entries, feedEntry{
				channelID: t.channelID,
				context:   ctxPosts,
				unread:    unread,
				mention:   t.mention,
			})
			for _, p := range ctxPosts {
				if _, have := known[p.UserId]; !have {
					need[p.UserId] = struct{}{}
				}
			}
			for _, p := range unread {
				if _, have := known[p.UserId]; !have {
					need[p.UserId] = struct{}{}
				}
			}
		}

		users := map[string]string{}
		if len(need) > 0 {
			ids := make([]string, 0, len(need))
			for id := range need {
				ids = append(ids, id)
			}
			if us, err := m.client.UsersByIDs(m.ctx, ids); err == nil {
				for _, u := range us {
					users[u.Id] = u.Username
				}
			}
		}
		// Grow the local corpus with whatever we fetched so a later
		// channel-open paints warm and search can find these posts.
		if m.store != nil && len(toPersist) > 0 {
			_ = m.store.UpsertMany(toPersist)
		}
		return feedLoadedMsg{seq: seq, entries: entries, users: users, members: freshMembers}
	}
}

// capUnread trims an oldest→newest unread slice to what a bubble may
// show. It first bounds the slice to the count-based "N new" total
// (keeping the newest), so the body can never contradict the header when
// the read boundary is stale or zero; a non-positive count skips that
// step. feedUnreadMax is then applied as an absolute ceiling.
func capUnread(unread []*model.Post, unreadCount int) []*model.Post {
	if unreadCount > 0 && len(unread) > unreadCount {
		unread = unread[len(unread)-unreadCount:]
	}
	if len(unread) > feedUnreadMax {
		unread = unread[len(unread)-feedUnreadMax:]
	}
	return unread
}

// unreadFromPostList flips a PostList into oldest→newest order and keeps
// only the genuine unread messages: non-deleted, non-system posts created
// after the last-viewed boundary. A non-positive boundary keeps every
// returned post (the caller already limited the page).
func unreadFromPostList(pl *model.PostList, lastViewedAt int64) []*model.Post {
	out := make([]*model.Post, 0, len(pl.Order))
	for i := len(pl.Order) - 1; i >= 0; i-- {
		p, ok := pl.Posts[pl.Order[i]]
		if !ok || p == nil {
			continue
		}
		if p.DeleteAt != 0 || p.IsSystemMessage() {
			continue
		}
		if lastViewedAt > 0 && p.CreateAt <= lastViewedAt {
			continue
		}
		out = append(out, p)
	}
	return out
}

// applyFeedResults installs a completed build if it's still fresh, then
// sorts the entries (mentions first, most-recent activity next) using the
// current mention state.
func (m Model) applyFeedResults(msg feedLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.feed.seq {
		return m, nil
	}
	// Adopt the boundary the worker fetched so the rest of the UI (and the
	// next build) sees the same fresh read state instead of the frozen
	// startup snapshot.
	if len(msg.members) > 0 {
		m.setMembers(msg.members)
	}
	for id, name := range msg.users {
		m.userNames[id] = name
	}
	entries := msg.entries
	sort.SliceStable(entries, func(i, j int) bool {
		mi, mj := m.mentions[entries[i].channelID] > 0, m.mentions[entries[j].channelID] > 0
		if mi != mj {
			return mi
		}
		return entries[i].lastActivity() > entries[j].lastActivity()
	})
	m.feed.entries = entries
	m.feed.loading = false
	m.feed.built = true
	if m.feed.idx >= len(entries) {
		m.feed.idx = len(entries) - 1
	}
	if m.feed.idx < 0 {
		m.feed.idx = 0
	}
	m.renderFeedResults()
	// An empty build lands on the calm-water splash — start the waves.
	return m, m.maybeStartFeedWaves()
}

// handleFeedKey owns the keystrokes routed to focus == focusFeed that
// aren't already consumed by the global shortcuts (q / ? / u / tab).
// Up/down select bubbles; enter opens the channel; m marks it read in
// place; r refreshes; esc returns to the tab strip.
func (m Model) handleFeedKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c": // hardwired quit
		return m, tea.Quit
	case msg.String() == "esc": // hardwired back-to-tab-strip
		m.focus = focusTeams
		return m, nil
	case key.Matches(msg, m.keys.Up), key.Matches(msg, m.keys.InputUp):
		// The feed is a pure list (no text input), so reading-pane ↑/k both
		// move; ctrl+n (input_down) is also honoured below for symmetry with
		// the search list. ctrl+p (input_up) is shadowed by the global switcher.
		if m.feed.idx > 0 {
			m.feed.idx--
			m.renderFeedResults()
		}
		return m, nil
	case key.Matches(msg, m.keys.Down), key.Matches(msg, m.keys.InputDown):
		if m.feed.idx < len(m.feed.entries)-1 {
			m.feed.idx++
			m.renderFeedResults()
		}
		return m, nil
	case key.Matches(msg, m.keys.Home):
		m.feed.idx = 0
		m.renderFeedResults()
		return m, nil
	case key.Matches(msg, m.keys.End):
		m.feed.idx = len(m.feed.entries) - 1
		m.renderFeedResults()
		return m, nil
	case msg.String() == "pgup":
		m.feed.view.ScrollUp(m.feed.view.Height() / 2)
		return m, nil
	case msg.String() == "pgdown":
		m.feed.view.ScrollDown(m.feed.view.Height() / 2)
		return m, nil
	case key.Matches(msg, m.keys.OpenChannel):
		return m.openFeedEntry()
	case key.Matches(msg, m.keys.MarkRead):
		return m.markFeedEntryRead()
	case key.Matches(msg, m.keys.Refresh):
		return m, m.buildFeed()
	case key.Matches(msg, m.keys.Tab):
		return m.cycleFocus(1)
	case key.Matches(msg, m.keys.ShiftTab):
		return m.cycleFocus(-1)
	}
	return m, nil
}

// openFeedEntry opens the selected bubble's channel (which marks it read,
// as a normal channel open does), jumping to the first unread message.
// The entry is dropped from the feed since it's no longer unread.
func (m Model) openFeedEntry() (tea.Model, tea.Cmd) {
	if m.feed.idx < 0 || m.feed.idx >= len(m.feed.entries) {
		return m, nil
	}
	e := m.feed.entries[m.feed.idx]
	ch := m.findChannel(e.channelID)
	if ch == nil {
		m.status = "channel not in the local list"
		return m, nil
	}
	m.removeFeedEntry(e.channelID)
	m.switchToChannelHomeTeam(ch)
	m.filterValue = ""
	m.filter.SetValue("")
	m.focus = focusMessages
	if len(e.unread) > 0 {
		m.pendingJumpPostID = e.unread[0].Id
	}
	return m, tea.Batch(m.openChannelLoadCmd(ch.Id), m.bumpChannelStat(ch.Id))
}

// markFeedEntryRead clears the selected channel's unread/mention state on
// the server and locally, and drops its bubble — without leaving the
// feed.
func (m Model) markFeedEntryRead() (tea.Model, tea.Cmd) {
	if m.feed.idx < 0 || m.feed.idx >= len(m.feed.entries) {
		return m, nil
	}
	e := m.feed.entries[m.feed.idx]
	delete(m.unread, e.channelID)
	delete(m.mentions, e.channelID)
	m.removeFeedEntry(e.channelID)
	m.status = "marked read"
	m.renderFeedResults()
	// Reading the last entry reveals the splash — animate it.
	return m, tea.Batch(m.markChannelViewed(e.channelID), m.maybeStartFeedWaves())
}

// removeFeedEntry drops the bubble for channelID and clamps the selection.
func (m *Model) removeFeedEntry(channelID string) {
	out := m.feed.entries[:0]
	for _, e := range m.feed.entries {
		if e.channelID != channelID {
			out = append(out, e)
		}
	}
	m.feed.entries = out
	if m.feed.idx >= len(m.feed.entries) {
		m.feed.idx = len(m.feed.entries) - 1
	}
	if m.feed.idx < 0 {
		m.feed.idx = 0
	}
}

// feedAppendPosted folds a live `posted` WS event for a background
// channel into the feed so it updates without a manual refresh. No-op
// until the feed has been built at least once.
func (m *Model) feedAppendPosted(p *model.Post) {
	if !m.feed.built || p == nil || p.Id == "" || p.DeleteAt != 0 || p.IsSystemMessage() {
		return
	}
	// Muted channels are excluded from the feed (see buildFeed), so a live
	// post in one must not slip a fresh bubble in either.
	if m.channelMuted(p.ChannelId) {
		return
	}
	for i := range m.feed.entries {
		if m.feed.entries[i].channelID != p.ChannelId {
			continue
		}
		for _, ex := range m.feed.entries[i].unread {
			if ex.Id == p.Id {
				return // already shown
			}
		}
		u := append(m.feed.entries[i].unread, p)
		if len(u) > feedUnreadMax {
			u = u[len(u)-feedUnreadMax:]
		}
		m.feed.entries[i].unread = u
		m.feed.entries[i].phantom = false // a real message arrived; no longer a ghost
		if m.onFeedTab() {
			m.renderFeedResults()
		}
		return
	}
	// First unread in a channel that isn't in the feed yet — add a bubble
	// at the top (newest activity).
	var ctxPosts []*model.Post
	if m.store != nil {
		ctxPosts, _ = m.store.BeforeInChannel(p.ChannelId, p.CreateAt, feedContextLines, false)
	}
	entry := feedEntry{
		channelID: p.ChannelId,
		context:   ctxPosts,
		unread:    []*model.Post{p},
		mention:   m.mentions[p.ChannelId] > 0,
	}
	m.feed.entries = append([]feedEntry{entry}, m.feed.entries...)
	if len(m.feed.entries) > 1 && m.feed.idx >= 0 {
		m.feed.idx++ // keep the previously-selected bubble selected
	}
	if m.onFeedTab() {
		m.renderFeedResults()
	}
}

// feedRemovePost drops a deleted post from the feed, removing the whole
// bubble if it leaves the channel with no unread left.
func (m *Model) feedRemovePost(postID string) {
	if !m.feed.built || postID == "" {
		return
	}
	changed := false
	for i := range m.feed.entries {
		u := m.feed.entries[i].unread
		for j, p := range u {
			if p.Id == postID {
				m.feed.entries[i].unread = append(u[:j], u[j+1:]...)
				changed = true
				break
			}
		}
	}
	if !changed {
		return
	}
	out := m.feed.entries[:0]
	for _, e := range m.feed.entries {
		if len(e.unread) > 0 || e.phantom {
			out = append(out, e)
		}
	}
	m.feed.entries = out
	if m.feed.idx >= len(m.feed.entries) {
		m.feed.idx = len(m.feed.entries) - 1
	}
	if m.feed.idx < 0 {
		m.feed.idx = 0
	}
	if m.onFeedTab() {
		m.renderFeedResults()
	}
}

// sizeFeedView keeps the feed viewport in sync with the body area. width
// is the pane's outer width (border included); height is the pane's inner
// content height (border already subtracted by the caller).
func (m *Model) sizeFeedView(width, height int) {
	innerW := width - 2 // strip the pane's left/right border
	if innerW < 10 {
		innerW = 10
	}
	// Header rows inside the pane: titleRow (1) + rule (1).
	const headerRows = 2
	bodyH := height - headerRows
	if bodyH < 1 {
		bodyH = 1
	}
	m.feed.view.SetWidth(innerW)
	m.feed.view.SetHeight(bodyH)
}

// renderFeedResults populates the viewport with one bubble per unread
// channel, scrolling the selected bubble into view. Mirrors
// renderSearchResults.
func (m *Model) renderFeedResults() {
	// Non-list states (error / splash / loading) carry no clickable bubbles;
	// the bubble loop below repopulates these when there are entries.
	m.feed.zones, m.feed.zonesTotal = nil, 0
	if m.feed.err != "" {
		m.feed.view.SetContent(
			lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("  feed error: " + m.feed.err),
		)
		return
	}
	if len(m.feed.entries) == 0 {
		if m.feed.loading {
			m.feed.view.SetContent(
				lipgloss.NewStyle().Foreground(dimColor).Render("  gathering unread messages…"))
			return
		}
		// Nothing unread: the calm ship-on-water splash, with slowly
		// drifting waves (animation armed by maybeStartFeedWaves).
		m.feed.view.SetContent(m.feedEmptyContent())
		return
	}

	if m.feed.idx >= len(m.feed.entries) {
		m.feed.idx = len(m.feed.entries) - 1
	}
	if m.feed.idx < 0 {
		m.feed.idx = 0
	}

	innerW := m.feed.view.Width()
	if innerW < 10 {
		innerW = 10
	}
	var allLines []string
	selStart, selEnd := -1, -1
	zones := make([]bubbleZone, 0, len(m.feed.entries))
	vw := m.feed.view.Width()
	acc := 0 // running visual-row count, for the per-bubble click zones
	for i, e := range m.feed.entries {
		bubble := m.renderFeedBubble(innerW-2, e, i == m.feed.idx)
		bubbleLines := strings.Split(bubble, "\n")
		zones = append(zones, bubbleZone{row0: acc, idx: i})
		if i == m.feed.idx {
			selStart = len(allLines)
			selEnd = selStart + len(bubbleLines)
		}
		allLines = append(allLines, bubbleLines...)
		acc += visualRowsBefore(bubbleLines, len(bubbleLines), vw) + 1 // +1 blank separator
		allLines = append(allLines, "")                                // blank separator between bubbles
	}
	m.feed.zones, m.feed.zonesTotal = zones, acc
	m.feed.view.SetContentLines(allLines)

	if h := m.feed.view.Height(); h > 0 && selStart >= 0 {
		visStart := visualRowsBefore(allLines, selStart, m.feed.view.Width())
		visEnd := visualRowsBefore(allLines, selEnd, m.feed.view.Width())
		off := m.feed.view.YOffset()
		switch {
		case visStart < off:
			off = visStart
		case visEnd > off+h:
			off = visEnd - h
		}
		if off < 0 {
			off = 0
		}
		m.feed.view.SetYOffset(off)
	}
}

// renderFeedBubble draws one unread channel as a bordered box: the
// breadcrumb + unread count on the top border, up to feedContextLines of
// already-read context (dim), a "new" divider, then the unread messages.
// Mention channels get a red border (and sort first); the selected bubble
// gets the focused border.
func (m Model) renderFeedBubble(outerW int, e feedEntry, selected bool) string {
	if outerW < 8 {
		outerW = 8
	}
	inner := outerW - 2
	contentW := inner - 2
	if contentW < 1 {
		contentW = 1
	}

	borderColor := dimColor
	if m.mentions[e.channelID] > 0 {
		borderColor = mentionTabColor // red, matching the mention vocabulary
	}
	if selected {
		borderColor = focusedColor
	}

	header := "?"
	if ch := m.findChannel(e.channelID); ch != nil {
		header = m.channelBreadcrumb(ch)
	}
	n := m.unread[e.channelID]
	if n <= 0 {
		n = len(e.unread)
	}

	// A phantom channel has a stale server count but no message to show: the
	// unread post was deleted or is a system post. Render a single line that
	// names the discrepancy and the way out, instead of a "new" count and an
	// empty body. Opening or marking it read clears the count.
	if e.phantom {
		if n > 0 {
			header += " · " + strconv.Itoa(n) + " stale"
		} else {
			header += " · stale" // count already cleared elsewhere; drop next refresh
		}
		body := lipgloss.NewStyle().Foreground(dimColor).
			Render("no unread messages — the count is out of sync · enter opens · m clears it")
		return bubbleBox(inner, header, []string{body}, borderColor, selected)
	}

	header += " · " + strconv.Itoa(n) + " new"
	if mc := m.mentions[e.channelID]; mc > 0 {
		header += " · " + plural(mc, "mention", "mentions")
	}

	var bodyLines []string
	for _, p := range e.context {
		bodyLines = append(bodyLines, m.renderHitLine(p, contentW, true, false))
	}
	bodyLines = append(bodyLines, feedDivider(contentW))

	shown := e.unread
	if len(shown) > feedUnreadCap {
		hiddenOlder := len(shown) - feedUnreadCap
		shown = shown[len(shown)-feedUnreadCap:]
		bodyLines = append(bodyLines,
			lipgloss.NewStyle().Foreground(dimColor).Render("↑ +"+strconv.Itoa(hiddenOlder)+" earlier unread"))
	}
	for _, p := range shown {
		bodyLines = append(bodyLines, m.renderHitLine(p, contentW, false, false))
	}
	return bubbleBox(inner, header, bodyLines, borderColor, selected)
}

// feedDivider renders the "new" separator drawn between the read context
// and the unread messages inside a bubble.
func feedDivider(width int) string {
	style := lipgloss.NewStyle().Foreground(dimColor)
	const label = "─ new "
	if width <= lipgloss.Width(label) {
		w := width
		if w < 1 {
			w = 1
		}
		return style.Render(strings.Repeat("─", w))
	}
	return style.Render(label + strings.Repeat("─", width-lipgloss.Width(label)))
}

// renderFeedPane composes the entire body of the Feed tab: title, a
// separator rule, then the bubble viewport. Mirrors renderSearchPane
// without the search input row.
func (m Model) renderFeedPane(height, width int) string {
	innerH := height - 1 // bottom border (top connects to the tab strip)
	if innerH < 1 {
		innerH = 1
	}
	if width < 10 {
		width = 10
	}

	dim := lipgloss.NewStyle().Foreground(dimColor)
	title := titleStyle.Render("Unread Feed")
	var meta string
	switch {
	case m.feed.loading:
		meta = dim.Render("  refreshing…")
	case len(m.feed.entries) > 0:
		meta = dim.Render("  " + plural(len(m.feed.entries), "channel", "channels") + "  ·  enter open · m mark read · r refresh")
	}
	titleRow := title + meta

	rule := dim.Render(strings.Repeat("─", width-2))
	body := m.feed.view.View()
	rows := []string{titleRow, rule, body}

	style := lipgloss.NewStyle().
		Border(border).
		UnsetBorderTop().
		Width(width).
		Height(innerH)
	if m.focus == focusFeed {
		style = style.BorderForeground(focusedColor)
	} else {
		style = style.BorderForeground(dimColor)
	}
	return style.Render(strings.Join(rows, "\n"))
}
