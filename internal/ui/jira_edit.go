package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// jiraAssigneeDebounce delays the assignable-user search after a keystroke so
// rapid typing coalesces into one request (mirrors mentionDebounce).
const jiraAssigneeDebounce = 200 * time.Millisecond

// Editable Jira fields. While the panel has focus (see jira.go) the keys
// s / p / a open a modal list picker for Status / Priority / Assignee, and P
// opens a numeric input for Story points. The pickers and the input are fully
// modal — they own every keystroke while open (dispatched in update.go before
// the focus-based routing) and overlay the screen (view.go), mirroring the
// reaction picker. A confirmed change writes to Jira (internal/jira), then the
// panel refetches the issue so authoritative — and any cascading — values show.

// jiraPickerKind selects which field a picker edits and which fetch/mutation
// backs it.
type jiraPickerKind int

const (
	jiraPickStatus jiraPickerKind = iota
	jiraPickPriority
	jiraPickAssignee
)

// jiraPickerItem is one selectable row. id is the value handed to the mutation
// (transition id / priority id / accountId; "" = unassign). current marks the
// issue's present value with a ✓.
type jiraPickerItem struct {
	id      string
	label   string
	current bool
}

// jiraPickerState is the modal list picker reused for the three list-style
// fields. The assignee picker is filterable: a textinput drives a debounced
// server-side search of assignable users (a large project has more than the
// default page); status and priority are short fixed lists with 1-9
// accelerators. fetchSeq is bumped on every (re)query so a late response from
// an earlier query is discarded — same idea as mentionState.fetchSeq.
type jiraPickerState struct {
	active      bool
	kind        jiraPickerKind
	issueKey    string
	title       string
	gen         int // drops a stale async load (picker reopened/closed since)
	loading     bool
	err         error
	items       []jiraPickerItem
	idx         int  // cursor into the item list
	filterable  bool // assignee: has a search box backed by the server
	filter      textinput.Model
	fetchSeq    int    // discards a search response from a superseded query
	curAssignee string // accountId of the issue's assignee, to mark ✓ across re-queries
}

// jiraPickerLoadedMsg carries the fetched option list for an open picker. gen +
// kind + seq drop a result the user has since closed, replaced, or re-queried.
type jiraPickerLoadedMsg struct {
	gen   int
	seq   int
	kind  jiraPickerKind
	items []jiraPickerItem
	err   error
}

// jiraAssigneeDebounceMsg fires after the debounce window to run the pending
// assignee search for seq (ignored if a newer keystroke has superseded it).
type jiraAssigneeDebounceMsg struct{ seq int }

// jiraMutatedMsg carries the result of a field write. On success the panel
// reloads the issue (the client already invalidated its cache).
type jiraMutatedMsg struct {
	key   string
	field string
	err   error
}

// startJiraPicker resets the picker to a fresh loading state for the current
// issue and returns the new generation so the caller's fetch can be matched
// against it.
func (m *Model) startJiraPicker(kind jiraPickerKind, title string, filterable bool) int {
	gen := m.jiraPicker.gen + 1
	m.jiraPicker = jiraPickerState{
		active:     true,
		kind:       kind,
		issueKey:   m.jiraIssue.Key,
		title:      title,
		gen:        gen,
		loading:    true,
		filterable: filterable,
		fetchSeq:   1,
	}
	if filterable {
		ti := textinput.New()
		ti.Prompt = "❯ "
		ti.Placeholder = "filter…"
		ti.SetWidth(40)
		ti.Focus()
		m.jiraPicker.filter = ti
	}
	return gen
}

// openJiraStatusPicker loads the issue's workflow transitions (the only status
// changes Jira accepts) into the picker.
func (m *Model) openJiraStatusPicker() tea.Cmd {
	gen := m.startJiraPicker(jiraPickStatus, "Set status — "+m.jiraIssue.Key, false)
	seq := m.jiraPicker.fetchSeq
	key, cur := m.jiraIssue.Key, m.jiraIssue.Status
	client, ctx := m.jiraClient, m.ctx
	return func() tea.Msg {
		opts, err := client.Transitions(ctx, key)
		if err != nil {
			return jiraPickerLoadedMsg{gen: gen, seq: seq, kind: jiraPickStatus, err: err}
		}
		items := make([]jiraPickerItem, 0, len(opts))
		for _, o := range opts {
			items = append(items, jiraPickerItem{id: o.ID, label: o.Name, current: o.Name == cur})
		}
		return jiraPickerLoadedMsg{gen: gen, seq: seq, kind: jiraPickStatus, items: items}
	}
}

