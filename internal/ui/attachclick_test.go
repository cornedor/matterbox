package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"
)

// attachClickModel is a painted channel view carrying two composer chips.
func attachClickModel(t *testing.T) Model {
	t.Helper()
	m := mouseModel([]*model.Post{{Id: "p1", Message: "hi", UserId: "u", CreateAt: 1}})
	m.width, m.height = 120, 30
	m.attachments = []pendingAttachment{
		{id: "a1", filename: "one.png", mime: "image/png", size: 1024, state: attUploaded, localPath: "/tmp/one.png", spinner: newAttachmentSpinner()},
		{id: "a2", filename: "two.txt", mime: "text/plain", size: 2048, state: attUploaded, localPath: "/tmp/two.txt", spinner: newAttachmentSpinner()},
	}
	m.layoutPanes()
	m.renderViewContent()
	return m
}

// closeCell locates the nth × in the rendered frame, in screen cells.
func closeCells(t *testing.T, frame string) [][2]int {
	t.Helper()
	var out [][2]int
	for y, line := range strings.Split(frame, "\n") {
		plain := ansi.Strip(line)
		for x, r := range []rune(plain) {
			if r == '×' {
				out = append(out, [2]int{x, y})
			}
		}
	}
	return out
}

// TestAttachChipHitZones: the zones recorded by the render map back to the
// cells the × buttons actually occupy, and the chip body beside them is a
// body hit (line 0) on the right chip.
func TestAttachChipHitZones(t *testing.T) {
	m := attachClickModel(t)
	frame := m.renderViewContent()
	cells := closeCells(t, frame)
	if len(cells) != 2 {
		t.Fatalf("want 2 × buttons in the frame, got %d", len(cells))
	}
	for i, c := range cells {
		h := m.hitTest(c[0], c[1])
		if h.zone != hitAttachment || h.idx != i || h.line != 1 {
			t.Errorf("× %d at (%d,%d): got zone=%v idx=%d line=%d", i, c[0], c[1], h.zone, h.idx, h.line)
		}
		// A cell well left of the × is the chip body, same chip.
		if h := m.hitTest(c[0]-3, c[1]); h.zone != hitAttachment || h.idx != i || h.line != 0 {
			t.Errorf("body of chip %d: got zone=%v idx=%d line=%d", i, h.zone, h.idx, h.line)
		}
	}
}

// TestAttachChipClickRemoves: a click on the × drops that chip.
func TestAttachChipClickRemoves(t *testing.T) {
	m := attachClickModel(t)
	cells := closeCells(t, m.renderViewContent())
	out, _ := m.handleMouseClick(click(tea.MouseLeft, cells[0][0], cells[0][1]))
	got := out.(Model)
	if len(got.attachments) != 1 || got.attachments[0].id != "a2" {
		t.Fatalf("want only a2 left, got %+v", got.attachments)
	}
}

// TestAttachChipClickSelects: a click on a chip body focuses the strip and
// selects that chip (no preview for a .txt).
func TestAttachChipClickSelects(t *testing.T) {
	m := attachClickModel(t)
	cells := closeCells(t, m.renderViewContent())
	out, _ := m.handleMouseClick(click(tea.MouseLeft, cells[1][0]-3, cells[1][1]))
	got := out.(Model)
	if got.focus != focusAttachments || got.attachmentIdx != 1 {
		t.Fatalf("focus=%v idx=%d", got.focus, got.attachmentIdx)
	}
	if len(got.attachments) != 2 {
		t.Fatalf("click on the body must not remove a chip")
	}
}

// TestAttachFocusWalk: ↑ in the composer stops at the chip strip first, and a
// second ↑ carries on into the transcript; ↓ comes back to the composer.
func TestAttachFocusWalk(t *testing.T) {
	m := attachClickModel(t)
	m.focus = focusInput
	m.input.Focus()
	up := tea.KeyPressMsg{Code: tea.KeyUp}

	out, _ := m.handleInputKey(up)
	got := out.(Model)
	if got.focus != focusAttachments || got.attachmentIdx != 0 {
		t.Fatalf("first ↑: focus=%v idx=%d, want the chip strip", got.focus, got.attachmentIdx)
	}
	out, _ = got.handleAttachmentsKey(up)
	got = out.(Model)
	if got.focus != focusMessages {
		t.Fatalf("second ↑: focus=%v, want the messages pane", got.focus)
	}

	out, _ = m.handleInputKey(up)
	got = out.(Model)
	out, _ = got.handleAttachmentsKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := out.(Model); got.focus != focusInput {
		t.Fatalf("↓: focus=%v, want the composer", got.focus)
	}
}

