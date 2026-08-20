package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/editor"
	"matterbox/internal/store"
)

func wheel(btn tea.MouseButton) tea.MouseWheelMsg {
	return tea.MouseWheelMsg(tea.Mouse{Button: btn})
}

// wheelOnce delivers one wheel event and applies the coalescing flush, so the
// move lands synchronously for assertions. handleMouseWheel now only accumulates
// the delta and arms a frame tick (wheelFlushMsg) to keep a trackpad's momentum
// flood from backing up the msg queue — see handleMouseWheel.
func wheelOnce(m Model, btn tea.MouseButton) Model {
	out, _ := m.handleMouseWheel(wheel(btn))
	out, _ = out.(Model).update(wheelFlushMsg{})
	return out.(Model)
}

func click(btn tea.MouseButton, x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg(tea.Mouse{Button: btn, X: x, Y: y})
}

func shiftClick(btn tea.MouseButton, x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg(tea.Mouse{Button: btn, X: x, Y: y, Mod: tea.ModShift})
}

func release(btn tea.MouseButton, x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg(tea.Mouse{Button: btn, X: x, Y: y})
}

func motion(btn tea.MouseButton, x, y int) tea.MouseMotionMsg {
	return tea.MouseMotionMsg(tea.Mouse{Button: btn, X: x, Y: y})
}

// mouseModel builds a model parked on a real team tab (so the channel/message
// layout — not the synthetic Feed/Search panes — is what hitTest sees), with
// two channels, a view cache, the keymap wired up, and the panes painted. The
// geometry the mouse handlers assume: tab strip on rows 0-2, the channel sidebar
// header on row 3 with rows below it, and the messages viewport's top row at 4
// with content starting at column 27 (channelsWidth+1).
func mouseModel(posts []*model.Post) Model {
	m := pagingModel(posts, 0)
	m.keys = newKeyMap("ctrl")
	m.vcache = &viewCache{}
	m.mouseEnabled = true
	m.teams = []*model.Team{{Id: "t1", DisplayName: "T1"}}
	m.channels = map[string][]*model.Channel{"t1": {
		{Id: "c", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "chan"},
		{Id: "c2", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "other"},
	}}
	m.teamIdx = m.firstTeamTabIdx() // land on the channel tab, not a synthetic one
	m.channelIdx = 0
	m.openChannelID = "c"
	ta := editor.New()
	ta.SetWidth(40)
	m.input = ta
	m.renderTeamTabs(nil)
	m.renderMessages()
	return m
}

// TestVisualHeightWrap: visualHeight matches the viewport's ceil(width/maxWidth)
// soft-wrap math the row-start tables rely on.
func TestVisualHeightWrap(t *testing.T) {
	cases := []struct {
		line  string
		width int
		want  int
	}{
		{"", 10, 1},
		{"short", 10, 1},
		{"0123456789", 10, 1},
		{"0123456789a", 10, 2},
		{"0123456789012345678", 10, 2},
		{"01234567890", 0, 1}, // width unknown → fall back to a single row
	}
	for _, c := range cases {
		if got := visualHeight(c.line, c.width); got != c.want {
			t.Errorf("visualHeight(%q, %d)=%d want %d", c.line, c.width, got, c.want)
		}
	}
}

// TestRowSearch: postAtVisualRow / lineAtVisualRow return the entry whose span
// contains the row (the largest start <= row).
func TestRowSearch(t *testing.T) {
	starts := []int{0, 2, 5, 9} // three entries: rows [0,2) [2,5) [5,9)
	cases := map[int]int{0: 0, 1: 0, 2: 1, 4: 1, 5: 2, 8: 2}
	for row, want := range cases {
		if got := postAtVisualRow(starts, row); got != want {
			t.Errorf("postAtVisualRow(%d)=%d want %d", row, got, want)
		}
		if got := lineAtVisualRow(starts, row); got != want {
			t.Errorf("lineAtVisualRow(%d)=%d want %d", row, got, want)
		}
	}
}

