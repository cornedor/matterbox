package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// chanActionTestModel is a Model with one team holding three channels, the
// middle one open.
func chanActionTestModel() Model {
	return Model{
		keys:   newKeyMap("ctrl"),
		width:  100,
		height: 44,
		teams:  []*model.Team{{Id: "t1", Name: "eng", DisplayName: "Engineering"}},
		channels: map[string][]*model.Channel{
			"t1": {
				{Id: "c1", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "alpha", DisplayName: "Alpha"},
				{Id: "c2", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "bravo", DisplayName: "Bravo"},
				{Id: "c3", TeamId: "t1", Type: model.ChannelTypeOpen, Name: "charlie", DisplayName: "Charlie"},
			},
		},
		me:            &model.User{Id: "me", Username: "me"},
		openChannelID: "c2",
		channelIdx:    1,
		unread:        map[string]int{"c2": 3},
		mentions:      map[string]int{"c2": 1},
		drafts:        map[string]string{},
	}
}

// pressConfirm sends one named key to the channel-confirm modal.
func pressConfirm(t *testing.T, m *Model, name string) {
	t.Helper()
	out, _ := m.handleChannelConfirmKey(keyMsg(t, name))
	*m = out.(Model)
}

// TestChannelActionCommandsInPalette: an open public channel offers all three
// actions, and the privacy entry is phrased for the direction it would go.
func TestChannelActionCommandsInPalette(t *testing.T) {
	m := chanActionTestModel()
	cmds, ok := m.channelActionCommands()
	if !ok || len(cmds) != 3 {
		t.Fatalf("channelActionCommands() = %d entries, ok=%v; want 3, true", len(cmds), ok)
	}
	var names []string
	for _, c := range cmds {
		names = append(names, c.name)
	}
	want := []string{"Make #Bravo private", "Leave #Bravo", "Archive #Bravo"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("palette entries = %v, want %v", names, want)
	}

	// A private channel offers the conversion the other way.
	m.channels["t1"][1].Type = model.ChannelTypePrivate
	cmds, _ = m.channelActionCommands()
	if !strings.HasPrefix(cmds[0].name, "Make 🔒Bravo public") {
		t.Errorf("privacy entry = %q, want it offering to make the private channel public", cmds[0].name)
	}
}

// TestChannelActionCommandsGated: DMs have no membership or privacy to manage,
// and the server refuses all three for a team's default channel — so neither
// is offered them.
func TestChannelActionCommandsGated(t *testing.T) {
	none := Model{}
	if _, ok := none.channelActionCommands(); ok {
		t.Error("channel actions offered with no open channel")
	}

	dm := chanActionTestModel()
	dm.channels["t1"][1].Type = model.ChannelTypeDirect
	if _, ok := dm.channelActionCommands(); ok {
		t.Error("channel actions offered for a DM")
	}

	def := chanActionTestModel()
	def.channels["t1"][1].Name = model.DefaultChannelName
	if _, ok := def.channelActionCommands(); ok {
		t.Error("channel actions offered for the team's default channel")
	}
}

// TestOpenChannelConfirmStatesTheConsequence: each confirm names what the user
// is about to lose, and the privacy confirm carries the type it converts to.
func TestOpenChannelConfirmStatesTheConsequence(t *testing.T) {
	m := chanActionTestModel()

	m.openChannelConfirm(chanConfirmArchive, "c2")
	if m.chanConfirm == nil || !strings.Contains(m.chanConfirm.title, "Archive #Bravo") {
		t.Fatalf("archive confirm title = %q", m.chanConfirm.title)
	}
	if !strings.Contains(m.chanConfirm.note, "system admin") {
		t.Errorf("archive note = %q, want it to say the archive isn't self-service to undo", m.chanConfirm.note)
	}

	m.openChannelConfirm(chanConfirmPrivacy, "c2")
	if m.chanConfirm.toType != model.ChannelTypePrivate {
		t.Errorf("toType = %q converting a public channel, want private", m.chanConfirm.toType)
	}

	// Leaving a private channel warns that getting back in needs an invite.
	m.channels["t1"][1].Type = model.ChannelTypePrivate
	m.openChannelConfirm(chanConfirmLeave, "c2")
	if !strings.Contains(m.chanConfirm.note, "invite") {
		t.Errorf("private leave note = %q, want the invite warning", m.chanConfirm.note)
	}
	m.openChannelConfirm(chanConfirmPrivacy, "c2")
	if m.chanConfirm.toType != model.ChannelTypeOpen {
		t.Errorf("toType = %q converting a private channel, want public", m.chanConfirm.toType)
	}
}

