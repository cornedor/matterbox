package ui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// The channel actions that change more than a field: archiving it, leaving it,
// and converting it between public and private. Each is one irreversible-ish
// request with a consequence worth stating, so they share a y/n confirm rather
// than firing straight off the > palette (F1). Reversing them needs someone
// else's help — a system admin to restore an archive, an invite to get back
// into a private channel — which is exactly what the confirm's note says.
//
// The sidebar is reconciled here on success (dropChannel) rather than waiting
// for the channel_deleted / user_removed the server echoes back, the same way
// applyChannelCreated splices a new channel in. Those events are handled too
// (see membership.go), for the same changes made from another client.

type channelConfirmKind int

const (
	chanConfirmArchive channelConfirmKind = iota
	chanConfirmLeave
	chanConfirmPrivacy
)

// channelConfirmState owns the modal. Boxed on Model (nil when closed) to keep
// Model's size ceiling, matching the other channel modals.
type channelConfirmState struct {
	kind      channelConfirmKind
	channelID string
	label     string
	title     string
	note      string            // the consequence, stated before the user commits
	toType    model.ChannelType // privacy target; unused by the other kinds
	running   bool
}

// isDefaultChannel reports whether the channel is its team's town-square. The
// server refuses to archive it, convert it, or let anyone leave it, so those
// commands aren't offered for it at all.
func isDefaultChannel(c *model.Channel) bool {
	return c != nil && c.Name == model.DefaultChannelName
}

// canManageChannel reports whether the "big" channel actions apply. DMs and
// group DMs have no membership or privacy to manage (you close them, you don't
// leave them), and a team's default channel is exempt server-side.
func canManageChannel(c *model.Channel) bool {
	return canEditChannel(c) && !isDefaultChannel(c)
}

// channelActionCommands returns the archive / leave / privacy palette entries
// for the open channel, and whether any apply.
func (m Model) channelActionCommands() ([]switcherCommand, bool) {
	c := m.findChannel(m.openChannelID)
	if !canManageChannel(c) {
		return nil, false
	}
	label := m.channelLabel(c)

	privacyName, privacyDesc := "Make "+label+" private", "only invited members will be able to read it"
	if c.Type == model.ChannelTypePrivate {
		privacyName, privacyDesc = "Make "+label+" public", "anyone on the team will be able to join and read it"
	}
	return []switcherCommand{
		{
			name: privacyName,
			desc: privacyDesc,
			run:  runChannelConfirm(chanConfirmPrivacy, c.Id),
		},
		{
			name: "Leave " + label,
			desc: "remove yourself from this channel",
			run:  runChannelConfirm(chanConfirmLeave, c.Id),
		},
		{
			name: "Archive " + label,
			desc: "close the channel for everyone (the history is kept)",
			run:  runChannelConfirm(chanConfirmArchive, c.Id),
		},
	}, true
}

// runChannelConfirm returns a runner that raises the confirm for the channel
// that was open when the palette was raised.
func runChannelConfirm(kind channelConfirmKind, channelID string) func(*Model, string) tea.Cmd {
	return func(m *Model, _ string) tea.Cmd {
		m.openChannelConfirm(kind, channelID)
		return nil
	}
}

// openChannelConfirm raises the confirm, phrasing the question and the
// consequence from the channel's current state.
func (m *Model) openChannelConfirm(kind channelConfirmKind, channelID string) {
	c := m.findChannel(channelID)
	if !canManageChannel(c) {
		m.status = "can't do that to this channel"
		return
	}
	st := &channelConfirmState{
		kind:      kind,
		channelID: c.Id,
		label:     m.channelLabel(c),
	}
	switch kind {
	case chanConfirmArchive:
		st.title = "Archive " + st.label + "?"
		st.note = "It closes for everyone. The history is kept, but only a system admin can bring it back."
	case chanConfirmLeave:
		st.title = "Leave " + st.label + "?"
		st.note = "You'll stop receiving its messages."
		if c.Type == model.ChannelTypePrivate {
			st.note = "You'll stop receiving its messages, and you'll need an invite to rejoin — it's private."
		}
	case chanConfirmPrivacy:
		if c.Type == model.ChannelTypePrivate {
			st.toType = model.ChannelTypeOpen
			st.title = "Make " + st.label + " public?"
			st.note = "Anyone on the team will be able to join it and read its history."
		} else {
			st.toType = model.ChannelTypePrivate
			st.title = "Make " + st.label + " private?"
			st.note = "Only its current members will keep access. Converting back is a separate change."
		}
	}
	m.chanConfirm = st
}