// TestHitTestMessageRow: a cell in the messages viewport resolves to the post
// whose visual rows cover it. shortPosts render two rows each (header + body),
// so post i spans viewport rows 2i..2i+1 (screen rows 4+2i, 5+2i).
func TestHitTestMessageRow(t *testing.T) {
	m := mouseModel(shortPosts(80))
	for _, c := range []struct {
		y, want int
	}{{4, 0}, {5, 0}, {6, 1}, {7, 1}, {8, 2}} {
		h := m.hitTest(30, c.y)
		if h.zone != hitMessage || h.idx != c.want {
			t.Errorf("hitTest(30,%d)=%v,%d want hitMessage,%d", c.y, h.zone, h.idx, c.want)
		}
	}
	// A cell below the loaded content is over nothing.
	if h := m.hitTest(30, 4+2*80); h.zone != hitNone {
		t.Errorf("click past content: zone=%v want hitNone", h.zone)
	}
}

// TestClickSelectsMessage: a left click on a message row moves the selection
// there, focuses the messages pane, and blurs the composer so its cursor stops
// rendering — otherwise the click leaves the textarea looking focused.
func TestClickSelectsMessage(t *testing.T) {
	m := mouseModel(shortPosts(80))
	m.focus = focusInput // start elsewhere to prove the click takes focus
	m.input.Focus()      // composer cursor showing before the click
	out, _ := m.handleMouseClick(click(tea.MouseLeft, 30, 6))
	m = out.(Model)
	if m.postIdx != 1 {
		t.Fatalf("click selected postIdx=%d want 1", m.postIdx)
	}
	if m.focus != focusMessages {
		t.Fatalf("click left focus=%v want focusMessages", m.focus)
	}
	if m.input.Focused() {
		t.Fatal("click left the composer focused; its cursor would keep rendering")
	}
}

// TestNextClickCount: presses at (about) the same cell bump the count to 2 then
// 3, a 4th restarts the cycle, and a press a cell or more away resets to 1.
func TestNextClickCount(t *testing.T) {
	var m Model
	for i, c := range []struct {
		x, y, want int
	}{
		{5, 5, 1},   // first press
		{5, 5, 2},   // double
		{6, 5, 3},   // within one cell → triple
		{5, 5, 1},   // fourth restarts the cycle
		{50, 50, 1}, // far away → reset
	} {
		if got := m.nextClickCount(c.x, c.y); got != c.want {
			t.Fatalf("step %d: nextClickCount(%d,%d)=%d want %d", i, c.x, c.y, got, c.want)
		}
	}
}

// TestMessageDoubleClickSelectsWord: a double-click on a message body selects the
// word under the pointer (and leaves it live, so it copies on release). The body
// of shortPosts is "line"; clicking on it selects "line", not the gutter.
func TestMessageDoubleClickSelectsWord(t *testing.T) {
	m := mouseModel(shortPosts(4))
	// Post 0's body is the second screen row of the post (row 5); content begins
	// at column channelsWidth+1, past the two-space gutter — so +3 lands on "line".
	x, y := channelsWidth+1+3, 5
	out, _ := m.handleMouseClick(click(tea.MouseLeft, x, y))
	m = out.(Model)
	out, _ = m.handleMouseClick(click(tea.MouseLeft, x, y))
	m = out.(Model)
	if !m.textSel.active {
		t.Fatal("double-click did not activate a selection")
	}
	if got := m.selectedText(); got != "line" {
		t.Fatalf("message double-click = %q, want %q", got, "line")
	}
}

// TestClickTeamTabSwitches: clicking the Feed tab (idx 0) switches to it.
func TestClickTeamTabSwitches(t *testing.T) {
	m := mouseModel(shortPosts(2))
	var feed *tabZone
	for i := range m.vcache.tabZones {
		if m.vcache.tabZones[i].idx == 0 {
			feed = &m.vcache.tabZones[i]
		}
	}
	if feed == nil {
		t.Fatal("no tab zone recorded for the Feed tab")
	}
	x := (feed.x0 + feed.x1) / 2
	if h := m.hitTest(x, 1); h.zone != hitTab || h.idx != 0 {
		t.Fatalf("hitTest on Feed tab=%v,%d want hitTab,0", h.zone, h.idx)
	}
	out, _ := m.handleMouseClick(click(tea.MouseLeft, x, 1))
	m = out.(Model)
	if m.teamIdx != 0 || !m.onFeedTab() {
		t.Fatalf("click didn't switch to Feed: teamIdx=%d onFeed=%v", m.teamIdx, m.onFeedTab())
	}
}