// TestChannelConfirmKeys: n and esc cancel without acting; y fires.
func TestChannelConfirmKeys(t *testing.T) {
	m := chanActionTestModel()
	m.openChannelConfirm(chanConfirmArchive, "c2")
	pressConfirm(t, &m, "n")
	if m.chanConfirm != nil {
		t.Error("n left the confirm open")
	}
	if m.findChannel("c2") == nil {
		t.Error("n archived the channel anyway")
	}

	m.openChannelConfirm(chanConfirmArchive, "c2")
	pressConfirm(t, &m, "esc")
	if m.chanConfirm != nil {
		t.Error("esc left the confirm open")
	}

	m.openChannelConfirm(chanConfirmArchive, "c2")
	out, cmd := m.handleChannelConfirmKey(keyMsg(t, "y"))
	m = out.(Model)
	if cmd == nil {
		t.Error("y fired no request")
	}
	if m.chanConfirm == nil || !m.chanConfirm.running {
		t.Error("the confirm should stay up, marked running, until the server answers")
	}
	if m.findChannel("c2") == nil {
		t.Error("the channel left the sidebar before the server confirmed")
	}
}

// TestApplyChannelActionArchive: a confirmed archive drops the channel from the
// sidebar and moves the pane to its neighbour.
func TestApplyChannelActionArchive(t *testing.T) {
	m := chanActionTestModel()
	m.openChannelConfirm(chanConfirmArchive, "c2")

	out, cmd := m.applyChannelActionDone(channelActionDoneMsg{
		kind: chanConfirmArchive, channelID: "c2", label: "#Bravo",
	})
	m = out.(Model)

	if m.chanConfirm != nil {
		t.Error("the confirm stayed open after the archive landed")
	}
	if m.findChannel("c2") != nil {
		t.Error("the archived channel is still in the sidebar")
	}
	if m.openChannelID != "c3" {
		t.Errorf("open channel = %q, want the neighbour c3", m.openChannelID)
	}
	if cmd == nil {
		t.Error("no Cmd; want the neighbour's messages loaded")
	}
	if m.unread["c2"] != 0 || m.mentions["c2"] != 0 {
		t.Error("the archived channel kept its unread/mention badges")
	}
	if !strings.Contains(m.status, "archived") {
		t.Errorf("status = %q, want it to report the archive", m.status)
	}
}

// TestApplyChannelActionLeave: leaving drops the channel the same way.
func TestApplyChannelActionLeave(t *testing.T) {
	m := chanActionTestModel()
	out, _ := m.applyChannelActionDone(channelActionDoneMsg{
		kind: chanConfirmLeave, channelID: "c2", label: "#Bravo",
	})
	m = out.(Model)

	if m.findChannel("c2") != nil {
		t.Error("the channel we left is still in the sidebar")
	}
	if !strings.Contains(m.status, "left") {
		t.Errorf("status = %q, want it to report the leave", m.status)
	}
}

// TestApplyChannelActionPrivacy: a conversion flips the type in place — the
// channel stays open and in the sidebar.
func TestApplyChannelActionPrivacy(t *testing.T) {
	m := chanActionTestModel()
	out, cmd := m.applyChannelActionDone(channelActionDoneMsg{
		kind: chanConfirmPrivacy, channelID: "c2", label: "#Bravo", typ: model.ChannelTypePrivate,
	})
	m = out.(Model)

	c := m.findChannel("c2")
	if c == nil {
		t.Fatal("the converted channel left the sidebar")
	}
	if c.Type != model.ChannelTypePrivate {
		t.Errorf("type = %q, want private", c.Type)
	}
	if m.openChannelID != "c2" {
		t.Errorf("open channel = %q, want it still open", m.openChannelID)
	}
	if cmd != nil {
		t.Error("a conversion returned a Cmd; want none — nothing needs reloading")
	}
	if !strings.Contains(m.status, "private") {
		t.Errorf("status = %q, want it to report the new privacy", m.status)
	}
}

