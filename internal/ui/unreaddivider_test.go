package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

const unreadLabel = "unread messages"

// renderWithBoundary builds a renderable model for the given posts, freezes the
// unread boundary at `boundary`, renders, and returns the model plus the
// viewport content split into visual rows.
func renderWithBoundary(posts []*model.Post, boundary int64) (Model, []string) {
	m := pagingModel(posts, len(posts)-1)
	m.unreadBoundary = boundary
	m.renderMessages()
	return m, strings.Split(m.msgsView.GetContent(), "\n")
}

// TestUnreadDividerAboveFirstUnread: with a boundary between two posts, the
// "unread messages" divider is drawn on the row immediately above the first
// post created after it, and msgRowStarts accounts for the extra row.
func TestUnreadDividerAboveFirstUnread(t *testing.T) {
	posts := []*model.Post{p("a", 100), p("b", 200), p("c", 300)}
	m, lines := renderWithBoundary(posts, 150) // b (200) is the first unread

	div := -1
	for i, l := range lines {
		if strings.Contains(l, unreadLabel) {
			if div != -1 {
				t.Fatalf("divider drawn more than once (rows %d and %d)", div, i)
			}
			div = i
		}
	}
	if div == -1 {
		t.Fatalf("no unread divider rendered:\n%s", strings.Join(lines, "\n"))
	}
	// The divider is a centered rule, not bare text.
	if !strings.Contains(lines[div], "─") {
		t.Errorf("divider row is not a rule: %q", lines[div])
	}
	// It sits in the gap right before post b (index 1); rowStarts[1] still
	// points at b's real first line, one row below the divider.
	if got := m.msgRowStarts[1]; got != div+1 {
		t.Errorf("divider not directly above first unread post: divider row %d, post b starts at %d", div, got)
	}
	// Row accounting stays consistent: the final rowStart equals the line count.
	if total := m.msgRowStarts[len(m.msgRowStarts)-1]; total != len(lines) {
		t.Errorf("row geometry off: total rows %d, content lines %d", total, len(lines))
	}
}

// TestNoUnreadDividerWhenRead: a zero boundary (channel opened already-read)
// draws no divider.
func TestNoUnreadDividerWhenRead(t *testing.T) {
	posts := []*model.Post{p("a", 100), p("b", 200), p("c", 300)}
	_, lines := renderWithBoundary(posts, 0)
	for _, l := range lines {
		if strings.Contains(l, unreadLabel) {
			t.Fatalf("divider drawn for a read channel: %q", l)
		}
	}
}

// TestNoUnreadDividerAtTop: when every loaded post is unread (boundary older
// than all of them) the divider would land at the very top, which carries no
// information, so it is suppressed.
func TestNoUnreadDividerAtTop(t *testing.T) {
	posts := []*model.Post{p("a", 100), p("b", 200), p("c", 300)}
	_, lines := renderWithBoundary(posts, 50)
	for _, l := range lines {
		if strings.Contains(l, unreadLabel) {
			t.Fatalf("divider drawn at top of list: %q", l)
		}
	}
}
