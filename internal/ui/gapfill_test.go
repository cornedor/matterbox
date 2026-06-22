package ui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	"github.com/mattermost/mattermost/server/public/model"
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

// TestReconcileDeletesAbsentPost is the offline-delete case: a post sits in
// the cache, then is deleted on the server while matterbox is closed. The
// authoritative recent page omits it, so the reconcile must drop it and
// report its Id for soft-deletion. Posts the page still carries stay put.
func TestReconcileDeletesAbsentPost(t *testing.T) {
	loaded := []*model.Post{p("a", 100), p("gone", 200), p("c", 300)}
	page := []*model.Post{p("a", 100), p("c", 300)} // "gone" deleted server-side

	kept, deleted := reconcileDeletedPosts(loaded, page)
	if order := ids(kept); !eq(order, []string{"a", "c"}) {
		t.Fatalf("absent post not dropped: got %v", order)
	}
	if !eq(deleted, []string{"gone"}) {
		t.Fatalf("wrong deleted set: got %v, want [gone]", deleted)
	}
}

// TestReconcileSparesOlderThanFloor guards the strict-floor bound: a cached
// post older than the page's oldest entry is simply out of the window the
// page is authoritative for (it could have been paged out), so its absence
// must NOT be read as a deletion.
func TestReconcileSparesOlderThanFloor(t *testing.T) {
	loaded := []*model.Post{p("ancient", 50), p("a", 100), p("b", 200)}
	page := []*model.Post{p("a", 100), p("b", 200)} // floor = 100; ancient < floor

	kept, deleted := reconcileDeletedPosts(loaded, page)
	if order := ids(kept); !eq(order, []string{"ancient", "a", "b"}) {
		t.Fatalf("post older than floor wrongly dropped: got %v", order)
	}
	if len(deleted) != 0 {
		t.Fatalf("nothing should be deleted, got %v", deleted)
	}
}

// TestReconcileSparesFloorTie: a post at exactly the floor create_at is left
// alone, because a tie there can be explained by pagination rather than a
// delete (the floor comparison is strict).
func TestReconcileSparesFloorTie(t *testing.T) {
	loaded := []*model.Post{p("a", 100), p("tie", 100), p("b", 200)}
	page := []*model.Post{p("a", 100), p("b", 200)} // floor = 100; "tie" also at 100

	_, deleted := reconcileDeletedPosts(loaded, page)
	if len(deleted) != 0 {
		t.Fatalf("floor-tie post wrongly deleted: got %v", deleted)
	}
}

// TestReconcileSkipsStubsAndAlreadyDeleted: an optimistic own-send (empty
// Id) and an already-soft-deleted post must never be reconciled away.
func TestReconcileSkipsStubsAndAlreadyDeleted(t *testing.T) {
	stub := &model.Post{Id: "", CreateAt: 0, Message: "sending…"}
	dead := &model.Post{Id: "dead", CreateAt: 250, DeleteAt: 240}
	loaded := []*model.Post{p("a", 100), dead, p("b", 300), stub}
	page := []*model.Post{p("a", 100), p("b", 300)}

	kept, deleted := reconcileDeletedPosts(loaded, page)
	if len(deleted) != 0 {
		t.Fatalf("stub/already-deleted reconciled: got %v", deleted)
	}
	if order := ids(kept); !eq(order, []string{"a", "dead", "b", ""}) {
		t.Fatalf("slice disturbed: got %v", order)
	}
}

// TestReconcileSparesNewerThanCeil guards the ceiling: a message that arrived
// live over WebSocket after the server snapshot but before the merge is newer
// than the page and absent from it — yet it must NOT be mistaken for a
// deletion (the bug a floor-only check would have).
func TestReconcileSparesNewerThanCeil(t *testing.T) {
	loaded := []*model.Post{p("a", 100), p("b", 200), p("live", 300)}
	page := []*model.Post{p("a", 100), p("b", 200)} // ceil = 200; "live" arrived after

	kept, deleted := reconcileDeletedPosts(loaded, page)
	if order := ids(kept); !eq(order, []string{"a", "b", "live"}) {
		t.Fatalf("live arrival wrongly dropped: got %v", order)
	}
	if len(deleted) != 0 {
		t.Fatalf("live arrival reconciled as delete: %v", deleted)
	}
}

