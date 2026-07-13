package ui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// joinChanTestModel is a Model on two teams, already in one channel of the
// first — the starting point for browsing what else there is to join.
func joinChanTestModel() Model {
	return Model{
		keys:   newKeyMap("ctrl"),
		width:  100,
		height: 44,
		teams: []*model.Team{
			{Id: "t1", Name: "eng", DisplayName: "Engineering"},
			{Id: "t2", Name: "ops", DisplayName: "Operations"},
		},
		channels: map[string][]*model.Channel{
			"t1": {{Id: "c_joined", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "general", DisplayName: "General"}},
		},
		me:       &model.User{Id: "me", Username: "me"},
		unread:   map[string]int{},
		mentions: map[string]int{},
		drafts:   map[string]string{},
	}
}

// pressJoin sends one named key to the join-channel modal.
func pressJoin(t *testing.T, m *Model, name string) {
	t.Helper()
	out, _ := m.handleJoinChannelKey(keyMsg(t, name))
	*m = out.(Model)
}

// typeIntoJoin feeds a string to the modal's filter box, key by key.
func typeIntoJoin(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		pressJoin(t, m, string(r))
	}
}

// catalogue is the public-channel list a team's fetch would return.
func catalogue(teamID string, names ...string) []*model.Channel {
	out := make([]*model.Channel, 0, len(names))
	for _, n := range names {
		out = append(out, &model.Channel{
			Id: "c_" + n, TeamId: teamID, Type: model.ChannelTypeOpen,
			Name: n, DisplayName: strings.ToUpper(n[:1]) + n[1:],
		})
	}
	return out
}

