package ui

import (
	"fmt"
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

	// Edit affordances: the four changeable fields (jira_edit.go) plus comments
	// (jira_comment.go).
	b.WriteString("\n" + refDimStyle.Render("s status · p priority · P points · a assignee · c comment · R reply") + "\n")

	if desc := strings.TrimSpace(iss.Description); desc != "" {
		divW := width
		if divW < 1 {
			divW = 1
		}
		b.WriteString("\n" + refDimStyle.Render(strings.Repeat("─", divW)) + "\n")
		b.WriteString(renderMarkdown(desc, m.emojiImg, nil, ""))
	}

	m.renderJiraComments(&b, iss, width)
	return b.String()
}

// renderJiraComments appends the issue's comment thread under the description: a
// divider, a "Comments (N)" heading, then each comment (oldest first, the order
// the API returns) as a dim author·timestamp line and its markdown body. When
// the issue has more comments than the inline field returned, a trailing note
// points at the browser.
func (m *Model) renderJiraComments(b *strings.Builder, iss *jira.Issue, width int) {
	if len(iss.Comments) == 0 && iss.CommentTotal == 0 {
		return
	}
	divW := width
	if divW < 1 {
		divW = 1
	}
	b.WriteString("\n" + refDimStyle.Render(strings.Repeat("─", divW)) + "\n")

	count := iss.CommentTotal
	if count < len(iss.Comments) {
		count = len(iss.Comments)
	}
	b.WriteString(refLabelStyle.Render(fmt.Sprintf("Comments (%d)", count)) + "\n\n")

	for i, c := range iss.Comments {
		author := c.Author
		if author == "" {
			author = "Unknown"
		}
		when := ""
		if !c.Created.IsZero() {
			when = " · " + c.Created.Format("2006-01-02 15:04")
		}
		b.WriteString(refDimStyle.Render(author+when) + "\n")
		if body := strings.TrimSpace(c.Body); body != "" {
			b.WriteString(renderMarkdown(body, m.emojiImg, nil, ""))
		}
		if i < len(iss.Comments)-1 {
			b.WriteString("\n")
		}
	}

	if extra := iss.CommentTotal - len(iss.Comments); extra > 0 {
		b.WriteString("\n" + refDimStyle.Render(fmt.Sprintf("…and %d more — o opens in browser", extra)) + "\n")
	}
}