// closeChannelConfirm tears the modal down. Safe to call when it isn't open.
func (m *Model) closeChannelConfirm() {
	m.chanConfirm = nil
}

// handleChannelConfirmKey owns every keystroke while the confirm is open:
// y/enter fires the action, n/esc cancels.
func (m Model) handleChannelConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "y", "Y", "enter":
		if m.chanConfirm.running {
			return m, nil
		}
		return m.applyChannelConfirm()
	case "n", "N", "esc":
		m.closeChannelConfirm()
		return m, nil
	}
	return m, nil
}

// channelActionDoneMsg carries the outcome of an archive / leave / privacy
// change. typ is the channel's new type, set by a privacy change only.
type channelActionDoneMsg struct {
	kind      channelConfirmKind
	channelID string
	label     string
	typ       model.ChannelType
	err       error
}

// applyChannelConfirm fires the confirmed action in the background. The modal
// stays up (marked running) until the result lands, so a slow server can't be
// mistaken for a no-op.
func (m Model) applyChannelConfirm() (tea.Model, tea.Cmd) {
	st := m.chanConfirm
	if st.kind == chanConfirmLeave && m.me == nil {
		m.status = "leave: user not loaded yet"
		return m, nil
	}
	st.running = true

	kind, channelID, label, toType := st.kind, st.channelID, st.label, st.toType
	client, ctx := m.client, m.ctx
	var meID string
	if m.me != nil {
		meID = m.me.Id
	}
	return m, func() tea.Msg {
		done := channelActionDoneMsg{kind: kind, channelID: channelID, label: label, typ: toType}
		switch kind {
		case chanConfirmArchive:
			done.err = client.ArchiveChannel(ctx, channelID)
		case chanConfirmLeave:
			done.err = client.RemoveChannelMember(ctx, channelID, meID)
		case chanConfirmPrivacy:
			_, done.err = client.UpdateChannelPrivacy(ctx, channelID, toType)
		}
		return done
	}
}

// applyChannelActionDone folds the result into the sidebar: an archive or a
// leave drops the channel, a privacy change just flips its type (which the
// sidebar glyph and the info panel read straight off the record). A failure
// leaves everything as it was and reports why.
func (m Model) applyChannelActionDone(msg channelActionDoneMsg) (tea.Model, tea.Cmd) {
	m.closeChannelConfirm()
	if msg.err != nil {
		m.status = channelActionVerb(msg.kind) + " " + msg.label + ": " + oneLine(msg.err.Error())
		return m, nil
	}
	switch msg.kind {
	case chanConfirmArchive:
		cmd := m.dropChannel(msg.channelID)
		m.status = "archived " + msg.label
		return m, cmd
	case chanConfirmLeave:
		cmd := m.dropChannel(msg.channelID)
		m.status = "left " + msg.label
		return m, cmd
	case chanConfirmPrivacy:
		kind := "public"
		if msg.typ == model.ChannelTypePrivate {
			kind = "private"
		}
		if c := m.findChannel(msg.channelID); c != nil {
			c.Type = msg.typ
		}
		m.status = msg.label + " is now " + kind
		return m, nil
	}
	return m, nil
}

// channelActionVerb names the action for an error message.
func channelActionVerb(kind channelConfirmKind) string {
	switch kind {
	case chanConfirmArchive:
		return "archive"
	case chanConfirmLeave:
		return "leave"
	}
	return "convert"
}