// openJiraPriorityPicker loads the instance priority list into the picker.
func (m *Model) openJiraPriorityPicker() tea.Cmd {
	gen := m.startJiraPicker(jiraPickPriority, "Set priority — "+m.jiraIssue.Key, false)
	seq := m.jiraPicker.fetchSeq
	curID := m.jiraIssue.PriorityID
	client, ctx := m.jiraClient, m.ctx
	return func() tea.Msg {
		opts, err := client.Priorities(ctx)
		if err != nil {
			return jiraPickerLoadedMsg{gen: gen, seq: seq, kind: jiraPickPriority, err: err}
		}
		items := make([]jiraPickerItem, 0, len(opts))
		for _, o := range opts {
			items = append(items, jiraPickerItem{id: o.ID, label: o.Name, current: o.ID == curID})
		}
		return jiraPickerLoadedMsg{gen: gen, seq: seq, kind: jiraPickPriority, items: items}
	}
}

// openJiraAssigneePicker opens the filterable assignee picker and runs the
// initial (empty-query) search. Typing re-runs it server-side (see
// handleJiraPickerKey / jiraAssigneeDebounceMsg).
func (m *Model) openJiraAssigneePicker() tea.Cmd {
	gen := m.startJiraPicker(jiraPickAssignee, "Set assignee — "+m.jiraIssue.Key, true)
	m.jiraPicker.curAssignee = m.jiraIssue.AssigneeAccountID
	return m.fetchAssignees(gen, m.jiraPicker.fetchSeq, m.jiraIssue.Key, "")
}

// fetchAssignees searches assignable users for query and builds the picker
// rows. With an empty query (the default view) it prepends Unassigned and
// "Assign to me"; while searching it shows server matches only.
func (m Model) fetchAssignees(gen, seq int, key, query string) tea.Cmd {
	client, ctx := m.jiraClient, m.ctx
	curID := m.jiraPicker.curAssignee
	return func() tea.Msg {
		users, err := client.AssignableUsers(ctx, key, query)
		if err != nil {
			return jiraPickerLoadedMsg{gen: gen, seq: seq, kind: jiraPickAssignee, err: err}
		}
		var items []jiraPickerItem
		meID := ""
		if strings.TrimSpace(query) == "" {
			items = append(items, jiraPickerItem{id: "", label: "Unassigned", current: curID == ""})
			// "Assign to me" is a convenience pinned near the top; a Myself
			// failure just omits it (and the dedup below is skipped).
			if me, meErr := client.Myself(ctx); meErr == nil && me.AccountID != "" {
				meID = me.AccountID
				items = append(items, jiraPickerItem{
					id:      me.AccountID,
					label:   "Assign to me (" + me.DisplayName + ")",
					current: me.AccountID == curID,
				})
			}
		}
		for _, u := range users {
			if meID != "" && u.AccountID == meID {
				continue // already shown as "Assign to me"
			}
			items = append(items, jiraPickerItem{id: u.AccountID, label: u.DisplayName, current: u.AccountID == curID})
		}
		return jiraPickerLoadedMsg{gen: gen, seq: seq, kind: jiraPickAssignee, items: items}
	}
}

// openJiraPointsInput shows the numeric story-points input seeded with the
// current value (empty when unset).
func (m *Model) openJiraPointsInput() {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.Placeholder = "number (empty clears)"
	ti.CharLimit = 12
	ti.SetWidth(24)
	ti.SetValue(m.jiraIssue.StoryPoints)
	ti.CursorEnd()
	ti.Focus()
	m.jiraPointsInput = ti
	m.jiraPointsActive = true
	m.jiraPointsKey = m.jiraIssue.Key
}

// handleJiraPickerLoaded installs a finished option fetch and parks the cursor
// on the current value (when present). Stale results are dropped.
func (m Model) handleJiraPickerLoaded(msg jiraPickerLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.jiraPicker.active || msg.gen != m.jiraPicker.gen || msg.kind != m.jiraPicker.kind || msg.seq != m.jiraPicker.fetchSeq {
		return m, nil
	}
	m.jiraPicker.loading = false
	m.jiraPicker.err = msg.err
	m.jiraPicker.items = msg.items
	m.jiraPicker.idx = 0
	for i, it := range msg.items {
		if it.current {
			m.jiraPicker.idx = i
			break
		}
	}
	return m, nil
}

// closeJiraPicker tears the picker down, preserving gen so a late load is
// ignored.
func (m *Model) closeJiraPicker() {
	m.jiraPicker = jiraPickerState{gen: m.jiraPicker.gen}
}

// closeJiraPoints tears the points input down.
func (m *Model) closeJiraPoints() {
	m.jiraPointsActive = false
	m.jiraPointsKey = ""
	m.jiraPointsInput = textinput.Model{}
}