// TestClickChannelOpens: clicking the second channel row opens that channel.
func TestClickChannelOpens(t *testing.T) {
	m := mouseModel(shortPosts(2))
	// Header on row 3, channel 0 on row 4, channel 1 on row 5.
	if h := m.hitTest(2, 5); h.zone != hitChannel || h.idx != 1 {
		t.Fatalf("hitTest on channel row=%v,%d want hitChannel,1", h.zone, h.idx)
	}
	out, _ := m.handleMouseClick(click(tea.MouseLeft, 2, 5))
	m = out.(Model)
	if m.channelIdx != 1 || m.openChannelID != "c2" {
		t.Fatalf("channel click: channelIdx=%d open=%q want 1,c2", m.channelIdx, m.openChannelID)
	}
}

// TestSelectedTextSingleLine: a drag within one body line copies exactly the
// spanned characters (the gutter sits at columns 0-1, so "hello" is 2..7).
func TestSelectedTextSingleLine(t *testing.T) {
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "hello world"}})
	m.textSel = textSel{pane: focusMessages, anchorLine: 1, anchorCol: 2, headLine: 1, headCol: 7, active: true}
	m.renderMessages() // active selection suppresses the bar, as in the live flow
	if got := m.selectedText(); got != "hello" {
		t.Fatalf("selectedText=%q want %q", got, "hello")
	}
}

// TestSelectedTextMultiLine: a drag spanning two body lines joins them with a
// newline, trimming trailing padding on the all-but-last line and dropping the
// two-space chrome gutter that each body line carries (here bravo is selected
// from column 0, but its gutter is excluded).
func TestSelectedTextMultiLine(t *testing.T) {
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "alpha\nbravo"}})
	// Lines: [header, "  alpha", "  bravo"]. Select alpha's text through bravo.
	m.textSel = textSel{pane: focusMessages, anchorLine: 1, anchorCol: 2, headLine: 2, headCol: 7, active: true}
	m.renderMessages() // active selection suppresses the bar, as in the live flow
	if got := m.selectedText(); got != "alpha\nbravo" {
		t.Fatalf("selectedText=%q want %q", got, "alpha\nbravo")
	}
}

// TestSelectedTextDropsGutter: a drag that starts inside the two-space gutter
// (column 0) still copies only the message text, not the chrome indent.
func TestSelectedTextDropsGutter(t *testing.T) {
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "hello world"}})
	// Line 1 is "  hello world"; anchor at column 0 (in the gutter).
	m.textSel = textSel{pane: focusMessages, anchorLine: 1, anchorCol: 0, headLine: 1, headCol: 13, active: true}
	m.renderMessages()
	if got := m.selectedText(); got != "hello world" {
		t.Fatalf("selectedText=%q want %q", got, "hello world")
	}
}

func TestRefClickFocusesPaneAndBlursComposer(t *testing.T) {
	base := withForges(mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "x"}}))
	m := openLoadedChange(t, base, forgeGitLab, mrLink, sampleMR())
	m.focus = focusInput
	m.input.Focus()

	x0, top, _, _, _ := m.refGeom()
	out, _ := m.handleMouseClick(click(tea.MouseLeft, x0+4, top+2))
	m = out.(Model)
	if m.focus != focusRef {
		t.Fatalf("click focus=%v want focusRef", m.focus)
	}
	if m.input.Focused() {
		t.Fatal("click left the composer focused")
	}
	if !m.textSel.dragging || m.textSel.pane != focusRef {
		t.Fatalf("click did not arm a ref selection: %+v", m.textSel)
	}
}

func TestRefDragThenReleaseCopiesSelection(t *testing.T) {
	base := withForges(mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "x"}}))
	mr := sampleMR()
	mr.Description = "Alpha bravo"
	m := openLoadedChange(t, base, forgeGitLab, mrLink, mr)

	lines, _ := m.ensureWrapIndex(focusRef, m.refView.Width())
	line, start := -1, -1
	for i, ln := range lines {
		if j := strings.Index(ansi.Strip(ln), "Alpha bravo"); j >= 0 {
			line, start = i, j
			break
		}
	}
	if line < 0 {
		t.Fatal("could not find ref-body text")
	}

	x0, top, width, _, yoff := m.refGeom()
	x1 := x0 + start%width
	y1 := top + (line + start/width - yoff)
	x2 := x0 + (start+5)%width
	y2 := top + (line + (start+5)/width - yoff)

	out, _ := m.handleMouseClick(click(tea.MouseLeft, x1, y1))
	m = out.(Model)
	out, _ = m.handleMouseMotion(motion(tea.MouseLeft, x2, y2))
	m = out.(Model)
	if !m.textSel.active || m.textSel.pane != focusRef {
		t.Fatalf("drag did not activate a ref selection: %+v", m.textSel)
	}
	out, cmd := m.handleMouseRelease(release(tea.MouseLeft, x2, y2))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("drag release did not produce a copy command")
	}
	if got := m.selectedText(); got != "Alpha" {
		t.Fatalf("selectedText=%q want %q", got, "Alpha")
	}
}

