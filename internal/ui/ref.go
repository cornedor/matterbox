package ui

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/gitlab"
	"matterbox/internal/jira"
)

// The reference side panel. Press the open-reference key (v by default) on a
// message that names a Jira issue or links a GitLab merge request to fetch it
// and read it inline. It mirrors the thread sidebar's layout (a right-side pane
// that splits the messages area) and hosts the single right slot, so opening it
// closes the thread pane (and vice-versa). When a post names several references
// — Jira and GitLab mixed, in order of appearance — ←/→ cycle them, each
// rendered by its provider; r refetches; o opens it in a browser; esc or v
// closes. Provider-specific loading/rendering/keys live in jira.go and
// gitlab.go; this file owns the shared panel machinery.

// refKind identifies which provider a reference targets, selecting the loader,
// renderer and key set used for it.
type refKind int

const (
	refJira refKind = iota
	refGitLab
)

// reference is one detected, openable target. Only the fields for its kind are
// set; pos is the byte offset of its first appearance in the source message, so
// refs from both providers can be ordered together.
type reference struct {
	kind    refKind
	jiraKey string // refJira: issue key, e.g. ABC-123
	glProj  string // refGitLab: project path, e.g. group/project
	glIID   int    // refGitLab: merge-request iid
	pos     int
}

// label is the canonical short id shown in titles and loading text.
func (r reference) label() string {
	switch r.kind {
	case refJira:
		return r.jiraKey
	case refGitLab:
		return fmt.Sprintf("%s!%d", r.glProj, r.glIID)
	}
	return ""
}

var (
	refKeyStyle   = lipgloss.NewStyle().Bold(true).Foreground(focusedColor)
	refLabelStyle = lipgloss.NewStyle().Foreground(dimColor)
	refDimStyle   = lipgloss.NewStyle().Foreground(dimColor)
	refErrStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// hostFromURL returns the lowercased host of raw, or "" — used to resolve a
// glab token for the configured GitLab instance.
func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}

// openRefForPost raises the reference panel for every Jira issue and GitLab
// merge request named in p, ordered by where they appear. With no provider
// configured it points the user at the config; with nothing detected it says
// so. The first ref loads immediately; ←/→ cycle the rest.
func (m Model) openRefForPost(p *model.Post) (tea.Model, tea.Cmd) {
	if p == nil {
		return m, nil
	}
	jiraOK := m.jiraClient.Enabled()
	glOK := m.glClient.Enabled()
	if !jiraOK && !glOK {
		m.status = "no reference provider configured — set jira.* or gitlab.* in config.yaml"
		return m, nil
	}

	var refs []reference
	if jiraOK {
		for _, r := range jira.Refs(p.Message, m.jiraClient.BaseURL(), m.jiraProjects) {
			refs = append(refs, reference{kind: refJira, jiraKey: r.Key, pos: r.Pos})
		}
	}
	if glOK {
		for _, r := range gitlab.Refs(p.Message, m.glClient.BaseURL()) {
			refs = append(refs, reference{kind: refGitLab, glProj: r.Project, glIID: r.IID, pos: r.Pos})
		}
	}
	if len(refs) == 0 {
		m.status = "no Jira issue or GitLab MR on this message"
		return m, nil
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].pos < refs[j].pos })

	// The reference panel and the thread sidebar share the single right slot.
	var threadCmd tea.Cmd
	if m.threadOpen {
		threadCmd = m.closeThread()
	}
	m.refOpen = true
	m.refs = refs
	m.refIdx = 0
	m.focus = focusRef
	m.status = refStatusHint(refs[0], len(refs))
	cmd := m.loadCurrentRef()
	m.resizeMessagesViewport()
	return m, tea.Batch(threadCmd, cmd)
}

// currentRef returns the reference currently shown, or nil when the panel is
// closed or the index is somehow out of range.
func (m *Model) currentRef() *reference {
	if !m.refOpen || m.refIdx < 0 || m.refIdx >= len(m.refs) {
		return nil
	}
	return &m.refs[m.refIdx]
}

// refCycleHint adds the ←/→ hint only when there's more than one reference.
func refCycleHint(n int) string {
	if n > 1 {
		return " · ←/→ cycles"
	}
	return ""
}

