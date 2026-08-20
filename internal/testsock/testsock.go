// Package testsock hands tests a unix-socket path short enough to bind.
//
// A unix address is a fixed-size struct, and its path field is small: 104
// bytes on macOS, 108 on Linux. Going over doesn't report a name-too-long
// error — bind and connect fail with EINVAL, which surfaces as "invalid
// argument" and reads like a bug in the code under test rather than a path
// that didn't fit.
//
// t.TempDir() walks straight into that: it bakes the test's name into the
// path, and macOS's per-user $TMPDIR (/var/folders/<2>/<26>/T/) spends 48
// bytes before that. matterbox's control-socket tests landed at 113-116 bytes
// and failed on macOS only. This gives out <tmp>/mbsock<random>/s.sock
// instead — the same length however the test is named, and it falls back to
// /tmp when $TMPDIR is itself too deep to leave room.
package testsock

import (
	"os"
	"path/filepath"
	"testing"
)

// sunPathMax is macOS's limit, the tighter of the two platforms matterbox
// targets. Holding both to it keeps a path that works on Linux from being the
// reason CI goes red on macOS.
const sunPathMax = 104

// Path returns a fresh socket path in a directory of its own. Nothing exists
// at it — binding is the caller's job, and a test that wants an absent socket
// can use it as is. The directory goes away when the test ends.
func Path(t *testing.T) string {
	t.Helper()
	var lastErr error
	for _, base := range []string{os.TempDir(), "/tmp"} {
		dir, err := os.MkdirTemp(base, "mbsock")
		if err != nil {
			lastErr = err
			continue
		}
		if path := filepath.Join(dir, "s.sock"); len(path) < sunPathMax {
			t.Cleanup(func() { os.RemoveAll(dir) })
			return path
		}
		os.RemoveAll(dir)
	}
	t.Fatalf("no directory short enough for a unix socket (TMPDIR=%q, last error: %v)", os.TempDir(), lastErr)
	return ""
}
