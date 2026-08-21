// Package telemetry reports anonymous usage data and error reports to PostHog
// for the users who asked for it — and does nothing at all for everyone else.
// Consent is a single config key (`telemetry.enabled`), and every function here
// is a no-op without it, so callers never have to check first: an opted-out
// user runs the same code paths with no client, no goroutines, no counters and
// no network.
//
// What may be sent is not up to the call sites. Every event and every property
// is declared in catalogue_events.go, and Capture drops anything undeclared or
// out-of-range before it is queued — so the published list in
// `docs/telemetry.md`, which is generated from those same declarations, is the
// whole truth rather than a description of it. See catalogue.go for why that
// inversion is the point.
//
// Two invariants hold over everything in this package:
//
//   - It stays anonymous. Events are attributed to a random id minted on
//     opt-in (config.TelemetryConfig.AnonymousID) that is unrelated to the
//     Mattermost account, the server, the hostname or the machine. Message
//     content, channel and team names, usernames, server URLs and file paths
//     have no representation in the catalogue: numbers about user content are
//     bucketed (buckets.go) and error text is scrubbed (scrub.go).
//   - It is cheap when off and cheap when on. The disabled path is a single
//     atomic load. The enabled path aggregates high-frequency signals in
//     memory (counters.go) rather than sending an event per keystroke.
package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/posthog/posthog-go"

	"matterbox/internal/config"
)

// projectKey is the PostHog project API key. It is compiled in rather than
// passed at build time because building from source is the normal way to get
// matterbox — the optional media features link against libav, whose licence
// makes distributing binaries awkward — so a key supplied only by the release
// build would be a key almost nobody has. Telemetry that only works for people
// who didn't build it themselves would answer questions about a population we
// don't have.
//
// Shipping it in the client is how PostHog is designed to be used: it is
// write-only, so it can queue events and read nothing back. It is not a secret
// and does not need protecting.
//
// It is therefore *not* the consent gate. `telemetry.enabled` is, and it is the
// only one: with this key present and consent absent, nothing is sent. Override
// the destination for local work with MATTERBOX_POSTHOG_KEY, or at build time:
//
//	make POSTHOG_KEY=phc_your_own_project
var projectKey = "phc_CfbVPKfDJpxS3PuiPtuTGYJCxrAK2op6anveocxwaeNR"

// endpoint is the PostHog ingest host. EU cloud, since that is where the
// project lives; PostHog's own default is the US one.
const endpoint = "https://eu.i.posthog.com"

// KeyEnv and HostEnv override the compiled-in project key and the ingest host,
// for pointing a local build at your own PostHog project while working on
// telemetry. They only override where events go — consent is still required.
const (
	KeyEnv  = "MATTERBOX_POSTHOG_KEY"
	HostEnv = "MATTERBOX_POSTHOG_HOST"
)

// Flush budgets bound how long Close may spend delivering what is queued.
// Different per mode because the cost of waiting is different: quitting the TUI
// can afford a moment, but `matterbox send` is called from scripts and
// notification handlers where an extra second per invocation is a real
// regression in something the user did not ask for.
const (
	flushBudgetTUI    = 3 * time.Second
	flushBudgetCLI    = 700 * time.Millisecond
	flushBudgetDaemon = 5 * time.Second
)

// Mode is how this process is being used, which selects the flush budget.
type Mode int

const (
	ModeTUI Mode = iota
	ModeCLI
	ModeDaemon
)

func (m Mode) flushBudget() time.Duration {
	switch m {
	case ModeCLI:
		return flushBudgetCLI
	case ModeDaemon:
		return flushBudgetDaemon
	}
	return flushBudgetTUI
}

// active is the fast path. Every entry point in the package reads it first,
// and an opted-out process pays exactly one atomic load per call — which is
// what makes it safe to put a counter bump on the keystroke path. It is
// separate from `client != nil` because reading that needs the mutex.
var active atomic.Bool

// strict makes a dropped property or an uncatalogued event a panic instead of
// a silent no-op. Tests turn it on: a mistake in an event should fail CI
// loudly, and fail silently in a user's terminal. Never enabled in a release.
var strict atomic.Bool

// Package-level state, guarded by mu. A single client per process, opened by
// Start and closed by Close; nil means telemetry is off, which is both the
// default and the opted-out state.
var (
	mu         sync.Mutex
	client     posthog.Client
	distinctID string
	snapStop   chan struct{}
)

