package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/editor"
	"matterbox/internal/viewport"
)

// These tests probe the "open at the bottom" invariant: after switching teams
// (or channels) the messages pane should be scrolled so the *newest* message is
// the bottom line — that's what the user expects to read first. The reported
// symptom is that this is "sometimes" not the case. The scenarios below pin down
// exactly when it holds and when it doesn't, by driving the real switch handlers
// and asserting the resulting viewport offset.
//
// Flow under test: a team switch (alt+<n>, ctrl+→, or a mouse click on a tab)
// runs gotoTab -> openChannelLoadCmd, which loads the destination's cached posts,
// parks postIdx on the newest, and calls renderMessages to position the
// viewport. The viewport (m.msgsView) is shared across channels and is NOT reset
// on open, so whatever scroll state the previous channel left behind is the
// starting point renderMessages works from.

// msgPost builds a one-line post by "u" with UpdateAt set (so the store keeps it).
func msgPost(id string, createAt int64, body string) *model.Post {
	return &model.Post{Id: id, CreateAt: createAt, UpdateAt: createAt, UserId: "u", Message: body}
}

// tallMsgPost builds a post whose body is `lines` short lines, so it renders
// taller than the test viewport (height 40) — a long "newest message".
func tallMsgPost(id string, createAt int64, lines int) *model.Post {
	return &model.Post{
		Id: id, CreateAt: createAt, UpdateAt: createAt, UserId: "u",
		Message: strings.TrimRight(strings.Repeat("line\n", lines), "\n"),
	}
}

// teamScrollModel builds a renderable Model parked on team t1 / channel c1, with
// a store seeded from byChannel and a second team t2 (channel c3) to switch to.
// byChannel maps channel id -> its cached posts; it must contain "c1" (the open
// channel). c1's posts are loaded and the pane rendered, so the model starts in
// the same place a real session would: a channel open, scrolled to its bottom.
func teamScrollModel(t *testing.T, byChannel map[string][]*model.Post) Model {
	t.Helper()

	var seed []*model.Post
	for cid, ps := range byChannel {
		for _, p := range ps {
			p.ChannelId = cid
			seed = append(seed, p)
		}
	}
	st := openSeededStore(t, seed...)

	vp := viewport.New()
	vp.SoftWrap = true
	vp.SetWidth(80)
	vp.SetHeight(40)
	ta := editor.New()
	ta.SetWidth(40)

	m := Model{
		teams: []*model.Team{
			{Id: "t1", DisplayName: "Engineering", Name: "eng"},
			{Id: "t2", DisplayName: "Design", Name: "design"},
		},
		channels: map[string][]*model.Channel{
			"t1": {
				{Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "general"},
				{Id: "c2", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "random"},
			},
			"t2": {
				{Id: "c3", TeamId: "t2", Type: model.ChannelTypeOpen, DisplayName: "ideas"},
			},
		},
		userNames:     map[string]string{"u": "u"},
		drafts:        map[string]string{},
		me:            &model.User{Id: "me"},
		keys:          newKeyMap("ctrl"),
		focus:         focusMessages,
		width:         100,
		height:        44,
		msgsView:      vp,
		input:         ta,
		filter:        textinput.New(),
		search:        newSearchState(false),
		feed:          newFeedState(false),
		showSQL:       true,
		sql:           newSQLState(false),
		vcache:        &viewCache{},
		store:         st,
		posts:         byChannel["c1"],
		postIdx:       len(byChannel["c1"]) - 1,
		openChannelID: "c1",
		channelIdx:    0,
	}
	m.teamIdx = m.firstTeamTabIdx() // land on t1
	m.renderMessages()              // paint c1 at its natural (bottom) position
	return m
}

// teamTabIdx returns the tab-strip index of the team with the given id.
func teamTabIdx(m Model, teamID string) int {
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, id, _ := m.tabAt(i); kind == tabTeam && id == teamID {
			return i
		}
	}
	return -1
}

