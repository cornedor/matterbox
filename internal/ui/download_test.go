package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestExpandUserPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	cases := []struct {
		in   string
		want string
	}{
		{"~", home},
		{"~/Downloads", filepath.Join(home, "Downloads")},
		{"~/a/b", filepath.Join(home, "a", "b")},
		{"/abs/path", "/abs/path"},
		{"relative/path", "relative/path"},
		{"~tilde-not-home", "~tilde-not-home"}, // only "~" or "~/" expand
		{"", ""},
	}
	for _, c := range cases {
		if got := expandUserPath(c.in); got != c.want {
			t.Errorf("expandUserPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDownloadNameStripsPathSeparators(t *testing.T) {
	cases := []struct {
		info *model.FileInfo
		want string
	}{
		{&model.FileInfo{Name: "report.pdf"}, "report.pdf"},
		{&model.FileInfo{Name: "../../etc/passwd"}, "passwd"},
		{&model.FileInfo{Name: "sub/dir/file.txt"}, "file.txt"},
		{&model.FileInfo{Name: "", Id: "abc123"}, "abc123"},
		{&model.FileInfo{Name: "", Id: ""}, "file"},
		{&model.FileInfo{Name: "/", Id: "id"}, "id"},
	}
	for _, c := range cases {
		if got := downloadName(c.info); got != c.want {
			t.Errorf("downloadName(%+v) = %q, want %q", c.info, got, c.want)
		}
	}
}

func TestUniqueDownloadPath(t *testing.T) {
	dir := t.TempDir()

	// First write of a name lands on the bare name.
	first := uniqueDownloadPath(dir, "report.pdf")
	if want := filepath.Join(dir, "report.pdf"); first != want {
		t.Fatalf("first = %q, want %q", first, want)
	}
	if err := os.WriteFile(first, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Collision inserts a counter before the extension.
	second := uniqueDownloadPath(dir, "report.pdf")
	if want := filepath.Join(dir, "report (1).pdf"); second != want {
		t.Fatalf("second = %q, want %q", second, want)
	}
	if err := os.WriteFile(second, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Next collision bumps the counter again.
	third := uniqueDownloadPath(dir, "report.pdf")
	if want := filepath.Join(dir, "report (2).pdf"); third != want {
		t.Fatalf("third = %q, want %q", third, want)
	}

	// A name with no extension still de-collides cleanly.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := uniqueDownloadPath(dir, "README"), filepath.Join(dir, "README (1)"); got != want {
		t.Fatalf("no-ext collision = %q, want %q", got, want)
	}
}

func TestPostFiles(t *testing.T) {
	if got := postFiles(nil); got != nil {
		t.Errorf("postFiles(nil) = %v, want nil", got)
	}
	if got := postFiles(&model.Post{}); got != nil {
		t.Errorf("postFiles(no metadata) = %v, want nil", got)
	}
	files := []*model.FileInfo{{Id: "1", Name: "a.png"}, {Id: "2", Name: "b.pdf"}}
	p := &model.Post{Metadata: &model.PostMetadata{Files: files}}
	got := postFiles(p)
	if len(got) != 2 || got[0].Id != "1" || got[1].Id != "2" {
		t.Errorf("postFiles = %+v, want the two metadata files in order", got)
	}
}
