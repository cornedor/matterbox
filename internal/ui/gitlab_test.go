package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/gitlab"
	"matterbox/internal/jira"
)

// configuredGitLabModel builds a Model whose GitLab client is enabled (dummy
// base URL + token) so the open path runs. No network call happens unless the
// returned fetch Cmd is invoked.
func configuredGitLabModel(t *testing.T) Model {
	t.Helper()
	m := New(nil, nil)
	m.width, m.height = 120, 40
	m.glClient = gitlab.New(gitlab.Config{BaseURL: "https://git.example.com", Token: "tok"})
	return m
}

func openLoadedMR(t *testing.T, m Model, mr *gitlab.MR) Model {
	t.Helper()
	post := &model.Post{Message: "see https://git.example.com/g/p/-/merge_requests/5 please"}
	m.posts = []*model.Post{post}
	m.postIdx = 0
	m.focus = focusMessages
	updated, _ := m.openRefForPost(post)
	got := updated.(Model)
	if !got.refOpen || got.refs[0].kind != refGitLab || got.refs[0].glIID != 5 {
		t.Fatalf("expected GitLab ref g/p!5 open, got %+v", got.refs)
	}
	final, _ := got.handleGitLabLoaded(gitlabLoadedMsg{gen: got.refGen, project: "g/p", iid: 5, mr: mr})
	return final.(Model)
}

func sampleMR() *gitlab.MR {
	return &gitlab.MR{
		Project: "g/p", IID: 5, Title: "Fix the widget", State: "opened",
		Author: "Ada Lovelace", SourceBranch: "fix/widget", TargetBranch: "main",
		DetailedMergeStatus: "mergeable", ChangesCount: "3",
		WebURL:      "https://git.example.com/g/p/-/merge_requests/5",
		Description: "Some **bold** detail.",
		Pipeline: &gitlab.Pipeline{
			Status: "success", Label: "passed", Duration: 251,
			Stages: []gitlab.Stage{
				{Name: "build", Jobs: []gitlab.Job{{Name: "compile", Status: "success"}}},
				{Name: "test", Jobs: []gitlab.Job{{Name: "lint", Status: "failed"}}},
			},
		},
	}
}

// TestRefPanelLinkIsClickable: a link in the reference panel (here a GitLab MR
// description) resolves under the pointer and opens on a click — the panel used
// to be inert to the mouse (hitTest returned hitNone), so a rendered link could
// not be clicked while mouse reporting was on. Hovering it also highlights it.
func TestRefPanelLinkIsClickable(t *testing.T) {
	url := "https://example.com/docs"
	// mouseModel parks us on a real team tab (so hitTest sees the channel/message
	// layout, not the synthetic Feed/Search panes) with mouse reporting on.
	base := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "x"}})
	base.glClient = gitlab.New(gitlab.Config{BaseURL: "https://git.example.com", Token: "tok"})
	mr := sampleMR()
	mr.Description = "Read [the docs](" + url + ") carefully."
	m := openLoadedMR(t, base, mr)

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
	if !strings.Contains(hov.refView.GetContent(), "48;5;238") {
		t.Fatal("hovered ref link not highlighted in the panel content")
	}
}

func TestGitLabPanelRendersMR(t *testing.T) {
	m := openLoadedMR(t, configuredGitLabModel(t), sampleMR())
	pane := m.renderRefPane(30, 60)
	for _, want := range []string{"!5", "GitLab", "Fix the widget", "main", "passed", "compile", "lint", "mergeable"} {
		if !strings.Contains(pane, want) {
			t.Errorf("rendered pane missing %q", want)
		}
	}
}

