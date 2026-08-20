package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/github"
)

func configuredGitHubModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	m.width, m.height = 120, 40
	m.ghClient = github.New(github.Config{
		BaseURL: "https://github.com",
		Token:   "ghp_test",
	})
	return m
}

func TestOpenReferenceGitHubIssue(t *testing.T) {
	m := configuredGitHubModel(t)
	post := &model.Post{Message: "fix https://github.com/org/repo/issues/99 please"}

	updated, cmd := m.openRefForPost(post)
	got := updated.(Model)

	if !got.refOpen {
		t.Fatal("expected reference panel to open")
	}
	if got.focus != focusRef {
		t.Errorf("focus = %v, want focusRef", got.focus)
	}
	if len(got.refs) != 1 || got.refs[0].kind != refGitHub {
		t.Errorf("refs = %+v, want one GitHub ref", got.refs)
	}
	if got.refs[0].ghRepo != "org/repo" || got.refs[0].ghNumber != 99 {
		t.Errorf("refs[0] = %+v", got.refs[0])
	}
	if cmd == nil {
		t.Error("expected a fetch Cmd")
	}
}

func TestOpenReferenceGitHubMixedWithGitLab(t *testing.T) {
	m := configuredGitHubModel(t)
	m.glClient = nil // only GitHub enabled in this test

	post := &model.Post{Message: "https://github.com/a/b/pull/1"}
	updated, _ := m.openRefForPost(post)
	got := updated.(Model)
	if !got.refOpen || got.refs[0].kind != refGitHub || !got.refs[0].ghIsPull {
		t.Errorf("refs = %+v", got.refs)
	}
}

func TestOpenReferenceGitHubNotConfigured(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 120, 40
	post := &model.Post{Message: "https://github.com/a/b/issues/1"}

	updated, cmd := m.openRefForPost(post)
	got := updated.(Model)
	if got.refOpen {
		t.Error("panel should stay closed without GitHub token")
	}
	if cmd != nil {
		t.Error("expected no Cmd")
	}
	if !strings.Contains(got.status, "no reference provider configured") {
		t.Errorf("status = %q", got.status)
	}
}

func TestGitHubPanelRendersItem(t *testing.T) {
	m := configuredGitHubModel(t)
	m.posts = []*model.Post{{Message: "https://github.com/org/repo/pull/19"}}
	m.postIdx = 0
	updated, _ := m.openRefForPost(m.posts[0])
	got := updated.(Model)

	final, _ := got.handleGitHubLoaded(githubLoadedMsg{
		gen:    got.refGen,
		repo:   "org/repo",
		number: 19,
		item: &github.Item{
			Repo:           "org/repo",
			Number:         19,
			IsPull:         true,
			Title:          "Add matterbox-server design",
			State:          "open",
			Author:         "cornedor",
			SourceBranch:   "feature-branch",
			TargetBranch:   "main",
			ChangedFiles:   23,
			MergeableState: "dirty",
			ChecksState:    "",
			Body:           "## Summary\nPlan details.",
			URL:            "https://github.com/org/repo/pull/19",
		},
	})
	out := final.(Model)
	content := out.refView.View()
	for _, want := range []string{
		"Add matterbox-server design",
		"feature-branch -> main",
		"23 files",
		"approve",
		"merge",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("rendered content missing %q:\n%s", want, content)
		}
	}
}

func TestGitHubMergeBlockedWhenNotMergeable(t *testing.T) {
	m := configuredGitHubModel(t)
	m.refs = []reference{{kind: refGitHub, ghRepo: "a/b", ghNumber: 1, ghIsPull: true}}
	m.refOpen = true
	m.ghItem = &github.Item{IsPull: true, State: "open", MergeableState: "dirty"}

	updated, cmd := m.handleRefKey(keyStr("M"))
	got := updated.(Model)
	if got.glConfirm.active {
		t.Fatal("expected no confirm for conflicted PR")
	}
	if cmd != nil {
		t.Fatal("expected no cmd")
	}
	if !strings.Contains(got.status, "conflicts") {
		t.Errorf("status = %q", got.status)
	}
}

func TestGitHubMergeConfirmWhenMergeable(t *testing.T) {
	m := configuredGitHubModel(t)
	m.refs = []reference{{kind: refGitHub, ghRepo: "a/b", ghNumber: 1, ghIsPull: true}}
	m.refOpen = true
	m.ghItem = &github.Item{IsPull: true, State: "open", MergeableState: "clean", TargetBranch: "main"}

	updated, _ := m.handleRefKey(keyStr("M"))
	got := updated.(Model)
	if !got.glConfirm.active || got.glConfirm.action != "merge" || got.glConfirm.kind != refGitHub {
		t.Fatalf("expected merge confirm, got %+v", got.glConfirm)
	}
}

func TestGitHubApproveOpensConfirm(t *testing.T) {
	m := configuredGitHubModel(t)
	m.refs = []reference{{kind: refGitHub, ghRepo: "a/b", ghNumber: 1, ghIsPull: true}}
	m.refOpen = true
	m.ghItem = &github.Item{IsPull: true, State: "open"}

	updated, _ := m.handleRefKey(keyStr("A"))
	got := updated.(Model)
	if !got.glConfirm.active || got.glConfirm.action != "approve" || got.glConfirm.kind != refGitHub {
		t.Fatalf("expected approve confirm, got %+v", got.glConfirm)
	}
}
