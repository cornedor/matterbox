//go:build linux

package mmauth

import (
	"context"
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
