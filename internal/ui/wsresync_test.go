package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
)

// Everything a WebSocket drop costs us is invisible: the events that fired
// while we were disconnected are never replayed, so the badge counters (bumped
// per `posted` event) and the open transcript both silently fall behind. These
// tests pin the re-check that runs when the socket comes back.

// batchLen counts the commands inside a tea.Batch without running any of them.
// Batch with two or more commands returns a closure yielding a tea.BatchMsg
// holding them, so invoking the outer command is safe — it only unwraps the
// slice and never touches the API client the inner commands close over.
func batchLen(t *testing.T, cmd tea.Cmd) int {
	t.Helper()
	if cmd == nil {
		return 0
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		return len(msg)
	default:
		return 1 // Batch collapses a lone command to itself
	}
}

// wsModel is a connected-and-settled client: user loaded, channels and members
// in hand, one conversation open. The shape a disconnect actually finds.
func wsModel(t *testing.T) Model {
	t.Helper()
	m := feedMouseModel(2)
	m.feed.built = false // most tests here don't want the feed rebuild in the way
	m.channelsLoaded = true
	m.membersLoaded = true
	m.openChannelID = "c0"
	return m
}

// The base commands every (re)connect issues: resume reading events, resync
// presence. Anything above this count is catch-up work.
const wsConnectBaseCmds = 2

// A connect that follows a drop has a gap to close, so it must issue the
// catch-up fetches on top of the usual two.
func TestWSReconnectIssuesResync(t *testing.T) {
	m := wsModel(t)
	m.wsRetry = 3 // three failed attempts behind us

	out, cmd := m.update(wsConnectedMsg{})
	got := out.(Model)

	if n := batchLen(t, cmd); n <= wsConnectBaseCmds {
		t.Fatalf("reconnect issued %d cmds; want more than the %d base cmds (no catch-up ran)", n, wsConnectBaseCmds)
	}
	if got.wsRetry != 0 {
		t.Errorf("wsRetry = %d after a successful connect; want 0", got.wsRetry)
	}
}

// The clean first connect has nothing to catch up on — Init's fetches are
// already in flight, and duplicating them would double every startup request.
func TestWSFirstConnectSkipsResync(t *testing.T) {
	m := wsModel(t)
	m.wsRetry = 0

	_, cmd := m.update(wsConnectedMsg{})

	if n := batchLen(t, cmd); n != wsConnectBaseCmds {
		t.Fatalf("first connect issued %d cmds; want exactly %d (no catch-up)", n, wsConnectBaseCmds)
	}
}

// The retry counter is the only signal that we were disconnected, and a
// successful connect resets it — so every *subsequent* drop has to re-arm the
// catch-up, not just the first one of the session.
func TestWSEveryReconnectResyncs(t *testing.T) {
	m := wsModel(t)

	for cycle := 1; cycle <= 3; cycle++ {
		out, _ := m.update(wsClosedMsg{})
		m = out.(Model)
		if m.wsRetry != 1 {
			t.Fatalf("cycle %d: wsRetry = %d after a drop; want 1", cycle, m.wsRetry)
		}

		out, cmd := m.update(wsConnectedMsg{})
		m = out.(Model)
		if n := batchLen(t, cmd); n <= wsConnectBaseCmds {
			t.Fatalf("cycle %d: reconnect issued %d cmds; want catch-up on top of the %d base cmds",
				cycle, n, wsConnectBaseCmds)
		}
	}
}

// A drop leaves no live socket behind and schedules the retry that eventually
// produces the reconnect the catch-up hangs off.
func TestWSCloseClearsSocketAndRetries(t *testing.T) {
	m := wsModel(t)

	out, cmd := m.update(wsClosedMsg{})
	got := out.(Model)

	if got.ws != nil {
		t.Errorf("ws still set after close")
	}
	if got.wsRetry != 1 {
		t.Errorf("wsRetry = %d; want 1", got.wsRetry)
	}
	if cmd == nil {
		t.Fatal("no retry scheduled after a close")
	}
}

