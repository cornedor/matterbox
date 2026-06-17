package ui

import (
	"fmt"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

func wheel(btn tea.MouseButton) tea.MouseWheelMsg {
	return tea.MouseWheelMsg(tea.Mouse{Button: btn})
}

func click(btn tea.MouseButton, x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg(tea.Mouse{Button: btn, X: x, Y: y})
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
	ta := textarea.New()
	ta.SetWidth(40)
	m.input = ta
	m.renderTeamTabs()
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
// there and focuses the messages pane.
func TestClickSelectsMessage(t *testing.T) {
	m := mouseModel(shortPosts(80))
	m.focus = focusInput // start elsewhere to prove the click takes focus
	out, _ := m.handleMouseClick(click(tea.MouseLeft, 30, 6))
	m = out.(Model)
	if m.postIdx != 1 {
		t.Fatalf("click selected postIdx=%d want 1", m.postIdx)
	}
	if m.focus != focusMessages {
		t.Fatalf("click left focus=%v want focusMessages", m.focus)
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
// newline, trimming trailing padding on the all-but-last line.
func TestSelectedTextMultiLine(t *testing.T) {
	m := mouseModel([]*model.Post{{Id: "p", CreateAt: 100, UserId: "u", Message: "alpha\nbravo"}})
	// Lines: [header, "  alpha", "  bravo"]. Select alpha's text through bravo.
	m.textSel = textSel{pane: focusMessages, anchorLine: 1, anchorCol: 2, headLine: 2, headCol: 7, active: true}
	m.renderMessages() // active selection suppresses the bar, as in the live flow
	if got := m.selectedText(); got != "alpha\n  bravo" {
		t.Fatalf("selectedText=%q want %q", got, "alpha\n  bravo")
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

	out, _ := m.handleMouseWheel(wheel(tea.MouseWheelDown))
	m = out.(Model)
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
		out, _ := m.handleMouseWheel(wheel(tea.MouseWheelUp))
		m = out.(Model)
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
		out, _ := m.handleMouseWheel(wheel(tea.MouseWheelUp))
		m = out.(Model)
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
