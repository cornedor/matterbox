package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// listWindow keeps the selection visible: the whole list when it fits, else
// a height-row window centred on idx and pinned to the ends.
func TestListWindow(t *testing.T) {
	cases := []struct {
		n, idx, h  int
		start, end int
	}{
		{5, 2, 10, 0, 5},    // fits
		{5, 4, 5, 0, 5},     // exactly fits
		{20, 0, 5, 0, 5},    // top
		{20, 10, 5, 8, 13},  // centred
		{20, 19, 5, 15, 20}, // bottom, pinned
		{20, 18, 5, 15, 20}, // near bottom, pinned
		{20, -3, 5, 0, 5},   // out of range low → clamped
		{20, 99, 5, 15, 20}, // out of range high → clamped
		{3, 1, 0, 0, 3},     // no height → everything
	}
	for _, c := range cases {
		s, e := listWindow(c.n, c.idx, c.h)
		if s != c.start || e != c.end {
			t.Errorf("listWindow(%d,%d,%d) = [%d,%d), want [%d,%d)", c.n, c.idx, c.h, s, e, c.start, c.end)
		}
		if c.h > 0 && c.n > c.h {
			idx := max(0, min(c.idx, c.n-1))
			if idx < s || idx >= e {
				t.Errorf("listWindow(%d,%d,%d) window [%d,%d) hides the selection", c.n, c.idx, c.h, s, e)
			}
		}
	}
}

// Every sheet — keys, templates, kaomoji, saved messages, text popup — is the
// same modal: identical outer width and height as the keys cheatsheet at the
// same terminal size, so they open as one frame rather than five.
func TestSheetsShareTheKeysModalFrame(t *testing.T) {
	m := newRenderableModel()
	m.width, m.height = 120, 40
	m.openKeysSheet()
	ref := m.renderKeysSheetPopup()
	m.closeKeysSheet()
	refW, refH := lipgloss.Width(ref), lipgloss.Height(ref)
	if refW != modalMaxWidth || refH != m.height-4 {
		t.Fatalf("keys sheet frame %dx%d, want %dx%d", refW, refH, modalMaxWidth, m.height-4)
	}

	sheets := []struct {
		name string
		open func()
		draw func() string
	}{
		{"templates", func() { m.templates = map[string]string{"a": "alpha"}; m.openTemplatePicker() }, m.renderTemplatePicker},
		{"templates-empty", func() { m.templates = map[string]string{}; m.openTemplatePicker() }, m.renderTemplatePicker},
		{"kaomoji", m.openKaomojiPicker, m.renderKaomojiPicker},
		{"saved-loading", func() { m.savedPosts = savedPostsState{active: true, loading: true} }, m.renderSavedPosts},
		{"saved-list", func() {
			m.savedPostIDs = map[string]bool{"s1": true}
			m.savedPosts = savedPostsState{active: true, items: []savedItem{{post: &model.Post{Id: "s1", ChannelId: "c1"}, channel: "#c1", text: "x"}}}
		}, m.renderSavedPosts},
		{"text", func() { m.openTextPopup("Message stats", "one\ntwo") }, m.renderTextPopup},
	}
	for _, s := range sheets {
		s.open()
		got := s.draw()
		if w, h := lipgloss.Width(got), lipgloss.Height(got); w != refW || h != refH {
			t.Errorf("%s sheet is %dx%d, keys sheet is %dx%d", s.name, w, h, refW, refH)
		}
		if !strings.Contains(got, "─") {
			t.Errorf("%s sheet has no title rule", s.name)
		}
	}
	// And they all resize with the terminal like the keys sheet does.
	m.width, m.height = 60, 20
	m.openKeysSheet()
	ref = m.renderKeysSheetPopup()
	m.openKaomojiPicker()
	if got := m.renderKaomojiPicker(); lipgloss.Width(got) != lipgloss.Width(ref) || lipgloss.Height(got) != lipgloss.Height(ref) {
		t.Errorf("after resize: kaomoji %dx%d vs keys %dx%d", lipgloss.Width(got), lipgloss.Height(got), lipgloss.Width(ref), lipgloss.Height(ref))
	}
}

