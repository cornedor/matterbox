package ui

import (
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/replyto"
	"matterbox/internal/viewport"
)

// nestThreadModel is a four-message thread with the composer sitting in it:
//
//	root      alice: what should we cache?
//	 r1       bram:  the rendered lines
//	 r2       sanne: or the markdown
//	  r3      alice: → r1, "that one"
func nestThreadModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	m.width, m.height = 120, 30
	m.groupWindow = 2 * time.Minute
	m.emojiImg = newEmojiImages("off", false)
	m.me = &model.User{Id: "u1", Username: "alice"}
	m.userNames["u1"] = "alice"
	m.userNames["u2"] = "bram"
	m.userNames["u3"] = "sanne"
	tv := viewport.New()
	tv.SetWidth(48)
	tv.SetHeight(20)
	m.threadView = tv
	m.keys = newKeyMap("ctrl")
	m.teams = []*model.Team{{Id: "t1", Name: "eng", DisplayName: "Engineering"}}
	m.channels = map[string][]*model.Channel{"t1": {{Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen}}}
	m.teamIdx = m.firstTeamTabIdx() // a channel tab: the only place with a composer
	m.openChannelID = "c1"
	m.threadOpen = true
	m.threadChannelID = "c1"
	m.threadRootID = "root1"
	m.threadPosts = nestSortThread([]*model.Post{
		{Id: "root1", ChannelId: "c1", UserId: "u1", CreateAt: 1000, Message: "what should we cache?"},
		{Id: "r1", ChannelId: "c1", UserId: "u2", RootId: "root1", CreateAt: 2000, Message: "the rendered lines"},
		{Id: "r2", ChannelId: "c1", UserId: "u3", RootId: "root1", CreateAt: 3000, Message: "or the markdown"},
		{Id: "r3", ChannelId: "c1", UserId: "u1", RootId: "root1", CreateAt: 4000,
			Message: replyto.Attach("that one", "r1")},
	})
	return m
}

// retree re-applies the pane ordering after a test rewrites a post's payload,
// standing in for the load path that normally does it (see orderedThread).
func retree(m *Model) { m.threadPosts = nestSortThread(m.threadPosts) }

// threadRows is the thread pane as plain indented text, one entry per row, for
// asserting on structure rather than on styling.
func threadRows(m *Model) []string {
	m.renderThread()
	var rows []string
	for _, l := range strings.Split(ansi.Strip(m.threadView.View()), "\n") {
		if strings.TrimSpace(l) != "" {
			rows = append(rows, strings.TrimRight(l, " "))
		}
	}
	return rows
}

// nestPost is the fixture's post with this id. Tree order means index 3 is no
// longer "the nested one".
func nestPost(t *testing.T, m *Model, id string) *model.Post {
	t.Helper()
	if p := m.threadPostByID(id); p != nil {
		return p
	}
	t.Fatalf("no post %q in the fixture thread", id)
	return nil
}

// indentOf is a row's leading-space count, which is what the tree is drawn in.
func indentOf(row string) int { return len(row) - len(strings.TrimLeft(row, " ")) }

// rowWith returns the row holding text, and its index, or ("", -1).
func rowWith(rows []string, text string) (string, int) {
	for i, r := range rows {
		if strings.Contains(r, text) {
			return r, i
		}
	}
	return "", -1
}

// The point of the whole feature: a reply lands directly beneath the message it
// answers, indented under it — ahead of the siblings that were sent later.
func TestNestedReplyIsLiftedUnderItsParent(t *testing.T) {
	m := nestThreadModel(t)
	rows := threadRows(&m)

	_, parent := rowWith(rows, "the rendered lines")
	_, reply := rowWith(rows, "that one")
	_, sibling := rowWith(rows, "or the markdown")
	if parent < 0 || reply < 0 || sibling < 0 {
		t.Fatalf("missing rows:\n%s", strings.Join(rows, "\n"))
	}
	if !(parent < reply && reply < sibling) {
		t.Fatalf("reply not lifted under its parent (parent=%d reply=%d later sibling=%d):\n%s",
			parent, reply, sibling, strings.Join(rows, "\n"))
	}
	if got, want := indentOf(rows[reply]), indentOf(rows[parent])+nestIndentStep; got != want {
		t.Fatalf("reply indent = %d, want %d (one step under its parent):\n%s",
			got, want, strings.Join(rows, "\n"))
	}
}

