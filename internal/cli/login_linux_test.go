//go:build linux

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestURLHandlerRoundTrip exercises the real Linux capture path: register
// the scheme handler + open the socket, then have the hidden url-handler
// dial it, and confirm the URL is delivered to the channel. XDG dirs are
// redirected to temp dirs so the test touches no real user files.
func TestURLHandlerRoundTrip(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := make(chan string, 2)
	cleanup, enabled := startURLHandlerCapture(ctx, ch)
	defer cleanup()
	if !enabled {
		t.Fatal("expected capture to be enabled with writable XDG dirs")
	}

	// The .desktop entry should have been written with the scheme mapping
	// and the %u-bearing Exec line.
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
	if err := runURLHandler([]string{want}); err != nil {
		t.Fatalf("runURLHandler: %v", err)
	}

	select {
	case got := <-ch:
		if got != want {
			t.Errorf("delivered %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for handler delivery")
	}
}

// TestURLHandlerNoListener confirms the handler is a quiet no-op when no
// login is waiting (socket absent) — the case where a stale browser tab
// fires mmauth:// long after `login` exited.
func TestURLHandlerNoListener(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if err := runURLHandler([]string{"mmauth://callback?MMAUTHTOKEN=tok"}); err != nil {
		t.Errorf("expected nil error with no listener, got %v", err)
	}
	if err := runURLHandler(nil); err != nil {
		t.Errorf("expected nil error with no args, got %v", err)
	}
}
