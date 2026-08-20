package ui

import (
	"strings"
	"testing"

	"matterbox/internal/github"
)

func TestParseGitHubURL(t *testing.T) {
	repo, n, isPull, ok := parseGitHubURL("https://github.com/org/repo/pull/19", "https://github.com")
	if !ok || repo != "org/repo" || n != 19 || !isPull {
		t.Errorf("pull = %s %d pull=%v ok=%v", repo, n, isPull, ok)
	}
	repo, n, isPull, ok = parseGitHubURL("https://github.com/org/repo/issues/7", "https://github.com")
	if !ok || repo != "org/repo" || n != 7 || isPull {
		t.Errorf("issue = %s %d pull=%v ok=%v", repo, n, isPull, ok)
	}
	_, _, _, ok = parseGitHubURL("https://gitlab.com/a/b/-/merge_requests/1", "https://github.com")
	if ok {
		t.Error("GitLab URL should not parse as GitHub")
	}
}

func TestRenderGHBadgePR(t *testing.T) {
	s := newGHStatusManager()
	client := github.New(github.Config{BaseURL: "https://github.com", Token: "tok"})
	s.sighted("cornedor/matterbox", 19, true, "p1")
	s.markReady("cornedor/matterbox", 19, &github.Item{
		Repo:   "cornedor/matterbox",
		Number: 19,
		IsPull: true,
		State:  "open",
	})
	badge := s.renderGHBadge("cornedor/matterbox", 19, client)
	if !strings.Contains(badge, "PR#19") {
		t.Errorf("badge missing PR#19: %q", badge)
	}
	if !strings.Contains(badge, "open") {
		t.Errorf("badge missing open status: %q", badge)
	}
	// Distinct from GitLab's !N form.
	if strings.Contains(badge, "!19") {
		t.Errorf("badge should not use GitLab !N form: %q", badge)
	}
}

func TestBuildMRInlineFnGitHub(t *testing.T) {
	m := configuredGitHubModel(t)
	m.ghStatus = newGHStatusManager()
	fn := m.buildMRInlineFn("post1")
	if fn == nil {
		t.Fatal("expected inline fn when GitHub is configured")
	}
	badge, ok := fn("https://github.com/cornedor/matterbox/pull/19")
	if !ok {
		t.Fatal("expected GitHub URL to be rewritten")
	}
	if !strings.Contains(badge, "PR#19") {
		t.Errorf("pending badge = %q, want PR#19", badge)
	}
}
