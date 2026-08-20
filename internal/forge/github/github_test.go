package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"matterbox/internal/forge"
)

// newTestClient points a Client at srv: the browse root is the test server, and
// so is the API root, since srv's host is not github.com (APIRoot would send us
// to api.github.com) — the /api/v3 suffix an Enterprise host uses is what the
// handlers below see.
func newTestClient(srv *httptest.Server) *Client {
	return New(Config{BaseURL: srv.URL, Token: "tok"})
}

const prJSON = `{
  "number": 42, "title": "Fix the widget", "state": "open", "draft": false,
  "merged": false, "locked": false, "body": "Some **bold** detail.",
  "html_url": "https://github.com/o/r/pull/42",
  "updated_at": "2026-06-15T06:52:20Z", "changed_files": 3,
  "mergeable": true, "mergeable_state": "clean",
  "user": {"login": "ada"},
  "assignees": [{"login": "grace"}],
  "requested_reviewers": [{"login": "linus"}],
  "labels": [{"name": "bug"}],
  "head": {"ref": "fix/widget", "sha": "deadbeef"},
  "base": {"ref": "main"}
}`

const checkRunsJSON = `{"check_runs": [
  {"name": "build", "status": "completed", "conclusion": "success",
   "started_at": "2026-06-15T06:00:00Z", "completed_at": "2026-06-15T06:02:00Z",
   "app": {"name": "GitHub Actions", "slug": "github-actions"}},
  {"name": "unit", "status": "completed", "conclusion": "failure",
   "started_at": "2026-06-15T06:00:00Z", "completed_at": "2026-06-15T06:04:11Z",
   "app": {"name": "GitHub Actions", "slug": "github-actions"}},
  {"name": "coverage", "status": "in_progress",
   "app": {"name": "Codecov", "slug": "codecov"}}
]}`

const statusJSON = `{"state": "success", "statuses": [
  {"context": "ci/jenkins", "state": "success"}
]}`

const reviewsJSON = `[
  {"state": "COMMENTED", "user": {"login": "mel"}},
  {"state": "CHANGES_REQUESTED", "user": {"login": "grace"}},
  {"state": "APPROVED", "user": {"login": "grace"}},
  {"state": "APPROVED", "user": {"login": "mel"}}
]`

// prHandler serves the four endpoints a full fetch touches.
func prHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/42/reviews"):
			w.Write([]byte(reviewsJSON))
		case strings.Contains(r.URL.Path, "/check-runs"):
			w.Write([]byte(checkRunsJSON))
		case strings.HasSuffix(r.URL.Path, "/commits/deadbeef/status"):
			w.Write([]byte(statusJSON))
		case strings.HasSuffix(r.URL.Path, "/pulls/42"):
			if got := r.Header.Get("Authorization"); got != "Bearer tok" {
				t.Errorf("Authorization = %q, want Bearer tok", got)
			}
			if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Errorf("Accept = %q", got)
			}
			w.Write([]byte(prJSON))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}
}

func TestGetPullRequest(t *testing.T) {
	srv := httptest.NewServer(prHandler(t))
	defer srv.Close()

	pr, err := newTestClient(srv).Get(context.Background(), "o/r", 42)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pr.Title != "Fix the widget" || pr.Author != "ada" {
		t.Errorf("PR basics wrong: %+v", pr)
	}
	if pr.State != forge.StateOpen || pr.SourceBranch != "fix/widget" || pr.TargetBranch != "main" {
		t.Errorf("state/branches wrong: %+v", pr)
	}
	if !pr.Mergeable || pr.MergeStatus != "mergeable" {
		t.Errorf("mergeable = %v (%q), want true (mergeable)", pr.Mergeable, pr.MergeStatus)
	}
	if pr.ChangesCount != "3" {
		t.Errorf("changes = %q, want 3", pr.ChangesCount)
	}
	// Check runs group by app, and the commit statuses arrive as one more group.
	if pr.Checks == nil || len(pr.Checks.Groups) != 3 {
		t.Fatalf("checks = %+v, want 3 groups", pr.Checks)
	}
	if pr.Checks.Groups[0].Name != "GitHub Actions" || len(pr.Checks.Groups[0].Jobs) != 2 {
		t.Errorf("first group = %+v", pr.Checks.Groups[0])
	}
	if pr.Checks.Groups[2].Name != "commit statuses" {
		t.Errorf("last group = %+v, want the commit statuses", pr.Checks.Groups[2])
	}
	// A failing job outranks a running and a passing one.
	if pr.Checks.Status != forge.StatusFailed || pr.Checks.Label != "failed" {
		t.Errorf("overall checks = %q/%q, want failed", pr.Checks.Status, pr.Checks.Label)
	}
	// Duration spans the earliest start to the latest completion: 6:00 → 6:04:11.
	if pr.Checks.Duration != 251 {
		t.Errorf("duration = %d, want 251", pr.Checks.Duration)
	}
}