// The catch-up covers every half of what went stale: the sidebar (the channel
// list), the badges (channel members) and the open transcript (the recent page).
func TestResyncFetchesChannelsMembersAndOpenChannel(t *testing.T) {
	m := wsModel(t)

	// No store on this model, so the deletion sweep — which reconciles the
	// cache — is correctly absent; channels + members + fetchRecent remain.
	cmds := m.resyncAfterReconnect()

	if len(cmds) != 3 {
		t.Fatalf("expected channels + members + fetchRecent, got %d cmds", len(cmds))
	}
	for i, c := range cmds {
		if c == nil {
			t.Fatalf("cmd %d is nil", i)
		}
	}
}

// With a cache to reconcile against, the catch-up also sweeps for messages
// deleted during the outage — fetchRecent can't see those, the API omits them.
func TestResyncSweepsDeletionsWhenCached(t *testing.T) {
	m := wsModel(t)
	m.store = openSeededStore(t, &model.Post{
		Id: "p1", ChannelId: "c0", UserId: "u", Message: "hi", CreateAt: 100, UpdateAt: 100,
	})

	if n := len(m.resyncAfterReconnect()); n != 4 {
		t.Fatalf("expected channels + members + fetchRecent + deletion sweep, got %d cmds", n)
	}
}

// An empty cache gives the sweep no watermark to start from, and PostsSince(0)
// would drag the channel's entire history over the wire. Skip it instead.
func TestResyncSkipsDeletionSweepWithEmptyCache(t *testing.T) {
	m := wsModel(t)
	m.store = openSeededStore(t)

	if n := len(m.resyncAfterReconnect()); n != 3 {
		t.Fatalf("expected no deletion sweep against an empty cache, got %d cmds", n)
	}
}

// Reconnecting while sitting on a non-channel tab (feed, search) leaves no
// transcript to reconcile — only the sidebar and the badges need refreshing.
func TestResyncWithoutOpenChannel(t *testing.T) {
	m := wsModel(t)
	m.openChannelID = ""

	if n := len(m.resyncAfterReconnect()); n != 2 {
		t.Fatalf("expected the channels + members refetch, got %d cmds", n)
	}
}

// A drop early in startup can beat /users/me. There's no user id to fetch
// members for yet, so the catch-up must stay quiet rather than fire a request
// with an empty id.
func TestResyncBeforeUserLoaded(t *testing.T) {
	m := wsModel(t)
	m.me = nil
	m.openChannelID = ""

	if n := len(m.resyncAfterReconnect()); n != 0 {
		t.Fatalf("expected no cmds before the user is loaded, got %d", n)
	}
}

// unreadSeedModel is a two-channel model whose server-side state is supplied
// per test via seedUnread. Badges start empty, as they would after a session
// spent relying on live events.
func unreadSeedModel() Model {
	m := feedMouseModel(2)
	m.feed.built = false
	m.channelsLoaded = true
	m.membersLoaded = true
	m.unread = map[string]int{}
	m.mentions = map[string]int{}
	m.members = nil
	// feedMouseModel parks teamIdx on the Feed tab; move it to the tab holding
	// the channels so the sidebar cursor means something here.
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if _, id, _ := m.tabAt(i); id == "t1" {
			m.teamIdx = i
			break
		}
	}
	return m
}

// seedUnread sets what the *server* reports for a channel: total posts, how
// many this member has seen, and their mention count. That difference is what
// the badge is derived from.
func seedUnread(m *Model, channelID string, total, seen, mentions int64) {
	ch := m.findChannel(channelID)
	ch.TotalMsgCount, ch.TotalMsgCountRoot = total, total
	m.members = append(m.members, model.ChannelMemberWithTeamData{
		ChannelMember: model.ChannelMember{
			ChannelId:    channelID,
			MsgCount:     seen,
			MsgCountRoot: seen,
			MentionCount: mentions,
		},
	})
}

// The headline case: messages landed in a background channel while the socket
// was down. No `posted` event ever reached us, so only the server's counters
// know — and the refreshed badge has to reflect them.
func TestUnreadRestoredForMessagesMissedWhileDisconnected(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = "c0"
	seedUnread(&m, "c1", 5, 2, 1) // three arrived, one of them a mention

	m.applyUnreadFromMembers()

	if m.unread["c1"] != 3 {
		t.Errorf("unread = %d; want the 3 messages missed during the outage", m.unread["c1"])
	}
	if m.mentions["c1"] != 1 {
		t.Errorf("mentions = %d; want 1", m.mentions["c1"])
	}
}