// TestReconcileInteriorWindow is the older-history shape: a page bounded on
// both sides (PostsBefore/PostsAfter). A post deleted strictly inside the
// window is reconciled; posts at/above the page's newest (the anchor we
// paged from) are spared.
func TestReconcileInteriorWindow(t *testing.T) {
	loaded := []*model.Post{p("o1", 100), p("goneMid", 150), p("o2", 200), p("anchor", 300)}
	page := []*model.Post{p("o1", 100), p("o2", 200)} // interior window (100, 200)

	kept, deleted := reconcileDeletedPosts(loaded, page)
	if order := ids(kept); !eq(order, []string{"o1", "o2", "anchor"}) {
		t.Fatalf("interior delete not reconciled / anchor not spared: got %v", order)
	}
	if !eq(deleted, []string{"goneMid"}) {
		t.Fatalf("wrong deleted set: got %v, want [goneMid]", deleted)
	}
}

// TestOlderPostsHandlerReconcilesDelete drives the scroll-up handler: paging
// older history surfaces that a post in the fetched range was deleted while
// offline, and it must vanish from the view while the rest merges normally.
func TestOlderPostsHandlerReconcilesDelete(t *testing.T) {
	// Loaded window holds a since-deleted middle post; selection on the tail.
	m := pagingModel([]*model.Post{p("o1", 100), p("goneMid", 150), p("o2", 200)}, 2)
	msg := olderPostsMsg{
		channelID: "c",
		posts:     []*model.Post{p("o1", 100), p("o2", 200)}, // server omits goneMid
	}
	out, _ := m.update(msg)
	got := out.(Model)

	if order := ids(got.posts); !eq(order, []string{"o1", "o2"}) {
		t.Fatalf("offline-deleted post not reconciled on scroll-up: got %v", order)
	}
}

// TestReconcileEmptyPageNoOp: an empty authoritative page (transient/empty
// response) must leave the loaded slice untouched — never nuke the cache.
func TestReconcileEmptyPageNoOp(t *testing.T) {
	loaded := []*model.Post{p("a", 100), p("b", 200)}
	kept, deleted := reconcileDeletedPosts(loaded, nil)
	if order := ids(kept); !eq(order, []string{"a", "b"}) {
		t.Fatalf("empty page mutated slice: got %v", order)
	}
	if len(deleted) != 0 {
		t.Fatalf("empty page reported deletions: %v", deleted)
	}
}

// TestGapFillReconcilesDeleteOnlyWhenAuthoritative drives the whole
// postsGapFilledMsg handler twice over the same state. A forward fill
// (reconcileDeletes=false) that omits a post must NOT delete it; the
// authoritative recent window (reconcileDeletes=true) must.
func TestGapFillReconcilesDeleteOnlyWhenAuthoritative(t *testing.T) {
	mk := func() Model {
		m := pagingModel([]*model.Post{p("a", 100), p("gone", 200), p("c", 300)}, 2)
		m.markReadDelay = time.Second // keep scheduleMarkViewed off the client path
		return m
	}

	// Forward fill: absence proves nothing, "gone" stays.
	fwd, _ := mk().update(postsGapFilledMsg{
		channelID: "c",
		posts:     []*model.Post{p("a", 100), p("c", 300)},
	})
	if order := ids(fwd.(Model).posts); !eq(order, []string{"a", "gone", "c"}) {
		t.Fatalf("forward fill dropped a post it shouldn't: got %v", order)
	}

	// Authoritative window: "gone" was deleted offline and must disappear.
	auth, _ := mk().update(postsGapFilledMsg{
		channelID:        "c",
		posts:            []*model.Post{p("a", 100), p("c", 300)},
		reconcileDeletes: true,
	})
	if order := ids(auth.(Model).posts); !eq(order, []string{"a", "c"}) {
		t.Fatalf("offline-deleted post not reconciled away: got %v", order)
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