func TestGetReviewsUseLatestVerdictPerReviewer(t *testing.T) {
	srv := httptest.NewServer(prHandler(t))
	defer srv.Close()

	pr, err := newTestClient(srv).Get(context.Background(), "o/r", 42)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	ap := pr.Approvals
	if ap == nil {
		t.Fatal("approvals not read")
	}
	// grace requested changes then approved; mel commented then approved. Both
	// count as approvals, and nothing is left blocking.
	if len(ap.By) != 2 || len(ap.ChangesRequested) != 0 {
		t.Fatalf("approvals = %+v, want two approvals and no blocks", ap)
	}
	if !ap.Approved {
		t.Errorf("Approved = false, want true (approvals and no changes requested)")
	}
	// linus was asked to review and hasn't: one verdict still outstanding.
	if ap.Left != 1 {
		t.Errorf("Left = %d, want 1 (linus hasn't reviewed)", ap.Left)
	}
}

func TestGetCaches(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/42/reviews"):
			w.Write([]byte(reviewsJSON))
		case strings.Contains(r.URL.Path, "/check-runs"), strings.HasSuffix(r.URL.Path, "/status"):
			w.Write([]byte(`{}`))
		default:
			hits++
			w.Write([]byte(prJSON))
		}
	}))
	defer srv.Close()
	c := newTestClient(srv)
	ctx := context.Background()
	c.Get(ctx, "o/r", 42)
	c.Get(ctx, "o/r", 42)
	if hits != 1 {
		t.Errorf("PR fetched %d times, want 1 (cached)", hits)
	}
	c.Invalidate("o/r", 42)
	c.Get(ctx, "o/r", 42)
	if hits != 2 {
		t.Errorf("after invalidate, hits = %d, want 2", hits)
	}
}

func TestChecksAndReviewFailuresAreNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/42"):
			w.Write([]byte(prJSON))
		default: // check runs, statuses, reviews all fail
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	pr, err := newTestClient(srv).Get(context.Background(), "o/r", 42)
	if err != nil {
		t.Fatalf("Get should succeed even when checks/reviews fail: %v", err)
	}
	if pr.Checks != nil {
		t.Errorf("checks should be nil when unavailable: %+v", pr.Checks)
	}
	if pr.Approvals != nil {
		t.Errorf("approvals should be nil when unavailable: %+v", pr.Approvals)
	}
}

func TestApproveAndMergeHitRightEndpoints(t *testing.T) {
	var approvePath, mergePath, mergeBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		switch {
		case strings.HasSuffix(r.URL.Path, "/reviews") && r.Method == http.MethodPost:
			approvePath = r.URL.Path
			if !strings.Contains(string(buf), `"APPROVE"`) {
				t.Errorf("approve body = %s", buf)
			}
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/merge") && r.Method == http.MethodPut:
			mergePath, mergeBody = r.URL.Path, string(buf)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv)
	ctx := context.Background()
	if err := c.Approve(ctx, "o/r", 42); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := c.Merge(ctx, "o/r", 42, "squash"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !strings.HasSuffix(approvePath, "/repos/o/r/pulls/42/reviews") {
		t.Errorf("approve path = %q", approvePath)
	}
	if !strings.HasSuffix(mergePath, "/repos/o/r/pulls/42/merge") {
		t.Errorf("merge path = %q", mergePath)
	}
	if !strings.Contains(mergeBody, `"merge_method":"squash"`) {
		t.Errorf("merge body = %q, want the chosen method", mergeBody)
	}
}

func TestMergedPRReportsMerged(t *testing.T) {
	body := strings.ReplaceAll(prJSON, `"state": "open", "draft": false,
  "merged": false`, `"state": "closed", "draft": false,
  "merged": true`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls/42") {
			w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	pr, err := newTestClient(srv).Get(context.Background(), "o/r", 42)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pr.State != forge.StateMerged {
		t.Errorf("state = %q, want %q", pr.State, forge.StateMerged)
	}
}

func TestMergeabilityPhrases(t *testing.T) {
	cases := []struct {
		state    string
		merge    bool
		contains string
	}{
		{"clean", true, "mergeable"},
		{"unstable", true, "check is failing"},
		{"dirty", false, "conflicts"},
		{"behind", false, "behind"},
		{"blocked", false, "blocked"},
		{"unknown", false, "not computed"},
	}
	for _, c := range cases {
		ok, txt := mergeability(apiPR{State: "open", MergeableState: c.state})
		if ok != c.merge || !strings.Contains(txt, c.contains) {
			t.Errorf("mergeable_state %q → (%v, %q), want (%v, containing %q)",
				c.state, ok, txt, c.merge, c.contains)
		}
	}
}

func TestAPIRootAndWebURL(t *testing.T) {
	// github.com talks to api.github.com; an Enterprise host serves /api/v3.
	if got := APIRoot("https://github.com"); got != "https://api.github.com" {
		t.Errorf("APIRoot(github.com) = %q", got)
	}
	if got := APIRoot("git.example.com"); got != "https://git.example.com/api/v3" {
		t.Errorf("APIRoot(enterprise) = %q", got)
	}
	c := New(Config{Token: "tok"})
	if c.BaseURL() != DefaultBaseURL {
		t.Errorf("BaseURL() = %q, want the github.com default", c.BaseURL())
	}
	if got := c.WebURL("o/r", 9); got != "https://github.com/o/r/pull/9" {
		t.Errorf("WebURL = %q", got)
	}
}

func TestStatusErrorSurfacesAPIMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte(`{"message": "Rebase merges are not allowed on this repository."}`))
	}))
	defer srv.Close()
	err := newTestClient(srv).Merge(context.Background(), "o/r", 42, "rebase")
	if err == nil || !strings.Contains(err.Error(), "Rebase merges are not allowed") {
		t.Errorf("error = %v, want it to surface the API message", err)
	}
}

