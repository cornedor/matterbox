package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"matterbox/internal/forge"
)

const diffRefsJSON = `{"iid": 42, "diff_refs": {"base_sha": "base1", "start_sha": "start1", "head_sha": "head1"}}`

const diffsJSON = `[
  {"old_path": "a.go", "new_path": "a.go", "new_file": false, "renamed_file": false,
   "deleted_file": false, "diff": "@@ -1,2 +1,2 @@\n-old\n+new\n"},
  {"old_path": "b.png", "new_path": "b.png", "new_file": true,
   "diff": "Binary files /dev/null and b/b.png differ\n"}
]`

func TestDiff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/merge_requests/42/diffs"):
			if r.URL.Query().Get("page") != "1" {
				t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			}
			w.Write([]byte(diffsJSON))
		case strings.HasSuffix(r.URL.Path, "/merge_requests/42"):
			w.Write([]byte(diffRefsJSON))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	d, err := c.Diff(context.Background(), "g/p", 42)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if d.Refs.HeadSHA != "head1" || d.Refs.BaseSHA != "base1" || d.Refs.StartSHA != "start1" {
		t.Errorf("refs = %+v", d.Refs)
	}
	if len(d.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(d.Files))
	}
	if d.Truncated {
		t.Error("short page reported as truncated")
	}
	if !d.Files[1].Binary {
		t.Errorf("binary file not flagged: %+v", d.Files[1])
	}

	// Second call is served from the cache: the server would fail the test if
	// it were hit again with an unexpected path, so assert on the count.
	hits := 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if strings.HasSuffix(r.URL.Path, "/diffs") {
			w.Write([]byte(diffsJSON))
			return
		}
		w.Write([]byte(diffRefsJSON))
	}))
	defer srv2.Close()
	c2 := newTestClient(srv2)
	if _, err := c2.Diff(context.Background(), "g/p", 42); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	after := hits
	if _, err := c2.Diff(context.Background(), "g/p", 42); err != nil {
		t.Fatalf("cached Diff: %v", err)
	}
	if hits != after {
		t.Errorf("cached Diff made %d more calls", hits-after)
	}
	c2.Invalidate("g/p", 42)
	if _, err := c2.Diff(context.Background(), "g/p", 42); err != nil {
		t.Fatalf("refetched Diff: %v", err)
	}
	if hits == after {
		t.Error("Invalidate did not drop the cached diff")
	}
}

// An instance older than GitLab 15.7 has no /diffs endpoint; the deprecated
// /changes answers with everything in one call.
func TestDiffFallsBackToChanges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/diffs"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message": "404 Not Found"}`))
		case strings.HasSuffix(r.URL.Path, "/changes"):
			w.Write([]byte(`{"diff_refs": {"base_sha": "b", "start_sha": "s", "head_sha": "h"},
			  "overflow": true,
			  "changes": [{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -1 +1 @@\n-x\n+y\n"}]}`))
		default:
			w.Write([]byte(diffRefsJSON))
		}
	}))
	defer srv.Close()

	d, err := newTestClient(srv).Diff(context.Background(), "g/p", 42)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(d.Files) != 1 || d.Files[0].NewPath != "a.go" {
		t.Fatalf("files = %+v", d.Files)
	}
	if !d.Truncated {
		t.Error("overflow not reported as truncated")
	}
	if d.Refs.HeadSHA != "h" {
		t.Errorf("refs = %+v", d.Refs)
	}
}

const discussionsJSON = `[
  {"id": "abc123", "notes": [
    {"id": 1, "body": "why this?", "system": false, "resolved": false,
     "created_at": "2026-06-15T06:52:20.294Z",
     "author": {"name": "Grace Hopper"},
     "position": {"position_type": "text", "old_path": "a.go", "new_path": "a.go", "new_line": 2}},
    {"id": 2, "body": "because", "system": false,
     "author": {"name": "Ada Lovelace"},
     "position": {"position_type": "text", "new_path": "a.go", "new_line": 2}}
  ]},
  {"id": "overall", "notes": [
    {"id": 3, "body": "looks good", "system": false, "author": {"name": "Ada Lovelace"}}
  ]},
  {"id": "sys", "notes": [
    {"id": 4, "body": "changed the title", "system": true, "author": {"name": "Ada Lovelace"},
     "position": {"position_type": "text", "new_path": "a.go", "new_line": 9}}
  ]}
]`

