package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestClickComposerFocusesAndPlacesCaret: a click in the compose box takes input
// focus and drops the caret under the pointer, a drag selects, and the release
// leaves the selection live (so a following backspace / keystroke can act on it).
func TestClickComposerFocusesAndPlacesCaret(t *testing.T) {
	m := mouseModel(shortPosts(4))
	m.input.SetValue("hello world")
	m.input.Blur()
	m.focus = focusMessages
	// composerGeom reads the body height a render recorded; prime it so the box
	// has a screen position (the hover path stays alloc-free by not re-rendering).
	m.vcache.bodyH = 20

	x0, top, _, h, _ := m.composerGeom()
	if h != 1 || x0 != channelsWidth+1 {
		t.Fatalf("composerGeom = (x0=%d, h=%d), want (%d, 1)", x0, h, channelsWidth+1)
	}

	// Click 5 columns in → caret after "hello" (prompt width is 0 here).
	out, _ := m.handleMouseClick(click(tea.MouseLeft, x0+5, top))
	m = out.(Model)
	if m.focus != focusInput || !m.input.Focused() {
		t.Fatalf("click did not focus composer: focus=%v focused=%v", m.focus, m.input.Focused())
	}
	if !m.composerDrag {
		t.Fatal("click did not arm a composer drag")
	}
	if off := m.input.CursorOffset(); off != 5 {
		t.Fatalf("caret offset = %d, want 5", off)
	}
	if m.input.HasSelection() {
		t.Fatal("a bare click should not select")
	}

	// Drag to column 11 → selects " world".
	out, _ = m.handleMouseMotion(motion(tea.MouseLeft, x0+11, top))
	m = out.(Model)
	if got := m.input.SelectedText(); got != " world" {
		t.Fatalf("dragged selection = %q, want %q", got, " world")
	}

	// Release ends the drag but keeps the selection alive.
	out, _ = m.handleMouseRelease(release(tea.MouseLeft, x0+11, top))
	m = out.(Model)
	if m.composerDrag {
		t.Fatal("release did not end the composer drag")
	}
	if !m.input.HasSelection() {
		t.Fatal("release dropped the live selection")
	}
}

// TestComposerDoubleTripleClick: a double-click in the composer selects the word
// under the pointer, a triple-click the whole line. Successive handleMouseClick
// calls at the same cell synthesise the multi-click (the terminal reports only
// individual presses).
func TestComposerDoubleTripleClick(t *testing.T) {
	prime := func() (Model, int, int) {
		m := mouseModel(shortPosts(4))
		m.input.SetValue("hello world")
		m.input.Blur()
		m.focus = focusMessages
		m.vcache.bodyH = 20
		x0, top, _, _, _ := m.composerGeom()
		return m, x0, top
	}
	clickN := func(m Model, x, y, n int) Model {
		for i := 0; i < n; i++ {
			out, _ := m.handleMouseClick(click(tea.MouseLeft, x, y))
			m = out.(Model)
		}
		return m
	}

	// Double-click on the "o" of "world" → selects "world".
	m, x0, top := prime()
	m = clickN(m, x0+7, top, 2)
	if got := m.input.SelectedText(); got != "world" {
		t.Fatalf("double-click word = %q, want %q", got, "world")
	}

	// Triple-click → selects the whole line.
	m, x0, top = prime()
	m = clickN(m, x0+7, top, 3)
	if got := m.input.SelectedText(); got != "hello world" {
		t.Fatalf("triple-click line = %q, want %q", got, "hello world")
	}
}

// TestComposerShiftClickExtends: a shift-click extends the selection from the
// caret to the clicked cell, instead of starting a fresh one.
func TestComposerShiftClickExtends(t *testing.T) {
	m := mouseModel(shortPosts(4))
	m.input.SetValue("hello world")
	m.input.Blur()
	m.focus = focusMessages
	m.vcache.bodyH = 20
	x0, top, _, _, _ := m.composerGeom()

	// Plain click drops the caret after "hello".
	out, _ := m.handleMouseClick(click(tea.MouseLeft, x0+5, top))
	m = out.(Model)
	out, _ = m.handleMouseRelease(release(tea.MouseLeft, x0+5, top))
	m = out.(Model)
	if m.input.HasSelection() {
		t.Fatal("plain click should not select")
	}
	// Shift-click at the start extends back over "hello".
	out, _ = m.handleMouseClick(shiftClick(tea.MouseLeft, x0, top))
	m = out.(Model)
	if got := m.input.SelectedText(); got != "hello" {
		t.Fatalf("shift-click extend = %q, want %q", got, "hello")
	}
}

// TestComposerGeomUnprimedIsInert: before any render the body height is unknown,
// so the composer has no hittable region (and composerGeom never re-renders the
// footer, keeping the per-motion hover path alloc-free).
func TestComposerGeomUnprimedIsInert(t *testing.T) {
	m := mouseModel(shortPosts(4))
	m.vcache.bodyH = 0
	if _, _, w, _, _ := m.composerGeom(); w != 0 {
		t.Fatalf("unprimed composerGeom width = %d, want 0", w)
	}
	if m.inComposer(channelsWidth+5, 21) {
		t.Fatal("inComposer reported true with no geometry")
	}
}
