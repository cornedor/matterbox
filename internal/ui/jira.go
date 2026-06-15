package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/jira"
)

// The Jira issue side panel. Press the open-reference key (v by default) on a
// message that names a Jira issue — either a full atlassian.net/browse/KEY link
// or, for an allowlisted project, a bare ABC-123 — to fetch the issue from Jira
// Cloud and read it inline. It mirrors the thread sidebar's layout (a right-side
// pane that splits the messages area) but is read-only: no composer moves into
// it. It hosts the single right slot, so opening it closes the thread pane (and
// vice-versa). When a post names several issues, ←/→ cycle them; r refetches; o
// opens the issue in a browser; esc or v closes.

// jiraLoadedMsg carries a finished background fetch. gen guards a stale result
// the user already cycled or closed past (see Model.jiraGen); key records which
// issue it was for.
type jiraLoadedMsg struct {
	gen   int
	key   string
	issue *jira.Issue
	err   error
}

var (
	jiraKeyStyle   = lipgloss.NewStyle().Bold(true).Foreground(focusedColor)
	jiraLabelStyle = lipgloss.NewStyle().Foreground(dimColor)
	jiraDimStyle   = lipgloss.NewStyle().Foreground(dimColor)
	jiraErrStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// openJiraForPost raises the Jira panel for the issue(s) named in p. With no
// client configured it points the user at the config; with no issue ref on the
// post it says so. The first ref loads immediately; ←/→ cycle the rest.
func (m Model) openJiraForPost(p *model.Post) (tea.Model, tea.Cmd) {
	if p == nil {
		return m, nil
	}
	if !m.jiraClient.Enabled() {
		m.status = "Jira not configured — set jira.base_url, email and api_token in config.yaml"
		return m, nil
	}
	refs := jira.Refs(p.Message, m.jiraClient.BaseURL(), m.jiraProjects)
	if len(refs) == 0 {
		m.status = "no Jira issue on this message"
		return m, nil
	}
	// The Jira panel and the thread sidebar share the single right slot.
	if m.threadOpen {
		m.closeThread()
	}
	m.jiraOpen = true
	m.jiraRefs = refs
	m.jiraRefIdx = 0
	m.focus = focusJira
	m.status = "esc closes · o opens in browser · r refreshes" + jiraCycleHint(len(refs))
	cmd := m.loadJiraIssue(refs[0])
	m.resizeMessagesViewport()
	return m, cmd
}

// jiraCycleHint adds the ←/→ hint only when there's more than one issue.
func jiraCycleHint(n int) string {
	if n > 1 {
		return " · ←/→ cycles"
	}
	return ""
}

// loadJiraIssue puts the panel into its loading state for key and returns the
// fetch Cmd, bumping jiraGen so any older in-flight fetch is dropped on arrival.
func (m *Model) loadJiraIssue(key string) tea.Cmd {
	m.jiraGen++
	m.jiraLoading = true
	m.jiraErr = nil
	m.jiraIssue = nil
	m.jiraView.GotoTop()
	m.renderJira()
	return m.fetchJira(m.jiraGen, key)
}

// fetchJira fetches (and caches) an issue in the background, returning a
// jiraLoadedMsg tagged with gen.
func (m Model) fetchJira(gen int, key string) tea.Cmd {
	client := m.jiraClient
	ctx := m.ctx
	return func() tea.Msg {
		issue, err := client.Get(ctx, key)
		return jiraLoadedMsg{gen: gen, key: key, issue: issue, err: err}
	}
}

// handleJiraLoaded installs a finished fetch, unless the user has since cycled
// or closed the panel (stale gen).
func (m Model) handleJiraLoaded(msg jiraLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.jiraOpen || msg.gen != m.jiraGen {
		return m, nil
	}
	m.jiraLoading = false
	if msg.err != nil {
		m.jiraErr = msg.err
		m.jiraIssue = nil
	} else {
		m.jiraErr = nil
		m.jiraIssue = msg.issue
	}
	m.renderJira()
	return m, nil
}

// closeJira tears the panel down and returns focus to the messages pane. jiraGen
// is bumped so any in-flight fetch is ignored on arrival.
func (m *Model) closeJira() {
	if !m.jiraOpen {
		return
	}
	m.jiraOpen = false
	m.jiraRefs = nil
	m.jiraRefIdx = 0
	m.jiraIssue = nil
	m.jiraErr = nil
	m.jiraLoading = false
	m.jiraGen++
	if m.focus == focusJira {
		m.focus = focusMessages
	}
	m.resizeMessagesViewport()
}

// cycleJiraRef moves to the previous/next issue named on the source post and
// loads it. No-op when the post named a single issue.
func (m Model) cycleJiraRef(delta int) (tea.Model, tea.Cmd) {
	n := len(m.jiraRefs)
	if n <= 1 {
		return m, nil
	}
	m.jiraRefIdx = ((m.jiraRefIdx+delta)%n + n) % n
	cmd := m.loadJiraIssue(m.jiraRefs[m.jiraRefIdx])
	return m, cmd
}

// refreshJira drops the cached copy of the shown issue and refetches it.
func (m Model) refreshJira() (tea.Model, tea.Cmd) {
	if m.jiraRefIdx < 0 || m.jiraRefIdx >= len(m.jiraRefs) {
		return m, nil
	}
	key := m.jiraRefs[m.jiraRefIdx]
	m.jiraClient.Invalidate(key)
	cmd := m.loadJiraIssue(key)
	return m, cmd
}

// handleJiraKey owns every keystroke while the panel has focus: esc / the
// open-reference key close it, ←/→ cycle issues, r refetches, o opens the issue
// in a browser, and anything else scrolls the viewport.
func (m Model) handleJiraKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeJira()
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.OpenRef): // same key that opened it closes it
		m.closeJira()
		return m, nil
	case key.Matches(msg, m.keys.Right):
		return m.cycleJiraRef(1)
	case key.Matches(msg, m.keys.Left):
		return m.cycleJiraRef(-1)
	case key.Matches(msg, m.keys.Refresh):
		return m.refreshJira()
	case key.Matches(msg, m.keys.OpenAttach):
		if m.jiraIssue == nil || m.jiraIssue.URL == "" {
			return m, nil
		}
		o := openable{name: m.jiraIssue.Key, url: m.jiraIssue.URL}
		m.status = "opening " + o.url + "…"
		return m, m.openOpenable(o)
	}
	var cmd tea.Cmd
	m.jiraView, cmd = m.jiraView.Update(msg)
	return m, cmd
}

