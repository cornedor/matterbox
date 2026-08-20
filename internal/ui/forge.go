package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"matterbox/internal/forge"
)

// The forge half of the reference panel: fetching a change request (a GitLab
// merge request, a GitHub pull request), rendering it with its CI breakdown, and
// the approve / merge actions. Nothing here names a forge — every difference
// (the API, the short-ref form, the merge strategies on offer) lives behind
// forge.Provider, so a new forge is a new subpackage of internal/forge and one
// config block, not a change to this file. The shared panel machinery
// (open/close/cycle/keys/layout) lives in ref.go.

// forgeLoadedMsg carries a finished background fetch. gen guards a stale result
// the user already cycled or closed past (see Model.refGen); provider is the
// index into Model.forges that answered.
type forgeLoadedMsg struct {
	gen      int
	provider int
	repo     string
	number   int
	change   *forge.Change
	err      error
}

// forgeMutatedMsg carries the result of an approve / merge action. On success
// the panel reloads the change request (the provider already invalidated its
// cache).
type forgeMutatedMsg struct {
	provider int
	repo     string
	number   int
	label    string // the forge's short form, for the status line
	action   string // "approve" / "merge"
	err      error
}

// refConfirmState is the modal shown before an approve or merge fires. It owns
// every keystroke while active (dispatched in update.go before the focus-based
// routing) and overlays the screen (view.go).
//
// An approve — and a merge on a forge with a single merge strategy — is a plain
// y/n. When the forge offers several strategies (GitHub's merge commit / squash
// / rebase) methods is non-empty and each one's key picks it, so the choice and
// the confirmation are one keystroke rather than two.
type refConfirmState struct {
	active   bool
	action   string // "approve" / "merge"
	provider int
	repo     string
	number   int
	title    string
	methods  []forge.MergeMethod
}

var (
	fgGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	fgRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	fgYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	fgPurple = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)

// forgeAt returns the provider at index i, or nil when the index is stale (the
// configured set is fixed at startup, so this only guards against a message
// arriving after a rebuild in tests).
func (m *Model) forgeAt(i int) forge.Provider {
	if i < 0 || i >= len(m.forges) {
		return nil
	}
	return m.forges[i]
}

// fetchForgeChange fetches (and caches) a change request in the background,
// returning a forgeLoadedMsg tagged with gen. kind is a detect-time hint
// (forge.KindPull / KindIssue / empty) forwarded to Provider.Get.
func (m Model) fetchForgeChange(gen, provider int, repo string, number int, kind string) tea.Cmd {
	p, ctx := m.forgeAt(provider), m.ctx
	return func() tea.Msg {
		msg := forgeLoadedMsg{gen: gen, provider: provider, repo: repo, number: number}
		if p == nil {
			msg.err = forge.ErrNotConfigured
			return msg
		}
		msg.change, msg.err = p.Get(ctx, repo, number, kind)
		return msg
	}
}

// handleForgeLoaded installs a finished fetch, unless the user has since cycled
// or closed the panel (stale gen) or moved to a non-forge reference.
func (m Model) handleForgeLoaded(msg forgeLoadedMsg) (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if !m.refOpen || msg.gen != m.refGen || r == nil || r.kind != refForge {
		return m, nil
	}
	m.refLoading = false
	if msg.err != nil {
		m.refErr = msg.err
		m.refChange = nil
	} else {
		m.refErr = nil
		m.refChange = msg.change
		m.status = m.refStatusHint(*r, len(m.refs))
	}
	m.renderRef()
	return m, nil
}

