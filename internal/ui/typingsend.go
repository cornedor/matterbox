package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// typingSendInterval throttles how often we announce our own typing to the
// server. Mattermost has no "stopped typing" event: a client re-emits
// `user_typing` while keys are pressed and receivers expire the cue on a
// TTL (see typingIndicatorTTL, 6s). So this interval must be comfortably
// under that TTL — otherwise a steady typist's cue would flicker off on the
// far side between our re-emits — while staying above the server's 1s floor
// (TimeBetweenUserTypingUpdatesMilliseconds, min 1000ms). 4s sits between
// the two with margin and tracks the webapp's 5s default re-emit cadence.
const typingSendInterval = 4 * time.Second

// maybeSendTyping announces that we're typing into the active composer
// target, throttled to one `user_typing` request per typingSendInterval.
// It's called after every composer keystroke that changed the draft, so the
// throttle — not the keystroke rate — sets the on-the-wire cadence.
//
// The target mirrors Send: a reply in an open thread carries that thread's
// channel and root post id (so the cue lands on the thread), otherwise it's
// the open channel with no parent. Returns nil (no-op) when we're not
// connected, have no channel to type into, or the throttle hasn't elapsed.
func (m *Model) maybeSendTyping(now time.Time) tea.Cmd {
	if m.ws == nil {
		return nil
	}
	var channelID, parentID string
	if m.threadOpen {
		channelID, parentID = m.threadChannelID, m.threadRootID
	} else {
		channelID = m.openChannelID
	}
	if channelID == "" {
		return nil
	}
	if now.Before(m.nextTypingSend) {
		return nil
	}
	m.nextTypingSend = now.Add(typingSendInterval)

	// UserTyping only enqueues onto the socket's write channel, but keep it
	// off the Update goroutine so a stalled writer can never block the UI.
	ws := m.ws
	return func() tea.Msg {
		ws.UserTyping(channelID, parentID)
		return nil
	}
}
