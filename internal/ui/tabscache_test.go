package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// tabsModel is a Model with a couple of teams, laid out, so the strip has
// something to draw and something to cache.
func tabsModel() Model {
	m := benchBlobModel(120, 40)
	m.teams = []*model.Team{
		{Id: "t1", Name: "one", DisplayName: "One"},
		{Id: "t2", Name: "two", DisplayName: "Two"},
	}
	m.channels = map[string][]*model.Channel{
		"t1": {{Id: "c1", TeamId: "t1", DisplayName: "general", Type: model.ChannelTypeOpen}},
	}
	m.layoutPanes()
	return m
}

// TestTabStripCacheHitAndMiss: the memo has to be exactly as picky as the
// strip is. Anything that changes a glyph must miss it; a frame that changed
// nothing must hit it and still leave the click zones armed, since a hit skips
// the layout that writes them.
func TestTabStripCacheHitAndMiss(t *testing.T) {
	m := tabsModel()
	joins := []int{0, m.width - 1}

	first := m.renderTeamTabs(joins)
	if !m.vcache.tabs.valid {
		t.Fatal("the first render did not fill the memo")
	}
	zones := m.vcache.tabZones
	if len(zones) == 0 {
		t.Fatal("no tab zones were recorded")
	}

	m.vcache.tabZones = nil // as renderViewContent would, before a repaint
	if again := m.renderTeamTabs(joins); again != first {
		t.Error("an unchanged strip re-rendered differently")
	}
	if len(m.vcache.tabZones) != len(zones) {
		t.Error("a cache hit left the tab click zones unarmed")
	}

	for _, tc := range []struct {
		what   string
		change func(m *Model)
	}{
		{"active tab", func(m *Model) { gotoTab(m, tabSearch) }},
		{"width", func(m *Model) { m.width -= 7 }},
		{"hover", func(m *Model) { m.hover = hoverState{zone: hitTab, idx: 0} }},
		{"team renamed", func(m *Model) { m.teams[1].DisplayName = "Two and a half" }},
		{"feed badge", func(m *Model) { m.unread = map[string]int{"c1": 3} }},
		{"rule joins", func(m *Model) { joins = []int{0, 20, m.width - 1} }},
	} {
		before := m.renderTeamTabs(joins)
		tc.change(&m)
		if after := m.renderTeamTabs(joins); after == before {
			t.Errorf("%s: the strip did not change — the memo is stale", tc.what)
		}
	}
}