// The mirror case: the outage isn't only about arrivals. A channel read on
// another client while we were away comes back read, so a badge we're still
// holding from before the drop has to clear.
func TestUnreadClearedWhenReadElsewhereDuringOutage(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = "c0"
	m.unread["c1"] = 2 // stale badge from before the drop
	m.mentions["c1"] = 1
	seedUnread(&m, "c1", 5, 5, 0) // server: fully caught up

	m.applyUnreadFromMembers()

	if n, ok := m.unread["c1"]; ok {
		t.Errorf("unread = %d; want the badge cleared after a read elsewhere", n)
	}
	if n, ok := m.mentions["c1"]; ok {
		t.Errorf("mentions = %d; want cleared", n)
	}
}

// A channel whose only missed message is a thread reply is genuinely unread
// here — matterbox renders replies inline. The root counters alone say "read",
// so the refresh must keep taking the max of both counter families.
func TestUnreadCountsThreadReplyMissedWhileDisconnected(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = "c0"
	ch := m.findChannel("c1")
	m.members = append(m.members, model.ChannelMemberWithTeamData{
		ChannelMember: model.ChannelMember{ChannelId: "c1", MsgCount: 4, MsgCountRoot: 2},
	})
	ch.TotalMsgCount, ch.TotalMsgCountRoot = 5, 2 // the extra post is a reply

	m.applyUnreadFromMembers()

	if m.unread["c1"] != 1 {
		t.Errorf("unread = %d; want 1 (a missed thread reply still counts)", m.unread["c1"])
	}
}

// The badge that gets skipped belongs to the *open* conversation, not to
// whatever the sidebar cursor happens to hover. The two diverge the moment the
// user moves the cursor without opening — the normal state by the time a
// reconnect re-seeds the badges.
func TestUnreadSkipsOpenChannelNotCursor(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = "c0"
	m.channelIdx = 1 // cursor parked on c1, which is not open
	seedUnread(&m, "c0", 3, 0, 0)
	seedUnread(&m, "c1", 3, 2, 0)

	m.applyUnreadFromMembers()

	if n, ok := m.unread["c0"]; ok {
		t.Errorf("open channel got a badge (%d); the user is reading it", n)
	}
	if m.unread["c1"] != 1 {
		t.Errorf("hovered channel unread = %d; want 1", m.unread["c1"])
	}
}

// The mark-read we send on open is fire-and-forget, so a catch-up landing
// right after it can still see the pre-view counters. Skipping the open
// channel is what keeps that from flashing a badge on the messages on screen.
func TestUnreadSkipsOpenChannelWithLaggingServerCount(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = "c1"
	m.channelIdx = 0
	seedUnread(&m, "c1", 9, 0, 4) // ViewChannel hasn't been processed yet

	m.applyUnreadFromMembers()

	if n, ok := m.unread["c1"]; ok {
		t.Errorf("unread = %d; want no badge on the channel being read", n)
	}
	if n, ok := m.mentions["c1"]; ok {
		t.Errorf("mentions = %d; want no badge on the channel being read", n)
	}
}

// The members fetch is in flight for a while. If the user switches channels
// before it lands, the skip must follow them — the channel they left is
// genuinely unread again, the one they arrived at is not.
func TestUnreadFollowsChannelSwitchedDuringResync(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = "c0" // open when the resync was dispatched
	seedUnread(&m, "c0", 4, 1, 0)
	seedUnread(&m, "c1", 4, 1, 0)

	m.openChannelID = "c1" // user moved on before the response arrived
	m.applyUnreadFromMembers()

	if m.unread["c0"] != 3 {
		t.Errorf("left-behind channel unread = %d; want 3", m.unread["c0"])
	}
	if n, ok := m.unread["c1"]; ok {
		t.Errorf("newly-open channel got a badge (%d)", n)
	}
}