// TestRefPanelBeatsComposerAtSameHeight: a click in the reference panel on the
// same screen rows as the composer must hit the panel, not the input — even when
// the composer's width still overhangs (the bug that made bottom-of-panel links
// focus the text input instead of opening).
func TestRefPanelBeatsComposerAtSameHeight(t *testing.T) {
	base := withForges(mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "x"}}))
	mr := sampleMR()
	mr.Description = "see https://example.com/bottom"
	m := openLoadedChange(t, base, forgeGitLab, mrLink, mr)
	m.vcache.bodyH = 20
	// Simulate the old bug: input still at full right-pane width after opening
	// the panel, so its hit box overlaps the side panel's bottom rows.
	m.input.SetWidth(m.width - channelsWidth - 4)

	x0, _, _, _, _ := m.refGeom()
	_, top, _, height, _ := m.composerGeom()
	if height < 1 {
		t.Fatal("composer has no height")
	}
	x, y := x0+2, top
	if h := m.hitTest(x, y); h.zone != hitRef {
		t.Fatalf("hitTest in ref column at composer row = zone %v, want hitRef", h.zone)
	}
	if m.inComposer(x, y) {
		// The overhang is still true for inComposer alone; hitTest must prefer the panel.
		t.Log("inComposer still true under overhang (expected); hitTest correctly preferred hitRef")
	}

	out, _ := m.handleMouseClick(click(tea.MouseLeft, x, y))
	got := out.(Model)
	if got.focus != focusRef {
		t.Fatalf("click focus=%v want focusRef (composer must not steal it)", got.focus)
	}
	if got.input.Focused() {
		t.Fatal("click focused the composer")
	}
}

// TestDragThenReleaseCopies: a mousedown + drag + release on a message produces
// a copy command (a non-empty selection), while a release at the press cell is
// a plain click that copies nothing.
func TestDragThenReleaseCopies(t *testing.T) {
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "hello world"}})

	// Bare click then release in place: selects the message, copies nothing.
	out, _ := m.handleMouseClick(click(tea.MouseLeft, 29, 5))
	m = out.(Model)
	if !m.textSel.dragging {
		t.Fatal("mousedown didn't arm a selection")
	}
	out, cmd := m.handleMouseRelease(release(tea.MouseLeft, 29, 5))
	m = out.(Model)
	if cmd != nil {
		t.Fatal("in-place release produced a copy command")
	}
	if m.textSel.dragging || m.textSel.active {
		t.Fatal("in-place release left a selection armed")
	}

	// Press, drag right, release: an active selection that copies.
	out, _ = m.handleMouseClick(click(tea.MouseLeft, 29, 5)) // column 2 of body line
	m = out.(Model)
	out, _ = m.handleMouseMotion(motion(tea.MouseLeft, 34, 5)) // drag to column 7
	m = out.(Model)
	if !m.textSel.active {
		t.Fatal("drag didn't activate the selection")
	}
	out, cmd = m.handleMouseRelease(release(tea.MouseLeft, 34, 5))
	if cmd == nil {
		t.Fatal("drag release didn't produce a copy command")
	}
}

// TestHoverTracksChannelAndTab: button-less motion sets the hovered element,
// and a keypress / a click clears any text selection.
func TestHoverTracksChannelAndTab(t *testing.T) {
	m := mouseModel(shortPosts(2))

	out, _ := m.handleMouseMotion(motion(tea.MouseNone, 2, 5)) // channel row 1
	m = out.(Model)
	if m.hover.zone != hitChannel || m.hover.idx != 1 {
		t.Fatalf("hover over channel=%v,%d want hitChannel,1", m.hover.zone, m.hover.idx)
	}

	// Move onto the messages pane: no longer hovering a tracked element.
	out, _ = m.handleMouseMotion(motion(tea.MouseNone, 30, 5))
	m = out.(Model)
	if m.hover.zone != hitNone {
		t.Fatalf("hover over messages=%v want hitNone", m.hover.zone)
	}
}