// renderJira rebuilds the panel viewport's content (loading / error / issue).
func (m *Model) renderJira() {
	if !m.jiraOpen {
		return
	}
	// New content generation: invalidates the jira scroll-geometry cache.
	m.jiraContentVer++
	switch {
	case m.jiraErr != nil:
		m.jiraView.SetContent(jiraErrStyle.Render(m.jiraErr.Error()))
	case m.jiraLoading || m.jiraIssue == nil:
		key := ""
		if m.jiraRefIdx >= 0 && m.jiraRefIdx < len(m.jiraRefs) {
			key = m.jiraRefs[m.jiraRefIdx]
		}
		m.jiraView.SetContent(jiraDimStyle.Render("loading " + key + "…"))
	default:
		m.jiraView.SetContent(m.renderJiraIssue(m.jiraIssue, m.jiraView.Width()))
	}
}

// renderJiraIssue formats one issue for the viewport: a key + type header, the
// summary, an aligned meta block, then the description rendered through the
// shared markdown renderer (so links are clickable and inline styling matches
// the message pane).
func (m *Model) renderJiraIssue(iss *jira.Issue, width int) string {
	var b strings.Builder

	header := jiraKeyStyle.Render(iss.Key)
	if iss.Type != "" {
		header += "  " + jiraDimStyle.Render(iss.Type)
	}
	b.WriteString(header + "\n")
	if iss.Summary != "" {
		b.WriteString(titleStyle.Render(iss.Summary) + "\n")
	}
	b.WriteString("\n")

	meta := func(label, value string) {
		if value == "" {
			return
		}
		lbl := label + ":"
		pad := 10 - len(lbl)
		if pad < 1 {
			pad = 1
		}
		b.WriteString(jiraLabelStyle.Render(lbl) + strings.Repeat(" ", pad) + value + "\n")
	}
	meta("Status", iss.Status)
	meta("Priority", iss.Priority)
	meta("Assignee", iss.Assignee)
	meta("Reporter", iss.Reporter)
	if len(iss.Labels) > 0 {
		meta("Labels", strings.Join(iss.Labels, ", "))
	}
	if !iss.Updated.IsZero() {
		meta("Updated", iss.Updated.Format("2006-01-02 15:04"))
	}

	if desc := strings.TrimSpace(iss.Description); desc != "" {
		divW := width
		if divW < 1 {
			divW = 1
		}
		b.WriteString("\n" + jiraDimStyle.Render(strings.Repeat("─", divW)) + "\n")
		b.WriteString(renderMarkdown(desc, m.emojiImg))
	}
	return b.String()
}

// renderJiraPane draws the bordered side pane: a title row + the scrollable
// issue viewport, with the shared right-border/scrollbar treatment. Mirrors
// renderThreadPane but has no composer.
func (m *Model) renderJiraPane(height, width int) string {
	innerH := height
	if innerH < 1 {
		innerH = 1
	}
	if width < threadPaneMinWidth {
		width = threadPaneMinWidth
	}

	title := "Jira"
	switch {
	case m.jiraLoading && m.jiraIssue == nil:
		title = "Jira (loading…)"
	case len(m.jiraRefs) > 1:
		title = fmt.Sprintf("Jira · %d/%d", m.jiraRefIdx+1, len(m.jiraRefs))
	}

	total, pct := m.jiraScrollGeom()
	showScrollbar := total > m.jiraView.Height() && pct < 1.0

	content := lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render(title), m.jiraView.View())

	borderColor := dimColor
	if m.focus == focusJira {
		borderColor = focusedColor
	}
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().
		Width(width - 1).Height(innerH).BorderForeground(borderColor)
	box := style.Render(content)

	rightBorder := renderRightBorder(innerH, 1, m.jiraView.Height(), total, pct, borderColor, showScrollbar)
	return lipgloss.JoinHorizontal(lipgloss.Top, box, rightBorder)
}
