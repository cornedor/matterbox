package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

// Adding people to the open channel. The action has two front doors onto the
// same command: the "Add members to …" entry in the > palette (F1), and the
// "+ Add members…" row at the foot of the channel-info panel's member list —
// which raises that command's argument prompt rather than growing an input of
// its own.

// canAddMembers reports whether people can be added to the channel. DMs and
// group DMs have fixed membership — Mattermost creates a *new* group DM rather
// than growing one — so they're excluded; use "Start group DM" for those.
func canAddMembers(c *model.Channel) bool {
	return c != nil && (c.Type == model.ChannelTypeOpen || c.Type == model.ChannelTypePrivate)
}

// channelMembersAddedMsg carries the outcome of an "Add members" run. added
// lists the usernames that joined; err covers the ones that didn't, so both
// can be non-empty when only some of the named users could be added.
type channelMembersAddedMsg struct {
	channelID string
	added     []string
	err       error
}

// addMembersCommand returns the "Add members" palette entry for the open
// channel, and whether one applies (there's nothing to add to on the
// Feed/Search/SQL tabs, or in a DM).
func (m Model) addMembersCommand() (switcherCommand, bool) {
	c := m.findChannel(m.openChannelID)
	if !canAddMembers(c) {
		return switcherCommand{}, false
	}
	return switcherCommand{
		name:           "Add members to " + m.channelLabel(c),
		desc:           "add people to this channel by username",
		argPrompt:      "users: ",
		argPlaceholder: "@alice, @bob",
		run:            runAddChannelMembers(c.Id),
	}, true
}

// runAddChannelMembers returns a runner that adds the users named in the
// argument to the channel that was open when the palette was raised. The
// resolution and the adds touch the network, so they run in the returned Cmd.
func runAddChannelMembers(channelID string) func(*Model, string) tea.Cmd {
	return func(m *Model, arg string) tea.Cmd {
		spec := strings.TrimSpace(arg)
		if spec == "" {
			m.status = "add members: name at least one user (e.g. @alice, @bob)"
			return nil
		}
		m.status = "adding members…"
		client, ctx := m.client, m.ctx
		return func() tea.Msg {
			added, err := mm.AddMembers(ctx, client, channelID, spec)
			return channelMembersAddedMsg{channelID: channelID, added: added, err: err}
		}
	}
}

// applyMembersAdded reports the outcome and refreshes the info panel's member
// list when the panel is showing the channel that grew. The server posts a
// system message for each addition, which reaches the message pane over the
// WebSocket, so nothing else needs a refetch.
func (m Model) applyMembersAdded(msg channelMembersAddedMsg) (tea.Model, tea.Cmd) {
	label := msg.channelID
	if c := m.findChannel(msg.channelID); c != nil {
		label = m.channelLabel(c)
	}
	switch {
	case msg.err != nil && len(msg.added) == 0:
		m.status = "add members: " + oneLine(msg.err.Error())
	case msg.err != nil:
		m.status = "added " + atNames(msg.added) + " to " + label + " · " + oneLine(msg.err.Error())
	default:
		m.status = "added " + atNames(msg.added) + " to " + label
	}
	if len(msg.added) > 0 && m.infoOpen && m.infoChannelID == msg.channelID {
		return m, m.fetchInfoMembers(msg.channelID)
	}
	return m, nil
}

// openAddMembersPrompt raises the switcher straight into the "Add members"
// command's captive argument prompt, so the info panel's "+ Add members…" row
// borrows the palette's input. The panel stays open behind the modal and
// refreshes its member list once the add lands.
func (m Model) openAddMembersPrompt() (tea.Model, tea.Cmd) {
	cmd, ok := m.addMembersCommand()
	if !ok {
		m.status = "can't add members to this channel"
		return m, nil
	}
	m.switcherMode = true
	m.enterCommandArgMode(cmd)
	return m, m.switcher.Focus()
}

// atNames renders resolved usernames as a readable "@a, @b" list.
func atNames(names []string) string {
	return "@" + strings.Join(names, ", @")
}

// oneLine folds a multi-error (errors.Join separates with newlines) onto the
// single-line status bar.
func oneLine(s string) string {
	return strings.ReplaceAll(s, "\n", "; ")
}
