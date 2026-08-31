package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestCachedFilePathStaysInCache pins the containment of a path that callers
// write to: openOpenable and readOrDownloadFile os.WriteFile whatever this
// returns, so an attachment named "../../../.zshenv" would be an arbitrary file
// write triggered by opening a message someone else posted.
func TestCachedFilePathStaysInCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var m Model

	base, err := m.cachedFilePath(&model.FileInfo{Id: "id", Name: "plain.pdf"})
	if err != nil {
		t.Fatalf("cachedFilePath: %v", err)
	}
	dir := filepath.Dir(base)

	names := []string{
		"../../../.zshenv",
		"../.zshenv",
		"..",
		"../../../../../../etc/cron.d/x",
		"/etc/passwd",
		"sub/dir/evil.sh",
		"",
	}
	for _, name := range names {
		got, err := m.cachedFilePath(&model.FileInfo{Id: "id", Name: name})
		if err != nil {
			t.Fatalf("cachedFilePath(%q): %v", name, err)
		}
		if filepath.Dir(got) != dir {
			t.Errorf("name %q escaped the cache: %s", name, got)
		}
		if strings.Contains(got, "..") {
			t.Errorf("name %q left a traversal component in %s", name, got)
		}
	}
}

// TestCachedFilePathKeepsOrdinaryNames guards the fix from over-reaching: the
// on-disk name is what the user sees in their file manager, and the id prefix
// is what keeps two uploads of "screenshot.png" apart.
func TestCachedFilePathKeepsOrdinaryNames(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var m Model
	got, err := m.cachedFilePath(&model.FileInfo{Id: "abc123", Name: "Quarterly Report (final).pdf"})
	if err != nil {
		t.Fatalf("cachedFilePath: %v", err)
	}
	if want := "abc123_Quarterly Report (final).pdf"; filepath.Base(got) != want {
		t.Errorf("basename = %q, want %q", filepath.Base(got), want)
	}
}
