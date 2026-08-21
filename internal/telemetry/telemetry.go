// Package telemetry reports anonymous usage data and error reports to PostHog
// for the users who asked for it — and does nothing at all for everyone else.
// Consent is a single config key (`telemetry.enabled`, off unless the first-run
// wizard was answered "yes"), and every function here is a no-op without it, so
// callers never have to check first: an opted-out user runs the same code paths
// with no client, no goroutines and no network.
//
// Nothing in matterbox captures an event yet — this is the transport, and what
// travels over it gets added deliberately, one call at a time. Two properties
// have to hold for whatever is added:
//
//   - It stays anonymous. Events are attributed to a random id minted on
//     opt-in (config.TelemetryConfig.AnonymousID) that is unrelated to the
//     Mattermost account, the server, the hostname or the machine. Never put
//     message content, channel or team names, usernames, server URLs or file
//     paths in an event — Error in particular hands PostHog an error string,
//     so its callers must pass errors that don't quote user data.
//   - It is documented on https://matterbox.work/docs/telemetry, which the
//     wizard links to when it asks. The page is the contract; an event that
//     isn't on it shouldn't ship.
package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/posthog/posthog-go"

	"matterbox/internal/config"
)

// projectKey is the PostHog project API key, baked into release builds:
//
//	go build -ldflags "-X matterbox/internal/telemetry.projectKey=phc_..."
//
// It is a write-only ingest key — shipping it inside the client is how PostHog
// is designed to be used — but a plain `make` build leaves it empty, which
// keeps telemetry off for anyone building from source no matter what their
// config says, since there is nowhere to send to.
var projectKey = ""

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

// shutdownTimeout bounds the flush in Close: matterbox exits when the user
// quits, and a slow network is not a reason to hold the terminal hostage.
const shutdownTimeout = 3 * time.Second

// Package-level state, guarded by mu. A single client per process, opened by
// Start and closed by Close; nil means telemetry is off, which is both the
// default and the opted-out state.
var (
	mu         sync.Mutex
	client     posthog.Client
	distinctID string
)

// Start opens the PostHog client, but only when the user has opted in and a
// project key is available; otherwise it returns having done nothing and every
// later call stays a no-op. Idempotent and safe from any goroutine, so a caller
// that isn't sure whether telemetry is already running can just call it.
//
// Errors are swallowed by design. Telemetry failing is not the user's problem
// and matterbox has no channel to report it on: writing to stderr would punch
// a hole through the TUI's alt-screen.
func Start(cfg *config.Config) {
	if cfg == nil || !cfg.TelemetryEnabled() {
		return
	}
	key := envOr(KeyEnv, projectKey)
	if key == "" {
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
		ShutdownTimeout: shutdownTimeout,
	})
	if err != nil {
		return
	}
	client = c
	distinctID = anonymousID(cfg)
}

// Enabled reports whether telemetry is actually running: consent given, a
// project key present, and the client open. Use it to skip work that only
// exists to feed an event, not as a permission check before Capture — Capture
// does that itself.
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return client != nil
}

// Capture records one named event. A nil or empty props is fine. No-op when
// telemetry is off; it never blocks on the network (PostHog batches in the
// background) and never reports an error, because a dropped event is not worth
// a code path in the UI.
//
// Keep props anonymous — counts, durations, enum-ish labels, feature names. See
// the package comment for what must not go in.
func Capture(event string, props map[string]any) {
	mu.Lock()
	c, id := client, distinctID
	mu.Unlock()
	if c == nil || event == "" {
		return
	}
	_ = c.Enqueue(posthog.Capture{
		DistinctId: id,
		Event:      event,
		Properties: posthog.Properties(props),
	})
}

// Error reports err as an anonymous PostHog exception, grouped by where: a
// short, stable, hand-written label for the operation that failed
// ("ws.connect", "store.migrate") rather than anything derived from user data.
//
// The error's text is sent verbatim, so only call this with errors that quote
// no message content, channel names, server URLs or file paths — wrap the
// message first if in doubt. No-op when telemetry is off or err is nil.
func Error(where string, err error) {
	mu.Lock()
	c, id := client, distinctID
	mu.Unlock()
	if c == nil || err == nil {
		return
	}
	if where == "" {
		where = "error"
	}
	_ = c.Enqueue(posthog.NewDefaultException(time.Now().UTC(), id, where, err.Error()))
}

// Close flushes whatever is queued and shuts the client down, bounded by
// shutdownTimeout. Safe to call when telemetry never started, and safe to call
// twice; a later Start would open a fresh client.
func Close() {
	mu.Lock()
	c := client
	client, distinctID = nil, ""
	mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
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