// In tree order the indent names the parent, so quoting it as well would be the
// same fact stated twice.
func TestTreeOrderDoesNotQuoteWhatTheIndentAlreadySays(t *testing.T) {
	m := nestThreadModel(t)
	for _, r := range threadRows(&m) {
		if strings.Contains(r, "↪") {
			t.Fatalf("quoted a parent the indent already points at: %q", r)
		}
	}
}

// The case that broke the first design: two sub-conversations answered in turn.
// Chronologically they interleave, and every reply's indent then measures against
// whichever post happens to precede it. Each sub-conversation must come out
// contiguous, with each reply one step under the message it answers.
func TestInterleavedSubConversationsComeOutContiguous(t *testing.T) {
	m := nestThreadModel(t)
	// root ─ a ─ b, then answers alternating between the two branches.
	m.threadPosts = nestSortThread([]*model.Post{
		{Id: "root1", ChannelId: "c1", UserId: "u1", CreateAt: 1000, Message: "boem goes the dynamite"},
		{Id: "a", ChannelId: "c1", UserId: "u1", RootId: "root1", CreateAt: 2000, Message: "boombastic!"},
		{Id: "b", ChannelId: "c1", UserId: "u2", RootId: "root1", CreateAt: 3000, Message: "mister boombastic!"},
		{Id: "b1", ChannelId: "c1", UserId: "u3", RootId: "root1", CreateAt: 4000,
			Message: replyto.Attach("whats happening here", "b")},
		{Id: "b2", ChannelId: "c1", UserId: "u1", RootId: "root1", CreateAt: 5000,
			Message: replyto.Attach("waaa", "b1")},
		{Id: "a1", ChannelId: "c1", UserId: "u2", RootId: "root1", CreateAt: 6000,
			Message: replyto.Attach("BOOMBASTIC", "a")},
		{Id: "b3", ChannelId: "c1", UserId: "u3", RootId: "root1", CreateAt: 7000,
			Message: replyto.Attach("AmAzInG", "b2")},
	})
	rows := threadRows(&m)

	// Each reply sits one step under the message it answers, and immediately
	// after that message's own subtree — never under an unrelated branch.
	for _, tc := range []struct{ child, parent string }{
		{"whats happening here", "mister boombastic!"},
		{"waaa", "whats happening here"},
		{"AmAzInG", "waaa"},
		{"BOOMBASTIC", "boombastic!"},
	} {
		crow, ci := rowWith(rows, tc.child)
		prow, pi := rowWith(rows, tc.parent)
		if ci < 0 || pi < 0 {
			t.Fatalf("missing %q or %q:\n%s", tc.child, tc.parent, strings.Join(rows, "\n"))
		}
		if ci <= pi {
			t.Errorf("%q is drawn above the %q it answers:\n%s", tc.child, tc.parent, strings.Join(rows, "\n"))
		}
		if got, want := indentOf(crow), indentOf(prow)+nestIndentStep; got != want {
			t.Errorf("%q indent = %d, want %d (one step under %q):\n%s",
				tc.child, got, want, tc.parent, strings.Join(rows, "\n"))
		}
	}
	// And the branch that was answered last is not spliced into the other one:
	// everything under "boombastic!" is contiguous.
	_, ai := rowWith(rows, "boombastic!")
	_, a1i := rowWith(rows, "BOOMBASTIC")
	_, bi := rowWith(rows, "mister boombastic!")
	if !(ai < a1i && a1i < bi) {
		t.Errorf("the two sub-conversations are still interleaved:\n%s", strings.Join(rows, "\n"))
	}
}

// A nest deeper than the pane can indent *does* get a quote line: past the cap a
// child and its parent draw in the same column, so the indent stops answering.
func TestQuoteReturnsWhenTheIndentRunsOut(t *testing.T) {
	m := nestThreadModel(t)
	posts := m.threadPosts[:2] // root + one reply
	prev := "r1"
	for i := range nestMaxDepth + 2 {
		id := "d" + strconv.Itoa(i)
		posts = append(posts, &model.Post{
			Id: id, ChannelId: "c1", UserId: "u2", RootId: "root1",
			CreateAt: int64(5000 + i*1000), Message: replyto.Attach("step "+id, prev),
		})
		prev = id
	}
	m.threadPosts = nestSortThread(posts)
	rows := threadRows(&m)

	// The deepest reply and its parent draw in the same column once the cap bites,
	// so that reply must name its parent in a quote line above its header.
	child := "step d" + strconv.Itoa(nestMaxDepth+1)
	parent := "step d" + strconv.Itoa(nestMaxDepth)
	crow, ci := rowWith(rows, child)
	_, qi := rowWith(rows, "↪ bram · "+parent)
	if ci < 0 || qi < 0 || qi >= ci {
		t.Fatalf("the reply past the indent cap doesn't name its parent:\n%s", strings.Join(rows, "\n"))
	}
	// quote, header, body: a quote line always forces the header back on, so the
	// reply it introduces is exactly two rows below it.
	if qi != ci-2 {
		t.Fatalf("quote line isn't attached to the reply it introduces (%d vs %d):\n%s",
			qi, ci, strings.Join(rows, "\n"))
	}
	_ = crow
	// And a reply still inside the cap does not get one.
	if _, at := rowWith(rows, "↪ bram · step d0"); at >= 0 {
		t.Fatalf("quoted a parent the indent still points at:\n%s", strings.Join(rows, "\n"))
	}
}

