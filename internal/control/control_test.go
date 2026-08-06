package control

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serveStatus stands in for a running TUI: it answers `status` with the given
// Status and ignores anything else, exactly like handleControlConn.
func serveStatus(t *testing.T, s Status) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tui.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc := bufio.NewScanner(conn)
				for sc.Scan() {
					if strings.TrimSpace(sc.Text()) != VerbStatus {
						continue
					}
					b, _ := json.Marshal(s)
					conn.Write(append(b, '\n'))
				}
			}()
		}
	}()
	return path
}

func TestQueryRoundTrip(t *testing.T) {
	want := Status{ChannelID: "c1", Focused: true, FocusKnown: true, IdleSeconds: 3, PID: 42}
	path := serveStatus(t, want)

	got, ok := Query(path, time.Second)
	if !ok {
		t.Fatal("Query reported no TUI, want an answer")
	}
	if got != want {
		t.Fatalf("Query = %+v, want %+v", got, want)
	}
}

// No socket at all is the everyday case for a daemon on another machine (and
// for anyone not running the TUI). It must read as "no TUI", promptly.
func TestQueryNoSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.sock")
	if s, ok := Query(path, time.Second); ok || s != (Status{}) {
		t.Fatalf("Query on a missing socket = %+v, %t; want zero, false", s, ok)
	}
}

// A socket that accepts but never answers (a wedged TUI) must not hold the
// caller past the timeout — a notification is waiting behind it.
func TestQuerySilentPeerTimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tui.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close() // accept, then say nothing
		}
	}()

	start := time.Now()
	if _, ok := Query(path, 150*time.Millisecond); ok {
		t.Fatal("a silent peer must not count as an answer")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Query blocked for %v, want it bounded by the timeout", elapsed)
	}
}

func TestViewing(t *testing.T) {
	cases := []struct {
		name    string
		status  Status
		channel string
		want    bool
	}{
		{"open and focused", Status{ChannelID: "c1", Focused: true}, "c1", true},
		{"open but blurred", Status{ChannelID: "c1"}, "c1", false},
		{"focused on another channel", Status{ChannelID: "c2", Focused: true}, "c1", false},
		{"no TUI", Status{}, "c1", false},
		// A TUI reporting no on-screen channel (a full-window tab) must not
		// match a post that carries no channel id either.
		{"nothing on screen", Status{Focused: true}, "", false},
	}
	for _, c := range cases {
		if got := c.status.Viewing(c.channel); got != c.want {
			t.Errorf("%s: Viewing(%q) = %t, want %t", c.name, c.channel, got, c.want)
		}
	}
}
