package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/config"
)

// channelStat is the persisted per-channel usage record. open_count is
// the total number of explicit opens (enter on the channel list, or
// selection from the ctrl+k switcher); last_opened is the unix-milli
// timestamp of the most recent bump. We track explicit selections only
// — auto-loads (team switch, initial load, jump-to-unread) don't count
// as "I want this channel" signals.
type channelStat struct {
	OpenCount  int   `json:"open_count"`
	LastOpened int64 `json:"last_opened"`
}

// lastActive records the last team and channel the user had open so we
// can restore the session on restart.
type lastActive struct {
	TeamID    string `json:"team_id"`
	ChannelID string `json:"channel_id"`
}

func (la *lastActive) teamID() string {
	if la == nil {
		return ""
	}
	return la.TeamID
}

func (la *lastActive) channelID() string {
	if la == nil {
		return ""
	}
	return la.ChannelID
}

type channelStatsFile struct {
	Stats      map[string]channelStat `json:"stats"`
	LastActive *lastActive            `json:"last_active,omitempty"`
	// LastChannelByTeam maps a persistent team bucket (real team or the
	// synthetic DMs bucket) to the last channel explicitly opened there,
	// so switching to a team reopens that channel instead of defaulting
	// to the first one. Synthetic tabs (Unread/Search/Feed) are excluded.
	LastChannelByTeam map[string]string `json:"last_channel_by_team,omitempty"`
}

func channelStatsPath() (string, error) {
	return config.File("channel_stats.json")
}

// loadChannelStats reads the persisted stats. Missing file / parse
// errors are silently swallowed — usage tracking is a nice-to-have, not
// load-bearing, and the user shouldn't see startup errors over it.
func loadChannelStats() (map[string]channelStat, *lastActive, map[string]string) {
	p, err := channelStatsPath()
	if err != nil {
		return map[string]channelStat{}, nil, map[string]string{}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return map[string]channelStat{}, nil, map[string]string{}
	}
	var f channelStatsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return map[string]channelStat{}, nil, map[string]string{}
	}
	if f.Stats == nil {
		f.Stats = map[string]channelStat{}
	}
	if f.LastChannelByTeam == nil {
		f.LastChannelByTeam = map[string]string{}
	}
	return f.Stats, f.LastActive, f.LastChannelByTeam
}

// writeChannelStats persists the given map atomically: write to a tmp
// file in the same dir, then rename. Same-fs rename is atomic on Linux.
func writeChannelStats(stats map[string]channelStat, la *lastActive, lastByTeam map[string]string) error {
	p, err := channelStatsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(channelStatsFile{Stats: stats, LastActive: la, LastChannelByTeam: lastByTeam}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "channel_stats-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, p)
}

// bumpChannelStat increments the in-memory count and timestamps the open.
// Returns a Cmd that persists the snapshot off the UI goroutine so the
// rename can't stall a keystroke.
func (m *Model) bumpChannelStat(channelID string) tea.Cmd {
	if channelID == "" {
		return nil
	}
	if m.openStats == nil {
		m.openStats = map[string]channelStat{}
	}
	s := m.openStats[channelID]
	s.OpenCount++
	s.LastOpened = time.Now().UnixMilli()
	m.openStats[channelID] = s

	teamID := m.currentTeamID()
	m.lastActiveTeamID = teamID
	m.lastActiveChannelID = channelID

	// Remember the last channel opened per persistent team bucket so a
	// later team switch can reopen it. The synthetic Search/Feed tabs
	// aren't restorable, so don't pollute the map with them — by the time
	// we reach here a channel opened from the Feed tab has already hopped
	// to its home team (switchToChannelHomeTeam), so teamID is real.
	if m.lastChannelByTeam == nil {
		m.lastChannelByTeam = map[string]string{}
	}
	if isRestorableTeamID(teamID) {
		m.lastChannelByTeam[teamID] = channelID
	}

	// Snapshot the maps so the background write isn't racing the live
	// maps while another bump happens.
	snapshot := make(map[string]channelStat, len(m.openStats))
	for k, v := range m.openStats {
		snapshot[k] = v
	}
	lastByTeam := make(map[string]string, len(m.lastChannelByTeam))
	for k, v := range m.lastChannelByTeam {
		lastByTeam[k] = v
	}
	la := &lastActive{TeamID: m.lastActiveTeamID, ChannelID: m.lastActiveChannelID}
	return func() tea.Msg {
		_ = writeChannelStats(snapshot, la, lastByTeam)
		return nil
	}
}
