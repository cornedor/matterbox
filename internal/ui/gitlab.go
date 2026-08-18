package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"matterbox/internal/gitlab"
)

// GitLab half of the reference panel: fetching a merge request, rendering it
// (with the CI pipeline breakdown), and the approve / merge actions. The shared
// panel machinery (open/close/cycle/keys/layout) lives in ref.go.

// gitlabLoadedMsg carries a finished background fetch. gen guards a stale result
// the user already cycled or closed past (see Model.refGen).
type gitlabLoadedMsg struct {
	gen     int
	project string
	iid     int
	mr      *gitlab.MR
	err     error
}

// gitlabMutatedMsg carries the result of an approve / merge action. On success
// the panel reloads the MR (the client already invalidated its cache).
type gitlabMutatedMsg struct {
	project string
	iid     int
	action  string // "approve" / "merge"
	err     error
}

// glConfirmState is the modal yes/no shown before an approve or merge fires. It
// owns every keystroke while active (dispatched in update.go before the
// focus-based routing) and overlays the screen (view.go).
type glConfirmState struct {
	active  bool
	action  string // "approve" / "merge"
	project string
	iid     int
	title   string
}

var (
	glGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	glRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	glYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	glPurple = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)

// fetchGitLabMR fetches (and caches) a merge request in the background,
// returning a gitlabLoadedMsg tagged with gen.
func (m Model) fetchGitLabMR(gen int, project string, iid int) tea.Cmd {
	client, ctx := m.glClient, m.ctx
	return func() tea.Msg {
		mr, err := client.Get(ctx, project, iid)
		return gitlabLoadedMsg{gen: gen, project: project, iid: iid, mr: mr, err: err}
	}
}

// handleGitLabLoaded installs a finished fetch, unless the user has since cycled
// or closed the panel (stale gen) or moved to a non-GitLab reference.
func (m Model) handleGitLabLoaded(msg gitlabLoadedMsg) (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if !m.refOpen || msg.gen != m.refGen || r == nil || r.kind != refGitLab {
		return m, nil
	}
	m.refLoading = false
	if msg.err != nil {
		m.refErr = msg.err
		m.glMR = nil
	} else {
		m.refErr = nil
		m.glMR = msg.mr
	}
	m.renderRef()
	return m, nil
}

// renderGitLabMR formats one merge request for the viewport: an iid + state
// header, the title, an aligned meta block, the pipeline breakdown, the action
// hint, then the description through the shared markdown renderer.
func (m *Model) renderGitLabMR(mr *gitlab.MR, width int) string {
	var b strings.Builder

	b.WriteString(refKeyStyle.Render(fmt.Sprintf("!%d", mr.IID)) + "  " + glStateBadge(mr) + "\n")
	if mr.Title != "" {
		b.WriteString(titleStyle.Render(mr.Title) + "\n")
	}
	b.WriteString(refDimStyle.Render(mr.Project) + "\n\n")

	if mr.SourceBranch != "" || mr.TargetBranch != "" {
		refMeta(&b, "Branch", mr.SourceBranch+" → "+mr.TargetBranch, 12)
	}
	refMeta(&b, "Author", mr.Author, 12)
	if len(mr.Assignees) > 0 {
		refMeta(&b, "Assignees", strings.Join(mr.Assignees, ", "), 12)
	}
	if len(mr.Reviewers) > 0 {
		refMeta(&b, "Reviewers", strings.Join(mr.Reviewers, ", "), 12)
	}
	refMeta(&b, "Merge", glMergeText(mr), 12)
	if a := glApprovalsText(mr.Approvals); a != "" {
		refMeta(&b, "Approvals", a, 12)
	}
	if len(mr.Labels) > 0 {
		refMeta(&b, "Labels", strings.Join(mr.Labels, ", "), 12)
	}
	if mr.ChangesCount != "" {
		refMeta(&b, "Changes", mr.ChangesCount+" files", 12)
	}
	if !mr.UpdatedAt.IsZero() {
		refMeta(&b, "Updated", mr.UpdatedAt.Format("2006-01-02 15:04"), 12)
	}

	b.WriteString("\n" + renderPipeline(mr.Pipeline, m.glJobsExpanded) + "\n")
	// Affordance line, in the same static "<key> <action>" style as the Jira
	// panel. Merge readiness lives in the "Merge:" row above; pressing M when
	// the MR isn't mergeable reports the reason in the status bar. The jobs
	// toggle is offered only when some stage is actually truncated.
	hint := helpKey(m.keys.GitLabApprove) + " approve · " + helpKey(m.keys.GitLabMerge) + " merge"
	if hasTruncatedStage(mr.Pipeline) {
		if m.glJobsExpanded {
			hint += " · " + helpKey(m.keys.GitLabJobs) + " fewer jobs"
		} else {
			hint += " · " + helpKey(m.keys.GitLabJobs) + " all jobs"
		}
	}
	b.WriteString("\n" + refDimStyle.Render(hint) + "\n")

	if desc := strings.TrimSpace(mr.Description); desc != "" {
		divW := width
		if divW < 1 {
			divW = 1
		}
		b.WriteString("\n" + refDimStyle.Render(strings.Repeat("─", divW)) + "\n")
		b.WriteString(renderMarkdown(desc, m.emojiImg, nil, ""))
	}
	return b.String()
}

