// Package control is the wire between a running matterbox TUI and the other
// processes that want to talk to it: the `matterbox open` verb that jumps the
// TUI to a conversation, and the `matterbox listen` daemon that asks what the
// user is looking at before it fires a notification.
//
// The transport is a unix socket beside config.yaml carrying newline-delimited
// commands. Only the TUI serves it (see internal/ui.ServeControlSocket); this
// package holds the path, the verbs, and the client half, so the daemon can ask
// without importing the whole TUI.
package control

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"matterbox/internal/config"
)

// The verbs the socket understands. Unknown lines are ignored, so a new verb
// never breaks an older TUI — it just gets no answer.
const (
	// VerbOpen jumps the TUI to a channel: "open <channel-id>". No reply.
	VerbOpen = "open"
	// VerbStatus asks what the TUI is showing: "status". Replies with one
	// JSON-encoded Status line.
	VerbStatus = "status"
)

// QueryTimeout bounds a status round-trip. The TUI answers from an atomic
// snapshot without going through its event loop, so this only ever has to
// cover the socket itself; a TUI wedged long enough to miss it counts as
// "not looking".
const QueryTimeout = 300 * time.Millisecond

// Status is what a running TUI reports about itself.
type Status struct {
	// ChannelID is the conversation the TUI has open — m.openChannelID, the
	// transcript on screen, not the sidebar cursor. Empty before the first
	// channel opens, or while a full-window overlay (Feed, Search, SQL) has
	// replaced the transcript, since then the conversation isn't on screen.
	ChannelID string `json:"channel_id"`
	// Focused reports whether the user is actually at the TUI: the terminal
	// holds focus, or — on a terminal that doesn't report focus at all — there
	// was recent input. This is the field to gate on; FocusKnown and
	// IdleSeconds are the raw inputs behind it, reported for diagnosis.
	Focused bool `json:"focused"`
	// FocusKnown says whether the terminal reports focus (DECSET 1004). False
	// means Focused is the idle-time approximation.
	FocusKnown bool `json:"focus_known"`
	// IdleSeconds is how long ago the last keystroke or mouse event was.
	IdleSeconds int `json:"idle_seconds"`
	// PID is the TUI process, for a human staring at a log line.
	PID int `json:"pid"`
}

// Viewing reports whether the user is looking at channelID right now: the TUI
// has it open and the terminal has focus. The zero Status (no TUI running) is
// never viewing anything.
func (s Status) Viewing(channelID string) bool {
	return s.Focused && s.ChannelID != "" && s.ChannelID == channelID
}

// SocketPath returns the unix socket a running TUI listens on: tui.sock in
// the matterbox config directory, so it sits beside config.yaml.
func SocketPath() (string, error) {
	return config.File("tui.sock")
}

// Query asks the TUI at path what it is showing. The second result is false
// when no TUI answered — no socket, a stale socket file from a crash, a TUI too
// old to know the verb, or a garbled reply. Callers should read that as "no
// TUI", never as an error worth failing over: a notification skipped because a
// dial failed would be a notification lost.
func Query(path string, timeout time.Duration) (Status, bool) {
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return Status{}, false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := fmt.Fprintln(conn, VerbStatus); err != nil {
		return Status{}, false
	}
	sc := bufio.NewScanner(conn)
	if !sc.Scan() {
		return Status{}, false
	}
	var s Status
	if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
		return Status{}, false
	}
	return s, true
}

// Send writes one command to the TUI's control socket and closes. A failed dial
// means no TUI is listening at path.
func Send(path, command string) error {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return fmt.Errorf("no running matterbox TUI to drive (control socket %s)", path)
	}
	defer conn.Close()
	_, err = fmt.Fprintln(conn, command)
	return err
}
