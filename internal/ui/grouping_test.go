package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
)

// post builds a minimal post for the grouping tests. minute is the
// create-time in whole minutes (so gaps are easy to read); user is the author.
func groupPost(user string, minute int) *model.Post {
	return &model.Post{
		UserId:   user,
		CreateAt: int64(minute) * 60 * 1000,
	}
}

// TestGroupWithPrev exercises the header-suppression decision: same author
// within the window collapses, anything that needs the header keeps it.
func TestGroupWithPrev(t *testing.T) {
	m := Model{
		groupWindow: 120 * time.Second, // two minutes
		userNames:   map[string]string{"john": "john", "corne": "corne"},
	}

	tests := []struct {
		name string
		cur  *model.Post
		prev *model.Post
		want bool
	}{
		{"no prev (run start)", groupPost("john", 13*60), nil, false},
		{"same author, same minute", groupPost("john", 13*60+0), groupPost("john", 13*60+0), true},
		{"same author within window", groupPost("john", 13*60+2), groupPost("john", 13*60+0), true},
		{"same author past window", groupPost("corne", 13*60+4), groupPost("corne", 13*60+1), false},
		{"different author", groupPost("corne", 13*60+0), groupPost("john", 13*60+0), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.groupWithPrev(tc.cur, tc.prev, false); got != tc.want {
				t.Errorf("groupWithPrev = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGroupWithPrevAffordances checks that posts whose header carries
// information (thread hints, an edited tag) are never collapsed in the main
// pane, while the thread pane still groups consecutive replies.
func TestGroupWithPrevAffordances(t *testing.T) {
	m := Model{groupWindow: 120 * time.Second, userNames: map[string]string{"john": "john"}}
	base := func() *model.Post { return groupPost("john", 100) }

	t.Run("current is a reply", func(t *testing.T) {
		cur := base()
		cur.RootId = "root1"
		if m.groupWithPrev(cur, base(), false) {
			t.Error("a reply should keep its ↳ header in the main pane")
		}
	})
	t.Run("previous is a reply", func(t *testing.T) {
		prev := base()
		prev.RootId = "root1"
		if m.groupWithPrev(base(), prev, false) {
			t.Error("a message after an inline reply should not merge into it")
		}
	})
	t.Run("current heads a thread", func(t *testing.T) {
		cur := base()
		cur.ReplyCount = 3
		if m.groupWithPrev(cur, base(), false) {
			t.Error("a thread root should keep its ↪ header")
		}
	})
	t.Run("current was edited", func(t *testing.T) {
		cur := base()
		cur.EditAt = cur.CreateAt + 1000
		if m.groupWithPrev(cur, base(), false) {
			t.Error("an edited message should keep its header so 'edited' shows")
		}
	})
	t.Run("current is pinned", func(t *testing.T) {
		cur := base()
		cur.IsPinned = true
		if m.groupWithPrev(cur, base(), false) {
			t.Error("a pinned message should keep its header so the pin mark shows")
		}
	})
	t.Run("thread replies still group", func(t *testing.T) {
		cur, prev := base(), base()
		cur.RootId, prev.RootId = "root1", "root1"
		if !m.groupWithPrev(cur, prev, true) {
			t.Error("consecutive same-author thread replies should group")
		}
	})
}

// TestGroupWithPrevDisabled confirms a zero window turns grouping off so every
// message keeps its header (the explicit group_message_seconds: 0 behaviour).
func TestGroupWithPrevDisabled(t *testing.T) {
	m := Model{groupWindow: 0, userNames: map[string]string{"john": "john"}}
	if m.groupWithPrev(groupPost("john", 100), groupPost("john", 100), false) {
		t.Error("grouping should be off when groupWindow is 0")
	}
}

// TestRenderPostLinesGrouped checks the wiring end to end: renderPostLines
// drops the name/time header when grouped, and keeps it otherwise.
func TestRenderPostLinesGrouped(t *testing.T) {
	m := Model{
		emojiImg:  newEmojiImages("off", false),
		userNames: map[string]string{"u1": "john"},
		posts:     []*model.Post{{Id: "p1", UserId: "u1", Message: "how are you"}},
	}
	m.msgsView = viewport.New()
	m.msgsView.SetWidth(40)
	m.msgsView.SetHeight(10)

	headed, _ := m.renderPostLines(m.posts[0], false)
	if !strings.Contains(strings.Join(headed, "\n"), "john") {
		t.Fatal("ungrouped post should show the author name")
	}

	grouped, _ := m.renderPostLines(m.posts[0], true)
	if strings.Contains(strings.Join(grouped, "\n"), "john") {
		t.Fatal("grouped post should omit the author name header")
	}
	if !strings.Contains(strings.Join(grouped, "\n"), "how are you") {
		t.Fatal("grouped post should still render its body")
	}
}
