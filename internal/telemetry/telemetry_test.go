package telemetry

import (
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"matterbox/internal/config"
)

// consentingConfig returns a config in an isolated directory that has opted in,
// so a test can exercise the running-client paths. The temp dir also means the
// id-minting write in Start can't touch the real config.yaml.
func consentingConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv(config.DirEnv, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	cfg.Telemetry.Enabled = &yes
	return cfg
}

// ingest stands in for PostHog: it records every batch body it is posted so a
// test can assert what actually went over the wire.
type ingest struct {
	mu     sync.Mutex
	bodies []string
}

func newIngest(t *testing.T) (*ingest, string) {
	t.Helper()
	in := &ingest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		in.mu.Lock()
		in.bodies = append(in.bodies, string(b))
		in.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return in, srv.URL
}

func (i *ingest) all() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return strings.Join(i.bodies, "\n")
}

// TestStartWithoutConsentIsNoop is the whole point of the package: a project
// key is available, but the user never opted in, so nothing starts.
func TestStartWithoutConsentIsNoop(t *testing.T) {
	t.Cleanup(Close)
	t.Setenv(config.DirEnv, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(KeyEnv, "phc_test")

	Start(cfg)
	if Enabled() {
		t.Fatal("telemetry started for a config that never opted in")
	}
	// The no-client paths must be safe to call unconditionally.
	Capture("something", map[string]any{"n": 1})
	Error("somewhere", errors.New("boom"))
}

// TestStartWithoutConsentDecline covers the explicit "no" as well as the absent
// key, since only true may start anything.
func TestStartWithoutConsentDecline(t *testing.T) {
	t.Cleanup(Close)
	cfg := consentingConfig(t)
	no := false
	cfg.Telemetry.Enabled = &no
	t.Setenv(KeyEnv, "phc_test")

	Start(cfg)
	if Enabled() {
		t.Fatal("telemetry started for a user who declined")
	}
}

// TestStartWithoutProjectKeyIsNoop: a build with the key blanked
// (`make POSTHOG_KEY=`) has nowhere to send, so consent alone doesn't start a
// client. The key is compiled in by default, so the test blanks it — this is
// the documented escape hatch for someone who wants a binary that cannot
// report at all, and it has to keep working.
func TestStartWithoutProjectKeyIsNoop(t *testing.T) {
	t.Cleanup(Close)
	cfg := consentingConfig(t)
	t.Setenv(KeyEnv, "")

	restore := projectKey
	projectKey = ""
	t.Cleanup(func() { projectKey = restore })

	Start(cfg)
	if Enabled() {
		t.Fatal("telemetry started without a project key")
	}
}

// TestConsentIsTheOnlyGate is the counterpart, and the promise the docs make:
// the compiled-in key does nothing by itself. A config that never opted in must
// start nothing even though the binary knows perfectly well where to send.
func TestConsentIsTheOnlyGate(t *testing.T) {
	t.Cleanup(Close)
	t.Setenv(config.DirEnv, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if projectKey == "" {
		t.Fatal("no project key is compiled in — release builds would report nothing")
	}

	Start(cfg) // consent absent
	if Enabled() {
		t.Fatal("the compiled-in key started telemetry without consent")
	}
	no := false
	cfg.Telemetry.Enabled = &no
	Start(cfg) // consent declined
	if Enabled() {
		t.Fatal("the compiled-in key started telemetry for a user who declined")
	}
}

// TestStartNilConfig guards the defensive branch: Start must not panic when
// called before a config was loaded.
func TestStartNilConfig(t *testing.T) {
	t.Cleanup(Close)
	t.Setenv(KeyEnv, "phc_test")
	Start(nil)
	if Enabled() {
		t.Fatal("telemetry started from a nil config")
	}
}

// TestCaptureAndErrorReachIngest is the end-to-end wiring check: with consent
// and a key, an event and an error report actually leave the process, carrying
// the anonymous id and nothing else identifying.
func TestCaptureAndErrorReachIngest(t *testing.T) {
	t.Cleanup(Close)
	in, url := newIngest(t)
	cfg := consentingConfig(t)
	cfg.Telemetry.AnonymousID = "0123456789abcdef0123456789abcdef"
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)

	Start(cfg)
	if !Enabled() {
		t.Fatal("telemetry did not start with consent + a project key")
	}
	// A catalogued event: Capture validates against the catalogue now, so an
	// invented name would be dropped rather than sent (see TestCaptureDropsUncataloguedEvent).
	Capture("version_upgraded", map[string]any{"from": "0.9.0", "to": "0.9.1"})
	// A real failure site: the label is validated against FailureSites, so an
	// invented one would be replaced rather than reported (see TestExceptionSiteIsValidated).
	Error("store.migrate", errors.New("boom"))
	Close() // flushes

	body := in.all()
	for _, want := range []string{"version_upgraded", "0.9.1", "phc_test", cfg.Telemetry.AnonymousID, "$exception", "store.migrate", "boom"} {
		if !strings.Contains(body, want) {
			t.Errorf("ingested batches missing %q\ngot: %s", want, body)
		}
	}
	if Enabled() {
		t.Error("Close left the client open")
	}
}

// TestStartIsIdempotent: a second Start while a client is open must not build a
// second one (which would double-report and leak goroutines).
func TestStartIsIdempotent(t *testing.T) {
	t.Cleanup(Close)
	_, url := newIngest(t)
	cfg := consentingConfig(t)
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)

	Start(cfg)
	first := currentClient()
	Start(cfg)
	if currentClient() != first {
		t.Fatal("a second Start replaced the open client")
	}
}

