package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSavePreservesComments: a save (here a team reorder, which the TUI does on
// every < / >) must update the file in place, not re-marshal it — keeping the
// user's comments and keys this version doesn't know, while still filling in
// missing defaults and dropping a migrated legacy key.
func TestSavePreservesComments(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DirEnv, dir)
	p := filepath.Join(dir, "config.yaml")
	const orig = `# my own notes about this file
server_url: https://chat.example.com # work server
team_order: [a, b]
# keep these short
reactions:
    - "+1"   # the classic
    - tada
animations:
    native_gif_protocol: true
some_future_key: keep me
rules:
    - name: ping # my rule
      actions:
        - type: log
`
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveTeamOrder([]string{"b", "a"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		"# my own notes about this file",
		"# work server",
		"# keep these short",
		"# the classic",
		"# my rule",
		"some_future_key: keep me",
		"native_animation: true",
		"mark_read_delay_seconds:", // a missing default is still filled in
		"yaml-language-server",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("saved config lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "native_gif_protocol") {
		t.Errorf("migrated legacy key kept:\n%s", got)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.TeamOrder, ",") != "b,a" || cfg.ServerURL != "https://chat.example.com" {
		t.Errorf("round trip: team_order=%v server_url=%q", cfg.TeamOrder, cfg.ServerURL)
	}
}
