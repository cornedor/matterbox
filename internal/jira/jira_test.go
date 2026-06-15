package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestADFToMarkdown(t *testing.T) {
	tests := []struct {
		name string
		adf  string
		want string
	}{
		{
			name: "nil description",
			adf:  `null`,
			want: "",
		},
		{
			name: "paragraph with marks",
			adf: `{"type":"doc","content":[
				{"type":"paragraph","content":[
					{"type":"text","text":"Hello "},
					{"type":"text","text":"bold","marks":[{"type":"strong"}]},
					{"type":"text","text":" and "},
					{"type":"text","text":"code","marks":[{"type":"code"}]}
				]}
			]}`,
			want: "Hello **bold** and `code`",
		},
		{
			name: "link mark",
			adf: `{"type":"doc","content":[
				{"type":"paragraph","content":[
					{"type":"text","text":"site","marks":[{"type":"link","attrs":{"href":"https://x.test"}}]}
				]}
			]}`,
			want: "[site](https://x.test)",
		},
		{
			name: "heading and bullet list",
			adf: `{"type":"doc","content":[
				{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Title"}]},
				{"type":"bulletList","content":[
					{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},
					{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}
				]}
			]}`,
			want: "## Title\n\n- one\n- two",
		},
		{
			name: "code block keeps verbatim",
			adf: `{"type":"doc","content":[
				{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"a := 1"}]}
			]}`,
			want: "```go\na := 1\n```",
		},
		{
			name: "unknown block degrades to text",
			adf: `{"type":"doc","content":[
				{"type":"panel","content":[{"type":"paragraph","content":[{"type":"text","text":"note"}]}]}
			]}`,
			want: "note",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adfToMarkdown(json.RawMessage(tt.adf))
			if got != tt.want {
				t.Errorf("adfToMarkdown()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestGetParsesIssue(t *testing.T) {
	const body = `{
		"key": "ABC-123",
		"fields": {
			"summary": "Fix the thing",
			"description": {"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"do it"}]}]},
			"labels": ["backend","urgent"],
			"updated": "2026-06-15T09:41:00.000+0200",
			"status": {"name": "In Progress"},
			"priority": {"name": "High"},
			"issuetype": {"name": "Bug"},
			"assignee": {"displayName": "Ada Lovelace"},
			"reporter": {"displayName": "Alan Turing"}
		}
	}`

	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	if !c.Enabled() {
		t.Fatal("client should be enabled")
	}
	iss, err := c.Get(context.Background(), "ABC-123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("expected Basic auth, got %q", gotAuth)
	}
	if gotPath != "/rest/api/3/issue/ABC-123" {
		t.Errorf("path = %q", gotPath)
	}
	if iss.Summary != "Fix the thing" {
		t.Errorf("summary = %q", iss.Summary)
	}
	if iss.Status != "In Progress" || iss.Priority != "High" || iss.Type != "Bug" {
		t.Errorf("meta = %+v", iss)
	}
	if iss.Assignee != "Ada Lovelace" || iss.Reporter != "Alan Turing" {
		t.Errorf("people = %q / %q", iss.Assignee, iss.Reporter)
	}
	if iss.Description != "do it" {
		t.Errorf("description = %q", iss.Description)
	}
	if iss.Updated.IsZero() {
		t.Error("updated not parsed")
	}
	if iss.URL != srv.URL+"/browse/ABC-123" {
		t.Errorf("url = %q", iss.URL)
	}
}

func TestStoryPointsAutoDetected(t *testing.T) {
	const fieldMeta = `[
		{"id":"summary","name":"Summary"},
		{"id":"customfield_10016","name":"Story point estimate"},
		{"id":"customfield_99","name":"Sprint"}
	]`
	const issueBody = `{"key":"ABC-7","fields":{"summary":"s","assignee":{"displayName":"x"},"customfield_10016":5}}`

	var gotIssueQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/field":
			_, _ = w.Write([]byte(fieldMeta))
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
			gotIssueQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(issueBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	iss, err := c.Get(context.Background(), "ABC-7")
	if err != nil {
		t.Fatal(err)
	}
	if iss.StoryPoints != "5" {
		t.Errorf("StoryPoints = %q, want 5", iss.StoryPoints)
	}
	if !strings.Contains(gotIssueQuery, "customfield_10016") {
		t.Errorf("issue request did not include the detected field: %q", gotIssueQuery)
	}
}

func TestStoryPointsOverrideSkipsMetadata(t *testing.T) {
	var metaCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/field":
			metaCalls++
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{"key":"ABC-8","fields":{"summary":"s","assignee":{"displayName":"x"},"customfield_42":2.5}}`))
		}
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok", StoryPointsField: "customfield_42"})
	iss, err := c.Get(context.Background(), "ABC-8")
	if err != nil {
		t.Fatal(err)
	}
	if iss.StoryPoints != "2.5" {
		t.Errorf("StoryPoints = %q, want 2.5", iss.StoryPoints)
	}
	if metaCalls != 0 {
		t.Errorf("expected no field-metadata call with an override, got %d", metaCalls)
	}
}

func TestStoryPointsAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/field" {
			_, _ = w.Write([]byte(`[{"id":"customfield_10016","name":"Story point estimate"}]`))
			return
		}
		// Issue has no story-points value set (null).
		_, _ = w.Write([]byte(`{"key":"ABC-9","fields":{"summary":"s","assignee":{"displayName":"x"},"customfield_10016":null}}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	iss, err := c.Get(context.Background(), "ABC-9")
	if err != nil {
		t.Fatal(err)
	}
	if iss.StoryPoints != "" {
		t.Errorf("StoryPoints = %q, want empty", iss.StoryPoints)
	}
}

func TestStoryPointsMetadataFailureIsNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/field" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"key":"ABC-1","fields":{"summary":"s","assignee":{"displayName":"x"}}}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	iss, err := c.Get(context.Background(), "ABC-1")
	if err != nil {
		t.Fatalf("a down field-metadata endpoint must not fail the issue fetch: %v", err)
	}
	if iss.StoryPoints != "" || iss.Summary != "s" {
		t.Errorf("unexpected issue %+v", iss)
	}
}

func TestGetCachesAndInvalidates(t *testing.T) {
	var calls int // issue fetches only — the field-metadata probe is routed apart
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/field" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		calls++
		_, _ = w.Write([]byte(`{"key":"ABC-1","fields":{"summary":"s","assignee":{"displayName":"x"}}}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	for i := 0; i < 3; i++ {
		if _, err := c.Get(context.Background(), "ABC-1"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call (cached), got %d", calls)
	}
	c.Invalidate("ABC-1")
	if _, err := c.Get(context.Background(), "ABC-1"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected refetch after Invalidate, got %d calls", calls)
	}
}

func TestGetUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	_, err := c.Get(context.Background(), "ABC-1")
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Errorf("expected auth error, got %v", err)
	}
}

func TestDisabledClient(t *testing.T) {
	c := New(Config{BaseURL: "https://example.atlassian.net"}) // no creds
	if c.Enabled() {
		t.Error("client without creds should be disabled")
	}
	if _, err := c.Get(context.Background(), "ABC-1"); err == nil {
		t.Error("expected error from disabled client")
	}
}

func TestGetParsesIDs(t *testing.T) {
	const body = `{"key":"ABC-1","fields":{"summary":"s",
		"priority":{"id":"2","name":"High"},
		"assignee":{"accountId":"acc-9","displayName":"Ada"}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok", StoryPointsField: "customfield_1"})
	iss, err := c.Get(context.Background(), "ABC-1")
	if err != nil {
		t.Fatal(err)
	}
	if iss.PriorityID != "2" {
		t.Errorf("PriorityID = %q, want 2", iss.PriorityID)
	}
	if iss.AssigneeAccountID != "acc-9" {
		t.Errorf("AssigneeAccountID = %q, want acc-9", iss.AssigneeAccountID)
	}
}

func TestTransitions(t *testing.T) {
	const body = `{"transitions":[
		{"id":"11","name":"Start","to":{"id":"3","name":"In Progress"}},
		{"id":"21","name":"Done","to":{"id":"5","name":"Done"}}
	]}`
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	opts, err := c.Transitions(context.Background(), "ABC-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/rest/api/3/issue/ABC-1/transitions" {
		t.Errorf("path = %q", gotPath)
	}
	if len(opts) != 2 || opts[0].ID != "11" || opts[0].Name != "In Progress" || opts[1].Name != "Done" {
		t.Errorf("transitions = %+v", opts)
	}
}

func TestDoTransitionPostsAndInvalidates(t *testing.T) {
	var gotMethod, gotBody string
	var issueCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/field":
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/transitions"):
			gotMethod = r.Method
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.WriteHeader(http.StatusNoContent)
		default: // GET issue
			issueCalls++
			_, _ = w.Write([]byte(`{"key":"ABC-1","fields":{"summary":"s"}}`))
		}
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	if _, err := c.Get(context.Background(), "ABC-1"); err != nil { // prime the cache
		t.Fatal(err)
	}
	if err := c.DoTransition(context.Background(), "ABC-1", "11"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(gotBody, `"transition"`) || !strings.Contains(gotBody, `"11"`) {
		t.Errorf("body = %q", gotBody)
	}
	// A transition must drop the cached copy so the next Get refetches.
	if _, err := c.Get(context.Background(), "ABC-1"); err != nil {
		t.Fatal(err)
	}
	if issueCalls != 2 {
		t.Errorf("expected refetch after transition, got %d issue calls", issueCalls)
	}
}

func TestPrioritiesCached(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`[{"id":"1","name":"Highest"},{"id":"2","name":"High"}]`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	for i := 0; i < 3; i++ {
		opts, err := c.Priorities(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(opts) != 2 || opts[0].Name != "Highest" {
			t.Errorf("priorities = %+v", opts)
		}
	}
	if calls != 1 {
		t.Errorf("expected 1 call (cached), got %d", calls)
	}
}

func TestSetPriority(t *testing.T) {
	var gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	if err := c.SetPriority(context.Background(), "ABC-1", "3"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if !strings.Contains(gotBody, `"priority"`) || !strings.Contains(gotBody, `"3"`) {
		t.Errorf("body = %q", gotBody)
	}
}

func TestAssignableUsersAndMyself(t *testing.T) {
	var myselfCalls int
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/user/assignable/search":
			if r.URL.Query().Get("issueKey") != "ABC-1" {
				t.Errorf("issueKey = %q", r.URL.Query().Get("issueKey"))
			}
			gotQuery = r.URL.Query().Get("query")
			_, _ = w.Write([]byte(`[{"accountId":"a1","displayName":"Ada"},{"accountId":"a2","displayName":"Alan"}]`))
		case "/rest/api/3/myself":
			myselfCalls++
			_, _ = w.Write([]byte(`{"accountId":"me-1","displayName":"Me"}`))
		}
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
	users, err := c.AssignableUsers(context.Background(), "ABC-1", "ala")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "ala" {
		t.Errorf("forwarded query = %q, want ala", gotQuery)
	}
	if len(users) != 2 || users[0].AccountID != "a1" || users[1].DisplayName != "Alan" {
		t.Errorf("users = %+v", users)
	}
	for i := 0; i < 2; i++ {
		me, err := c.Myself(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if me.AccountID != "me-1" {
			t.Errorf("myself = %+v", me)
		}
	}
	if myselfCalls != 1 {
		t.Errorf("expected myself cached (1 call), got %d", myselfCalls)
	}
}

func TestSetAssignee(t *testing.T) {
	tests := []struct {
		name      string
		accountID string
		wantBody  string
	}{
		{"assign", "a1", `{"accountId":"a1"}`},
		{"unassign", "", `{"accountId":null}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody, gotMethod string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				b, _ := io.ReadAll(r.Body)
				gotBody = strings.TrimSpace(string(b))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
			if err := c.SetAssignee(context.Background(), "ABC-1", tt.accountID); err != nil {
				t.Fatal(err)
			}
			if gotMethod != http.MethodPut {
				t.Errorf("method = %q, want PUT", gotMethod)
			}
			if gotBody != tt.wantBody {
				t.Errorf("body = %q, want %q", gotBody, tt.wantBody)
			}
		})
	}
}

func TestSetStoryPoints(t *testing.T) {
	const fieldMeta = `[{"id":"customfield_10016","name":"Story point estimate"}]`
	var gotBody string
	newSrv := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/rest/api/3/field" {
				_, _ = w.Write([]byte(fieldMeta))
				return
			}
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.WriteHeader(http.StatusNoContent)
		}))
	}

	t.Run("set", func(t *testing.T) {
		srv := newSrv()
		defer srv.Close()
		c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
		if err := c.SetStoryPoints(context.Background(), "ABC-1", "5"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(gotBody, `"customfield_10016":5`) {
			t.Errorf("body = %q", gotBody)
		}
	})

	t.Run("clear", func(t *testing.T) {
		srv := newSrv()
		defer srv.Close()
		c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
		if err := c.SetStoryPoints(context.Background(), "ABC-1", ""); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(gotBody, `"customfield_10016":null`) {
			t.Errorf("body = %q", gotBody)
		}
	})

	t.Run("non-numeric", func(t *testing.T) {
		srv := newSrv()
		defer srv.Close()
		c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
		err := c.SetStoryPoints(context.Background(), "ABC-1", "lots")
		if err == nil || !strings.Contains(err.Error(), "must be a number") {
			t.Errorf("expected numeric error, got %v", err)
		}
	})

	t.Run("no field detected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[]`)) // field metadata returns nothing story-points-like
		}))
		defer srv.Close()
		c := New(Config{BaseURL: srv.URL, Email: "me@x.test", APIToken: "tok"})
		err := c.SetStoryPoints(context.Background(), "ABC-1", "5")
		if err == nil || !strings.Contains(err.Error(), "no story-points field") {
			t.Errorf("expected no-field error, got %v", err)
		}
	})
}
