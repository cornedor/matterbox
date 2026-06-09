// Package embed talks to an OpenAI-compatible /v1/embeddings endpoint (a local
// llama.cpp server running an embedding model, by default) to turn message text
// and search queries into vectors for matterbox's semantic search. It is the
// embedding twin of the chat-completions client used for summaries and AI
// search, and deliberately has no dependency on the UI or store packages so it
// can be unit-tested against an httptest server with no model running.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// defaultEndpoint is the embeddings server matterbox assumes when none is
// configured: a second local llama.cpp instance (separate from the chat server
// on :8321) launched with --embeddings. See scripts/llama-embeddings.sh.
const defaultEndpoint = "http://127.0.0.1:8322"

// requestTimeout bounds a single /v1/embeddings call. Embedding a batch of
// short messages on a small local model is fast; this is a generous ceiling so
// a stalled server fails the backfill batch rather than hanging it forever.
const requestTimeout = 2 * time.Minute

// Client is a configured embeddings caller. The zero value is not usable; use
// New. It is safe for concurrent use (http.Client is).
type Client struct {
	endpoint string
	apiKey   string
	model    string
	// dim, when > 0, truncates each returned vector to its first dim components
	// and renormalizes — valid for Matryoshka models like EmbeddingGemma, where
	// the leading dimensions are themselves a usable lower-dimensional embedding.
	// 0 keeps the model's native dimensionality.
	dim  int
	http *http.Client
}

// New builds a Client. endpoint may be a bare host (":8322"-style base URL) or
// already end in "/v1"; embeddingsURL normalizes it. apiKey is optional (sent
// as a Bearer token when non-empty — not needed for a local server). model is
// sent verbatim as the request's "model" field. dim ≤ 0 means "native size".
func New(endpoint, apiKey, model string, dim int) *Client {
	return &Client{
		endpoint: endpoint,
		apiKey:   apiKey,
		model:    model,
		dim:      dim,
		http:     &http.Client{},
	}
}

// embeddingsURL builds the /v1/embeddings URL from a base endpoint, tolerating
// a trailing slash or an endpoint that already ends in "/v1" — mirroring the
// chat client's chatCompletionsURL so both behave the same way.
func embeddingsURL(endpoint string) string {
	e := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if e == "" {
		e = defaultEndpoint
	}
	if strings.HasSuffix(e, "/v1") {
		return e + "/embeddings"
	}
	return e + "/v1/embeddings"
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns one vector per input, in the same order as inputs. It issues a
// single request, so the caller controls batch size (the backfill pass chunks
// posts before calling). An empty inputs slice returns nil with no call. The
// server's per-item order is not trusted: results are reordered by their
// reported index. Each vector is truncated/renormalized to the configured dim.
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if c == nil {
		return nil, fmt.Errorf("embed: nil client")
	}
	if len(inputs) == 0 {
		return nil, nil
	}

	payload, err := json.Marshal(embeddingRequest{Model: c.model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	url := embeddingsURL(c.endpoint)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, fmt.Errorf("embeddings server %s: %s", resp.Status, msg)
	}

	var decoded embeddingResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(decoded.Data) != len(inputs) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(decoded.Data), len(inputs))
	}

	// Trust the reported index over array position so a reordering server can't
	// silently misalign vectors with their source text.
	sort.Slice(decoded.Data, func(i, j int) bool {
		return decoded.Data[i].Index < decoded.Data[j].Index
	})

	out := make([][]float32, len(decoded.Data))
	for i, d := range decoded.Data {
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("embeddings: empty vector at index %d", i)
		}
		out[i] = c.shrink(d.Embedding)
	}
	return out, nil
}

// EmbedOne is the single-text convenience used for the search query itself.
func (c *Client) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	vecs, err := c.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embeddings: no vector returned")
	}
	return vecs[0], nil
}

// shrink truncates v to the configured dim (when set and shorter than v) and
// renormalizes to unit length. Truncation is only meaningful for Matryoshka
// models; for others, set dim to 0 to keep the full vector. Renormalizing keeps
// the stored-vector invariant that a dot product equals cosine similarity.
func (c *Client) shrink(v []float32) []float32 {
	if c.dim > 0 && c.dim < len(v) {
		v = v[:c.dim]
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		// Copy so callers never alias the decoded slice; an all-zero vector is
		// returned as-is (it carries no direction to normalize).
		return append([]float32(nil), v...)
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out
}
