package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/jira"
)

// Jira-specific half of the reference panel: fetching an issue and rendering it.
// The shared panel machinery (open/close/cycle/keys/layout) lives in ref.go;
// the inline field editors live in jira_edit.go.

// jiraLoadedMsg carries a finished background fetch. gen guards a stale result
// the user already cycled or closed past (see Model.refGen); key records which
// issue it was for.
type jiraLoadedMsg struct {
	gen   int
	key   string
	issue *jira.Issue
	err   error
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
// or closed the panel (stale gen) or moved to a non-Jira reference.
func (m Model) handleJiraLoaded(msg jiraLoadedMsg) (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if !m.refOpen || msg.gen != m.refGen || r == nil || r.kind != refJira {
		return m, nil
	}
	m.refLoading = false
	if msg.err != nil {
		m.refErr = msg.err
		m.jiraIssue = nil
	} else {
		m.refErr = nil
		m.jiraIssue = msg.issue
	}
	m.renderRef()
	return m, nil
}

// renderJiraIssue formats one issue for the viewport: a key + type header, the
// summary, an aligned meta block, then the description rendered through the
// shared markdown renderer (so links are clickable and inline styling matches
// the message pane).
func (m *Model) renderJiraIssue(iss *jira.Issue, width int) string {
	var b strings.Builder

	header := refKeyStyle.Render(iss.Key)
	if iss.Type != "" {
		header += "  " + refDimStyle.Render(iss.Type)
	}
	b.WriteString(header + "\n")
	if iss.Summary != "" {
		b.WriteString(titleStyle.Render(iss.Summary) + "\n")
	}
	b.WriteString("\n")

	refMeta(&b, "Status", iss.Status, 10)
	refMeta(&b, "Priority", iss.Priority, 10)
	refMeta(&b, "Points", iss.StoryPoints, 10)
	refMeta(&b, "Assignee", iss.Assignee, 10)
	refMeta(&b, "Reporter", iss.Reporter, 10)
	if len(iss.Labels) > 0 {
		refMeta(&b, "Labels", strings.Join(iss.Labels, ", "), 10)
	}
	if !iss.Updated.IsZero() {
		refMeta(&b, "Updated", iss.Updated.Format("2006-01-02 15:04"), 10)
	}

	// Edit affordances for the four changeable fields (see jira_edit.go).
	b.WriteString("\n" + refDimStyle.Render("s status · p priority · P points · a assignee") + "\n")

	if desc := strings.TrimSpace(iss.Description); desc != "" {
		divW := width
		if divW < 1 {
			divW = 1
		}
		b.WriteString("\n" + refDimStyle.Render(strings.Repeat("─", divW)) + "\n")
		b.WriteString(renderMarkdown(desc, m.emojiImg))
	}
	return b.String()
}