// channelActionGerund is the in-flight footer while the request is out.
func channelActionGerund(kind channelConfirmKind) string {
	switch kind {
	case chanConfirmArchive:
		return "archiving…"
	case chanConfirmLeave:
		return "leaving…"
	}
	return "converting…"
}

// dropChannel removes a channel we can no longer see (archived, or left) from
// the sidebar. When it's the open conversation, the pane moves to its
// neighbour in the same team — or empties out when the team has nothing left.
// Returns the load Cmd for the channel it moved to, if any.
func (m *Model) dropChannel(channelID string) tea.Cmd {
	c := m.findChannel(channelID)
	if c == nil {
		return nil
	}
	teamID := c.TeamId
	if teamID == "" {
		teamID = dmTeamID
	}
	list := m.channels[teamID]
	idx := -1
	for i, ch := range list {
		if ch.Id == channelID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	// Copy on remove: the bucket's backing array is shared with slices handed to
	// the render/sidebar paths, so shifting in place would corrupt them.
	rest := make([]*model.Channel, 0, len(list)-1)
	rest = append(rest, list[:idx]...)
	rest = append(rest, list[idx+1:]...)
	m.channels[teamID] = rest

	delete(m.unread, channelID)
	delete(m.mentions, channelID)
	if m.infoOpen && m.infoChannelID == channelID {
		m.closeInfo()
	}
	if m.openChannelID != channelID {
		// Every row below the one that went away shifted up: follow it, so the
		// cursor stays on the channel it was on.
		if idx < m.channelIdx {
			m.channelIdx--
		}
		if m.channelIdx >= len(rest) {
			m.channelIdx = max(len(rest)-1, 0)
		}
		return nil
	}

	return m.landAfterOpenChannelGone(rest, idx)
}

// landAfterOpenChannelGone moves the user off a conversation that no longer
// exists: they left it or archived it (dropChannel), or a membership resync
// came back without it — removed from the channel, or from its whole team,
// while this client was asleep. Leaving openChannelID pointing at a channel
// that isn't in the sidebar any more means a transcript nothing can refresh and
// a composer aimed at a channel the user isn't in.
//
// near is where the departed channel sat in bucket, so the landing is its
// neighbour rather than the top of the list.
func (m *Model) landAfterOpenChannelGone(bucket []*model.Channel, near int) tea.Cmd {
	// A live sidebar filter indexes a different list than channelIdx does, so
	// clear it before re-pointing the cursor (as the create/join paths do).
	m.filterValue = ""
	m.filter.SetValue("")
	if len(bucket) == 0 {
		m.enterChannel("", "restore")
		m.channelIdx = 0
		m.posts = nil
		m.postIdx = 0
		m.renderMessages()
		return nil
	}
	next := bucket[min(max(near, 0), len(bucket)-1)]
	m.switchToChannelHomeTeam(next)
	// Not a conversation the user picked — the app landing them somewhere after
	// the one they were in went away.
	return m.openChannelLoadCmd(next.Id, "restore")
}

// renderChannelConfirm draws the centred yes/no modal, with the consequence
// spelled out under the question.
func (m *Model) renderChannelConfirm() string {
	st := m.chanConfirm
	if st == nil {
		return ""
	}
	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 30 {
		outerW = 30
	}
	inner := outerW - 8 // border (2) + padding (6)
	if inner < 1 {
		inner = 1
	}

	question := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).Bold(true).
		Render(truncate(st.title, inner))
	note := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).
		Foreground(dimColor).Italic(true).Render(st.note)

	footer := "y confirm · n cancel"
	if st.running {
		footer = channelActionGerund(st.kind)
	}
	hint := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center).
		Foreground(dimColor).Italic(true).Render(footer)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(focusedColor).
		Padding(1, 3).
		Render(lipgloss.JoinVertical(lipgloss.Left, question, "", note, "", hint))
}
