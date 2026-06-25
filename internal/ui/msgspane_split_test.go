package ui

import (
	"strconv"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestBorderSplitIdentity proves the load-bearing identity behind
// renderMessagesPane's split: a single left+bottom-bordered box around stacked
// content is byte-identical to an upper box (left border only) concatenated with
// a lower box (left+bottom border) holding the tail of that content — provided
// the upper box's height equals its content's row count (so it adds no padding
// of its own). Covers both exact-fill and slack (content shorter than the box,
// so the bottom pad must land in the same place) cases.
func TestBorderSplitIdentity(t *testing.T) {
	cases := []struct{ w, h, upperRows, nLower int }{
		{60, 20, 12, 4},   // slack: 16 content rows in a 19-row inner area
		{40, 10, 6, 3},    // exact fill
		{100, 30, 20, 9},  // exact fill, wide
		{30, 8, 5, 1},     // tiny lower
		{80, 25, 10, 2},   // slack, large gap
	}
	mk := func(prefix string, n int) []string {
		s := make([]string, n)
		for i := range s {
			s[i] = prefix + strconv.Itoa(i)
		}
		return s
	}
	for _, c := range cases {
		upperLines := mk("u", c.upperRows)
		lowerLines := mk("low", c.nLower)
		all := append(append([]string{}, upperLines...), lowerLines...)

		single := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().
			Width(c.w-1).Height(c.h).BorderForeground(dimColor).
			Render(lipgloss.JoinVertical(lipgloss.Left, all...))

		upper := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().UnsetBorderBottom().
			Width(c.w-1).Height(c.upperRows).BorderForeground(dimColor).
			Render(lipgloss.JoinVertical(lipgloss.Left, upperLines...))
		lower := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().
			Width(c.w-1).Height(c.h-c.upperRows).BorderForeground(dimColor).
			Render(lipgloss.JoinVertical(lipgloss.Left, lowerLines...))
		split := upper + "\n" + lower

		if single != split {
			t.Errorf("w=%d h=%d upperRows=%d nLower=%d: split != single box\n--- single ---\n%q\n--- split ---\n%q",
				c.w, c.h, c.upperRows, c.nLower, single, split)
		}
	}
}

// TestMsgsPaneCacheMatchesUncached asserts the scrollback (upper-box) memo never
// changes a single rendered byte: the cached full frame must always equal the
// frame a Model with no viewCache produces, across every mutation that should
// invalidate the cache (typing, scrolling, focus change, content rebuild). A
// fingerprint gap would surface here as a stale cached frame diverging from the
// fresh one.
func TestMsgsPaneCacheMatchesUncached(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 120, 40
	m.focus = focusInput
	posts, names := benchPosts(80)
	m.posts = posts
	m.userNames = names
	m.postIdx = len(posts) - 1
	m.resizeMessagesViewport()

	check := func(label string) {
		t.Helper()
		// Cached: force a fresh render (update() would have cleared this live).
		if m.vcache != nil {
			m.vcache.viewValid = false
		}
		got := m.viewContent()
		// Uncached: a value copy with no viewCache renders everything from
		// scratch. The copy shares maps/slices (read-only here); nil-ing vcache
		// on the copy doesn't touch the original's caches.
		un := m
		un.vcache = nil
		want := un.viewContent()
		if got != want {
			t.Fatalf("%s: cached frame != uncached frame", label)
		}
	}

	check("initial")

	m.input.SetValue("hello")
	m.syncInputHeight()
	check("typed one line")

	m.input.SetValue("hello\nworld\nthree\nfour")
	m.syncInputHeight()
	check("typed grows input (shrinks viewport)")

	m.input.SetValue("hello")
	m.syncInputHeight()
	check("typed shrinks back")

	m.msgsView.SetYOffset(25)
	check("scrolled up")

	m.focus = focusMessages
	m.renderMessages()
	check("focus messages (selection bar)")

	m.posts = m.posts[:30]
	m.postIdx = 10
	m.renderMessages()
	check("content rebuild")
}
