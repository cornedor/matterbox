package telemetry

import (
	"errors"
	"strings"
	"testing"
)

// metaMap is an in-memory VersionStore, standing in for the message cache.
type metaMap struct {
	m        map[string]string
	writeErr error
}

func (s *metaMap) GetMeta(key string) (string, bool, error) {
	v, ok := s.m[key]
	return v, ok, nil
}

func (s *metaMap) SetMeta(key, value string) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[key] = value
	return nil
}

// TestCheckVersionReportsOnlyRealUpgrades covers the three cases that decide
// whether "do people update?" is answerable: a first run is an install and must
// not look like a rollout, an unchanged version must say nothing at all, and a
// changed one must report both ends of the move.
func TestCheckVersionReportsOnlyRealUpgrades(t *testing.T) {
	t.Cleanup(Close)
	in, url := newIngest(t)
	cfg := consentingConfig(t)
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)
	Start(cfg)

	st := &metaMap{}

	// First run: nothing remembered, so nothing to compare against.
	CheckVersion(st, "v0.9.0")
	if got := st.m[versionMetaKey]; got != "v0.9.0" {
		t.Errorf("first run recorded %q, want the running version", got)
	}

	// Same build again: no news.
	CheckVersion(st, "v0.9.0")

	// A new build: the upgrade itself.
	CheckVersion(st, "v1.0.0")
	if got := st.m[versionMetaKey]; got != "v1.0.0" {
		t.Errorf("remembered %q after an upgrade, want v1.0.0", got)
	}
	Close()

	body := in.all()
	if n := strings.Count(body, "version_upgraded"); n != 1 {
		t.Errorf("version_upgraded sent %d times, want exactly one: %s", n, body)
	}
	if !strings.Contains(body, `"from":"v0.9.0"`) || !strings.Contains(body, `"to":"v1.0.0"`) {
		t.Errorf("the upgrade lost one of its ends: %s", body)
	}
}

// TestCheckVersionNeedsNothing: the call sites don't guard, so every degenerate
// input has to be safe — telemetry off, no cache, no version string.
func TestCheckVersionNeedsNothing(t *testing.T) {
	t.Cleanup(func() { active.Store(false) })
	active.Store(false)
	CheckVersion(&metaMap{}, "v1.0.0") // telemetry off
	active.Store(true)
	CheckVersion(nil, "v1.0.0")  // no store (the cache failed to open)
	CheckVersion(&metaMap{}, "") // no build stamp
}

// TestCheckVersionSkipsReportOnFailedWrite: an upgrade announced on every launch
// would overstate the rollout, so the report only follows a successful write.
func TestCheckVersionSkipsReportOnFailedWrite(t *testing.T) {
	t.Cleanup(Close)
	in, url := newIngest(t)
	cfg := consentingConfig(t)
	t.Setenv(KeyEnv, "phc_test")
	t.Setenv(HostEnv, url)
	Start(cfg)

	st := &metaMap{m: map[string]string{versionMetaKey: "v0.9.0"}, writeErr: errors.New("read-only database")}
	CheckVersion(st, "v1.0.0")
	Close()

	if body := in.all(); strings.Contains(body, "version_upgraded") {
		t.Errorf("reported an upgrade it could not remember: %s", body)
	}
}
