package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// runMessageStats ("> Message stats") lists the channels the user posted in
// most over the last week, counted from the local cache, in a text sheet.
func runMessageStats(m *Model, _ string) tea.Cmd {
	if m.store == nil || m.me == nil {
		m.status = "message stats need the local cache and a loaded user"
		return nil
	}
	since := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	rows, err := m.store.AuthoredCountsByChannel(m.me.Id, since, 0, 12)
	if err != nil {
		m.status = "message stats: " + oneLine(err.Error())
		return nil
	}
	if len(rows) == 0 {
		m.openTextPopup("Message stats", "No cached messages from you in the last 7 days.")
		return nil
	}
	var lines []string
	lines = append(lines, "Your most active channels in the last 7 days:")
	for _, row := range rows {
		label := row.ChannelID
		if c := m.findChannel(row.ChannelID); c != nil {
			label = m.channelLabel(c)
		}
		lines = append(lines, fmt.Sprintf("• %2d  %s", row.Count, label))
	}
	m.openTextPopup("Message stats", strings.Join(lines, "\n"))
	return nil
}
