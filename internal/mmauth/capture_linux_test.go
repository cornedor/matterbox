//go:build linux

package mmauth

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCaptureRoundTrip exercises the real Linux capture path: register the
// scheme handler + open the socket, then have the hidden url-handler dial it,
// and confirm the URL is delivered on the channel. XDG dirs are redirected to
// temp dirs so the test touches no real user files.
func TestCaptureRoundTrip(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	// Registration shells out to xdg-mime, which edits mimeapps.list in
	// XDG_CONFIG_HOME — the user's real one unless it is redirected too.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cap, enabled := StartCapture(ctx)
	defer cap.Close()
	if !enabled {
		t.Fatal("expected capture to be enabled with writable XDG dirs")
	}

	// The .desktop entry should carry the scheme mapping and %u-bearing Exec.
	desktop, err := os.ReadFile(filepath.Join(dataHome, "applications", "matterbox-mmauth.desktop"))
	if err != nil {
		t.Fatalf("read .desktop: %v", err)
	}
	if !strings.Contains(string(desktop), "x-scheme-handler/mmauth;") {
		t.Errorf(".desktop missing scheme mapping:\n%s", desktop)
	}
	if !strings.Contains(string(desktop), "url-handler %u") {
		t.Errorf(".desktop missing url-handler Exec:\n%s", desktop)
	}

	want := "mmauth://callback?MMAUTHTOKEN=tok123&MMCSRF=xyz"
	if err := HandleURL(want); err != nil {
		t.Fatalf("HandleURL: %v", err)
	}

	select {
	case got := <-cap.URL:
		if got != want {
			t.Errorf("delivered %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for handler delivery")
	}
}

// TestCaptureCloseUnblocksWaiter confirms Close releases a waiter blocked on
// URL (the channel is closed) rather than leaking the goroutine.
func TestCaptureCloseUnblocksWaiter(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cap, enabled := StartCapture(context.Background())
	if !enabled {
		t.Fatal("expected capture to be enabled")
	}
	cap.Close()
	select {
	case _, ok := <-cap.URL:
		if ok {
			t.Fatal("expected URL channel to be closed after Close, got a value")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not unblock the URL waiter")
	}
}

// TestHandleURLNoListener confirms the handler is a quiet no-op when nothing is
// waiting (socket absent) — e.g. a stale tab fires mmauth:// long after exit.
func TestHandleURLNoListener(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if err := HandleURL("mmauth://callback?MMAUTHTOKEN=tok"); err != nil {
		t.Errorf("expected nil error with no listener, got %v", err)
	}
	if err := HandleURL(""); err != nil {
		t.Errorf("expected nil error with empty url, got %v", err)
	}
}

// TestCaptureSocketIsPrivate pins the mode on the login socket. Whatever is
// written to it is stored as a session token, so a socket any local user can
// connect to lets them hand us an mmauth://callback of their choosing. On a
// systemd box XDG_RUNTIME_DIR is already 0700, but the os.TempDir() fallback
// is not — the mode is what holds in both cases.
func TestCaptureSocketIsPrivate(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "") // force the shared-directory fallback
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cap, enabled := StartCapture(ctx)
	defer cap.Close()
	if !enabled {
		t.Skip("capture unavailable in this environment")
	}
	path := SocketPath()
	if !strings.HasPrefix(path, tmp) {
		t.Fatalf("socket %s did not land in the redirected temp dir", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("login socket is %04o; any local user could inject a token", perm)
	}
}

// TestCaptureSurvivesSilentPeer guards the accept loop: a connection that never
// writes must not hold the socket, or one stray connect (or a deliberate one)
// blocks the browser's real callback from ever being delivered.
func TestCaptureSurvivesSilentPeer(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	old := readTimeout
	readTimeout = 50 * time.Millisecond
	t.Cleanup(func() { readTimeout = old })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cap, enabled := StartCapture(ctx)
	defer cap.Close()
	if !enabled {
		t.Skip("capture unavailable in this environment")
	}

	// A peer that connects and says nothing, held open for the whole test.
	silent, err := net.Dial("unix", SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer silent.Close()

	want := "mmauth://callback?MMAUTHTOKEN=tok123"
	deadline := time.After(5 * time.Second)
	for {
		if err := HandleURL(want); err != nil {
			t.Fatalf("HandleURL: %v", err)
		}
		select {
		case got := <-cap.URL:
			if got != want {
				t.Fatalf("captured %q, want %q", got, want)
			}
			return
		case <-deadline:
			t.Fatal("the silent peer blocked delivery of the real callback")
		case <-time.After(25 * time.Millisecond):
			// The silent conn may still be draining its deadline; retry.
		}
	}
}
