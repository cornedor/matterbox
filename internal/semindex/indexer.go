// Package semindex orchestrates embedding matterbox's stored messages for
// semantic search. It is the glue between the embeddings transport (internal/
// embed) and the message store (internal/store): pull posts that lack a vector,
// ask the server to embed them, write the vectors back. Both the TUI's
// background indexer and the `matterbox embed` CLI command drive it, so the
// embed-a-batch logic lives here exactly once.
package semindex

import (
	"context"
	"fmt"
	"strings"
	"time"

	"matterbox/internal/embed"
	"matterbox/internal/store"
)

// DefaultBatch is the number of posts embedded per server round-trip when the
// caller doesn't specify one. Small enough to stay well inside EmbeddingGemma's
// batch token budget (see scripts/llama-embeddings.sh), large enough that the
// per-request overhead is amortised.
const DefaultBatch = 64

// maxChunkRunes bounds a single embedding input. EmbeddingGemma's context is a
// hard 2048 tokens (its trained length — the server rejects longer inputs and
// raising -c can't lift it). A long message is split into chunks of at most this
// many runes; each chunk's vector is then mean-pooled into one vector for the
// post (see RunOnce). 1200 runes keeps ordinary and moderately dense text (code,
// URLs — up to ~1.7 tokens/rune) inside 2048 tokens with the document prefix.
// This is only a first-try heuristic: a rune cap can't *guarantee* a token
// count, so a chunk that still overflows is split further at embed time (see
// embedSpan) rather than aborting the backfill. Splitting (not truncating) keeps
// the tail of a long paste searchable, and guarantees every post gets a vector
// so it's never re-queued forever.
const maxChunkRunes = 1200

// Indexer embeds missing posts under a single model identity. It holds no
// mutable state, so the same value can be reused across batches and is safe to
// rebuild cheaply per call. A nil store or client makes every method a no-op.
type Indexer struct {
	store  *store.Store
	client *embed.Client
	tag    string // per-vector model identity (see ModelTag)
	batch  int
	// share is the percentage of wall-clock the backfill may spend embedding,
	// 0 or 100 meaning "no pacing". See SetGPUShare.
	share int
}

// SetGPUShare paces Backfill so it embeds for at most share percent of the
// wall-clock, idling for the rest. A backfill otherwise runs the embedding
// server flat out, and on a machine where the GPU also drives the display that
// is visible as dropped frames — the model server and the compositor are
// contending for the same device. share 50 halves throughput and hands back
// half the GPU; 0 or 100 disables pacing entirely.
//
// Pacing is proportional rather than a fixed sleep on purpose: batches vary in
// how long they take, and a fixed pause would be most of the cycle for a quick
// batch and a rounding error for a slow one.
func (ix *Indexer) SetGPUShare(share int) {
	if ix == nil || share <= 0 || share >= 100 {
		return
	}
	ix.share = share
}

// New builds an Indexer. model is the configured embedding model id and dim its
// truncation (0 = native); together they form the stored ModelTag so that
// changing either re-embeds the corpus rather than mixing vector spaces. A
// non-positive batch falls back to DefaultBatch.
func New(st *store.Store, client *embed.Client, model string, dim, batch int) *Indexer {
	if batch <= 0 {
		batch = DefaultBatch
	}
	return &Indexer{store: st, client: client, tag: ModelTag(model, dim), batch: batch}
}

// ModelTag is the model identity stored alongside each vector. Folding the
// truncation dim into the tag means a config change to either the model or the
// dim makes the existing vectors "not ours", so PostsMissingVectors re-pends
// everything and the store never ends up comparing vectors of different lengths.
func ModelTag(model string, dim int) string {
	if dim > 0 {
		return fmt.Sprintf("%s@%d", model, dim)
	}
	return model
}

// Tag returns the model identity this indexer writes under.
func (ix *Indexer) Tag() string {
	if ix == nil {
		return ""
	}
	return ix.tag
}

