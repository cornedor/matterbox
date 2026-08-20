package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/forge"
	"matterbox/internal/forge/github"
	"matterbox/internal/forge/gitlab"
	"matterbox/internal/jira"
)

// The two provider slots every test model carries, in the order newModel builds
// them — a reference names its forge by index, so the tests need the same order.
const (
	forgeGitLab = 0
	forgeGitHub = 1
)

// withForges enables both forges on m (dummy base URLs + tokens) so the open
// path runs. No network call happens unless a returned fetch Cmd is invoked.
func withForges(m Model) Model {
	m.forges = []forge.Provider{
		gitlab.New(gitlab.Config{BaseURL: "https://git.example.com", Token: "tok"}),
		github.New(github.Config{Token: "tok"}),
	}
	return m
}

// configuredForgeModel builds a Model with both forges enabled.
func configuredForgeModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	m.width, m.height = 120, 40
	return withForges(m)
}

// openLoadedChange opens the reference panel on a post linking the change
// request and installs ch as the fetch result, as the real fetch Cmd would.
func openLoadedChange(t *testing.T, m Model, provider int, message string, ch *forge.Change) Model {
	t.Helper()
	post := &model.Post{Message: message}
	m.posts = []*model.Post{post}
	m.postIdx = 0
	m.focus = focusMessages
	updated, _ := m.openRefForPost(post)
	got := updated.(Model)
	if !got.refOpen || len(got.refs) == 0 || got.refs[0].kind != refForge {
		t.Fatalf("expected a forge ref open, got %+v", got.refs)
	}
	if got.refs[0].forge != provider || got.refs[0].number != ch.Number {
		t.Fatalf("ref = %+v, want provider %d number %d", got.refs[0], provider, ch.Number)
	}
	final, _ := got.handleForgeLoaded(forgeLoadedMsg{
		gen: got.refGen, provider: provider, repo: ch.Repo, number: ch.Number, change: ch,
	})
	return final.(Model)
}

const mrLink = "see https://git.example.com/g/p/-/merge_requests/5 please"

const prLink = "see https://github.com/o/r/pull/7 please"

// sampleMR is a mergeable GitLab merge request with a two-stage pipeline.
func sampleMR() *forge.Change {
	return &forge.Change{
		Repo: "g/p", Number: 5, Title: "Fix the widget", State: forge.StateOpen,
		Author: "Ada Lovelace", SourceBranch: "fix/widget", TargetBranch: "main",
		Mergeable: true, MergeStatus: "mergeable", ChangesCount: "3",
		WebURL:      "https://git.example.com/g/p/-/merge_requests/5",
		Description: "Some **bold** detail.",
		Checks: &forge.Checks{
			Status: forge.StatusSuccess, Label: "passed", Duration: 251,
			Groups: []forge.Group{
				{Name: "build", Jobs: []forge.Job{{Name: "compile", Status: forge.StatusSuccess}}},
				{Name: "test", Jobs: []forge.Job{{Name: "lint", Status: forge.StatusFailed}}},
			},
		},
	}
}

// samplePR is the GitHub equivalent: a mergeable pull request with one check
// group and a review verdict.
func samplePR() *forge.Change {
	return &forge.Change{
		Repo: "o/r", Number: 7, Title: "Teach it GitHub", State: forge.StateOpen,
		Author: "grace", SourceBranch: "feat/github", TargetBranch: "main",
		Mergeable: true, MergeStatus: "mergeable", ChangesCount: "12",
		WebURL:      "https://github.com/o/r/pull/7",
		Description: "Adds the *other* forge.",
		Checks: &forge.Checks{
			Status: forge.StatusSuccess, Label: "passed",
			Groups: []forge.Group{
				{Name: "GitHub Actions", Jobs: []forge.Job{{Name: "test", Status: forge.StatusSuccess}}},
			},
		},
		Approvals: &forge.Approvals{Approved: true, By: []string{"linus"}},
	}
}

