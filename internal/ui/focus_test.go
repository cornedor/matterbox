package ui

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/control"
)

// blurred marks the terminal as reporting focus, and as not having it — the
// state the mark-read gate and the notification suppression both hang on.
func blurred(m *Model) {
	m.termFocusKnown, m.termFocused = true, false
}

// --- the mark-read gate ---------------------------------------------------

// A dwell that runs out while the terminal is blurred must not mark the channel
// read: the user isn't looking, and LastViewedAt is what tells `matterbox
// listen` (and every other client) that a message has been seen.
func TestMarkViewedMsgBlurredIsNoop(t *testing.T) {
	m := newMarkReadModel(5 * time.Second)
	blurred(&m)

	next, cmd := m.update(markViewedMsg{channelID: "chan1", gen: 1})
	nm := next.(Model)

	if !isUnread(nm, "chan1") {
		t.Fatal("a dwell that elapsed while blurred must leave the badges alone")
	}
	if nm.viewSettled {
		t.Fatal("viewSettled must stay false so refocusing re-arms the dwell")
	}
	if cmd != nil {
		t.Fatal("no mark-read command may be issued while blurred")
	}
}

// Opening a channel in a window you aren't looking at — what `matterbox open`
// from a notification does — must not mark it read either, with or without a
// dwell configured.
func TestScheduleMarkViewedBlurredIsNoop(t *testing.T) {
	for _, delay := range []time.Duration{0, 5 * time.Second} {
		m := newMarkReadModel(delay)
		blurred(&m)

		if cmd := m.scheduleMarkViewed("chan1"); cmd != nil {
			t.Fatalf("delay %v: expected no command while blurred", delay)
		}
		if !isUnread(m, "chan1") {
			t.Fatalf("delay %v: badges cleared while blurred", delay)
		}
		if m.viewSettled {
			t.Fatalf("delay %v: view settled while blurred", delay)
		}
	}
}

// A terminal that never reports focus (no FocusMsg/BlurMsg ever arrives) must
// behave exactly as it did before the gate existed — the fallback can only ever
// suppress a mark-read we positively know the user missed.
func TestUnknownFocusStillMarksRead(t *testing.T) {
	m := newMarkReadModel(5 * time.Second)
	if !m.terminalFocused() {
		t.Fatal("an unreporting terminal must count as focused")
	}
	next, _ := m.update(markViewedMsg{channelID: "chan1", gen: 1})
	if isUnread(next.(Model), "chan1") {
		t.Fatal("the dwell must still clear badges when focus is unknown")
	}
}

// Focus coming back finishes the job: a dwell that was dropped while away is
// re-armed, so the channel is marked read once the user is actually there.
func TestFocusReturnReArmsDwell(t *testing.T) {
	m := newMarkReadModel(5 * time.Second)
	blurred(&m)

	cmd := m.applyTerminalFocus(true)

	if !m.termFocused {
		t.Fatal("applyTerminalFocus(true) must record the focus")
	}
	if cmd == nil {
		t.Fatal("expected the dwell to be re-armed on refocus")
	}
	if m.viewSettled {
		t.Fatal("the re-armed dwell must not settle the view before it fires")
	}
}

// If the dwell had already completed before the user looked away, refocusing
// marks the channel read outright — that catches up everything that arrived
// while the window was buried.
func TestFocusReturnCatchesUpSettledChannel(t *testing.T) {
	m := newMarkReadModel(5 * time.Second)
	m.me = &model.User{Id: "me"} // markChannelViewed needs a user to act
	m.viewSettled = true
	blurred(&m)

	if cmd := m.applyTerminalFocus(true); cmd == nil {
		t.Fatal("expected a mark-read command when focus returns to a settled channel")
	}
}

// Losing focus is not, by itself, a reason to touch the server.
func TestBlurIssuesNoCommand(t *testing.T) {
	m := newMarkReadModel(5 * time.Second)
	m.termFocusKnown, m.termFocused = true, true

	if cmd := m.applyTerminalFocus(false); cmd != nil {
		t.Fatal("blur must not issue a command")
	}
	if m.terminalFocused() {
		t.Fatal("terminalFocused must report false once the terminal says so")
	}
}

