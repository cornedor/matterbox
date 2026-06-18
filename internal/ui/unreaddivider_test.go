package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

const unreadLabel = "unread messages"

// pPost builds a post with an Id, create_at, and author for divider tests.
func pPost(id string, createAt int64, userID string) *model.Post {
	return &model.Post{Id: id, CreateAt: createAt, UserId: userID}
}

// dividerModel builds a renderable model whose current user is "me" and whose
// unread boundary is frozen at `boundary`.
func dividerModel(posts []*model.Post, boundary int64) Model {
	m := pagingModel(posts, len(posts)-1)
	m.me = &model.User{Id: "me"}
	m.unreadBoundary = boundary
	return m
}

// dividerRow renders the model and returns the index of the row carrying the
// "unread messages" divider, or -1 if none. Fails if drawn more than once.
func dividerRow(t *testing.T, m *Model) int {
	t.Helper()
	m.renderMessages()
	row := -1
	for i, l := range strings.Split(m.msgsView.GetContent(), "\n") {
		if strings.Contains(l, unreadLabel) {
			if row != -1 {
				t.Fatalf("divider drawn more than once (rows %d and %d)", row, i)
			}
			row = i
		}
	}
	return row
}

// TestUnreadDividerAboveFirstUnread: with a boundary between two posts, the
// "unread messages" divider is drawn on the row immediately above the first
// post created after it, and msgRowStarts accounts for the extra row.
func TestUnreadDividerAboveFirstUnread(t *testing.T) {
	posts := []*model.Post{pPost("a", 100, "other"), pPost("b", 200, "other"), pPost("c", 300, "other")}
	m := dividerModel(posts, 150) // b (200) is the first unread

	div := dividerRow(t, &m)
	if div == -1 {
		t.Fatalf("no unread divider rendered:\n%s", m.msgsView.GetContent())
	}
	lines := strings.Split(m.msgsView.GetContent(), "\n")
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
	posts := []*model.Post{pPost("a", 100, "other"), pPost("b", 200, "other")}
	m := dividerModel(posts, 0)
	if div := dividerRow(t, &m); div != -1 {
		t.Fatalf("divider drawn for a read channel at row %d", div)
	}
}

// TestNoUnreadDividerAtTop: when every loaded post is unread (boundary older
// than all of them) the divider would land at the very top, which carries no
// information, so it is suppressed.
func TestNoUnreadDividerAtTop(t *testing.T) {
	posts := []*model.Post{pPost("a", 100, "other"), pPost("b", 200, "other")}
	m := dividerModel(posts, 50)
	if div := dividerRow(t, &m); div != -1 {
		t.Fatalf("divider drawn at top of list at row %d", div)
	}
}

// TestNoUnreadDividerAboveOwnSentMessage reproduces the reported bug: the read
// boundary has drifted past every loaded post, so at open there is nothing to
// mark. When the user then sends a message it must NOT sprout an "unread
// messages" divider above their own post.
func TestNoUnreadDividerAboveOwnSentMessage(t *testing.T) {
	posts := []*model.Post{pPost("a", 100, "other"), pPost("b", 200, "other")}
	m := dividerModel(posts, 250) // boundary past both loaded posts → nothing to mark

	if div := dividerRow(t, &m); div != -1 {
		t.Fatalf("divider drawn before any unread post existed (row %d)", div)
	}
	// User sends a message; its echo lands as a post newer than the boundary.
	m.posts = append(m.posts, pPost("mine", 300, "me"))
	if div := dividerRow(t, &m); div != -1 {
		t.Fatalf("divider drawn above the user's own sent message (row %d)", div)
	}
}

// TestUnreadDividerFrozenAfterSend: a divider legitimately shown above the
// first unread post stays anchored there after the user sends a message, never
// jumping to the new post.
func TestUnreadDividerFrozenAfterSend(t *testing.T) {
	posts := []*model.Post{pPost("a", 100, "other"), pPost("b", 200, "other")}
	m := dividerModel(posts, 150) // b is the first unread

	if div := dividerRow(t, &m); div == -1 {
		t.Fatalf("expected a divider above the first unread post")
	}
	bStart := m.msgRowStarts[1]
	m.posts = append(m.posts, pPost("mine", 300, "me"))
	div := dividerRow(t, &m)
	if div == -1 {
		t.Fatalf("divider vanished after sending a message")
	}
	if got := m.msgRowStarts[1]; got != div+1 || got != bStart {
		t.Errorf("divider moved after send: now above row %d (post b at %d, was %d)", div, got, bStart)
	}
}