// A reply naming a parent that isn't loaded says so, rather than letting the
// reader assume it answers whatever happens to sit above it.
func TestUnknownParentIsNamedAsUnknown(t *testing.T) {
	m := nestThreadModel(t)
	m.threadPosts[3].Message = replyto.Attach("that one", "gonebeforeweloadedit000000")
	m.renderThread()
	if out := ansi.Strip(m.threadView.View()); !strings.Contains(out, "↪ an earlier message") {
		t.Fatalf("an unresolvable parent went unmentioned:\n%s", out)
	}
}

func TestNestDepthAndIndentCap(t *testing.T) {
	m := nestThreadModel(t)
	// A chain: r1 ← r3 ← r4 ← r5 ← r6 ← r7, deeper than the cap.
	prev := "r1"
	for i, id := range []string{"r3", "r4", "r5", "r6", "r7"} {
		if i == 0 {
			m.threadPosts[3].Id = id
			m.threadPosts[3].Message = replyto.Attach("step "+id, prev)
		} else {
			m.threadPosts = append(m.threadPosts, &model.Post{
				Id: id, ChannelId: "c1", UserId: "u2", RootId: "root1",
				CreateAt: int64(5000 + i*1000), Message: replyto.Attach("step "+id, prev),
			})
		}
		prev = id
	}
	infos := m.nestInfos(m.threadPosts, true, nestMaxDepth)
	if infos == nil {
		t.Fatal("no nesting found in a nested thread")
	}
	last := infos[len(infos)-1]
	if last.depth < nestMaxDepth {
		t.Fatalf("depth = %d, want at least the cap %d", last.depth, nestMaxDepth)
	}
	if got, want := last.indent(maxNestDepth(48)), nestMaxDepth*nestIndentStep; got != want {
		t.Fatalf("indent = %d, want the capped %d", got, want)
	}
	// A narrow pane gives up fewer levels than a wide one.
	if maxNestDepth(24) >= maxNestDepth(96) {
		t.Fatal("a narrow pane indents as deeply as a wide one")
	}
}