// maxJobsPerStage caps how many jobs each stage lists when collapsed (the
// default), so a long pipeline stays readable. The `t` key toggles expansion.
const maxJobsPerStage = 3

// renderPipeline draws the head pipeline's overall status and its per-stage job
// breakdown. Stages and jobs keep the pipeline's own order. When not expanded,
// each stage lists at most maxJobsPerStage jobs with a "… N more" marker; the
// stage header always carries an aggregate status glyph so a hidden failing job
// is still visible at a glance.
func renderPipeline(p *gitlab.Pipeline, expanded bool) string {
	if p == nil {
		return refLabelStyle.Render("Pipeline:") + "   " + refDimStyle.Render("none")
	}
	label := p.Label
	if label == "" {
		label = p.Status
	}
	glyph, style := glStatusGlyph(p.Status)
	head := refLabelStyle.Render("Pipeline:") + "   " + style.Render(glyph+" "+label)
	if p.Duration > 0 {
		head += refDimStyle.Render("  (" + humanDur(p.Duration) + ")")
	}

	var b strings.Builder
	b.WriteString(head)
	for _, stage := range p.Stages {
		sg, sst := glStatusGlyph(stageStatus(stage.Jobs))
		b.WriteString("\n  " + sst.Render(sg) + " " + refDimStyle.Render(stage.Name))
		jobs := stage.Jobs
		hidden := 0
		if !expanded && len(jobs) > maxJobsPerStage {
			hidden = len(jobs) - maxJobsPerStage
			jobs = jobs[:maxJobsPerStage]
		}
		for _, j := range jobs {
			jg, jst := glStatusGlyph(j.Status)
			b.WriteString("\n      " + jst.Render(jg) + " " + j.Name)
		}
		if hidden > 0 {
			b.WriteString("\n      " + refDimStyle.Render(fmt.Sprintf("… %d more", hidden)))
		}
	}
	return b.String()
}

// hasTruncatedStage reports whether any stage would hide jobs when collapsed —
// i.e. whether offering the jobs toggle is meaningful.
func hasTruncatedStage(p *gitlab.Pipeline) bool {
	if p == nil {
		return false
	}
	for _, s := range p.Stages {
		if len(s.Jobs) > maxJobsPerStage {
			return true
		}
	}
	return false
}

// stageStatus collapses a stage's jobs into one status, worst-wins, so the stage
// header reflects a failing/running job even when it's below the collapsed cut.
// Order of severity: failed > running > queued > success > manual > canceled >
// skipped; an empty stage reports success.
func stageStatus(jobs []gitlab.Job) string {
	rank := map[string]int{
		"failed": 7, "running": 6,
		"pending": 5, "created": 5, "scheduled": 5, "preparing": 5, "waiting_for_resource": 5,
		"success": 4, "manual": 3, "canceled": 2, "canceling": 2, "skipped": 1,
	}
	worst, worstRank := "success", 0
	for _, j := range jobs {
		r, ok := rank[j.Status]
		if !ok {
			r = 5 // unknown ~ in-progress, surfaced rather than hidden as success
		}
		if r > worstRank {
			worst, worstRank = j.Status, r
		}
	}
	return worst
}

// glStatusGlyph maps a CI status to a glyph and a colour style.
func glStatusGlyph(status string) (string, lipgloss.Style) {
	switch status {
	case "success":
		return "✓", glGreen
	case "failed":
		return "✗", glRed
	case "running":
		return "●", glYellow
	case "canceled", "canceling":
		return "⊘", refDimStyle
	case "skipped":
		return "»", refDimStyle
	case "manual":
		return "⊙", refDimStyle
	default: // created / pending / scheduled / preparing / waiting_for_resource
		return "○", refDimStyle
	}
}

// glStateBadge renders the MR state (or "draft") as a coloured chip.
func glStateBadge(mr *gitlab.MR) string {
	if mr.Draft {
		return glYellow.Render("draft")
	}
	switch mr.State {
	case "opened":
		return glGreen.Render("open")
	case "merged":
		return glPurple.Render("merged")
	case "closed":
		return glRed.Render("closed")
	case "locked":
		return refDimStyle.Render("locked")
	}
	return refDimStyle.Render(mr.State)
}

// glMergeText is the merge-readiness line: green when mergeable, otherwise the
// humanized blocking reason (plus an explicit conflicts note).
func glMergeText(mr *gitlab.MR) string {
	if mr.Mergeable() {
		return glGreen.Render("mergeable")
	}
	txt := humanDMS(mr.DetailedMergeStatus)
	if txt == "" {
		txt = "not mergeable"
	}
	if mr.HasConflicts && !strings.Contains(txt, "conflict") {
		txt += " · conflicts"
	}
	return glYellow.Render(txt)
}

