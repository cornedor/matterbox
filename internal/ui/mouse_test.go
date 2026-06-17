package ui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

func wheel(btn tea.MouseButton) tea.MouseWheelMsg {
	return tea.MouseWheelMsg(tea.Mouse{Button: btn})
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

// TestMouseModeGatedByConfig: View requests mouse reporting only when enabled.
func TestMouseModeGatedByConfig(t *testing.T) {
	on := Model{mouseEnabled: true}.View()
	if on.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse on: MouseMode=%v want CellMotion", on.MouseMode)
	}
	off := Model{mouseEnabled: false}.View()
	if off.MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse off: MouseMode=%v want None", off.MouseMode)
	}
}
