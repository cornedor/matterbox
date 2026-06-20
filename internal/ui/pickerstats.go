package ui

import (
	"encoding/json"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
)

// pickerStatsFile is the persisted popularity record for the autocomplete
// pickers. Emoji counts a shortcode every time it's accepted from the `:`
// picker or chosen in the reaction picker; Mention counts a username every
// time it's accepted from the `@` picker. Both are keyed by the stable
// display token (the bare emoji shortcode / the username), so the maps
// survive even when the underlying ids change.
type pickerStatsFile struct {
	Emoji   map[string]int `json:"emoji,omitempty"`
	Mention map[string]int `json:"mention,omitempty"`
}

func pickerStatsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "matterbox", "picker_stats.json"), nil
}

// loadPickerStats reads the persisted popularity maps. Like channel stats,
// a missing file or parse error degrades silently to empty maps — picker
// weighting is a nicety, not load-bearing, so the user never sees a startup
// error over it.
func loadPickerStats() (emoji, mention map[string]int) {
	emoji = map[string]int{}
	mention = map[string]int{}
	p, err := pickerStatsPath()
	if err != nil {
		return emoji, mention
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return emoji, mention
	}
	var f pickerStatsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return emoji, mention
	}
	if f.Emoji != nil {
		emoji = f.Emoji
	}
	if f.Mention != nil {
		mention = f.Mention
	}
	return emoji, mention
}

// writePickerStats persists both maps atomically (tmp file + same-dir
// rename), mirroring writeChannelStats.
func writePickerStats(emoji, mention map[string]int) error {
	p, err := pickerStatsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(pickerStatsFile{Emoji: emoji, Mention: mention}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "picker_stats-*.tmp")
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

// persistPickerStats snapshots both usage maps and returns a Cmd that writes
// them off the UI goroutine, so the rename can't stall a keystroke. Snapshots
// are taken on the caller's goroutine so the background write can't race a
// later bump.
func (m *Model) persistPickerStats() tea.Cmd {
	emoji := make(map[string]int, len(m.emojiUsage))
	for k, v := range m.emojiUsage {
		emoji[k] = v
	}
	mention := make(map[string]int, len(m.mentionUsage))
	for k, v := range m.mentionUsage {
		mention[k] = v
	}
	return func() tea.Msg {
		_ = writePickerStats(emoji, mention)
		return nil
	}
}

// bumpEmojiStat records one more selection of the given emoji shortcode
// (bare, no colons) and returns the persist Cmd. The in-memory bump is
// immediate so the very next picker open already reflects it.
func (m *Model) bumpEmojiStat(name string) tea.Cmd {
	if name == "" {
		return nil
	}
	if m.emojiUsage == nil {
		m.emojiUsage = map[string]int{}
	}
	m.emojiUsage[name]++
	return m.persistPickerStats()
}

// bumpMentionStat records one more selection of the given username and
// returns the persist Cmd.
func (m *Model) bumpMentionStat(username string) tea.Cmd {
	if username == "" {
		return nil
	}
	if m.mentionUsage == nil {
		m.mentionUsage = map[string]int{}
	}
	m.mentionUsage[username]++
	return m.persistPickerStats()
}
