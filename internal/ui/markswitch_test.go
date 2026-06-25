package ui

import (
	"path/filepath"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
)

// Jumping from an open DM to the synthetic Feed tab must NOT mark the DM read.
// The Feed tab shows its own pane without opening a channel, so openChannelID
// and viewGen stay pointed at the DM — but the DM is no longer on screen, so
// its pending mark-read dwell must not complete. This is the reported bug
// ("open a DM, go to Feed, the DM gets marked read").
func TestJumpingToFeedDoesNotMarkDMRead(t *testing.T) {
	m := Model{
		me:            &model.User{Id: "me"},
		userNames:     map[string]string{"me": "me", "u2": "u2"},
		drafts:        map[string]string{},
		unread:        map[string]int{"dm": 3},
		mentions:      map[string]int{"dm": 1},
		markReadDelay: time.Second,
		// No DMs/SQL tabs → tab index 0 is the Feed tab (see tabAt).
		hasDMs:  false,
		showSQL: false,
	}
	dmPosts := []*model.Post{{Id: "p1", ChannelId: "dm", CreateAt: 1000, UserId: "u2", Message: "hi"}}

	// Open the DM on a channel view; arm the dwell.
	m.openChannelLoadCmd("dm")
	dmGen := m.viewGen
	next, _ := m.update(postsLoadedMsg{channelID: "dm", posts: dmPosts, users: map[string]string{"u2": "u2"}})
	m = next.(Model)

	// Jump to the Feed tab. This does NOT open a channel, so viewGen and
	// openChannelID still point at the DM.
	m.teamIdx = 0
	m.focus = focusFeed
	if !m.onFeedTab() {
		t.Fatalf("test setup: expected to be on the Feed tab")
	}
	if m.openChannelID != "dm" || m.viewGen != dmGen {
		t.Fatalf("test setup: feed jump unexpectedly changed openChannelID=%q viewGen=%d", m.openChannelID, m.viewGen)
	}

	// The DM's dwell tick fires while we're on the Feed.
	next, _ = m.update(markViewedMsg{channelID: "dm", gen: dmGen})
	m = next.(Model)

	if m.unread["dm"] != 3 || m.mentions["dm"] != 1 {
		t.Fatalf("DM marked read after jumping to the Feed (unread=%d mentions=%d); want it left unread",
			m.unread["dm"], m.mentions["dm"])
	}
}

// reproBug drives the user-reported flow: open an unread DM, then switch to
// another channel before the mark-read dwell elapses. The pending dwell tick
// (captured against the DM's viewGen) must be ignored once the channel changed
// out from under it, so the DM stays unread.
func TestSwitchingAwayBeforeDwellKeepsDMUnread(t *testing.T) {
	m := Model{
		ctx:           nil,
		me:            &model.User{Id: "me"},
		userNames:     map[string]string{"me": "me", "u2": "u2"},
		drafts:        map[string]string{},
		unread:        map[string]int{"dm": 3, "food": 1},
		mentions:      map[string]int{},
		markReadDelay: time.Second,
		viewGen:       0,
	}

	dmPosts := []*model.Post{{Id: "p1", ChannelId: "dm", CreateAt: 1000, UserId: "u2", Message: "hi"}}
	foodPosts := []*model.Post{{Id: "p2", ChannelId: "food", CreateAt: 1000, UserId: "u2", Message: "yo"}}

	// Open the DM (uncached: store is nil → fetchPosts path). This bumps viewGen
	// to 1 and points openChannelID at the DM.
	m.openChannelLoadCmd("dm")
	if m.openChannelID != "dm" {
		t.Fatalf("openChannelID = %q; want dm", m.openChannelID)
	}
	dmGen := m.viewGen

	// The DM's posts arrive → schedules a mark-read tick at the current viewGen.
	next, _ := m.update(postsLoadedMsg{channelID: "dm", posts: dmPosts, users: map[string]string{"u2": "u2"}})
	m = next.(Model)

	// Switch to Food before the dwell elapses. This bumps viewGen again.
	m.openChannelLoadCmd("food")
	if m.openChannelID != "food" {
		t.Fatalf("openChannelID = %q; want food", m.openChannelID)
	}
	if m.viewGen == dmGen {
		t.Fatalf("viewGen did not advance on switch (still %d) — the dwell guard relies on it", m.viewGen)
	}
	next, _ = m.update(postsLoadedMsg{channelID: "food", posts: foodPosts, users: map[string]string{"u2": "u2"}})
	m = next.(Model)

	// Now the DM's dwell tick finally fires. It carries the DM's old viewGen.
	next, _ = m.update(markViewedMsg{channelID: "dm", gen: dmGen})
	m = next.(Model)

	if m.unread["dm"] != 3 {
		t.Fatalf("DM unread = %d; want 3 (switching to Food before the dwell must NOT mark the DM read)", m.unread["dm"])
	}
}