func TestThreads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(discussionsJSON))
	}))
	defer srv.Close()

	th, err := newTestClient(srv).Threads(context.Background(), "g/p", 42)
	if err != nil {
		t.Fatalf("Threads: %v", err)
	}
	// The positioned one and the merge request's own thread survive; the
	// system note does not.
	if len(th) != 2 {
		t.Fatalf("threads = %d (%+v), want 2", len(th), th)
	}
	if th[0].ID != "abc123" || th[0].Path != "a.go" || th[0].NewLine != 2 || !th[0].Inline() {
		t.Errorf("inline thread = %+v", th[0])
	}
	if len(th[0].Notes) != 2 || th[0].Notes[0].Author != "Grace Hopper" {
		t.Errorf("notes = %+v", th[0].Notes)
	}
	if th[0].Notes[0].Created.IsZero() {
		t.Error("created_at not parsed")
	}
	if th[1].ID != "overall" || th[1].Inline() {
		t.Errorf("overall thread = %+v", th[1])
	}
	if got := forge.InlineThreads(th); len(got) != 1 || got[0].ID != "abc123" {
		t.Errorf("InlineThreads = %+v", got)
	}
}

func TestAddNotePosition(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Write([]byte(`{"id": "new"}`))
	}))
	defer srv.Close()

	err := newTestClient(srv).AddNote(context.Background(), "g/p", 42, forge.NewNote{
		Body:    "nit: name",
		Refs:    forge.DiffRefs{BaseSHA: "b", StartSHA: "s", HeadSHA: "h"},
		OldPath: "a.go",
		NewPath: "a.go",
		NewLine: 7,
	})
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	pos, _ := got["position"].(map[string]any)
	if pos == nil {
		t.Fatalf("no position in %v", got)
	}
	if pos["new_line"] != float64(7) {
		t.Errorf("new_line = %v", pos["new_line"])
	}
	// An added line has no old number, and sending a zero is rejected by GitLab.
	if _, ok := pos["old_line"]; ok {
		t.Errorf("old_line sent for an added line: %v", pos["old_line"])
	}
	if pos["head_sha"] != "h" || pos["position_type"] != "text" {
		t.Errorf("position = %v", pos)
	}
}

func TestAddNoteReply(t *testing.T) {
	var path string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	err := newTestClient(srv).AddNote(context.Background(), "g/p", 42, forge.NewNote{
		Body:    "agreed",
		ReplyTo: "abc123",
	})
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if !strings.HasSuffix(path, "/discussions/abc123/notes") {
		t.Errorf("path = %s", path)
	}
	if _, ok := got["position"]; ok {
		t.Errorf("reply carried a position: %v", got)
	}
}

func TestResolveThread(t *testing.T) {
	var method, path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		w.Write([]byte(`{"id": "abc123"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.ResolveThread(context.Background(), "g/p", 42, "abc123", true); err != nil {
		t.Fatalf("ResolveThread: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %s, want PUT", method)
	}
	if !strings.HasSuffix(path, "/merge_requests/42/discussions/abc123") {
		t.Errorf("path = %s", path)
	}
	if body["resolved"] != true {
		t.Errorf("body = %v, want resolved true", body)
	}

	// Reopening is the same call with the flag flipped.
	if err := c.ResolveThread(context.Background(), "g/p", 42, "abc123", false); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if body["resolved"] != false {
		t.Errorf("reopen body = %v", body)
	}

	// No discussion id is a caller bug, not a request.
	if err := c.ResolveThread(context.Background(), "g/p", 42, "", true); err == nil {
		t.Error("resolving nothing was accepted")
	}
}
