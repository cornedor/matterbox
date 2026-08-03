package ui

import (
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/viewport"
)

// The selection bar ("▎ ", see renderMessages) is the only on-screen answer to
// "which message will this keystroke act on". These tests pin that promise:
// the bar must mark exactly m.posts[m.postIdx] (m.threadPosts[m.threadIdx] in
// the sidebar), only while that pane is the one keys reach, and the rows it
// decorates must resolve back to the same post through m.msgRowStarts — the
// table a click and the scroll anchor read. A bar that survives into a pane
// that no longer has focus, or that sits on a post other than the one an action
// key would hit, is the indicator lying.

// barredPosts returns the post indices whose rendered lines carry the selection
// bar, resolved the way a mouse click resolves a row: visual row →
// msgRowStarts. That deliberately routes the answer through the row table
// instead of the render loop's own bookkeeping, so a skew between the decorated
// lines and the geometry every other consumer reads shows up as a wrong index
// rather than passing silently.
func barredPosts(t *testing.T, m *Model, pane focus) []int {
	t.Helper()
	width, rowStarts := m.msgsView.Width(), m.msgRowStarts
	if pane == focusThread {
		width, rowStarts = m.threadView.Width(), m.threadRowStarts
	}
	lines, lineStarts := m.ensureWrapIndex(pane, width)
	seen := map[int]bool{}
	var out []int
	for i, ln := range lines {
		if !strings.HasPrefix(ansi.Strip(ln), "▎") {
			continue
		}
		idx := postAtVisualRow(rowStarts, lineStarts[i])
		if !seen[idx] {
			seen[idx] = true
			out = append(out, idx)
		}
	}
	sort.Ints(out)
	return out
}

// barredLineCount counts the rendered lines carrying the bar.
func barredLineCount(t *testing.T, m *Model, pane focus) int {
	t.Helper()
	width := m.msgsView.Width()
	if pane == focusThread {
		width = m.threadView.Width()
	}
	lines, _ := m.ensureWrapIndex(pane, width)
	n := 0
	for _, ln := range lines {
		if strings.HasPrefix(ansi.Strip(ln), "▎") {
			n++
		}
	}
	return n
}

// wantBarOn asserts the bar marks exactly post `want` in the pane (and nothing
// else). want < 0 means "no bar anywhere".
func wantBarOn(t *testing.T, m *Model, pane focus, want int, ctx string) {
	t.Helper()
	got := barredPosts(t, m, pane)
	if want < 0 {
		if len(got) != 0 {
			t.Errorf("%s: bar drawn on posts %v, want none", ctx, got)
		}
		return
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("%s: bar on posts %v, want exactly [%d]", ctx, got, want)
	}
}

// TestSelBarMarksSelectedPost: the bar decorates every line of the selected
// post and no line of any other, and each decorated row resolves back through
// msgRowStarts to the selected index.
func TestSelBarMarksSelectedPost(t *testing.T) {
	posts := []*model.Post{
		{Id: "a", CreateAt: 100, UserId: "u", Message: "alpha"},
		{Id: "b", CreateAt: 200, UserId: "u2", Message: "bravo\ncharlie"},
		{Id: "c", CreateAt: 300, UserId: "u", Message: "delta"},
	}
	m := pagingModel(posts, 1)
	m.userNames["u2"] = "other"
	m.renderMessages()

	wantBarOn(t, &m, focusMessages, 1, "postIdx=1")
	// header + two body lines, all decorated.
	if got := barredLineCount(t, &m, focusMessages); got != 3 {
		t.Errorf("decorated lines = %d, want 3 (header + 2 body lines)", got)
	}
}

