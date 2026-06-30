package ui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The composer (m.input) draws its cursor from its own focus flag, while m.focus
// is the source of truth for which pane is active. These two must never drift:
// a stale "focused" composer leaves a cursor blinking in the messages pane, and
// a stale "blurred" one leaves the editor dark while you type. syncComposerFocus
// (run on every Update) is the guarantee; these tests pin it down for the paths
// that switch focus — `matterbox open`, permalink jumps, tab switches and the
// i/esc round trip — plus the pure invariant across every focus value.

// composingModel is navModel left mid-compose: focus on the input with the
// editor actually focused, exactly the state a stray focus change must clean up.
func composingModel() Model {
	m := navModel()
	m.focus = focusInput
	m.input.Focus()
	m.input.SetValue("half-written draft")
	return m
}

// asModel runs one message through the real Update entry point (so the
// syncComposerFocus net fires) and returns the resulting Model.
func asModel(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	out, _ := m.Update(msg)
	got, ok := out.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", out)
	}
	return got
}

// assertComposerSynced checks the load-bearing invariant: the editor is focused
// exactly when m.focus is focusInput.
func assertComposerSynced(t *testing.T, m Model, where string) {
	t.Helper()
	wantFocused := m.focus == focusInput
	if m.input.Focused() != wantFocused {
		t.Fatalf("%s: focus=%v but input.Focused()=%v (want %v) — composer cursor out of sync",
			where, m.focus, m.input.Focused(), wantFocused)
	}
}

// TestSyncComposerFocusInvariant is the exhaustive core: for every focus value,
// starting from either editor state, the reconcile must end with the cursor
// shown iff we're in the composer. This is what makes "add a new focus" safe.
func TestSyncComposerFocusInvariant(t *testing.T) {
	for f := focus(0); f < numFocus; f++ {
		for _, startFocused := range []bool{false, true} {
			m := navModel()
			m.focus = f
			if startFocused {
				m.input.Focus()
			} else {
				m.input.Blur()
			}
			m.syncComposerFocus()
			want := f == focusInput
			if m.input.Focused() != want {
				t.Errorf("focus=%v startFocused=%v: input.Focused()=%v, want %v",
					f, startFocused, m.input.Focused(), want)
			}
		}
	}
}

// TestExternalOpenBlursComposer: `matterbox open <id>` arrives as an async
// openChannelRequestMsg while you may be mid-compose. It switches to the
// messages pane, so the cursor must leave the composer with it.
func TestExternalOpenBlursComposer(t *testing.T) {
	m := composingModel()
	got := asModel(t, m, openChannelRequestMsg{channelID: "c2"})
	if got.focus != focusMessages {
		t.Fatalf("matterbox open: focus=%v, want focusMessages", got.focus)
	}
	assertComposerSynced(t, got, "after matterbox open")
	if got.input.Focused() {
		t.Fatal("matterbox open left the composer cursor visible in the messages pane")
	}
}

// TestExternalOpenUnknownChannelStillSyncs: an open for a channel we don't have
// leaves focus untouched but must still reconcile the cursor — proof the net
// heals a desync on *any* event, not just the ones that move focus.
func TestExternalOpenUnknownChannelStillSyncs(t *testing.T) {
	m := navModel()
	m.focus = focusMessages
	m.input.Focus() // simulate a stale cursor left by some earlier path
	got := asModel(t, m, openChannelRequestMsg{channelID: "does-not-exist"})
	if got.focus != focusMessages {
		t.Fatalf("unknown open changed focus to %v, want focusMessages", got.focus)
	}
	if got.input.Focused() {
		t.Fatal("net failed to heal a stale composer cursor on an inert event")
	}
}

// TestPermalinkJumpBlursComposer: resolving a permalink to a post in another
// channel lands you in that channel's messages pane (openChannelAtPost). The
// composer must blur even though that handler only sets m.focus.
func TestPermalinkJumpBlursComposer(t *testing.T) {
	m := composingModel()
	// c2 lives in t1, "p1" isn't loaded and there's no store, so this drives the
	// cross-channel openChannelAtPost path that only assigns m.focus.
	got := asModel(t, m, permalinkResolvedMsg{postID: "p1", channelID: "c2", fallbackURL: "https://x/y/pl/p1"})
	if got.focus != focusMessages {
		t.Fatalf("permalink jump: focus=%v, want focusMessages", got.focus)
	}
	assertComposerSynced(t, got, "after permalink jump")
	if got.input.Focused() {
		t.Fatal("permalink jump left the composer cursor visible")
	}
}

// TestComposeLeaveRoundTripThroughUpdate walks the everyday keyboard path: i to
// start composing (cursor on), esc to leave (cursor off), each through Update so
// the net is in the loop.
func TestComposeLeaveRoundTripThroughUpdate(t *testing.T) {
	m := navModel() // focusMessages, composer blurred

	m = asModel(t, m, keyPress('i'))
	if m.focus != focusInput {
		t.Fatalf("i: focus=%v, want focusInput", m.focus)
	}
	if !m.input.Focused() {
		t.Fatal("i: composer not focused — no cursor where the user is typing")
	}
	assertComposerSynced(t, m, "after i")

	m = asModel(t, m, keyPress(tea.KeyEscape))
	if m.focus != focusMessages {
		t.Fatalf("esc: focus=%v, want focusMessages", m.focus)
	}
	if m.input.Focused() {
		t.Fatal("esc: composer still focused — cursor lingers after leaving the editor")
	}
	assertComposerSynced(t, m, "after esc")
}

