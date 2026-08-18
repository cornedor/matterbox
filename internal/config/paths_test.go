package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The override names the matterbox directory itself — appending "matterbox" to
// it would silently relocate every file a caller asks for.
func TestDirHonoursOverrideVerbatim(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DirEnv, dir)

	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got != dir {
		t.Errorf("Dir() = %q, want the override verbatim (%q)", got, dir)
	}

	f, err := File("config.yaml")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if want := filepath.Join(dir, "config.yaml"); f != want {
		t.Errorf("File() = %q, want %q", f, want)
	}
}

// Without the override the answer stays what it has always been: the platform
// config dir plus "matterbox", which is where everyone's existing files are.
// An empty override counts as absent — a blank variable must not root
// matterbox at the filesystem root.
func TestDirFallsBackToPlatformConfigDir(t *testing.T) {
	// t.Setenv first: its cleanup restores whatever the caller's environment
	// had, including for the Unsetenv below.
	t.Setenv(DirEnv, "")

	base, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			t.Skip("machine has neither a config dir nor a home dir")
		}
		base = filepath.Join(home, ".config")
	}
	want := filepath.Join(base, "matterbox")

	if got, err := Dir(); err != nil || got != want {
		t.Errorf("Dir() with an empty override = %q, %v; want %q", got, err, want)
	}

	os.Unsetenv(DirEnv)
	if got, err := Dir(); err != nil || got != want {
		t.Errorf("Dir() with no override = %q, %v; want %q", got, err, want)
	}
}
