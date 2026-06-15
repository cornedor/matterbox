// Package chat talks to an OpenAI-compatible /v1/chat/completions endpoint (a
// local llama.cpp / Ollama style server, by default) to produce short text
// completions. It is the non-streaming, headless twin of the TUI's streaming
// summary client (internal/ui/summary.go): the `matterbox listen` daemon uses
// it to summarize the context around a mention before pushing a notification.
//
// Like internal/embed it deliberately has no dependency on the UI or store
// packages so it can be unit-tested against an httptest server with no model
// running.
package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultEndpoint is the chat server matterbox assumes when none is configured:
// the local llama.cpp instance on :8321 (the same one the TUI summary command
// uses). The embeddings server lives on a separate port (:8322).
const defaultEndpoint = "http://127.0.0.1:8321"

// requestTimeout bounds a single chat-completions call. A small local model can
// be slow on the first token, so this is generous; a stalled server fails the
// call rather than hanging the daemon's notify goroutine forever.
const requestTimeout = 3 * time.Minute

// Client is a configured chat-completions caller. The zero value is not usable;
// use New. It is safe for concurrent use (http.Client is).
type Client struct {
	endpoint string
	apiKey   string
	model    string
	http     *http.Client
}

// New builds a Client. endpoint may be a bare base URL or already end in "/v1";
// completionsURL normalizes it. apiKey is optional (sent as a Bearer token when
// non-empty — not needed for a local server). model is sent verbatim.
func New(endpoint, apiKey, model string) *Client {
	return &Client{
		endpoint: endpoint,
		apiKey:   apiKey,
		model:    model,
		http:     &http.Client{},
	}
}

// completionsURL builds the /v1/chat/completions URL from a base endpoint,
// tolerating a trailing slash or an endpoint that already ends in "/v1" —
// mirroring the embeddings client so both behave the same way.
func completionsURL(endpoint string) string {
	e := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if e == "" {
		e = defaultEndpoint
	}
	if strings.HasSuffix(e, "/v1") {
		return e + "/chat/completions"
	}
	return e + "/v1/chat/completions"
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature"`
}

type response struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends a system + user message pair and returns the model's answer
// with any <think>…</think> reasoning stripped. It is non-streaming: the whole
// reply is returned at once, which is all the notifier needs.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("chat: nil client")
	}

	payload, err := json.Marshal(request{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Stream:      false,
		Temperature: 0.3,
	})
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	url := completionsURL(c.endpoint)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("call %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("chat server %s: %s", resp.Status, msg)
	}

	var decoded response
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return "", fmt.Errorf("chat server: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("chat server returned no choices")
	}
	return StripThinking(decoded.Choices[0].Message.Content), nil
}

// StripThinking removes <think>…</think> reasoning blocks from a model reply,
// keeping only the user-visible answer. An unclosed <think> (the model was cut
// off mid-reasoning) drops everything after the tag. Exported so callers can
// reuse it on content gathered elsewhere; mirrors the TUI's splitThinking.
func StripThinking(raw string) string {
	var b strings.Builder
	rest := raw
	for {
		i := strings.Index(rest, "<think>")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		rest = rest[i+len("<think>"):]
		j := strings.Index(rest, "</think>")
		if j < 0 {
			// Unclosed: discard the trailing reasoning.
			break
		}
		rest = rest[j+len("</think>"):]
	}
	return strings.TrimSpace(b.String())
}
