package ui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
	"matterbox/internal/viewport"
)

// feedTop / searchTop are the screen rows where each pane's bubble viewport
// begins: the Feed stacks a title + rule above it (2 rows), the Search stacks a
// title, a 2-row input box and a rule (4 rows), both below the tab strip.
const (
	feedTop   = tabsHeight + 2
	searchTop = tabsHeight + 4
)

// feedMouseModel parks a model on the synthetic Feed tab (teamIdx 0, no DMs)
// with n unread bubbles, a sized viewport, and the results painted so the
// per-bubble click zones are populated. A messages viewport is wired up so an
// open (which hops to the channel) doesn't trip over a nil pane.
func feedMouseModel(n int) Model {
	fp := newFeedState()
	fp.view.SetWidth(76)
	fp.view.SetHeight(30)
	mvp := viewport.New()
	mvp.SoftWrap = true
	mvp.SetWidth(80)
	mvp.SetHeight(40)
	m := Model{
		keys:         newKeyMap("ctrl"),
		mouseEnabled: true,
		teams:        []*model.Team{{Id: "t1", DisplayName: "T1", Name: "t1"}},
		userNames:    map[string]string{"u": "alice"},
		unread:       map[string]int{},
		mentions:     map[string]int{},
		feed:         fp,
		msgsView:     mvp,
		me:           &model.User{Id: "me"},
		width:        100,
		height:       44,
	}
	chans := make([]*model.Channel, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		chans[i] = &model.Channel{Id: id, TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: fmt.Sprintf("ch%d", i)}
		m.unread[id] = 1
		m.feed.entries = append(m.feed.entries, feedEntry{
			channelID: id,
			unread:    []*model.Post{{Id: id + "p", ChannelId: id, CreateAt: int64(100 + i), UserId: "u", Message: "hello"}},
		})
	}
	m.channels = map[string][]*model.Channel{"t1": chans}
	m.feed.built = true
	m.teamIdx = 0 // Feed tab
	m.focus = focusFeed
	m.renderFeedResults()
	return m
}

// searchMouseModel is the Search-tab mirror of feedMouseModel: n hit bubbles on
// the synthetic Search tab (teamIdx 1, no DMs), results painted.
func searchMouseModel(n int) Model {
	sp := newSearchState(true)
	sp.view.SetWidth(76)
	sp.view.SetHeight(30)
	mvp := viewport.New()
	mvp.SoftWrap = true
	mvp.SetWidth(80)
	mvp.SetHeight(40)
	m := Model{
		keys:         newKeyMap("ctrl"),
		mouseEnabled: true,
		teams:        []*model.Team{{Id: "t1", DisplayName: "T1", Name: "t1"}},
		userNames:    map[string]string{"u": "alice"},
		unread:       map[string]int{},
		mentions:     map[string]int{},
		search:       sp,
		msgsView:     mvp,
		me:           &model.User{Id: "me"},
		width:        100,
		height:       44,
	}
	chans := make([]*model.Channel, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		chans[i] = &model.Channel{Id: id, TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: fmt.Sprintf("ch%d", i)}
		m.search.hits = append(m.search.hits, store.SearchHit{
			Match: &model.Post{Id: id + "p", ChannelId: id, CreateAt: int64(100 + i), UserId: "u", Message: "match"},
		})
	}
	m.channels = map[string][]*model.Channel{"t1": chans}
	m.search.query = "match"
	m.teamIdx = 1 // Search tab
	m.focus = focusSearch
	m.renderSearchResults()
	return m
}

// TestHitFeedBubbleMapsRows: each recorded bubble zone maps its top screen row
// back to that entry, and a click below the last bubble is over nothing.
func TestHitFeedBubbleMapsRows(t *testing.T) {
	m := feedMouseModel(3)
	if len(m.feed.zones) != 3 {
		t.Fatalf("feed zones=%d want 3", len(m.feed.zones))
	}
	for i, z := range m.feed.zones {
		y := feedTop + z.row0 // YOffset 0 at the top
		if h := m.hitTest(10, y); h.zone != hitFeed || h.idx != i {
			t.Errorf("hitTest(10,%d)=%v,%d want hitFeed,%d", y, h.zone, h.idx, i)
		}
	}
	if h := m.hitTest(10, feedTop+m.feed.zonesTotal); h.zone != hitNone {
		t.Errorf("click past the last bubble = %v want hitNone", h.zone)
	}
}