// TestRefPanelLinkIsClickable: a link in the reference panel (here a merge
// request description) resolves under the pointer and opens on a click — the
// panel used to be inert to the mouse (hitTest returned hitNone), so a rendered
// link could not be clicked while mouse reporting was on. Hovering it also
// highlights it.
func TestRefPanelLinkIsClickable(t *testing.T) {
	url := "https://example.com/docs"
	// mouseModel parks us on a real team tab (so hitTest sees the channel/message
	// layout, not the synthetic Feed/Search panes) with mouse reporting on.
	base := withForges(mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "x"}}))
	mr := sampleMR()
	mr.Description = "Read [the docs](" + url + ") carefully."
	m := openLoadedChange(t, base, forgeGitLab, mrLink, mr)

	// Find the link in the rendered panel: the logical line carrying its OSC 8
	// open marker, and the first display column the link covers.
	width := m.refView.Width()
	lines, starts := m.ensureWrapIndex(focusRef, width)
	li, col := -1, -1
	for i, ln := range lines {
		if !strings.Contains(ln, "\x1b]8;;"+url) {
			continue
		}
		for c := 0; c <= len(ln); c++ {
			if got, ok := m.linkAt(focusRef, i, c); ok && got == url {
				li, col = i, c
				break
			}
		}
		break
	}
	if li < 0 {
		t.Fatalf("link %q not resolvable anywhere in the ref panel", url)
	}

	// Map (logical line, display column) to a screen cell.
	x0, top, _, _, yoff := m.refGeom()
	x := x0 + col%width
	y := top + (starts[li] + col/width - yoff)

	if h := m.hitTest(x, y); h.zone != hitRef {
		t.Fatalf("hitTest over the ref link = zone %v, want hitRef", h.zone)
	}

	out, _ := m.handleMouseClick(click(tea.MouseLeft, x, y))
	got := out.(Model)
	if !strings.Contains(got.status, "opening") || !strings.Contains(got.status, url) {
		t.Fatalf("clicking the ref link did not open it; status=%q", got.status)
	}
	if got.linkConfirm.active {
		t.Fatal("a web link in the ref panel should open without the warning modal")
	}

	// Hovering the same cell records the link (the footer reads hoverLink.url)
	// and highlights it in the panel content.
	out, _ = m.handleMouseMotion(motion(tea.MouseNone, x, y))
	hov := out.(Model)
	if hov.hoverLink.url != url || hov.hoverLink.pane != focusRef {
		t.Fatalf("hover over ref link: hoverLink=%+v, want url=%q pane=focusRef", hov.hoverLink, url)
	}
	if !strings.Contains(hov.refView.GetContent(), bgSGR(panelHoverBg)) {
		t.Fatal("hovered ref link not highlighted in the panel content")
	}
}

func TestForgePanelRendersMergeRequest(t *testing.T) {
	m := openLoadedChange(t, configuredForgeModel(t), forgeGitLab, mrLink, sampleMR())
	pane := m.renderRefPane(30, 60)
	for _, want := range []string{"!5", "GitLab", "Fix the widget", "main", "Pipeline:", "passed", "compile", "lint", "mergeable"} {
		if !strings.Contains(pane, want) {
			t.Errorf("rendered pane missing %q", want)
		}
	}
}

// The same renderer draws a pull request, titled by its own forge and numbered
// with GitHub's "#" rather than GitLab's "!".
func TestForgePanelRendersPullRequest(t *testing.T) {
	m := openLoadedChange(t, configuredForgeModel(t), forgeGitHub, prLink, samplePR())
	pane := m.renderRefPane(30, 60)
	for _, want := range []string{"#7", "GitHub", "Teach it GitHub", "Checks:", "GitHub Actions", "approved (linus)"} {
		if !strings.Contains(pane, want) {
			t.Errorf("rendered pane missing %q:\n%s", want, pane)
		}
	}
	if strings.Contains(pane, "!7") {
		t.Error("a pull request must not be labelled with GitLab's ! sigil")
	}
}