// A live message arriving in the open channel is marked read only when the user
// is actually in front of it. This is the DM case that started all this: you're
// chatting, so nothing should notify — but with the window buried, the reply
// must stay unread so it can.
func TestLiveMarkReadFollowsFocus(t *testing.T) {
	m := newMarkReadModel(5 * time.Second)
	m.me = &model.User{Id: "me"}
	m.viewSettled = true

	if m.liveMarkRead("chan1") == nil {
		t.Fatal("a message arriving while focused must mark the channel read")
	}
	blurred(&m)
	if m.liveMarkRead("chan1") != nil {
		t.Fatal("a message arriving while blurred must leave the channel unread")
	}
	m.termFocused, m.viewSettled = true, false
	if m.liveMarkRead("chan1") != nil {
		t.Fatal("a message arriving before the dwell completes must not mark read early")
	}
}

// --- what the control socket answers --------------------------------------

// queryStatus asks the socket the way `matterbox listen` does.
func queryStatus(t *testing.T, path string) control.Status {
	t.Helper()
	s, ok := control.Query(path, 2*time.Second)
	if !ok {
		t.Fatal("no answer from the control socket")
	}
	return s
}

// The published status is what the daemon gates on, so it has to track the
// conversation actually on screen and the terminal's focus.
func TestStatusReportsOpenChannelAndFocus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tui.sock")
	stop := serveControlAt(path, func(tea.Msg) {})
	defer stop()

	m := newMarkReadModel(time.Second)
	m.termFocusKnown, m.termFocused = true, true
	m.publishStatus()

	got := queryStatus(t, path)
	if !got.Viewing("chan1") {
		t.Fatalf("status %+v should be viewing chan1", got)
	}
	if !got.FocusKnown || got.PID == 0 {
		t.Fatalf("status %+v should carry the focus-known flag and a pid", got)
	}

	// Look away: same channel open, but nobody is reading it.
	m.termFocused = false
	m.publishStatus()
	if got := queryStatus(t, path); got.Viewing("chan1") {
		t.Fatalf("a blurred terminal must not report viewing (%+v)", got)
	}
}

// A conversation hidden behind a full-window tab isn't being read, even though
// openChannelID still points at it — the same distinction isCurrentChannel
// draws for the mark-read dwell.
func TestStatusHidesChannelBehindFullWindowTab(t *testing.T) {
	m := newMarkReadModel(time.Second)
	m.termFocusKnown, m.termFocused = true, true
	m.hasDMs = false // no DMs/SQL tab → tab 0 is the Feed
	if !m.onFeedTab() {
		t.Fatal("setup: expected to be on the Feed tab")
	}
	m.publishStatus()

	if got := publishedStatus(); got.Viewing("chan1") {
		t.Fatalf("a channel behind the Feed tab must not report as viewed (%+v)", got)
	}
}

// On a terminal that never reports focus, "are you there" degrades to recent
// input rather than answering a confident false forever.
func TestStatusIdleFallbackWithoutFocusReports(t *testing.T) {
	m := newMarkReadModel(time.Second)
	m.publishStatus() // focusKnown false — the terminal never told us

	lastInputNanos.Store(time.Now().UnixNano())
	if got := publishedStatus(); !got.Focused || got.FocusKnown {
		t.Fatalf("recent input should read as focused via the fallback (%+v)", got)
	}

	lastInputNanos.Store(time.Now().Add(-2 * activeIdleWindow).UnixNano())
	if got := publishedStatus(); got.Focused {
		t.Fatalf("input long past the idle window must not read as focused (%+v)", got)
	}
}

// noteActivity is what feeds that fallback: keystrokes and mouse events count,
// background traffic (a WebSocket post, a timer) does not.
func TestNoteActivityOnlyCountsInput(t *testing.T) {
	lastInputNanos.Store(0)
	noteActivity(postsLoadedMsg{})
	if lastInputNanos.Load() != 0 {
		t.Fatal("a background message must not count as user activity")
	}
	noteActivity(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if lastInputNanos.Load() == 0 {
		t.Fatal("a keystroke must count as user activity")
	}
}

// The status verb must not disturb the program: it answers from the snapshot,
// never by sending a message into the event loop.
func TestStatusVerbSendsNoMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tui.sock")
	got := make(chan tea.Msg, 1)
	stop := serveControlAt(path, func(m tea.Msg) { got <- m })
	defer stop()

	queryStatus(t, path)
	select {
	case m := <-got:
		t.Fatalf("status pushed %#v into the program", m)
	case <-time.After(100 * time.Millisecond):
	}
}

// The reply is one JSON line, so a reader can scan lines without knowing the
// payload — and an unknown verb on the same connection is skipped without
// derailing it.
func TestStatusReplyIsOneJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tui.sock")
	stop := serveControlAt(path, func(tea.Msg) {})
	defer stop()

	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("wibble\nstatus\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	dec := json.NewDecoder(conn)
	var s control.Status
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
}
