package ui

import (
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// A composer edit while connected to a channel announces typing and arms
// the throttle so the next announce is held off until the interval elapses.
func TestMaybeSendTypingThrottles(t *testing.T) {
	m := Model{openChannelID: "chan1", ws: &model.WebSocketClient{}}

	t0 := time.Unix(0, 0)
	if cmd := m.maybeSendTyping(t0); cmd == nil {
		t.Fatal("first edit should announce typing")
	}
	if !m.nextTypingSend.Equal(t0.Add(typingSendInterval)) {
		t.Fatalf("throttle should advance to t0+interval, got %v", m.nextTypingSend)
	}
	// Still inside the window: suppressed.
	if cmd := m.maybeSendTyping(t0.Add(typingSendInterval - time.Millisecond)); cmd != nil {
		t.Fatal("a second edit inside the interval should be throttled")
	}
	// Window elapsed: announces again.
	if cmd := m.maybeSendTyping(t0.Add(typingSendInterval)); cmd == nil {
		t.Fatal("an edit after the interval should announce again")
	}
}

// Without a live socket or an open channel there's nobody to tell, so the
// announce is a no-op and the throttle stays untouched.
func TestMaybeSendTypingNoTarget(t *testing.T) {
	now := time.Unix(0, 0)

	noWS := Model{openChannelID: "chan1"}
	if cmd := noWS.maybeSendTyping(now); cmd != nil {
		t.Fatal("no websocket: should not announce")
	}
	if !noWS.nextTypingSend.IsZero() {
		t.Fatal("no websocket: throttle should be untouched")
	}

	noChan := Model{ws: &model.WebSocketClient{}}
	if cmd := noChan.maybeSendTyping(now); cmd != nil {
		t.Fatal("no open channel: should not announce")
	}
	if !noChan.nextTypingSend.IsZero() {
		t.Fatal("no open channel: throttle should be untouched")
	}
}

// A reply in an open thread targets the thread's channel and root post, not
// the channel selected in the sidebar — mirroring where the reply will land.
func TestMaybeSendTypingThreadTarget(t *testing.T) {
	m := Model{
		openChannelID:   "sidebar-chan",
		threadOpen:      true,
		threadChannelID: "thread-chan",
		threadRootID:    "root1",
		ws:              &model.WebSocketClient{},
	}
	if cmd := m.maybeSendTyping(time.Unix(0, 0)); cmd == nil {
		t.Fatal("typing into an open thread should announce")
	}
	// The throttle advancing confirms the target was accepted (a thread with
	// a non-empty channel), rather than being dropped as "no channel".
	if m.nextTypingSend.IsZero() {
		t.Fatal("thread target should have armed the throttle")
	}
}