// Start opens the PostHog client, but only when the user has opted in and a
// project key is available; otherwise it returns having done nothing and every
// later call stays a no-op. Idempotent and safe from any goroutine, so a caller
// that isn't sure whether telemetry is already running can just call it.
//
// Errors are swallowed by design. Telemetry failing is not the user's problem
// and matterbox has no channel to report it on: writing to stderr would punch
// a hole through the TUI's alt-screen.
func Start(cfg *config.Config) { StartMode(cfg, ModeTUI) }

// StartMode is Start with an explicit mode, which decides how long Close may
// spend flushing. Use ModeCLI for one-shot subcommands and ModeDaemon for
// `matterbox listen`.
func StartMode(cfg *config.Config, mode Mode) {
	if cfg == nil || !cfg.TelemetryEnabled() {
		// No consent: anything a pre-consent surface held is discarded here
		// rather than left in memory for a client that will never open.
		DropPending()
		return
	}
	// Empty only when a build deliberately blanked it (a fork pointing
	// somewhere else, or a test): there is nowhere to send, so do nothing.
	key := resolvedKey()
	if key == "" {
		DropPending()
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if client != nil {
		return
	}
	c, err := posthog.NewWithConfig(key, posthog.Config{
		Endpoint: envOr(HostEnv, endpoint),
		// PostHog's default logger writes to stderr, which would scribble over
		// the TUI mid-frame. Everything it has to say is about telemetry
		// delivery, which the user did not ask to hear about.
		Logger:          silentLogger{},
		ShutdownTimeout: mode.flushBudget(),
	})
	if err != nil {
		return
	}
	client = c
	distinctID = anonymousID(cfg)

	now := time.Now()
	tally.mu.Lock()
	tally.sessionStart = now
	tally.windowStart = now
	tally.mu.Unlock()

	// Only now is the package live: the counters must have a window start
	// before anything can bump them, or the first snapshot would report a
	// nonsense span.
	active.Store(true)
	if mode != ModeCLI {
		// A one-shot subcommand exits long before the first tick, and Close
		// flushes regardless — so it gets no goroutine.
		snapStop = make(chan struct{})
		go startSnapshots(snapStop)
	}
}

// resolvedKey is the project key this process would report to: the run-time
// override if one is set, otherwise whatever was compiled in. Empty means there
// is nowhere to send, whatever the config says.
func resolvedKey() string { return envOr(KeyEnv, projectKey) }

// HasProjectKey reports whether this build could report anywhere at all, which
// is a different question from whether it is allowed to. False only for a binary
// whose key was deliberately blanked (`make POSTHOG_KEY=`) or overridden to
// empty — and since the key is compiled in, asking the binary is the only way to
// confirm that worked, which is why `matterbox --version` says.
func HasProjectKey() bool { return resolvedKey() != "" }

// Enabled reports whether telemetry is actually running: consent given, a
// project key present, and the client open. Use it to skip work that only
// exists to feed an event, not as a permission check before Capture — Capture
// does that itself.
func Enabled() bool { return active.Load() }

// SetStrict turns catalogue violations into panics, for tests. Returns the
// previous setting so a test can restore it.
func SetStrict(on bool) bool {
	prev := strict.Load()
	strict.Store(on)
	return prev
}

// Capture records one named event, after checking it against the catalogue:
// an event name that isn't declared is dropped, and so is any property the
// declaration doesn't allow or whose value falls outside it (see
// EventSpec.sanitize). A nil or empty props is fine.
//
// It never blocks on the network — PostHog batches in the background — and
// never reports an error, because a dropped event is not worth a code path in
// the UI. Prefer the typed helpers in emit.go over calling this directly;
// they exist so a call site cannot misspell a property name.
func Capture(event string, props map[string]any) {
	capture(event, props, nil)
}

// capture is the shared implementation. personProps names properties that
// should also be mirrored onto the PostHog person via $set — used by
// app_started for the environment facts that describe an install rather than a
// moment, so "how many people are on kitty" is a person-level breakdown rather
// than a count of launches. They are drawn from the event's own already
// validated properties, so they need no separate whitelist.
func capture(event string, props map[string]any, personProps []string) {
	if !active.Load() {
		// Even with telemetry off, a mistyped event should fail a test that
		// asked for strictness.
		if strict.Load() {
			checkStrict(event, props)
		}
		// A surface that runs before consent can be checked holds its events
		// instead of losing them; they are replayed only if a client later
		// opens, which requires consent. See pending.go.
		holdEvent(event, props)
		return
	}
	spec, ok := Spec(event)
	if !ok {
		if strict.Load() {
			panic("telemetry: event not in catalogue: " + event)
		}
		return
	}
	clean, dropped := spec.sanitize(props)
	if len(dropped) > 0 && strict.Load() {
		panic("telemetry: event " + event + " has undeclared or invalid properties: " +
			strings.Join(dropped, ", "))
	}
	if len(personProps) > 0 {
		if set := pick(clean, personProps); len(set) > 0 {
			if clean == nil {
				clean = make(map[string]any, 1)
			}
			clean["$set"] = set
		}
	}

	mu.Lock()
	c, id := client, distinctID
	mu.Unlock()
	if c == nil {
		return
	}
	_ = c.Enqueue(posthog.Capture{
		DistinctId: id,
		Event:      event,
		Properties: posthog.Properties(clean),
	})
}

// checkStrict validates without sending, so a test can assert an event is
// well-formed without opting the test process in to a network client.
func checkStrict(event string, props map[string]any) {
	spec, ok := Spec(event)
	if !ok {
		panic("telemetry: event not in catalogue: " + event)
	}
	if _, dropped := spec.sanitize(props); len(dropped) > 0 {
		panic("telemetry: event " + event + " has undeclared or invalid properties: " +
			strings.Join(dropped, ", "))
	}
}

// pick copies the named keys out of props.
func pick(props map[string]any, names []string) map[string]any {
	out := make(map[string]any, len(names))
	for _, n := range names {
		if v, ok := props[n]; ok {
			out[n] = v
		}
	}
	return out
}

// Close flushes whatever is queued and shuts the client down, bounded by the
// mode's flush budget. The pending usage_snapshot goes out first, so the tail of a
// session isn't lost. Safe to call when telemetry never started, and safe to
// call twice; a later Start would open a fresh client.
func Close() {
	if active.Load() {
		Flush(true)
	}
	active.Store(false)

	mu.Lock()
	c := client
	stop := snapStop
	client, distinctID, snapStop = nil, "", nil
	mu.Unlock()

	if stop != nil {
		close(stop)
	}
	if c != nil {
		_ = c.Close()
	}
	// Nothing can be replayed once the client is gone, so a buffer that
	// outlived it is garbage rather than data.
	DropPending()
	resetExceptionBudget()
}

// anonymousID returns the id events are attributed to, minting and persisting
// one when the config carries none — which happens when telemetry was enabled
// by hand instead of through the wizard, or when the user deleted the id to
// start over. A failed write only costs the id's stability across restarts, so
// it is ignored: the in-memory value still works for this run.
func anonymousID(cfg *config.Config) string {
	if id := strings.TrimSpace(cfg.Telemetry.AnonymousID); id != "" {
		return id
	}
	id := NewAnonymousID()
	cfg.Telemetry.AnonymousID = id
	_ = config.SaveTelemetry(cfg.Telemetry)
	return id
}

// NewAnonymousID mints a fresh random identifier for a user who has just opted
// in. 16 random bytes, hex: no account, server, hostname or timestamp goes into
// it, so it links a user's own events together and says nothing else. Exported
// because the setup wizard mints one the moment consent is given, before any
// client exists.
func NewAnonymousID() string {
	var b [16]byte
	// crypto/rand.Read never fails on the platforms Go supports (it panics
	// internally on a broken entropy source), so there is nothing to handle.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// envOr returns the environment variable name's value, or def when it is unset
// or blank.
func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// silentLogger swallows the PostHog client's logging. It satisfies
// posthog.Logger; see the Start comment for why nothing may reach stderr.
type silentLogger struct{}

func (silentLogger) Debugf(string, ...any) {}
func (silentLogger) Logf(string, ...any)   {}
func (silentLogger) Warnf(string, ...any)  {}
func (silentLogger) Errorf(string, ...any) {}

// compile-time check that we still satisfy the SDK's logger interface.
var _ posthog.Logger = silentLogger{}