// A long list windows around the selection: the selected row is drawn (in
// the selection style) and the frame height doesn't grow with the list.
func TestListModalKeepsSelectionVisible(t *testing.T) {
	m := newRenderableModel()
	m.width, m.height = 100, 20
	rows := make([]string, 50)
	for i := range rows {
		rows[i] = "row-" + string(rune('A'+i%26)) + strings.Repeat("x", i)
	}
	_, innerH := m.modalDims()
	got := m.renderListModal("T", "h", "", len(rows), 40, func(i int) string { return rows[i] })
	if lipgloss.Height(got) != m.height-4 {
		t.Fatalf("frame height %d, want %d", lipgloss.Height(got), m.height-4)
	}
	if !strings.Contains(got, rows[40][:8]) {
		t.Fatalf("selected row 40 not in the window:\n%s", got)
	}
	if strings.Contains(got, rows[0]) {
		t.Fatalf("row 0 should be scrolled out (window is %d rows)", innerH)
	}
	// Only the window's rows are rendered — a long list costs its window.
	calls := 0
	m.renderListModal("T", "h", "", len(rows), 40, func(i int) string { calls++; return rows[i] })
	if calls != innerH {
		t.Fatalf("row() called %d times for a %d-row window", calls, innerH)
	}
	// Selection styling lands on the selected row only.
	sel := selectedRow.Render(truncate(rows[40], m.modalInnerWidth()))
	if !strings.Contains(got, sel) {
		t.Fatalf("selected row not rendered with selectedRow style")
	}
}

// renderModal hand-draws the frame; it has to be exactly what the lipgloss
// Border+Width style it replaced would draw — same bytes, styles included —
// for every body shape a sheet produces (plain, styled rows, wide runes,
// empty lines, a trailing newline). Over-wide lines are the one deliberate
// difference (truncated, not wrapped — see below) and are kept out of the
// byte comparison.
func TestRenderModalMatchesLipgloss(t *testing.T) {
	m := newRenderableModel()
	for _, size := range [][2]int{{120, 40}, {60, 20}, {40, 12}} {
		m.width, m.height = size[0], size[1]
		outerW, _ := m.modalDims()
		inner := m.modalInnerWidth()
		bodies := map[string]string{
			"plain":     "hello\nworld",
			"styled":    "a\n" + selectedRow.Render("selected row") + "\nb",
			"wide":      "日本語のテキスト ʕ•ᴥ•ʔ\n¯\\_(ツ)_/¯",
			"empty":     "",
			"blank":     "\n\n",
			"trailing":  "one\ntwo\n",
			"fullwidth": strings.Repeat("x", inner),
		}
		for name, body := range bodies {
			for _, hint := range []string{"", "esc/q close"} {
				dim := lipgloss.NewStyle().Foreground(dimColor)
				head := titleStyle.Render("Title")
				if hint != "" {
					head += "  " + dim.Render(hint)
				}
				want := lipgloss.NewStyle().
					Border(border).
					BorderForeground(focusedColor).
					Padding(0, 1).
					Width(outerW).
					Render(strings.Join([]string{head, dim.Render(strings.Repeat("─", inner)), body}, "\n"))
				got := m.renderModal("Title", hint, body)
				if got != want {
					t.Errorf("%dx%d %s hint=%q:\n got %q\nwant %q", size[0], size[1], name, hint, got, want)
				}
			}
		}
	}
}

// A line wider than the frame — a long title+hint on a narrow terminal, or a
// row a caller didn't truncate — is cut to the frame, so the sheet keeps the
// height its viewport was sized for (lipgloss would have wrapped it onto a
// second row and grown the frame).
func TestRenderModalTruncatesOverWideLines(t *testing.T) {
	m := newRenderableModel()
	m.width, m.height = 31, 12
	got := m.renderModal("Keyboard shortcuts", "esc/q close · ↑/↓ scroll", "one\n"+strings.Repeat("y", 80))
	if h := lipgloss.Height(got); h != 6 { // 2 borders + head + rule + 2 body rows
		t.Fatalf("height = %d, want 6 (borders + head + rule + 2 body rows): %q", h, got)
	}
	outerW, _ := m.modalDims()
	for i, l := range strings.Split(got, "\n") {
		if w := lipgloss.Width(l); w != outerW {
			t.Errorf("row %d is %d wide, want %d: %q", i, w, outerW, l)
		}
	}
}
