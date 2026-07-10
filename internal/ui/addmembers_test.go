package ui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	"github.com/mattermost/mattermost/server/public/model"
)

// TestCanAddMembers: only real channels take new members; a DM / group DM has
// fixed membership.
func TestCanAddMembers(t *testing.T) {
	cases := []struct {
		typ  model.ChannelType
		want bool
	}{
		{model.ChannelTypeOpen, true},
		{model.ChannelTypePrivate, true},
		{model.ChannelTypeDirect, false},
		{model.ChannelTypeGroup, false},
	}
	for _, c := range cases {
		if got := canAddMembers(&model.Channel{Type: c.typ}); got != c.want {
			t.Errorf("canAddMembers(%q) = %v, want %v", c.typ, got, c.want)
		}
	}
	if canAddMembers(nil) {
		t.Error("canAddMembers(nil) = true, want false")
	}
}

// TestAddMembersCommandContext: the palette entry exists for the open channel
// and names it, and is absent on a DM (and with no channel open at all).
func TestAddMembersCommandContext(t *testing.T) {
	m := infoTestModel()
	cmd, ok := m.addMembersCommand()
	if !ok || !strings.HasPrefix(cmd.name, "Add members to ") {
		t.Fatalf("addMembersCommand = %q, ok=%v; want an \"Add members to …\" command", cmd.name, ok)
	}
	if cmd.argPrompt == "" {
		t.Error("addMembersCommand has no argPrompt; the palette must ask for the user list")
	}

	dm := infoTestModel()
	dm.channels["t1"][0].Type = model.ChannelTypeDirect
	if _, ok := dm.addMembersCommand(); ok {
		t.Error("addMembersCommand applies to a DM; want not applicable")
	}
	if _, ok := (Model{}).addMembersCommand(); ok {
		t.Error("addMembersCommand applies with no open channel; want not applicable")
	}
}

// TestAddMembersCommandInPalette: the contextual command is listed alongside
// the mute toggle, just under Summarize.
func TestAddMembersCommandInPalette(t *testing.T) {
	m := infoTestModel()
	var found bool
	for _, c := range m.allCommands() {
		if strings.HasPrefix(c.name, "Add members to ") {
			found = true
		}
	}
	if !found {
		t.Error("allCommands() has no \"Add members to …\" entry for an open channel")
	}
}

// TestRunAddChannelMembersRejectsEmpty: an empty user list never touches the
// network.
func TestRunAddChannelMembersRejectsEmpty(t *testing.T) {
	m := infoTestModel()
	if cmd := runAddChannelMembers("chan123")(&m, "   "); cmd != nil {
		t.Error("empty spec returned a Cmd; want none")
	}
	if !strings.Contains(m.status, "name at least one user") {
		t.Errorf("status = %q, want the empty-spec hint", m.status)
	}
}

// TestInfoAddMemberRow: the "+ Add members…" row closes the member list on a
// channel that accepts members, and is absent on a DM.
func TestInfoAddMemberRow(t *testing.T) {
	m := infoTestModel()
	m.infoMembers = []*model.User{{Id: "u_alice", Username: "alice"}}
	m.infoMembersLoaded = true
	m.infoPinned = []*model.Post{{Id: "pin1", ChannelId: "chan123", UserId: "u_alice", Message: "pinned"}}
	m.infoPinnedLoaded = true
	m.renderInfo()

	if !strings.Contains(m.infoView.GetContent(), "Add members") {
		t.Errorf("info panel has no add-members row\n---\n%s", m.infoView.GetContent())
	}
	// It belongs inside the Members section: after the last member, before the
	// first pin.
	var kinds []infoTargetKind
	for _, tgt := range m.infoTargets {
		kinds = append(kinds, tgt.kind)
	}
	want := []infoTargetKind{infoTargetLink, infoTargetMember, infoTargetAddMember, infoTargetPin, infoTargetMedia}
	if len(kinds) != len(want) {
		t.Fatalf("target kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("target kinds = %v, want %v", kinds, want)
		}
	}

	dm := infoTestModel()
	dm.channels["t1"][0].Type = model.ChannelTypeDirect
	dm.infoMembersLoaded = true
	dm.infoPinnedLoaded = true
	dm.renderInfo()
	for _, tgt := range dm.infoTargets {
		if tgt.kind == infoTargetAddMember {
			t.Error("DM info panel offers an add-members row; want none")
		}
	}
}

