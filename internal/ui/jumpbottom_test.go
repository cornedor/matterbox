package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// jumpModel is mouseModel laid out for real (panes sized from the terminal
// dimensions), scrolled to the top of a long channel so the pill is showing.
func jumpModel(t *testing.T) Model {
	t.Helper()
	m := mouseModel(shortPosts(80))
	m.resizeMessagesViewport()
	m.postIdx = 0
	m.renderMessages()
	if !m.msgsScrolledUp() {
		t.Fatal("setup: expected the transcript to be scrolled up")
	}
	m.viewContent() // arms vcache.jumpZone, as the real View path does
	return m
}

// TestJumpPillShowsOnlyWhenScrolledUp: the pill appears in the rendered frame
// while there's content below the fold and vanishes once the newest message is
// on screen. The label carries the bottom action's configured key.
func TestJumpPillShowsOnlyWhenScrolledUp(t *testing.T) {
	m := jumpModel(t)
	want := "Jump to bottom (" + m.keys.End.Help().Key + ")"

	m.vcache.viewValid = false
	if frame := ansi.Strip(m.viewContent()); !strings.Contains(frame, want) {
		t.Fatalf("scrolled up: frame missing %q", want)
	}

	m.selectLastMessage()
	m.renderMessages()
	if m.msgsScrolledUp() {
		t.Fatal("at bottom: msgsScrolledUp still true")
	}
	m.vcache.viewValid = false
	if frame := ansi.Strip(m.viewContent()); strings.Contains(frame, want) {
		t.Errorf("at bottom: frame still shows the pill")
	}
	if m.vcache.jumpZone.active {
		t.Errorf("at bottom: jumpZone left armed")
	}
}

// TestJumpPillZoneMatchesViewport: the recorded screen rect sits on the message
// viewport's last row, inside its content columns — the cells the overlay
// actually painted.
func TestJumpPillZoneMatchesViewport(t *testing.T) {
	m := jumpModel(t)
	z := m.vcache.jumpZone
	if !z.active {
		t.Fatal("jumpZone not armed")
	}
	x0, top, w, h, _ := m.messagesGeom()
	if wantY := top + h - 1; z.y != wantY {
		t.Errorf("pill row = %d, want the viewport's last row %d", z.y, wantY)
	}
	if z.x0 < x0 || z.x1 > x0+w {
		t.Errorf("pill spans [%d,%d), outside the viewport's content columns [%d,%d)", z.x0, z.x1, x0, x0+w)
	}
	if got := z.x1 - z.x0; got != lipgloss.Width(m.jumpBottomText()) {
		t.Errorf("pill width = %d, want %d", got, lipgloss.Width(m.jumpBottomText()))
	}
}

// TestJumpPillHitAndHover: the pill's cells resolve to hitJumpBottom for both a
// click and the (cheaper) hover path, and win over the message row underneath.
// One cell to either side falls through to that message.
func TestJumpPillHitAndHover(t *testing.T) {
	m := jumpModel(t)
	z := m.vcache.jumpZone
	mid := (z.x0 + z.x1) / 2

	for _, x := range []int{z.x0, mid, z.x1 - 1} {
		if h := m.hitTest(x, z.y); h.zone != hitJumpBottom {
			t.Errorf("hitTest(%d,%d).zone = %v, want hitJumpBottom", x, z.y, h.zone)
		}
		if hv := m.hoverAt(x, z.y); hv.zone != hitJumpBottom {
			t.Errorf("hoverAt(%d,%d).zone = %v, want hitJumpBottom", x, z.y, hv.zone)
		}
	}
	for _, x := range []int{z.x0 - 1, z.x1} {
		if h := m.hitTest(x, z.y); h.zone == hitJumpBottom {
			t.Errorf("hitTest(%d,%d) hit the pill outside its columns", x, z.y)
		}
	}
	if h := m.hitTest(mid, z.y-1); h.zone == hitJumpBottom {
		t.Errorf("hitTest one row above the pill hit it")
	}
}

// TestJumpPillHoverRepaints: crossing into the pill changes the rendered frame
// (the hover style lands) even though the memoized upper box is otherwise
// unchanged — i.e. hover is in its fingerprint.
func TestJumpPillHoverRepaints(t *testing.T) {
	m := jumpModel(t)
	z := m.vcache.jumpZone

	m.vcache.viewValid = false
	rest := m.viewContent()

	out, _ := m.handleMouseMotion(motion(tea.MouseNone, (z.x0+z.x1)/2, z.y))
	hovered := out.(Model)
	if hovered.hover.zone != hitJumpBottom {
		t.Fatalf("hover zone = %v, want hitJumpBottom", hovered.hover.zone)
	}
	hovered.vcache.viewValid = false
	if hovered.viewContent() == rest {
		t.Error("hovered frame is byte-identical to the resting frame; the hover style never rendered")
	}
	// The pill's text is unchanged by hover — only its styling.
	if !strings.Contains(ansi.Strip(hovered.viewContent()), "Jump to bottom") {
		t.Error("hovered frame lost the pill label")
	}
}