// renderForgeChange formats one change request for the viewport: a number +
// state header, the title, an aligned meta block, the CI breakdown, the action
// hint, then the description through the shared markdown renderer.
func (m *Model) renderForgeChange(p forge.Provider, ch *forge.Change, width int) string {
	var b strings.Builder

	sigil := "#"
	if p != nil {
		sigil = p.Sigil()
	}
	b.WriteString(refKeyStyle.Render(fmt.Sprintf("%s%d", sigil, ch.Number)) + "  " + changeStateBadge(ch) + "\n")
	if ch.Title != "" {
		b.WriteString(titleStyle.Render(ch.Title) + "\n")
	}
	b.WriteString(refDimStyle.Render(ch.Repo) + "\n\n")

	if ch.SourceBranch != "" || ch.TargetBranch != "" {
		refMeta(&b, "Branch", ch.SourceBranch+" → "+ch.TargetBranch, 12)
	}
	refMeta(&b, "Author", ch.Author, 12)
	if len(ch.Assignees) > 0 {
		refMeta(&b, "Assignees", strings.Join(ch.Assignees, ", "), 12)
	}
	if len(ch.Reviewers) > 0 {
		refMeta(&b, "Reviewers", strings.Join(ch.Reviewers, ", "), 12)
	}
	if !ch.IsIssue {
		refMeta(&b, "Merge", mergeText(ch), 12)
		if a := approvalsText(ch.Approvals); a != "" {
			refMeta(&b, "Approvals", a, 12)
		}
	}
	if len(ch.Labels) > 0 {
		refMeta(&b, "Labels", strings.Join(ch.Labels, ", "), 12)
	}
	if ch.ChangesCount != "" {
		refMeta(&b, "Changes", ch.ChangesCount+" files", 12)
	}
	if !ch.UpdatedAt.IsZero() {
		refMeta(&b, "Updated", ch.UpdatedAt.Format("2006-01-02 15:04"), 12)
	}

	if !ch.IsIssue {
		b.WriteString("\n" + renderChecks(checksHeading(p), ch.Checks, m.refJobsExpanded) + "\n")
	}
	// Affordance line, in the same static "<key> <action>" style as the Jira
	// panel. Merge readiness lives in the "Merge:" row above; pressing the merge
	// key when the forge won't merge reports the reason in the status bar. The
	// jobs toggle is offered only when some group is actually truncated.
	var hint string
	if ch.IsIssue {
		hint = "issue (read-only)"
	} else {
		hint = helpKey(m.keys.RefApprove) + " approve · " + helpKey(m.keys.RefMerge) + " merge"
		if hasTruncatedGroup(ch.Checks) {
			if m.refJobsExpanded {
				hint += " · " + helpKey(m.keys.RefJobs) + " fewer jobs"
			} else {
				hint += " · " + helpKey(m.keys.RefJobs) + " all jobs"
			}
		}
	}
	b.WriteString("\n" + refDimStyle.Render(hint) + "\n")

	if desc := strings.TrimSpace(ch.Description); desc != "" {
		divW := width
		if divW < 1 {
			divW = 1
		}
		b.WriteString("\n" + refDimStyle.Render(strings.Repeat("─", divW)) + "\n")
		b.WriteString(renderMarkdown(desc, m.emojiImg, nil, ""))
	}
	return b.String()
}

// maxJobsPerGroup caps how many jobs each group lists when collapsed (the
// default), so a long pipeline stays readable. The jobs key toggles expansion.
const maxJobsPerGroup = 3

// checksHeading is the forge's word for its CI section ("Pipeline", "Checks"),
// falling back to the neutral one when the provider is unknown.
func checksHeading(p forge.Provider) string {
	if p == nil || p.ChecksHeading() == "" {
		return "Checks"
	}
	return p.ChecksHeading()
}

// renderChecks draws the CI verdict and its per-group job breakdown, under the
// forge's own heading. Groups and jobs keep the forge's own order — pipeline
// stages on GitLab, check-producing apps on GitHub. When not expanded, each group
// lists at most maxJobsPerGroup jobs with a "… N more" marker; the group header
// always carries an aggregate status glyph so a hidden failing job is still
// visible at a glance.
func renderChecks(heading string, c *forge.Checks, expanded bool) string {
	// The heading shares the meta block's value column, so it lines up with the
	// Merge / Approvals rows above it.
	lbl := refLabelStyle.Render(heading + ":")
	pad := strings.Repeat(" ", max(1, 12-len(heading)-1))
	if c == nil {
		return lbl + pad + refDimStyle.Render("none")
	}
	label := c.Label
	if label == "" {
		label = c.Status
	}
	glyph, style := checkGlyph(c.Status)
	head := lbl + pad + style.Render(glyph+" "+label)
	if c.Duration > 0 {
		head += refDimStyle.Render("  (" + humanDur(c.Duration) + ")")
	}

	var b strings.Builder
	b.WriteString(head)
	for _, group := range c.Groups {
		gg, gst := checkGlyph(forge.WorstStatus(group.Jobs))
		b.WriteString("\n  " + gst.Render(gg) + " " + refDimStyle.Render(group.Name))
		jobs := group.Jobs
		hidden := 0
		if !expanded && len(jobs) > maxJobsPerGroup {
			hidden = len(jobs) - maxJobsPerGroup
			jobs = jobs[:maxJobsPerGroup]
		}
		for _, j := range jobs {
			jg, jst := checkGlyph(j.Status)
			b.WriteString("\n      " + jst.Render(jg) + " " + j.Name)
		}
		if hidden > 0 {
			b.WriteString("\n      " + refDimStyle.Render(fmt.Sprintf("… %d more", hidden)))
		}
	}
	return b.String()
}

