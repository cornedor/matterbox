package ui

import (
	"testing"
	"time"
)

// newMarkReadModel builds a minimal Model with one unread+mentioned channel
// open, enough to exercise the dwell-based mark-read reducer without any
// viewport/render setup.
func newMarkReadModel(delay time.Duration) Model {
	return Model{
		openChannelID: "chan1",
		viewGen:       1,
		markReadDelay: delay,
		unread:        map[string]int{"chan1": 3},
		mentions:      map[string]int{"chan1": 1},
	}
}

func isUnread(m Model, channelID string) bool {
	_, u := m.unread[channelID]
	_, n := m.mentions[channelID]
	return u || n
}

// A markViewedMsg whose generation and channel still match the open channel
// clears the badges and marks the view settled.
func TestMarkViewedMsgClearsWhenCurrent(t *testing.T) {
	m := newMarkReadModel(5 * time.Second)
	next, _ := m.update(markViewedMsg{channelID: "chan1", gen: 1})
	nm := next.(Model)
	if isUnread(nm, "chan1") {
		t.Fatalf("expected chan1 badges cleared after matching dwell, still unread")
	}
	if !nm.viewSettled {
		t.Fatalf("expected viewSettled=true after the dwell elapsed")
	}
}

// A stale generation (the user switched away and back, or opened another
// channel) must be ignored so it doesn't clear unread early.
func TestMarkViewedMsgStaleGenIsNoop(t *testing.T) {
	m := newMarkReadModel(5 * time.Second)
	m.viewGen = 2 // a newer focus session has begun
	next, _ := m.update(markViewedMsg{channelID: "chan1", gen: 1})
	nm := next.(Model)
	if !isUnread(nm, "chan1") {
		t.Fatalf("expected chan1 to stay unread for a stale-generation tick")
	}
	if nm.viewSettled {
		t.Fatalf("stale tick must not mark the view settled")
	}
}

// A tick for a channel that is no longer the open one is ignored.
func TestMarkViewedMsgDifferentChannelIsNoop(t *testing.T) {
	m := newMarkReadModel(5 * time.Second)
	next, _ := m.update(markViewedMsg{channelID: "other", gen: 1})
	nm := next.(Model)
	if !isUnread(nm, "chan1") {
		t.Fatalf("expected chan1 to stay unread when the tick targets another channel")
	}
}

// With a zero delay configured, scheduleMarkViewed clears the badges
// immediately and returns the mark-read command (the original behaviour).
func TestScheduleMarkViewedImmediate(t *testing.T) {
	m := newMarkReadModel(0)
	cmd := m.scheduleMarkViewed("chan1")
	if isUnread(m, "chan1") {
		t.Fatalf("zero delay should clear badges immediately")
	}
	if !m.viewSettled {
		t.Fatalf("zero delay should mark the view settled immediately")
	}
	// m.me is nil so markChannelViewed returns nil; the immediate path still
	// returns that (nil) command — the badge clear is the observable effect.
	_ = cmd
}

// With a positive delay, scheduleMarkViewed leaves the badges intact (the
// peek hasn't lasted long enough yet) and returns a non-nil tick command.
func TestScheduleMarkViewedDefersBadge(t *testing.T) {
	m := newMarkReadModel(5 * time.Second)
	cmd := m.scheduleMarkViewed("chan1")
	if !isUnread(m, "chan1") {
		t.Fatalf("a positive delay must hold the unread badge until the dwell elapses")
	}
	if m.viewSettled {
		t.Fatalf("the view must not be settled before the dwell tick fires")
	}
	if cmd == nil {
		t.Fatalf("expected a tick command to be scheduled")
	}
}