// handleJiraPickerKey owns every keystroke while the list picker is open.
// Arrow/ctrl-nav move the cursor; for the fixed lists 1-9 jump+apply; enter
// applies; esc cancels. For the assignee picker, other keys feed the filter.
func (m Model) handleJiraPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeJiraPicker()
		return m, nil
	case "enter":
		return m.applyJiraPick()
	}

	if m.jiraPicker.filterable {
		// Arrows + ctrl+p/ctrl+n navigate so letters stay available for typing.
		switch {
		case key.Matches(msg, m.keys.InputUp):
			m.jiraPickerMove(-1)
			return m, nil
		case key.Matches(msg, m.keys.InputDown):
			m.jiraPickerMove(1)
			return m, nil
		}
		before := m.jiraPicker.filter.Value()
		var cmd tea.Cmd
		m.jiraPicker.filter, cmd = m.jiraPicker.filter.Update(msg)
		if m.jiraPicker.filter.Value() == before {
			return m, cmd
		}
		// Query changed: schedule a debounced server search. fetchSeq drops any
		// earlier in-flight search. The existing rows stay on screen meanwhile.
		m.jiraPicker.fetchSeq++
		seq := m.jiraPicker.fetchSeq
		debounce := tea.Tick(jiraAssigneeDebounce, func(time.Time) tea.Msg {
			return jiraAssigneeDebounceMsg{seq: seq}
		})
		return m, tea.Batch(cmd, debounce)
	}

	switch {
	case key.Matches(msg, m.keys.Up):
		m.jiraPickerMove(-1)
		return m, nil
	case key.Matches(msg, m.keys.Down):
		m.jiraPickerMove(1)
		return m, nil
	}
	// Digit accelerators 1..9 over the (short) fixed list.
	if s := msg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		idx := int(s[0] - '1')
		if idx < len(m.jiraPicker.items) {
			m.jiraPicker.idx = idx
			return m.applyJiraPick()
		}
	}
	return m, nil
}

// handleJiraAssigneeDebounce runs the pending assignee search once the debounce
// window elapses, unless a newer keystroke has superseded it.
func (m Model) handleJiraAssigneeDebounce(msg jiraAssigneeDebounceMsg) (tea.Model, tea.Cmd) {
	if !m.jiraPicker.active || m.jiraPicker.kind != jiraPickAssignee || msg.seq != m.jiraPicker.fetchSeq {
		return m, nil
	}
	return m, m.fetchAssignees(m.jiraPicker.gen, msg.seq, m.jiraPicker.issueKey, m.jiraPicker.filter.Value())
}

// jiraPickerMove nudges the cursor within the item list, clamped to bounds. The
// render windows around it (see renderJiraPicker), so no scroll state is tracked
// here.
func (m *Model) jiraPickerMove(delta int) {
	n := len(m.jiraPicker.items)
	if n == 0 {
		m.jiraPicker.idx = 0
		return
	}
	idx := m.jiraPicker.idx + delta
	if idx < 0 {
		idx = 0
	}
	if idx > n-1 {
		idx = n - 1
	}
	m.jiraPicker.idx = idx
}

// handleJiraPointsKey owns every keystroke while the points input is open.
func (m Model) handleJiraPointsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeJiraPoints()
		return m, nil
	case "enter":
		return m.applyJiraPoints()
	}
	var cmd tea.Cmd
	m.jiraPointsInput, cmd = m.jiraPointsInput.Update(msg)
	return m, cmd
}

// applyJiraPick closes the picker and fires the mutation for the highlighted
// row.
func (m Model) applyJiraPick() (tea.Model, tea.Cmd) {
	items := m.jiraPicker.items
	if m.jiraPicker.idx < 0 || m.jiraPicker.idx >= len(items) {
		return m, nil
	}
	it := items[m.jiraPicker.idx]
	kind := m.jiraPicker.kind
	key := m.jiraPicker.issueKey
	m.closeJiraPicker()

	client, ctx := m.jiraClient, m.ctx
	var field string
	var run func() error
	switch kind {
	case jiraPickStatus:
		field = "status"
		run = func() error { return client.DoTransition(ctx, key, it.id) }
	case jiraPickPriority:
		field = "priority"
		run = func() error { return client.SetPriority(ctx, key, it.id) }
	case jiraPickAssignee:
		field = "assignee"
		run = func() error { return client.SetAssignee(ctx, key, it.id) }
	default:
		return m, nil
	}
	m.status = fmt.Sprintf("updating %s %s…", key, field)
	return m, jiraMutateCmd(key, field, run)
}

// applyJiraPoints closes the input and fires the story-points write.
func (m Model) applyJiraPoints() (tea.Model, tea.Cmd) {
	key := m.jiraPointsKey
	raw := m.jiraPointsInput.Value()
	m.closeJiraPoints()
	client, ctx := m.jiraClient, m.ctx
	m.status = fmt.Sprintf("updating %s points…", key)
	return m, jiraMutateCmd(key, "points", func() error { return client.SetStoryPoints(ctx, key, raw) })
}