func TestForgeChecksCapJobsPerGroup(t *testing.T) {
	mr := sampleMR()
	// A group with more than the cap, the failing one hidden below the cut.
	mr.Checks.Groups = []forge.Group{{Name: "test", Jobs: []forge.Job{
		{Name: "unit-a", Status: forge.StatusSuccess},
		{Name: "unit-b", Status: forge.StatusSuccess},
		{Name: "unit-c", Status: forge.StatusSuccess},
		{Name: "unit-d", Status: forge.StatusSuccess},
		{Name: "e2e", Status: forge.StatusFailed},
	}}}
	m := openLoadedChange(t, configuredForgeModel(t), forgeGitLab, mrLink, mr)

	collapsed := m.renderRefPane(40, 70)
	if !strings.Contains(collapsed, "… 2 more") {
		t.Errorf("collapsed pane should mark the 2 hidden jobs:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "e2e") {
		t.Error("the 5th job should be hidden when collapsed")
	}
	// Group status must still surface the hidden failure: a red ✗ on the header.
	if !strings.Contains(collapsed, "✗") {
		t.Error("group header should show a failed glyph even though the failing job is hidden")
	}
	if !strings.Contains(collapsed, "t all jobs") {
		t.Error("expected the jobs-toggle hint when a group is truncated")
	}

	// `t` expands: all jobs visible, hint flips, marker gone.
	updated, _ := m.handleRefKey(keyStr("t"))
	got := updated.(Model)
	if !got.refJobsExpanded {
		t.Fatal("t should expand the jobs view")
	}
	expanded := got.renderRefPane(40, 70)
	if !strings.Contains(expanded, "e2e") || strings.Contains(expanded, "… 2 more") {
		t.Errorf("expanded pane should show every job and no marker:\n%s", expanded)
	}
	if !strings.Contains(expanded, "t fewer jobs") {
		t.Error("expected the collapse hint once expanded")
	}
}

func TestForgeMergeBlockedWhenNotMergeable(t *testing.T) {
	mr := sampleMR()
	mr.Mergeable, mr.MergeStatus = false, "ci still running"
	m := openLoadedChange(t, configuredForgeModel(t), forgeGitLab, mrLink, mr)

	updated, cmd := m.handleRefKey(keyStr("M"))
	got := updated.(Model)
	if got.refConfirm.active {
		t.Error("merge confirm should not open when the change request isn't mergeable")
	}
	if cmd != nil {
		t.Error("expected no Cmd for a blocked merge")
	}
	if !strings.Contains(got.status, "cannot merge") || !strings.Contains(got.status, "ci still running") {
		t.Errorf("status = %q, want a 'cannot merge' reason", got.status)
	}
}

// GitLab merges one way, so its confirm is a plain yes/no.
func TestForgeMergeConfirmIsYesNoWithOneMethod(t *testing.T) {
	m := openLoadedChange(t, configuredForgeModel(t), forgeGitLab, mrLink, sampleMR())
	updated, _ := m.handleRefKey(keyStr("M"))
	got := updated.(Model)
	if !got.refConfirm.active || got.refConfirm.action != "merge" {
		t.Fatalf("expected merge confirm, got %+v", got.refConfirm)
	}
	if got.refConfirm.choosing() {
		t.Error("a single-method forge should not offer a choice")
	}
	if !strings.Contains(got.renderRefConfirm(), "y confirm") {
		t.Error("expected the yes/no hint")
	}
	// Cancelling leaves the panel untouched.
	updated, _ = got.handleRefConfirmKey(keyStr("n"))
	if updated.(Model).refConfirm.active {
		t.Error("n should dismiss the confirm")
	}
	// Confirming fires the mutation.
	if _, cmd := got.handleRefConfirmKey(keyStr("y")); cmd == nil {
		t.Error("y should fire a merge mutation Cmd")
	}
}

// GitHub offers three strategies, so the confirm asks which one — and a stray
// y/enter must not pick one on the user's behalf.
func TestForgeMergeConfirmChoosesMethod(t *testing.T) {
	m := openLoadedChange(t, configuredForgeModel(t), forgeGitHub, prLink, samplePR())
	updated, _ := m.handleRefKey(keyStr("M"))
	got := updated.(Model)
	if !got.refConfirm.choosing() {
		t.Fatalf("expected a merge-method choice, got %+v", got.refConfirm)
	}
	modal := got.renderRefConfirm()
	for _, want := range []string{"m merge commit", "s squash", "r rebase", "n cancel"} {
		if !strings.Contains(modal, want) {
			t.Errorf("confirm modal missing %q:\n%s", want, modal)
		}
	}
	if _, cmd := got.handleRefConfirmKey(keyStr("y")); cmd != nil {
		t.Error("y must not merge when a strategy still has to be picked")
	}
	if _, cmd := got.handleRefConfirmKey(keyStr("enter")); cmd != nil {
		t.Error("enter must not merge when a strategy still has to be picked")
	}
	after, cmd := got.handleRefConfirmKey(keyStr("s"))
	if cmd == nil {
		t.Fatal("s should fire the squash merge")
	}
	if after.(Model).refConfirm.active {
		t.Error("picking a strategy should close the modal")
	}
}

func TestForgeApproveOpensConfirm(t *testing.T) {
	m := openLoadedChange(t, configuredForgeModel(t), forgeGitHub, prLink, samplePR())
	updated, _ := m.handleRefKey(keyStr("A"))
	got := updated.(Model)
	if !got.refConfirm.active || got.refConfirm.action != "approve" {
		t.Fatalf("expected approve confirm, got %+v", got.refConfirm)
	}
	if !strings.Contains(got.refConfirm.title, "o/r#7") {
		t.Errorf("confirm title = %q, want the forge's own short form", got.refConfirm.title)
	}
	// Confirming fires a mutation Cmd.
	_, cmd := got.handleRefConfirmKey(keyStr("y"))
	if cmd == nil {
		t.Error("y should fire an approve mutation Cmd")
	}
}

// A closed change request can't be approved; the panel says so rather than
// firing a call the forge would refuse.
func TestForgeApproveRefusedOnClosedChange(t *testing.T) {
	pr := samplePR()
	pr.State = forge.StateMerged
	m := openLoadedChange(t, configuredForgeModel(t), forgeGitHub, prLink, pr)
	updated, _ := m.handleRefKey(keyStr("A"))
	got := updated.(Model)
	if got.refConfirm.active {
		t.Error("approve confirm should not open for a merged pull request")
	}
	if !strings.Contains(got.status, "cannot approve") || !strings.Contains(got.status, "pull request") {
		t.Errorf("status = %q, want a refusal naming the forge's noun", got.status)
	}
}

func TestRefPanelCyclesAcrossProviders(t *testing.T) {
	// A message naming a merge request, a pull request and a Jira issue opens
	// all three, ordered by where they appear.
	m := configuredForgeModel(t)
	m.jiraClient = jira.New(jira.Config{BaseURL: "https://example.atlassian.net", Email: "me@x.test", APIToken: "tok"})
	m.jiraProjects = []string{"JIRA"}
	post := &model.Post{Message: "https://git.example.com/g/p/-/merge_requests/5 and o/r#7 fix JIRA-1"}
	updated, _ := m.openRefForPost(post)
	got := updated.(Model)
	if len(got.refs) != 3 {
		t.Fatalf("expected 3 refs (GitLab + GitHub + Jira), got %+v", got.refs)
	}
	if got.refs[0].kind != refForge || got.refs[0].forge != forgeGitLab {
		t.Errorf("first ref should be the merge request: %+v", got.refs[0])
	}
	if got.refs[1].kind != refForge || got.refs[1].forge != forgeGitHub || got.refs[1].number != 7 {
		t.Errorf("second ref should be the pull request: %+v", got.refs[1])
	}
	if got.refs[2].kind != refJira {
		t.Errorf("third ref should be the Jira issue: %+v", got.refs[2])
	}
	// Cycling forward moves to the pull request and switches the pane title.
	cycled, _ := got.cycleRef(1)
	cg := cycled.(Model)
	if cg.refIdx != 1 || cg.refPaneTitle() == "" || !strings.Contains(cg.refPaneTitle(), "GitHub") {
		t.Errorf("after cycle, idx=%d title=%q", cg.refIdx, cg.refPaneTitle())
	}
}

// A message linking a pull request gets an inline badge once the status lands,
// keyed to the forge that owns the link.
func TestInlineBadgeUsesTheOwningForge(t *testing.T) {
	m := configuredForgeModel(t)
	ref, p, ok := m.matchChangeURL("https://github.com/o/r/pull/7")
	if !ok {
		t.Fatal("a github.com pull-request URL should match the GitHub provider")
	}
	if ref.provider != forgeGitHub || ref.repo != "o/r" || ref.number != 7 {
		t.Fatalf("matched %+v, want the GitHub provider on o/r#7", ref)
	}
	// Before the fetch lands: a plain, linked reference with no pill.
	m.changeStatus.sighted(ref, "post-1")
	plain := m.changeStatus.renderChangeBadge(ref, p)
	if !strings.Contains(plain, "#7") || strings.Contains(plain, reactionCapLeft) {
		t.Errorf("pending badge should be a plain #7 link, got %q", plain)
	}
	// After it lands: a pill carrying the state and the check glyph.
	m.changeStatus.markReady(ref, samplePR())
	pill := m.changeStatus.renderChangeBadge(ref, p)
	if !strings.Contains(pill, "open") || !strings.Contains(pill, "✓") {
		t.Errorf("ready badge should show state and check status, got %q", pill)
	}
	// A GitLab link on the same feed resolves to the other provider.
	glRef, _, ok := m.matchChangeURL("https://git.example.com/g/p/-/merge_requests/5")
	if !ok || glRef.provider != forgeGitLab {
		t.Errorf("merge-request URL matched %+v, want the GitLab provider", glRef)
	}
}
