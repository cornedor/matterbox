package ui

import "testing"

func TestRecognisedCommandSpan(t *testing.T) {
	cases := []struct {
		text      string
		wantOK    bool
		wantStart int
		wantEnd   int
	}{
		{"/me", true, 0, 3},         // built-in
		{"/me waves", true, 0, 3},   // only the trigger token, not the args
		{"/dm @a hi", true, 0, 3},   // built-in with args
		{"/MSG @a hi", true, 0, 4},  // alias, case-insensitive (msg → dm)
		{"/help\nmore", true, 0, 5}, // command on line 0, args on line 1
		{"/m", false, 0, 0},         // a prefix is not (yet) a known command
		{"/zzznope", false, 0, 0},   // matches nothing
		{"/", false, 0, 0},          // bare slash
		{"/ foo", false, 0, 0},      // space right after "/" → empty word
		{"hello /me", false, 0, 0},  // "/" not at line start
		{"", false, 0, 0},           // empty composer
		{"just text", false, 0, 0},  // no leading slash
	}
	for _, tc := range cases {
		m := newSlashTestModel(tc.text)
		start, end, ok := m.recognisedCommandSpan()
		if ok != tc.wantOK || (ok && (start != tc.wantStart || end != tc.wantEnd)) {
			t.Errorf("recognisedCommandSpan(%q) = (%d,%d,%v), want (%d,%d,%v)",
				tc.text, start, end, ok, tc.wantStart, tc.wantEnd, tc.wantOK)
		}
	}
}

func TestRecognisedCommandSpanSuppressedWhileEditing(t *testing.T) {
	// A leading "/" in an edited post is literal text — never a command.
	m := newSlashTestModel("/me")
	m.editingPostID = "post123"
	if _, _, ok := m.recognisedCommandSpan(); ok {
		t.Error("recognisedCommandSpan should be false while editing a post")
	}
}

func TestUpdateCommandHighlight(t *testing.T) {
	// A recognised command marks the editor span and arms the animation loop.
	m := newSlashTestModel("/me waves")
	cmd := m.updateCommandHighlight()
	if s, e, ok := m.input.CommandSpan(); !ok || s != 0 || e != 3 {
		t.Fatalf("editor span = (%d,%d,%v), want (0,3,true)", s, e, ok)
	}
	if !m.cmdShimmer.active {
		t.Error("shimmer loop should be active for a recognised command")
	}
	if cmd == nil {
		t.Error("first recognised command should return a tick Cmd to start the loop")
	}
	// Idempotent: an already-running loop is not restarted.
	if cmd := m.updateCommandHighlight(); cmd != nil {
		t.Error("a second call should not start a second loop")
	}

	// Typing past recognition clears the editor span (the running loop stops
	// itself on its next tick, so active is left as-is here).
	m.input.SetValue("/zzznope")
	m.updateCommandHighlight()
	if _, _, ok := m.input.CommandSpan(); ok {
		t.Error("an unrecognised command should clear the editor span")
	}
}

func TestApplyCmdShimmerTickStops(t *testing.T) {
	m := newSlashTestModel("/me")
	m.focus = focusInput
	m.updateCommandHighlight()
	if !m.cmdShimmer.active {
		t.Fatal("precondition: loop should be active")
	}
	// With the span gone, the next tick stops the loop.
	m.input.ClearCommandSpan()
	if cmd := m.applyCmdShimmerTick(); cmd != nil {
		t.Error("tick should return nil when the command span is gone")
	}
	if m.cmdShimmer.active {
		t.Error("loop should have stopped once the span is gone")
	}

	// It also stops when focus leaves the composer.
	m.input.SetValue("/me")
	m.updateCommandHighlight()
	m.focus = focusMessages
	if cmd := m.applyCmdShimmerTick(); cmd != nil {
		t.Error("tick should return nil when the input is unfocused")
	}
	if m.cmdShimmer.active {
		t.Error("loop should have stopped once focus left the input")
	}
}