// TestJoinChannelInPalette: the palette offers the command, and it opens the
// browse list rather than a captive arg prompt.
func TestJoinChannelInPalette(t *testing.T) {
	m := joinChanTestModel()
	var found *switcherCommand
	all := m.allCommands()
	for i := range all {
		if all[i].name == "Join a channel" {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatal("allCommands() has no \"Join a channel\" entry")
	}
	if found.argPrompt != "" {
		t.Errorf("argPrompt = %q, want none — the command opens its own list", found.argPrompt)
	}
	found.run(&m, "")
	if m.joinChan == nil {
		t.Error("running the palette command didn't open the browse list")
	}
}

// TestOpenJoinChannelDefaults: the list opens on the focused team and starts a
// fetch for it.
func TestOpenJoinChannelDefaults(t *testing.T) {
	m := joinChanTestModel()
	m.teamIdx = m.teamIdxForTest("t2")
	cmd := m.openJoinChannel()

	st := m.joinChan
	if st == nil {
		t.Fatal("openJoinChannel left the modal closed")
	}
	if got := st.joinTeamID(); got != "t2" {
		t.Errorf("browsing team %q, want the focused tab's team t2", got)
	}
	if !st.loading {
		t.Error("the modal opened without a fetch in flight")
	}
	if cmd == nil {
		t.Error("no Cmd; want the catalogue fetched")
	}
}

// TestOpenJoinChannelNoTeams: with no team to browse, the command reports rather
// than opening an empty list.
func TestOpenJoinChannelNoTeams(t *testing.T) {
	m := joinChanTestModel()
	m.teams = nil
	if cmd := m.openJoinChannel(); cmd != nil {
		t.Error("openJoinChannel returned a Cmd with no teams; want none")
	}
	if m.joinChan != nil {
		t.Error("the browse list opened with no team to browse")
	}
	if !strings.Contains(m.status, "any team") {
		t.Errorf("status = %q, want the no-team hint", m.status)
	}
}

// TestApplyPublicChannelsSkipsJoined: the catalogue lists what you can join —
// the channels you're already in are the switcher's job, not this list's.
func TestApplyPublicChannelsSkipsJoined(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()

	fetched := catalogue("t1", "general", "random", "design") // "general" is already joined…
	fetched[0].Id = "c_joined"                                // …under the id the sidebar knows
	out, _ := m.applyPublicChannels(publicChannelsMsg{gen: m.joinChan.gen, teamID: "t1", channels: fetched})
	m = out.(Model)

	st := m.joinChan
	if st.loading {
		t.Error("still loading after the catalogue landed")
	}
	var names []string
	for _, c := range st.joinResults() {
		names = append(names, c.Name)
	}
	if want := "random,design"; strings.Join(names, ",") != want {
		t.Errorf("joinable = %v, want only the channels not already joined (%q)", names, want)
	}
}

// TestApplyPublicChannelsStale: a catalogue for a team the user has already
// tabbed away from is dropped, not shown under the wrong team.
func TestApplyPublicChannelsStale(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	stale := m.joinChan.gen - 1

	out, _ := m.applyPublicChannels(publicChannelsMsg{gen: stale, teamID: "t1", channels: catalogue("t1", "random")})
	m = out.(Model)

	if _, cached := m.joinChan.cache["t1"]; cached {
		t.Error("a stale catalogue was cached")
	}
	if !m.joinChan.loading {
		t.Error("a stale response cleared the in-flight fetch")
	}
}

// TestApplyPublicChannelsError: a failed fetch says so instead of showing an
// empty catalogue as if the team had nothing to join.
func TestApplyPublicChannelsError(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	out, _ := m.applyPublicChannels(publicChannelsMsg{
		gen: m.joinChan.gen, teamID: "t1", err: errors.New("500: server exploded"),
	})
	m = out.(Model)

	if m.joinChan.loading {
		t.Error("still loading after the fetch failed")
	}
	if !strings.Contains(m.joinChan.errMsg, "exploded") {
		t.Errorf("errMsg = %q, want the server's reason", m.joinChan.errMsg)
	}
	if got := m.renderJoinChannel(); !strings.Contains(got, "couldn't load") {
		t.Errorf("the list doesn't report the failed fetch\n---\n%s", got)
	}
}

// TestJoinResultsFilter: typing narrows the list on both the URL slug and the
// display name, and resets the selection.
func TestJoinResultsFilter(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	out, _ := m.applyPublicChannels(publicChannelsMsg{
		gen: m.joinChan.gen, teamID: "t1", channels: catalogue("t1", "random", "design", "design-review"),
	})
	m = out.(Model)
	m.joinChan.idx = 2

	typeIntoJoin(t, &m, "design")
	if got := len(m.joinChan.joinResults()); got != 2 {
		t.Errorf("filtered list = %d entries, want the 2 design channels", got)
	}
	if m.joinChan.idx != 0 {
		t.Errorf("selection = %d after the list changed under it, want it reset to 0", m.joinChan.idx)
	}

	// The filter matches the display name too ("Random"), not just the slug.
	m.joinChan.filter.SetValue("Rand")
	if got := len(m.joinChan.joinResults()); got != 1 {
		t.Errorf("display-name filter matched %d entries, want 1", got)
	}
}

// TestJoinChannelSelection: ↑/↓ move the cursor and stop at the ends.
func TestJoinChannelSelection(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	out, _ := m.applyPublicChannels(publicChannelsMsg{
		gen: m.joinChan.gen, teamID: "t1", channels: catalogue("t1", "random", "design"),
	})
	m = out.(Model)

	pressJoin(t, &m, "up") // already at the top
	if m.joinChan.idx != 0 {
		t.Errorf("idx = %d after ↑ at the top, want it clamped to 0", m.joinChan.idx)
	}
	pressJoin(t, &m, "down")
	pressJoin(t, &m, "down") // past the end
	if m.joinChan.idx != 1 {
		t.Errorf("idx = %d after ↓ past the end, want it clamped to the last row", m.joinChan.idx)
	}
}

// TestJoinChannelTeamCycle: tab moves to the next team and fetches its
// catalogue, but a team already browsed is served from the cache.
func TestJoinChannelTeamCycle(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	out, _ := m.applyPublicChannels(publicChannelsMsg{
		gen: m.joinChan.gen, teamID: "t1", channels: catalogue("t1", "random"),
	})
	m = out.(Model)

	out, cmd := m.handleJoinChannelKey(keyMsg(t, "tab"))
	m = out.(Model)
	if got := m.joinChan.joinTeamID(); got != "t2" {
		t.Errorf("team after tab = %q, want t2", got)
	}
	if cmd == nil {
		t.Error("tabbing to an unbrowsed team didn't fetch its catalogue")
	}
	if !m.joinChan.loading {
		t.Error("tabbing to an unbrowsed team left no fetch in flight")
	}

	// Back to t1, which is cached: no refetch, and the list is there.
	out, cmd = m.handleJoinChannelKey(keyMsg(t, "shift+tab"))
	m = out.(Model)
	if cmd != nil {
		t.Error("tabbing back to a browsed team refetched it; want the cache used")
	}
	if m.joinChan.loading {
		t.Error("the cached team still reads as loading")
	}
	if len(m.joinChan.joinResults()) != 1 {
		t.Error("the cached catalogue didn't come back")
	}
}

// TestSubmitJoinChannel: enter joins the selected channel.
func TestSubmitJoinChannel(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	out, _ := m.applyPublicChannels(publicChannelsMsg{
		gen: m.joinChan.gen, teamID: "t1", channels: catalogue("t1", "random", "design"),
	})
	m = out.(Model)
	m.joinChan.idx = 1

	out, cmd := m.submitJoinChannel()
	m = out.(Model)
	if cmd == nil {
		t.Fatal("enter fired no join request")
	}
	if !m.joinChan.joining {
		t.Error("the modal doesn't read as joining while the request is out")
	}
}

// TestSubmitJoinChannelEmpty: enter on an empty list is a no-op, not a panic.
func TestSubmitJoinChannelEmpty(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	out, _ := m.applyPublicChannels(publicChannelsMsg{gen: m.joinChan.gen, teamID: "t1"})
	m = out.(Model)

	out, cmd := m.submitJoinChannel()
	m = out.(Model)
	if cmd != nil {
		t.Error("enter on an empty list fired a request")
	}
	if m.joinChan == nil {
		t.Error("enter on an empty list closed the modal")
	}
}

// TestApplyChannelJoined: the joined channel is spliced into the sidebar in
// sorted position and opened.
func TestApplyChannelJoined(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	joined := &model.Channel{Id: "c_design", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "design", DisplayName: "Design"}

	out, cmd := m.applyChannelJoined(channelJoinedMsg{ch: joined})
	m = out.(Model)

	if m.joinChan != nil {
		t.Error("the browse list stayed open after a successful join")
	}
	if cmd == nil {
		t.Error("no Cmd; want the joined channel loaded")
	}
	var names []string
	for _, c := range m.channels["t1"] {
		names = append(names, c.Name)
	}
	if want := "design,general"; strings.Join(names, ",") != want {
		t.Errorf("sidebar bucket = %v, want the joined channel in sorted position (%q)", names, want)
	}
	if m.openChannelID != "c_design" {
		t.Errorf("open channel = %q, want the freshly-joined channel", m.openChannelID)
	}
	if m.focus != focusInput {
		t.Errorf("focus = %v, want the composer", m.focus)
	}
	if !strings.Contains(m.status, "Design") {
		t.Errorf("status = %q, want it to name the joined channel", m.status)
	}
}

// TestApplyChannelJoinedError: a refused join keeps the list open with the
// server's reason, so another channel can be picked.
func TestApplyChannelJoinedError(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	m.joinChan.joining = true

	out, cmd := m.applyChannelJoined(channelJoinedMsg{
		ch:  &model.Channel{Id: "c_design", TeamId: "t1", Name: "design"},
		err: errors.New("channel is archived\nand cannot be joined"),
	})
	m = out.(Model)

	if m.joinChan == nil {
		t.Fatal("the list closed on a failed join; want it kept open")
	}
	if m.joinChan.joining {
		t.Error("still joining after the error came back")
	}
	if !strings.Contains(m.joinChan.errMsg, "archived") {
		t.Errorf("errMsg = %q, want the server's reason", m.joinChan.errMsg)
	}
	if strings.Contains(m.joinChan.errMsg, "\n") {
		t.Errorf("errMsg = %q, want it folded onto one line", m.joinChan.errMsg)
	}
	if cmd != nil {
		t.Error("a failed join returned a Cmd; want none")
	}
	if m.findChannel("c_design") != nil {
		t.Error("the channel joined the sidebar despite the server refusing")
	}
}

// TestJoinChannelEscCloses: esc tears the list down without joining.
func TestJoinChannelEscCloses(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	pressJoin(t, &m, "esc")
	if m.joinChan != nil {
		t.Error("esc left the browse list open")
	}
}

// TestJoinChannelIsModal: the list is a body overlay, so the panes beneath it
// don't act on keys.
func TestJoinChannelIsModal(t *testing.T) {
	m := joinChanTestModel()
	m.focus = focusInput
	if m.inModal() {
		t.Fatal("inModal() is true with no modal open")
	}
	m.openJoinChannel()
	if !m.inModal() {
		t.Error("inModal() = false with the browse list open")
	}
	if !m.bodyOverlayActive() {
		t.Error("bodyOverlayActive() = false; the composer would keep the terminal cursor")
	}
}

// TestRenderJoinChannel: the list shows the team, the channels and their
// purposes, and says so when there's nothing left to join.
func TestRenderJoinChannel(t *testing.T) {
	m := joinChanTestModel()
	if got := m.renderJoinChannel(); got != "" {
		t.Errorf("renderJoinChannel() with the modal closed = %q, want empty", got)
	}
	m.openJoinChannel()
	if got := m.renderJoinChannel(); !strings.Contains(got, "loading channels…") {
		t.Errorf("the list doesn't show the fetch in flight\n---\n%s", got)
	}

	chans := catalogue("t1", "random", "design")
	chans[0].Purpose = "watercooler chatter"
	out, _ := m.applyPublicChannels(publicChannelsMsg{gen: m.joinChan.gen, teamID: "t1", channels: chans})
	m = out.(Model)

	got := m.renderJoinChannel()
	for _, want := range []string{"Join a channel", "Engineering", "#Random", "#Design", "watercooler", "↵ join"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered list is missing %q\n---\n%s", want, got)
		}
	}

	// Nothing left to join reads as such, rather than as an empty box.
	m.joinChan.cache["t1"] = nil
	if got := m.renderJoinChannel(); !strings.Contains(got, "already in every public channel") {
		t.Errorf("an exhausted catalogue doesn't say so\n---\n%s", got)
	}
}