// refStatusHint builds the status-bar line for the current ref: the keys that
// act on it (provider-specific) plus the shared o/r/esc affordances.
func refStatusHint(r reference, n int) string {
	switch r.kind {
	case refJira:
		return "s/p/P/a edit · c comment · R reply · o browser · r refresh · esc closes" + refCycleHint(n)
	case refGitLab:
		return "A approve · M merge · o browser · r refresh · esc closes" + refCycleHint(n)
	}
	return "o browser · r refresh · esc closes" + refCycleHint(n)
}

// loadCurrentRef puts the panel into its loading state for the current ref and
// returns the provider fetch Cmd, bumping refGen so any older in-flight fetch is
// dropped on arrival.
func (m *Model) loadCurrentRef() tea.Cmd {
	r := m.currentRef()
	if r == nil {
		return nil
	}
	m.refGen++
	m.refLoading = true
	m.refErr = nil
	m.jiraIssue = nil
	m.glMR = nil
	m.refView.GotoTop()
	m.renderRef()
	switch r.kind {
	case refJira:
		return m.fetchJira(m.refGen, r.jiraKey)
	case refGitLab:
		return m.fetchGitLabMR(m.refGen, r.glProj, r.glIID)
	}
	return nil
}

// closeRef tears the panel down and returns focus to the messages pane. refGen
// is bumped so any in-flight fetch is ignored on arrival.
func (m *Model) closeRef() {
	if !m.refOpen {
		return
	}
	m.refOpen = false
	m.refs = nil
	m.refIdx = 0
	m.jiraIssue = nil
	m.glMR = nil
	m.refErr = nil
	m.refLoading = false
	m.refGen++
	// Tear down any open editor/confirm so it can't outlive the panel.
	m.closeJiraPicker()
	m.closeJiraPoints()
	m.closeJiraComment()
	m.glConfirm = glConfirmState{}
	m.glJobsExpanded = false
	if m.focus == focusRef {
		m.focus = focusMessages
	}
	m.resizeMessagesViewport()
}

// cycleRef moves to the previous/next reference named on the source post and
// loads it. No-op when the post named a single reference.
func (m Model) cycleRef(delta int) (tea.Model, tea.Cmd) {
	n := len(m.refs)
	if n <= 1 {
		return m, nil
	}
	m.refIdx = ((m.refIdx+delta)%n + n) % n
	m.status = refStatusHint(m.refs[m.refIdx], n)
	cmd := m.loadCurrentRef()
	return m, cmd
}

// refreshRef drops the cached copy of the shown reference and refetches it.
func (m Model) refreshRef() (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if r == nil {
		return m, nil
	}
	switch r.kind {
	case refJira:
		m.jiraClient.Invalidate(r.jiraKey)
	case refGitLab:
		m.glClient.Invalidate(r.glProj, r.glIID)
	}
	cmd := m.loadCurrentRef()
	return m, cmd
}

// openCurrentRefURL opens the shown reference in a browser.
func (m Model) openCurrentRefURL() (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if r == nil {
		return m, nil
	}
	var o openable
	switch {
	case r.kind == refJira && m.jiraIssue != nil:
		o = openable{name: m.jiraIssue.Key, url: m.jiraIssue.URL}
	case r.kind == refGitLab && m.glMR != nil:
		o = openable{name: r.label(), url: m.glMR.WebURL}
	}
	if o.url == "" {
		return m, nil
	}
	m.status = "opening " + o.url + "…"
	return m, m.openOpenable(o)
}