// TestJumpPillClickJumps: clicking the pill lands on the newest message and
// leaves the composer focused — a click while typing must not steal the caret.
func TestJumpPillClickJumps(t *testing.T) {
	m := jumpModel(t)
	m.focus = focusInput
	m.msgScrollFree = true
	m.msgFreeOffset = 0
	m.renderMessages()
	z := m.vcache.jumpZone

	out, _ := m.handleMouseClick(click(tea.MouseLeft, (z.x0+z.x1)/2, z.y))
	got := out.(Model)

	if got.postIdx != len(got.posts)-1 {
		t.Errorf("postIdx = %d, want the newest post %d", got.postIdx, len(got.posts)-1)
	}
	if got.msgScrollFree {
		t.Error("click left the wheel free-scroll flag set")
	}
	if got.focus != focusInput {
		t.Errorf("focus = %v, want focusInput (the click must not steal the composer)", got.focus)
	}
	if got.msgsScrolledUp() {
		t.Error("still scrolled up after clicking jump-to-bottom")
	}
}

// TestOverlayJumpPillPreservesWidth: the overlay replaces cells rather than
// inserting them, so every row keeps its display width — a widened row would
// push the pane's right border (and the scrollbar) out of column.
func TestOverlayJumpPillPreservesWidth(t *testing.T) {
	const w = 40
	lines := []string{
		strings.Repeat("a", w),
		"\x1b[1;31m" + strings.Repeat("b", w) + "\x1b[0m", // styled row
		strings.Repeat("c", w),
	}
	p := jumpPill{active: true, col0: 10, text: " Jump to bottom (end/G) ↓ "}
	if lipgloss.Width(p.text) > w {
		t.Skip("label wider than the test row")
	}
	for _, view := range []string{
		strings.Join(lines, "\n"),
		strings.Join(lines[:1], "\n"), // single row
	} {
		got := overlayJumpPill(view, p)
		for i, line := range strings.Split(got, "\n") {
			if w2 := lipgloss.Width(line); w2 != w {
				t.Errorf("row %d width = %d, want %d", i, w2, w)
			}
		}
		if !strings.Contains(ansi.Strip(got), "Jump to bottom") {
			t.Error("overlay dropped the label")
		}
	}
}

// TestOverlayJumpPillShortRow: a row shorter than the pill's start column is
// padded out rather than letting the pill slide left.
func TestOverlayJumpPillShortRow(t *testing.T) {
	p := jumpPill{active: true, col0: 12, text: "[jump]"}
	got := overlayJumpPill("abc", p)
	stripped := ansi.Strip(got)
	if idx := strings.Index(stripped, "[jump]"); idx != p.col0 {
		t.Errorf("pill starts at column %d, want %d (row %q)", idx, p.col0, stripped)
	}
}

// TestJumpPillInactiveIsNoop: the zero pill leaves the viewport bytes alone.
func TestJumpPillInactiveIsNoop(t *testing.T) {
	view := "one\ntwo\nthree"
	if got := overlayJumpPill(view, jumpPill{}); got != view {
		t.Errorf("inactive pill mutated the view: %q", got)
	}
	if got := overlayJumpPill("", jumpPill{active: true, text: "x"}); got != "" {
		t.Errorf("empty view mutated: %q", got)
	}
}

// TestJumpPillHiddenWhenPaneUnsplit: on a terminal too short for renderMessagesPane
// to split its box, the viewport is clipped rather than laid out — so the pill's
// row may not be on screen at all. Draw no pill, and leave no clickable target
// behind pointing at a row the user can't see.
func TestJumpPillHiddenWhenPaneUnsplit(t *testing.T) {
	m := jumpModel(t)
	// A viewport taller than the pane's inner area forces the degenerate branch
	// (upperRows > innerH), the same shape a very short terminal produces.
	m.msgsView.SetHeight(m.bodyHeight() + 4)
	m.vcache.viewValid = false
	frame := ansi.Strip(m.viewContent())

	if strings.Contains(frame, "Jump to bottom") {
		t.Error("pill drawn into a clipped, unsplit pane")
	}
	if m.vcache.jumpZone.active {
		t.Error("clipped pane left a clickable target armed")
	}
}

// TestJumpPillHiddenWhenPaneTooNarrow: a pane that can't hold the label without
// crowding the text under it draws nothing rather than truncating.
func TestJumpPillHiddenWhenPaneTooNarrow(t *testing.T) {
	m := jumpModel(t)
	m.msgsView.SetWidth(lipgloss.Width(m.jumpBottomText()) + 1)
	if p := m.jumpPillFor(true); p.active {
		t.Error("pill drawn in a pane too narrow for it")
	}
	m.msgsView.SetWidth(lipgloss.Width(m.jumpBottomText()) + 2)
	if p := m.jumpPillFor(true); !p.active {
		t.Error("pill hidden in a pane wide enough for it")
	}
}