// TestFeedSwitchBlursComposer: the realistic flow is leave-then-switch (the
// composer swallows alt+u while focused). After esc + alt+u we must be on the
// Feed with no composer cursor.
func TestFeedSwitchBlursComposer(t *testing.T) {
	m := composingModel()
	m = asModel(t, m, keyPress(tea.KeyEscape)) // back to messages
	m = asModel(t, m, altKey('u'))             // Feed
	if m.focus != focusFeed {
		t.Fatalf("alt+u: focus=%v, want focusFeed", m.focus)
	}
	if m.input.Focused() {
		t.Fatal("switching to the Feed left the composer cursor visible")
	}
	assertComposerSynced(t, m, "after alt+u to Feed")
}

// TestSearchTabBlursComposer guards the openSearchTab path directly: it sets
// focusSearch and must leave the composer dark (the Search tab has no composer).
func TestSearchTabBlursComposer(t *testing.T) {
	m := composingModel()
	m.openSearchTab()
	m.syncComposerFocus() // the Update net; called explicitly for the unit path
	if m.focus != focusSearch {
		t.Fatalf("openSearchTab: focus=%v, want focusSearch", m.focus)
	}
	if m.input.Focused() {
		t.Fatal("Search tab left the composer cursor visible")
	}
}

// TestSQLTabBlursComposer guards the openSQLTab path: focusSQL, composer dark,
// and the SQL editor (a separate input) takes over.
func TestSQLTabBlursComposer(t *testing.T) {
	m := composingModel()
	m.openSQLTab()
	m.syncComposerFocus()
	if m.focus != focusSQL {
		t.Fatalf("openSQLTab: focus=%v, want focusSQL", m.focus)
	}
	if m.input.Focused() {
		t.Fatal("SQL tab left the composer cursor visible")
	}
	if !m.sql.input.Focused() {
		t.Fatal("SQL tab didn't focus its own editor")
	}
}

// TestWindowResizeHealsStaleCursor: a resize touches no focus state, yet a model
// that was already desynced must come out reconciled — the net runs after every
// message, not just focus-changing ones.
func TestWindowResizeHealsStaleCursor(t *testing.T) {
	m := navModel()
	m.focus = focusMessages
	m.input.Focus() // stale
	got := asModel(t, m, tea.WindowSizeMsg{Width: 120, Height: 50})
	if got.input.Focused() {
		t.Fatal("a resize failed to heal a stale composer cursor")
	}
}

// TestComposerCursorTracksFocusSync ties the focus net to the native terminal
// cursor: composerCursor (which positions the real cursor) only fires when both
// m.focus is focusInput *and* the editor reports a visible caret. A desync —
// focus moved into the composer but the editor never focused — makes the cursor
// silently vanish ("visibility is sometimes off"); the net repairs it. The
// reverse (focus on messages) must never show a composer cursor.
func TestComposerCursorTracksFocusSync(t *testing.T) {
	m := navModel()
	m.vcache = &viewCache{bodyH: 30} // composerGeom needs a rendered body height
	m.input.SetWidth(40)

	// Reading the transcript: no terminal cursor parked in the composer.
	m.focus = focusMessages
	m.input.Blur()
	if _, _, ok := m.composerCursor(); ok {
		t.Fatal("composer cursor placed while focus is on the messages pane")
	}

	// Composing but desynced (focus moved in, editor never focused): the cursor
	// would silently disappear. Confirm the gap, then heal it.
	m.focus = focusInput
	m.input.Blur()
	if _, _, ok := m.composerCursor(); ok {
		t.Fatal("precondition: expected no cursor while the editor focus is stale")
	}
	m.syncComposerFocus()
	if _, _, ok := m.composerCursor(); !ok {
		t.Fatal("after sync: no terminal cursor while composing — composer looks dark")
	}
}

// fgSeq returns the SGR prefix lipgloss emits for a foreground colour, so a test
// can spot which colour painted a given cell without hard-coding the escape.
func fgSeq(c color.Color) string {
	r := lipgloss.NewStyle().Foreground(c).Render("@")
	if i := strings.IndexByte(r, '@'); i >= 0 {
		return r[:i]
	}
	return ""
}

// TestMessagesPaneVisiblyBlursWhenComposing pins Fix A: focusing the editor must
// visibly dim the messages pane frame (the bright frame is reserved for when the
// message list itself is the active reading target). The composer's own rule
// lights up instead, so the two states are never both bright.
func TestMessagesPaneVisiblyBlursWhenComposing(t *testing.T) {
	focusedSeq := fgSeq(focusedColor)
	dimSeq := fgSeq(dimColor)

	reading := navModel()
	reading.focus = focusMessages
	readingPane := reading.renderMessagesPane(38, 80)

	composing := navModel()
	composing.focus = focusInput
	composing.input.Focus()
	composingPane := composing.renderMessagesPane(38, 80)

	if readingPane == composingPane {
		t.Fatal("messages pane looks identical reading vs composing — no visible blur")
	}

	// The title row's frame carries the pane's focus colour (titleStyle adds no
	// colour of its own), so it's a clean probe of the outer border.
	readTitle := firstLine(readingPane)
	composeTitle := firstLine(composingPane)

	if !strings.Contains(readTitle, focusedSeq) {
		t.Errorf("reading: messages frame not highlighted (no focusedColor on the title row)")
	}
	if strings.Contains(composeTitle, focusedSeq) {
		t.Errorf("composing: messages frame still highlighted — it should blur")
	}
	if !strings.Contains(composeTitle, dimSeq) {
		t.Errorf("composing: messages frame not dimmed")
	}
}
