package ui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Joining a channel. Unlike the other channel commands this one can't take an
// argument and be done with it — you have to see what's there — so the "Join a
// channel" palette entry (F1) opens a browsable catalogue of the team's public
// channels minus the ones you're already in, filtered as you type. Private
// channels are absent by design: you can only get into one by invitation.
//
// The catalogue is fetched per team and cached for the modal's lifetime, so
// paging through teams with tab doesn't refetch what's already on screen.

// joinListHeight is how many channel rows the list shows at once; longer
// catalogues scroll a window around the selection.
const joinListHeight = 10

// joinDialogWidth is the modal's preferred outer width.
const joinDialogWidth = 68
const joinMinDialogWidth = 40

// joinChannelState owns the modal. Boxed on Model (nil when closed): the
// catalogue and the filter input are cold, fat state.
type joinChannelState struct {
	teams   []*model.Team // snapshot at open, so the tab order is stable
	teamIdx int

	filter textinput.Model
	idx    int // selection into the filtered list

	// cache holds the joinable channels per team id, so switching back to a
	// team already browsed doesn't refetch. A team with no joinable channels
	// caches an empty (non-nil) slice, which is what distinguishes "fetched,
	// nothing to join" from "not fetched yet".
	cache map[string][]*model.Channel

	// gen rises on every fetch so a slow response for a team the user has
	// already tabbed away from is dropped rather than shown.
	gen     int
	loading bool
	errMsg  string
	joining bool
}

// runJoinChannel is the > command entry point. The switcher has already closed
// itself, so this just raises the modal.
func runJoinChannel(m *Model, _ string) tea.Cmd {
	return m.openJoinChannel()
}

// openJoinChannel inflates the modal on the focused team (falling back to any
// real team, since the DMs/Feed/Search tabs have none of their own) and starts
// the first catalogue fetch.
func (m *Model) openJoinChannel() tea.Cmd {
	if len(m.teams) == 0 {
		m.status = "join channel: you don't belong to any team"
		return nil
	}
	st := &joinChannelState{
		teams: append([]*model.Team(nil), m.teams...),
		cache: map[string][]*model.Channel{},
	}
	target := m.fallbackTeamID()
	for i, t := range st.teams {
		if t.Id == target {
			st.teamIdx = i
			break
		}
	}
	ti := textinput.New()
	ti.Prompt = "filter: "
	ti.Placeholder = "channel name"
	ti.SetWidth(joinValueWidth(m.width))
	st.filter = ti

	m.joinChan = st
	return tea.Batch(st.filter.Focus(), m.fetchJoinableChannels())
}

// closeJoinChannel tears the modal down. Safe to call when it isn't open.
func (m *Model) closeJoinChannel() {
	m.joinChan = nil
}

// joinDims returns the modal's outer and inner width, and the width left for
// the filter input.
func joinDims(termWidth int) (outer, inner, value int) {
	outer = joinDialogWidth
	if cap := termWidth - 4; cap > 0 && outer > cap {
		outer = cap
	}
	if outer < joinMinDialogWidth {
		outer = joinMinDialogWidth
	}
	inner = outer - 8 // border (2) + padding (6)
	// The filter's "filter: " prompt (8) plus the cell its cursor parks in past
	// the last character — without that the input renders one column wider than
	// the box and pushes the border out.
	value = inner - 9
	if value < 1 {
		value = 1
	}
	return outer, inner, value
}

func joinValueWidth(termWidth int) int {
	_, _, v := joinDims(termWidth)
	return v
}

// joinTeamID is the team currently being browsed.
func (st *joinChannelState) joinTeamID() string {
	return st.teams[st.teamIdx].Id
}

// ---- catalogue -----------------------------------------------------------

// publicChannelsMsg carries a team's public-channel catalogue. gen identifies
// the fetch, so a response for a team the user has tabbed away from is dropped.
type publicChannelsMsg struct {
	gen      int
	teamID   string
	channels []*model.Channel
	err      error
}

// fetchJoinableChannels loads the current team's catalogue, unless it's already
// cached.
func (m *Model) fetchJoinableChannels() tea.Cmd {
	st := m.joinChan
	teamID := st.joinTeamID()
	if _, ok := st.cache[teamID]; ok {
		st.loading, st.errMsg = false, ""
		return nil
	}
	st.gen++
	st.loading, st.errMsg = true, ""
	gen := st.gen
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		chans, err := client.PublicChannelsForTeam(ctx, teamID)
		return publicChannelsMsg{gen: gen, teamID: teamID, channels: chans, err: err}
	}
}

