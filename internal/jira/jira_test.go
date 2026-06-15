package jira

import (
	"context"
	"encoding/json"
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

func TestGetCachesAndInvalidates(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
