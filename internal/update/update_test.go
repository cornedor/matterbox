package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"matterbox/internal/config"
)

// serve stands in for the endpoint, counting requests so a test can prove the
// daily interval is doing its job. The returned handler answers with body and
// status; a body of "" means "valid latest.json naming v9.9.9".
func serve(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	if body == "" {
		body = `{"version":"v9.9.9","url":"https://example.invalid/v9.9.9"}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// The request must say nothing about who is asking — that promise is
		// the reason this check is not behind the telemetry consent, so it is
		// worth a test rather than a comment.
		if ua := r.Header.Get("User-Agent"); ua != "matterbox" {
			t.Errorf("User-Agent = %q, want a bare %q", ua, "matterbox")
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query string %q, want none", r.URL.RawQuery)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// isolate points both the endpoint and the state file at this test.
func isolate(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)
	old := Endpoint
	Endpoint = srv.URL
	t.Cleanup(func() { Endpoint = old })
	return dir
}

func TestCheckReportsANewerRelease(t *testing.T) {
	srv, hits := serve(t, http.StatusOK, "")
	isolate(t, srv)

	rel, err := Check(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel == nil {
		t.Fatal("Check returned nothing, want v9.9.9")
	}
	if rel.Version != "v9.9.9" {
		t.Errorf("version = %q, want v9.9.9", rel.Version)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("%d requests, want 1", got)
	}
}

// The whole point of the state file: a second launch on the same day must not
// ask again, but must still know the answer.
func TestCheckHonoursTheInterval(t *testing.T) {
	srv, hits := serve(t, http.StatusOK, "")
	isolate(t, srv)

	for i := range 3 {
		rel, err := Check(context.Background(), "v1.0.0")
		if err != nil {
			t.Fatalf("Check %d: %v", i, err)
		}
		if rel == nil || rel.Version != "v9.9.9" {
			t.Fatalf("Check %d returned %v, want v9.9.9", i, rel)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("%d requests over three checks, want 1", got)
	}
}

func TestCheckSaysNothingWhenCurrent(t *testing.T) {
	srv, _ := serve(t, http.StatusOK, `{"version":"v1.0.0"}`)
	isolate(t, srv)

	rel, err := Check(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel != nil {
		t.Errorf("Check returned %v for the current version, want nil", rel)
	}
}

// An unstamped build has nothing to compare, and must not ask at all: there is
// no answer that could be acted on.
func TestCheckSkipsUnversionedBuilds(t *testing.T) {
	srv, hits := serve(t, http.StatusOK, "")
	isolate(t, srv)

	rel, err := Check(context.Background(), "abc1234-dirty")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel != nil {
		t.Errorf("Check returned %v for an unstamped build, want nil", rel)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("%d requests, want none", got)
	}
}

// A failed check is not an outage for the user: the last good answer is still a
// fact, and the error is reported alongside it for the caller who wants to say
// why.
func TestCheckFallsBackToTheLastGoodAnswer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)
	writeState(state{
		Checked: time.Now().Add(-48 * time.Hour),
		Release: &Release{Version: "v9.9.9"},
	})

	srv, _ := serve(t, http.StatusInternalServerError, "nope")
	old := Endpoint
	Endpoint = srv.URL
	t.Cleanup(func() { Endpoint = old })

	rel, err := Check(context.Background(), "v1.0.0")
	if err == nil {
		t.Error("Check reported no error for a 500")
	}
	if rel == nil || rel.Version != "v9.9.9" {
		t.Fatalf("Check returned %v, want the remembered v9.9.9", rel)
	}
}

// A failure must not turn into a request on every launch, but must be retried
// sooner than a success would be.
func TestFailedCheckRetriesSoonerThanADay(t *testing.T) {
	srv, hits := serve(t, http.StatusInternalServerError, "nope")
	dir := isolate(t, srv)

	if _, err := Check(context.Background(), "v1.0.0"); err == nil {
		t.Fatal("want an error from a 500")
	}
	if _, err := Check(context.Background(), "v1.0.0"); err != nil {
		t.Fatalf("second Check: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("%d requests, want the second to be held off", got)
	}

	// Older than the retry wait but well inside the success interval: the point
	// is that a failure does not cost a whole day of silence.
	var s state
	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("state file: %v", err)
	}
	if !s.Failed {
		t.Error("the failure was not recorded")
	}
	s.Checked = time.Now().Add(-2 * retryInterval)
	writeState(s)

	if _, err := Check(context.Background(), "v1.0.0"); err == nil {
		t.Fatal("want an error from a 500")
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("%d requests, want the retry to have happened", got)
	}
}

// Anything that is not a version is not an answer, however well-formed the JSON
// around it is.
func TestCheckRejectsAJunkVersion(t *testing.T) {
	srv, _ := serve(t, http.StatusOK, `{"version":"latest"}`)
	isolate(t, srv)

	rel, err := Check(context.Background(), "v1.0.0")
	if err == nil {
		t.Error("Check accepted a non-version")
	}
	if rel != nil {
		t.Errorf("Check returned %v, want nil", rel)
	}
}

// Unknown fields are what lets the endpoint grow one without breaking a
// matterbox that shipped before it existed.
func TestCheckIgnoresUnknownFields(t *testing.T) {
	srv, _ := serve(t, http.StatusOK, `{"version":"v9.9.9","urgent":true,"notes":"…"}`)
	isolate(t, srv)

	rel, err := Check(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel == nil || rel.Version != "v9.9.9" {
		t.Fatalf("Check returned %v, want v9.9.9", rel)
	}
}

func TestPendingRoundTrips(t *testing.T) {
	t.Cleanup(func() { SetPending(nil) })
	if got := Pending(); got != nil {
		t.Fatalf("Pending() = %v before anything was set, want nil", got)
	}
	rel := &Release{Version: "v9.9.9"}
	SetPending(rel)
	if got := Pending(); got != rel {
		t.Errorf("Pending() = %v, want %v", got, rel)
	}
}
