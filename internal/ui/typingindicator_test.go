package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// typingEvent builds a `typing` WebSocket event for the given channel and
// user, mirroring how Mattermost broadcasts them (channel on the
// broadcast, typer in the data payload).
func typingEvent(channelID, userID string) *model.WebSocketEvent {
	ev := model.NewWebSocketEvent(model.WebsocketEventTyping, "", channelID, "", nil, "")
	ev.Add("user_id", userID)
	return ev
}

// A typing event for the open channel from someone else records the typer
// and arms the animation.
func TestApplyTypingEventRecordsOther(t *testing.T) {
	m := Model{openChannelID: "chan1", me: &model.User{Id: "me"}}
	cmd := m.applyTypingEvent(typingEvent("chan1", "alice"))
	if cmd == nil {
		t.Fatal("expected an animation tick cmd to be armed")
	}
	if !m.typingIndicatorVisible() {
		t.Fatal("indicator should be visible after a typing event for the open channel")
	}
	if _, ok := m.typingIndicator.users["alice"]; !ok {
		t.Fatal("alice should be recorded as typing")
	}
	if !m.typingIndicator.animActive {
		t.Fatal("animation loop should be marked active")
	}
}

// Typing events for other channels, and our own typing, are ignored so
// the cue only ever describes the composer on screen.
func TestApplyTypingEventIgnoresNoise(t *testing.T) {
	m := Model{openChannelID: "chan1", me: &model.User{Id: "me"}}

	if cmd := m.applyTypingEvent(typingEvent("chan2", "alice")); cmd != nil {
		t.Fatal("typing in another channel should be ignored")
	}
	if cmd := m.applyTypingEvent(typingEvent("chan1", "me")); cmd != nil {
		t.Fatal("our own typing should be ignored")
	}
	if m.typingIndicatorVisible() {
		t.Fatal("indicator should not be visible")
	}
}

// A second event from the same run must not stack another tick loop.
func TestMaybeStartTypingAnimIdempotent(t *testing.T) {
	m := Model{openChannelID: "chan1", me: &model.User{Id: "me"}}
	m.applyTypingEvent(typingEvent("chan1", "alice"))
	if cmd := m.maybeStartTypingAnim(); cmd != nil {
		t.Fatal("a second start while active must not arm another tick")
	}
}

// Switching channels mid-event resets the tracked set rather than mixing
// typers across channels.
func TestNoteTypingResetsOnChannelChange(t *testing.T) {
	now := time.Now()
	m := Model{openChannelID: "chan2"}
	m.noteTyping("chan1", "alice", now)
	m.noteTyping("chan2", "bob", now)
	if _, ok := m.typingIndicator.users["alice"]; ok {
		t.Fatal("alice (old channel) should have been cleared on channel change")
	}
	if _, ok := m.typingIndicator.users["bob"]; !ok {
		t.Fatal("bob should be recorded for the new channel")
	}
	if m.typingIndicator.channelID != "chan2" {
		t.Fatalf("tracked channel = %q, want chan2", m.typingIndicator.channelID)
	}
}

// pruneTyping drops expired entries and keeps live ones.
func TestPruneTyping(t *testing.T) {
	now := time.Now()
	m := Model{openChannelID: "chan1"}
	m.noteTyping("chan1", "alice", now.Add(-typingIndicatorTTL)) // already expired
	m.noteTyping("chan1", "bob", now)                            // fresh
	m.pruneTyping(now)
	if _, ok := m.typingIndicator.users["alice"]; ok {
		t.Fatal("expired alice should be pruned")
	}
	if _, ok := m.typingIndicator.users["bob"]; !ok {
		t.Fatal("fresh bob should survive")
	}
}

// The tick stops the loop once every typer has expired, and the cue then
// reports invisible.
func TestTypingTickStopsWhenAllExpired(t *testing.T) {
	m := Model{openChannelID: "chan1"}
	m.noteTyping("chan1", "alice", time.Now().Add(-typingIndicatorTTL))
	m.typingIndicator.animActive = true
	if cmd := m.applyTypingIndicatorTick(); cmd != nil {
		t.Fatal("tick should not reschedule once everyone has stopped typing")
	}
	if m.typingIndicator.animActive {
		t.Fatal("animation loop should be marked inactive")
	}
	if m.typingIndicatorVisible() {
		t.Fatal("indicator should be hidden after the last typer expires")
	}
}

// The tick stops and clears when the tracked channel is no longer open,
// so switching channels kills the loop promptly instead of waiting for
// the TTL.
func TestTypingTickStopsOnChannelSwitch(t *testing.T) {
	m := Model{openChannelID: "chan1"}
	m.noteTyping("chan1", "alice", time.Now())
	m.typingIndicator.animActive = true

	m.openChannelID = "chan2" // user switched away
	if cmd := m.applyTypingIndicatorTick(); cmd != nil {
		t.Fatal("tick should not reschedule for a channel that's no longer open")
	}
	if m.typingIndicator.animActive {
		t.Fatal("loop should stop after switching channels")
	}
	if m.typingIndicatorVisible() {
		t.Fatal("indicator from the old channel should not show")
	}
}

// A live typer keeps the loop running, advancing the frame.
func TestTypingTickAdvancesFrame(t *testing.T) {
	m := Model{openChannelID: "chan1"}
	m.noteTyping("chan1", "alice", time.Now())
	m.typingIndicator.animActive = true
	if cmd := m.applyTypingIndicatorTick(); cmd == nil {
		t.Fatal("tick should reschedule while someone is still typing")
	}
	if m.typingIndicator.phase != 1 {
		t.Fatalf("phase = %d, want 1", m.typingIndicator.phase)
	}
}

// renderTypingDots always emits exactly typingDotCount single-width dots
// (the sidebar presence glyph), and sweeps the lit one across positions
// before a rest frame.
func TestRenderTypingDots(t *testing.T) {
	for phase := 0; phase < 8; phase++ {
		got := renderTypingDots(phase)
		if n := strings.Count(got, statusDot); n != typingDotCount {
			t.Fatalf("phase %d: %d dots, want %d", phase, n, typingDotCount)
		}
		if w := lipgloss.Width(got); w != typingDotCount {
			t.Fatalf("phase %d: display width %d, want %d", phase, w, typingDotCount)
		}
	}
}

// overlayTypingDots preserves the full rule width (so the layout never
// shifts), insets the dots by typingDotOffset, and falls back to a plain
// rule when the pane is too narrow.
func TestOverlayTypingDotsWidth(t *testing.T) {
	for _, width := range []int{0, 2, typingDotOffset + typingDotCount, 10, 40} {
		got := overlayTypingDots(0, width, dimColor)
		if w := lipgloss.Width(got); w != width {
			t.Fatalf("width %d: rendered display width %d", width, w)
		}
	}
	// Wide enough: the dots are present, inset by typingDotOffset columns
	// of rule (──•••…).
	wide := overlayTypingDots(0, 40, dimColor)
	if strings.Count(wide, statusDot) != typingDotCount {
		t.Fatal("wide rule should carry the dots")
	}
	if idx := strings.Index(wide, statusDot); idx < 0 {
		t.Fatal("expected a dot in the wide rule")
	} else if lead := lipgloss.Width(wide[:idx]); lead != typingDotOffset {
		t.Fatalf("dots inset by %d columns, want %d", lead, typingDotOffset)
	}
	// Too narrow: plain rule, no dots crowding the prompt.
	narrow := overlayTypingDots(0, typingDotOffset+typingDotCount-1, dimColor)
	if strings.Contains(narrow, statusDot) {
		t.Fatal("narrow rule should drop the dots")
	}
}