// owner/repo#N names an issue and a pull request alike, so a 404 from the pulls
// endpoint gets a second look before it reaches the panel.
func TestIssueNumberSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/42"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message": "Not Found"}`))
		case strings.HasSuffix(r.URL.Path, "/issues/42"):
			w.Write([]byte(`{"number": 42, "title": "A plain issue"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	_, err := newTestClient(srv).Get(context.Background(), "o/r", 42)
	if err == nil || !strings.Contains(err.Error(), "is an issue, not a pull request") {
		t.Errorf("error = %v, want it to name the issue/PR mix-up", err)
	}
}

// A number that is neither keeps the plain not-found error.
func TestMissingNumberStaysNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message": "Not Found"}`))
	}))
	defer srv.Close()
	_, err := newTestClient(srv).Get(context.Background(), "o/r", 42)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want the not-found message", err)
	}
}

// Without a token GitHub still reads public repositories, so the panel is
// offered — but nothing may fetch on its own, and the write actions refuse
// before spending a call.
func TestTokenlessIsReadOnlyAndOnRequest(t *testing.T) {
	c := New(Config{})
	if !c.Enabled() {
		t.Error("a tokenless client should still be readable")
	}
	if c.AutoFetch() {
		t.Error("a tokenless client must not be fetched automatically")
	}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"approve", c.Approve(context.Background(), "o/r", 1)},
		{"merge", c.Merge(context.Background(), "o/r", 1, "merge")},
	} {
		if tc.err == nil || !strings.Contains(tc.err.Error(), "needs a token") {
			t.Errorf("%s without a token: err = %v, want it to ask for a token", tc.name, tc.err)
		}
	}
	// With one, both are on.
	if withTok := New(Config{Token: "tok"}); !withTok.AutoFetch() {
		t.Error("a token should enable automatic fetches")
	}
}

// The rate limiter answers 403, which would otherwise read as a permissions
// problem — the one error a tokenless setup is most likely to meet.
func TestRateLimitErrorIsNotAboutScopes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message": "API rate limit exceeded for 203.0.113.7."}`))
	}))
	defer srv.Close()
	_, err := New(Config{BaseURL: srv.URL}).Get(context.Background(), "o/r", 42)
	if err == nil || !strings.Contains(err.Error(), "out of anonymous requests") {
		t.Errorf("error = %v, want it to name the anonymous rate limit", err)
	}
	if err != nil && strings.Contains(err.Error(), "scopes") {
		t.Errorf("error = %v, should not blame token scopes", err)
	}
}

// A 404 without a token is as likely to be a private repository as a typo.
func TestTokenless404MentionsPrivateRepositories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message": "Not Found"}`))
	}))
	defer srv.Close()
	_, err := New(Config{BaseURL: srv.URL}).Get(context.Background(), "o/r", 42)
	if err == nil || !strings.Contains(err.Error(), "private repository needs a token") {
		t.Errorf("error = %v, want the private-repository hint", err)
	}
}