// At startup nothing is open yet and members can land before the first
// fetchPosts, so the cursor still stands in for "the channel about to open".
func TestUnreadFallsBackToCursorBeforeAnythingIsOpen(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = ""
	m.channelIdx = 0
	seedUnread(&m, "c0", 3, 0, 0)
	seedUnread(&m, "c1", 3, 2, 0)

	m.applyUnreadFromMembers()

	if n, ok := m.unread["c0"]; ok {
		t.Errorf("about-to-open channel got a badge (%d)", n)
	}
	if m.unread["c1"] != 1 {
		t.Errorf("c1 unread = %d; want 1", m.unread["c1"])
	}
}

// A DM opened while we were disconnected comes back in the members list with
// no matching channel in the sidebar (nothing refetches the channel list).
// That's a known gap — but it must be a quiet one, not a nil-deref, and it
// must not stop the other channels from being refreshed.
func TestUnreadIgnoresMemberWithUnknownChannel(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = "c0"
	m.members = append(m.members, model.ChannelMemberWithTeamData{
		ChannelMember: model.ChannelMember{ChannelId: "brand-new-dm", MsgCount: 0, MentionCount: 2},
	})
	seedUnread(&m, "c1", 2, 1, 0)

	m.applyUnreadFromMembers()

	if _, ok := m.unread["brand-new-dm"]; ok {
		t.Errorf("unknown channel should be skipped, not badged")
	}
	if m.unread["c1"] != 1 {
		t.Errorf("known channel unread = %d; want 1 (an unknown member stopped the loop)", m.unread["c1"])
	}
}

// Members arriving after a reconnect move the badges, so a feed built before
// the drop is showing a stale set — rebuild it rather than wait for the user
// to notice.
func TestMembersLoadedRebuildsBuiltFeed(t *testing.T) {
	m := feedMouseModel(2)
	m.channelsLoaded = true
	seq := m.feed.seq

	out, cmd := m.update(membersLoadedMsg{})
	got := out.(Model)

	if got.feed.seq == seq {
		t.Errorf("feed not rebuilt after a members refresh (seq still %d)", seq)
	}
	if !got.feed.loading {
		t.Errorf("feed not marked loading")
	}
	if cmd == nil {
		t.Errorf("no feed fetch issued")
	}
}

// Startup goes through the same handler long before any feed exists; it must
// not conjure one there.
func TestMembersLoadedLeavesUnbuiltFeedAlone(t *testing.T) {
	m := feedMouseModel(2)
	m.channelsLoaded = true
	m.feed.built = false
	m.feed.entries = nil
	seq := m.feed.seq

	out, cmd := m.update(membersLoadedMsg{})
	got := out.(Model)

	if got.feed.seq != seq {
		t.Errorf("feed rebuilt at startup (seq %d → %d)", seq, got.feed.seq)
	}
	if cmd != nil {
		t.Errorf("unexpected feed fetch at startup")
	}
}

// catchupModel is a transcript short enough to overflow its pane, so where the
// viewport ends up after a merge is actually observable.
func catchupModel(posts []*model.Post, postIdx int) Model {
	vp := viewport.New()
	vp.SoftWrap = true
	vp.SetWidth(80)
	vp.SetHeight(4) // shorter than the content: scroll position means something
	for _, pp := range posts {
		pp.ChannelId, pp.UserId = "c", "u"
		pp.Message = "message " + pp.Id
	}
	return Model{
		posts:         posts,
		postIdx:       postIdx,
		openChannelID: "c",
		userNames:     map[string]string{"u": "alice"},
		unread:        map[string]int{},
		mentions:      map[string]int{},
		focus:         focusMessages,
		width:         100,
		height:        10,
		msgsView:      vp,
	}
}

// The transcript half of the catch-up, end to end: the recent page fetched on
// reconnect carries messages posted during the outage. For a user sitting at
// the bottom they must merge in, take the selection, and leave the pane
// scrolled to the newest message rather than top-aligning it.
func TestGapFillLandsMissedPostsAtBottom(t *testing.T) {
	m := catchupModel([]*model.Post{p("live1", 100), p("live2", 200)}, 1)

	out, _ := m.update(postsGapFilledMsg{
		channelID: "c",
		posts: []*model.Post{
			p("live2", 200),
			p("missed1", 300), // posted while the socket was down
			p("missed2", 400),
		},
	})
	got := out.(Model)

	if order := ids(got.posts); !eq(order, []string{"live1", "live2", "missed1", "missed2"}) {
		t.Fatalf("missed posts not merged: got %v", order)
	}
	if got.posts[got.postIdx].Id != "missed2" {
		t.Errorf("selection on %q; want the newest missed post", got.posts[got.postIdx].Id)
	}
	if !got.msgsView.AtBottom() {
		t.Errorf("pane not left at the bottom (offset %d of %d rows); the newest message is off screen",
			got.msgsView.YOffset(), got.msgsView.TotalLineCount())
	}
}

