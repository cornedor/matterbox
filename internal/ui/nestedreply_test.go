package ui

import (
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
	m.threadPosts = []*model.Post{
		{Id: "root1", ChannelId: "c1", UserId: "u1", CreateAt: 1000, Message: "what should we cache?"},
		{Id: "r1", ChannelId: "c1", UserId: "u2", RootId: "root1", CreateAt: 2000, Message: "the rendered lines"},
		{Id: "r2", ChannelId: "c1", UserId: "u3", RootId: "root1", CreateAt: 3000, Message: "or the markdown"},
		{Id: "r3", ChannelId: "c1", UserId: "u1", RootId: "root1", CreateAt: 4000,
			Message: replyto.Attach("that one", "r1")},
	}
	return m
}

// The point of the whole feature: a reply that answers one message inside the
// thread is drawn under it and says so, while its siblings stay where they are.
func TestNestedReplyIsIndentedAndQuoted(t *testing.T) {
	m := nestThreadModel(t)
	m.renderThread()
	out := ansi.Strip(m.threadView.View())

	if !strings.Contains(out, "↪ bram · the rendered lines") {
		t.Fatalf("nested reply doesn't quote what it answers:\n%s", out)
	}
	var body string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "that one") {
			body = l
			break
		}
	}
	if body == "" {
		t.Fatalf("nested reply is missing from the pane:\n%s", out)
	}
	if got := len(body) - len(strings.TrimLeft(body, " ")); got != nestIndentStep+2 {
		// +2 is the ordinary body gutter every post already carries.
		t.Fatalf("nested reply indented by %d columns, want %d:\n%s", got, nestIndentStep+2, out)
	}
	// The siblings it did not answer keep their own place.
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "or the markdown") && strings.HasPrefix(l, "    ") {
			t.Fatalf("an unrelated reply got indented:\n%s", out)
		}
	}
}

// A flat thread must render exactly as it did before nested replies existed —
// the feature costs nothing where it isn't used.
func TestFlatThreadRendersUnchanged(t *testing.T) {
	m := nestThreadModel(t)
	m.threadPosts = m.threadPosts[:3] // drop the nested reply
	m.renderThread()
	before := ansi.Strip(m.threadView.View())

	if m.nestInfos(m.threadPosts) != nil {
		t.Fatal("a flat thread built nesting state; the fast path is gone")
	}
	for _, l := range strings.Split(before, "\n") {
		if strings.Contains(l, "↪") {
			t.Fatalf("a flat thread drew a nesting hint: %q", l)
		}
	}
}

// A reply whose parent is the line directly above needs no quote — the indent
// already says it — but it still steps in.
func TestQuoteSuppressedWhenParentIsDirectlyAbove(t *testing.T) {
	m := nestThreadModel(t)
	m.threadPosts[3].Message = replyto.Attach("that one", "r2") // answers the post above it
	m.renderThread()
	out := ansi.Strip(m.threadView.View())

	if strings.Contains(out, "↪ sanne") {
		t.Fatalf("quoted the message directly above, which the indent already shows:\n%s", out)
	}
	if !strings.Contains(out, "  "+strings.Repeat(" ", nestIndentStep)+"that one") {
		t.Fatalf("reply to the line above wasn't indented:\n%s", out)
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
	infos := m.nestInfos(m.threadPosts)
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
	go func() { done <- m.nestInfos(m.threadPosts) }()
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
	m.posts = m.threadPosts
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
		if len(got.threadPosts) == 0 {
			t.Fatal("nothing was sent")
		}
		return got.threadPosts[len(got.threadPosts)-1].Message
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
	p := m.threadPosts[3] // the nested one
	m.focus = focusThread
	m.threadIdx = 3
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
	m.threadIdx = 3
	m.beginEditPost(m.threadPosts[3])
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
	m.threadIdx = 3
	next, _ := m.handleThreadKey(keyPress('p'))
	if got := out(t, next); got.threadIdx != 1 {
		t.Fatalf("threadIdx = %d, want 1 (the message it answers)", got.threadIdx)
	}

	m = nestThreadModel(t)
	m.focus = focusThread
	m.threadIdx = 2 // a flat reply
	next, _ = m.handleThreadKey(keyPress('p'))
	if got := out(t, next); got.threadIdx != 2 || got.status == "" {
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
				m.nestInfos(tc.posts)
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
	m.threadIdx = 3
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
