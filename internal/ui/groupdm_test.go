package ui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	"github.com/mattermost/mattermost/server/public/model"
)

// groupDMTestModel returns a Model with the fields applyGroupDMResolved
// touches initialised (the textinputs, textarea and viewport need their
// constructors; m.channels must be a live map so the insert doesn't panic).
func groupDMTestModel() Model {
	return Model{
		me:       &model.User{Id: "me123", Username: "me"},
		channels: map[string][]*model.Channel{},
		filter:   textinput.New(),
		switcher: textinput.New(),
		input:    textarea.New(),
		msgsView: viewport.New(),
		width:    80,
	}
}

// TestStartGroupDMCommandRegistered pins the command into the F1 catalogue
// with a captive arg-prompt, so a refactor can't silently drop it.
func TestStartGroupDMCommandRegistered(t *testing.T) {
	var cmd *switcherCommand
	for _, c := range builtinCommands() {
		c := c
		if c.name == "Start group DM" {
			cmd = &c
			break
		}
	}
	if cmd == nil {
		t.Fatal(`"Start group DM" not found in the command catalogue`)
	}
	if cmd.argPrompt == "" {
		t.Error("Start group DM should prompt for the user list (empty argPrompt)")
	}
	if cmd.run == nil {
		t.Error("Start group DM has no runner")
	}
}

// TestRunStartGroupDMRejectsEmpty checks an empty user list is caught before
// any network call (nil Cmd) and surfaces guidance in the status line.
func TestRunStartGroupDMRejectsEmpty(t *testing.T) {
	m := groupDMTestModel()
	if cmd := runStartGroupDM(&m, "   "); cmd != nil {
		t.Error("empty arg should not kick off a resolve Cmd")
	}
	if !strings.Contains(m.status, "group DM") {
		t.Errorf("status = %q, want it to mention the group DM", m.status)
	}
}

// TestApplyGroupDMResolvedInsertsAndSwitches checks a freshly-created group DM
// (not yet in the sidebar) is inserted into the DM bucket and opened.
func TestApplyGroupDMResolvedInsertsAndSwitches(t *testing.T) {
	m := groupDMTestModel()
	ch := &model.Channel{Id: "grp1", Type: model.ChannelTypeGroup, DisplayName: "alice, bob"}

	updated, _ := m.applyGroupDMResolved(groupDMResolvedMsg{ch: ch})
	mm := updated.(Model)

	if mm.findChannel("grp1") == nil {
		t.Fatal("new group DM was not inserted into the channel list")
	}
	if !mm.hasDMs {
		t.Error("inserting the first DM should flip hasDMs")
	}
	if mm.openChannelID != "grp1" {
		t.Errorf("openChannelID = %q, want grp1", mm.openChannelID)
	}
	if mm.focus != focusInput {
		t.Error("focus should land in the composer after opening a group DM")
	}
}

// TestApplyGroupDMResolvedExistingNotDuplicated checks opening a group DM that
// is already in the sidebar switches to it without adding a duplicate row.
func TestApplyGroupDMResolvedExistingNotDuplicated(t *testing.T) {
	m := groupDMTestModel()
	ch := &model.Channel{Id: "grp1", Type: model.ChannelTypeGroup, DisplayName: "alice, bob"}
	m.channels[dmTeamID] = []*model.Channel{ch}
	m.hasDMs = true

	updated, _ := m.applyGroupDMResolved(groupDMResolvedMsg{ch: ch})
	mm := updated.(Model)

	if n := len(mm.channels[dmTeamID]); n != 1 {
		t.Errorf("DM bucket has %d channels, want 1 (no duplicate)", n)
	}
	if mm.openChannelID != "grp1" {
		t.Errorf("openChannelID = %q, want grp1", mm.openChannelID)
	}
}

// TestApplyGroupDMResolvedError checks a resolve failure surfaces in the status
// line and doesn't touch the channel list.
func TestApplyGroupDMResolvedError(t *testing.T) {
	m := groupDMTestModel()

	updated, _ := m.applyGroupDMResolved(groupDMResolvedMsg{err: errors.New("no user \"@ghost\"")})
	mm := updated.(Model)

	if !strings.Contains(mm.status, "ghost") {
		t.Errorf("status = %q, want it to carry the resolve error", mm.status)
	}
	if len(mm.channels[dmTeamID]) != 0 {
		t.Error("a failed resolve should not insert any channel")
	}
	if mm.openChannelID != "" {
		t.Error("a failed resolve should not switch channels")
	}
}