// Someone reading back through history when the socket returns must not be
// yanked to the bottom by the catch-up — the missed posts merge in, but the
// selection and the scroll position stay where they were.
func TestGapFillKeepsPlaceWhenReadingHistory(t *testing.T) {
	m := catchupModel([]*model.Post{p("old1", 100), p("old2", 200), p("old3", 300)}, 0)

	out, _ := m.update(postsGapFilledMsg{
		channelID: "c",
		posts:     []*model.Post{p("missed1", 400), p("missed2", 500)},
	})
	got := out.(Model)

	if order := ids(got.posts); !eq(order, []string{"old1", "old2", "old3", "missed1", "missed2"}) {
		t.Fatalf("missed posts not merged: got %v", order)
	}
	if got.posts[got.postIdx].Id != "old1" {
		t.Errorf("selection moved to %q; want to stay on old1", got.posts[got.postIdx].Id)
	}
	if got.msgsView.AtBottom() {
		t.Errorf("reader was yanked to the bottom by the catch-up")
	}
}

// A recent page for a channel the user has since left must not be spliced into
// whatever is on screen now.
func TestGapFillIgnoresChannelSwitchedAwayFrom(t *testing.T) {
	m := pagingModel([]*model.Post{p("a", 100)}, 0)

	out, _ := m.update(postsGapFilledMsg{channelID: "other", posts: []*model.Post{p("z", 50)}})
	got := out.(Model)

	if order := ids(got.posts); !eq(order, []string{"a"}) {
		t.Fatalf("stale-channel page mutated the view: got %v", order)
	}
}

// The skip that protects the conversation being read must not extend to a
// conversation that only *lingers* as openChannelID. A full-window tab (Feed,
// Search, SQL) replaces the transcript without opening anything, so nothing
// marked the channel underneath it read — the server's count is the truth, and
// dropping it strands unread messages with no badge, no bubble and no way back.
func TestUnreadKeptForOpenChannelBehindFeedTab(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = "c1"
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, _, _ := m.tabAt(i); kind == tabFeed {
			m.teamIdx = i
			break
		}
	}
	if !m.onFeedTab() {
		t.Fatal("setup: expected the Feed tab to be showing")
	}
	seedUnread(&m, "c1", 5, 2, 1)

	m.applyUnreadFromMembers()

	if m.unread["c1"] != 3 {
		t.Errorf("unread = %d; want 3 — the Feed tab is on screen, not the DM", m.unread["c1"])
	}
	if m.mentions["c1"] != 1 {
		t.Errorf("mentions = %d; want 1", m.mentions["c1"])
	}
}

// Same reasoning for a blurred terminal: the mark-read was suppressed, so the
// server's counters are not lagging us — they are ahead of us.
func TestUnreadKeptForOpenChannelWhileBlurred(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = "c1"
	m.termFocusKnown, m.termFocused = true, false
	seedUnread(&m, "c1", 5, 2, 0)

	m.applyUnreadFromMembers()

	if m.unread["c1"] != 3 {
		t.Errorf("unread = %d; want 3 — a blurred terminal never marked it read", m.unread["c1"])
	}
}

// Startup keeps its cursor fallback, but only while the terminal is focused:
// blurred, the first open won't mark anything read either.
func TestUnreadKeptForCursorChannelWhileBlurred(t *testing.T) {
	m := unreadSeedModel()
	m.openChannelID = ""
	m.channelIdx = 0
	m.termFocusKnown, m.termFocused = true, false
	seedUnread(&m, "c0", 3, 0, 0)

	m.applyUnreadFromMembers()

	if m.unread["c0"] != 3 {
		t.Errorf("unread = %d; want 3 — nothing has been marked read yet", m.unread["c0"])
	}
}