// TestKeypressClearsTextSel: any keypress dismisses an active selection.
func TestKeypressClearsTextSel(t *testing.T) {
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "hello world"}})
	m.textSel = textSel{pane: focusMessages, anchorLine: 1, anchorCol: 2, headLine: 1, headCol: 7, active: true}
	out, _ := m.handleKey(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.textSel.active {
		t.Fatal("keypress didn't clear the text selection")
	}
}

// shortPosts builds n one-line posts — enough of them to overflow the test
// viewport (height 40) so there's room to scroll.
func shortPosts(n int) []*model.Post {
	posts := make([]*model.Post, n)
	for i := range posts {
		posts[i] = &model.Post{Id: fmt.Sprintf("p%d", i), CreateAt: int64(100 + i), UserId: "u", Message: "line"}
	}
	return posts
}

// TestWheelFreeScrollsViewport: the wheel scrolls the viewport, not the
// selection — postIdx is unchanged and the offset moves by the viewport's
// wheel delta.
func TestWheelFreeScrollsViewport(t *testing.T) {
	m := scrollModel(shortPosts(80), 0)
	m.renderMessages()
	if off := m.msgsView.YOffset(); off != 0 {
		t.Fatalf("precondition: YOffset=%d want 0", off)
	}

	m = wheelOnce(m, tea.MouseWheelDown)
	if m.postIdx != 0 {
		t.Fatalf("wheel moved the selection: postIdx=%d want 0", m.postIdx)
	}
	if off := m.msgsView.YOffset(); off <= 0 {
		t.Fatalf("wheel didn't scroll the viewport: YOffset=%d", off)
	}
	if !m.msgScrollFree {
		t.Fatal("wheel didn't enter free-scroll mode")
	}
}

// TestWheelEntersFreeScrollBeforeFlush: the sticky free-scroll flag is set the
// instant a wheel event arrives, not deferred to the flush — so a background
// re-render between the gesture and the coalescing tick keeps the offset.
func TestWheelEntersFreeScrollBeforeFlush(t *testing.T) {
	m := scrollModel(shortPosts(80), 0)
	m.renderMessages()

	out, cmd := m.handleMouseWheel(wheel(tea.MouseWheelDown))
	m = out.(Model)
	if !m.msgScrollFree {
		t.Fatal("free-scroll not entered on the wheel event itself")
	}
	if cmd == nil {
		t.Fatal("first wheel event didn't arm a flush tick")
	}
	if m.wheelPending == 0 {
		t.Fatal("wheel event didn't accumulate a pending delta")
	}
}

// TestWheelCoalescesBurst: a burst of wheel events accumulates into one pending
// delta and arms exactly one tick (not one per event — that's what let a
// trackpad flood back up the queue); a single flush then applies the whole burst
// and disarms.
func TestWheelCoalescesBurst(t *testing.T) {
	m := scrollModel(shortPosts(80), 0)
	m.renderMessages()
	step := m.msgsView.MouseWheelDelta

	const burst = 5
	for i := 0; i < burst; i++ {
		out, cmd := m.handleMouseWheel(wheel(tea.MouseWheelDown))
		m = out.(Model)
		switch {
		case i == 0 && cmd == nil:
			t.Fatal("first wheel event didn't arm a flush tick")
		case i > 0 && cmd != nil:
			t.Fatalf("event %d re-armed a tick; should coalesce into the pending one", i)
		}
	}
	if off := m.msgsView.YOffset(); off != 0 {
		t.Fatalf("burst moved the viewport before the flush: YOffset=%d", off)
	}
	if m.wheelPending != burst*step {
		t.Fatalf("pending=%d want %d", m.wheelPending, burst*step)
	}

	out, _ := m.update(wheelFlushMsg{})
	m = out.(Model)
	if m.wheelPending != 0 || m.wheelTicking {
		t.Fatalf("flush left state armed: pending=%d ticking=%v", m.wheelPending, m.wheelTicking)
	}
	if off := m.msgsView.YOffset(); off != burst*step {
		t.Fatalf("flush applied %d lines, want %d", off, burst*step)
	}
}