// TestClickFeedSelectsThenOpens: the first click on a bubble selects it (and
// focuses the feed) without opening; a click on the now-selected bubble opens
// its channel, dropping the entry and leaving the feed.
func TestClickFeedSelectsThenOpens(t *testing.T) {
	m := feedMouseModel(3)
	m.feed.idx = 0
	m.focus = focusTeams // prove the click takes focus

	y := feedTop + m.feed.zones[2].row0
	out, _ := m.handleMouseClick(click(tea.MouseLeft, 10, y))
	m = out.(Model)
	if m.feed.idx != 2 || m.focus != focusFeed {
		t.Fatalf("first click idx=%d focus=%v want 2,focusFeed", m.feed.idx, m.focus)
	}
	if len(m.feed.entries) != 3 {
		t.Fatalf("first click opened the entry: entries=%d want 3", len(m.feed.entries))
	}

	y = feedTop + m.feed.zones[2].row0 // re-render kept the same heights
	out, _ = m.handleMouseClick(click(tea.MouseLeft, 10, y))
	m = out.(Model)
	if len(m.feed.entries) != 2 {
		t.Fatalf("second click didn't open: entries=%d want 2", len(m.feed.entries))
	}
	if m.focus != focusMessages {
		t.Fatalf("second click focus=%v want focusMessages", m.focus)
	}
}

// TestHitSearchBubbleMapsRows mirrors TestHitFeedBubbleMapsRows for Search.
func TestHitSearchBubbleMapsRows(t *testing.T) {
	m := searchMouseModel(3)
	if len(m.search.zones) != 3 {
		t.Fatalf("search zones=%d want 3", len(m.search.zones))
	}
	for i, z := range m.search.zones {
		y := searchTop + z.row0
		if h := m.hitTest(10, y); h.zone != hitSearch || h.idx != i {
			t.Errorf("hitTest(10,%d)=%v,%d want hitSearch,%d", y, h.zone, h.idx, i)
		}
	}
}

// TestClickSearchSelectsThenOpens: first click selects a hit, second click on
// it opens its channel (centering the messages pane on the match).
func TestClickSearchSelectsThenOpens(t *testing.T) {
	m := searchMouseModel(3)
	m.search.idx = 0
	m.focus = focusTeams

	y := searchTop + m.search.zones[2].row0
	out, _ := m.handleMouseClick(click(tea.MouseLeft, 10, y))
	m = out.(Model)
	if m.search.idx != 2 || m.focus != focusSearch {
		t.Fatalf("first click idx=%d focus=%v want 2,focusSearch", m.search.idx, m.focus)
	}
	if m.openChannelID == "c2" {
		t.Fatal("first click opened the hit")
	}

	y = searchTop + m.search.zones[2].row0
	out, _ = m.handleMouseClick(click(tea.MouseLeft, 10, y))
	m = out.(Model)
	if m.focus != focusMessages {
		t.Fatalf("second click focus=%v want focusMessages", m.focus)
	}
	if m.openChannelID != "c2" {
		t.Fatalf("second click opened %q want c2", m.openChannelID)
	}
}

// TestClickSearchLoadMore: when the page came back full the load-more row is a
// click zone (idx == len(hits)); clicking it selects it, then a click on the
// selected row expands the search.
func TestClickSearchLoadMore(t *testing.T) {
	m := searchMouseModel(2)
	m.search.limit = 2 // len(hits) >= limit ⇒ hasMore
	m.renderSearchResults()
	if len(m.search.zones) != 3 {
		t.Fatalf("zones=%d want 3 (2 hits + load-more)", len(m.search.zones))
	}
	last := m.search.zones[2]
	if last.idx != len(m.search.hits) {
		t.Fatalf("load-more zone idx=%d want %d", last.idx, len(m.search.hits))
	}
	y := searchTop + last.row0
	if h := m.hitTest(10, y); h.zone != hitSearch || h.idx != 2 {
		t.Fatalf("hitTest on load-more=%v,%d want hitSearch,2", h.zone, h.idx)
	}

	m.focus = focusSearch
	m.search.idx = 0
	out, _ := m.handleMouseClick(click(tea.MouseLeft, 10, y))
	m = out.(Model)
	if m.search.idx != 2 {
		t.Fatalf("click didn't select load-more: idx=%d want 2", m.search.idx)
	}
	out, cmd := m.handleMouseClick(click(tea.MouseLeft, 10, y))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("click on the selected load-more row didn't expand the search")
	}
}

// TestWheelScrollsFeedAndSearch: the wheel free-scrolls each bubble viewport
// (like PageUp/Down), independent of the selection.
func TestWheelScrollsFeedAndSearch(t *testing.T) {
	mf := feedMouseModel(20) // overflow the height-30 viewport
	if off := mf.feed.view.YOffset(); off != 0 {
		t.Fatalf("feed precondition: YOffset=%d want 0", off)
	}
	mf = wheelOnce(mf, tea.MouseWheelDown)
	if mf.feed.view.YOffset() <= 0 {
		t.Fatalf("wheel didn't scroll the feed: YOffset=%d", mf.feed.view.YOffset())
	}
	if mf.feed.idx != 0 {
		t.Fatalf("wheel moved the feed selection: idx=%d want 0", mf.feed.idx)
	}

	ms := searchMouseModel(20)
	ms = wheelOnce(ms, tea.MouseWheelDown)
	if ms.search.view.YOffset() <= 0 {
		t.Fatalf("wheel didn't scroll the search results: YOffset=%d", ms.search.view.YOffset())
	}
}