// assertAtBottom checks the messages pane is scrolled so the newest message sits
// on the bottom line (the viewport is pinned to its maximum offset).
func assertAtBottom(t *testing.T, m Model, ctx string) {
	t.Helper()
	total := 0
	if n := len(m.msgRowStarts); n > 0 {
		total = m.msgRowStarts[n-1]
	}
	h := m.msgsView.Height()
	wantBottom := max(0, total-h)
	if !m.msgsView.AtBottom() {
		t.Errorf("%s: messages pane not at the bottom: YOffset=%d, want %d "+
			"(content=%d rows, pane=%d rows) — the newest message is off-screen below",
			ctx, m.msgsView.YOffset(), wantBottom, total, h)
	}
}

// ---- Team switching via the keyboard, short newest message (the common case) ----

// TestTeamSwitchAltDigitLandsAtBottom: alt+2 jumps to team t2 (channel c3) whose
// newest message is short. The pane should open pinned to the bottom. This is
// the everyday case and is expected to PASS.
func TestTeamSwitchAltDigitLandsAtBottom(t *testing.T) {
	m := teamScrollModel(t, map[string][]*model.Post{
		"c1": {tallMsgPost("c1a", 100, 60), msgPost("c1b", 200, "latest in c1")},
		"c3": {tallMsgPost("c3a", 100, 60), msgPost("c3b", 200, "older"), msgPost("c3c", 300, "newest in c3")},
	})
	m.msgsView.GotoBottom() // we were reading c1 at its bottom

	out, _ := m.handleKey(altKey('2'))
	got := out.(Model)
	if got.openChannelID != "c3" {
		t.Fatalf("alt+2 opened %q, want c3", got.openChannelID)
	}
	assertAtBottom(t, got, "alt+2 -> c3 (short newest)")
}

// TestTeamSwitchCtrlArrowLandsAtBottom: ctrl+→ steps to the next team (t2/c3)
// and should also open at the bottom. Expected to PASS.
func TestTeamSwitchCtrlArrowLandsAtBottom(t *testing.T) {
	m := teamScrollModel(t, map[string][]*model.Post{
		"c1": {tallMsgPost("c1a", 100, 60), msgPost("c1b", 200, "latest in c1")},
		"c3": {tallMsgPost("c3a", 100, 60), msgPost("c3b", 200, "older"), msgPost("c3c", 300, "newest in c3")},
	})
	m.msgsView.GotoBottom()

	out, _ := m.handleKey(ctrlKey(tea.KeyRight))
	got := out.(Model)
	if got.openChannelID != "c3" {
		t.Fatalf("ctrl+→ opened %q, want c3", got.openChannelID)
	}
	assertAtBottom(t, got, "ctrl+→ -> c3 (short newest)")
}

// ---- Team switching onto a channel whose newest message is taller than the pane ----

// TestTeamSwitchTallNewestFromBottom_LandsAtBottom: switching to a channel whose
// newest message is taller than the pane, *while the previous channel was
// scrolled down*, should still show the bottom of that newest message. Today it
// does NOT: renderMessages' default "keep the selection visible" branch anchors
// the (taller-than-pane) newest post to its TOP, leaving the newest lines
// off-screen below. Expected to FAIL — this is the "sometimes not at the bottom"
// the report describes, triggered by a long last message.
func TestTeamSwitchTallNewestFromBottom_LandsAtBottom(t *testing.T) {
	m := teamScrollModel(t, map[string][]*model.Post{
		"c1": {tallMsgPost("c1a", 100, 60), msgPost("c1b", 200, "latest in c1")},
		// c3's newest message is 60 lines — taller than the 40-row pane.
		"c3": {msgPost("c3a", 100, "older"), msgPost("c3b", 200, "older2"), tallMsgPost("c3big", 300, 60)},
	})
	m.msgsView.GotoBottom() // previous channel left a large scroll offset

	out, _ := m.handleKey(altKey('2'))
	got := out.(Model)
	if got.openChannelID != "c3" {
		t.Fatalf("alt+2 opened %q, want c3", got.openChannelID)
	}
	assertAtBottom(t, got, "alt+2 -> c3 (tall newest, prev scrolled down)")
}