// applyPublicChannels caches the fetched catalogue minus the channels the user
// is already in — those are what the ctrl+p switcher is for, and offering to
// "join" them would be a no-op.
func (m Model) applyPublicChannels(msg publicChannelsMsg) (tea.Model, tea.Cmd) {
	st := m.joinChan
	if st == nil || msg.gen != st.gen {
		return m, nil // modal closed, or the user tabbed on
	}
	st.loading = false
	if msg.err != nil {
		st.errMsg = oneLine(msg.err.Error())
		return m, nil
	}
	joinable := make([]*model.Channel, 0, len(msg.channels))
	for _, c := range msg.channels {
		if m.findChannel(c.Id) == nil {
			joinable = append(joinable, c)
		}
	}
	st.cache[msg.teamID] = joinable
	st.idx = 0
	return m, nil
}

// joinResults returns the browsable channels for the current team, narrowed by
// the filter (matched against both the display name and the URL slug, since the
// list shows both).
func (st *joinChannelState) joinResults() []*model.Channel {
	all := st.cache[st.joinTeamID()]
	q := strings.ToLower(strings.TrimSpace(st.filter.Value()))
	if q == "" {
		return all
	}
	out := make([]*model.Channel, 0, len(all))
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.DisplayName), q) || strings.Contains(strings.ToLower(c.Name), q) {
			out = append(out, c)
		}
	}
	return out
}

// ---- key handling --------------------------------------------------------

// handleJoinChannelKey owns every keystroke while the modal is open. The filter
// input keeps focus throughout, so tab (not ←/→, which the input needs for its
// cursor) is what cycles teams.
func (m Model) handleJoinChannelKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	st := m.joinChan
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeJoinChannel()
		return m, nil
	case "enter":
		return m.submitJoinChannel()
	case "up", "ctrl+p":
		st.moveJoinIdx(-1)
		return m, nil
	case "down", "ctrl+n":
		st.moveJoinIdx(1)
		return m, nil
	case "tab":
		return m, m.cycleJoinTeam(1)
	case "shift+tab":
		return m, m.cycleJoinTeam(-1)
	}

	before := st.filter.Value()
	var cmd tea.Cmd
	st.filter, cmd = st.filter.Update(msg)
	if st.filter.Value() != before {
		st.idx = 0 // the list under the cursor just changed
	}
	return m, cmd
}

// moveJoinIdx steps the selection, clamped to the filtered list.
func (st *joinChannelState) moveJoinIdx(delta int) {
	n := len(st.joinResults())
	if n == 0 {
		st.idx = 0
		return
	}
	st.idx = min(max(st.idx+delta, 0), n-1)
}

// cycleJoinTeam moves to the next/previous team and fetches its catalogue if
// it hasn't been browsed yet.
func (m *Model) cycleJoinTeam(delta int) tea.Cmd {
	st := m.joinChan
	if len(st.teams) < 2 {
		return nil
	}
	st.teamIdx = (st.teamIdx + delta + len(st.teams)) % len(st.teams)
	st.idx = 0
	return m.fetchJoinableChannels()
}

// ---- join ----------------------------------------------------------------

// channelJoinedMsg carries the outcome of a join. ch is the channel that was
// joined; err is set when the server refused (e.g. the channel was archived out
// from under the catalogue).
type channelJoinedMsg struct {
	ch  *model.Channel
	err error
}

// submitJoinChannel joins the selected channel. Mattermost has no join
// endpoint: joining a public channel is adding yourself to it.
func (m Model) submitJoinChannel() (tea.Model, tea.Cmd) {
	st := m.joinChan
	if st.joining {
		return m, nil
	}
	if m.me == nil {
		st.errMsg = "user not loaded yet"
		return m, nil
	}
	results := st.joinResults()
	if len(results) == 0 || st.idx >= len(results) {
		return m, nil
	}
	ch := results[st.idx]
	st.joining = true
	st.errMsg = ""
	meID := m.me.Id
	client, ctx := m.client, m.ctx
	return m, func() tea.Msg {
		if err := client.AddChannelMember(ctx, ch.Id, meID); err != nil {
			return channelJoinedMsg{ch: ch, err: err}
		}
		return channelJoinedMsg{ch: ch}
	}
}