// TestSelBarFollowsKeyboardNavigation: ↑/↓/Home/End move the bar with the
// selection on the very render the key triggers — no key path may leave the
// bar behind on the previous post.
func TestSelBarFollowsKeyboardNavigation(t *testing.T) {
	m := scrollModel(shortPosts(6), 0)
	m.renderMessages()
	wantBarOn(t, &m, focusMessages, 0, "initial")

	for _, step := range []struct {
		key  tea.KeyPressMsg
		want int
	}{
		{keyPress(tea.KeyDown), 1},
		{keyPress(tea.KeyDown), 2},
		{keyPress(tea.KeyUp), 1},
	} {
		out, _ := m.handleMessagesKey(step.key)
		m = out.(Model)
		wantBarOn(t, &m, focusMessages, step.want, "after nav key")
	}

	out, _ := m.handleMessagesKey(keyPress(tea.KeyHome))
	m = out.(Model)
	wantBarOn(t, &m, focusMessages, 0, "home")

	out, _ = m.handleMessagesKey(keyPress(tea.KeyEnd))
	m = out.(Model)
	wantBarOn(t, &m, focusMessages, len(m.posts)-1, "end")
}

// TestSelBarHiddenWhenPaneUnfocused: the bar claims "keys act here", so it must
// be gone the moment the messages pane isn't the pane keys reach — whichever
// pane took over.
func TestSelBarHiddenWhenPaneUnfocused(t *testing.T) {
	for _, f := range []struct {
		name  string
		focus focus
	}{
		{"input", focusInput},
		{"thread", focusThread},
		{"ref", focusRef},
		{"info", focusInfo},
		{"teams", focusTeams},
		{"channels", focusChannels},
		{"attachments", focusAttachments},
	} {
		m := pagingModel(shortPosts(4), 2)
		m.focus = f.focus
		m.renderMessages()
		wantBarOn(t, &m, focusMessages, -1, "focus="+f.name)
	}
}

// TestSelBarSuppressedByTextSelectionInPane: a mouse text selection in the
// messages pane drops the bar (it would shift the header two cells and skew the
// cell→content mapping the drag reads), but a selection living in the *thread*
// pane must not blank the messages pane's indicator.
func TestSelBarSuppressedByTextSelectionInPane(t *testing.T) {
	m := pagingModel(shortPosts(4), 2)
	m.textSel = textSel{pane: focusMessages, active: true, anchorLine: 1, headLine: 1, headCol: 4}
	m.renderMessages()
	wantBarOn(t, &m, focusMessages, -1, "text selection in messages pane")

	m.textSel = textSel{pane: focusThread, active: true, anchorLine: 1, headLine: 1, headCol: 4}
	m.renderMessages()
	wantBarOn(t, &m, focusMessages, 2, "text selection in the thread pane")

	// Armed but not yet dragged off the anchor (a plain click): still inactive,
	// so the bar stays — the click just moved the selection.
	m.textSel = textSel{pane: focusMessages, dragging: true, anchorLine: 1, headLine: 1}
	m.renderMessages()
	wantBarOn(t, &m, focusMessages, 2, "armed but inactive selection")
}

// TestSelBarClampedWhenPostsShrink: posts deleted out from under the selection
// leave postIdx past the end; the render clamps it and the bar must land on the
// clamped post rather than vanishing.
func TestSelBarClampedWhenPostsShrink(t *testing.T) {
	m := pagingModel(shortPosts(5), 4)
	m.renderMessages()
	wantBarOn(t, &m, focusMessages, 4, "before shrink")

	m.posts = m.posts[:2] // postIdx (4) now out of range
	m.renderMessages()
	if m.postIdx != 1 {
		t.Fatalf("postIdx not clamped: %d, want 1", m.postIdx)
	}
	wantBarOn(t, &m, focusMessages, 1, "after shrink")
}

// TestSelBarSkipsDividers: the unread and date dividers are drawn in the gap
// above their post. They are chrome, not the message — they must not carry the
// bar, and the row table must still point at the post's own first line so the
// bar's rows resolve to the post (not to the one above it).
func TestSelBarSkipsDividers(t *testing.T) {
	const day = 24 * 60 * 60 * 1000
	posts := []*model.Post{
		{Id: "a", CreateAt: 1_000_000, UserId: "u", Message: "yesterday"},
		{Id: "b", CreateAt: 1_000_000 + 2*day, UserId: "u", Message: "today"},
		{Id: "c", CreateAt: 1_000_000 + 2*day + 1000, UserId: "u", Message: "later"},
	}
	m := pagingModel(posts, 1)
	m.showDateSeparators = true
	m.unreadDividerID = "b" // frozen anchor; unreadBoundary 0 keeps resolve a no-op
	m.renderMessages()

	wantBarOn(t, &m, focusMessages, 1, "post under both dividers")

	lines, _ := m.ensureWrapIndex(focusMessages, m.msgsView.Width())
	for i, ln := range lines {
		plain := ansi.Strip(ln)
		if !strings.HasPrefix(plain, "▎") {
			continue
		}
		if strings.Contains(plain, "unread") || strings.Contains(plain, "─") {
			t.Errorf("divider line %d carries the selection bar: %q", i, plain)
		}
	}
}

