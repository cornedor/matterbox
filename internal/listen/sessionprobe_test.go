package listen

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestProbeSessionClassifies pins the three-way split the probe exists for.
// Confusing the 401 case with the unreachable case is the expensive mistake:
// one way the daemon never notices a dead session, the other way every network
// hiccup tells the user to log in again.
func TestProbeSessionClassifies(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		want    error
		wantLog bool
	}{
		{"healthy session", nil, nil, false},
		{"401 is expiry", errors.New("Invalid or expired session, please login again. (401)"), errSessionExpired, false},
		{"unauthorized wording is expiry", errors.New("received Unauthorized response"), errSessionExpired, false},
		{"server down is not expiry", errors.New("502 Bad Gateway"), nil, true},
		{"dns failure is not expiry", errors.New("dial tcp: lookup chat.example.com: no such host"), nil, true},
		{"timeout is not expiry", context.DeadlineExceeded, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var logbuf strings.Builder
			e := &Engine{
				log:   log.New(&logbuf, "", 0),
				probe: func(context.Context) error { return c.err },
			}
			got := e.probeSession(t.Context())
			if !errors.Is(got, c.want) {
				t.Errorf("probeSession() = %v, want %v", got, c.want)
			}
			if logged := logbuf.Len() > 0; logged != c.wantLog {
				t.Errorf("logged = %v, want %v (log: %q)", logged, c.wantLog, logbuf.String())
			}
		})
	}
}

// TestProbeSessionDuringShutdown guards against a shutdown being misreported as
// an expired session: cancelling ctx makes the in-flight call fail, and that
// failure must not raise the "log in again" alert.
func TestProbeSessionDuringShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	e := &Engine{
		log:   log.New(io.Discard, "", 0),
		probe: func(context.Context) error { return errors.New("401 unauthorized") },
	}
	if err := e.probeSession(ctx); err != nil {
		t.Errorf("probeSession() during shutdown = %v, want nil", err)
	}
}

// TestCheckSessionAppliesTimeout verifies the probe cannot wedge consume: a
// server that accepts the connection and then never answers must not block the
// event loop indefinitely.
func TestCheckSessionAppliesTimeout(t *testing.T) {
	e := &Engine{
		log: log.New(io.Discard, "", 0),
		probe: func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("probe ctx has no deadline; a hung request would block consume forever")
				return nil
			}
			if d := time.Until(deadline); d <= 0 || d > sessionProbeTimeout {
				t.Errorf("probe deadline in %s, want (0, %s]", d, sessionProbeTimeout)
			}
			return nil
		},
	}
	if err := e.checkSession(t.Context()); err != nil {
		t.Fatalf("checkSession: %v", err)
	}
}

// newProbeWS builds a WebSocketClient that is never dialled — consume only ever
// reads its channels, so the exported fields are enough to drive it.
func newProbeWS() *model.WebSocketClient {
	return &model.WebSocketClient{
		EventChannel:       make(chan *model.WebSocketEvent, 1),
		PingTimeoutChannel: make(chan bool, 1),
	}
}

// TestConsumeStopsOnExpiredSession is the regression test for the outage: a
// connected daemon whose session died. The socket stays open and the ping
// watchdog stays quiet, so only the probe can end this — consume must return
// errSessionExpired rather than sitting on a live-but-useless connection.
func TestConsumeStopsOnExpiredSession(t *testing.T) {
	e := &Engine{
		log:        log.New(io.Discard, "", 0),
		probeEvery: time.Millisecond,
		probe:      func(context.Context) error { return errors.New("401 unauthorized") },
	}

	done := make(chan error, 1)
	go func() { done <- e.consume(t.Context(), newProbeWS()) }()

	select {
	case err := <-done:
		if !errors.Is(err, errSessionExpired) {
			t.Errorf("consume() = %v, want errSessionExpired", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consume did not return; a dead session went undetected")
	}
}

// TestConsumeKeepsRunningWhenServerUnreachable is the other half: a failing
// probe that is not a 401 must leave the connection alone, since the daemon
// would otherwise tear down a perfectly good socket every time the API blips.
func TestConsumeKeepsRunningWhenServerUnreachable(t *testing.T) {
	var calls int
	var mu sync.Mutex
	e := &Engine{
		log:        log.New(io.Discard, "", 0),
		probeEvery: time.Millisecond,
		probe: func(context.Context) error {
			mu.Lock()
			calls++
			mu.Unlock()
			return errors.New("503 Service Unavailable")
		},
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.consume(ctx, newProbeWS()) }()

	// Let several probes fail, then confirm consume is still draining.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := calls
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("consume returned early (%v) on an unreachable server", err)
		case <-deadline:
			t.Fatalf("probe ran %d times in 5s, want >= 3", n)
		case <-time.After(time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("consume() after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consume did not honour ctx cancellation")
	}
}

// TestConsumeStillHandlesEvents checks the probe ticker did not disturb the
// event path it shares a select with.
func TestConsumeStillHandlesEvents(t *testing.T) {
	e := newTestEngine(t, Options{})
	e.probeEvery = time.Hour // never fires during the test
	e.probe = func(context.Context) error { return nil }

	wsc := newProbeWS()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.consume(ctx, wsc) }()

	// A close of EventChannel is the ordinary disconnect signal.
	close(wsc.EventChannel)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("consume() on disconnect = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consume did not return when EventChannel closed")
	}
	cancel()
}

// TestConsumePingTimeoutIsNotExpiry keeps the two failure modes distinct: a
// half-open socket should reconnect, not stop the daemon.
func TestConsumePingTimeoutIsNotExpiry(t *testing.T) {
	e := &Engine{
		log:        log.New(io.Discard, "", 0),
		probeEvery: time.Hour,
		probe:      func(context.Context) error { return nil },
	}
	wsc := newProbeWS()
	wsc.PingTimeoutChannel <- true

	done := make(chan error, 1)
	go func() { done <- e.consume(t.Context(), wsc) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("consume() on ping timeout = %v, want nil (reconnect, not stop)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consume did not return on ping timeout")
	}
}
