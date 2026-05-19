package ui

import (
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// switcherWidth is the rendered outer width of the popup. Falls back
// gracefully when the terminal is narrower (clamped in renderSwitcher).
const switcherWidth = 60

// switcherLimit caps how many matches we render — narrow enough to keep
// the popup short, generous enough for "go to <project>" muscle memory.
const switcherLimit = 12

// openSwitcher activates the global ctrl+k channel switcher. The
// textinput is reset (no stale query), focused so the cursor blinks,
// and selection lands on the first match.
func (m Model) openSwitcher() (tea.Model, tea.Cmd) {
	m.switcherMode = true
	m.switcher.SetValue("")
	m.switcher.Focus()
	m.switcherIdx = 0
	return m, nil
}

func (m *Model) closeSwitcher() {
	m.switcherMode = false
	m.switcher.SetValue("")
	m.switcher.Blur()
	m.switcherIdx = 0
}

// handleSwitcherKey owns every keystroke while the switcher is open.
func (m Model) handleSwitcherKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "ctrl+k":
		m.closeSwitcher()
		return m, nil
	case "up", "ctrl+p":
		if m.switcherIdx > 0 {
			m.switcherIdx--
		}
		return m, nil
	case "down", "ctrl+n":
		results := m.switcherResults()
		if m.switcherIdx < len(results)-1 {
			m.switcherIdx++
		}
		return m, nil
	case "enter":
		results := m.switcherResults()
		if len(results) == 0 || m.switcherIdx >= len(results) {
			return m, nil
		}
		ch := results[m.switcherIdx]
		m.closeSwitcher()
		// Hop to the channel's home team so isCurrentChannel keeps
		// tracking the open channel. Clear any channel filter so the
		// target is actually visible in the sidebar list.
		m.switchToChannelHomeTeam(ch)
		m.filterValue = ""
		m.filter.SetValue("")
		m.focus = focusInput
		m.posts = nil
		m.status = "loading messages…"
		m.renderMessages()
		return m, tea.Batch(m.input.Focus(), m.fetchPosts(ch.Id), m.bumpChannelStat(ch.Id))
	}
	old := m.switcher.Value()
	var cmd tea.Cmd
	m.switcher, cmd = m.switcher.Update(msg)
	if m.switcher.Value() != old {
		m.switcherIdx = 0
	}
	return m, cmd
}

// switcherResults returns up to switcherLimit channels matching the
// current query across every bucket (teams + DMs + group-DMs).
// Empty query lists everything (alphabetical).
func (m Model) switcherResults() []*model.Channel {
	needle := strings.ToLower(strings.TrimSpace(m.switcher.Value()))
	type match struct {
		ch    *model.Channel
		label string
		score int
	}
	var matches []match
	seen := map[string]bool{}
	for _, list := range m.channels {
		for _, c := range list {
			if seen[c.Id] {
				continue
			}
			seen[c.Id] = true
			label := m.channelLabel(c)
			score, ok := fuzzyScore(strings.ToLower(label), needle)
			if !ok {
				continue
			}
			matches = append(matches, match{ch: c, label: label, score: score})
		}
	}
	// tier ranks attention (lower = higher priority): mentions, then
	// plain unreads, then everything else. With an empty query every
	// fuzzyScore is 0 so tier dominates — so a bare ctrl+k surfaces
	// unread/mention channels first. With a typed query, score still
	// decides; tier only breaks ties.
	tier := func(ch *model.Channel) int {
		switch {
		case m.mentions[ch.Id] > 0:
			return 0
		case m.unread[ch.Id] > 0:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.score != b.score {
			return a.score < b.score
		}
		ta, tb := tier(a.ch), tier(b.ch)
		if ta != tb {
			return ta < tb
		}
		// Persisted usage: most-opened channels rank above never-opened
		// ones within the same tier. Recency breaks count ties so a
		// freshly-opened channel beats an equally-counted dormant one.
		ca, cb := m.openStats[a.ch.Id], m.openStats[b.ch.Id]
		if ca.OpenCount != cb.OpenCount {
			return ca.OpenCount > cb.OpenCount
		}
		if ca.LastOpened != cb.LastOpened {
			return ca.LastOpened > cb.LastOpened
		}
		return strings.ToLower(a.label) < strings.ToLower(b.label)
	})
	if len(matches) > switcherLimit {
		matches = matches[:switcherLimit]
	}
	out := make([]*model.Channel, 0, len(matches))
	for _, mt := range matches {
		out = append(out, mt.ch)
	}
	return out
}

