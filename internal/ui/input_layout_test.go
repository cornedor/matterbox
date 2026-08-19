package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// growInput simulates typing `lines` logical lines into the compose input and
// reflowing the layout the way the live update loop does.
func growInput(m *Model, lines int) {
	val := strings.TrimSuffix(strings.Repeat("x\n", lines), "\n")
	m.input.SetValue(val)
	m.syncInputHeight()
	// In the live loop a keypress goes through update(), which invalidates the
	// memoized view; this helper bypasses that, so invalidate explicitly or
	// viewContent() would return the stale cached frame.
	if m.vcache != nil {
		m.vcache.viewValid = false
	}
}

// TestInputGrowthNeverOverflows guards against the regression where a growing
// compose textarea pushed the footer (and then its own content) off-screen:
// the rendered view must always fit exactly within the terminal height.
func TestInputGrowthNeverOverflows(t *testing.T) {
	sizes := []struct{ w, h int }{{120, 40}, {120, 24}, {120, 16}, {100, 16}}
	for _, thread := range []bool{false, true} {
		for _, sz := range sizes {
			m := newTestModel()
			m.width, m.height = sz.w, sz.h
			m.focus = focusInput
			m.threadOpen = thread
			m.resizeMessagesViewport()
			m.resizeInput()
			for _, lines := range []int{1, 2, 4, 6, 12} {
				growInput(&m, lines)
				if got := lipgloss.Height(m.viewContent()); got != m.height {
					t.Errorf("thread=%v %dx%d, %d input lines: viewContent height = %d, want %d",
						thread, sz.w, sz.h, lines, got, m.height)
				}
				// The input must never grow past what fits in the pane.
				if m.input.Height() > maxInputHeight {
					t.Errorf("thread=%v %dx%d: input height %d exceeds cap %d",
						thread, sz.w, sz.h, m.input.Height(), maxInputHeight)
				}
			}
		}
	}
}

// TestInputBottomAligned checks that growing the input by N rows shrinks the
// message viewport by exactly N — i.e. the input stays pinned to the bottom
// instead of floating with blank rows beneath it.
func TestInputBottomAligned(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 120, 40
	m.focus = focusInput
	m.resizeMessagesViewport()
	m.resizeInput()

	growInput(&m, 1)
	baseInput := m.input.Height()
	baseView := m.msgsView.Height()

	growInput(&m, 4)
	grewBy := m.input.Height() - baseInput
	if grewBy <= 0 {
		t.Fatalf("input did not grow: height stayed at %d", baseInput)
	}
	if shrank := baseView - m.msgsView.Height(); shrank != grewBy {
		t.Errorf("viewport shrank by %d but input grew by %d; input is not bottom-aligned",
			shrank, grewBy)
	}
}

// TestComposerNewlinePastMaxHeight verifies that the textarea accepts newlines
// beyond MaxHeight (6 visible rows). The textarea must scroll internally, not
// block input once the logical line count reaches the visible height cap.
// Regression: bubbles/v2 textarea's atContentLimit() checks len(value) >= MaxHeight
// when MaxContentHeight is unset, blocking newlines at 6 lines.
func TestComposerNewlinePastMaxHeight(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 120, 40
	m.focus = focusInput
	m.input.Focus()
	m.resizeMessagesViewport()
	m.resizeInput()

	shiftEnter := tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift})
	for i := 0; i < 10; i++ {
		out, _ := m.handleInputKey(shiftEnter)
		m = out.(Model)
	}

	lines := strings.Count(m.input.Value(), "\n") + 1
	if lines <= 6 {
		t.Fatalf("composer blocked newlines at %d lines; want > 6", lines)
	}
	// The visible height stays capped at MaxHeight.
	if m.input.Height() > maxInputHeight {
		t.Fatalf("input height %d exceeds cap %d", m.input.Height(), maxInputHeight)
	}
}
