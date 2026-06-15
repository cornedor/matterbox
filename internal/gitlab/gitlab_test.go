package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient points a Client at srv with a dummy token (so Enabled is true).
func newTestClient(srv *httptest.Server) *Client {
	return New(Config{BaseURL: srv.URL, Token: "tok"})
}

const mrJSON = `{
  "iid": 42, "project_id": 220, "title": "Fix the widget", "state": "opened",
  "draft": false, "source_branch": "fix/widget", "target_branch": "main",
  "labels": ["bug"], "changes_count": "3", "detailed_merge_status": "mergeable",
  "has_conflicts": false, "description": "Some **bold** detail.",
  "web_url": "https://git.example.com/g/p/-/merge_requests/42",
  "updated_at": "2026-06-15T06:52:20.294Z",
  "author": {"name": "Ada Lovelace", "username": "ada"},
  "reviewers": [{"name": "Grace Hopper", "username": "grace"}],
  "head_pipeline": {"id": 451939, "status": "success", "duration": 251,
    "web_url": "https://git.example.com/g/p/-/pipelines/451939",
    "detailed_status": {"label": "passed"}}
}`

const jobsJSON = `[
  {"name": "build", "stage": "build", "status": "success"},
  {"name": "lint", "stage": "test", "status": "success"},
  {"name": "unit", "stage": "test", "status": "failed"}
]`

const approvalsJSON = `{"approved": true, "approvals_required": 1, "approvals_left": 0,
  "approved_by": [{"user": {"name": "Grace Hopper", "username": "grace"}}]}`

func TestGetMergeRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Errorf("missing PRIVATE-TOKEN header")
		}
		switch {
		case strings.Contains(r.URL.Path, "/merge_requests/42/approvals"):
			w.Write([]byte(approvalsJSON))
		case strings.Contains(r.URL.Path, "/pipelines/451939/jobs"):
			w.Write([]byte(jobsJSON))
		case strings.HasSuffix(r.URL.Path, "/merge_requests/42"):
			// Project path must be URL-encoded with %2F (raw RequestURI).
			if !strings.Contains(r.RequestURI, "g%2Fp") {
				t.Errorf("project path not encoded: %s", r.RequestURI)
			}
			w.Write([]byte(mrJSON))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	mr, err := newTestClient(srv).Get(context.Background(), "g/p", 42)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mr.Title != "Fix the widget" || mr.Author != "Ada Lovelace" {
		t.Errorf("MR basics wrong: %+v", mr)
	}
	if !mr.Mergeable() {
		t.Errorf("expected mergeable, dms=%q", mr.DetailedMergeStatus)
	}
	if mr.Pipeline == nil || mr.Pipeline.Label != "passed" {
		t.Fatalf("pipeline = %+v", mr.Pipeline)
	}
	if len(mr.Pipeline.Stages) != 2 {
		t.Fatalf("stages = %+v, want 2 (build, test)", mr.Pipeline.Stages)
	}
	if mr.Pipeline.Stages[1].Name != "test" || len(mr.Pipeline.Stages[1].Jobs) != 2 {
		t.Errorf("test stage = %+v", mr.Pipeline.Stages[1])
	}
	if mr.Approvals == nil || !mr.Approvals.Approved || len(mr.Approvals.By) != 1 {
		t.Errorf("approvals = %+v", mr.Approvals)
	}
}

func TestGetCaches(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/merge_requests/42") {
			hits++
		}
		switch {
		case strings.Contains(r.URL.Path, "approvals"):
			w.Write([]byte(approvalsJSON))
		case strings.Contains(r.URL.Path, "jobs"):
			w.Write([]byte(jobsJSON))
		default:
			w.Write([]byte(mrJSON))
		}
	}))
	defer srv.Close()
	c := newTestClient(srv)
	ctx := context.Background()
	c.Get(ctx, "g/p", 42)
	c.Get(ctx, "g/p", 42)
	if hits != 1 {
		t.Errorf("MR fetched %d times, want 1 (cached)", hits)
	}
	c.Invalidate("g/p", 42)
	c.Get(ctx, "g/p", 42)
	if hits != 2 {
		t.Errorf("after invalidate, hits = %d, want 2", hits)
	}
}

func TestPipelineJobsFailureIsNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "jobs"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.Contains(r.URL.Path, "approvals"):
			w.WriteHeader(http.StatusNotFound) // approvals disabled
		default:
			w.Write([]byte(mrJSON))
		}
	}))
	defer srv.Close()
	mr, err := newTestClient(srv).Get(context.Background(), "g/p", 42)
	if err != nil {
		t.Fatalf("Get should succeed even when jobs/approvals fail: %v", err)
	}
	if mr.Pipeline == nil || len(mr.Pipeline.Stages) != 0 {
		t.Errorf("pipeline stages should be empty on jobs failure: %+v", mr.Pipeline)
	}
	if mr.Approvals != nil {
		t.Errorf("approvals should be nil when unavailable: %+v", mr.Approvals)
	}
}

func TestApproveAndMergeHitRightEndpoints(t *testing.T) {
	var gotApprove, gotMerge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/approve") && r.Method == http.MethodPost:
			gotApprove = r.URL.Path
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/merge") && r.Method == http.MethodPut:
			gotMerge = r.URL.Path
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv)
	ctx := context.Background()
	if err := c.Approve(ctx, "g/p", 42); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := c.Merge(ctx, "g/p", 42, true); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !strings.HasSuffix(gotApprove, "/merge_requests/42/approve") {
		t.Errorf("approve path = %q", gotApprove)
	}
	if !strings.HasSuffix(gotMerge, "/merge_requests/42/merge") {
		t.Errorf("merge path = %q", gotMerge)
	}
}

func TestNewNormalizesBaseURL(t *testing.T) {
	// A scheme-less host must still parse to a host so link detection (which
	// goes through BaseURL) works, and WebURL must be well-formed.
	c := New(Config{BaseURL: "git.example.com", Token: "tok"})
	if c.BaseURL() != "https://git.example.com" {
		t.Errorf("BaseURL() = %q, want https://git.example.com", c.BaseURL())
	}
	refs := Refs("https://git.example.com/g/p/-/merge_requests/9", c.BaseURL())
	if len(refs) != 1 || refs[0].String() != "g/p!9" {
		t.Errorf("scheme-less base should still detect the link, got %v", refs)
	}
	if c.WebURL("g/p", 9) != "https://git.example.com/g/p/-/merge_requests/9" {
		t.Errorf("WebURL = %q", c.WebURL("g/p", 9))
	}
}

func TestStatusErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte(`{"message": "405 Method Not Allowed"}`))
	}))
	defer srv.Close()
	err := newTestClient(srv).Merge(context.Background(), "g/p", 42, false)
	if err == nil || !strings.Contains(err.Error(), "405 Method Not Allowed") {
		t.Errorf("error = %v, want it to surface the API message", err)
	}
}