// TestActivateAddMemberRowOpensPrompt: selecting the row raises the switcher's
// captive arg prompt for the add-members command, leaving the panel open behind
// it so the refreshed member list lands somewhere visible.
func TestActivateAddMemberRowOpensPrompt(t *testing.T) {
	m := infoTestModel()
	ti := textinput.New()
	m.switcher = &ti
	m.infoMembersLoaded = true
	m.infoPinnedLoaded = true
	m.renderInfo()

	idx := -1
	for i, tgt := range m.infoTargets {
		if tgt.kind == infoTargetAddMember {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no add-members target to activate")
	}
	m.infoIdx = idx

	out, _ := m.activateInfoTarget()
	m = out.(Model)
	if !m.switcherMode || !m.inCommandArgMode() {
		t.Fatalf("switcherMode=%v argMode=%v; want the captive arg prompt open", m.switcherMode, m.inCommandArgMode())
	}
	if m.switcherCmdPending.argPrompt != "users: " {
		t.Errorf("arg prompt = %q, want %q", m.switcherCmdPending.argPrompt, "users: ")
	}
	if !m.infoOpen {
		t.Error("the info panel closed; want it to stay open behind the prompt")
	}
}

// TestApplyMembersAdded reports who joined and refreshes the panel's member
// list only when it's showing the channel that grew.
func TestApplyMembersAdded(t *testing.T) {
	m := infoTestModel()
	out, cmd := m.applyMembersAdded(channelMembersAddedMsg{channelID: "chan123", added: []string{"alice", "bob"}})
	m = out.(Model)
	if !strings.Contains(m.status, "@alice, @bob") {
		t.Errorf("status = %q, want it to name both users", m.status)
	}
	if cmd == nil {
		t.Error("no Cmd; want the open info panel's member list refetched")
	}

	// A result for a channel the panel isn't showing refreshes nothing.
	other := infoTestModel()
	_, cmd = other.applyMembersAdded(channelMembersAddedMsg{channelID: "elsewhere", added: []string{"alice"}})
	if cmd != nil {
		t.Error("refetched members for a channel the panel isn't showing")
	}
}

// TestApplyMembersAddedErrors: a total failure reads as an error; a partial one
// reports the users who did join alongside the reason the rest didn't, folded
// onto the single-line status bar.
func TestApplyMembersAddedErrors(t *testing.T) {
	m := infoTestModel()
	out, cmd := m.applyMembersAdded(channelMembersAddedMsg{channelID: "chan123", err: errors.New("no user \"@nobdy\"")})
	m = out.(Model)
	if !strings.HasPrefix(m.status, "add members: ") {
		t.Errorf("status = %q, want the add-members error prefix", m.status)
	}
	if cmd != nil {
		t.Error("nothing was added; want no member refetch")
	}

	joined := errors.Join(errors.New("@bob: not on this team"), errors.New("@eve: not on this team"))
	out, _ = infoTestModel().applyMembersAdded(channelMembersAddedMsg{channelID: "chan123", added: []string{"alice"}, err: joined})
	m = out.(Model)
	if !strings.Contains(m.status, "@alice") || !strings.Contains(m.status, "@bob") {
		t.Errorf("status = %q, want both the added and the refused users", m.status)
	}
	if strings.Contains(m.status, "\n") {
		t.Errorf("status = %q, want the multi-error folded onto one line", m.status)
	}
}