func TestGitLabPipelineCapsJobsPerStage(t *testing.T) {
	mr := sampleMR()
	// A stage with more than the cap, the failing one hidden below the cut.
	mr.Pipeline.Stages = []gitlab.Stage{{Name: "test", Jobs: []gitlab.Job{
		{Name: "unit-a", Status: "success"},
		{Name: "unit-b", Status: "success"},
		{Name: "unit-c", Status: "success"},
		{Name: "unit-d", Status: "success"},
		{Name: "e2e", Status: "failed"},
	}}}
	m := openLoadedMR(t, configuredGitLabModel(t), mr)

	collapsed := m.renderRefPane(40, 70)
	if !strings.Contains(collapsed, "… 2 more") {
		t.Errorf("collapsed pane should mark the 2 hidden jobs:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "e2e") {
		t.Error("the 5th job should be hidden when collapsed")
	}
	// Stage status must still surface the hidden failure: a red ✗ on the header.
	if !strings.Contains(collapsed, "✗") {
		t.Error("stage header should show a failed glyph even though the failing job is hidden")
	}
	if !strings.Contains(collapsed, "t all jobs") {
		t.Error("expected the jobs-toggle hint when a stage is truncated")
	}

	// `t` expands: all jobs visible, hint flips, marker gone.
	updated, _ := m.handleRefKey(keyStr("t"))
	got := updated.(Model)
	if !got.glJobsExpanded {
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

func TestStageStatusWorstWins(t *testing.T) {
	cases := []struct {
		name string
		jobs []gitlab.Job
		want string
	}{
		{"failure dominates", []gitlab.Job{{Status: "success"}, {Status: "failed"}, {Status: "running"}}, "failed"},
		{"running over success", []gitlab.Job{{Status: "success"}, {Status: "running"}}, "running"},
		{"success over skipped", []gitlab.Job{{Status: "success"}, {Status: "skipped"}}, "success"},
		{"empty is success", nil, "success"},
	}
	for _, c := range cases {
		if got := stageStatus(c.jobs); got != c.want {
			t.Errorf("%s: stageStatus = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestGitLabMergeBlockedWhenNotMergeable(t *testing.T) {
	mr := sampleMR()
	mr.DetailedMergeStatus = "ci_still_running"
	m := openLoadedMR(t, configuredGitLabModel(t), mr)

	updated, cmd := m.handleRefKey(keyStr("M"))
	got := updated.(Model)
	if got.glConfirm.active {
		t.Error("merge confirm should not open when the MR isn't mergeable")
	}
	if cmd != nil {
		t.Error("expected no Cmd for a blocked merge")
	}
	if !strings.Contains(got.status, "cannot merge") {
		t.Errorf("status = %q, want a 'cannot merge' reason", got.status)
	}
}

func TestGitLabMergeConfirmWhenMergeable(t *testing.T) {
	m := openLoadedMR(t, configuredGitLabModel(t), sampleMR())
	updated, _ := m.handleRefKey(keyStr("M"))
	got := updated.(Model)
	if !got.glConfirm.active || got.glConfirm.action != "merge" {
		t.Fatalf("expected merge confirm, got %+v", got.glConfirm)
	}
	// Cancelling leaves the panel untouched.
	updated, _ = got.handleGitLabConfirmKey(keyStr("n"))
	if updated.(Model).glConfirm.active {
		t.Error("n should dismiss the confirm")
	}
}

func TestGitLabApproveOpensConfirm(t *testing.T) {
	m := openLoadedMR(t, configuredGitLabModel(t), sampleMR())
	updated, _ := m.handleRefKey(keyStr("A"))
	got := updated.(Model)
	if !got.glConfirm.active || got.glConfirm.action != "approve" {
		t.Fatalf("expected approve confirm, got %+v", got.glConfirm)
	}
	// Confirming fires a mutation Cmd.
	_, cmd := got.handleGitLabConfirmKey(keyStr("y"))
	if cmd == nil {
		t.Error("y should fire an approve mutation Cmd")
	}
}

func TestRefPanelCyclesAcrossProviders(t *testing.T) {
	// A message with a GitLab URL then a Jira key opens both, ordered by where
	// they appear (GitLab first here).
	m := configuredGitLabModel(t)
	m.jiraClient = jira.New(jira.Config{BaseURL: "https://example.atlassian.net", Email: "me@x.test", APIToken: "tok"})
	m.jiraProjects = []string{"JIRA"}
	post := &model.Post{Message: "https://git.example.com/g/p/-/merge_requests/5 fixes JIRA-1"}
	updated, _ := m.openRefForPost(post)
	got := updated.(Model)
	if len(got.refs) != 2 {
		t.Fatalf("expected 2 refs (GitLab + Jira), got %+v", got.refs)
	}
	if got.refs[0].kind != refGitLab || got.refs[1].kind != refJira {
		t.Errorf("refs out of appearance order: %+v", got.refs)
	}
	// Cycling forward moves to the Jira ref and switches the pane title.
	cycled, _ := got.cycleRef(1)
	cg := cycled.(Model)
	if cg.refIdx != 1 || cg.currentRef().kind != refJira {
		t.Errorf("after cycle, idx=%d kind=%v", cg.refIdx, cg.currentRef().kind)
	}
}