// Counterpart: if the DM stays the open channel until the dwell tick fires, it
// IS marked read. This is the configured behaviour (mark_read_delay_seconds),
// not a bug — confirms the difference is purely whether you switched in time.
func TestStayingOnDMUntilDwellMarksItRead(t *testing.T) {
	m := Model{
		me:            &model.User{Id: "me"},
		userNames:     map[string]string{"me": "me", "u2": "u2"},
		drafts:        map[string]string{},
		unread:        map[string]int{"dm": 3},
		mentions:      map[string]int{},
		markReadDelay: time.Second,
		// On the DMs tab (a channel-viewing tab), not a synthetic Feed/Search/SQL
		// tab, so the dwell is allowed to complete.
		hasDMs: true,
	}
	dmPosts := []*model.Post{{Id: "p1", ChannelId: "dm", CreateAt: 1000, UserId: "u2", Message: "hi"}}

	m.openChannelLoadCmd("dm")
	dmGen := m.viewGen
	next, _ := m.update(postsLoadedMsg{channelID: "dm", posts: dmPosts, users: map[string]string{"u2": "u2"}})
	m = next.(Model)

	// No switch — the dwell tick fires with the DM still open.
	next, _ = m.update(markViewedMsg{channelID: "dm", gen: dmGen})
	m = next.(Model)

	if _, ok := m.unread["dm"]; ok {
		t.Fatalf("DM still unread after the dwell elapsed while it stayed open; want cleared")
	}
	if !m.viewSettled {
		t.Fatalf("viewSettled = false; want true after the dwell")
	}
}

// Same as the first test but for the CACHED open path — what the user actually
// hits, since their DMs come from the local message store. The cached path
// paints immediately and schedules the dwell off postsGapFilledMsg (via
// fetchRecent) rather than postsLoadedMsg, so cover it explicitly.
func TestSwitchingAwayBeforeDwellKeepsCachedDMUnread(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "marks.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dmPosts := []*model.Post{{Id: "p1", ChannelId: "dm", UserId: "u2", CreateAt: 1000, UpdateAt: 1000, Message: "hi"}}
	if err := st.UpsertMany(dmPosts); err != nil { // makes the DM "cached"
		t.Fatalf("seed store: %v", err)
	}

	vp := viewport.New()
	vp.SoftWrap = true
	vp.SetWidth(80)
	vp.SetHeight(40)
	m := Model{
		store:         st,
		me:            &model.User{Id: "me"},
		userNames:     map[string]string{"me": "me", "u2": "u2"},
		drafts:        map[string]string{},
		unread:        map[string]int{"dm": 3, "food": 1},
		mentions:      map[string]int{},
		markReadDelay: time.Second,
		width:         100,
		height:        44,
		focus:         focusMessages,
		msgsView:      vp,
	}

	// Open the cached DM: loadFromStore paints, fetchRecent is returned (ignored
	// here). viewGen advances to the DM's generation.
	m.openChannelLoadCmd("dm")
	dmGen := m.viewGen
	// fetchRecent's result arrives as a gap-fill → schedules the dwell tick.
	next, _ := m.update(postsGapFilledMsg{channelID: "dm", posts: dmPosts, users: map[string]string{"u2": "u2"}})
	m = next.(Model)

	// Switch to Food before the dwell.
	m.openChannelLoadCmd("food")
	if m.viewGen == dmGen {
		t.Fatalf("viewGen did not advance on switch")
	}

	// DM dwell fires late, carrying the stale generation.
	next, _ = m.update(markViewedMsg{channelID: "dm", gen: dmGen})
	m = next.(Model)

	if m.unread["dm"] != 3 {
		t.Fatalf("cached DM unread = %d; want 3 (switching to Food before the dwell must NOT mark it read)", m.unread["dm"])
	}
}