// TestSelBarGeometryIsSelfConsistent: msgRowStarts must describe the content as
// decorated. The bar widens header lines by two cells, which can push one into
// a second wrapped row; if the row count weren't recounted after decorating,
// every consumer of msgRowStarts (clicks, scroll anchoring, the animation
// viewport check) would be off by that row for as long as the post stayed
// selected.
func TestSelBarGeometryIsSelfConsistent(t *testing.T) {
	m := pagingModel(shortPosts(6), 3)
	// A body line long enough to wrap at the test width, so the check covers
	// wrapped rows and not just the one-line case.
	m.posts[3].Message = strings.Repeat("wrap ", 40)
	m.renderMessages()

	width := m.msgsView.Width()
	content := m.msgsView.GetContent()
	if got, want := m.msgRowStarts[len(m.msgRowStarts)-1], viewportVisualRows(content, width); got != want {
		t.Errorf("msgRowStarts total = %d, content is %d visual rows", got, want)
	}
	wantBarOn(t, &m, focusMessages, 3, "wrapped selected post")

	// Every visual row of the selected post must be barred: the bar is what
	// makes a tall post read as one selected unit.
	visStart, visEnd := m.msgRowStarts[3], m.msgRowStarts[4]
	if got := barredLineCount(t, &m, focusMessages); got == 0 {
		t.Fatal("no barred lines on the selected post")
	}
	lines, lineStarts := m.ensureWrapIndex(focusMessages, width)
	for i, ln := range lines {
		row := lineStarts[i]
		inSel := row >= visStart && row < visEnd
		barred := strings.HasPrefix(ansi.Strip(ln), "▎")
		if inSel != barred {
			t.Errorf("line %d (row %d) barred=%v, in selected span [%d,%d)=%v: %q",
				i, row, barred, visStart, visEnd, inSel, ansi.Strip(ln))
		}
	}
}

// TestSelBarStaysOnPostAcrossNewMessage: a live message arriving from someone
// else while you're reading history must not slide the bar onto a different
// post.
func TestSelBarStaysOnPostAcrossNewMessage(t *testing.T) {
	m := mouseModel(shortPosts(10))
	m.postIdx = 3
	m.renderMessages()
	selID := m.posts[m.postIdx].Id

	ev := postedEvent(&model.Post{Id: "new", ChannelId: "c", CreateAt: 9999, UserId: "u", Message: "hi"})
	out, _ := m.update(wsEventMsg{ev: ev})
	m = out.(Model)
	if len(m.posts) != 11 {
		t.Fatalf("live post not appended: %d posts", len(m.posts))
	}

	got := barredPosts(t, &m, focusMessages)
	if len(got) != 1 {
		t.Fatalf("bar on posts %v, want exactly one", got)
	}
	if id := m.posts[got[0]].Id; id != selID {
		t.Errorf("bar moved to %q after a new message, want %q", id, selID)
	}
	if got[0] != m.postIdx {
		t.Errorf("bar on index %d but postIdx=%d — keys would act elsewhere", got[0], m.postIdx)
	}
}

