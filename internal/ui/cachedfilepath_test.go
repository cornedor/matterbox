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
		got, err := m.cachedFilePath(&model.FileInfo{Id: validFileID, Name: name})
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

	// The id prefix is just as server-controlled as the name, and inline
	// thumbnails write here without any user gesture at all.
	ids := []string{
		"../../../../home/u/.config/autostart/x.desktop",
		"../../.zshenv",
		"..",
		"/etc/passwd",
		"sub/dir",
		"",
	}
	for _, id := range ids {
		got, err := m.cachedFilePath(&model.FileInfo{Id: id, Name: "plain.pdf"})
		if err != nil {
			t.Fatalf("cachedFilePath(id %q): %v", id, err)
		}
		if filepath.Dir(got) != dir {
			t.Errorf("id %q escaped the cache: %s", id, got)
		}
		if strings.Contains(got, "..") {
			t.Errorf("id %q left a traversal component in %s", id, got)
		}
	}

	// Distinct hostile ids must still land on distinct cache entries.
	a, _ := m.cachedFilePath(&model.FileInfo{Id: "../a", Name: "plain.pdf"})
	b, _ := m.cachedFilePath(&model.FileInfo{Id: "../b", Name: "plain.pdf"})
	if a == b {
		t.Errorf("hostile ids collapsed onto one cache entry: %s", a)
	}
}

// TestCachedFilePathKeepsOrdinaryNames guards the fix from over-reaching: the
// on-disk name is what the user sees in their file manager, and the id prefix
// is what keeps two uploads of "screenshot.png" apart.
func TestCachedFilePathKeepsOrdinaryNames(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var m Model
	got, err := m.cachedFilePath(&model.FileInfo{Id: validFileID, Name: "Quarterly Report (final).pdf"})
	if err != nil {
		t.Fatalf("cachedFilePath: %v", err)
	}
	if want := validFileID + "_Quarterly Report (final).pdf"; filepath.Base(got) != want {
		t.Errorf("basename = %q, want %q", filepath.Base(got), want)
	}
}
