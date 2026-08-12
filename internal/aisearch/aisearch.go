// Package aisearch is the headless, UI-free core of matterbox's agentic
// search: given a natural-language question and a small set of tools, a local
// (OpenAI-compatible) chat model drives the tools itself — searching the local
// message cache, discovering which channels a topic lives in, and narrowing
// down — until it can answer. The messages it surfaces are collected as
// store.SearchHit values; the model's prose answer comes back alongside.
//
// It is shared by the TUI (internal/ui, which streams Updates onto a channel to
// render a live trace and clickable hit bubbles) and the `matterbox listen`
// daemon (which calls Ask for a one-shot answer to forward to Telegram). It
// depends only on the store, embeddings, and Mattermost model packages — never
// on the UI — so both callers run the exact same agent loop.
package aisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"matterbox/internal/embed"
	"matterbox/internal/store"
)

// defaultMaxSteps caps the tool-call rounds when Config.MaxSteps is unset.
const defaultMaxSteps = 32

// Config carries everything Run/Ask need that isn't the catalog or the
// conversation: the chat endpoint + model, the step budget, the message store,
// and the optional embeddings client for semantic/hybrid search.
type Config struct {
	Store    *store.Store
	Endpoint string
	APIKey   string
	Model    string
	MaxSteps int

	// EmbedClient enables search_messages' semantic/hybrid modes. nil makes
	// those modes fall back to keyword. EmbedModel/EmbedDim identify the model
	// (and Matryoshka truncation) the stored vectors were built with.
	EmbedClient *embed.Client
	EmbedModel  string
	EmbedDim    int
}

// Message is one chat-completions turn (system/user/assistant/tool).
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a function call the model requested.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolCallFunc `json:"function"`
}

// ToolCallFunc is the name + JSON arguments of a requested tool call.
type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Update is one message from the agent loop: either a trace step, or a terminal
// result (Answer + collected Hits, or an Err). On a terminal update History
// holds the full transcript so a follow-up can continue the same conversation.
type Update struct {
	Step      TraceStep
	HasStep   bool
	Done      bool
	Answer    string
	Hits      []store.SearchHit
	Tentative bool // answer is an unconfirmed best guess (step budget ran out)
	History   []Message
	Err       error
}

// Result is the terminal outcome of Ask: the answer, the hits it surfaced,
// whether it's a best guess, and the transcript for follow-ups.
type Result struct {
	Answer    string
	Hits      []store.SearchHit
	Tentative bool
	History   []Message
}

