package ui

import (
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
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

// openSwitcher activates the global ctrl+p channel switcher. The
// textinput is reset (no stale query), focused so the cursor blinks,
// and selection lands on the first match.
func (m Model) openSwitcher() (tea.Model, tea.Cmd) {
	m.switcherMode = true
	m.switcher.SetValue("")
	m.switcher.Prompt = "> "
	m.switcher.Placeholder = "switch to channel or > for commands…"
	m.switcher.Focus()
	m.switcherIdx = 0
	m.switcherCmdPending = nil
	return m, nil
}

// openCommandPicker activates the switcher straight into "> command" mode,
// with the ">" prefix pre-filled (the F1 shortcut) so the command catalogue
// shows immediately — equivalent to opening the switcher and typing ">".
func (m Model) openCommandPicker() (tea.Model, tea.Cmd) {
	m.switcherMode = true
	m.switcher.SetValue(">")
	m.switcher.CursorEnd()
	m.switcher.Placeholder = "switch to channel or > for commands…"
	m.switcher.Focus()
	m.switcherIdx = 0
	m.switcherCmdPending = nil
	m.syncSwitcherPrompt() // value starts with ">", so drop the "> " prompt
	return m, nil
}

func (m *Model) closeSwitcher() {
	m.switcherMode = false
	m.switcher.SetValue("")
	m.switcher.Prompt = "> "
	m.switcher.Placeholder = "switch to channel or > for commands…"
	m.switcher.Blur()
	m.switcherIdx = 0
	m.switcherCmdPending = nil
}

// handleSwitcherKey owns every keystroke while the switcher is open.
// Three sub-modes coexist behind the same popup: normal channel switch,
// "> command" list, and a captive arg-prompt for a selected command.
func (m Model) handleSwitcherKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c": // hardwired quit
		return m, tea.Quit
	case msg.String() == "esc": // hardwired modal cancel
		if m.inCommandArgMode() {
			m.leaveCommandArgMode()
			return m, nil
		}
		m.closeSwitcher()
		return m, nil
	case key.Matches(msg, m.keys.Switcher):
		// ctrl+p opened the switcher; pressing it again toggles it closed.
		// (It used to be ctrl+k, which is now "prev channel" in the global nav.)
		// Checked before InputUp so it wins ctrl+p (a whitelisted shadow).
		m.closeSwitcher()
		return m, nil
	case key.Matches(msg, m.keys.InputUp):
		if m.inCommandArgMode() {
			// Captive arg-prompt: no list to navigate.
			return m, nil
		}
		if m.switcherIdx > 0 {
			m.switcherIdx--
		}
		return m, nil
	case key.Matches(msg, m.keys.InputDown):
		if m.inCommandArgMode() {
			return m, nil
		}
		var max int
		if m.inCommandMode() {
			max = len(m.commandResults())
		} else {
			max = len(m.switcherResults())
		}
		if m.switcherIdx < max-1 {
			m.switcherIdx++
		}
		return m, nil
	case key.Matches(msg, m.keys.OpenChannel):
		if m.inCommandArgMode() {
			cmd := *m.switcherCmdPending
			arg := m.switcher.Value()
			m.closeSwitcher()
			return m, cmd.run(&m, arg)
		}
		if m.inCommandMode() {
			results := m.commandResults()
			if len(results) == 0 || m.switcherIdx >= len(results) {
				return m, nil
			}
			sel := results[m.switcherIdx]
			if sel.argPrompt == "" {
				m.closeSwitcher()
				return m, sel.run(&m, "")
			}
			m.enterCommandArgMode(sel)
			return m, nil
		}
		results := m.switcherResults()
		if len(results) == 0 || m.switcherIdx >= len(results) {
			return m, nil
		}
		ch := results[m.switcherIdx]
		m.closeSwitcher()
		// The channel-info panel describes the channel it was opened for; close
		// it when jumping elsewhere so it can't show stale info.
		if m.infoOpen && ch.Id != m.infoChannelID {
			m.closeInfo()
		}
		// Hop to the channel's home team so isCurrentChannel keeps
		// tracking the open channel. Clear any channel filter so the
		// target is actually visible in the sidebar list.
		m.switchToChannelHomeTeam(ch)
		m.filterValue = ""
		m.filter.SetValue("")
		// The switcher (ctrl+p) is a deliberate "jump there and start typing"
		// action, so land focus in the composer. (Unlike enter on the sidebar,
		// which stays in the channel list so navigation keys keep working.)
		m.focus = focusInput
		// Stash the open channel's draft and restore the target's before
		// repointing openChannelID (this path bypasses openChannelLoadCmd).
		draftCmd := m.swapChannelDraft(ch.Id)
		m.openChannelID = ch.Id
		// New focus session: start a fresh mark-read dwell (this path
		// doesn't go through openChannelLoadCmd).
		m.viewGen++
		m.viewSettled = false
		m.posts = nil
		m.status = "loading messages…"
		m.renderMessages()
		return m, tea.Batch(draftCmd, m.input.Focus(), m.fetchPosts(ch.Id), m.bumpChannelStat(ch.Id))
	}
	old := m.switcher.Value()
	var cmd tea.Cmd
	m.switcher, cmd = m.switcher.Update(msg)
	if m.switcher.Value() != old {
		m.switcherIdx = 0
	}
	// Keep the textinput's prompt in sync with the current mode so the
	// visible characters are exactly what the user typed (no double
	// "> " when in command mode).
	m.syncSwitcherPrompt()
	return m, cmd
}

