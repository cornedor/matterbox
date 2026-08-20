package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const issueJSON = `{
  "title": "Fix bug",
  "state": "open",
  "html_url": "https://github.com/org/repo/issues/42",
  "updated_at": "2026-01-15T12:00:00Z",
  "body": "Details here.",
  "user": {"login": "alice"},
  "labels": [{"name": "bug"}]
}`

const issueAsPRJSON = `{
  "title": "Add feature",
  "state": "open",
  "html_url": "https://github.com/org/repo/pull/7",
  "updated_at": "2026-01-15T12:00:00Z",
  "body": "PR body.",
  "user": {"login": "bob"},
  "labels": [],
  "pull_request": {}
}`

const pullJSON = `{
  "draft": false,
  "mergeable_state": "clean",
  "changed_files": 5,
  "head": {"ref": "feature", "sha": "abc123"},
  "base": {"ref": "main"}
}`

const statusJSON = `{"state": "success"}`

const checkRunsJSON = `{"check_runs": [
  {"name": "build", "status": "completed", "conclusion": "success"},
  {"name": "test", "status": "completed", "conclusion": "failure"}
]}`

const reviewsJSON = `[
  {"state": "CHANGES_REQUESTED", "user": {"login": "grace"}},
  {"state": "APPROVED", "user": {"login": "grace"}},
  {"state": "APPROVED", "user": {"login": "linus"}}
]`

func newTestClient(srv *httptest.Server) *Client {
	return New(Config{BaseURL: srv.URL, Token: "ghp_test"})
}

func TestGetIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("missing Bearer token")
		}
		if !strings.HasSuffix(r.URL.Path, "/repos/org/repo/issues/42") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(issueJSON))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	item, err := c.Get(context.Background(), "org/repo", 42)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Fix bug" || item.Author != "alice" || item.IsPull {
		t.Errorf("item = %+v", item)
	}
	if len(item.Labels) != 1 || item.Labels[0] != "bug" {
		t.Errorf("labels = %v", item.Labels)
	}
}

func TestGetPullRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/7"):
			w.Write([]byte(issueAsPRJSON))
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			w.Write([]byte(pullJSON))
		case strings.Contains(r.URL.Path, "/check-runs"):
			w.Write([]byte(checkRunsJSON))
		case strings.HasSuffix(r.URL.Path, "/commits/abc123/status"):
			w.Write([]byte(statusJSON))
		case strings.HasSuffix(r.URL.Path, "/pulls/7/reviews"):
			w.Write([]byte(reviewsJSON))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	item, err := c.Get(context.Background(), "org/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if !item.IsPull || item.MergeableState != "clean" {
		t.Errorf("item = %+v, want PR with mergeable_state clean", item)
	}
	if item.SourceBranch != "feature" || item.TargetBranch != "main" {
		t.Errorf("branches = %q -> %q", item.SourceBranch, item.TargetBranch)
	}
	if item.ChangedFiles != 5 {
		t.Errorf("changed_files = %d", item.ChangedFiles)
	}
	// Check runs win over a green commit status when any run failed.
	if item.ChecksState != "failure" {
		t.Errorf("checks = %q, want failure from check runs", item.ChecksState)
	}
	if item.Approvals == nil || len(item.Approvals.By) != 2 || len(item.Approvals.ChangesRequested) != 0 {
		t.Errorf("approvals = %+v, want grace+linus approved", item.Approvals)
	}
	if !item.Mergeable() {
		t.Error("expected mergeable PR")
	}
}

func TestChecksPreferActionsCheckRuns(t *testing.T) {
	// Actions-only repos post check runs and leave the legacy status empty —
	// the panel used to show "none" forever.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/7"):
			w.Write([]byte(issueAsPRJSON))
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			w.Write([]byte(pullJSON))
		case strings.Contains(r.URL.Path, "/check-runs"):
			w.Write([]byte(`{"check_runs":[{"name":"CI","status":"completed","conclusion":"success"}]}`))
		case strings.HasSuffix(r.URL.Path, "/status"):
			w.Write([]byte(`{"state":"pending","statuses":[]}`))
		case strings.Contains(r.URL.Path, "/reviews"):
			w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	item, err := newTestClient(srv).Get(context.Background(), "org/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if item.ChecksState != "success" {
		t.Fatalf("checks = %q, want success from Actions check runs", item.ChecksState)
	}
}

func TestApproveAndMergeHitRightEndpoints(t *testing.T) {
	var approve, merge bool
	var mergeBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls/3/reviews"):
			approve = true
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id": 1}`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/pulls/3/merge"):
			merge = true
			mergeBody = string(buf)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"merged": true}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.Approve(context.Background(), "org/repo", 3); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := c.Merge(context.Background(), "org/repo", 3, "squash"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !approve || !merge {
		t.Errorf("approve=%v merge=%v", approve, merge)
	}
	if !strings.Contains(mergeBody, `"merge_method":"squash"`) {
		t.Errorf("merge body = %q, want squash method", mergeBody)
	}
}

func TestNewNormalizesSchemeLessBaseURL(t *testing.T) {
	c := New(Config{BaseURL: "github.com", Token: "t"})
	if c.webBase != "https://github.com" {
		t.Fatalf("webBase = %q", c.webBase)
	}
	if c.apiBase != "https://api.github.com" {
		t.Fatalf("apiBase = %q", c.apiBase)
	}
}

func TestGetPullRequestFailsClosedOnPRFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/7"):
			w.Write([]byte(issueAsPRJSON))
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if _, err := c.Get(context.Background(), "org/repo", 7); err == nil {
		t.Fatal("expected error when /pulls fails")
	}
	// Second call must hit the network again (not a cached stub).
	calls := 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/7"):
			w.Write([]byte(issueAsPRJSON))
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			w.Write([]byte(pullJSON))
		case strings.HasSuffix(r.URL.Path, "/commits/abc123/status"):
			w.Write([]byte(statusJSON))
		case strings.Contains(r.URL.Path, "/check-runs"):
			w.Write([]byte(`{"check_runs":[]}`))
		case strings.Contains(r.URL.Path, "/reviews"):
			w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv2.Close()
	c2 := newTestClient(srv2)
	if _, err := c2.Get(context.Background(), "org/repo", 7); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("expected issue+pull fetches, calls=%d", calls)
	}
}

func TestDoJSONIncludesAPIMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Pull Request is not mergeable"}`))
	}))
	defer srv.Close()
	c := newTestClient(srv)
	err := c.Merge(context.Background(), "org/repo", 1, "merge")
	if err == nil || !strings.Contains(err.Error(), "not mergeable") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnabledRequiresToken(t *testing.T) {
	c := New(Config{BaseURL: "https://github.com", Token: ""})
	if c.Enabled() {
		t.Error("client without token should not be enabled")
	}
}