// TestInputFlushesPendingWheel: a keypress routed through update() applies any
// coalesced wheel delta before acting, so a key pressed within a frame of the
// last wheel event still operates on the scrolled position.
func TestInputFlushesPendingWheel(t *testing.T) {
	m := scrollModel(shortPosts(80), 0)
	m.renderMessages()

	out, _ := m.handleMouseWheel(wheel(tea.MouseWheelDown))
	m = out.(Model)
	if m.wheelPending == 0 {
		t.Fatal("precondition: expected a pending wheel delta")
	}

	out, _ = m.update(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.wheelPending != 0 {
		t.Fatalf("keypress didn't flush pending wheel: pending=%d", m.wheelPending)
	}
}

// TestFreeScrollSurvivesRerender: while free-scrolled, a background re-render
// (e.g. a new message) keeps the wheel's offset instead of snapping back to the
// selection.
func TestFreeScrollSurvivesRerender(t *testing.T) {
	m := scrollModel(shortPosts(80), 79) // selection at the bottom
	m.anchorMsgSelBottom = true
	m.renderMessages()
	bottomOff := m.msgsView.YOffset()
	if bottomOff == 0 {
		t.Fatal("precondition: expected a non-zero bottom offset")
	}

	// Wheel up well past the top so the offset clamps to 0.
	for i := 0; i < 60; i++ {
		m = wheelOnce(m, tea.MouseWheelUp)
	}
	if off := m.msgsView.YOffset(); off != 0 {
		t.Fatalf("wheel-up didn't reach the top: YOffset=%d", off)
	}

	// A re-render (selection is still at the bottom) must NOT snap back.
	m.renderMessages()
	if off := m.msgsView.YOffset(); off != 0 {
		t.Fatalf("re-render snapped back to the selection: YOffset=%d want 0", off)
	}
}

// TestKeypressExitsFreeScroll: a keypress leaves free-scroll, syncs the
// selection to the on-screen post, and resumes selection-following.
func TestKeypressExitsFreeScroll(t *testing.T) {
	m := scrollModel(shortPosts(80), 79) // selection at the bottom
	m.anchorMsgSelBottom = true
	m.renderMessages()

	// Wheel to the top.
	for i := 0; i < 60; i++ {
		m = wheelOnce(m, tea.MouseWheelUp)
	}
	if !m.msgScrollFree {
		t.Fatal("precondition: expected free-scroll mode")
	}

	out, _ := m.handleKey(keyPress(tea.KeyDown))
	m = out.(Model)
	if m.msgScrollFree {
		t.Fatal("keypress didn't clear free-scroll mode")
	}
	// Synced to the top-visible post (≈0), then ↓ stepped once.
	if m.postIdx != 1 {
		t.Fatalf("selection not synced to the visible post: postIdx=%d want 1", m.postIdx)
	}
}

// TestMouseModeGatedByConfig: View requests all-motion mouse reporting (needed
// for hover) only when enabled.
func TestMouseModeGatedByConfig(t *testing.T) {
	on := Model{mouseEnabled: true}.View()
	if on.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse on: MouseMode=%v want AllMotion", on.MouseMode)
	}
	off := Model{mouseEnabled: false}.View()
	if off.MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse off: MouseMode=%v want None", off.MouseMode)
	}
}

// TestWheelTopPaginatesOlderHistoryOnce: an upward wheel flick that lands at the
// very top of the loaded window requests older history, and a second flick while
// that fetch is still in flight does NOT stack another (the loadingOlder guard,
// so a trackpad momentum flood can't fire a request per frame).
func TestWheelTopPaginatesOlderHistoryOnce(t *testing.T) {
	m := pagingModel([]*model.Post{p("m1", 500), p("m2", 600)}, 1)
	m.renderMessages()
	// Content is shorter than the viewport, so the offset is pinned at the top.
	out, cmd := wheelFlush(m, tea.MouseWheelUp)
	got := out
	if cmd == nil {
		t.Fatal("wheel-up at top returned no command; expected an older-history fetch")
	}
	if !got.loadingOlder {
		t.Error("loadingOlder not set after the wheel-triggered fetch")
	}
	if got.status != "loading older messages…" {
		t.Errorf("status = %q; want the loading notice", got.status)
	}
	// Second flick while the first fetch is unresolved must be a no-op.
	out2, cmd2 := wheelFlush(got, tea.MouseWheelUp)
	if cmd2 != nil {
		t.Error("second wheel-up while loading stacked another fetch; want none")
	}
	if !out2.loadingOlder {
		t.Error("guard cleared by the second flick; want it held until the fetch returns")
	}
}