// TestApplyChannelActionError: a refused action changes nothing locally and says
// why, on one line.
func TestApplyChannelActionError(t *testing.T) {
	m := chanActionTestModel()
	m.openChannelConfirm(chanConfirmArchive, "c2")

	out, _ := m.applyChannelActionDone(channelActionDoneMsg{
		kind: chanConfirmArchive, channelID: "c2", label: "#Bravo",
		err: errors.New("you do not have permission\nto archive this channel"),
	})
	m = out.(Model)

	if m.chanConfirm != nil {
		t.Error("the confirm stayed open after a failure; want it dismissed with the reason")
	}
	if m.findChannel("c2") == nil {
		t.Error("the channel was dropped despite the server refusing")
	}
	if m.openChannelID != "c2" {
		t.Errorf("open channel = %q, want it untouched", m.openChannelID)
	}
	if !strings.Contains(m.status, "permission") {
		t.Errorf("status = %q, want the server's reason", m.status)
	}
	if strings.Contains(m.status, "\n") {
		t.Errorf("status = %q, want it folded onto one line", m.status)
	}
}

// TestDropChannelLastInTeam: dropping the only channel in a team empties the
// message pane instead of leaving it showing a channel that's gone.
func TestDropChannelLastInTeam(t *testing.T) {
	m := chanActionTestModel()
	m.channels["t1"] = m.channels["t1"][1:2] // just the open one
	m.channelIdx = 0
	m.posts = []*model.Post{{Id: "p1", ChannelId: "c2", Message: "hi"}}

	cmd := m.dropChannel("c2")
	if cmd != nil {
		t.Error("a Cmd was returned with no channel left to open")
	}
	if m.openChannelID != "" {
		t.Errorf("open channel = %q, want none", m.openChannelID)
	}
	if len(m.posts) != 0 {
		t.Error("the message pane still holds the dropped channel's posts")
	}
	if len(m.channels["t1"]) != 0 {
		t.Error("the team bucket still holds the dropped channel")
	}
}

// TestDropChannelNotOpen: dropping a channel that isn't the open one leaves the
// conversation alone, and the sidebar cursor stays on the row it was on even
// though every row beneath the dropped one shifted up.
func TestDropChannelNotOpen(t *testing.T) {
	m := chanActionTestModel() // cursor on c2, the middle row
	if cmd := m.dropChannel("c1"); cmd != nil {
		t.Error("dropping a background channel reloaded the pane; want no Cmd")
	}
	if m.openChannelID != "c2" {
		t.Errorf("open channel = %q, want the untouched c2", m.openChannelID)
	}
	if m.findChannel("c1") != nil {
		t.Error("the dropped channel is still in the sidebar")
	}
	if got := m.channels["t1"][m.channelIdx].Id; got != "c2" {
		t.Errorf("cursor on %q after a row above it went away, want it still on c2", got)
	}
}

// TestDropChannelSharesNoBackingArray: the sidebar's slice is handed to the
// render paths, so removing a row must not shift the shared backing array.
func TestDropChannelSharesNoBackingArray(t *testing.T) {
	m := chanActionTestModel()
	snapshot := m.channels["t1"]
	m.dropChannel("c2")

	var names []string
	for _, c := range snapshot {
		names = append(names, c.Name)
	}
	if want := "alpha,bravo,charlie"; strings.Join(names, ",") != want {
		t.Errorf("a slice taken before the drop now reads %v; want the untouched %q", names, want)
	}
}

// TestChannelConfirmIsModal: the confirm is a body overlay, so the panes beneath
// it don't act on keys and the composer doesn't keep the terminal cursor.
func TestChannelConfirmIsModal(t *testing.T) {
	m := chanActionTestModel()
	m.focus = focusInput
	if m.inModal() {
		t.Fatal("inModal() is true with no modal open")
	}
	m.openChannelConfirm(chanConfirmLeave, "c2")
	if !m.inModal() {
		t.Error("inModal() = false with the confirm open")
	}
	if !m.bodyOverlayActive() {
		t.Error("bodyOverlayActive() = false; the composer would keep the terminal cursor")
	}
}

// TestRenderChannelConfirm: the dialog asks the question, states the
// consequence, and offers the keys.
func TestRenderChannelConfirm(t *testing.T) {
	m := chanActionTestModel()
	if got := m.renderChannelConfirm(); got != "" {
		t.Errorf("renderChannelConfirm() with the modal closed = %q, want empty", got)
	}
	m.openChannelConfirm(chanConfirmArchive, "c2")
	out := m.renderChannelConfirm()
	for _, want := range []string{"Archive", "Bravo", "y confirm", "n cancel"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered confirm is missing %q\n---\n%s", want, out)
		}
	}

	m.chanConfirm.running = true
	if got := m.renderChannelConfirm(); !strings.Contains(got, "archiving…") {
		t.Errorf("in-flight confirm doesn't say so\n---\n%s", got)
	}
}
