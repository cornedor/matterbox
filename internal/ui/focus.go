package ui

import (
	"os"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/control"
)

// activeIdleWindow is how long after the last keystroke or mouse event a
// terminal that does NOT report focus still counts as "the user is here". It
// only ever applies as a fallback (see publishedStatus): a terminal that speaks
// DECSET 1004 gives us the real answer and this is never consulted.
const activeIdleWindow = 60 * time.Second

// statusSnapshot is the part of the TUI's status that changes rarely — the
// conversation on screen and whether the terminal has focus. It is published to
// a package-level atomic rather than reached out of the Model because the
// control socket answers on its own goroutine, outside the bubbletea event
// loop: a query must never have to wait for (or perturb) an Update cycle. There
// is exactly one TUI per process, so a package global is the whole truth.
type statusSnapshot struct {
	channelID  string
	focused    bool
	focusKnown bool
}

var (
	liveStatus atomic.Pointer[statusSnapshot]
	// lastInputNanos is the last keystroke/mouse event, kept out of the
	// snapshot (and out of the Model) so the per-keystroke write is a single
	// atomic store with no allocation.
	lastInputNanos atomic.Int64
)

// noteActivity records user input for the idle fallback. Called for every
// event, so it stays a type switch plus one atomic store.
func noteActivity(msg tea.Msg) {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.PasteMsg, tea.MouseClickMsg, tea.MouseWheelMsg, tea.MouseMotionMsg:
		lastInputNanos.Store(time.Now().UnixNano())
	}
}

// applyTerminalFocus folds a terminal focus/blur report into the model. The
// first one also flips focusKnown: until the terminal proves it reports focus
// we must not act on our guess, or a terminal that never sends these events
// (an old xterm, tmux without focus-events) would look permanently blurred and
// silently stop marking channels read.
func (m *Model) applyTerminalFocus(focused bool) tea.Cmd {
	m.termFocusKnown = true
	if m.termFocused == focused {
		return nil
	}
	m.termFocused = focused
	if !focused {
		return nil
	}
	// Focus came back. Anything that arrived while we were away was deliberately
	// left unread on the server (see terminalFocused), so catch up now: finish
	// the dwell if it never completed, otherwise mark the channel read outright.
	id := m.openChannelID
	if !m.isCurrentChannel(id) {
		return nil
	}
	if !m.viewSettled {
		return m.scheduleMarkViewed(id)
	}
	return m.markChannelViewed(id)
}

// terminalFocused reports whether the terminal holds focus, for the purposes of
// marking a channel read. A terminal that doesn't report focus counts as
// focused — the pre-focus behaviour — so the gate can only ever suppress a
// mark-read we know the user wasn't there for.
func (m *Model) terminalFocused() bool {
	return !m.termFocusKnown || m.termFocused
}

// publishStatus mirrors the on-screen conversation and the focus flag into the
// atomic the control socket serves. Called from the Update wrapper, so no
// channel-opening or focus-changing path has to remember to; it stores only on
// a real change, which makes the ordinary keystroke a couple of comparisons.
func (m *Model) publishStatus() {
	snap := statusSnapshot{
		channelID:  m.onScreenChannelID(),
		focused:    m.termFocused,
		focusKnown: m.termFocusKnown,
	}
	if cur := liveStatus.Load(); cur != nil && *cur == snap {
		return
	}
	liveStatus.Store(&snap)
}

// onScreenChannelID is the conversation actually visible: m.openChannelID
// unless a full-window tab (Feed, Search, SQL) has replaced the transcript, in
// which case nothing is being read and the answer is "none".
func (m *Model) onScreenChannelID() string {
	if !m.isCurrentChannel(m.openChannelID) {
		return ""
	}
	return m.openChannelID
}

// publishedStatus renders the current snapshot as the wire Status, resolving
// the focus fallback for terminals that don't report it: recent input means the
// user is here. Safe to call from any goroutine.
func publishedStatus() control.Status {
	s := statusSnapshot{}
	if cur := liveStatus.Load(); cur != nil {
		s = *cur
	}
	idle := time.Since(time.Unix(0, lastInputNanos.Load()))
	focused := s.focused
	if !s.focusKnown {
		focused = idle <= activeIdleWindow
	}
	return control.Status{
		ChannelID:   s.channelID,
		Focused:     focused,
		FocusKnown:  s.focusKnown,
		IdleSeconds: int(idle.Seconds()),
		PID:         os.Getpid(),
	}
}
