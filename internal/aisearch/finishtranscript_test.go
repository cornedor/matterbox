package aisearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"matterbox/internal/store"
)

// TestFinishAnswersEveryToolCall: a model may call finish alongside other
// tools in one turn. The returned History must still answer every tool_call
// id of that turn, or a follow-up replays an invalid transcript (HTTP 400).
func TestFinishAnswersEveryToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[
			{"id":"a","type":"function","function":{"name":"list_channels","arguments":"{}"}},
			{"id":"b","type":"function","function":{"name":"finish","arguments":"{\"answer\":\"done\"}"}},
			{"id":"c","type":"function","function":{"name":"search_messages","arguments":"{\"query\":\"x\"}"}}]}}]}`))
	}))
	defer srv.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "fin.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ch := make(chan Update, 8)
	go Run(context.Background(), Config{Store: st, Endpoint: srv.URL, MaxSteps: 3}, testCatalog(),
		[]Message{{Role: "user", Content: "q"}}, ch)

	var final Update
	for u := range ch {
		if u.Done {
			final = u
		}
	}
	if final.Err != nil || final.Answer != "done" {
		t.Fatalf("want answer %q, got %q (err %v)", "done", final.Answer, final.Err)
	}
	answered := map[string]bool{}
	for _, m := range final.History {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	for _, id := range []string{"a", "b", "c"} {
		if !answered[id] {
			t.Errorf("tool_call %q has no tool result in History", id)
		}
	}
	if last := final.History[len(final.History)-1]; last.Role != "assistant" || last.Content != "done" {
		t.Errorf("History should end with the answer turn, got %+v", last)
	}
}
