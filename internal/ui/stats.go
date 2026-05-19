package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
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
}

func channelStatsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "matterbox", "channel_stats.json"), nil
}

// loadChannelStats reads the persisted stats. Missing file / parse
// errors are silently swallowed — usage tracking is a nice-to-have, not
// load-bearing, and the user shouldn't see startup errors over it.
func loadChannelStats() (map[string]channelStat, *lastActive) {
	p, err := channelStatsPath()
	if err != nil {
		return map[string]channelStat{}, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return map[string]channelStat{}, nil
	}
	var f channelStatsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return map[string]channelStat{}, nil
	}
	if f.Stats == nil {
		f.Stats = map[string]channelStat{}
	}
	return f.Stats, f.LastActive
}

// writeChannelStats persists the given map atomically: write to a tmp
// file in the same dir, then rename. Same-fs rename is atomic on Linux.
func writeChannelStats(stats map[string]channelStat, la *lastActive) error {
	p, err := channelStatsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(channelStatsFile{Stats: stats, LastActive: la}, "", "  ")
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

	m.lastActiveTeamID = m.currentTeamID()
	m.lastActiveChannelID = channelID

	// Snapshot the map so the background write isn't racing the live
	// map while another bump happens.
	snapshot := make(map[string]channelStat, len(m.openStats))
	for k, v := range m.openStats {
		snapshot[k] = v
	}
	la := &lastActive{TeamID: m.lastActiveTeamID, ChannelID: m.lastActiveChannelID}
	return func() tea.Msg {
		_ = writeChannelStats(snapshot, la)
		return nil
	}
}