// A payload pointing in a circle must not hang the renderer.
func TestNestDepthSurvivesACycle(t *testing.T) {
	m := nestThreadModel(t)
	m.threadPosts[1].Message = replyto.Attach("the rendered lines", "r3")
	m.threadPosts[3].Message = replyto.Attach("that one", "r1")
	done := make(chan []nestInfo, 1)
	go func() { done <- m.nestInfos(m.threadPosts, true, nestMaxDepth) }()
	select {
	case infos := <-done:
		if infos == nil {
			t.Fatal("no nesting found")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nestInfos hung on a cyclic parent chain")
	}
}

// The channel transcript quotes but does not indent: it interleaves whole
// conversations and a staircase through them would obscure the chronology.
func TestChannelPaneQuotesWithoutIndenting(t *testing.T) {
	m := nestThreadModel(t)
	m.threadOpen = false
	m.openChannelID = "c1"
	// The channel transcript is chronological — it is not tree-sorted — so the
	// nested reply sits after the sibling that was sent before it, away from the
	// message it answers.
	m.posts = append([]*model.Post(nil), m.threadPosts...)
	sort.SliceStable(m.posts, func(i, j int) bool { return m.posts[i].CreateAt < m.posts[j].CreateAt })
	m.msgsView.SetWidth(60)
	m.msgsView.SetHeight(20)
	m.renderMessages()
	out := ansi.Strip(m.msgsView.View())

	if !strings.Contains(out, "↪ bram · the rendered lines") {
		t.Fatalf("channel pane didn't say what the reply answers:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "that one") && strings.HasPrefix(l, "   ") {
			t.Fatalf("channel pane indented a nested reply:\n%s", out)
		}
	}
}

// out coerces a handler's return value back to a Model. Handlers are value
// receivers (Update's post-dispatch pass drops anything else), but a *Model is
// accepted so a slip shows up as a real assertion failure rather than a panic.
func out(t *testing.T, v tea.Model) Model {
	t.Helper()
	switch got := v.(type) {
	case Model:
		return got
	case *Model:
		return *got
	}
	t.Fatalf("handler returned %T, want a Model", v)
	return Model{}
}

// --- composer target -------------------------------------------------------

// r on a reply targets it; the strip says so, the prompt says so, and the next
// message carries it.
func TestReplyKeyTargetsTheSelectedReply(t *testing.T) {
	m := nestThreadModel(t)
	m.focus = focusThread
	m.threadIdx = 1 // bram's "the rendered lines"
	m.renderThread()

	next, _ := m.handleThreadKey(keyPress('r'))
	got := out(t, next)
	if got.replyParentID != "r1" {
		t.Fatalf("replyParentID = %q, want r1", got.replyParentID)
	}
	if got.focus != focusInput {
		t.Fatalf("focus = %v, want the composer", got.focus)
	}
	if bar := ansi.Strip(got.replyBar(60)); !strings.Contains(bar, "replying to bram") {
		t.Fatalf("strip above the composer = %q", bar)
	}
	if got.replyBarHeight() != 1 {
		t.Fatal("the strip is drawn but the layout reserves no row for it")
	}
}

// Answering the root is what a plain Mattermost reply already means, so it must
// not encode a parent every client can already see.
func TestReplyKeyOnRootClearsTheTarget(t *testing.T) {
	m := nestThreadModel(t)
	m.focus = focusThread
	m.replyParentID = "r1"
	m.threadIdx = 0 // the root
	next, _ := m.handleThreadKey(keyPress('r'))
	if got := out(t, next); got.replyParentID != "" {
		t.Fatalf("replyParentID = %q, want cleared", got.replyParentID)
	}
}

func TestReplyKeyRefusesAPostThatHasNotLanded(t *testing.T) {
	m := nestThreadModel(t)
	m.focus = focusThread
	m.threadPosts = append(m.threadPosts, &model.Post{UserId: "u1", RootId: "root1", Message: "sending…"})
	m.threadIdx = len(m.threadPosts) - 1
	next, _ := m.handleThreadKey(keyPress('r'))
	got := out(t, next)
	if got.replyParentID != "" {
		t.Fatalf("targeted a post with no id: %q", got.replyParentID)
	}
	if got.status == "" {
		t.Fatal("refused silently")
	}
}

// The target rides on the outgoing body, and only there — the same composer
// answering the thread as a whole must send a plain reply.
func TestSendAttachesTheTargetOnlyWhenSet(t *testing.T) {
	send := func(m Model) string {
		m.focus = focusInput
		m.input.Focus()
		m.input.SetValue("that one")
		next, _ := m.handleInputKey(keyPress(tea.KeyEnter))
		got := out(t, next)
		// Tree order puts the optimistic copy under the message it answers, so
		// it is the one post without a server id — not the last one.
		for _, p := range got.threadPosts {
			if p.Id == "" {
				return p.Message
			}
		}
		t.Fatal("nothing was sent")
		return ""
	}

	m := nestThreadModel(t)
	m.replyParentID = "r1"
	if id, ok := replyto.Parse(send(m)); !ok || id != "r1" {
		t.Fatalf("sent body carries parent %q, %v; want r1", id, ok)
	}

	m = nestThreadModel(t)
	if _, ok := replyto.Parse(send(m)); ok {
		t.Fatal("a plain thread reply carried a parent reference")
	}

	// Targeting the root is not a nested reply either.
	m = nestThreadModel(t)
	m.replyParentID = "root1"
	if _, ok := replyto.Parse(send(m)); ok {
		t.Fatal("answering the root encoded a redundant parent reference")
	}
}

// A send consumes the target: the next message goes to the thread, not silently
// under whatever the last one answered.
func TestSendConsumesTheTarget(t *testing.T) {
	m := nestThreadModel(t)
	m.replyParentID = "r1"
	m.focus = focusInput
	m.input.Focus()
	m.input.SetValue("that one")
	next, _ := m.handleInputKey(keyPress(tea.KeyEnter))
	if got := out(t, next); got.replyParentID != "" {
		t.Fatalf("replyParentID = %q after sending, want cleared", got.replyParentID)
	}
}

// Escape peels the target off before it leaves the composer — the draft and the
// focus both stay put.
func TestEscapePeelsTheTargetBeforeLeaving(t *testing.T) {
	m := nestThreadModel(t)
	m.replyParentID = "r1"
	m.focus = focusInput
	m.input.Focus()
	m.input.SetValue("half a thought")

	next, _ := m.handleInputKey(keyPress(tea.KeyEscape))
	got := out(t, next)
	if got.replyParentID != "" {
		t.Fatal("first escape didn't drop the target")
	}
	if got.focus != focusInput {
		t.Fatal("first escape left the composer as well as dropping the target")
	}
	if got.input.Value() != "half a thought" {
		t.Fatalf("draft lost: %q", got.input.Value())
	}
	next, _ = got.handleInputKey(keyPress(tea.KeyEscape))
	if got2 := out(t, next); got2.focus == focusInput {
		t.Fatal("second escape didn't leave the composer")
	}
}

// Editing a nested reply must not flatten it: the parent it answers survives a
// round trip through the composer.
func TestEditKeepsTheReplyNested(t *testing.T) {
	m := nestThreadModel(t)
	p := nestPost(t, &m, "r3") // the nested one
	m.focus = focusThread
	m.threadIdx = indexOfPost(m.threadPosts, "r3")
	m.beginEditPost(p)
	if m.replyParentID != "r1" {
		t.Fatalf("edit didn't adopt the post's parent: %q", m.replyParentID)
	}
	if got := m.input.Value(); got != "that one" {
		t.Fatalf("composer shows %q; the payload should not be visible", got)
	}
	m.input.SetValue("that one, yes")
	next, _ := m.handleInputKey(keyPress(tea.KeyEnter))
	got := out(t, next)
	if got.replyParentID != "" {
		t.Fatal("the edit target outlived the edit")
	}
	if got.editingPostID != "" {
		t.Fatal("still in edit mode after saving")
	}
}

// Escape while editing un-nests deliberately — the one way to flatten a reply
// after the fact.
func TestEditCanUnNestAReply(t *testing.T) {
	m := nestThreadModel(t)
	m.focus = focusThread
	m.threadIdx = indexOfPost(m.threadPosts, "r3")
	m.beginEditPost(nestPost(t, &m, "r3"))
	if !m.clearReplyParent() {
		t.Fatal("nothing to clear while editing a nested reply")
	}
	if m.replyParentID != "" {
		t.Fatal("target survived the clear")
	}
}

// Switching or closing the thread drops a target that is no longer on screen.
func TestTargetDoesNotOutliveItsThread(t *testing.T) {
	m := nestThreadModel(t)
	m.replyParentID = "r1"
	m.closeThread()
	if m.replyParentID != "" {
		t.Fatal("target survived closing the thread")
	}

	m = nestThreadModel(t)
	m.replyParentID = "r1"
	next, _ := m.openThreadForPost(&model.Post{Id: "other", ChannelId: "c1"})
	if got := out(t, next); got.replyParentID != "" {
		t.Fatal("target followed the composer into a different thread")
	}
}

// p walks back up to what a reply answers — the way out of a deep nest, and the
// only way to reach a parent that has scrolled away.
func TestGotoParentMovesTheSelection(t *testing.T) {
	m := nestThreadModel(t)
	m.focus = focusThread
	m.threadIdx = indexOfPost(m.threadPosts, "r3")
	want := indexOfPost(m.threadPosts, "r1")
	next, _ := m.handleThreadKey(keyPress('p'))
	if got := out(t, next); got.threadIdx != want {
		t.Fatalf("threadIdx = %d, want %d (the message it answers)", got.threadIdx, want)
	}

	m = nestThreadModel(t)
	m.focus = focusThread
	m.threadIdx = indexOfPost(m.threadPosts, "r2") // a flat reply
	at := m.threadIdx
	next, _ = m.handleThreadKey(keyPress('p'))
	if got := out(t, next); got.threadIdx != at || got.status == "" {
		t.Fatalf("threadIdx = %d, status = %q; want no move and an explanation", got.threadIdx, got.status)
	}
}

// r in the channel transcript means the same thing it means in the thread pane:
// answer this message. On a reply that is now something the wire can express.
func TestReplyKeyInChannelAimsAtTheSelectedReply(t *testing.T) {
	m := nestThreadModel(t)
	m.threadOpen = false
	m.focus = focusMessages
	m.posts = m.threadPosts
	m.postIdx = 1 // bram's reply
	next, _ := m.handleMessagesKey(keyPress('r'))
	got := out(t, next)
	if !got.threadOpen || got.threadRootID != "root1" {
		t.Fatalf("thread not opened: open=%v root=%q", got.threadOpen, got.threadRootID)
	}
	if got.replyParentID != "r1" {
		t.Fatalf("replyParentID = %q, want r1", got.replyParentID)
	}
}

// On a root there is nothing to name — a reply to the root is exactly what a
// plain Mattermost reply already is.
func TestReplyKeyInChannelOnARootAimsAtTheThread(t *testing.T) {
	m := nestThreadModel(t)
	m.threadOpen = false
	m.focus = focusMessages
	m.posts = m.threadPosts
	m.postIdx = 0 // the root
	next, _ := m.handleMessagesKey(keyPress('r'))
	if got := out(t, next); got.replyParentID != "" {
		t.Fatalf("replyParentID = %q, want empty", got.replyParentID)
	}
}

// enter opens a thread to read it and must not disturb a target already set.
func TestOpenThreadKeepsAnExistingTarget(t *testing.T) {
	m := nestThreadModel(t)
	m.replyParentID = "r1"
	m.focus = focusMessages
	m.posts = m.threadPosts
	m.postIdx = 2
	next, _ := m.handleMessagesKey(keyPress(tea.KeyEnter))
	if got := out(t, next); got.replyParentID != "r1" {
		t.Fatalf("replyParentID = %q; opening an already-open thread dropped the target", got.replyParentID)
	}
}

// The nesting scan runs over every post in the render window on every render, so
// its flat-channel cost (one substring search per post, no allocation) is the
// number that matters. See PERF_NOTES.md on the View hot path.
func BenchmarkNestInfos(b *testing.B) {
	mk := func(nested bool) []*model.Post {
		posts := make([]*model.Post, 400)
		for i := range posts {
			body := "a fairly ordinary message body, about the length people actually type in chat"
			if nested && i > 0 && i%10 == 0 {
				body = replyto.Attach(body, posts[i-1].Id)
			}
			posts[i] = &model.Post{Id: "p" + strconv.Itoa(i), RootId: "root1", Message: body}
		}
		return posts
	}
	m := &Model{}
	for _, tc := range []struct {
		name  string
		posts []*model.Post
	}{{"flat", mk(false)}, {"nested", mk(true)}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				m.nestInfos(tc.posts, false, 0)
			}
		})
	}
}

