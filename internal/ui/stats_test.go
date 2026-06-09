package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestChannelStatsRoundTrip verifies the per-team last-open map survives a
// write → load cycle alongside the existing stats and last-active record.
func TestChannelStatsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	stats := map[string]channelStat{"c1": {OpenCount: 3, LastOpened: 42}}
	la := &lastActive{TeamID: "t1", ChannelID: "c1"}
	lastByTeam := map[string]string{"t1": "c1", dmTeamID: "dm9"}

	if err := writeChannelStats(stats, la, lastByTeam); err != nil {
		t.Fatalf("writeChannelStats: %v", err)
	}

	gotStats, gotLA, gotByTeam := loadChannelStats()
	if gotStats["c1"].OpenCount != 3 {
		t.Errorf("stats round-trip: OpenCount = %d, want 3", gotStats["c1"].OpenCount)
	}
	if gotLA.teamID() != "t1" || gotLA.channelID() != "c1" {
		t.Errorf("lastActive round-trip = %+v, want {t1 c1}", gotLA)
	}
	if gotByTeam["t1"] != "c1" || gotByTeam[dmTeamID] != "dm9" {
		t.Errorf("lastChannelByTeam round-trip = %v, want {t1:c1, %s:dm9}", gotByTeam, dmTeamID)
	}
}

// TestLoadChannelStatsMissingFile returns empty, non-nil maps so callers can
// write to them without a nil-map panic.
func TestLoadChannelStatsMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	stats, la, byTeam := loadChannelStats()
	if stats == nil || byTeam == nil {
		t.Fatalf("missing file should yield non-nil maps, got stats=%v byTeam=%v", stats, byTeam)
	}
	if la != nil {
		t.Errorf("missing file should yield nil lastActive, got %+v", la)
	}
}

func TestBumpChannelStatRecordsPerTeam(t *testing.T) {
	t.Run("records under the focused real team", func(t *testing.T) {
		m := withTeams(threeTeams(), 1) // Design (t2) active
		m.bumpChannelStat("chanA")
		if got := m.lastChannelByTeam["t2"]; got != "chanA" {
			t.Fatalf("lastChannelByTeam[t2] = %q, want chanA", got)
		}
	})

	t.Run("skips the synthetic Unread tab", func(t *testing.T) {
		m := Model{teams: threeTeams()}
		m.teamIdx = 0 // Unread (first synthetic tab; no DMs)
		m.bumpChannelStat("chanA")
		if _, ok := m.lastChannelByTeam[unreadTeamID]; ok {
			t.Fatalf("synthetic Unread tab should not be recorded, map = %v", m.lastChannelByTeam)
		}
	})
}

func TestPreferredChannelIdx(t *testing.T) {
	vis := []*model.Channel{{Id: "cA"}, {Id: "cB"}, {Id: "cC"}}

	cases := []struct {
		name       string
		remembered map[string]string
		want       int
	}{
		{"reopens remembered channel", map[string]string{"t1": "cB"}, 1},
		{"no memory falls back to first", nil, 0},
		{"stale channel falls back to first", map[string]string{"t1": "gone"}, 0},
		{"other team's memory is ignored", map[string]string{"t2": "cC"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := withTeams(threeTeams(), 0) // Engineering (t1) active
			m.lastChannelByTeam = tc.remembered
			if got := m.preferredChannelIdx(vis); got != tc.want {
				t.Fatalf("preferredChannelIdx() = %d, want %d", got, tc.want)
			}
		})
	}
}
