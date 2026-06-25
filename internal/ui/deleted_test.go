package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
)

// deletedEvent builds a post_deleted WebSocket event carrying p, the way the
// server broadcasts it (JSON under the "post" data key).
func deletedEvent(p *model.Post) *model.WebSocketEvent {
	ev := model.NewWebSocketEvent(model.WebsocketEventPostDeleted, "", "", "me", nil, "")
	b, _ := json.Marshal(p)
	ev.Add("post", string(b))
	return ev
}

// TestGroupWithPrevDeleted: a tombstone keeps its own header, and the post
// directly below one never folds up into it.
func TestGroupWithPrevDeleted(t *testing.T) {
	m := Model{groupWindow: 120 * time.Second, userNames: map[string]string{"john": "john"}}
	base := func() *model.Post { return groupPost("john", 100) }

	t.Run("current is a tombstone", func(t *testing.T) {
		cur := base()
		cur.DeleteAt = cur.CreateAt + 1000
		if m.groupWithPrev(cur, base(), false) {
			t.Error("a deleted message should keep its own header")
		}
	})
	t.Run("previous is a tombstone", func(t *testing.T) {
		prev := base()
		prev.DeleteAt = prev.CreateAt + 1000
		if m.groupWithPrev(base(), prev, false) {
			t.Error("a message after a tombstone should not merge into it")
		}
	})
}

// TestRenderPostLinesDeleted: a removed post renders the dim tombstone in place
// of its original content, even when it would otherwise be a grouped
// continuation.
func TestRenderPostLinesDeleted(t *testing.T) {
	p := &model.Post{Id: "p1", UserId: "u1", Message: "the secret plans", DeleteAt: 1234}
	m := Model{
		emojiImg:  newEmojiImages("off", false),
		userNames: map[string]string{"u1": "john"},
		posts:     []*model.Post{p},
	}
	m.msgsView = viewport.New()
	m.msgsView.SetWidth(40)
	m.msgsView.SetHeight(10)

	for _, grouped := range []bool{false, true} {
		lines, _ := m.renderPostLines(p, grouped)
		out := strings.Join(lines, "\n")
		if !strings.Contains(out, "message deleted") {
			t.Errorf("grouped=%v: tombstone should show the deleted indicator, got %q", grouped, out)
		}
		if strings.Contains(out, "secret plans") {
			t.Errorf("grouped=%v: tombstone must not render the removed content, got %q", grouped, out)
		}
		// The author/time header keeps the gap attributable.
		if !strings.Contains(out, "john") {
			t.Errorf("grouped=%v: tombstone should keep the author header, got %q", grouped, out)
		}
	}
}

// TestMarkPostDeleted strips the content a tombstone must never expose and
// always lands a nonzero DeleteAt so the renderer and the line-cache
// fingerprint treat the post as removed.
func TestMarkPostDeleted(t *testing.T) {
	t.Run("event carries DeleteAt", func(t *testing.T) {
		p := &model.Post{
			Message:  "gone",
			FileIds:  []string{"f1"},
			Metadata: &model.PostMetadata{Files: []*model.FileInfo{{Id: "f1"}}},
		}
		markPostDeleted(p, 5000)
		if p.DeleteAt != 5000 {
			t.Errorf("DeleteAt = %d, want 5000", p.DeleteAt)
		}
		if p.Message != "" || p.Metadata != nil || p.FileIds != nil {
			t.Errorf("content not stripped: msg=%q meta=%v files=%v", p.Message, p.Metadata, p.FileIds)
		}
	})
	t.Run("falls back when event DeleteAt is zero", func(t *testing.T) {
		p := &model.Post{UpdateAt: 42}
		markPostDeleted(p, 0)
		if p.DeleteAt != 42 {
			t.Errorf("DeleteAt = %d, want UpdateAt fallback 42", p.DeleteAt)
		}
	})
	t.Run("never lands on zero", func(t *testing.T) {
		p := &model.Post{}
		markPostDeleted(p, 0)
		if p.DeleteAt == 0 {
			t.Error("DeleteAt must be nonzero so the post reads as deleted")
		}
	})
}