// TestSelBarStaysOnPostAcrossOlderPage: same promise across a page of older
// history merged in above the selection.
func TestSelBarStaysOnPostAcrossOlderPage(t *testing.T) {
	m := pagingModel([]*model.Post{p("m1", 500), p("m2", 600)}, 0)
	m.renderMessages()

	out, _ := m.update(olderPostsMsg{
		channelID: "c",
		posts:     []*model.Post{p("old1", 100), p("old2", 200), p("mid", 300)},
	})
	m = out.(Model)

	got := barredPosts(t, &m, focusMessages)
	if len(got) != 1 || m.posts[got[0]].Id != "m1" {
		t.Fatalf("bar on %v (%v), want the post that was selected (m1)", got, ids(m.posts))
	}
	if got[0] != m.postIdx {
		t.Errorf("bar on index %d but postIdx=%d", got[0], m.postIdx)
	}
}

// threadBarModel builds a channel with the thread sidebar open, both panes
// rendered, so cross-pane indicator assertions have something to look at.
func threadBarModel(postIdx, threadIdx int) Model {
	m := pagingModel(shortPosts(6), postIdx)
	m.keys = newKeyMap("ctrl")
	tv := viewport.New()
	tv.SoftWrap = true
	tv.SetWidth(30)
	tv.SetHeight(20)
	m.threadView = tv
	m.threadOpen = true
	m.threadRootID = "p0"
	m.threadChannelID = "c"
	m.threadPosts = []*model.Post{
		{Id: "p0", ChannelId: "c", CreateAt: 100, UserId: "u", Message: "root"},
		{Id: "r1", ChannelId: "c", RootId: "p0", CreateAt: 150, UserId: "u", Message: "reply one"},
		{Id: "r2", ChannelId: "c", RootId: "p0", CreateAt: 200, UserId: "u", Message: "reply two"},
	}
	m.threadIdx = threadIdx
	return m
}

// TestSelBarOnlyInFocusedPane: with the thread sidebar open, exactly one of the
// two panes shows an indicator — the one keys reach. Two bars on screen is the
// ambiguity the whole decorate-on-focus rule exists to prevent.
func TestSelBarOnlyInFocusedPane(t *testing.T) {
	m := threadBarModel(2, 1)

	m.focus = focusMessages
	m.renderMessages()
	m.renderThread()
	wantBarOn(t, &m, focusMessages, 2, "messages focused")
	wantBarOn(t, &m, focusThread, -1, "messages focused")

	m.focus = focusThread
	m.renderMessages()
	m.renderThread()
	wantBarOn(t, &m, focusMessages, -1, "thread focused")
	wantBarOn(t, &m, focusThread, 1, "thread focused")

	// Composing a reply: neither pane acts on keys, so neither may claim to.
	m.focus = focusInput
	m.renderMessages()
	m.renderThread()
	wantBarOn(t, &m, focusMessages, -1, "composer focused")
	wantBarOn(t, &m, focusThread, -1, "composer focused")
}

// TestSelBarClearedInOtherPaneOnClick: clicking a message in one pane moves
// focus there — the pane that just lost focus must drop its bar on the same
// render, or the screen shows two selected messages at once.
func TestSelBarClearedInOtherPaneOnClick(t *testing.T) {
	m := threadBarModel(2, 1)
	m.vcache = &viewCache{}
	m.mouseEnabled = true
	m.teams = []*model.Team{{Id: "t1", DisplayName: "T1"}}
	m.channels = map[string][]*model.Channel{"t1": {
		{Id: "c", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "chan"},
	}}
	m.teamIdx = m.firstTeamTabIdx()
	m.focus = focusThread
	m.renderMessages()
	m.renderThread()
	wantBarOn(t, &m, focusThread, 1, "thread focused before the click")

	// Click the first message row in the messages pane (its viewport starts at
	// screen row tabsHeight+1, content at channelsWidth+1). Routed through
	// Update, as a real click is.
	out, _ := m.Update(click(tea.MouseLeft, channelsWidth+3, tabsHeight+1))
	m = out.(Model)
	if m.focus != focusMessages {
		t.Fatalf("click didn't focus the messages pane: focus=%v", m.focus)
	}
	wantBarOn(t, &m, focusMessages, 0, "after clicking a message")
	wantBarOn(t, &m, focusThread, -1, "thread pane after focus left it")
}

