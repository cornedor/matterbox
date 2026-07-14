package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"matterbox/internal/editor"
	"matterbox/internal/jira"
)

// The Jira comment composer: a modal multi-line input for adding a comment to
// the open issue (c) or replying to one (R, via the reply-target picker in
// jira_edit.go). It mirrors the message composer's keys — Enter posts,
// alt/shift+enter inserts a newline, esc cancels — and is fully modal: it owns
// every keystroke while open (dispatched in update.go before the focus-based
// routing) and overlays the screen (view.go), like the field pickers. A
// confirmed post goes through internal/jira, reusing the jiraMutated path so the
// panel refetches and the new comment shows. A reply prefills an editable quote
// of the original and pings its author with a real ADF mention (a plain "@name"
// would not notify).

// commentQuoteMaxLines caps how much of the original comment a reply quotes, so
// a long comment doesn't bury the composer.
const commentQuoteMaxLines = 8

// newCommentTextarea builds the modal's textarea, configured like the message
// composer (dynamic height, Enter posts, alt/shift+enter newline, native
// terminal cursor).
func newCommentTextarea() editor.Model {
	ta := editor.New()
	ta.Placeholder = "comment…"
	ta.CharLimit = 32767
	ta.DynamicHeight = true
	ta.MinHeight = 3
	ta.MaxHeight = maxInputHeight
	ta.MaxContentHeight = 10000
	ta.SetHeight(3)
	ta.SetPromptFunc(2, inputPromptFunc("┃ "))
	// Place the real terminal cursor in the comment editor (see jiraCommentCursor).
	ta.NativeCursor = true
	ta.Styles.Placeholder = lipgloss.NewStyle().Foreground(dimColor)
	ta.ContinueLists = true
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("alt+enter", "shift+enter"),
		key.WithHelp("alt+↵/shift+↵", "newline"),
	)
	ta.Focus()
	return ta
}

// openJiraCommentInput opens an empty composer for a new top-level comment on
// the shown issue.
func (m *Model) openJiraCommentInput() {
	m.jiraCommentActive = true
	m.jiraCommentKey = m.jiraIssue.Key
	m.jiraCommentMention = nil
	m.jiraCommentReplyTo = ""
	m.jiraCommentInput = newCommentTextarea()
}

// openJiraReply opens the composer prefilled with an editable quote of c and
// arranged to @mention its author, so the post reads as (and notifies like) a
// reply. The user is free to trim the quote or change the text before posting.
func (m *Model) openJiraReply(c jira.Comment) {
	m.jiraCommentActive = true
	m.jiraCommentKey = m.jiraIssue.Key
	m.jiraCommentReplyTo = c.Author
	if c.AuthorID != "" {
		m.jiraCommentMention = &jira.Mention{AccountID: c.AuthorID, DisplayName: c.Author}
	} else {
		m.jiraCommentMention = nil
	}
	ta := newCommentTextarea()
	ta.SetValue(replyQuote(c))
	ta.CursorEnd()
	m.jiraCommentInput = ta
}

// replyQuote builds the editable reply seed: a markdown blockquote of c's
// author + body (capped), then a blank line the cursor lands on.
func replyQuote(c jira.Comment) string {
	author := c.Author
	if author == "" {
		author = "comment"
	}
	var b strings.Builder
	b.WriteString("> " + author + " wrote:\n")
	lines := strings.Split(strings.TrimSpace(c.Body), "\n")
	for i, ln := range lines {
		if i >= commentQuoteMaxLines {
			b.WriteString("> …\n")
			break
		}
		b.WriteString("> " + ln + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// closeJiraComment tears the composer down.
func (m *Model) closeJiraComment() {
	m.jiraCommentActive = false
	m.jiraCommentKey = ""
	m.jiraCommentMention = nil
	m.jiraCommentReplyTo = ""
	m.jiraCommentInput = editor.Model{}
}

// handleJiraCommentKey owns every keystroke while the composer is open: esc
// cancels, Enter posts, alt/shift+enter insert a newline (bound on the
// textarea), everything else edits the text.
func (m Model) handleJiraCommentKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeJiraComment()
		return m, nil
	case "enter":
		return m.applyJiraComment()
	}
	var cmd tea.Cmd
	m.jiraCommentInput, cmd = m.jiraCommentInput.Update(msg)
	return m, cmd
}

// applyJiraComment closes the composer and posts the comment (or reply). An
// empty body with no mention is treated as a cancel.
func (m Model) applyJiraComment() (tea.Model, tea.Cmd) {
	key := m.jiraCommentKey
	text := strings.TrimSpace(m.jiraCommentInput.Value())
	mention := m.jiraCommentMention
	m.closeJiraComment()
	if text == "" && mention == nil {
		return m, nil
	}
	client, ctx := m.jiraClient, m.ctx
	verb := "comment on"
	if mention != nil {
		verb = "reply to"
	}
	m.status = fmt.Sprintf("posting %s %s…", verb, key)
	return m, jiraMutateCmd(key, "comment", func() error {
		return client.AddComment(ctx, key, text, mention)
	})
}

// renderJiraCommentInput draws the modal composer, mirroring
// renderJiraPointsInput but multi-line, with a "replying to" line in reply mode.
func (m *Model) renderJiraCommentInput() string {
	if !m.jiraCommentActive {
		return ""
	}
	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 40 {
		outerW = 40
	}
	inner := outerW - 8
	if inner < 1 {
		inner = 1
	}
	m.jiraCommentInput.SetWidth(inner)

	titleTxt := "Comment — " + m.jiraCommentKey
	if m.jiraCommentReplyTo != "" {
		titleTxt = "Reply — " + m.jiraCommentKey
	}
	header := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).Render(titleTxt)

	parts := []string{header, ""}
	if m.jiraCommentReplyTo != "" {
		parts = append(parts, lipgloss.NewStyle().Width(inner).Foreground(dimColor).Italic(true).Render("↩ replying to "+m.jiraCommentReplyTo), "")
	}
	hint := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Foreground(dimColor).Italic(true).Render("↵ post · alt+↵ newline · esc cancel")
	parts = append(parts, m.jiraCommentInput.View(), "", hint)

	body := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(focusedColor).Padding(1, 3).Render(body)
}