// The reply handlers must hand Update a Model *value*: it drops anything else on
// the floor, skipping the focus / selection-bar / status reconciliation every
// event depends on (see Update).
func TestReplyHandlersReturnAValueModel(t *testing.T) {
	m := nestThreadModel(t)
	m.focus = focusThread
	m.threadIdx = indexOfPost(m.threadPosts, "r3")
	for name, call := range map[string]func() tea.Model{
		"set target":   func() tea.Model { v, _ := m.setReplyParent(m.threadPosts[1]); return v },
		"no selection": func() tea.Model { m2 := m; m2.threadIdx = -1; v, _ := m2.setReplyParent(nil); return v },
		"goto parent":  func() tea.Model { v, _ := m.gotoReplyParent(); return v },
		"not a nested": func() tea.Model { m2 := m; m2.threadIdx = 2; v, _ := m2.gotoReplyParent(); return v },
	} {
		if _, ok := call().(Model); !ok {
			t.Errorf("%s: handler returned a pointer, so Update would skip its post-dispatch pass", name)
		}
	}
}

// End to end through Update, the way a user actually does it: put the cursor on a
// reply, press r, type, send — then let the server's copy come back over the
// WebSocket and replace the optimistic stub. The nesting has to survive all of
// it, because every one of those steps is a place it could be dropped.
func TestNestedReplySurvivesSendAndWSEcho(t *testing.T) {
	m := nestThreadModel(t)
	m.focus = focusThread
	m.threadIdx = indexOfPost(m.threadPosts, "r2") // sanne's "or the markdown"

	// r targets it.
	m = asModel(t, m, keyPress('r'))
	if m.replyParentID != "r2" {
		t.Fatalf("r through Update didn't target the reply: %q", m.replyParentID)
	}
	if m.focus != focusInput {
		t.Fatalf("focus = %v, want the composer", m.focus)
	}

	// Type and send. The optimistic copy is already sorted under its parent, so
	// it is found by id rather than at the end of the slice.
	m.input.SetValue("agreed, cache the markdown")
	m = asModel(t, m, keyPress(tea.KeyEnter))
	var stub *model.Post
	for _, p := range m.threadPosts {
		if p.Id == "" {
			stub = p
		}
	}
	if stub == nil {
		t.Fatal("no optimistic copy of the sent reply")
	}
	if id, ok := replyto.Parse(stub.Message); !ok || id != "r2" {
		t.Fatalf("the sent body carries parent %q, %v; want r2", id, ok)
	}
	if at := indexOfPost(m.threadPosts, "r2"); m.threadPosts[at+1] != stub {
		t.Fatalf("the optimistic copy isn't under the message it answers:\n%s",
			strings.Join(threadRows(&m), "\n"))
	}

	// The server's copy arrives and replaces the stub, byte for byte.
	landed := &model.Post{
		Id: "r9", ChannelId: "c1", UserId: "u1", RootId: "root1",
		CreateAt: 9000, Message: stub.Message,
	}
	m.appendThreadPost(landed)
	for _, p := range m.threadPosts {
		if p.Id == "" {
			t.Fatal("the optimistic stub outlived the server's copy")
		}
	}

	// Still under its parent, one indent step in, in the pane.
	rows := threadRows(&m)
	prow, pi := rowWith(rows, "or the markdown")
	crow, ci := rowWith(rows, "agreed, cache the markdown")
	if pi < 0 || ci < 0 || ci <= pi {
		t.Fatalf("the landed reply isn't under its parent:\n%s", strings.Join(rows, "\n"))
	}
	if got, want := indentOf(crow), indentOf(prow)+nestIndentStep; got != want {
		t.Fatalf("landed reply indent = %d, want %d:\n%s", got, want, strings.Join(rows, "\n"))
	}

	// The channel transcript stays chronological, so there the reply is marked by
	// its quote line instead.
	m.threadOpen = false
	m.posts = append(append([]*model.Post(nil), m.threadPosts...), nil)
	m.posts = m.posts[:len(m.posts)-1]
	sort.SliceStable(m.posts, func(i, j int) bool { return m.posts[i].CreateAt < m.posts[j].CreateAt })
	m.msgsView.SetWidth(60)
	m.msgsView.SetHeight(24)
	m.renderMessages()
	if got := ansi.Strip(m.msgsView.View()); !strings.Contains(got, "↪ sanne · or the markdown") {
		t.Fatalf("channel pane didn't mark the nested reply:\n%s", got)
	}
}