// switcherResults returns up to switcherLimit channels matching the
// current query across every bucket (teams + DMs + group-DMs).
// Empty query lists everything (alphabetical).
func (m *Model) switcherResults() []*model.Channel {
	needle := strings.ToLower(strings.TrimSpace(m.switcher.Value()))
	type match struct {
		ch    *model.Channel
		label string
		band  int
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
			band, score, ok := fuzzyScore(strings.ToLower(label), needle)
			if !ok {
				continue
			}
			matches = append(matches, match{ch: c, label: label, band: band, score: score})
		}
	}
	// tier ranks attention (lower = higher priority): mentions, then
	// plain unreads, then everything else.
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
	// Ordering keys, in priority order. The first three are the same
	// attention/usage ranking a bare ctrl+p shows; the only change while
	// filtering is that match quality is now coarse (band) instead of a
	// fine score, so it stops dominating. That way typing a name still
	// jumps to the strongest textual match, but among comparable matches
	// the unread / most-used channel wins instead of whichever happened to
	// match at an earlier position.
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		// 1. Coarse match quality: exact > prefix > substring > subsequence.
		if a.band != b.band {
			return a.band < b.band
		}
		// 2. Attention: mentions, then unreads.
		ta, tb := tier(a.ch), tier(b.ch)
		if ta != tb {
			return ta < tb
		}
		// 3. Persisted usage: most-opened channels rank above never-opened
		// ones. Recency breaks count ties so a freshly-opened channel beats
		// an equally-counted dormant one.
		ca, cb := m.openStats[a.ch.Id], m.openStats[b.ch.Id]
		if ca.OpenCount != cb.OpenCount {
			return ca.OpenCount > cb.OpenCount
		}
		if ca.LastOpened != cb.LastOpened {
			return ca.LastOpened > cb.LastOpened
		}
		// 4. Finer match position, then label, as a stable last resort.
		if a.score != b.score {
			return a.score < b.score
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

// fuzzyScore returns (band, score, ok). band is the coarse match-quality
// tier (lower = better): 0 exact, 1 prefix, 2 interior substring, 3
// subsequence. It's the primary ranking key so a stronger textual match
// always wins. score is a finer within-band discriminator (lower = better):
// earliest position + haystack length for substrings, gap count for
// subsequences. An empty needle accepts everything in band 0 with a neutral
// score, so a bare list is ordered entirely by attention/usage.
func fuzzyScore(haystack, needle string) (band, score int, ok bool) {
	if needle == "" {
		return 0, 0, true
	}
	if i := strings.Index(haystack, needle); i >= 0 {
		switch {
		case len(haystack) == len(needle):
			band = 0 // exact
		case i == 0:
			band = 1 // prefix
		default:
			band = 2 // interior substring
		}
		return band, i*2 + (len(haystack) - len(needle)), true
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
			return 0, 0, false
		}
		hi++
	}
	return 3, gaps, true
}