// glApprovalsText summarizes approvals, or "" when the instance/project has
// none configured (nothing useful to show).
func glApprovalsText(a *gitlab.Approvals) string {
	if a == nil {
		return ""
	}
	var txt string
	switch {
	case a.Required > 0:
		txt = fmt.Sprintf("%d/%d", a.Required-a.Left, a.Required)
	case a.Approved:
		txt = "approved"
	case len(a.By) > 0:
		txt = fmt.Sprintf("%d", len(a.By))
	default:
		return ""
	}
	if len(a.By) > 0 {
		txt += " (" + strings.Join(a.By, ", ") + ")"
	}
	return txt
}

// humanDMS turns a detailed_merge_status token (ci_still_running) into prose
// (ci still running).
func humanDMS(s string) string {
	return strings.ReplaceAll(s, "_", " ")
}

// humanDur formats a duration in seconds as e.g. "11s" or "4m11s".
func humanDur(sec int) string {
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	return fmt.Sprintf("%dm%ds", sec/60, sec%60)
}

// openGitLabApprove raises the approve confirm for the shown MR.
func (m Model) openGitLabApprove() (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if r == nil || r.kind != refGitLab || m.glMR == nil {
		return m, nil
	}
	if m.glMR.State != "opened" {
		m.status = "cannot approve " + r.label() + ": MR is " + m.glMR.State
		return m, nil
	}
	m.glConfirm = glConfirmState{
		active:  true,
		action:  "approve",
		project: r.glProj,
		iid:     r.glIID,
		title:   "Approve " + r.label() + "?",
	}
	return m, nil
}

// openGitLabMerge raises the merge confirm, but only when GitLab reports the MR
// mergeable; otherwise it reports the blocking reason and does nothing.
func (m Model) openGitLabMerge() (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if r == nil || r.kind != refGitLab || m.glMR == nil {
		return m, nil
	}
	if !m.glMR.Mergeable() {
		m.status = "cannot merge " + r.label() + ": " + humanDMS(m.glMR.DetailedMergeStatus)
		return m, nil
	}
	m.glConfirm = glConfirmState{
		active:  true,
		action:  "merge",
		project: r.glProj,
		iid:     r.glIID,
		title:   fmt.Sprintf("Merge %s into %s?", r.label(), m.glMR.TargetBranch),
	}
	return m, nil
}

// handleGitLabConfirmKey owns every keystroke while the confirm modal is open:
// y/enter fires the action, n/esc cancels.
func (m Model) handleGitLabConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "y", "Y", "enter":
		return m.applyGitLabConfirm()
	case "n", "N", "esc":
		m.glConfirm = glConfirmState{}
		return m, nil
	}
	return m, nil
}

// applyGitLabConfirm closes the modal and fires the chosen action in the
// background.
func (m Model) applyGitLabConfirm() (tea.Model, tea.Cmd) {
	c := m.glConfirm
	m.glConfirm = glConfirmState{}
	client, ctx := m.glClient, m.ctx
	var run func() error
	switch c.action {
	case "approve":
		run = func() error { return client.Approve(ctx, c.project, c.iid) }
	case "merge":
		run = func() error { return client.Merge(ctx, c.project, c.iid, true) }
	default:
		return m, nil
	}
	m.status = fmt.Sprintf("%s %s!%d…", c.action, c.project, c.iid)
	return m, gitlabMutateCmd(c.project, c.iid, c.action, run)
}

// gitlabMutateCmd runs an action in the background and reports the result.
func gitlabMutateCmd(project string, iid int, action string, run func() error) tea.Cmd {
	return func() tea.Msg {
		return gitlabMutatedMsg{project: project, iid: iid, action: action, err: run()}
	}
}

// handleGitLabMutated reports the action outcome and reloads the MR on success
// so the panel shows the authoritative (and any cascading) state.
func (m Model) handleGitLabMutated(msg gitlabMutatedMsg) (tea.Model, tea.Cmd) {
	label := fmt.Sprintf("%s!%d", msg.project, msg.iid)
	if msg.err != nil {
		m.status = fmt.Sprintf("%s %s failed: %v", label, msg.action, msg.err)
		return m, nil
	}
	m.status = fmt.Sprintf("%s %sd", label, msg.action) // approved / merged
	if r := m.currentRef(); r != nil && r.kind == refGitLab && r.glProj == msg.project && r.glIID == msg.iid {
		return m, m.loadCurrentRef()
	}
	return m, nil
}

// renderGitLabConfirm draws the centred yes/no modal for an approve / merge.
func (m *Model) renderGitLabConfirm() string {
	if !m.glConfirm.active {
		return ""
	}
	outerW := 50
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 28 {
		outerW = 28
	}
	inner := outerW - 8
	if inner < 1 {
		inner = 1
	}
	header := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).Render(m.glConfirm.title)
	hint := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Foreground(dimColor).Italic(true).Render("y confirm · n cancel")
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", hint)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(focusedColor).Padding(1, 3).Render(body)
}