// fuzzyScore returns (score, ok) where lower scores rank higher. Empty
// needle accepts everything with a neutral score. Substring matches
// (preferred) score by earliest position + haystack length; subsequence
// matches fall into a strictly worse band so substring hits always win.
func fuzzyScore(haystack, needle string) (int, bool) {
	if needle == "" {
		return 0, true
	}
	if i := strings.Index(haystack, needle); i >= 0 {
		return i*2 + (len(haystack) - len(needle)), true
	}
	hr := []rune(haystack)
	nr := []rune(needle)
	hi := 0
	gaps := 0
	for _, c := range nr {
		for hi < len(hr) && hr[hi] != c {
			hi++
			gaps++
		}
		if hi >= len(hr) {
			return 0, false
		}
		hi++
	}
	return 1000 + gaps, true
}

// teamHintForChannel returns a short label describing where the channel
// lives, shown dim next to the match. "DM" for direct/group, the team's
// display name otherwise.
func (m Model) teamHintForChannel(ch *model.Channel) string {
	if ch.Type == model.ChannelTypeDirect || ch.Type == model.ChannelTypeGroup {
		return "DM"
	}
	for _, t := range m.teams {
		if t.Id == ch.TeamId {
			return displayTeam(t)
		}
	}
	return ""
}

// renderSwitcher draws the centered popup: title, input row, separator,
// then up to switcherLimit result rows with mention/unread badges and a
// dim team-name suffix.
func (m Model) renderSwitcher() string {
	w := switcherWidth
	if cap := m.width - 4; cap > 0 && w > cap {
		w = cap
	}
	if w < 24 {
		w = 24
	}
	inner := w - 4 // outer border (2) + padding (2)
	if inner < 1 {
		inner = 1
	}

	dim := lipgloss.NewStyle().Foreground(dimColor)
	results := m.switcherResults()

	rows := []string{
		titleStyle.Render("Switch channel"),
		m.switcher.View(),
		dim.Render(strings.Repeat("─", inner)),
	}

	if len(results) == 0 {
		rows = append(rows, dim.Render("  no matches"))
	} else {
		for i, ch := range results {
			label := m.channelLabel(ch)
			team := m.teamHintForChannel(ch)

			mentionN := m.mentions[ch.Id]
			unreadN := m.unread[ch.Id]
			var badge string
			switch {
			case mentionN > 0:
				badge = mentionStyle.Render(" " + strconv.Itoa(mentionN) + "!")
			case unreadN > 0:
				badge = unreadStyle.Render(" " + strconv.Itoa(unreadN))
			}

			// Reserve space for: leading "  ", trailing badge, " <team>".
			teamSuffix := ""
			if team != "" {
				teamSuffix = "  " + team
			}
			reserved := 2 + lipgloss.Width(badge) + lipgloss.Width(teamSuffix)
			labelText := label
			if reserved < inner {
				labelText = truncate(label, inner-reserved)
			}
			switch {
			case mentionN > 0:
				labelText = mentionStyle.Render(labelText)
			case unreadN > 0:
				labelText = unreadStyle.Render(labelText)
			}
			line := "  " + labelText + badge
			if teamSuffix != "" {
				line += dim.Render(teamSuffix)
			}
			if i == m.switcherIdx {
				line = selectedRow.Width(inner).Render(line)
			}
			rows = append(rows, line)
		}
	}

	// lipgloss v2: Width() is the outer width (includes border + padding).
	// We want the popup to render at outer = w, so pass it directly.
	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Width(w).
		Render(strings.Join(rows, "\n"))
}