func currentClient() any {
	mu.Lock()
	defer mu.Unlock()
	return client
}

// TestStartMintsAndPersistsAnonymousID covers the hand-edited config: consent
// without an id gets one, and it is written back so reports stay grouped across
// restarts.
func TestStartMintsAndPersistsAnonymousID(t *testing.T) {
	t.Cleanup(Close)
	_, url := newIngest(t)
	cfg := consentingConfig(t)
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)

	Start(cfg)
	if cfg.Telemetry.AnonymousID == "" {
		t.Fatal("Start did not mint an anonymous id")
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Telemetry.AnonymousID != cfg.Telemetry.AnonymousID {
		t.Errorf("persisted anonymous id = %q, want %q", reloaded.Telemetry.AnonymousID, cfg.Telemetry.AnonymousID)
	}
	if !reloaded.TelemetryEnabled() {
		t.Error("persisting the id dropped the consent")
	}
}

// TestNewAnonymousIDIsRandomHex pins the shape of the identifier: 16 random
// bytes, hex, and not the same twice.
func TestNewAnonymousIDIsRandomHex(t *testing.T) {
	a, b := NewAnonymousID(), NewAnonymousID()
	if a == b {
		t.Fatal("two anonymous ids came out identical")
	}
	for _, id := range []string{a, b} {
		raw, err := hex.DecodeString(id)
		if err != nil {
			t.Fatalf("anonymous id %q is not hex: %v", id, err)
		}
		if len(raw) != 16 {
			t.Errorf("anonymous id %q decodes to %d bytes, want 16", id, len(raw))
		}
	}
}

// TestCallsBeforeStartAreSafe: every entry point is a no-op on a cold package,
// so callers never have to guard.
func TestCallsBeforeStartAreSafe(t *testing.T) {
	Close() // ensure cold
	Capture("event", nil)
	Capture("", nil)
	Error("where", errors.New("boom"))
	Error("", nil)
	Close()
	if Enabled() {
		t.Fatal("Enabled reported true with no client")
	}
}

// TestEnvOverridesFallBackToDefaults: a blank override must not blank out the
// compiled-in value.
func TestEnvOverridesFallBackToDefaults(t *testing.T) {
	t.Setenv(HostEnv, "  ")
	if got := envOr(HostEnv, endpoint); got != endpoint {
		t.Errorf("blank %s override gave %q, want the default %q", HostEnv, got, endpoint)
	}
	t.Setenv(HostEnv, "https://example.invalid")
	if got := envOr(HostEnv, endpoint); got != "https://example.invalid" {
		t.Errorf("%s override not honoured: %q", HostEnv, got)
	}
}