// TestAttachFocusWalkNoChips: with nothing attached ↑ still goes straight to
// the transcript.
func TestAttachFocusWalkNoChips(t *testing.T) {
	m := attachClickModel(t)
	m.attachments = nil
	m.focus = focusInput
	m.input.Focus()
	out, _ := m.handleInputKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := out.(Model); got.focus != focusMessages {
		t.Fatalf("focus=%v, want the messages pane", got.focus)
	}
}

// TestAttachmentPreviewable: only formats the preview modal can render.
func TestAttachmentPreviewable(t *testing.T) {
	cases := map[string]bool{"a.png": true, "a.svg": true, "a.txt": false, "a.pdf": false, "a.JPEG": true}
	for name, want := range cases {
		if got := attachmentPreviewable(pendingAttachment{filename: name}); got != want {
			t.Errorf("attachmentPreviewable(%q)=%v want %v", name, got, want)
		}
	}
	if !attachmentPreviewable(pendingAttachment{filename: "clip", mime: "image/png"}) {
		t.Error("MIME alone should be enough when the name has no extension")
	}
}

// TestAttachChipClickPreviews: clicking an image chip's body routes to the
// preview modal (here it can only report why the terminal can't render one,
// which is proof enough that the click reached it).
func TestAttachChipClickPreviews(t *testing.T) {
	m := attachClickModel(t)
	cells := closeCells(t, m.renderViewContent())
	out, _ := m.handleMouseClick(click(tea.MouseLeft, cells[0][0]-3, cells[0][1]))
	got := out.(Model)
	if !strings.Contains(got.status, "image preview") {
		t.Fatalf("status=%q, want the preview modal's report", got.status)
	}
	if got.focus != focusAttachments || got.attachmentIdx != 0 {
		t.Fatalf("focus=%v idx=%d", got.focus, got.attachmentIdx)
	}
}

// TestAttachCloseHover: the pointer lights up the × it is on, and nothing else
// on the chip.
func TestAttachCloseHover(t *testing.T) {
	m := attachClickModel(t)
	cells := closeCells(t, m.renderViewContent())
	out, _ := m.handleMouseMotion(motion(tea.MouseNone, cells[1][0], cells[1][1]))
	got := out.(Model)
	if got.hover.zone != hitAttachment || got.hover.idx != 1 {
		t.Fatalf("hover=%+v, want the second chip's ×", got.hover)
	}
	// Off the button (the chip body) drops the hover again.
	out, _ = got.handleMouseMotion(motion(tea.MouseNone, cells[1][0]-3, cells[1][1]))
	if h := out.(Model).hover; h.zone != hitNone {
		t.Fatalf("hover=%+v over the chip body, want none", h)
	}
}

// TestAttachBarFocusLayout: the strip's hint row (drawn only while it has
// focus) is accounted for in the layout, so the pane keeps its height and its
// bottom border through the whole walk — focus in, remove a chip, focus back
// out. A missing relayout showed as a blank row above the chips and, on the way
// back, a phantom row under the composer.
func TestAttachBarFocusLayout(t *testing.T) {
	m := attachClickModel(t)
	m.focus = focusInput
	m.input.Focus()

	want := strings.Count(m.renderViewContent(), "\n")
	steps := []struct {
		name string
		msg  tea.Msg
	}{
		{"focus the strip", tea.KeyPressMsg{Code: tea.KeyUp}},
		{"remove a chip", tea.KeyPressMsg{Code: 'x', Text: "x"}},
		{"back to the composer", tea.KeyPressMsg{Code: tea.KeyDown}},
	}
	for _, s := range steps {
		out, _ := m.Update(s.msg)
		m = out.(Model)
		frame := m.renderViewContent()
		if got := strings.Count(frame, "\n"); got != want {
			t.Fatalf("%s: frame is %d rows, want %d", s.name, got, want)
		}
		lines := strings.Split(frame, "\n")
		// The body's bottom border is the row above the footer; a strip the
		// layout didn't budget for pushes it apart.
		if bottom := ansi.Strip(lines[len(lines)-2]); !strings.HasPrefix(bottom, "└") || !strings.HasSuffix(strings.TrimRight(bottom, " "), "┘") {
			t.Fatalf("%s: bottom border broken: %q", s.name, bottom)
		}
	}
}