// TestSelBarClearedInMessagesOnThreadClick is the mirror: clicking a thread
// reply must clear the messages pane's bar.
func TestSelBarClearedInMessagesOnThreadClick(t *testing.T) {
	m := threadBarModel(2, 0)
	m.vcache = &viewCache{}
	m.mouseEnabled = true
	m.teams = []*model.Team{{Id: "t1", DisplayName: "T1"}}
	m.channels = map[string][]*model.Channel{"t1": {
		{Id: "c", TeamId: "t1", Type: model.ChannelTypeOpen, DisplayName: "chan"},
	}}
	m.teamIdx = m.firstTeamTabIdx()
	m.focus = focusMessages
	m.renderMessages()
	m.renderThread()
	wantBarOn(t, &m, focusMessages, 2, "messages focused before the click")

	x0, top, _, _, _ := m.threadGeom()
	out, _ := m.Update(click(tea.MouseLeft, x0+1, top))
	m = out.(Model)
	if m.focus != focusThread {
		t.Fatalf("click didn't focus the thread pane: focus=%v", m.focus)
	}
	wantBarOn(t, &m, focusThread, 0, "after clicking the thread root")
	wantBarOn(t, &m, focusMessages, -1, "messages pane after focus left it")
}

// TestSelBarMatchesActionTargetAfterWheelScroll: the first keypress after a
// wheel scroll re-anchors the selection so the key acts on something visible
// (syncMsgSelToViewport). It must not re-anchor away from a post that is fully
// on screen — the bar is still drawn on it, and an action key (edit, thread,
// copy, react) would otherwise silently hit a different message.
func TestSelBarMatchesActionTargetAfterWheelScroll(t *testing.T) {
	m := scrollModel(shortPosts(80), 10)
	m.renderMessages()

	// Scroll a few rows: the selected post stays fully on screen.
	m = wheelOnce(m, tea.MouseWheelDown)
	if !m.msgScrollFree {
		t.Fatal("wheel didn't enter free-scroll")
	}
	barred := barredPosts(t, &m, focusMessages)
	if len(barred) != 1 {
		t.Fatalf("bar on %v, want exactly one post", barred)
	}
	off, h := m.msgsView.YOffset(), m.msgsView.Height()
	visStart, visEnd := m.msgRowStarts[barred[0]], m.msgRowStarts[barred[0]+1]
	if visStart < off || visEnd > off+h {
		t.Fatalf("test setup: barred post %d (rows %d-%d) not fully visible in [%d,%d)",
			barred[0], visStart, visEnd, off, off+h)
	}

	// Any key ends free-scroll. The post the user can still see barred is the
	// one the next action key must act on.
	out, _ := m.handleKey(keyPress('y'))
	m = out.(Model)
	if m.postIdx != barred[0] {
		t.Errorf("keypress after a wheel scroll moved the selection off the barred post: postIdx=%d, bar was on %d", m.postIdx, barred[0])
	}
}

// TestSelBarReanchorsWhenScrolledOffScreen: the other half of the rule — once
// the selected post is scrolled out of view there is no indicator on screen to
// contradict, so the keypress re-anchors to the top visible post (which is what
// keeps the view from jumping back).
func TestSelBarReanchorsWhenScrolledOffScreen(t *testing.T) {
	m := scrollModel(shortPosts(80), 0)
	m.renderMessages()
	for i := 0; i < 8; i++ {
		m = wheelOnce(m, tea.MouseWheelDown)
	}
	off := m.msgsView.YOffset()
	if off <= m.msgRowStarts[1] {
		t.Fatalf("test setup: post 0 still visible at offset %d", off)
	}
	want := postAtVisualRow(m.msgRowStarts, off)

	out, _ := m.handleKey(keyPress('y'))
	m = out.(Model)
	if m.postIdx != want {
		t.Errorf("postIdx=%d after keypress, want %d (top visible post)", m.postIdx, want)
	}
	m.renderMessages()
	wantBarOn(t, &m, focusMessages, want, "after re-anchor")
}

