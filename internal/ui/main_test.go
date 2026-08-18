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