// hasTruncatedGroup reports whether any group would hide jobs when collapsed —
// i.e. whether offering the jobs toggle is meaningful.
func hasTruncatedGroup(c *forge.Checks) bool {
	if c == nil {
		return false
	}
	for _, g := range c.Groups {
		if len(g.Jobs) > maxJobsPerGroup {
			return true
		}
	}
	return false
}

// checkGlyph maps a normalized CI status to a glyph and a colour style.
func checkGlyph(status string) (string, lipgloss.Style) {
	switch status {
	case forge.StatusSuccess:
		return "✓", fgGreen
	case forge.StatusFailed:
		return "✗", fgRed
	case forge.StatusRunning:
		return "●", fgYellow
	case forge.StatusCanceled:
		return "⊘", refDimStyle
	case forge.StatusSkipped:
		return "»", refDimStyle
	case forge.StatusManual:
		return "⊙", refDimStyle
	default: // pending
		return "○", refDimStyle
	}
}

// changeStateBadge renders the change request's state (or "draft") as a
// coloured chip.
func changeStateBadge(ch *forge.Change) string {
	if ch.Draft {
		return fgYellow.Render("draft")
	}
	switch ch.State {
	case forge.StateOpen:
		return fgGreen.Render("open")
	case forge.StateMerged:
		return fgPurple.Render("merged")
	case forge.StateClosed:
		return fgRed.Render("closed")
	case forge.StateLocked:
		return refDimStyle.Render("locked")
	}
	return refDimStyle.Render(ch.State)
}

// mergeText is the merge-readiness line: green when the forge would merge it
// now, otherwise the blocking reason in warning yellow. The provider phrases the
// reason; this only colours it.
func mergeText(ch *forge.Change) string {
	txt := ch.MergeStatus
	if txt == "" {
		if ch.Mergeable {
			txt = "mergeable"
		} else {
			txt = "not mergeable"
		}
	}
	if ch.Mergeable {
		return fgGreen.Render(txt)
	}
	return fgYellow.Render(txt)
}

// approvalsText summarizes approvals, or "" when the forge has nothing to
// report. Up to three facts show up, each optional: how much of the approval
// requirement is met (GitLab counts them; GitHub keeps that in branch
// protection, which needs admin rights to read), who has approved, and who is
// blocking or still to answer.
func approvalsText(a *forge.Approvals) string {
	if a == nil {
		return ""
	}
	var parts []string
	switch {
	case a.Required > 0:
		txt := fmt.Sprintf("%d/%d", a.Required-a.Left, a.Required)
		if len(a.By) > 0 {
			txt += " (" + strings.Join(a.By, ", ") + ")"
		}
		parts = append(parts, txt)
	case len(a.By) > 0:
		parts = append(parts, "approved ("+strings.Join(a.By, ", ")+")")
	}
	if len(a.ChangesRequested) > 0 {
		parts = append(parts, fgYellow.Render("changes requested: "+strings.Join(a.ChangesRequested, ", ")))
	}
	if a.Required == 0 && a.Left > 0 {
		parts = append(parts, refDimStyle.Render(fmt.Sprintf("%d pending", a.Left)))
	}
	return strings.Join(parts, " · ")
}

// humanDur formats a duration in seconds as e.g. "11s" or "4m11s".
func humanDur(sec int) string {
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	return fmt.Sprintf("%dm%ds", sec/60, sec%60)
}

// openForgeApprove raises the approve confirm for the shown change request.
func (m Model) openForgeApprove() (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if r == nil || r.kind != refForge || m.refChange == nil {
		return m, nil
	}
	p := m.forgeAt(r.forge)
	if p == nil {
		return m, nil
	}
	if m.refChange.IsIssue {
		m.status = "cannot approve " + r.label(p) + ": it is an issue, not a pull request"
		return m, nil
	}
	if m.refChange.State != forge.StateOpen {
		m.status = "cannot approve " + r.label(p) + ": " + p.Noun() + " is " + m.refChange.State
		return m, nil
	}
	m.refConfirm = refConfirmState{
		active:   true,
		action:   "approve",
		provider: r.forge,
		repo:     r.repo,
		number:   r.number,
		title:    "Approve " + r.label(p) + "?",
	}
	return m, nil
}

