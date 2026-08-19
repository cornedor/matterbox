package ui

import (
	"os"
	"testing"

	"matterbox/internal/config"
)

// TestMain points the whole package at a throwaway config directory. New()
// opens the message cache and reads the stats files, so without this any test
// that builds a Model would touch the user's real ~/.config/matterbox —
// creating a messages.db there when the suite runs, and letting their live
// channel_stats.json leak into test expectations.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "matterbox-ui-test")
	if err != nil {
		panic(err)
	}
	os.Setenv(config.DirEnv, dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// newTestModel builds a Model through New() and then puts it in the state the
// app is in once startup has finished: no splash, nothing loading. New() arms
// the startup splash, which covers the whole screen until the first transcript
// lands (see splash.go) — a test that renders panes, places the cursor, or
// measures layout is by definition past that point, and would otherwise be
// looking at the progress list.
func newTestModel() Model {
	m := New(nil, nil)
	m.loading = false
	m.splash.stop()
	return m
}