// teamHintForChannel returns a short label describing where the channel
// lives, shown dim next to the match. "DM" for direct/group, the team's
// display name otherwise.
func (m *Model) teamHintForChannel(ch *model.Channel) string {
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
// dim team-name suffix. maxH is the body height it's centered in, used to
// bound the (two-line) command list so it never pushes the footer off-screen.
func (m *Model) renderSwitcher(maxH int) string {
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

	// Sub-modes have their own renderers; the channel switcher below is
	// the default path.
	if m.inCommandArgMode() {
		return m.renderSwitcherArgPrompt(w, inner)
	}
	if m.inCommandMode() {
		return m.renderSwitcherCommands(w, inner, maxH)
	}

	dim := lipgloss.NewStyle().Foreground(dimColor)
	results := m.switcherResults()

	rows := []string{
		titleStyle.Render("Switch channel"),
		m.switcher.View(),
		dim.Render(strings.Repeat("─", inner)),
	}

	if len(results) == 0 {
		rows = append(rows, dim.Render("  no matches  (type > for commands)"))
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
			selected := i == m.switcherIdx
			switch {
			case mentionN > 0:
				labelText = mentionStyle.Render(labelText)
			case unreadN > 0:
				labelText = unreadStyle.Render(labelText)
			}
			line := "  " + labelText + badge
			if teamSuffix != "" {
				// On the selected row, leave the team suffix unstyled so the
				// selectedRow foreground applies — dim grey on the highlight
				// background is unreadable.
				if selected {
					line += teamSuffix
				} else {
					line += dim.Render(teamSuffix)
				}
			}
			if selected {
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

// renderSwitcherCommands renders the popup for "> command" mode: the
// title flips to "Run command" and the result list is the registered
// command catalogue (filtered by anything after the ">").
func (m *Model) renderSwitcherCommands(w, inner, maxH int) string {
	dim := lipgloss.NewStyle().Foreground(dimColor)
	results := m.commandResults()

	rows := []string{
		titleStyle.Render("Run command"),
		m.switcher.View(),
		dim.Render(strings.Repeat("─", inner)),
	}

	if len(results) == 0 {
		rows = append(rows, dim.Render("  no matching commands"))
	} else {
		// Each command renders as a name line plus its description on its own
		// indented line below — far roomier than squeezing both onto one row.
		// That doubles the list height, so window it around the selection to
		// keep the popup within maxH (overflow would shove the footer off the
		// bottom). Chrome inside the box is the title + input + separator (3)
		// plus the border (2); the rest is two rows per command.
		visible := maxH - 5
		if visible < 2 {
			visible = 2
		}
		perPage := visible / 2
		if perPage < 1 {
			perPage = 1
		}
		start := 0
		if len(results) > perPage {
			// Keep the selected row roughly centered, clamped to the ends.
			start = m.switcherIdx - perPage/2
			if start < 0 {
				start = 0
			}
			if start > len(results)-perPage {
				start = len(results) - perPage
			}
		}
		end := start + perPage
		if end > len(results) {
			end = len(results)
		}
		for i := start; i < end; i++ {
			c := results[i]
			selected := i == m.switcherIdx
			block := "  " + truncate(c.name, inner-2)
			if c.desc != "" {
				// On the selected row, skip the dim style so the selectedRow
				// foreground applies — dim grey on the highlight background is
				// unreadable.
				desc := truncate(c.desc, inner-4)
				if selected {
					block += "\n    " + desc
				} else {
					block += "\n    " + dim.Render(desc)
				}
			}
			if selected {
				// Width() pads both lines of the block so the highlight spans
				// the full row.
				block = selectedRow.Width(inner).Render(block)
			}
			rows = append(rows, block)
		}
	}

	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Width(w).
		Render(strings.Join(rows, "\n"))
}

// renderSwitcherArgPrompt renders the captive argument-entry view that
// appears after a command with an argPrompt is selected.
func (m *Model) renderSwitcherArgPrompt(w, inner int) string {
	dim := lipgloss.NewStyle().Foreground(dimColor)
	title := titleStyle.Render(m.switcherCmdPending.name)
	rows := []string{
		title,
		m.switcher.View(),
		dim.Render(strings.Repeat("─", inner)),
		dim.Render("  enter to run · esc to go back"),
	}
	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Width(w).
		Render(strings.Join(rows, "\n"))
}
