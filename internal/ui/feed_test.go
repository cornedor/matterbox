package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

func newBreadcrumbModel() Model {
	return Model{
		teams:     []*model.Team{{Id: "t1", DisplayName: "Engineering", Name: "eng"}},
		userNames: map[string]string{"u2": "alice"},
		me:        &model.User{Id: "u1", Username: "me"},
	}
}

func TestChannelBreadcrumb(t *testing.T) {
	m := newBreadcrumbModel()
	cases := []struct {
		name string
		ch   *model.Channel
		want string
	}{
		{
			name: "public channel prefixes its team",
			ch:   &model.Channel{Type: model.ChannelTypeOpen, TeamId: "t1", DisplayName: "General"},
			want: "Engineering › #General",
		},
		{
			name: "private channel prefixes its team",
			ch:   &model.Channel{Type: model.ChannelTypePrivate, TeamId: "t1", DisplayName: "Secrets"},
			want: "Engineering › 🔒Secrets",
		},
		{
			name: "direct message is under DMs",
			ch:   &model.Channel{Type: model.ChannelTypeDirect, Name: "u1__u2"},
			want: "DMs › @alice",
		},
		{
			name: "group message is under DMs",
			ch:   &model.Channel{Type: model.ChannelTypeGroup, DisplayName: "Group Chat"},
			want: "DMs › ·Group Chat",
		},
		{
			name: "unknown team falls back to the bare label",
			ch:   &model.Channel{Type: model.ChannelTypeOpen, TeamId: "gone", DisplayName: "Orphan"},
			want: "#Orphan",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.channelBreadcrumb(tc.ch); got != tc.want {
				t.Errorf("channelBreadcrumb = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestUnreadFromPostList(t *testing.T) {
	// Order is newest-first as the server returns it.
	pl := &model.PostList{
		Order: []string{"p5", "p4", "p3", "p2", "p1"},
		Posts: map[string]*model.Post{
			"p1": {Id: "p1", CreateAt: 100, Message: "already read"},
			"p2": {Id: "p2", CreateAt: 200, Message: "first unread"},
			"p3": {Id: "p3", CreateAt: 250, Message: "system", Type: "system_join_channel"},
			"p4": {Id: "p4", CreateAt: 300, Message: "deleted", DeleteAt: 999},
			"p5": {Id: "p5", CreateAt: 350, Message: "second unread"},
		},
	}
	got := unreadFromPostList(pl, 150)
	if len(got) != 2 {
		t.Fatalf("got %d posts; want 2 (%v)", len(got), got)
	}
	// Oldest → newest, system + deleted + already-read filtered out.
	if got[0].Id != "p2" || got[1].Id != "p5" {
		t.Errorf("got order [%s %s]; want [p2 p5]", got[0].Id, got[1].Id)
	}
}

func TestUnreadFromPostListNoBoundary(t *testing.T) {
	// A non-positive boundary keeps every non-system, non-deleted post
	// (the caller already limited the page).
	pl := &model.PostList{
		Order: []string{"p2", "p1"},
		Posts: map[string]*model.Post{
			"p1": {Id: "p1", CreateAt: 100, Message: "a"},
			"p2": {Id: "p2", CreateAt: 200, Message: "b"},
		},
	}
	got := unreadFromPostList(pl, 0)
	if len(got) != 2 || got[0].Id != "p1" || got[1].Id != "p2" {
		t.Fatalf("got %v; want [p1 p2]", got)
	}
}

func TestCapUnread(t *testing.T) {
	mk := func(ids ...string) []*model.Post {
		out := make([]*model.Post, len(ids))
		for i, id := range ids {
			out[i] = &model.Post{Id: id}
		}
		return out
	}
	ids := func(ps []*model.Post) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = p.Id
		}
		return out
	}

	// The regression: a stale/zero boundary surfaced more posts than the
	// channel's count-based "N new" total. capUnread keeps only the newest
	// `count`, so the body can't contradict the header.
	got := capUnread(mk("a", "b", "c", "d", "e"), 2)
	if want := []string{"d", "e"}; !slices.Equal(ids(got), want) {
		t.Errorf("count cap: got %v; want %v", ids(got), want)
	}

	// A non-positive count means "unknown" — leave the slice to the
	// feedUnreadMax ceiling instead of zeroing the bubble.
	got = capUnread(mk("a", "b", "c"), 0)
	if want := []string{"a", "b", "c"}; !slices.Equal(ids(got), want) {
		t.Errorf("zero count: got %v; want %v", ids(got), want)
	}

	// A count at/above the slice length is a no-op.
	got = capUnread(mk("a", "b"), 5)
	if want := []string{"a", "b"}; !slices.Equal(ids(got), want) {
		t.Errorf("count above len: got %v; want %v", ids(got), want)
	}

	// feedUnreadMax is the absolute ceiling even when the count is larger.
	big := make([]string, feedUnreadMax+10)
	for i := range big {
		big[i] = strconv.Itoa(i)
	}
	got = capUnread(mk(big...), feedUnreadMax+5)
	if len(got) != feedUnreadMax {
		t.Errorf("feedUnreadMax ceiling: got %d posts; want %d", len(got), feedUnreadMax)
	}
	if got[len(got)-1].Id != big[len(big)-1] {
		t.Errorf("feedUnreadMax ceiling kept the wrong tail: last = %s; want %s", got[len(got)-1].Id, big[len(big)-1])
	}
}

func TestFeedEntryLastActivity(t *testing.T) {
	e := feedEntry{unread: []*model.Post{
		{Id: "a", CreateAt: 10},
		{Id: "b", CreateAt: 42},
	}}
	if got := e.lastActivity(); got != 42 {
		t.Errorf("lastActivity = %d; want 42", got)
	}
	if got := (feedEntry{}).lastActivity(); got != 0 {
		t.Errorf("empty lastActivity = %d; want 0", got)
	}
}

// TestFetchFeedPhantomEntry covers the off-by-one ghost: a channel the server
// still counts as unread whose only post past the read boundary is deleted (or
// a system post). unreadFromPostList drops it, so `full` is empty — but rather
// than vanish (leaving the tab badge counting a channel the pane never shows),
// fetchFeed must surface a phantom entry: a stale count with no message, which
// opening or marking read will clear.
func TestFetchFeedPhantomEntry(t *testing.T) {
	mustJSON := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return b
	}
	// One post past the boundary, but deleted — the exact town-square case:
	// it bumped total_msg_count_root yet leaves nothing to read.
	postsJSON := mustJSON(model.PostList{
		Order: []string{"p1"},
		Posts: map[string]*model.Post{
			"p1": {Id: "p1", ChannelId: "c", CreateAt: 1000, DeleteAt: 2000, Message: "gone"},
		},
	})
	membersJSON := mustJSON(model.ChannelMembersWithTeamData{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/channel_members"):
			_, _ = w.Write(membersJSON)
		case strings.HasSuffix(r.URL.Path, "/posts"):
			_, _ = w.Write(postsJSON)
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	m := Model{
		client:    mm.New(srv.URL, "token"),
		ctx:       context.Background(),
		me:        &model.User{Id: "me", Username: "me"},
		userNames: map[string]string{"me": "me"},
	}

	targets := []feedTarget{{channelID: "c", lastViewedAt: 1, unreadCount: 1}}
	msg, ok := m.fetchFeed(1, targets)().(feedLoadedMsg)
	if !ok {
		t.Fatalf("fetchFeed did not return feedLoadedMsg")
	}
	if len(msg.entries) != 1 {
		t.Fatalf("entries = %d; want 1 phantom entry", len(msg.entries))
	}
	switch e := msg.entries[0]; {
	case !e.phantom:
		t.Error("entry.phantom = false; want true")
	case len(e.unread) != 0:
		t.Errorf("entry.unread = %d; want 0 (phantom has no message)", len(e.unread))
	case e.channelID != "c":
		t.Errorf("entry.channelID = %q; want \"c\"", e.channelID)
	}
}

func TestChannelMuted(t *testing.T) {
	muted := model.ChannelMember{ChannelId: "c-muted", NotifyProps: model.StringMap{
		model.MarkUnreadNotifyProp: model.ChannelMarkUnreadMention,
	}}
	loud := model.ChannelMember{ChannelId: "c-loud", NotifyProps: model.StringMap{
		model.MarkUnreadNotifyProp: model.ChannelMarkUnreadAll,
	}}
	m := Model{members: model.ChannelMembersWithTeamData{
		{ChannelMember: muted},
		{ChannelMember: loud},
	}}
	m.rebuildMutedChannels()
	if !m.channelMuted("c-muted") {
		t.Error("channelMuted(c-muted) = false; want true")
	}
	if m.channelMuted("c-loud") {
		t.Error("channelMuted(c-loud) = true; want false")
	}
	if m.channelMuted("c-unknown") {
		t.Error("channelMuted(c-unknown) = true; want false (no member row)")
	}
}

func TestFeedBadgeCountsSkipsMuted(t *testing.T) {
	muted := model.ChannelMember{ChannelId: "c-muted", NotifyProps: model.StringMap{
		model.MarkUnreadNotifyProp: model.ChannelMarkUnreadMention,
	}}
	loud := model.ChannelMember{ChannelId: "c-loud", NotifyProps: model.StringMap{
		model.MarkUnreadNotifyProp: model.ChannelMarkUnreadAll,
	}}
	m := Model{
		members: model.ChannelMembersWithTeamData{
			{ChannelMember: muted},
			{ChannelMember: loud},
		},
		// Both channels carry unread + a mention; only the loud one counts.
		unread:   map[string]int{"c-muted": 3, "c-loud": 1},
		mentions: map[string]int{"c-muted": 2, "c-loud": 1},
	}
	m.rebuildMutedChannels()
	unread, mention := m.feedBadgeCounts()
	if unread != 1 {
		t.Errorf("unread badge = %d; want 1 (muted channel excluded)", unread)
	}
	if mention != 1 {
		t.Errorf("mention badge = %d; want 1 (muted channel excluded)", mention)
	}
}

// mutedFeedModel builds a model with one muted and one loud channel, both
// unread, on the Feed tab — the state every show-muted assertion needs.
func mutedFeedModel(showMuted bool) Model {
	member := func(id, level string) model.ChannelMemberWithTeamData {
		return model.ChannelMemberWithTeamData{ChannelMember: model.ChannelMember{
			ChannelId:   id,
			NotifyProps: model.StringMap{model.MarkUnreadNotifyProp: level},
		}}
	}
	m := Model{
		channels: map[string][]*model.Channel{
			"t1": {
				{Id: "c-muted", DisplayName: "noisy", Type: model.ChannelTypeOpen},
				{Id: "c-loud", DisplayName: "general", Type: model.ChannelTypeOpen},
			},
		},
		members: model.ChannelMembersWithTeamData{
			member("c-muted", model.ChannelMarkUnreadMention),
			member("c-loud", model.ChannelMarkUnreadAll),
		},
		unread:   map[string]int{"c-muted": 3, "c-loud": 1},
		mentions: map[string]int{},
		feed:     newFeedState(showMuted),
	}
	m.rebuildMutedChannels()
	return m
}

func TestFeedExcludesMuted(t *testing.T) {
	hiding := mutedFeedModel(false)
	if !hiding.feedExcludes("c-muted") {
		t.Error("feedExcludes(c-muted) = false with show-muted off; want true")
	}
	if hiding.feedExcludes("c-loud") {
		t.Error("feedExcludes(c-loud) = true; want false (not muted)")
	}
	if n := hiding.feedHiddenMuted(); n != 1 {
		t.Errorf("feedHiddenMuted = %d; want 1", n)
	}
	if unread, _ := hiding.feedBadgeCounts(); unread != 1 {
		t.Errorf("unread badge = %d; want 1 (muted channel excluded)", unread)
	}

	showing := mutedFeedModel(true)
	if showing.feedExcludes("c-muted") {
		t.Error("feedExcludes(c-muted) = true with show-muted on; want false")
	}
	if n := showing.feedHiddenMuted(); n != 0 {
		t.Errorf("feedHiddenMuted = %d with show-muted on; want 0", n)
	}
	// The badge must count exactly what the feed would show, both ways.
	if unread, _ := showing.feedBadgeCounts(); unread != 2 {
		t.Errorf("unread badge = %d; want 2 (muted channel included)", unread)
	}
}

// TestToggleFeedMutedFlips covers the M key's effect on the filter itself. The
// rebuild it returns needs a client, so only the flag + status are asserted.
func TestToggleFeedMutedFlips(t *testing.T) {
	m := mutedFeedModel(false)
	m.toggleFeedMuted()
	if !m.feed.showMuted {
		t.Fatal("toggleFeedMuted did not turn show-muted on")
	}
	if !strings.Contains(m.status, "showing muted") {
		t.Errorf("status = %q; want it to mention showing muted", m.status)
	}
	m.toggleFeedMuted()
	if m.feed.showMuted {
		t.Error("second toggleFeedMuted did not turn show-muted back off")
	}
}

// TestFeedAppendPostedMuted pins the live-WS path to the same rule as the
// build: a muted channel's post only makes a bubble when muted are shown, and
// then it joins at the bottom rather than jumping the queue.
func TestFeedAppendPostedMuted(t *testing.T) {
	post := &model.Post{Id: "p1", ChannelId: "c-muted", CreateAt: 200, Message: "hi"}

	hiding := mutedFeedModel(false)
	hiding.feed.built = true
	hiding.feedAppendPosted(post)
	if len(hiding.feed.entries) != 0 {
		t.Errorf("feed gained %d entries for a muted channel; want 0", len(hiding.feed.entries))
	}

	showing := mutedFeedModel(true)
	showing.feed.built = true
	showing.feed.entries = []feedEntry{{
		channelID: "c-loud",
		unread:    []*model.Post{{Id: "p0", ChannelId: "c-loud", CreateAt: 100}},
	}}
	showing.feedAppendPosted(post)
	if len(showing.feed.entries) != 2 {
		t.Fatalf("feed has %d entries; want 2", len(showing.feed.entries))
	}
	if showing.feed.entries[1].channelID != "c-muted" {
		t.Errorf("muted bubble landed at index 0; want it last (entries: %q, %q)",
			showing.feed.entries[0].channelID, showing.feed.entries[1].channelID)
	}
	if showing.feed.idx != 0 {
		t.Errorf("selection moved to %d; want it to stay on 0", showing.feed.idx)
	}
}

func TestFeedMutedCommand(t *testing.T) {
	// Off the Feed tab (teamIdx 1 = Search) the toggle doesn't apply.
	off := mutedFeedModel(false)
	off.teamIdx = 1
	if _, ok := off.feedMutedCommand(); ok {
		t.Error("feedMutedCommand applies off the Feed tab; want not applicable")
	}

	on := mutedFeedModel(false)
	cmd, ok := on.feedMutedCommand()
	if !ok || cmd.name != "Feed: show muted channels" {
		t.Fatalf("feedMutedCommand = %q, ok=%v; want the show command", cmd.name, ok)
	}
	cmd.run(&on, "")
	if !on.feed.showMuted {
		t.Fatal("running the command did not turn show-muted on")
	}
	if cmd, _ := on.feedMutedCommand(); cmd.name != "Feed: hide muted channels" {
		t.Errorf("feedMutedCommand = %q; want the hide command once muted are shown", cmd.name)
	}
}

func TestMuteCommand(t *testing.T) {
	member := func(muted bool) model.ChannelMemberWithTeamData {
		level := model.ChannelMarkUnreadAll
		if muted {
			level = model.ChannelMarkUnreadMention
		}
		return model.ChannelMemberWithTeamData{ChannelMember: model.ChannelMember{
			ChannelId:   "c1",
			NotifyProps: model.StringMap{model.MarkUnreadNotifyProp: level},
		}}
	}
	newModel := func(muted bool) Model {
		return Model{
			openChannelID: "c1",
			me:            &model.User{Id: "u1"},
			channels: map[string][]*model.Channel{
				"t1": {{Id: "c1", DisplayName: "general", Type: model.ChannelTypeOpen}},
			},
			members: model.ChannelMembersWithTeamData{member(muted)},
		}
	}

	// No open channel → no mute command, and allCommands == the static set.
	// teamIdx 1 parks the model on the Search tab: the zero value sits on the
	// Feed tab, where the feed's show-muted toggle legitimately applies (see
	// TestFeedMutedCommand) and allCommands would not be the static set.
	none := Model{teamIdx: 1}
	if _, ok := none.muteCommand(); ok {
		t.Error("muteCommand applies with no open channel; want not applicable")
	}
	if len(none.allCommands()) != len(builtinCommands()) {
		t.Error("allCommands added a command when none should apply")
	}

	// Unmuted channel → a "Mute …" command, listed second (after Summarize),
	// ahead of the other channel-contextual commands (add members, the edit
	// forms, archive/leave/privacy).
	m := newModel(false)
	cmd, ok := m.muteCommand()
	if !ok || cmd.name != "Mute #general" {
		t.Fatalf("muteCommand = %q, ok=%v; want \"Mute #general\", true", cmd.name, ok)
	}
	all := m.allCommands()
	if len(all) <= len(builtinCommands()) {
		t.Fatalf("allCommands = %d entries; want more than the %d builtins", len(all), len(builtinCommands()))
	}
	if all[1].name != "Mute #general" {
		t.Fatalf("allCommands[1] = %q; want the mute command second", all[1].name)
	}

	// Running it flips the cached member to muted, so the label inverts.
	cmd.run(&m, "")
	if !m.channelMuted("c1") {
		t.Error("after running Mute, channelMuted(c1) = false; want true")
	}
	cmd2, _ := m.muteCommand()
	if cmd2.name != "Unmute #general" {
		t.Errorf("after muting, muteCommand = %q; want \"Unmute #general\"", cmd2.name)
	}
	cmd2.run(&m, "")
	if m.channelMuted("c1") {
		t.Error("after running Unmute, channelMuted(c1) = true; want false")
	}
}