// jiraMutateCmd runs a field write in the background and reports the result.
func jiraMutateCmd(key, field string, run func() error) tea.Cmd {
	return func() tea.Msg {
		return jiraMutatedMsg{key: key, field: field, err: run()}
	}
}

// handleJiraMutated reports the write outcome and reloads the issue on success
// so the panel shows the authoritative (and any cascading) values.
func (m Model) handleJiraMutated(msg jiraMutatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.status = fmt.Sprintf("%s %s update failed: %v", msg.key, msg.field, msg.err)
		return m, nil
	}
	m.status = fmt.Sprintf("%s %s updated", msg.key, msg.field)
	if m.jiraOpen && m.jiraRefIdx >= 0 && m.jiraRefIdx < len(m.jiraRefs) && m.jiraRefs[m.jiraRefIdx] == msg.key {
		return m, m.loadJiraIssue(msg.key)
	}
	return m, nil
}

// renderJiraPicker draws the modal list picker. Layout mirrors
// renderReactionPicker (rounded border, centred title, ✓ on the current value,
// cursor row in focusedColor, footer hint); a long list is windowed around the
// selection the same way renderSwitcherCommands does, so it never overflows
// maxH (the body area height passed by view.go).
func (m *Model) renderJiraPicker(maxH int) string {
	if !m.jiraPicker.active {
		return ""
	}
	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 32 {
		outerW = 32
	}
	inner := outerW - 8
	if inner < 1 {
		inner = 1
	}

	parts := []string{lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).Render(m.jiraPicker.title)}
	if m.jiraPicker.filterable {
		parts = append(parts, m.jiraPicker.filter.View())
	}

	switch {
	case m.jiraPicker.loading:
		parts = append(parts, "", jiraDimStyle.Render("loading…"))
	case m.jiraPicker.err != nil:
		parts = append(parts, "", jiraErrStyle.Render(m.jiraPicker.err.Error()))
	default:
		vis := m.jiraPicker.items
		if len(vis) == 0 {
			parts = append(parts, "", jiraDimStyle.Render("no matches"))
			break
		}
		// Window a long set (e.g. assignable users) around the selection so the
		// popup stays within maxH, mirroring renderSwitcherCommands. Chrome inside
		// the box is the title + blank + two scroll markers + blank + hint (6) plus
		// the border + padding (4), plus the filter input when present.
		win := maxH - 10
		if m.jiraPicker.filterable {
			win--
		}
		if win < 3 {
			win = 3
		}
		start := 0
		if len(vis) > win {
			// Keep the selected row roughly centred, clamped to the ends.
			start = m.jiraPicker.idx - win/2
			if start < 0 {
				start = 0
			}
			if start > len(vis)-win {
				start = len(vis) - win
			}
		}
		end := start + win
		if end > len(vis) {
			end = len(vis)
		}

		cursorStyle := lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
		rows := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			it := vis[i]
			prefix := ""
			if !m.jiraPicker.filterable {
				accel := " "
				if i < 9 {
					accel = fmt.Sprintf("%d", i+1)
				}
				prefix = "[" + accel + "] "
			}
			marker := " "
			if it.current {
				marker = "✓"
			}
			text := fmt.Sprintf("%s%s %s", prefix, marker, it.label)
			if i == m.jiraPicker.idx {
				rows = append(rows, cursorStyle.Render("▸ "+text))
			} else {
				rows = append(rows, "  "+text)
			}
		}

		// Scroll markers, shown only when there's something off-screen (win is
		// sized assuming both can appear, so the popup stays within maxH).
		parts = append(parts, "")
		if start > 0 {
			parts = append(parts, jiraDimStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
		}
		parts = append(parts, strings.Join(rows, "\n"))
		if end < len(vis) {
			parts = append(parts, jiraDimStyle.Render(fmt.Sprintf("  ↓ %d more", len(vis)-end)))
		}
	}

	hintTxt := "↑/↓ move · ↵ apply · esc cancel"
	if m.jiraPicker.filterable {
		hintTxt = "type to filter · ↑/↓ move · ↵ apply · esc cancel"
	}
	parts = append(parts, "", lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Foreground(dimColor).Italic(true).Render(hintTxt))

	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(focusedColor).Padding(1, 3).
		Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
}

// renderJiraPointsInput draws the numeric story-points modal.
func (m *Model) renderJiraPointsInput() string {
	if !m.jiraPointsActive {
		return ""
	}
	outerW := 40
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 24 {
		outerW = 24
	}
	inner := outerW - 8
	if inner < 1 {
		inner = 1
	}
	header := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).Render("Set story points — " + m.jiraPointsKey)
	hint := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Foreground(dimColor).Italic(true).Render("↵ save · empty clears · esc cancel")
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", m.jiraPointsInput.View(), "", hint)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(focusedColor).Padding(1, 3).Render(body)
}