// TestRenderJoinChannelWindows: a catalogue longer than the list height scrolls
// a window around the selection rather than growing the modal off-screen.
func TestRenderJoinChannelWindows(t *testing.T) {
	m := joinChanTestModel()
	m.openJoinChannel()
	names := make([]string, 0, 30)
	for i := range 30 {
		names = append(names, "chan"+string(rune('a'+i%26))+string(rune('0'+i/26)))
	}
	out, _ := m.applyPublicChannels(publicChannelsMsg{
		gen: m.joinChan.gen, teamID: "t1", channels: catalogue("t1", names...),
	})
	m = out.(Model)

	rows := m.renderJoinList(60)
	if len(rows) > joinListHeight+2 { // + the two scroll markers
		t.Errorf("list rendered %d rows, want at most %d", len(rows), joinListHeight+2)
	}
	if !strings.Contains(strings.Join(rows, "\n"), "more") {
		t.Error("a truncated list doesn't say how much more there is")
	}

	// Scrolling to the end keeps the selection on screen. Rows are labelled with
	// the display name, the way the sidebar labels them.
	m.joinChan.idx = 29
	rows = m.renderJoinList(60)
	last := displayChannel(m.joinChan.joinResults()[29])
	if !strings.Contains(strings.Join(rows, "\n"), last) {
		t.Errorf("the selection (%q) scrolled out of the rendered window", last)
	}
}