// The reply key has to be *on screen* in the thread pane, not just in the
// cheatsheet: it is the one key there that does something the pane can't
// otherwise express, and a key nobody can find is a feature nobody has. It must
// also survive the footer's truncation on a narrow terminal.
func TestThreadFooterAdvertisesTheReplyKey(t *testing.T) {
	for _, w := range []int{80, 100, 140} {
		m := nestThreadModel(t)
		m.width, m.height = w, 30
		m.focus = focusThread
		got := ansi.Strip(m.renderFooter())
		for _, want := range []string{"r reply to message", "p jump to parent"} {
			if !strings.Contains(got, want) {
				t.Errorf("width %d: footer is missing %q:\n%s", w, want, got)
			}
		}
	}
}

// nestSortThread's contract, which the renderer relies on: send order within
// siblings, a flat thread untouched, idempotent (the live append path re-sorts
// after every message), and nothing ever lost.
func TestNestSortThreadContract(t *testing.T) {
	mk := func(id, parent string, at int64) *model.Post {
		msg := "m" + id
		if parent != "" {
			msg = replyto.Attach(msg, parent)
		}
		return &model.Post{Id: id, RootId: "root1", CreateAt: at, Message: msg}
	}
	ids := func(posts []*model.Post) []string {
		out := make([]string, len(posts))
		for i, p := range posts {
			out[i] = p.Id
		}
		return out
	}

	t.Run("a flat thread is returned untouched", func(t *testing.T) {
		in := []*model.Post{mk("root1", "", 1), mk("a", "", 2), mk("b", "", 3), mk("c", "", 4)}
		if got := strings.Join(ids(nestSortThread(in)), ","); got != "root1,a,b,c" {
			t.Fatalf("order = %s, want it unchanged", got)
		}
	})

	t.Run("descendants follow their parent, siblings in send order", func(t *testing.T) {
		in := []*model.Post{
			mk("root1", "", 1), mk("a", "", 2), mk("b", "", 3),
			mk("b1", "b", 4), mk("a1", "a", 5), mk("b2", "b", 6), mk("b1a", "b1", 7),
		}
		if got := strings.Join(ids(nestSortThread(in)), ","); got != "root1,a,a1,b,b1,b1a,b2" {
			t.Fatalf("order = %s, want root1,a,a1,b,b1,b1a,b2", got)
		}
	})

	t.Run("a reply naming the root stays a sibling", func(t *testing.T) {
		in := []*model.Post{mk("root1", "", 1), mk("a", "", 2), mk("b", "root1", 3)}
		if got := strings.Join(ids(nestSortThread(in)), ","); got != "root1,a,b" {
			t.Fatalf("order = %s; answering the root is what every reply already does", got)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		in := []*model.Post{
			mk("root1", "", 1), mk("a", "", 2), mk("b", "", 3),
			mk("b1", "b", 4), mk("a1", "a", 5), mk("b1a", "b1", 6),
		}
		once := nestSortThread(in)
		if got, want := strings.Join(ids(nestSortThread(once)), ","), strings.Join(ids(once), ","); got != want {
			t.Fatalf("re-sorting moved posts:\n got %s\nwant %s", got, want)
		}
	})

	t.Run("a payload cycle costs an indent, never a post", func(t *testing.T) {
		// a↔b name each other; nothing may vanish and nothing may hang.
		in := []*model.Post{mk("root1", "", 1), mk("a", "b", 2), mk("b", "a", 3), mk("c", "", 4)}
		got := nestSortThread(in)
		if len(got) != len(in) {
			t.Fatalf("lost a post to a cycle: %s", strings.Join(ids(got), ","))
		}
		seen := map[string]int{}
		for _, p := range got {
			seen[p.Id]++
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("post %s appears %d times: %s", id, n, strings.Join(ids(got), ","))
			}
		}
	})
}

