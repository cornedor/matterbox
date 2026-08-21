package ui

import (
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
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

	// showMuted lets muted channels into the feed (and the tab badge), which
	// otherwise leave them out. Seeded from config.FeedShowMuted and flipped
	// for the session by the toggle key (M) or the "> Feed: …" command.
	showMuted bool

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
// at startup from New(). showMuted is the config's starting answer to "do
// muted channels belong in the feed?"; M flips it for the session.
func newFeedState(showMuted bool) feedState {
	vp := viewport.New()
	vp.SoftWrap = true
	// Seed a short initial idle so the first gull appears soon after the splash
	// is first viewed, then settles into the rare random cadence.
	return feedState{view: vp, showMuted: showMuted, birdWait: int(birdGapFirst / feedWaveInterval)}
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
func (m *Model) openFeedTab(via string) tea.Cmd {
	m.recordFeedAction("opened", via)
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
// The lookup is O(1) against the mutedChannels set setMembers maintains; a nil
// set (no members loaded yet) reports nothing muted.
func (m *Model) channelMuted(channelID string) bool {
	return m.mutedChannels[channelID]
}

// feedExcludes reports whether the feed (and its tab badge) should leave the
// channel out: muted channels are opted out of the "things to read" list,
// unless the user asked to see them (feed_show_muted / the M toggle). Every
// place that decides what belongs in the feed goes through here so the build,
// the live-post path and the badge can't disagree.
func (m *Model) feedExcludes(channelID string) bool {
	return m.channelMuted(channelID) && !m.feed.showMuted
}

// toggleFeedMuted flips whether muted channels appear in the feed and rebuilds
// it, so both directions (letting them in, throwing them out) are one code
// path. The choice lasts the session; feed_show_muted sets the startup state.
func (m *Model) toggleFeedMuted() tea.Cmd {
	m.feed.showMuted = !m.feed.showMuted
	if m.feed.showMuted {
		m.status = "feed: showing muted channels"
	} else {
		m.status = "feed: hiding muted channels"
	}
	return m.buildFeed()
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
// messages and with mentions for the Feed tab badge. Excluded channels are
// skipped to match buildFeed, so the badge always counts exactly what the feed
// would show: muted channels keep their unread/mention counts for the sidebar
// but stay out of both, unless feed.showMuted lets them in.
func (m *Model) feedBadgeCounts() (unread, mention int) {
	for id, n := range m.unread {
		if n > 0 && !m.feedExcludes(id) {
			unread++
		}
	}
	for id, n := range m.mentions {
		if n > 0 && !m.feedExcludes(id) {
			mention++
		}
	}
	return unread, mention
}

// buildFeed snapshots the current unread channels and fires the worker
// that fetches each channel's unread posts. Bumps the seq so any earlier
// in-flight build is ignored when it lands. Muted channels are skipped —
// they still carry an unread count, but the feed is the "things to read"
// list and muted channels are explicitly opted out of that — unless
// feed.showMuted (M / feed_show_muted) asks for them.
func (m *Model) buildFeed() tea.Cmd {
	m.feed.seq++
	m.feed.loading = true
	m.feed.err = ""
	m.renderFeedResults()

	lastViewed := m.lastViewedByChannel()
	chans := m.unreadChannels()
	targets := make([]feedTarget, 0, len(chans))
	for _, c := range chans {
		if m.feedExcludes(c.Id) {
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
		// Muted channels sink below everything else: they're shown on request,
		// but a chatty muted channel must not push the channels you actually
		// follow off the top of the feed.
		qi, qj := m.channelMuted(entries[i].channelID), m.channelMuted(entries[j].channelID)
		if qi != qj {
			return qj
		}
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
	case key.Matches(msg, m.keys.PageUp):
		m.feed.view.ScrollUp(m.feed.view.Height() / 2)
		return m, nil
	case key.Matches(msg, m.keys.PageDown):
		m.feed.view.ScrollDown(m.feed.view.Height() / 2)
		return m, nil
	case key.Matches(msg, m.keys.OpenChannel):
		m.recordFeedAction("open_channel", "key")
		return m.openFeedEntry()
	case key.Matches(msg, m.keys.FeedReply):
		m.recordFeedAction("reply", "key")
		return m.replyFromFeedEntry()
	case key.Matches(msg, m.keys.MarkRead):
		m.recordFeedAction("mark_read", "key")
		return m.markFeedEntryRead()
	case key.Matches(msg, m.keys.MarkAllRead):
		m.recordFeedAction("mark_all_read", "key")
		return m, m.markAllFeedRead()
	case key.Matches(msg, m.keys.Refresh):
		m.recordFeedAction("refresh", "key")
		return m, m.buildFeed()
	case key.Matches(msg, m.keys.FeedMuted):
		m.recordFeedAction("toggle_muted", "key")
		return m, m.toggleFeedMuted()
	case key.Matches(msg, m.keys.Tab):
		return m.cycleFocus(1)
	case key.Matches(msg, m.keys.ShiftTab):
		return m.cycleFocus(-1)
	}
	return m, nil
}

// enterFeedEntry opens the selected bubble's channel exactly as a sidebar
// open does (enterChannel via openChannelLoadCmd — so openChannelID, the
// composer target, the title and live routing all move), jumping to its first
// unread message. The bubble is dropped from the feed since the open marks
// the channel read. Shared by open (enter) and reply (R). Returns the entry
// and the load command; ok=false when nothing usable is selected.
func (m *Model) enterFeedEntry() (e feedEntry, cmd tea.Cmd, ok bool) {
	if m.feed.idx < 0 || m.feed.idx >= len(m.feed.entries) {
		return feedEntry{}, nil, false
	}
	e = m.feed.entries[m.feed.idx]
	ch := m.findChannel(e.channelID)
	if ch == nil {
		m.status = "channel not in the local list"
		return feedEntry{}, nil, false
	}
	m.removeFeedEntry(e.channelID)
	m.switchToChannelHomeTeam(ch)
	m.filterValue = ""
	m.filter.SetValue("")
	m.focus = focusMessages
	if len(e.unread) > 0 {
		m.pendingJumpPostID = e.unread[0].Id
	}
	return e, tea.Batch(m.openChannelLoadCmd(ch.Id, "feed"), m.bumpChannelStat(ch.Id)), true
}

// openFeedEntry opens the selected bubble's channel (enter).
func (m Model) openFeedEntry() (tea.Model, tea.Cmd) {
	_, cmd, ok := m.enterFeedEntry()
	if !ok {
		return m, nil
	}
	return m, cmd
}

// replyFromFeedEntry (R) opens the selected bubble's channel like
// openFeedEntry and then the thread of its newest unread message — the one
// at the bottom of the bubble, the message you're answering (the oldest may
// be an old thread's reply, or above the bubble's cap and not shown at all)
// — with the composer focused on the reply. So the reply lands in that
// thread, in that channel, not wherever the previous open channel was.
func (m Model) replyFromFeedEntry() (tea.Model, tea.Cmd) {
	e, cmd, ok := m.enterFeedEntry()
	if !ok {
		return m, nil
	}
	if len(e.unread) == 0 {
		return m, cmd
	}
	next, threadCmd := m.openThreadForPost(e.unread[len(e.unread)-1], "feed")
	return next, tea.Batch(cmd, threadCmd)
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

// markAllFeedRead clears every bubble at once: the same thing m does to the
// selected channel, done to all of them. It marks exactly what the feed shows
// — muted channels stay unread unless they're on screen (M / feed_show_muted),
// because the feed is the "things to read" list and the button can only speak
// for the list it sits above. Bound to A and to the button on the title row.
func (m *Model) markAllFeedRead() tea.Cmd {
	if len(m.feed.entries) == 0 {
		return nil
	}
	ids := make([]string, 0, len(m.feed.entries))
	for _, e := range m.feed.entries {
		delete(m.unread, e.channelID)
		delete(m.mentions, e.channelID)
		ids = append(ids, e.channelID)
	}
	m.feed.entries = nil
	m.feed.idx = 0
	m.hover = hoverState{} // the button just vanished from under the pointer
	m.status = "marked " + plural(len(ids), "channel", "channels") + " read"
	m.renderFeedResults()
	// Emptying the feed reveals the splash — animate it.
	return tea.Batch(m.markChannelsViewed(ids), m.maybeStartFeedWaves())
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
	// post in one must not slip a fresh bubble in either — unless the user is
	// showing muted channels, in which case it belongs like any other.
	if m.feedExcludes(p.ChannelId) {
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
	if m.channelMuted(p.ChannelId) {
		// Muted channels sort last (see applyFeedResults), so a live post in one
		// joins at the bottom rather than jumping the queue. The selection index
		// is untouched — nothing shifted under it.
		m.feed.entries = append(m.feed.entries, entry)
	} else {
		m.feed.entries = append([]feedEntry{entry}, m.feed.entries...)
		if len(m.feed.entries) > 1 && m.feed.idx >= 0 {
			m.feed.idx++ // keep the previously-selected bubble selected
		}
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
	if m.channelMuted(e.channelID) {
		// Only reachable with showMuted on; name why this one is here so a
		// muted channel isn't mistaken for something that wants attention.
		header += " · muted"
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

// feedHiddenMuted counts the muted channels with unread messages the feed is
// currently leaving out, so the header can admit they exist. Zero whenever
// showMuted is on — nothing is hidden then.
func (m *Model) feedHiddenMuted() int {
	if m.feed.showMuted {
		return 0
	}
	n := 0
	for id, c := range m.unread {
		if c > 0 && m.channelMuted(id) {
			n++
		}
	}
	return n
}

// feedHints builds the title-row key legend from the live bindings, so a
// rebind (or an unbind) is reflected instead of the hint claiming a key that
// does nothing. The muted toggle names the direction it would take you in, and
// carries the hidden count when there is one.
func (m *Model) feedHints() string {
	var hints []string
	add := func(b key.Binding, label string) {
		if !b.Enabled() || b.Help().Key == "" {
			return
		}
		hints = append(hints, b.Help().Key+" "+label)
	}
	if len(m.feed.entries) > 0 {
		add(m.keys.OpenChannel, "open")
		add(m.keys.FeedReply, "reply")
		add(m.keys.MarkRead, "mark read")
		add(m.keys.Refresh, "refresh")
	}
	if m.feed.showMuted {
		add(m.keys.FeedMuted, "hide muted")
	} else if n := m.feedHiddenMuted(); n > 0 {
		add(m.keys.FeedMuted, "show "+strconv.Itoa(n)+" muted")
	}
	return strings.Join(hints, " · ")
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
		meta = dim.Render("  " + plural(len(m.feed.entries), "channel", "channels") + "  ·  " + m.feedHints())
	case m.feedHiddenMuted() > 0:
		// Nothing to read but muted channels are being held back — say so, and
		// name the key that lets them in, or the splash is the whole story.
		meta = dim.Render("  " + m.feedHints())
	}
	contentW := width - 2 // the pane's inner width, between its side borders
	if contentW < 1 {
		contentW = 1
	}
	btn := m.feedMarkAllButton(contentW)
	m.armFeedButtonZone(btn)
	titleRow := feedTitleRow(title+meta, contentW, btn)

	rule := dim.Render(strings.Repeat("─", width-2))
	body := m.feed.view.View()
	rows := []string{titleRow, rule, body}
	// The rule under the title is a section divider across the pane, so it meets
	// the side borders as ├ ┤ rather than floating between them.
	titleRule := contentRows(rows[:1])

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
	return joinRuleRows(style.Render(strings.Join(rows, "\n")), titleRule)
}
