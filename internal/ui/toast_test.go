package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// A toast's timer must only ever clear the toast that set it. Two in a row and
// the first timer would otherwise take the second one down early.
func TestToastTimerOnlyClearsItsOwn(t *testing.T) {
	var m Model
	m.showToast("first", "")
	stale := m.toast.gen
	m.showToast("second", "")

	m.expireToast(stale)
	if m.toast.title != "second" {
		t.Errorf("toast = %q, want the older timer ignored", m.toast.title)
	}
	m.expireToast(m.toast.gen)
	if m.toast.active() {
		t.Errorf("toast = %q, want its own timer to take it down", m.toast.title)
	}
}

// A dismissal has to survive the timer still in flight behind it — otherwise a
// second toast raised in the meantime is cleared by the first one's tick.
func TestDismissToastOutlivesItsTimer(t *testing.T) {
	var m Model
	m.showToast("news", "")
	pending := m.toast.gen
	m.dismissToast()
	if m.toast.active() {
		t.Fatalf("toast = %q after dismiss, want it down", m.toast.title)
	}

	m.showToast("later", "")
	m.expireToast(pending)
	if m.toast.title != "later" {
		t.Errorf("toast = %q, want the dismissed toast's tick to be a no-op", m.toast.title)
	}
}

// The box floats over a pane. It may cover cells, never add or drop any: a row
// that grows shoves the pane's right border and the scrollbar out of column.
func TestStampBlockKeepsRowWidths(t *testing.T) {
	rows := []string{
		"│ general        │ hello there            │",
		"│ random         │ and a second line      │",
		"│ off-topic      │ third                  │",
	}
	view := strings.Join(rows, "\n")
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Render("hi")

	got := stampBlock(view, box, 0, 2)
	gotRows := strings.Split(got, "\n")
	if len(gotRows) != len(rows) {
		t.Fatalf("got %d rows, want %d", len(gotRows), len(rows))
	}
	for i, r := range gotRows {
		if want := lipgloss.Width(rows[i]); lipgloss.Width(r) != want {
			t.Errorf("row %d width = %d, want %d", i, lipgloss.Width(r), want)
		}
	}
	if !strings.Contains(got, "hi") {
		t.Error("stamped view does not contain the box")
	}
	// Row 0's tail is past the box, so it survives; the untouched rows below the
	// box come back byte-for-byte.
	if !strings.Contains(gotRows[0], "hello there") {
		t.Errorf("row 0 = %q, want the text right of the box kept", gotRows[0])
	}
}

// A block taller than the view must stamp what fits rather than extending it.
func TestStampBlockClipsToTheView(t *testing.T) {
	got := stampBlock("one row", "a\nb\nc", 0, 0)
	if strings.Count(got, "\n") != 0 {
		t.Errorf("got %q, want the view's own height kept", got)
	}
}

// Two terminals with no room for the box: it is skipped rather than truncated
// into nonsense or drawn over the whole pane.
func TestRenderToastNeedsRoom(t *testing.T) {
	base := func() Model {
		var m Model
		m.width, m.height = 100, 40
		m.showToast(updateNoticeTitle(rel("v9.9.9")), updateNoticeHint)
		return m
	}

	m := base()
	if box := m.renderToast(30); box == "" {
		t.Fatal("renderToast drew nothing at 100x40, want the box")
	} else if lipgloss.Width(box) > m.width {
		t.Errorf("box width = %d, want it inside the terminal (%d)", lipgloss.Width(box), m.width)
	}

	narrow := base()
	narrow.width = 18
	if box := narrow.renderToast(30); box != "" {
		t.Errorf("renderToast drew %q in an 18-column terminal, want it skipped", box)
	}

	short := base()
	if box := short.renderToast(3); box != "" {
		t.Errorf("renderToast drew %q with a 3-row body, want it skipped", box)
	}
}

// The box owns the cells it covers: a click on it dismisses the notice instead
// of reaching the sidebar row hidden underneath.
func TestToastZoneOwnsItsCells(t *testing.T) {
	var m Model
	m.width, m.height = 100, 40
	m.vcache = &viewCache{}
	m.showToast(updateNoticeTitle(rel("v9.9.9")), updateNoticeHint)

	box := m.renderToast(30)
	if box == "" {
		t.Fatal("renderToast drew nothing, want the box")
	}
	m.armToastZone(box)

	col := m.toastCol(box)
	if want := m.width - toastInset - lipgloss.Width(box); col != want {
		t.Errorf("toastCol = %d, want the box right-anchored at %d", col, want)
	}
	inside := [][2]int{
		{col, tabsHeight + toastTop},
		{col + lipgloss.Width(box) - 1, tabsHeight + toastTop + lipgloss.Height(box) - 1},
	}
	for _, c := range inside {
		if h := m.hitTest(c[0], c[1]); h.zone != hitToast {
			t.Errorf("hitTest(%d,%d) = %v, want hitToast", c[0], c[1], h.zone)
		}
		if hv := m.hoverAt(c[0], c[1]); hv.zone != hitNone {
			t.Errorf("hoverAt(%d,%d) = %v, want nothing hoverable", c[0], c[1], hv.zone)
		}
	}
	// One row past the bottom edge is the transcript again, and one column left
	// of the box is too.
	if h := m.hitTest(col, tabsHeight+toastTop+lipgloss.Height(box)); h.zone == hitToast {
		t.Error("the zone extends past the box's last row")
	}
	if h := m.hitTest(col-1, tabsHeight+toastTop); h.zone == hitToast {
		t.Error("the zone extends past the box's left edge")
	}

	m.armToastZone("")
	if h := m.hitTest(col, tabsHeight+toastTop); h.zone == hitToast {
		t.Error("the zone survived a frame without a toast")
	}
}