// Tree order means the newest message is no longer the last row, so the paths
// that follow live conversation have to ask for it by time.
func TestFollowsTheNewestMessageNotTheLastRow(t *testing.T) {
	m := nestThreadModel(t)
	m.threadIdx = newestPostIdx(m.threadPosts)

	// A reply to the *first* reply lands mid-pane; the cursor has to follow it.
	landed := &model.Post{
		Id: "r9", ChannelId: "c1", UserId: "u2", RootId: "root1", CreateAt: 9000,
		Message: replyto.Attach("one more thing", "r1"),
	}
	m.appendThreadPost(landed)
	if at := indexOfPost(m.threadPosts, "r9"); m.threadIdx != at {
		t.Fatalf("threadIdx = %d, want %d (the message that just arrived)", m.threadIdx, at)
	}
	if m.threadIdx == len(m.threadPosts)-1 {
		t.Fatal("this case is only meaningful when the new message is not the last row")
	}
}

// Pressing enter on a reply in the channel opens its thread with the cursor on
// that reply — acting on a message and finding the cursor elsewhere is the kind
// of small betrayal that makes a key feel broken.
func TestOpenThreadOnAReplyLandsOnIt(t *testing.T) {
	m := nestThreadModel(t)
	posts := m.threadPosts
	m.threadOpen = false
	m.threadPosts = nil
	m.focus = focusMessages
	m.posts = posts
	m.postIdx = indexOfPost(posts, "r2")

	next, _ := m.handleMessagesKey(keyPress(tea.KeyEnter))
	got := out(t, next)
	if got.threadSelectID != "r2" {
		t.Fatalf("threadSelectID = %q, want r2", got.threadSelectID)
	}
	got2, _ := got.Update(threadLoadedMsg{rootID: "root1", posts: posts})
	landed := out(t, got2)
	if want := indexOfPost(landed.threadPosts, "r2"); landed.threadIdx != want {
		t.Fatalf("threadIdx = %d, want %d (the reply enter was pressed on)", landed.threadIdx, want)
	}
	if landed.threadSelectID != "" {
		t.Fatal("the pending selection outlived the load it was for")
	}
}

// Opening a thread on its root means "read this thread", which lands on the
// newest message rather than on the root.
func TestOpenThreadOnTheRootLandsOnTheNewest(t *testing.T) {
	m := nestThreadModel(t)
	posts := m.threadPosts
	m.threadOpen = false
	m.threadPosts = nil
	m.focus = focusMessages
	m.posts = posts
	m.postIdx = indexOfPost(posts, "root1")

	next, _ := m.handleMessagesKey(keyPress(tea.KeyEnter))
	got := out(t, next)
	if got.threadSelectID != "" {
		t.Fatalf("threadSelectID = %q, want none for a root", got.threadSelectID)
	}
	got2, _ := got.Update(threadLoadedMsg{rootID: "root1", posts: posts})
	landed := out(t, got2)
	if want := newestPostIdx(landed.threadPosts); landed.threadIdx != want {
		t.Fatalf("threadIdx = %d, want %d (the newest message)", landed.threadIdx, want)
	}
}
