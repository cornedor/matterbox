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

func TestCommandHintGhost(t *testing.T) {
	cases := []struct {
		text string
		want string // ghost set on the editor after updateCommandHighlight
	}{
		{"/shrug", "[message]"},             // recognised, no args yet → show the hint
		{"/shrug ", "[message]"},            // trailing space (post-accept) still shows
		{"/shrug hello", ""},                // the only arg is being typed → hint gone
		{"/dm", "@user[,@user…] [message]"}, // a multi-arg hint, untouched
		{"/dm @bob", "[message]"},           // first arg supplied → only the rest trails
		{"/dm @bob ", "[message]"},          // …with or without a trailing space
		{"/dm @bob hi", ""},                 // last arg being typed → nothing left
		{"/help", ""},                       // recognised but advertises no args hint
		{"/zzznope", ""},                    // unrecognised → no hint
		{"/", ""},                           // bare slash
		{"/shrug\n", ""},                    // wrapped to a second line → committed
	}
	for _, tc := range cases {
		m := newSlashTestModel(tc.text)
		m.updateCommandHighlight()
		if _, _, ok := m.input.CommandSpan(); !ok {
			// No command span means no ghost should be set either.
			if tc.want != "" {
				t.Errorf("%q: no command span, want hint %q", tc.text, tc.want)
			}
			continue
		}
		got := m.commandHintAfter(spanEnd(m))
		if got != tc.want {
			t.Errorf("commandHintAfter(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// spanEnd returns the editor's current command-span end offset.
func spanEnd(m *Model) int {
	_, e, _ := m.input.CommandSpan()
	return e
}

// TestRemainingHint exercises the progressive trimming directly, including the
// quote-aware tokenising and the repeating final slot that /poll relies on.
func TestRemainingHint(t *testing.T) {
	const poll = `"[Question]" "[Answer 1]" "[Answer 2]"...`
	cases := []struct{ hint, typed, want string }{
		{poll, ``, `"[Question]" "[Answer 1]" "[Answer 2]"...`},      // nothing typed → full template
		{poll, ` "Wh`, `"[Answer 1]" "[Answer 2]"...`},               // mid-question → answers next
		{poll, ` "What for lunch?"`, `"[Answer 1]" "[Answer 2]"...`}, // a quoted, spaced question is one slot
		{poll, ` "What" `, `"[Answer 1]" "[Answer 2]"...`},           // question done
		{poll, ` "What" "Pizza"`, `"[Answer 2]"...`},                 // one answer in
		{poll, ` "What" "Pizza" "Sushi"`, `"[Answer 2]"...`},         // repeating slot keeps offering itself
		{`[message]`, ``, `[message]`},
		{`[message]`, ` hi`, ``}, // the lone free-text slot vanishes as it's typed
		{`@user[,@user…] [message]`, ` @bob`, `[message]`},
		{`@user[,@user…] [message]`, ` @bob hi`, ``},
		{``, ` x`, ``}, // no hint → nothing to trail
	}
	for _, tc := range cases {
		if got := remainingHint(tc.hint, tc.typed); got != tc.want {
			t.Errorf("remainingHint(%q, %q) = %q, want %q", tc.hint, tc.typed, got, tc.want)
		}
	}
}

// TestCommandHintGhostServerPoll checks the progressive hint end-to-end for a
// server-advertised command (poll), the case the user cares about.
func TestCommandHintGhostServerPoll(t *testing.T) {
	const pollHint = `"[Question]" "[Answer 1]" "[Answer 2]"...`
	cases := []struct{ text, want string }{
		{`/poll`, pollHint},
		{`/poll "What for lunch?"`, `"[Answer 1]" "[Answer 2]"...`},
		{`/poll "What for lunch?" "Pizza"`, `"[Answer 2]"...`},
	}
	for _, tc := range cases {
		m := newSlashTestModel(tc.text)
		// commandTeamID resolves to "" for the bare test model, so cache poll there.
		m.serverCmds[""] = []serverCommand{{trigger: "poll", desc: "create a poll", hint: pollHint}}
		m.updateCommandHighlight()
		if _, _, ok := m.input.CommandSpan(); !ok {
			t.Fatalf("%q: poll should be recognised", tc.text)
		}
		if got := m.commandHintAfter(spanEnd(m)); got != tc.want {
			t.Errorf("%q: ghost = %q, want %q", tc.text, got, tc.want)
		}
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