// applyChannelJoined closes the modal and opens the channel we just joined. A
// failure keeps the modal open with the server's message.
func (m Model) applyChannelJoined(msg channelJoinedMsg) (tea.Model, tea.Cmd) {
	m.recordFeature("channel_join", "palette", noLatency, 0, msg.err)
	if msg.err != nil {
		if m.joinChan == nil {
			m.status = "join channel: " + oneLine(msg.err.Error())
			return m, nil
		}
		m.joinChan.joining = false
		m.joinChan.errMsg = oneLine(msg.err.Error())
		return m, nil
	}
	m.closeJoinChannel()
	cmd := m.adoptChannel(msg.ch)
	m.status = "joined " + m.channelLabel(msg.ch)
	return m, cmd
}

// ---- render --------------------------------------------------------------

// renderJoinChannel draws the catalogue: the team being browsed, the filter,
// and a window of channels around the selection.
func (m *Model) renderJoinChannel() string {
	st := m.joinChan
	if st == nil {
		return ""
	}
	_, inner, valueW := joinDims(m.width)
	if st.filter.Width() != valueW {
		st.filter.SetWidth(valueW)
	}

	dim := lipgloss.NewStyle().Foreground(dimColor)
	hint := lipgloss.NewStyle().Foreground(dimColor).Italic(true)

	teamRow := lipgloss.NewStyle().MaxWidth(inner).Render(
		dim.Render("‹ ") +
			lipgloss.NewStyle().Bold(true).Render(truncate(displayTeam(st.teams[st.teamIdx]), inner-8)) +
			dim.Render(" ›"))

	body := []string{
		lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).Render("Join a channel"),
		"",
		teamRow,
		lipgloss.NewStyle().MaxWidth(inner).Render(st.filter.View()),
		"",
	}
	body = append(body, m.renderJoinList(inner)...)

	footer := "↑↓ select · ↵ join · tab team · esc cancel"
	if st.joining {
		footer = "joining…"
	}
	body = append(body, "")
	if st.errMsg != "" {
		body = append(body, lipgloss.NewStyle().Foreground(lipgloss.Color("9")).
			Width(inner).Render(truncate(st.errMsg, inner)))
	}
	body = append(body, hint.Width(inner).Align(lipgloss.Center).Render(footer))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(focusedColor).
		Padding(1, 3).
		Render(lipgloss.JoinVertical(lipgloss.Left, body...))
}

// renderJoinList draws the channel rows: one line each ("# name — purpose"),
// windowed around the selection so a big catalogue can't grow the modal past
// the screen.
func (m *Model) renderJoinList(inner int) []string {
	st := m.joinChan
	dim := lipgloss.NewStyle().Foreground(dimColor)
	hint := lipgloss.NewStyle().Foreground(dimColor).Italic(true)

	switch {
	case st.loading:
		return []string{hint.Width(inner).Render("loading channels…")}
	case st.errMsg != "" && len(st.cache[st.joinTeamID()]) == 0:
		return []string{hint.Width(inner).Render("couldn't load the channel list")}
	}
	results := st.joinResults()
	if len(results) == 0 {
		if strings.TrimSpace(st.filter.Value()) != "" {
			return []string{hint.Width(inner).Render("no channel matches")}
		}
		return []string{hint.Width(inner).Render("you're already in every public channel here")}
	}

	// Window the list around the selection, keeping the cursor on screen.
	start := 0
	if st.idx >= joinListHeight {
		start = st.idx - joinListHeight + 1
	}
	end := min(start+joinListHeight, len(results))

	out := make([]string, 0, joinListHeight+2)
	if start > 0 {
		out = append(out, dim.Render("  ↑ "+strconv.Itoa(start)+" more"))
	}
	for i := start; i < end; i++ {
		c := results[i]
		marker, style := "  ", lipgloss.NewStyle()
		if i == st.idx {
			marker, style = "› ", lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
		}
		// Named the way the sidebar names it, so the row the user picks here is
		// the row they'll look for there. The filter still matches the URL slug.
		row := marker + style.Render("#"+displayChannel(c))
		if purpose := strings.TrimSpace(strings.ReplaceAll(c.Purpose, "\n", " ")); purpose != "" {
			if room := inner - lipgloss.Width(row) - 3; room >= 12 {
				row += dim.Render(" — " + truncate(purpose, room))
			}
		}
		out = append(out, lipgloss.NewStyle().MaxWidth(inner).Render(row))
	}
	if end < len(results) {
		out = append(out, dim.Render("  ↓ "+strconv.Itoa(len(results)-end)+" more"))
	}
	return out
}
