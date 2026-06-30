package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Every channel-open kicks off an async reconcile: the cached page is painted
// synchronously (pinned to the bottom by openChannelLoadCmd), then a server
// fetch comes back as a postsGapFilledMsg (warm cache) or postsLoadedMsg (cold
// cache) and re-renders. These tests assert that the reconcile keeps the pane
// pinned to the bottom — and therefore that ↓ on the freshly-opened channel
// drops into the composer rather than scrolling around inside the newest post.
//
// The reported symptom: switch to a channel, see the newest message selected,
// press ↓ expecting the input — but the view has jumped up to a message higher
// in the pane and ↓ no longer reaches the composer. It shows up when the newest
// message is taller than the pane: the WS new-post handler re-pins to the bottom
// via anchorMsgSelBottom, but the reconcile handlers did not, so renderMessages'
// default "keep selection visible" branch anchored that tall newest post to its
// *top*.

// switchToC3 builds a warm model (c1 + c3 cached), jumps to team t2 / channel c3
// with alt+2, and returns the post-switch model. After the switch the pane is at
// the bottom on the newest c3 post — the starting point the reconcile mutates.
func switchToC3(t *testing.T, c3 []*model.Post) Model {
	t.Helper()
	m := teamScrollModel(t, map[string][]*model.Post{
		"c1": {tallMsgPost("c1a", 100, 60), msgPost("c1b", 200, "latest in c1")},
		"c3": c3,
	})
	m.msgsView.GotoBottom()
	out, _ := m.handleKey(altKey('2'))
	got := out.(Model)
	if got.openChannelID != "c3" {
		t.Fatalf("alt+2 opened %q, want c3", got.openChannelID)
	}
	assertAtBottom(t, got, "precondition: synchronous open of c3")
	return got
}

// TestReconcileTallNewestStaysAtBottom: the warm-open reconcile (fetchRecent ->
// postsGapFilledMsg) must leave the pane pinned to the bottom when the newest
// message is taller than the pane. Expected to FAIL — the reconcile re-anchors
// the tall newest post to its top.
func TestReconcileTallNewestStaysAtBottom(t *testing.T) {
	c3 := []*model.Post{
		msgPost("c3a", 100, "older"),
		msgPost("c3b", 200, "older2"),
		tallMsgPost("c3big", 300, 60), // newest message, taller than the 40-row pane
	}
	m := switchToC3(t, c3)

	// fetchRecent returns the same page (no new posts) — a pure reconcile.
	out, _ := m.update(postsGapFilledMsg{channelID: "c3", posts: c3})
	assertAtBottom(t, out.(Model), "after warm-open reconcile (tall newest)")
}

// TestDownAfterReconcileTallNewestFocusesComposer: with the newest message at the
// bottom, ↓ should drop into the composer. After the reconcile knocks the view
// off the bottom, ↓ instead scrolls inside the tall post and never reaches the
// input. Expected to FAIL.
func TestDownAfterReconcileTallNewestFocusesComposer(t *testing.T) {
	c3 := []*model.Post{
		msgPost("c3a", 100, "older"),
		msgPost("c3b", 200, "older2"),
		tallMsgPost("c3big", 300, 60),
	}
	m := switchToC3(t, c3)

	out, _ := m.update(postsGapFilledMsg{channelID: "c3", posts: c3})
	got := out.(Model)

	out2, _ := got.handleKey(keyPress(tea.KeyDown))
	got2 := out2.(Model)
	if got2.focus != focusInput {
		t.Errorf("↓ on the newest message did not focus the composer: focus=%v, want focusInput "+
			"(postIdx=%d, YOffset=%d) — the reconcile left the view off the bottom",
			got2.focus, got2.postIdx, got2.msgsView.YOffset())
	}
}

// TestReconcileShortNewestStaysAtBottom: the same flow with a short newest
// message is the working control — reconcile keeps the bottom and ↓ focuses the
// composer. Expected to PASS.
func TestReconcileShortNewestStaysAtBottom(t *testing.T) {
	c3 := []*model.Post{
		tallMsgPost("c3a", 100, 60),
		msgPost("c3b", 200, "older"),
		msgPost("c3c", 300, "newest in c3"),
	}
	m := switchToC3(t, c3)

	out, _ := m.update(postsGapFilledMsg{channelID: "c3", posts: c3})
	got := out.(Model)
	assertAtBottom(t, got, "after warm-open reconcile (short newest)")

	out2, _ := got.handleKey(keyPress(tea.KeyDown))
	if g := out2.(Model); g.focus != focusInput {
		t.Errorf("↓ on the short newest message did not focus the composer: focus=%v, want focusInput", g.focus)
	}
}

// TestColdOpenTallNewestStaysAtBottom: the cold-cache path (no cached posts ->
// fetchPosts -> postsLoadedMsg) must also open the tall newest message pinned to
// the bottom. Expected to FAIL — postsLoadedMsg renders without the bottom
// anchor.
func TestColdOpenTallNewestStaysAtBottom(t *testing.T) {
	// c3 is NOT seeded in the store, so the switch takes the no-cache path and
	// leaves posts empty until the server fetch resolves.
	m := teamScrollModel(t, map[string][]*model.Post{
		"c1": {tallMsgPost("c1a", 100, 60), msgPost("c1b", 200, "latest in c1")},
	})
	m.msgsView.GotoBottom()

	out, _ := m.handleKey(altKey('2'))
	got := out.(Model)
	if got.openChannelID != "c3" {
		t.Fatalf("alt+2 opened %q, want c3", got.openChannelID)
	}
	if len(got.posts) != 0 {
		t.Fatalf("precondition: cold open should leave posts empty, got %d", len(got.posts))
	}

	// The server's first page lands with a tall newest message.
	c3 := []*model.Post{
		msgPost("c3a", 100, "older"),
		tallMsgPost("c3big", 300, 60),
	}
	out2, _ := got.update(postsLoadedMsg{channelID: "c3", posts: c3})
	assertAtBottom(t, out2.(Model), "after cold-open server load (tall newest)")
}
