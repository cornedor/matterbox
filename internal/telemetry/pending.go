package telemetry

import (
	"sync"

	"matterbox/internal/config"
)

// Events that happen before consent is known.
//
// The setup wizard asks the telemetry question at its last step, so every step
// before it runs with the answer still unknown — and sending anything then
// would be collecting data from someone who has not been asked yet, which is
// the one thing this package exists not to do. The same problem, for a
// different reason, applies to the subcommands: reportCommand starts the client
// once the verb has finished (so `matterbox decode` doesn't pay for a config
// read it has no use for), which means an event emitted *during* the work would
// find nothing running and be dropped.
//
// Both are solved by holding events in memory instead of dropping them, and
// replaying them if — and only if — a client later opens, which requires
// consent. A wizard abandoned before the question, or a run by someone who said
// no, exits with the buffer unsent: it never reaches the network, and nothing
// on disk records that it existed.
//
// Holding is opt-in per process (BeginPending) rather than automatic, because
// the hot paths must stay a single atomic load. The high-frequency signals go
// through the counters in counters.go, which check `active` and return; only
// Capture consults the buffer, and only while a surface has asked it to.

// pendingCap bounds the buffer. The surfaces that use it emit a handful of
// events — five wizard steps, a login failure, one subcommand's worth — so the
// cap is a runaway guard, not a budget. Past it, events are dropped rather than
// growing memory in a process that may never be allowed to send anything.
const pendingCap = 24

// pendingEvent is one held event, kept as the raw name and properties so it is
// validated against the catalogue at replay time, exactly as a live one is.
type pendingEvent struct {
	event string
	props map[string]any
}

var pending struct {
	mu      sync.Mutex
	holding bool
	events  []pendingEvent
	// dropped counts what the cap turned away, so a full buffer is visible in
	// the data rather than being a silent gap.
	dropped int
}

// BeginPending starts holding events that would otherwise be dropped for want
// of a client. Call it on a surface that emits before consent can be checked —
// the setup wizard and the one-shot subcommands — and pair it with exactly one
// of ReleasePending or DropPending.
func BeginPending() {
	pending.mu.Lock()
	pending.holding = true
	pending.mu.Unlock()
}

// ReleasePending opens the client for cfg and replays whatever was held, in the
// order it happened. It is a no-op without consent — StartMode checks that —
// and in that case the buffer is discarded rather than kept, so a declined
// wizard leaves nothing behind.
//
// This is the only path by which a pre-consent event ever reaches PostHog, and
// it runs after the answer is recorded in the config, so "was this allowed?" is
// answered by the same check every other event goes through.
func ReleasePending(cfg *config.Config, mode Mode) {
	StartMode(cfg, mode)
	if !active.Load() {
		DropPending()
		return
	}
	replayPending()
}

// DropPending discards the buffer and stops holding. Called when the answer is
// no, and by Close, so nothing accumulates for a process that will never send.
func DropPending() {
	pending.mu.Lock()
	pending.holding = false
	pending.events = nil
	pending.dropped = 0
	pending.mu.Unlock()
}

// holdEvent buffers one event, reporting whether it was taken. It returns false
// when nothing is holding, which is the signal for capture to fall through to
// its normal "drop it" path.
func holdEvent(event string, props map[string]any) bool {
	pending.mu.Lock()
	defer pending.mu.Unlock()
	if !pending.holding {
		return false
	}
	if len(pending.events) >= pendingCap {
		pending.dropped++
		return true
	}
	pending.events = append(pending.events, pendingEvent{event: event, props: props})
	return true
}

// replayPending sends the held events and stops holding. Each one goes through
// Capture, so the catalogue validates it now exactly as it would have then — a
// held event gets no privileges from having waited.
func replayPending() {
	pending.mu.Lock()
	held := pending.events
	pending.events = nil
	pending.holding = false
	pending.mu.Unlock()

	for _, e := range held {
		Capture(e.event, e.props)
	}
}