// TestApplyPostDeletedLeavesTombstone: the post_deleted event leaves the post
// in place as a contentless tombstone rather than yanking it out of the
// transcript, so a live deletion no longer makes a message silently vanish.
func TestApplyPostDeletedLeavesTombstone(t *testing.T) {
	m := navModel() // openChannelID = c1
	m.emojiImg = newEmojiImages("off", false)
	m.userNames["u1"] = "john"
	m.posts = []*model.Post{
		{Id: "p1", ChannelId: "c1", UserId: "u1", CreateAt: 1000, Message: "first"},
		{Id: "p2", ChannelId: "c1", UserId: "u1", CreateAt: 2000, Message: "the secret plans"},
		{Id: "p3", ChannelId: "c1", UserId: "u1", CreateAt: 3000, Message: "third"},
	}
	m.postIdx = 2

	(&m).applyPostDeleted(deletedEvent(&model.Post{Id: "p2", ChannelId: "c1", DeleteAt: 2500}))

	if len(m.posts) != 3 {
		t.Fatalf("post count = %d, want 3 (tombstone kept in place)", len(m.posts))
	}
	if m.postIdx != 2 {
		t.Errorf("postIdx = %d, want 2 (selection unshifted)", m.postIdx)
	}
	p2 := m.posts[1]
	if p2.Id != "p2" {
		t.Fatalf("posts[1].Id = %q, want p2 (order preserved)", p2.Id)
	}
	if p2.DeleteAt == 0 {
		t.Error("deleted post should be flagged with a nonzero DeleteAt")
	}
	if p2.Message != "" {
		t.Errorf("deleted content lingered: %q", p2.Message)
	}

	lines, _ := m.renderPostLines(p2, false)
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "message deleted") {
		t.Errorf("tombstone should render the deleted indicator, got %q", out)
	}
	if strings.Contains(out, "secret plans") {
		t.Errorf("tombstone must not render the removed content, got %q", out)
	}
}

// TestMergeTombstonesCarriesOverDeleted: a full server reload (postsLoadedMsg)
// never includes deleted posts, so mergeTombstones must fold the on-screen
// tombstones back in (by time) while dropping optimistic stubs.
func TestMergeTombstonesCarriesOverDeleted(t *testing.T) {
	tomb := &model.Post{Id: "d1", CreateAt: 150, DeleteAt: 999}
	prev := []*model.Post{
		{Id: "a", CreateAt: 100},
		tomb,
		{Id: "", CreateAt: 175, Message: "optimistic stub"}, // empty Id: dropped
	}
	fetched := []*model.Post{
		{Id: "a", CreateAt: 100},
		{Id: "b", CreateAt: 200},
	}
	got := mergeTombstones(prev, fetched)
	if len(got) != 3 {
		t.Fatalf("want 3 posts (a, d1, b), got %d: %+v", len(got), got)
	}
	if got[0].Id != "a" || got[1].Id != "d1" || got[2].Id != "b" {
		t.Errorf("wrong merge/order: %s, %s, %s", got[0].Id, got[1].Id, got[2].Id)
	}
}

// TestMergeTombstonesPrefersFetched: if the fetch returns the same Id as a
// local tombstone, the server's (live) copy wins — no duplicate, no stale
// tombstone shadowing a resurrected post.
func TestMergeTombstonesPrefersFetched(t *testing.T) {
	prev := []*model.Post{{Id: "x", CreateAt: 100, DeleteAt: 999}}
	fetched := []*model.Post{{Id: "x", CreateAt: 100}}
	got := mergeTombstones(prev, fetched)
	if len(got) != 1 {
		t.Fatalf("want 1 post, got %d", len(got))
	}
	if got[0].DeleteAt != 0 {
		t.Errorf("fetched live copy should win over the local tombstone")
	}
}

// TestDeletionsSyncedFlipsOpenTranscript: a deletionsSyncedMsg for the open
// channel (offline-deletion sweep) flips the matching live post into a
// tombstone in-place, without a reopen.
func TestDeletionsSyncedFlipsOpenTranscript(t *testing.T) {
	m := navModel() // openChannelID = c1
	m.emojiImg = newEmojiImages("off", false)
	m.userNames["u1"] = "john"
	m.posts = []*model.Post{
		{Id: "p1", ChannelId: "c1", UserId: "u1", CreateAt: 1000, Message: "first"},
		{Id: "p2", ChannelId: "c1", UserId: "u1", CreateAt: 2000, Message: "removed offline"},
	}
	m.postIdx = 1

	out, _ := m.update(deletionsSyncedMsg{
		channelID: "c1",
		deleted:   []*model.Post{{Id: "p2", ChannelId: "c1", DeleteAt: 2500}},
	})
	got := out.(Model)

	if len(got.posts) != 2 {
		t.Fatalf("post count changed: %d", len(got.posts))
	}
	p2 := got.posts[1]
	if p2.DeleteAt == 0 {
		t.Error("offline deletion not flagged on the open transcript")
	}
	if p2.Message != "" {
		t.Errorf("offline-deleted content lingered: %q", p2.Message)
	}
}

// TestDeletionsSyncedIgnoresOtherChannel: a sweep result for a channel that
// isn't open touches nothing in the visible transcript (it's already persisted).
func TestDeletionsSyncedIgnoresOtherChannel(t *testing.T) {
	m := navModel() // openChannelID = c1
	m.posts = []*model.Post{{Id: "p2", ChannelId: "c1", UserId: "u1", CreateAt: 2000, Message: "still here"}}

	out, _ := m.update(deletionsSyncedMsg{
		channelID: "c2",
		deleted:   []*model.Post{{Id: "zz", ChannelId: "c2", DeleteAt: 9}},
	})
	got := out.(Model)
	if got.posts[0].DeleteAt != 0 || got.posts[0].Message != "still here" {
		t.Errorf("a different channel's sweep mutated the open transcript: %+v", got.posts[0])
	}
}

