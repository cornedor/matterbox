package ui

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/testsock"
)

// dialAndSend opens the control socket, writes one line, and closes.
func dialAndSend(t *testing.T, path, line string) {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, line); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestServeControlSocket_OpenForwardsChannelID(t *testing.T) {
	path := testsock.Path(t)
	got := make(chan tea.Msg, 1)
	stop := serveControlAt(path, func(m tea.Msg) { got <- m })
	defer stop()

	dialAndSend(t, path, "open abc123def456")

	select {
	case m := <-got:
		req, ok := m.(openChannelRequestMsg)
		if !ok {
			t.Fatalf("got %T, want openChannelRequestMsg", m)
		}
		if req.channelID != "abc123def456" {
			t.Fatalf("channelID = %q, want abc123def456", req.channelID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message delivered")
	}
}

func TestServeControlSocket_IgnoresBlankAndUnknown(t *testing.T) {
	path := testsock.Path(t)
	got := make(chan tea.Msg, 4)
	stop := serveControlAt(path, func(m tea.Msg) { got <- m })
	defer stop()

	// Blank arg, unknown verb, and bare "open" must not produce a message;
	// the real open afterwards must, proving the connection still works.
	dialAndSend(t, path, "open   ")
	dialAndSend(t, path, "wibble x")
	dialAndSend(t, path, "open")
	dialAndSend(t, path, "open xyz")

	select {
	case m := <-got:
		if req := m.(openChannelRequestMsg); req.channelID != "xyz" {
			t.Fatalf("first delivered msg = %q, want only xyz", req.channelID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the valid open was dropped")
	}
	// Nothing else should arrive.
	select {
	case m := <-got:
		t.Fatalf("unexpected extra message: %#v", m)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestServeControlSocket_ReclaimsStaleSocketFile(t *testing.T) {
	path := testsock.Path(t)
	// A leftover non-socket file at the path simulates a crashed instance.
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := make(chan tea.Msg, 1)
	stop := serveControlAt(path, func(m tea.Msg) { got <- m })
	defer stop()

	dialAndSend(t, path, "open reclaimed")
	select {
	case m := <-got:
		if req := m.(openChannelRequestMsg); req.channelID != "reclaimed" {
			t.Fatalf("channelID = %q", req.channelID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale socket was not reclaimed")
	}
}
