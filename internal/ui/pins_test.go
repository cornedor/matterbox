package ui

import (
	"errors"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/editor"
	"matterbox/internal/viewport"
)

// newRenderableModel is the minimum Model that survives a renderMessages /
// renderThread call and can open the sheet modals: sized viewports, a
// composer and the default keymap.
func newRenderableModel() Model {
	m := Model{
		keys:       newKeyMap("ctrl"),
		width:      100,
		height:     30,
		msgsView:   viewport.New(),
		threadView: viewport.New(),
		input:      editor.New(),
		userNames:  map[string]string{},
	}
	m.msgsView.SetWidth(60)
	m.msgsView.SetHeight(20)
	m.threadView.SetWidth(30)
	m.threadView.SetHeight(20)
	ks := viewport.New()
	ks.SoftWrap = true
	m.keysSheetView = &ks
	return m
}

// selectedPost follows focus, because the selection bar does (selBarWanted):
// the thread reply under the bar when the thread pane has focus, the channel
// post when the message pane does, nothing from the composer or elsewhere —
// a command must not act on a message the user can't see selected.
func TestSelectedPostFollowsFocus(t *testing.T) {
	root := &model.Post{Id: "root", ChannelId: "c1"}
	reply := &model.Post{Id: "reply", ChannelId: "c1", RootId: "root"}
	m := Model{
		posts: []*model.Post{root}, postIdx: 0,
		threadOpen: true, threadPosts: []*model.Post{root, reply}, threadIdx: 1,
	}
	cases := []struct {
		focus focus
		want  *model.Post
	}{
		{focusMessages, root},
		{focusThread, reply},
		{focusInput, nil},
		{focusFeed, nil},
		{focusTeams, nil},
	}
	for _, c := range cases {
		m.focus = c.focus
		if got := m.selectedPost(); got != c.want {
			t.Errorf("focus %v: selectedPost = %v, want %v", c.focus, got, c.want)
		}
	}
	m.focus, m.postIdx = focusMessages, 7 // stale index
	if got := m.selectedPost(); got != nil {
		t.Errorf("out-of-range postIdx: selectedPost = %v, want nil", got)
	}
}

// The pin toggle's label follows the selected post's state; the pin commands
// are listed whenever a channel is open (like Mute), regardless of focus,
// and gone when none is.
func TestPinCommand(t *testing.T) {
	ch := &model.Channel{Id: "c1", TeamId: "t1", DisplayName: "general", Name: "general"}
	p := &model.Post{Id: "p1", ChannelId: "c1", Message: "hi"}
	m := Model{
		teams:         []*model.Team{{Id: "t1"}},
		channels:      map[string][]*model.Channel{"t1": {ch}},
		openChannelID: "c1",
		posts:         []*model.Post{p}, postIdx: 0,
		focus: focusMessages,
	}
	if name := m.pinCommand().name; name != "Pin message" {
		t.Fatalf("unpinned: %q", name)
	}
	p.IsPinned = true
	if name := m.pinCommand().name; name != "Unpin message" {
		t.Fatalf("pinned: %q", name)
	}
	cmds, ok := m.pinCommands()
	if !ok || len(cmds) != 2 || cmds[0].name != "Unpin message" || cmds[1].name != "Pinned messages" {
		t.Fatalf("pinCommands = %v ok=%v", cmdNames(cmds), ok)
	}
	// From the composer the toggle is still listed (its default label, since
	// nothing is selected) and running it reports that instead of acting.
	m.focus = focusInput
	cmds, ok = m.pinCommands()
	if !ok || cmds[0].name != "Pin message" {
		t.Fatalf("composer focus: pinCommands = %v ok=%v", cmdNames(cmds), ok)
	}
	if cmd := runTogglePinned(&m, ""); cmd != nil || m.status != "no message selected" || !p.IsPinned {
		t.Fatalf("run without selection: cmd=%v status=%q pinned=%v", cmd != nil, m.status, p.IsPinned)
	}
	m.openChannelID = ""
	if _, ok := m.pinCommands(); ok {
		t.Fatal("no open channel: pin commands should not be listed")
	}
}

// runTogglePinned flips the post at once (so the label and the "· pinned"
// mark update before the round-trip) and applyPinnedChanged reverts on error.
func TestTogglePinnedOptimistic(t *testing.T) {
	m := newRenderableModel()
	p := &model.Post{Id: "p1", ChannelId: "c1", Message: "hi", UserId: "u1"}
	m.posts = []*model.Post{p}
	m.postIdx = 0
	m.focus = focusMessages
	if cmd := runTogglePinned(&m, ""); cmd == nil {
		t.Fatal("expected the server command")
	}
	if !p.IsPinned || m.status != "pinning message…" {
		t.Fatalf("after run: pinned=%v status=%q", p.IsPinned, m.status)
	}
	next, _ := m.applyPinnedChanged(pinnedChangedMsg{channelID: "c1", postID: "p1", pinned: true, err: errors.New("boom")})
	got := next.(Model)
	if p.IsPinned || got.status != "boom" {
		t.Fatalf("failed pin should revert: pinned=%v status=%q", p.IsPinned, got.status)
	}
}

func cmdNames(cmds []switcherCommand) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.name
	}
	return out
}
