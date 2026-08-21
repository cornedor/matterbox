package ui

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/control"
)

// openChannelRequestMsg asks the running TUI to open a channel by id. It is
// delivered by the control socket (see ServeControlSocket), which the
// `matterbox open` CLI verb — and, through it, the `matterbox listen`
// notification buttons — write to.
type openChannelRequestMsg struct{ channelID string }

// ServeControlSocket starts a unix-socket listener that lets another process
// drive the running TUI (`open <channel-id>`, which sends an
// openChannelRequestMsg into the program) or ask what it is showing (`status`,
// answered from the atomic snapshot in focus.go — `matterbox listen` uses it to
// stay quiet about the conversation you're reading). It returns a stop func
// that closes the listener and removes the socket; call it (deferred) when the
// program exits. Any setup failure degrades to a no-op stop: the TUI runs fine,
// just without external control.
func ServeControlSocket(p *tea.Program) func() {
	path, err := control.SocketPath()
	if err != nil {
		return func() {}
	}
	return serveControlAt(path, p.Send)
}

// serveControlAt is the testable core of ServeControlSocket: it takes the
// socket path and the program's send func directly, so a test can exercise the
// round-trip without a real tea.Program or the user's real config dir.
func serveControlAt(path string, send func(tea.Msg)) func() {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}
	}
	ln, err := listenReclaim(path)
	if err != nil {
		return func() {}
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed by stop()
			}
			go handleControlConn(conn, send)
		}
	}()
	return func() {
		ln.Close()
		os.Remove(path)
	}
}

// listenReclaim binds the unix socket, reclaiming a stale one left by a crashed
// instance: if the existing socket answers a dial a live TUI owns it (return
// the bind error so the new instance backs off), otherwise the dead file is
// removed and the bind retried.
func listenReclaim(path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err == nil {
		return ln, nil
	}
	if c, derr := net.Dial("unix", path); derr == nil {
		c.Close()
		return nil, err // a live instance already owns it
	}
	os.Remove(path)
	return net.Listen("unix", path)
}

// handleControlConn reads newline-delimited commands from one connection:
// `open <channel-id>` (no reply) and `status` (one JSON line back). Blank or
// unknown lines are ignored so the protocol can grow without breaking older
// callers. status is answered straight from the published snapshot — never by
// asking the program — so a query can't block on, or disturb, the event loop.
func handleControlConn(conn net.Conn, send func(tea.Msg)) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		verb, arg, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		switch verb {
		case control.VerbOpen:
			if id := strings.TrimSpace(arg); id != "" {
				send(openChannelRequestMsg{channelID: id})
			}
		case control.VerbStatus:
			if b, err := json.Marshal(publishedStatus()); err == nil {
				_, _ = conn.Write(append(b, '\n'))
			}
		}
	}
}

// openChannelExternal opens a channel the way the feed and switcher do, in
// response to a control-socket request. An id that isn't in the local channel
// list is surfaced in the status line rather than silently dropped.
func (m Model) openChannelExternal(channelID string) (tea.Model, tea.Cmd) {
	ch := m.findChannel(channelID)
	if ch == nil {
		m.status = "open: channel not in the local list"
		return m, nil
	}
	m.switchToChannelHomeTeam(ch)
	m.filterValue = ""
	m.filter.SetValue("")
	m.focus = focusMessages
	// "cli" covers both callers of the control socket: a hand-run `matterbox
	// open` and the desktop-notification helper running the same verb. They are
	// indistinguishable on the wire, so claiming "notification" for either would
	// be a guess.
	return m, tea.Batch(m.openChannelLoadCmd(ch.Id, "cli"), m.bumpChannelStat(ch.Id))
}
