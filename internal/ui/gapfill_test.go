package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
)

// p builds a post with an Id and create_at (unix-ms) for ordering assertions.
func p(id string, createAt int64) *model.Post {
	return &model.Post{Id: id, CreateAt: createAt}
}

func ids(posts []*model.Post) []string {
	out := make([]string, len(posts))
	for i, pp := range posts {
		out[i] = pp.Id
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestMergeFillsInteriorGap is the reported bug in miniature: the cache holds
// an island of old posts and an island of new ones (caught live over
// WebSocket) with a hole between them. The server's recent page carries the
// missing middle posts, and the merge must slot them into place rather than
// (as a plain append did) drop them because they're older than the newest
// loaded post.
func TestMergeFillsInteriorGap(t *testing.T) {
	cached := []*model.Post{
		p("old1", 100),
		p("old2", 200),
		// gap: 300, 400 missing
		p("new1", 500), // arrived live; newest cached
	}
	serverRecent := []*model.Post{
		p("old2", 200),
		p("mid1", 300), // the "yesterday" message that was invisible
		p("mid2", 400),
		p("new1", 500),
	}

	got := ids(mergePostsByTime(cached, serverRecent))
	want := []string{"old1", "old2", "mid1", "mid2", "new1"}
	if !eq(got, want) {
		t.Fatalf("interior gap not filled in order:\n got %v\nwant %v", got, want)
	}
}

// TestMergeDedupsIncomingWins verifies an Id present in both slices appears
// once, taking the incoming (fresher server) copy.
func TestMergeDedupsIncomingWins(t *testing.T) {
	stale := &model.Post{Id: "x", CreateAt: 100, Message: "old text"}
	fresh := &model.Post{Id: "x", CreateAt: 100, Message: "edited text"}

	merged := mergePostsByTime([]*model.Post{stale}, []*model.Post{fresh})
	if len(merged) != 1 {
		t.Fatalf("len = %d; want 1 (deduped)", len(merged))
	}
	if merged[0].Message != "edited text" {
		t.Errorf("incoming copy did not win: got %q", merged[0].Message)
	}
}

// TestMergeKeepsOptimisticStubLast ensures an unconfirmed own-send (empty Id)
// stays at the newest end after the merge — it can't be matched by Id and
// sorts after every real post.
func TestMergeKeepsOptimisticStubLast(t *testing.T) {
	cached := []*model.Post{
		p("a", 100),
		{Id: "", CreateAt: 0, Message: "sending…"}, // stub, zero create_at
	}
	incoming := []*model.Post{p("b", 200)}

	merged := mergePostsByTime(cached, incoming)
	want := []string{"a", "b", ""}
	if got := ids(merged); !eq(got, want) {
		t.Fatalf("stub not kept last: got %v; want %v", got, want)
	}
	if merged[len(merged)-1].Message != "sending…" {
		t.Errorf("stub content lost")
	}
}

// TestMergeForwardAppendStillWorks confirms the common case (incoming is
// strictly newer than everything loaded — the old forward-fill path) still
// produces a plain appended, ordered slice.
func TestMergeForwardAppendStillWorks(t *testing.T) {
	cached := []*model.Post{p("a", 100), p("b", 200)}
	incoming := []*model.Post{p("c", 300), p("d", 400)}

	got := ids(mergePostsByTime(cached, incoming))
	want := []string{"a", "b", "c", "d"}
	if !eq(got, want) {
		t.Fatalf("forward append broke: got %v; want %v", got, want)
	}
}

// TestMergeEmptyIncoming is the "cache already current" case: nothing to
// reconcile leaves the loaded slice unchanged in order.
func TestMergeEmptyIncoming(t *testing.T) {
	cached := []*model.Post{p("a", 100), p("b", 200)}
	got := ids(mergePostsByTime(cached, nil))
	want := []string{"a", "b"}
	if !eq(got, want) {
		t.Fatalf("empty incoming changed slice: got %v; want %v", got, want)
	}
}

// pagingModel builds a minimal but renderable Model for driving the
// scroll-paging message handlers (olderPostsMsg / newerPostsMsg).
func pagingModel(posts []*model.Post, postIdx int) Model {
	vp := viewport.New()
	vp.SoftWrap = true
	vp.SetWidth(80)
	vp.SetHeight(40)
	names := map[string]string{}
	for _, pp := range posts {
		pp.ChannelId = "c"
		names[pp.UserId] = "u"
	}
	return Model{
		posts:         posts,
		postIdx:       postIdx,
		openChannelID: "c",
		userNames:     names,
		focus:         focusMessages,
		width:         100,
		height:        44,
		msgsView:      vp,
	}
}

// TestOlderPostsHandlerFillsHole drives the scroll-up handler: the user is
// at the top of a window holding only the "morning" island; the server's
// older page carries the previously-missing middle post, which must slot in
// while the selection stays put.
func TestOlderPostsHandlerFillsHole(t *testing.T) {
	m := pagingModel([]*model.Post{p("m1", 500), p("m2", 600)}, 0) // top = m1
	msg := olderPostsMsg{
		channelID: "c",
		posts:     []*model.Post{p("old1", 100), p("old2", 200), p("mid", 300)},
	}
	out, _ := m.update(msg)
	got := out.(Model)

	if order := ids(got.posts); !eq(order, []string{"old1", "old2", "mid", "m1", "m2"}) {
		t.Fatalf("older page not merged in order: got %v", order)
	}
	if got.posts[got.postIdx].Id != "m1" {
		t.Errorf("selection not preserved: on %q, want m1", got.posts[got.postIdx].Id)
	}
}

// TestOlderPostsHandlerBeginningOfChannel: an empty page with atChannelStart
// reports the genuine start without disturbing the loaded slice.
func TestOlderPostsHandlerBeginningOfChannel(t *testing.T) {
	m := pagingModel([]*model.Post{p("a", 100), p("b", 200)}, 0)
	out, _ := m.update(olderPostsMsg{channelID: "c", atChannelStart: true})
	got := out.(Model)

	if order := ids(got.posts); !eq(order, []string{"a", "b"}) {
		t.Fatalf("slice changed on empty page: got %v", order)
	}
	if got.status != "beginning of channel" {
		t.Errorf("status = %q; want \"beginning of channel\"", got.status)
	}
}

// TestNewerPostsHandlerFillsForwardHole drives the scroll-down handler: the
// user is at the bottom of an older island; the server's newer page carries
// the gap toward the live tail.
func TestNewerPostsHandlerFillsForwardHole(t *testing.T) {
	m := pagingModel([]*model.Post{p("old1", 100), p("old2", 200)}, 1) // bottom = old2
	msg := newerPostsMsg{
		channelID: "c",
		posts:     []*model.Post{p("mid", 300), p("new1", 400)},
	}
	out, _ := m.update(msg)
	got := out.(Model)

	if order := ids(got.posts); !eq(order, []string{"old1", "old2", "mid", "new1"}) {
		t.Fatalf("newer page not merged in order: got %v", order)
	}
	if got.posts[got.postIdx].Id != "old2" {
		t.Errorf("selection not preserved: on %q, want old2", got.posts[got.postIdx].Id)
	}
}

// TestOlderPostsHandlerIgnoresOtherChannel: a page for a channel the user
// already navigated away from must not mutate the visible slice.
func TestOlderPostsHandlerIgnoresOtherChannel(t *testing.T) {
	m := pagingModel([]*model.Post{p("a", 100)}, 0)
	out, _ := m.update(olderPostsMsg{channelID: "other", posts: []*model.Post{p("z", 50)}})
	got := out.(Model)

	if order := ids(got.posts); !eq(order, []string{"a"}) {
		t.Fatalf("stale-channel page mutated the view: got %v", order)
	}
}
