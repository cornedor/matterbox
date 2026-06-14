package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompletionsURL(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8321":     "http://127.0.0.1:8321/v1/chat/completions",
		"http://127.0.0.1:8321/":    "http://127.0.0.1:8321/v1/chat/completions",
		"http://127.0.0.1:8321/v1":  "http://127.0.0.1:8321/v1/chat/completions",
		"http://127.0.0.1:8321/v1/": "http://127.0.0.1:8321/v1/chat/completions",
		"":                          defaultEndpoint + "/v1/chat/completions",
	}
	for in, want := range cases {
		if got := completionsURL(in); got != want {
			t.Errorf("completionsURL(%q)=%q want %q", in, got, want)
		}
	}
}

func TestStripThinking(t *testing.T) {
	cases := map[string]string{
		"plain answer":                         "plain answer",
		"<think>reasoning</think>the answer":   "the answer",
		"a<think>r</think>b<think>r2</think>c": "abc",
		"<think>cut off mid thought":           "", // unclosed → drop trailing
		"answer first<think>then cut off":      "answer first",
		"  <think>x</think>  spaced  ":         "spaced",
	}
	for in, want := range cases {
		if got := StripThinking(in); got != want {
			t.Errorf("StripThinking(%q)=%q want %q", in, got, want)
		}
	}
}

func TestComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing/wrong auth header: %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Stream {
			t.Error("daemon should use non-streaming requests")
		}
		if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
			t.Errorf("unexpected messages: %+v", req.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"<think>hmm</think>Bob needs a review."}}]}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "secret", "test-model")
	got, err := c.Complete(context.Background(), "system prompt", "transcript")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "Bob needs a review." {
		t.Errorf("got %q, want stripped answer", got)
	}
}

func TestCompleteServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "model not loaded")
	}))
	defer srv.Close()

	c := New(srv.URL, "", "m")
	_, err := c.Complete(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "model not loaded") {
		t.Fatalf("want error mentioning server body, got %v", err)
	}
}