// TestTeamSwitchTallNewestFromTop_LandsAtBottom: the *same* tall-newest channel,
// but reached while the previous channel was scrolled to its TOP (small carried
// offset). Here the default branch happens to scroll down to the bottom, so it
// works. Contrasting this PASS with the FAIL above demonstrates the bug's
// "sometimes" nature: the outcome depends on where you were scrolled in the
// channel you left.
func TestTeamSwitchTallNewestFromTop_LandsAtBottom(t *testing.T) {
	m := teamScrollModel(t, map[string][]*model.Post{
		"c1": {tallMsgPost("c1a", 100, 60), msgPost("c1b", 200, "latest in c1")},
		"c3": {msgPost("c3a", 100, "older"), msgPost("c3b", 200, "older2"), tallMsgPost("c3big", 300, 60)},
	})
	m.msgsView.SetYOffset(0) // previous channel was at its top

	out, _ := m.handleKey(altKey('2'))
	got := out.(Model)
	if got.openChannelID != "c3" {
		t.Fatalf("alt+2 opened %q, want c3", got.openChannelID)
	}
	assertAtBottom(t, got, "alt+2 -> c3 (tall newest, prev at top)")
}

// ---- Switching via the mouse while in wheel free-scroll mode ----

// TestTeamSwitchMouseTabKeepsFreeScroll_LandsAtBottom: the user mouse-wheels up
// in c1 (which arms sticky free-scroll: msgScrollFree + msgFreeOffset), then
// clicks team t2's tab. handleMouseClick routes a tab hit straight to gotoTab
// without clearing msgScrollFree (only handleKey clears it), so renderMessages
// keeps the carried wheel offset for the *new* channel instead of pinning to the
// bottom. Expected to FAIL — switching by mouse-click leaves the new channel
// stuck at the old scroll position.
func TestTeamSwitchMouseTabKeepsFreeScroll_LandsAtBottom(t *testing.T) {
	m := teamScrollModel(t, map[string][]*model.Post{
		"c1": {tallMsgPost("c1a", 100, 60), msgPost("c1b", 200, "latest in c1")},
		"c3": {tallMsgPost("c3a", 100, 60), msgPost("c3b", 200, "older"), msgPost("c3c", 300, "newest in c3")},
	})
	// Simulate a wheel scroll up that left free-scroll mode active near the top.
	m.msgScrollFree = true
	m.msgFreeOffset = 3
	m.msgsView.SetYOffset(3)

	// A click on team t2's tab is dispatched as gotoTab(tabIdx) by handleMouseClick.
	out, _ := m.gotoTab(teamTabIdx(m, "t2"))
	got := out.(Model)
	if got.openChannelID != "c3" {
		t.Fatalf("tab click opened %q, want c3", got.openChannelID)
	}
	assertAtBottom(t, got, "mouse tab-click -> c3 (free-scroll carried over)")
}

// TestChannelSwitchMouseClickKeepsFreeScroll_LandsAtBottom: the same free-scroll
// carry-over, but switching channels within a team by clicking the sidebar
// (openVisibleChannel). Also expected to FAIL — the sidebar click path likewise
// never clears msgScrollFree.
func TestChannelSwitchMouseClickKeepsFreeScroll_LandsAtBottom(t *testing.T) {
	m := teamScrollModel(t, map[string][]*model.Post{
		"c1": {tallMsgPost("c1a", 100, 60), msgPost("c1b", 200, "latest in c1")},
		"c2": {tallMsgPost("c2a", 100, 60), msgPost("c2b", 200, "newest in c2")},
	})
	m.msgScrollFree = true
	m.msgFreeOffset = 3
	m.msgsView.SetYOffset(3)

	// A click on the c2 row in the sidebar is dispatched as openVisibleChannel(1).
	out, _ := m.openVisibleChannel(1)
	got := out.(Model)
	if got.openChannelID != "c2" {
		t.Fatalf("sidebar click opened %q, want c2", got.openChannelID)
	}
	assertAtBottom(t, got, "mouse sidebar-click -> c2 (free-scroll carried over)")
}