// TestRenderJoinChannelFitsTerminal: no long channel name, purpose or error may
// push the modal past the terminal's width.
func TestRenderJoinChannelFitsTerminal(t *testing.T) {
	for _, w := range []int{44, 60, 80, 120} {
		m := joinChanTestModel()
		m.width = w
		m.teams = []*model.Team{{Id: "t1", Name: "eng", DisplayName: strings.Repeat("Long Team ", 8)}}
		m.openJoinChannel()

		long := &model.Channel{
			Id: "c_long", TeamId: "t1", Type: model.ChannelTypeOpen,
			Name:        strings.Repeat("long-name-", 8),
			DisplayName: strings.Repeat("Long Display ", 6),
			Purpose:     strings.Repeat("a very long purpose ", 8),
		}
		out, _ := m.applyPublicChannels(publicChannelsMsg{
			gen: m.joinChan.gen, teamID: "t1", channels: []*model.Channel{long},
		})
		m = out.(Model)
		m.joinChan.errMsg = strings.Repeat("very long server error ", 6)

		rendered := m.renderJoinChannel()
		outer, _, _ := joinDims(w)
		for i, line := range strings.Split(rendered, "\n") {
			if got := lipgloss.Width(line); got > outer {
				t.Errorf("width=%d: line %d is %d cols wide, want <= %d\n%s", w, i, got, outer, line)
				break
			}
		}
		if outer > w {
			t.Errorf("width=%d: modal outer width %d overflows the terminal", w, outer)
		}
	}
}