// handleRefKey owns every keystroke while the panel has focus: esc / the
// open-reference key close it, ←/→ cycle references, r refetches, o opens it in
// a browser, provider-specific keys edit/act on it, and anything else scrolls
// the viewport.
func (m Model) handleRefKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeRef()
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.OpenRef): // same key that opened it closes it
		m.closeRef()
		return m, nil
	case key.Matches(msg, m.keys.Right):
		return m.cycleRef(1)
	case key.Matches(msg, m.keys.Left):
		return m.cycleRef(-1)
	case key.Matches(msg, m.keys.Refresh):
		return m.refreshRef()
	case key.Matches(msg, m.keys.OpenAttach):
		return m.openCurrentRefURL()
	}
	// Provider-specific keys, only once the data is loaded; otherwise they fall
	// through to scroll the viewport.
	r := m.currentRef()
	if r != nil && r.kind == refJira && m.jiraIssue != nil {
		switch msg.String() {
		case "s":
			return m, m.openJiraStatusPicker()
		case "p":
			return m, m.openJiraPriorityPicker()
		case "P":
			m.openJiraPointsInput()
			return m, nil
		case "a":
			return m, m.openJiraAssigneePicker()
		case "c":
			m.openJiraCommentInput()
			return m, nil
		case "R":
			if len(m.jiraIssue.Comments) == 0 {
				m.status = "no comments to reply to"
				return m, nil
			}
			m.openJiraReplyPicker()
			return m, nil
		}
	}
	if r != nil && r.kind == refGitLab && m.glMR != nil {
		switch msg.String() {
		case "A":
			return m.openGitLabApprove()
		case "M":
			return m.openGitLabMerge()
		case "t":
			m.glJobsExpanded = !m.glJobsExpanded
			m.refView.GotoTop()
			m.renderRef()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.refView, cmd = m.refView.Update(msg)
	return m, cmd
}

// renderRef rebuilds the panel viewport's content (loading / error / the
// provider's rendered detail).
func (m *Model) renderRef() {
	if !m.refOpen {
		return
	}
	// New content generation: invalidates the ref scroll-geometry cache.
	m.refContentVer++
	r := m.currentRef()
	switch {
	case m.refErr != nil:
		m.refView.SetContent(refErrStyle.Render(m.refErr.Error()))
	case m.refLoading || r == nil:
		label := ""
		if r != nil {
			label = r.label()
		}
		m.refView.SetContent(refDimStyle.Render("loading " + label + "…"))
	case r.kind == refJira && m.jiraIssue != nil:
		m.refView.SetContent(m.refHover(expandTables(m.renderJiraIssue(m.jiraIssue, m.refView.Width()), m.refView.Width())))
	case r.kind == refGitLab && m.glMR != nil:
		m.refView.SetContent(m.refHover(expandTables(m.renderGitLabMR(m.glMR, m.refView.Width()), m.refView.Width())))
	default:
		m.refView.SetContent(refDimStyle.Render("loading…"))
	}
}

// refHover paints the hovered link's background when the pointer rests on a
// link in this panel, mirroring the message pane's hover highlight. The content
// is still unwrapped here (the viewport soft-wraps it on display), so each
// link's OSC 8 open/inner/close is contiguous for highlightLink. A no-op unless
// the hovered link belongs to this pane.
func (m *Model) refHover(content string) string {
	if m.hoverLink.pane == focusRef && m.hoverLink.url != "" {
		return highlightLink(content, m.hoverLink.url, mdLinkHoverStyle)
	}
	return content
}

// refMeta writes one aligned "label: value" row, skipping empty values. width
// is the column the values align to (label + colon, space-padded).
func refMeta(b *strings.Builder, label, value string, width int) {
	if value == "" {
		return
	}
	lbl := label + ":"
	pad := width - len(lbl)
	if pad < 1 {
		pad = 1
	}
	b.WriteString(refLabelStyle.Render(lbl) + strings.Repeat(" ", pad) + value + "\n")
}

// renderRefPane draws the bordered side pane: a title row + the scrollable
// detail viewport, with the shared right-border/scrollbar treatment. Mirrors
// renderThreadPane but has no composer.
func (m *Model) renderRefPane(height, width int) string {
	innerH := height
	if innerH < 1 {
		innerH = 1
	}
	if width < threadPaneMinWidth {
		width = threadPaneMinWidth
	}

	title := m.refPaneTitle()

	total, pct := m.refScrollGeom()
	showScrollbar := total > m.refView.Height() && pct < 1.0

	content := lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render(title), m.refView.View())

	borderColor := dimColor
	if m.focus == focusRef {
		borderColor = focusedColor
	}
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().UnsetBorderBottom().
		Width(width - 1).Height(innerH).BorderForeground(borderColor)
	box := style.Render(content)

	rightBorder := renderRightBorder(innerH, 1, m.refView.Height(), total, pct, borderColor, showScrollbar, -1)
	return lipgloss.JoinHorizontal(lipgloss.Top, box, rightBorder)
}

// refPaneTitle is the pane's heading: the provider name, plus an X/Y counter
// when several references are open.
func (m *Model) refPaneTitle() string {
	name := "Reference"
	if r := m.currentRef(); r != nil {
		switch r.kind {
		case refJira:
			name = "Jira"
		case refGitLab:
			name = "GitLab"
		}
	}
	switch {
	case m.refLoading && m.jiraIssue == nil && m.glMR == nil:
		return name + " (loading…)"
	case len(m.refs) > 1:
		return fmt.Sprintf("%s · %d/%d", name, m.refIdx+1, len(m.refs))
	}
	return name
}