// openForgeMerge raises the merge confirm, but only when the forge reports the
// change request mergeable; otherwise it reports the blocking reason and does
// nothing. When the forge offers several merge strategies they ride along on the
// confirm, so picking one is the confirmation.
func (m Model) openForgeMerge() (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if r == nil || r.kind != refForge || m.refChange == nil {
		return m, nil
	}
	p := m.forgeAt(r.forge)
	if p == nil {
		return m, nil
	}
	if m.refChange.IsIssue {
		m.status = "cannot merge " + r.label(p) + ": it is an issue, not a pull request"
		return m, nil
	}
	if !m.refChange.Mergeable {
		reason := m.refChange.MergeStatus
		if reason == "" {
			reason = "not mergeable"
		}
		m.status = "cannot merge " + r.label(p) + ": " + reason
		return m, nil
	}
	m.refConfirm = refConfirmState{
		active:   true,
		action:   "merge",
		provider: r.forge,
		repo:     r.repo,
		number:   r.number,
		title:    fmt.Sprintf("Merge %s into %s?", r.label(p), m.refChange.TargetBranch),
		methods:  p.MergeMethods(),
	}
	return m, nil
}

// handleRefConfirmKey owns every keystroke while the confirm modal is open:
// y/enter fires a single-choice action, a merge strategy's own key picks it,
// n/esc cancels. With several strategies on offer, y/enter deliberately do
// nothing — a stray enter must not merge a branch one way when the user meant
// another.
func (m Model) handleRefConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	pressed := msg.String()
	switch pressed {
	case "ctrl+c":
		return m, tea.Quit
	case "n", "N", "esc":
		m.refConfirm = refConfirmState{}
		return m, nil
	}
	if m.refConfirm.choosing() {
		for _, mm := range m.refConfirm.methods {
			if pressed == mm.Key {
				return m.applyRefConfirm(mm.ID)
			}
		}
		return m, nil
	}
	if pressed == "y" || pressed == "Y" || pressed == "enter" {
		return m.applyRefConfirm(m.refConfirm.defaultMethod())
	}
	return m, nil
}

// choosing reports whether the modal is offering a choice of merge strategies
// rather than a plain yes/no.
func (c refConfirmState) choosing() bool {
	return c.action == "merge" && len(c.methods) > 1
}

// defaultMethod is the merge method a plain confirmation uses: the forge's first
// (and, when choosing() is false, only) one.
func (c refConfirmState) defaultMethod() string {
	if len(c.methods) == 0 {
		return ""
	}
	return c.methods[0].ID
}

// applyRefConfirm closes the modal and fires the chosen action in the
// background.
func (m Model) applyRefConfirm(method string) (tea.Model, tea.Cmd) {
	c := m.refConfirm
	m.refConfirm = refConfirmState{}
	p := m.forgeAt(c.provider)
	if p == nil {
		return m, nil
	}
	ctx := m.ctx
	var run func() error
	switch c.action {
	case "approve":
		run = func() error { return p.Approve(ctx, c.repo, c.number) }
	case "merge":
		run = func() error { return p.Merge(ctx, c.repo, c.number, method) }
	default:
		return m, nil
	}
	label := forge.Label(p, c.repo, c.number)
	m.status = fmt.Sprintf("%s %s…", c.action, label)
	msg := forgeMutatedMsg{provider: c.provider, repo: c.repo, number: c.number, label: label, action: c.action}
	return m, forgeMutateCmd(msg, run)
}

// forgeMutateCmd runs an action in the background and reports the result.
func forgeMutateCmd(msg forgeMutatedMsg, run func() error) tea.Cmd {
	return func() tea.Msg {
		msg.err = run()
		return msg
	}
}

// handleForgeMutated reports the action outcome and reloads the change request
// on success so the panel shows the authoritative (and any cascading) state.
func (m Model) handleForgeMutated(msg forgeMutatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.status = fmt.Sprintf("%s %s failed: %v", msg.label, msg.action, msg.err)
		return m, nil
	}
	m.status = fmt.Sprintf("%s %sd", msg.label, msg.action) // approved / merged
	r := m.currentRef()
	if r != nil && r.kind == refForge && r.forge == msg.provider && r.repo == msg.repo && r.number == msg.number {
		return m, m.loadCurrentRef()
	}
	return m, nil
}

// renderRefConfirm draws the centred modal for an approve / merge: a yes/no
// question, or the merge-strategy choice when the forge offers one.
func (m *Model) renderRefConfirm() string {
	if !m.refConfirm.active {
		return ""
	}
	outerW := 50
	if m.refConfirm.choosing() {
		outerW = 64 // the strategy keys need a wider line than a yes/no
	}
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
	keys := "y confirm · n cancel"
	if m.refConfirm.choosing() {
		parts := make([]string, 0, len(m.refConfirm.methods)+1)
		for _, mm := range m.refConfirm.methods {
			parts = append(parts, mm.Key+" "+mm.Label)
		}
		keys = strings.Join(parts, " · ") + " · n cancel"
	}
	header := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).Render(m.refConfirm.title)
	hint := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Foreground(dimColor).Italic(true).Render(keys)
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", hint)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(focusedColor).Padding(1, 3).Render(body)
}
