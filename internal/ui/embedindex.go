package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/semindex"
)

// The background semantic indexer keeps post_vectors populated while the app
// runs. It is a single self-rescheduling chain of commands — at most one batch
// is ever in flight — so it can't race the store or pile up goroutines:
//
//	startEmbedder → embedBatchCmd → embedBatchMsg ─┬─ more  → tick(pause)  → embedBatchCmd …
//	                                               ├─ idle  → tick(idle)   → embedBatchCmd …  (catches new posts)
//	                                               └─ error → tick(backoff)→ embedBatchCmd …  (server likely down)
//
// Because PostsMissingVectors returns newest-first, the same loop both backfills
// history and picks up messages that just arrived over the WebSocket — no
// separate hook into post ingestion is needed.

const (
	// embedBatchPause is the gap between consecutive batches during an active
	// backfill — a politeness throttle so the embedder doesn't monopolise the
	// GPU it shares with the chat model.
	embedBatchPause = 750 * time.Millisecond
	// embedIdlePoll is how often the loop re-checks for work once the corpus is
	// fully embedded, so newly-arrived messages get embedded within ~a minute.
	embedIdlePoll = 45 * time.Second
	// embedBackoff is the wait after a failed batch (typically the embeddings
	// server being down). Long enough not to spam a dead endpoint; short enough
	// that indexing resumes soon after the user starts the server.
	embedBackoff = 60 * time.Second
)

// embedderState tracks the background indexer. enabled is fixed at New(); the
// rest evolve as batches run. seq guards against stale ticks if the loop is
// ever restarted (it isn't today, but the guard is cheap and future-proof).
type embedderState struct {
	enabled bool
	seq     int
	running bool // a batch cmd is in flight; blocks a tick from launching a second
	total   int  // messages embedded this session (status/debug only)
}

// embedBatchMsg reports one finished background batch.
type embedBatchMsg struct {
	seq  int
	n    int  // messages embedded in this batch
	more bool // a full batch came back, so more likely remain
	err  error
}

// embedTickMsg wakes the loop to attempt the next batch.
type embedTickMsg struct{ seq int }

// startEmbedder kicks the loop off once at startup. No-op (nil) when the indexer
// is disabled. Called from Init; it can't mutate running (Init runs on a copy),
// which is fine — no tick is outstanding before the first batch returns.
func (m Model) startEmbedder() tea.Cmd {
	if !m.embedder.enabled {
		return nil
	}
	return m.embedBatchCmd(m.embedder.seq)
}

// embedBatchCmd runs one batch off the UI goroutine and reports the outcome.
// It builds a fresh semindex.Indexer (cheap) from the snapshotted config.
func (m Model) embedBatchCmd(seq int) tea.Cmd {
	st := m.store
	client := m.embedClient
	model := m.embedModel
	dim := m.embedDim
	batch := m.embedBatch
	ctx := m.ctx
	return func() tea.Msg {
		ix := semindex.New(st, client, model, dim, batch)
		n, more, err := ix.RunOnce(ctx)
		return embedBatchMsg{seq: seq, n: n, more: more, err: err}
	}
}

// embedTick schedules the next wake-up after d.
func embedTick(d time.Duration, seq int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return embedTickMsg{seq: seq} })
}

// applyEmbedBatch records a finished batch and schedules the next wake-up: soon
// if more remain, lazily once caught up, after a backoff on error. Stale results
// (mismatched seq) are dropped. Runs silently — the background indexer never
// touches the status line.
func (m *Model) applyEmbedBatch(msg embedBatchMsg) tea.Cmd {
	if !m.embedder.enabled || msg.seq != m.embedder.seq {
		return nil
	}
	m.embedder.running = false
	if msg.err != nil {
		// Once per session: the indexer retries on a backoff, so an embeddings
		// server that is down would otherwise report every few seconds for
		// hours. One event says the same thing.
		if m.firstTime("embed_index/error") {
			m.recordFeature("embed_index", "auto", noLatency, 0, msg.err)
		}
		return embedTick(embedBackoff, m.embedder.seq)
	}
	// Also once: this is a background loop, and what is worth knowing is that
	// semantic indexing ran at all on a real cache — not how many batches it
	// took. The running total goes out with app_stopped's counters.
	if msg.n > 0 && m.firstTime("embed_index/ok") {
		m.recordFeature("embed_index", "auto", noLatency, msg.n, nil)
	}
	m.embedder.total += msg.n
	if msg.more {
		return embedTick(embedBatchPause, m.embedder.seq)
	}
	return embedTick(embedIdlePoll, m.embedder.seq)
}

// applyEmbedTick launches the next batch when the loop is idle, guarding against
// a second batch sneaking in alongside one already running.
func (m *Model) applyEmbedTick(msg embedTickMsg) tea.Cmd {
	if !m.embedder.enabled || msg.seq != m.embedder.seq || m.embedder.running {
		return nil
	}
	m.embedder.running = true
	return m.embedBatchCmd(m.embedder.seq)
}
