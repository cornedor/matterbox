package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/viewport"
)

// longBody is content taller than any sheet modal's inner height, so every
// sheet under test actually has somewhere to scroll to.
func longBody() string {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "line"
	}
	return strings.Join(lines, "\n")
}

// wheelSheetModel builds a sheet-sized model whose message pane is also
// scrollable, so a test can tell "the sheet scrolled" from "the transcript
// behind it scrolled".
func wheelSheetModel() Model {
	sheet := viewport.New()
	sheet.SoftWrap = true
	msgs := viewport.New()
	msgs.SetWidth(80)
	msgs.SetHeight(10)
	msgs.SetContent(longBody())
	m := Model{
		keys:          newKeyMap("ctrl"),
		focus:         focusMessages,
		width:         100,
		height:        44,
		keysSheetView: &sheet,
		msgsView:      msgs,
	}
	return m
}

// TestWheelScrollsSheetModals: every scrollable sheet takes the wheel the same
// way ↑/↓ do — one notch moves its viewport by MouseWheelDelta, and the
// transcript buried behind the popup stays put.
func TestWheelScrollsSheetModals(t *testing.T) {
	cases := []struct {
		name string
		open func(m *Model)
	}{
		{"keys sheet", func(m *Model) {
			m.openKeysSheet()
			m.keysSheetView.SetContent(longBody())
		}},
		{"text popup", func(m *Model) { m.openTextPopup("Stats", longBody()) }},
		{"edit history", func(m *Model) {
			hv := viewport.New()
			hv.SoftWrap = true
			m.historyView = &hv
			m.historyMode = true
			m.sizeHistoryView()
			m.historyView.SetContent(longBody())
		}},
		{"summary result", func(m *Model) {
			m.summary = newSummaryState()
			m.summary.phase = summaryDone
			m.summary.view.SetWidth(m.modalInnerWidth())
			m.summary.view.SetHeight(10)
			m.summary.view.SetContent(longBody())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := wheelSheetModel()
			tc.open(&m)
			if !m.inModal() {
				t.Fatal("setup did not open a modal")
			}
			vp := m.modalScrollView()
			if vp == nil {
				t.Fatal("open sheet has no scroll viewport")
			}
			step := vp.MouseWheelDelta
			msgsBefore := m.msgsView.YOffset()

			got := wheelOnce(m, tea.MouseWheelDown)
			vp = got.modalScrollView()
			if vp.YOffset() != step {
				t.Errorf("wheel down: sheet YOffset=%d, want %d", vp.YOffset(), step)
			}
			if got.msgsView.YOffset() != msgsBefore {
				t.Errorf("wheel leaked to the transcript behind the sheet: msgsView YOffset=%d, want %d",
					got.msgsView.YOffset(), msgsBefore)
			}

			got = wheelOnce(got, tea.MouseWheelUp)
			if vp := got.modalScrollView(); vp.YOffset() != 0 {
				t.Errorf("wheel up: sheet YOffset=%d, want 0", vp.YOffset())
			}
		})
	}
}

// TestWheelBlockedByNonScrollingModal: a modal with nothing to scroll (a
// picker, a confirm) swallows the wheel rather than letting it move the pane it
// covers. Focus is untouched while a modal is up, so without the guard the
// gesture landed on the transcript underneath.
func TestWheelBlockedByNonScrollingModal(t *testing.T) {
	m := wheelSheetModel()
	m.deleteConfirmPostID = "p1"
	if m.modalScrollView() != nil {
		t.Fatal("the delete confirm should have no scroll viewport")
	}
	before := m.msgsView.YOffset()
	got := wheelOnce(m, tea.MouseWheelDown)
	if got.msgsView.YOffset() != before {
		t.Errorf("wheel scrolled behind a non-scrolling modal: msgsView YOffset=%d, want %d",
			got.msgsView.YOffset(), before)
	}
}

// TestWheelSheetClosedMidBurst: the coalesced delta is resolved to a target at
// flush time, so a sheet dismissed between the gesture and the flush drops it
// instead of letting it fall through to the transcript.
func TestWheelSheetClosedMidBurst(t *testing.T) {
	m := wheelSheetModel()
	m.openKeysSheet()
	m.keysSheetView.SetContent(longBody())
	before := m.msgsView.YOffset()

	out, _ := m.handleMouseWheel(wheel(tea.MouseWheelDown))
	got := out.(Model)
	got.closeKeysSheet()
	out, _ = got.update(wheelFlushMsg{})
	got = out.(Model)

	if got.msgsView.YOffset() != before {
		t.Errorf("pending delta fell through to the transcript: msgsView YOffset=%d, want %d",
			got.msgsView.YOffset(), before)
	}
}