// TestThreadSelBarFollowsThreadIdx: the sidebar's bar tracks threadIdx the same
// way, including across the thread's own row table.
func TestThreadSelBarFollowsThreadIdx(t *testing.T) {
	m := threadBarModel(0, 0)
	m.focus = focusThread
	m.renderThread()
	wantBarOn(t, &m, focusThread, 0, "thread root selected")

	out, _ := m.handleThreadKey(keyPress(tea.KeyDown))
	m = out.(Model)
	wantBarOn(t, &m, focusThread, 1, "after ↓ in the thread")

	out, _ = m.handleThreadKey(keyPress(tea.KeyUp))
	m = out.(Model)
	wantBarOn(t, &m, focusThread, 0, "after ↑ in the thread")

	width := m.threadView.Width()
	if got, want := m.threadRowStarts[len(m.threadRowStarts)-1], viewportVisualRows(m.threadView.GetContent(), width); got != want {
		t.Errorf("threadRowStarts total = %d, content is %d visual rows", got, want)
	}
}

// inertMsg is a message no handler acts on, for exercising the per-event
// invariants in Update's wrapper on their own.
type inertMsg struct{}

// TestSelBarSyncedOnEveryEvent: syncSelBarFocus is the backstop for any
// focus-changing path that forgets to repaint a transcript pane — the same
// guarantee syncComposerFocus gives the composer cursor. A focus move with no
// render at all must still leave exactly one pane marking a selection.
func TestSelBarSyncedOnEveryEvent(t *testing.T) {
	m := threadBarModel(2, 1)
	m.focus = focusMessages
	m.renderMessages()
	m.renderThread()
	wantBarOn(t, &m, focusMessages, 2, "messages focused")

	// A path that moves focus and renders nothing.
	m.focus = focusThread
	out, _ := m.Update(inertMsg{})
	m = out.(Model)
	wantBarOn(t, &m, focusMessages, -1, "messages pane after an unrendered focus move")
	wantBarOn(t, &m, focusThread, 1, "thread pane after an unrendered focus move")

	m.focus = focusInput
	out, _ = m.Update(inertMsg{})
	m = out.(Model)
	wantBarOn(t, &m, focusMessages, -1, "composer focused")
	wantBarOn(t, &m, focusThread, -1, "composer focused")
}

// TestSelBarSyncCostsNothingWhenSettled: the backstop must not turn every event
// into a re-render of both transcripts — View() runs on each keystroke as it
// is. A settled model re-renders neither pane (the content version is the
// render counter).
func TestSelBarSyncCostsNothingWhenSettled(t *testing.T) {
	for _, f := range []focus{focusMessages, focusThread, focusInput} {
		m := threadBarModel(2, 1)
		m.focus = f
		m.renderMessages()
		m.renderThread()
		msgsVer, threadVer := m.msgsContentVer, m.threadContentVer

		out, _ := m.Update(inertMsg{})
		m = out.(Model)
		if m.msgsContentVer != msgsVer {
			t.Errorf("focus %v: messages pane re-rendered on a settled event", f)
		}
		if m.threadContentVer != threadVer {
			t.Errorf("focus %v: thread pane re-rendered on a settled event", f)
		}
	}
}

// TestSelBarAbsentWithNoPosts: an empty channel has nothing to select, so the
// placeholder must not carry a bar.
func TestSelBarAbsentWithNoPosts(t *testing.T) {
	m := pagingModel(nil, 0)
	m.renderMessages()
	wantBarOn(t, &m, focusMessages, -1, "empty channel")
	if m.msgRowStarts != nil {
		t.Errorf("msgRowStarts = %v, want nil for an empty channel", m.msgRowStarts)
	}
}

// TestSelBarOnTombstone: a deleted post keeps its place in the transcript and
// stays selectable, so the bar must still mark it — the actions that refuse to
// run on a tombstone say so in the status line.
func TestSelBarOnTombstone(t *testing.T) {
	posts := []*model.Post{
		{Id: "a", CreateAt: 100, UserId: "u", Message: "alpha"},
		{Id: "b", CreateAt: 200, UserId: "u", Message: "", DeleteAt: 250},
		{Id: "c", CreateAt: 300, UserId: "u", Message: "charlie"},
	}
	m := pagingModel(posts, 1)
	m.renderMessages()
	wantBarOn(t, &m, focusMessages, 1, "tombstone selected")
}
