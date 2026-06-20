package languagetool

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sampleResponse is a trimmed real /check reply for "som sentnce".
const sampleResponse = `{
  "matches": [
    {
      "message": "Possible spelling mistake found.",
      "shortMessage": "Spelling mistake",
      "offset": 0,
      "length": 3,
      "replacements": [{"value":"some"},{"value":"so"},{"value":"son"},{"value":"com"},{"value":"Tom"},{"value":"sum"},{"value":"Sam"},{"value":"Dom"},{"value":"SAM"},{"value":"sop"}],
      "rule": {"id":"MORFOLOGIK_RULE_EN_US","issueType":"misspelling","category":{"id":"TYPOS"}}
    },
    {
      "message": "The verb is plural.",
      "shortMessage": "Grammar error",
      "offset": 4,
      "length": 7,
      "replacements": [{"value":"sentence"}],
      "rule": {"id":"X","issueType":"grammar","category":{"id":"GRAMMAR"}}
    }
  ]
}`

func TestCheckParsesMatches(t *testing.T) {
	var gotForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/check") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		gotForm = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, sampleResponse)
	}))
	defer srv.Close()

	c := New(srv.URL+"/v2", "auto", false, 0)
	matches, err := c.Check(context.Background(), "som sentnce")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(matches))
	}

	// Form carried the text, language and the default level.
	if !strings.Contains(gotForm, "text=som+sentnce") || !strings.Contains(gotForm, "language=auto") {
		t.Errorf("request form missing fields: %q", gotForm)
	}
	if !strings.Contains(gotForm, "level=default") {
		t.Errorf("non-picky client should send level=default: %q", gotForm)
	}

	first := matches[0]
	if first.Offset != 0 || first.Length != 3 {
		t.Errorf("first span = (%d,%d), want (0,3)", first.Offset, first.Length)
	}
	if first.IssueType != "misspelling" || first.Category != "TYPOS" {
		t.Errorf("first type/category = %q/%q", first.IssueType, first.Category)
	}
	if len(first.Replacements) != maxReplacements {
		t.Errorf("replacements capped to %d, got %d", maxReplacements, len(first.Replacements))
	}
	if first.Replacements[0] != "some" {
		t.Errorf("best replacement = %q, want some", first.Replacements[0])
	}
	if matches[1].IssueType != "grammar" {
		t.Errorf("second issue type = %q, want grammar", matches[1].IssueType)
	}
}

func TestCheckServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL+"/v2", "en-US", false, 0)
	if _, err := c.Check(context.Background(), "hello"); err == nil {
		t.Fatal("expected an error on 500, got nil")
	}
}

func TestCheckPickyLevel(t *testing.T) {
	var gotForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm = string(body)
		_, _ = io.WriteString(w, `{"matches":[]}`)
	}))
	defer srv.Close()

	c := New(srv.URL+"/v2", "en-US", true, 0)
	if _, err := c.Check(context.Background(), "hello"); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(gotForm, "level=picky") {
		t.Errorf("picky client should send level=picky: %q", gotForm)
	}
}

func TestCheckEmptyMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"matches":[]}`)
	}))
	defer srv.Close()

	c := New(srv.URL+"/v2", "en-US", false, 0)
	matches, err := c.Check(context.Background(), "all good")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("got %d matches, want 0", len(matches))
	}
}
