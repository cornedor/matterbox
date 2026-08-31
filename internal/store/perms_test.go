package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestOpenTightensPerms pins that the cache is owner-only. It holds every
// message the client has seen, DMs included, as plaintext in raw_json — SQLite
// would otherwise create it 0644 under the default umask.
func TestOpenTightensPerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// A write forces the -wal and -shm siblings into existence; they carry the
	// same content, so they need the same mode.
	if err := s.Upsert(&model.Post{Id: "p1", ChannelId: "c1", UserId: "u1", CreateAt: 1, Message: "secret"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is %04o, want no group/other bits", filepath.Base(p), perm)
		}
	}
}

// TestOpenTightensExistingPerms covers the upgrade path: databases created
// before the fix are already on disk at 0644.
func TestOpenTightensExistingPerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.Close()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("reopen left the database at %04o", perm)
	}
}