// TestWheelOlderMergeKeepsViewAnchored: when a wheel-triggered older page
// arrives mid-scroll, the post at the viewport top must stay put (no jump) and
// the free-scroll flag must survive so the wheel doesn't snap back to the
// selection.
func TestWheelOlderMergeKeepsViewAnchored(t *testing.T) {
	m := pagingModel([]*model.Post{p("m1", 500), p("m2", 600), p("m3", 700)}, 2)
	m.renderMessages()
	m.msgScrollFree = true
	m.loadingOlder = true
	// Park the viewport top exactly at m2.
	m.msgFreeOffset = m.msgRowStarts[1]
	m.msgsView.SetYOffset(m.msgFreeOffset)

	out, _ := m.update(olderPostsMsg{
		channelID: "c",
		posts:     []*model.Post{p("old1", 100), p("old2", 200)},
	})
	got := out.(Model)

	if got.loadingOlder {
		t.Error("loadingOlder not cleared after olderPostsMsg")
	}
	if !got.msgScrollFree {
		t.Error("free-scroll flag dropped; the wheel view would snap to the selection")
	}
	if order := ids(got.posts); !eq(order, []string{"old1", "old2", "m1", "m2", "m3"}) {
		t.Fatalf("older page not merged in order: got %v", order)
	}
	// m2 must remain at the viewport top: its new row-start equals the offset.
	idx := got.postIndexByID("m2")
	if want := got.msgRowStarts[idx]; got.msgFreeOffset != want {
		t.Errorf("view jumped: msgFreeOffset=%d, want %d (m2 row-start)", got.msgFreeOffset, want)
	}
}

// wheelFlush delivers one wheel event and the coalescing flush, returning the
// resulting model and the flush command (the older-history fetch, when the
// gesture pages). Unlike wheelOnce it keeps the command for assertions.
func wheelFlush(m Model, btn tea.MouseButton) (Model, tea.Cmd) {
	out, _ := m.handleMouseWheel(wheel(btn))
	out2, cmd := out.(Model).update(wheelFlushMsg{})
	return out2.(Model), cmd
}