// RunOnce embeds up to one batch of not-yet-embedded posts (newest first) and
// returns how many it wrote and whether more remain. (0, false, nil) means the
// corpus is fully embedded. An error from the embeddings server is returned
// verbatim so the caller can decide to back off — the server is optional and
// may simply be down, which is not fatal to matterbox.
func (ix *Indexer) RunOnce(ctx context.Context) (n int, more bool, err error) {
	if ix == nil || ix.store == nil || ix.client == nil {
		return 0, false, nil
	}
	pending, err := ix.store.PostsMissingVectors(ix.tag, ix.batch)
	if err != nil {
		return 0, false, fmt.Errorf("find pending: %w", err)
	}
	if len(pending) == 0 {
		return 0, false, nil
	}
	// Flatten posts into chunk inputs, remembering which post each chunk
	// belongs to, so a long message can span several inputs and still resolve
	// to a single stored vector.
	var inputs []string
	var owner []int // owner[k] = index into pending of input k's post
	for i, p := range pending {
		for _, c := range chunks(p.Message) {
			inputs = append(inputs, embed.DocumentText(c))
			owner = append(owner, i)
		}
	}
	vecs, err := ix.embedAll(ctx, inputs)
	if err != nil {
		return 0, false, err
	}
	if len(vecs) != len(inputs) {
		return 0, false, fmt.Errorf("embed: got %d vectors for %d inputs", len(vecs), len(inputs))
	}
	// Mean-pool each post's chunk vectors. The store L2-normalizes on write, so
	// summing (rather than averaging) is enough — the direction is the mean.
	sums := make([][]float32, len(pending))
	for k, v := range vecs {
		o := owner[k]
		if sums[o] == nil {
			sums[o] = make([]float32, len(v))
		}
		for j := range v {
			sums[o][j] += v[j]
		}
	}
	items := make([]store.VectorInput, len(pending))
	for i := range pending {
		items[i] = store.VectorInput{PostID: pending[i].ID, Vec: sums[i]}
	}
	if err := ix.store.UpsertVectors(items, ix.tag, time.Now().UnixMilli()); err != nil {
		return 0, false, fmt.Errorf("store vectors: %w", err)
	}
	// A full batch implies there may be more; a short batch means we drained the
	// queue. Worst case the next RunOnce returns (0, false) — one cheap query.
	return len(pending), len(pending) == ix.batch, nil
}

// Backfill runs batches until the corpus is fully embedded, ctx is cancelled, or
// a batch errors. progress (optional) is called with the cumulative count after
// each non-empty batch so a CLI can show a running tally. On ctx cancellation it
// returns the partial total and ctx.Err(); already-embedded batches are durably
// committed, so a re-run resumes where this left off.
func (ix *Indexer) Backfill(ctx context.Context, progress func(total int)) (int, error) {
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		start := time.Now()
		n, more, err := ix.RunOnce(ctx)
		if err != nil {
			return total, err
		}
		total += n
		if n > 0 && progress != nil {
			progress(total)
		}
		if !more {
			return total, nil
		}
		if !ix.pause(ctx, time.Since(start)) {
			return total, ctx.Err()
		}
	}
}

