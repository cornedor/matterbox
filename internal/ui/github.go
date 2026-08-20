package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"matterbox/internal/github"
)

// githubLoadedMsg carries a finished background fetch. gen guards a stale
// result the user already cycled/closed past.
type githubLoadedMsg struct {
	gen    int
	repo   string
	number int
	item   *github.Item
	err    error
}

// githubMutatedMsg carries the result of an approve / merge action.
type githubMutatedMsg struct {
	repo   string
	number int
	action string // "approve" / "merge"
	err    error
}

var (
	ghGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	ghRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	ghYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	ghOrange = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	ghPurple = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	ghCyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

// fetchGitHub fetches (and caches) an issue / pull-request in the
// background, returning a githubLoadedMsg tagged with gen.
func (m Model) fetchGitHub(gen int, repo string, number int) tea.Cmd {
	client := m.ghClient
	ctx := m.ctx
	return func() tea.Msg {
		item, err := client.Get(ctx, repo, number)
		return githubLoadedMsg{gen: gen, repo: repo, number: number, item: item, err: err}
	}
}

// handleGitHubLoaded installs a finished fetch unless the panel has since
// moved to a non-GitHub ref.
func (m Model) handleGitHubLoaded(msg githubLoadedMsg) (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if !m.refOpen || msg.gen != m.refGen || r == nil || r.kind != refGitHub {
		return m, nil
	}
	if r.ghRepo != msg.repo || r.ghNumber != msg.number {
		return m, nil
	}

	m.refLoading = false
	if msg.err != nil {
		m.refErr = msg.err
		m.ghItem = nil
	} else {
		m.refErr = nil
		m.ghItem = msg.item
	}
	m.renderRef()
	return m, nil
}

// renderGitHubItem formats one issue/PR for the viewport.
func (m *Model) renderGitHubItem(it *github.Item, width int) string {
	var b strings.Builder

	b.WriteString(refKeyStyle.Render(fmt.Sprintf("#%d", it.Number)) + "  " + ghStateBadge(it) + "\n")
	if it.Title != "" {
		b.WriteString(titleStyle.Render(it.Title) + "\n")
	}
	b.WriteString(refDimStyle.Render(it.Repo) + "\n\n")

	if it.IsPull {
		if it.SourceBranch != "" || it.TargetBranch != "" {
			refMeta(&b, "Branch", it.SourceBranch+" -> "+it.TargetBranch, 12)
		}
		refMeta(&b, "Author", it.Author, 12)
		if ms := ghMergeText(it); ms != "" {
			refMeta(&b, "Merge", ms, 12)
		}
		if it.ChangedFiles > 0 {
			refMeta(&b, "Changes", fmt.Sprintf("%d files", it.ChangedFiles), 12)
		}
		if !it.UpdatedAt.IsZero() {
			refMeta(&b, "Updated", it.UpdatedAt.Format("2006-01-02 15:04"), 12)
		}
		refMeta(&b, "Checks", ghChecksText(it.ChecksState), 12)
	} else {
		refMeta(&b, "Author", it.Author, 12)
		if len(it.Labels) > 0 {
			refMeta(&b, "Labels", strings.Join(it.Labels, ", "), 12)
		}
		if !it.UpdatedAt.IsZero() {
			refMeta(&b, "Updated", it.UpdatedAt.Format("2006-01-02 15:04"), 12)
		}
	}

	if it.IsPull && it.State == "open" && !it.Draft {
		hint := helpKey(m.keys.GitHubApprove) + " approve · " + helpKey(m.keys.GitHubMerge) + " merge"
		b.WriteString("\n" + refDimStyle.Render(hint) + "\n")
	}

	b.WriteString("\n" + refDimStyle.Render(helpKey(m.keys.OpenAttach)+" browser · "+helpKey(m.keys.Refresh)+" refresh") + "\n")

	if desc := strings.TrimSpace(it.Body); desc != "" {
		divW := width
		if divW < 1 {
			divW = 1
		}
		b.WriteString("\n" + refDimStyle.Render(strings.Repeat("─", divW)) + "\n")
		b.WriteString(renderMarkdown(desc, m.emojiImg, nil, ""))
	}

	return b.String()
}

// ghStateBadge renders the issue/PR state as a coloured chip.
func ghStateBadge(it *github.Item) string {
	if it.IsPull && it.Draft {
		return ghYellow.Render("draft")
	}
	switch it.State {
	case "open":
		return ghCyan.Render("open")
	case "closed":
		return ghRed.Render("closed")
	case "merged":
		return ghPurple.Render("merged")
	}
	return refDimStyle.Render(it.State)
}

// ghMergeText is the merge-readiness line with GitHub-like colouring.
func ghMergeText(it *github.Item) string {
	if it == nil || !it.IsPull {
		return ""
	}
	if it.State == "merged" {
		return ghPurple.Render("merged")
	}
	if it.State != "open" {
		return refDimStyle.Render("n/a")
	}
	switch it.MergeableState {
	case "clean":
		return ghGreen.Render("mergeable")
	case "dirty":
		return ghOrange.Render("conflicts")
	case "blocked":
		return ghRed.Render("blocked")
	case "behind":
		return ghYellow.Render("behind")
	case "unstable":
		return ghRed.Render("checks failing")
	case "unknown", "":
		return refDimStyle.Render("unknown")
	default:
		return ghYellow.Render(strings.ReplaceAll(it.MergeableState, "_", " "))
	}
}

// ghChecksText renders combined CI status for the PR head commit.
func ghChecksText(state string) string {
	switch state {
	case "success":
		return ghGreen.Render("passing")
	case "pending":
		return ghYellow.Render("pending")
	case "failure":
		return ghRed.Render("failing")
	case "error":
		return ghRed.Render("error")
	case "":
		return refDimStyle.Render("none")
	default:
		return refDimStyle.Render(state)
	}
}

// humanGhMergeState turns mergeable_state into a short blocking reason.
func humanGhMergeState(it *github.Item) string {
	if it == nil {
		return "not mergeable"
	}
	switch it.MergeableState {
	case "dirty":
		return "conflicts"
	case "blocked":
		return "blocked"
	case "behind":
		return "behind base branch"
	case "unstable":
		return "checks failing"
	case "unknown", "":
		return "not mergeable"
	default:
		return strings.ReplaceAll(it.MergeableState, "_", " ")
	}
}

// openGitHubApprove raises the shared approve/merge confirm for the shown PR.
func (m Model) openGitHubApprove() (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if r == nil || r.kind != refGitHub || m.ghItem == nil || !m.ghItem.IsPull {
		return m, nil
	}
	if m.ghItem.State != "open" {
		m.status = "cannot approve " + r.label() + ": PR is " + m.ghItem.State
		return m, nil
	}
	m.glConfirm = glConfirmState{
		active: true,
		action: "approve",
		kind:   refGitHub,
		repo:   r.ghRepo,
		number: r.ghNumber,
		title:  "Approve " + r.label() + "?",
	}
	return m, nil
}

// openGitHubMerge raises the shared confirm when GitHub reports the PR mergeable.
func (m Model) openGitHubMerge() (tea.Model, tea.Cmd) {
	r := m.currentRef()
	if r == nil || r.kind != refGitHub || m.ghItem == nil || !m.ghItem.IsPull {
		return m, nil
	}
	if !m.ghItem.Mergeable() {
		m.status = "cannot merge " + r.label() + ": " + humanGhMergeState(m.ghItem)
		return m, nil
	}
	m.glConfirm = glConfirmState{
		active: true,
		action: "merge",
		kind:   refGitHub,
		repo:   r.ghRepo,
		number: r.ghNumber,
		title:  fmt.Sprintf("Merge %s into %s?", r.label(), m.ghItem.TargetBranch),
	}
	return m, nil
}

// applyGitHubConfirm fires a GitHub approve / merge in the background.
func (m Model) applyGitHubConfirm(c glConfirmState) (tea.Model, tea.Cmd) {
	client, ctx := m.ghClient, m.ctx
	var run func() error
	switch c.action {
	case "approve":
		run = func() error { return client.Approve(ctx, c.repo, c.number) }
	case "merge":
		run = func() error { return client.Merge(ctx, c.repo, c.number) }
	default:
		return m, nil
	}
	m.status = fmt.Sprintf("%s %s#%d…", c.action, c.repo, c.number)
	return m, githubMutateCmd(c.repo, c.number, c.action, run)
}

func githubMutateCmd(repo string, number int, action string, run func() error) tea.Cmd {
	return func() tea.Msg {
		return githubMutatedMsg{repo: repo, number: number, action: action, err: run()}
	}
}

// handleGitHubMutated reports the action outcome and reloads the PR on success.
func (m Model) handleGitHubMutated(msg githubMutatedMsg) (tea.Model, tea.Cmd) {
	label := fmt.Sprintf("%s#%d", msg.repo, msg.number)
	if msg.err != nil {
		m.status = fmt.Sprintf("%s %s failed: %v", label, msg.action, msg.err)
		return m, nil
	}
	m.status = fmt.Sprintf("%s %sd", label, msg.action)
	if r := m.currentRef(); r != nil && r.kind == refGitHub && r.ghRepo == msg.repo && r.ghNumber == msg.number {
		return m, m.loadCurrentRef()
	}
	return m, nil
}
