package telemetry

import (
	"strings"
	"testing"

	"matterbox/internal/config"
)

// TestPendingIsDiscardedWithoutConsent is the privacy invariant of the buffer:
// the setup wizard records its funnel before the telemetry question is answered,
// and a "no" must mean that funnel is never sent. Anything else would be
// collecting from someone who declined.
func TestPendingIsDiscardedWithoutConsent(t *testing.T) {
	t.Cleanup(Close)
	in, url := newIngest(t)
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)

	BeginPending()
	SetupStep("server", 1)
	SetupStep("login", 1)
	LoginFailed("password", "auth", false)

	// A config that answered no. ReleasePending must open nothing and drop what
	// it held.
	t.Setenv(config.DirEnv, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	no := false
	cfg.Telemetry.Enabled = &no
	ReleasePending(cfg, ModeCLI)

	if Enabled() {
		t.Error("a declined config opened a client")
	}
	// Opting in afterwards must not resurrect the buffer: the events were
	// recorded while consent was absent, and the answer that arrived was no.
	Close()
	Start(consentingConfig(t))
	Close()

	if body := in.all(); strings.Contains(body, "setup_step") || strings.Contains(body, "login_failed") {
		t.Errorf("a discarded pre-consent event reached the wire: %s", body)
	}
}

// TestPendingReplaysOnConsent is the other half: a wizard that is answered yes
// must deliver the funnel it collected, or the activation funnel would only ever
// contain the one event that happened after the answer.
func TestPendingReplaysOnConsent(t *testing.T) {
	t.Cleanup(Close)
	in, url := newIngest(t)
	cfg := consentingConfig(t)
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)

	BeginPending()
	SetupStep("server", 1)
	SetupStep("login", 2)
	ReleasePending(cfg, ModeCLI)
	Close()

	body := in.all()
	if !strings.Contains(body, "setup_step") {
		t.Errorf("a held event was lost on consent: %s", body)
	}
	if n := strings.Count(body, `"step":"login"`); n != 1 {
		t.Errorf("login step appeared %d times, want 1: %s", n, body)
	}
	// Replay must not leave the buffer holding, or a second Release would
	// double-send everything.
	if held := len(pending.events); held != 0 {
		t.Errorf("%d events still held after replay", held)
	}
	if pending.holding {
		t.Error("still holding after replay")
	}
}

// TestPendingIsBounded: the buffer sits in a process that may never be allowed
// to send anything, so a runaway caller must not grow it without limit.
func TestPendingIsBounded(t *testing.T) {
	t.Cleanup(DropPending)
	DropPending()
	BeginPending()
	for i := 0; i < pendingCap*3; i++ {
		SetupStep("server", i+1)
	}
	if got := len(pending.events); got != pendingCap {
		t.Errorf("held %d events, want the cap of %d", got, pendingCap)
	}
	if pending.dropped == 0 {
		t.Error("the cap turned events away without counting them")
	}
}

// TestPendingValidatesOnReplay: a held event gets no privileges from having
// waited — the catalogue checks it at replay exactly as it would have live.
func TestPendingValidatesOnReplay(t *testing.T) {
	t.Cleanup(Close)
	in, url := newIngest(t)
	cfg := consentingConfig(t)
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)

	BeginPending()
	// An out-of-set enum value and an undeclared property, held and then
	// replayed. Both must be dropped, and the event must still go.
	Capture("setup_step", map[string]any{"step": "not-a-step", "typed": "hello"})
	ReleasePending(cfg, ModeCLI)
	Close()

	body := in.all()
	if strings.Contains(body, "not-a-step") || strings.Contains(body, "hello") {
		t.Errorf("replay skipped catalogue validation: %s", body)
	}
	if !strings.Contains(body, "setup_step") {
		t.Errorf("the event itself was lost: %s", body)
	}
}