// TestWheelBottomPaginatesNewerHistory: a downward wheel flick that lands at the
// bottom of the loaded window (whose tail sits below the live tail — e.g.
// reading forward from a search hit) pages in newer history, painting the cached
// page at once and asking the server for more. A second flick while that fetch
// is in flight must not stack another (the loadingNewer guard).
func TestWheelBottomPaginatesNewerHistory(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "wheel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// Newer posts cached past the loaded tail.
	if err := st.UpsertMany([]*model.Post{
		{Id: "n1", ChannelId: "c", UserId: "u", CreateAt: 700, UpdateAt: 700},
		{Id: "n2", ChannelId: "c", UserId: "u", CreateAt: 800, UpdateAt: 800},
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	m := pagingModel([]*model.Post{p("m1", 500), p("m2", 600)}, 1)
	m.store = st
	m.renderMessages()
	// Content is shorter than the viewport, so the offset is pinned at the
	// bottom (== top); a downward flick still reads as "at the bottom".
	out, cmd := wheelFlush(m, tea.MouseWheelDown)
	got := out
	if cmd == nil {
		t.Fatal("wheel-down at bottom returned no command; expected a newer-history fetch")
	}
	if !got.loadingNewer {
		t.Error("loadingNewer not set after the wheel-triggered fetch")
	}
	if order := ids(got.posts); !eq(order, []string{"m1", "m2", "n1", "n2"}) {
		t.Fatalf("cached newer page not appended: got %v", order)
	}
	_, cmd2 := wheelFlush(got, tea.MouseWheelDown)
	if cmd2 != nil {
		t.Error("second wheel-down while loading stacked another fetch; want none")
	}
}

// TestWheelBottomAtLiveTailIsNoOp: a downward flick at the live tail (nothing
// newer cached) does nothing — no fetch, and crucially no drop into the composer
// (the keyboard ↓ does that; the wheel must not).
func TestWheelBottomAtLiveTailIsNoOp(t *testing.T) {
	m := pagingModel([]*model.Post{p("m1", 500), p("m2", 600)}, 1)
	m.focus = focusMessages
	m.renderMessages()
	out, cmd := wheelFlush(m, tea.MouseWheelDown) // no store → nothing newer
	if cmd != nil {
		t.Error("wheel-down at the live tail issued a command; want a no-op")
	}
	if out.focus != focusMessages {
		t.Errorf("wheel-down dropped focus to %v; the wheel must not enter the composer", out.focus)
	}
}

// TestWheelNewerMergeKeepsViewAnchored: when a wheel-triggered newer page
// arrives mid-scroll, the post at the viewport top stays put (no jump) and the
// free-scroll flag survives.
func TestWheelNewerMergeKeepsViewAnchored(t *testing.T) {
	m := pagingModel([]*model.Post{p("m1", 500), p("m2", 600), p("m3", 700)}, 0)
	m.renderMessages()
	m.msgScrollFree = true
	m.loadingNewer = true
	// Park the viewport top at m2.
	m.msgFreeOffset = m.msgRowStarts[1]
	m.msgsView.SetYOffset(m.msgFreeOffset)

	out, _ := m.update(newerPostsMsg{
		channelID: "c",
		posts:     []*model.Post{p("n1", 800), p("n2", 900)},
	})
	got := out.(Model)

	if got.loadingNewer {
		t.Error("loadingNewer not cleared after newerPostsMsg")
	}
	if !got.msgScrollFree {
		t.Error("free-scroll flag dropped; the wheel view would snap to the selection")
	}
	if order := ids(got.posts); !eq(order, []string{"m1", "m2", "m3", "n1", "n2"}) {
		t.Fatalf("newer page not merged in order: got %v", order)
	}
	idx := got.postIndexByID("m2")
	if want := got.msgRowStarts[idx]; got.msgFreeOffset != want {
		t.Errorf("view jumped: msgFreeOffset=%d, want %d (m2 row-start)", got.msgFreeOffset, want)
	}
}

// Channel-info shares the right slot with the reference panel: a click in the
// info column at composer height must hit the info pane, not the compose box —
// including when resizeInput hasn't narrowed the input yet.
func TestInfoPanelBeatsComposerAtSameHeight(t *testing.T) {
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "x"}})
	m.raiseChannelInfo()
	if !m.infoOpen {
		t.Fatal("expected info panel open")
	}
	m.vcache.bodyH = 20
	m.input.SetWidth(m.width - channelsWidth - 4)

	x0, _, _, _, _ := m.infoGeom()
	_, top, _, height, _ := m.composerGeom()
	if height < 1 {
		t.Fatal("composer has no height")
	}
	x, y := x0+2, top
	if h := m.hitTest(x, y); h.zone != hitInfo {
		t.Fatalf("hitTest in info column at composer row = zone %v, want hitInfo", h.zone)
	}

	out, _ := m.handleMouseClick(click(tea.MouseLeft, x, y))
	got := out.(Model)
	if got.focus != focusInfo {
		t.Fatalf("click focus=%v want focusInfo", got.focus)
	}
	if got.input.Focused() {
		t.Fatal("click focused the composer")
	}
}

// Channel-info uses the same text-selection path as the reference panel.
func TestInfoDragThenReleaseCopiesSelection(t *testing.T) {
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "x"}})
	m.raiseChannelInfo()
	m.renderInfo()

	const needle = "Members"
	lines, _ := m.ensureWrapIndex(focusInfo, m.infoView.Width())
	line, start := -1, -1
	for i, ln := range lines {
		if j := strings.Index(ansi.Strip(ln), needle); j >= 0 {
			line, start = i, j
			break
		}
	}
	if line < 0 {
		t.Fatal("could not find info-body text")
	}

	x0, top, width, _, yoff := m.infoGeom()
	x1 := x0 + start%width
	y1 := top + (line + start/width - yoff)
	x2 := x0 + (start+len(needle))%width
	y2 := top + (line + (start+len(needle))/width - yoff)

	out, _ := m.handleMouseClick(click(tea.MouseLeft, x1, y1))
	m = out.(Model)
	out, _ = m.handleMouseMotion(motion(tea.MouseLeft, x2, y2))
	m = out.(Model)
	if !m.textSel.active || m.textSel.pane != focusInfo {
		t.Fatalf("drag did not activate an info selection: %+v", m.textSel)
	}
	out, cmd := m.handleMouseRelease(release(tea.MouseLeft, x2, y2))
	m = out.(Model)
	if cmd == nil {
		t.Fatal("drag release did not produce a copy command")
	}
	if got := m.selectedText(); got != needle {
		t.Fatalf("selectedText=%q want %q", got, needle)
	}
}