// TestTombstoneBlocksThreadOpenAndReply: pressing open-thread/reply-in-thread on
// a tombstone is refused (it keeps Id/RootId, so without the guard it would open
// a thread on — or reply into — a removed message). Mirrors the edit/react/
// history guards.
func TestTombstoneBlocksThreadOpenAndReply(t *testing.T) {
	for _, k := range []string{"enter" /* OpenThread */, "r" /* ReplyInThread */} {
		m := navModel() // openChannelID = c1, focusMessages
		m.posts = []*model.Post{{Id: "p1", ChannelId: "c1", UserId: "u1", CreateAt: 1000, DeleteAt: 1500}}
		m.postIdx = 0

		out, _ := m.handleMessagesKey(keyStr(k))
		got := out.(Model)
		if got.threadOpen {
			t.Errorf("key %q opened a thread on a tombstone", k)
		}
		if got.status != "message was deleted" {
			t.Errorf("key %q: status = %q, want \"message was deleted\"", k, got.status)
		}
	}
}

// TestTombstoneBlocksCopy: copy-markdown on a tombstone is refused rather than
// silently copying the stripped (empty) body and toasting success.
func TestTombstoneBlocksCopy(t *testing.T) {
	m := navModel()
	m.posts = []*model.Post{{Id: "p1", ChannelId: "c1", UserId: "u1", CreateAt: 1000, DeleteAt: 1500}}
	m.postIdx = 0

	out, cmd := m.handleMessagesKey(keyStr("y")) // CopyMD
	got := out.(Model)
	if got.status != "message was deleted" {
		t.Errorf("CopyMD on a tombstone: status = %q, want \"message was deleted\"", got.status)
	}
	if cmd != nil {
		t.Error("CopyMD on a tombstone should not return a clipboard command")
	}
}

// threadModel builds a navModel with an open thread sidebar (root + one reply)
// ready for renderThread, used by the deletion-sync thread tests.
func threadModel(t *testing.T) Model {
	t.Helper()
	m := navModel() // openChannelID = c1
	m.emojiImg = newEmojiImages("off", false)
	m.userNames["u1"] = "john"
	tv := viewport.New()
	tv.SetWidth(40)
	tv.SetHeight(20)
	m.threadView = tv
	m.threadOpen = true
	m.threadChannelID = "c1"
	m.threadRootID = "root1"
	m.threadPosts = []*model.Post{
		{Id: "root1", ChannelId: "c1", UserId: "u1", CreateAt: 1000, Message: "the root"},
		{Id: "r2", ChannelId: "c1", UserId: "u1", RootId: "root1", CreateAt: 2000, Message: "secret reply"},
	}
	m.posts = []*model.Post{{Id: "root1", ChannelId: "c1", UserId: "u1", CreateAt: 1000, Message: "the root"}}
	return m
}

// TestDeletionsSyncedClosesDeletedRootThread: an offline-sweep deletion of the
// open thread's root tears the sidebar down, matching applyPostDeleted (the live
// WS path) rather than leaving it anchored on a removed root.
func TestDeletionsSyncedClosesDeletedRootThread(t *testing.T) {
	m := threadModel(t)

	out, _ := m.update(deletionsSyncedMsg{
		channelID: "c1",
		deleted:   []*model.Post{{Id: "root1", ChannelId: "c1", DeleteAt: 1500}},
	})
	got := out.(Model)
	if got.threadOpen {
		t.Error("deleting the thread root should have closed the sidebar")
	}
}

// TestDeletionsSyncedFlipsThreadReply: an offline-sweep deletion of a reply
// inside the open thread flips it to a stripped tombstone in place, leaving the
// sidebar open.
func TestDeletionsSyncedFlipsThreadReply(t *testing.T) {
	m := threadModel(t)

	out, _ := m.update(deletionsSyncedMsg{
		channelID: "c1",
		deleted:   []*model.Post{{Id: "r2", ChannelId: "c1", RootId: "root1", DeleteAt: 2500}},
	})
	got := out.(Model)
	if !got.threadOpen {
		t.Fatal("flipping a reply must not close the thread")
	}
	r2 := got.threadPosts[1]
	if r2.Id != "r2" {
		t.Fatalf("thread order changed: posts[1].Id = %q", r2.Id)
	}
	if r2.DeleteAt == 0 {
		t.Error("offline-deleted thread reply not flagged")
	}
	if r2.Message != "" {
		t.Errorf("deleted thread-reply content lingered: %q", r2.Message)
	}
}