// ---- chat wire -----------------------------------------------------------

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []tool    `json:"tools,omitempty"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		FinishReason string  `json:"finish_reason"`
		Message      Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// postChat sends one non-streaming chat-completions request with tools and
// returns the parsed response.
func postChat(ctx context.Context, endpoint, apiKey, mdl string, messages []Message, tools []tool) (*chatResponse, error) {
	body := chatRequest{
		Model:       mdl,
		Messages:    messages,
		Tools:       tools,
		Temperature: 0.2,
		Stream:      false,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	url := chatCompletionsURL(endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, msg)
	}
	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, errors.New(out.Error.Message)
	}
	return &out, nil
}

// chatCompletionsURL builds the chat-completions URL from a base endpoint,
// tolerating a trailing slash or an endpoint that already ends in "/v1".
func chatCompletionsURL(endpoint string) string {
	e := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if e == "" {
		e = "http://127.0.0.1:8321"
	}
	if strings.HasSuffix(e, "/v1") {
		return e + "/chat/completions"
	}
	return e + "/v1/chat/completions"
}

// ---- the agent loop ------------------------------------------------------

// Run drives the bounded tool-call loop and pushes updates onto ch. It builds
// the tool implementations from cfg + cat, closes ch on return, and stops early
// if ctx is cancelled. The TUI uses this directly to render a live trace; most
// other callers want Ask.
func Run(ctx context.Context, cfg Config, cat Catalog, messages []Message, ch chan<- Update) {
	defer close(ch)

	tools := Tools{
		store:       cfg.Store,
		catalog:     cat,
		refs:        newHitRefTable(),
		memo:        newCallMemo(),
		embedClient: cfg.EmbedClient,
		embedModel:  cfg.EmbedModel,
		embedDim:    cfg.EmbedDim,
		ctx:         ctx,
	}
	maxSteps := cfg.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}

	send := func(u Update) bool {
		select {
		case ch <- u:
			return true
		case <-ctx.Done():
			return false
		}
	}

	defs := toolDefs()
	var collected []store.SearchHit
	seenPost := map[string]struct{}{}
	addHits := func(hits []store.SearchHit) {
		for _, h := range hits {
			if h.Match == nil || len(collected) >= aiSearchMaxHits {
				continue
			}
			if _, dup := seenPost[h.Match.Id]; dup {
				continue
			}
			seenPost[h.Match.Id] = struct{}{}
			collected = append(collected, h)
		}
	}

	for step := 0; step < maxSteps; step++ {
		resp, err := postChat(ctx, cfg.Endpoint, cfg.APIKey, cfg.Model, messages, defs)
		if err != nil {
			if ctx.Err() != nil {
				return // cancelled — the reader stopped caring
			}
			send(Update{Done: true, Err: err, Hits: collected, History: messages})
			return
		}
		if len(resp.Choices) == 0 {
			send(Update{Done: true, Err: errors.New("model returned no choices"), Hits: collected, History: messages})
			return
		}
		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			// The model answered in prose without calling finish — accept it, and
			// keep the assistant turn in the transcript so a follow-up continues it.
			answer := strings.TrimSpace(msg.Content)
			if answer == "" {
				answer = "(the model returned an empty answer)"
			}
			messages = append(messages, Message{Role: "assistant", Content: msg.Content})
			send(Update{Done: true, Answer: answer, Hits: collected, History: messages})
			return
		}

		// Echo the assistant turn (with its tool_calls) back into the history.
		messages = append(messages, Message{Role: "assistant", Content: msg.Content, ToolCalls: msg.ToolCalls})

		for _, tc := range msg.ToolCalls {
			switch tc.Function.Name {
			case "finish":
				var fin struct {
					Answer string `json:"answer"`
				}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &fin)
				answer := strings.TrimSpace(fin.Answer)
				if answer == "" {
					answer = "(no answer text provided)"
				}
				// Close out the finish tool call and record the answer as an
				// assistant turn so the transcript is a valid continuation point.
				messages = append(messages,
					Message{Role: "tool", ToolCallID: tc.ID, Content: "Answer delivered to the user."},
					Message{Role: "assistant", Content: answer})
				send(Update{Done: true, Answer: answer, Hits: collected, History: messages})
				return
			case "search_messages":
				result, ts, hits := tools.execSearch(tc.Function.Arguments)
				addHits(hits)
				if !send(Update{Step: ts, HasStep: true}) {
					return
				}
				messages = append(messages, Message{Role: "tool", ToolCallID: tc.ID, Content: result})
			case "read_around":
				result, ts := tools.execReadAround(tc.Function.Arguments)
				if !send(Update{Step: ts, HasStep: true}) {
					return
				}
				messages = append(messages, Message{Role: "tool", ToolCallID: tc.ID, Content: result})
			case "list_channels":
				result, ts := tools.execListChannels(tc.Function.Arguments)
				if !send(Update{Step: ts, HasStep: true}) {
					return
				}
				messages = append(messages, Message{Role: "tool", ToolCallID: tc.ID, Content: result})
			default:
				messages = append(messages, Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    "Unknown tool. Use search_messages, read_around, list_channels, or finish.",
				})
			}
		}
	}

	// Hit the step cap without a finish. If we gathered anything, give the
	// model one last (tool-less) turn to commit to a best-guess answer from
	// what it has so far, flagged as unconfirmed — better than bailing with a
	// canned line when the evidence is probably enough. With nothing gathered
	// there's nothing to guess from, so keep the honest "ran out" note.
	if len(collected) > 0 {
		if guess, err := finalGuess(ctx, cfg, messages); err == nil {
			hist := append(messages[:len(messages):len(messages)],
				Message{Role: "user", Content: finalGuessNudge},
				Message{Role: "assistant", Content: guess})
			send(Update{Done: true, Answer: guess, Hits: collected, Tentative: true, History: hist})
			return
		} else if ctx.Err() != nil {
			return // cancelled while asking for the guess
		}
		answer := "I gathered some possibly-relevant messages but ran out of search steps before confirming an answer — see the matches below."
		send(Update{Done: true, Hits: collected, Answer: answer,
			History: append(messages, Message{Role: "assistant", Content: answer})})
		return
	}
	answer := "I ran out of search steps before reaching a confident answer."
	send(Update{Done: true, Hits: collected, Answer: answer,
		History: append(messages, Message{Role: "assistant", Content: answer})})
}

// Ask runs the agent loop to its terminal update and returns the result. It is
// the blocking convenience for headless callers (the listen daemon): onStep, if
// non-nil, is invoked for each trace step as it happens (e.g. to update a live
// progress message). A cancelled/timed-out ctx yields ctx.Err().
func Ask(ctx context.Context, cfg Config, cat Catalog, messages []Message, onStep func(TraceStep)) (Result, error) {
	ch := make(chan Update, 8)
	go Run(ctx, cfg, cat, messages, ch)

	var res Result
	var got bool
	for u := range ch {
		switch {
		case u.HasStep:
			if onStep != nil {
				onStep(u.Step)
			}
		case u.Done:
			if u.Err != nil {
				return Result{}, u.Err
			}
			res = Result{Answer: u.Answer, Hits: u.Hits, Tentative: u.Tentative, History: u.History}
			got = true
		}
	}
	if !got {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, errors.New("aisearch: no result")
	}
	return res, nil
}

// finalGuessNudge is the message appended when the step budget is spent: it
// tells the model it can't search again and asks it to commit to a best-effort
// answer from what it already found, while forbidding invention.
const finalGuessNudge = "You've used all your search steps and cannot search again. " +
	"Using only what the searches above turned up, give the user your best-effort answer now, in one or two sentences, naming the channel(s) the evidence came from. " +
	"It's fine to be uncertain — say what the messages suggest even if you couldn't fully confirm it — but do not invent facts the messages don't support."

// finalGuess makes one last chat call with the tools withheld (so the model
// must reply in prose rather than searching again) to coax a best-effort answer
// out of what it has gathered. Returns an error if the call fails or comes back
// empty, so the caller can fall back to the canned note.
func finalGuess(ctx context.Context, cfg Config, messages []Message) (string, error) {
	msgs := append(messages[:len(messages):len(messages)], Message{Role: "user", Content: finalGuessNudge})
	resp, err := postChat(ctx, cfg.Endpoint, cfg.APIKey, cfg.Model, msgs, nil)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("model returned no choices")
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		return "", errors.New("empty answer")
	}
	return answer, nil
}