// pause idles for however long the configured GPU share implies after a batch
// that took worked. Reports false if ctx ended while waiting, so the caller
// stops rather than starting another batch.
func (ix *Indexer) pause(ctx context.Context, worked time.Duration) bool {
	if ix.share <= 0 || ix.share >= 100 || worked <= 0 {
		return true
	}
	idle := time.Duration(float64(worked) * float64(100-ix.share) / float64(ix.share))
	if idle <= 0 {
		return true
	}
	t := time.NewTimer(idle)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// maxRequestRunes bounds the total text in one /v1/embeddings call so a batch
// of long posts can't exceed the server's logical batch size (-b/-ub tokens).
// Comfortably under that budget at any realistic token density; a single chunk
// (≤ maxChunkRunes) always fits, so even one oversized input is sent on its own.
const maxRequestRunes = 6000

// minSplitRunes is the floor for overflow recovery: an input shorter than this
// that *still* overflows the model's context is surfaced as an error rather than
// split toward zero. No realistic text tokenizes to >2048 tokens in this few
// runes, so reaching the floor signals a genuine misconfiguration (e.g. a
// server started with a tiny -c) worth reporting instead of looping.
const minSplitRunes = 64

// embedAll embeds inputs in size-bounded sub-requests and concatenates the
// results in order, so the returned slice still aligns one-to-one with inputs.
// Splitting by total runes keeps each call within the server's batch budget
// regardless of how many long messages land in one indexing batch.
func (ix *Indexer) embedAll(ctx context.Context, inputs []string) ([][]float32, error) {
	var out [][]float32
	for i := 0; i < len(inputs); {
		j, runes := i, 0
		for j < len(inputs) {
			r := len([]rune(inputs[j]))
			if j > i && runes+r > maxRequestRunes {
				break
			}
			runes += r
			j++
		}
		vecs, err := ix.embedSpan(ctx, inputs[i:j])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
		i = j
	}
	return out, nil
}

// embedSpan embeds one sub-request, recovering from the server's hard context
// limit so a single over-long message can't halt the whole backfill. A batch
// that overflows is retried one input at a time (isolating the culprit so the
// well-sized inputs aren't penalised); an individual input that still overflows
// is split and mean-pooled into one vector by embedOversized — preserving the
// one-vector-per-input contract embedAll's callers rely on. Non-overflow errors
// (server down, decode failure) are returned as-is so the caller can back off.
func (ix *Indexer) embedSpan(ctx context.Context, span []string) ([][]float32, error) {
	vecs, err := ix.client.Embed(ctx, span)
	if err == nil {
		return vecs, nil
	}
	if !embed.IsOverflow(err) {
		return nil, err
	}
	if len(span) > 1 {
		out := make([][]float32, 0, len(span))
		for _, in := range span {
			v, err := ix.embedSpan(ctx, []string{in})
			if err != nil {
				return nil, err
			}
			out = append(out, v...)
		}
		return out, nil
	}
	v, err := ix.embedOversized(ctx, span[0])
	if err != nil {
		return nil, err
	}
	return [][]float32{v}, nil
}

// embedOversized embeds an input the server rejected as too long by halving it
// on a rune boundary and mean-pooling the pieces into one vector (the store
// renormalizes on write, so summing is enough — the direction is the mean). It
// routes the halves back through embedSpan, so a piece that is itself still too
// long is split again. The document prefix rides on the left half only; for this
// rare fallback on a giant dense paste that imperfection is immaterial next to
// keeping the message searchable at all. Below minSplitRunes it gives up and
// surfaces the error rather than recursing toward an empty input.
func (ix *Indexer) embedOversized(ctx context.Context, input string) ([]float32, error) {
	r := []rune(input)
	if len(r) <= minSplitRunes {
		return nil, fmt.Errorf("embed: input still over the context limit at %d runes (server context too small?)", len(r))
	}
	mid := len(r) / 2
	var sum []float32
	for _, piece := range []string{strings.TrimSpace(string(r[:mid])), strings.TrimSpace(string(r[mid:]))} {
		if piece == "" {
			continue
		}
		vecs, err := ix.embedSpan(ctx, []string{piece})
		if err != nil {
			return nil, err
		}
		if sum == nil {
			sum = make([]float32, len(vecs[0]))
		}
		for j := range vecs[0] {
			sum[j] += vecs[0][j]
		}
	}
	if sum == nil {
		return nil, fmt.Errorf("embed: oversized input reduced to empty")
	}
	return sum, nil
}

// chunks splits a message into pieces of at most maxChunkRunes runes, trimmed,
// so each stays inside the model's context window. A short message yields one
// chunk; an over-long one is divided on rune boundaries (never mid-character).
// An empty/whitespace message yields no chunks — but callers only pass posts
// that PostsMissingVectors already filtered to non-empty text.
func chunks(msg string) []string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return nil
	}
	r := []rune(msg)
	if len(r) <= maxChunkRunes {
		return []string{msg}
	}
	var out []string
	for len(r) > 0 {
		n := maxChunkRunes
		if n > len(r) {
			n = len(r)
		}
		if c := strings.TrimSpace(string(r[:n])); c != "" {
			out = append(out, c)
		}
		r = r[n:]
	}
	return out
}
