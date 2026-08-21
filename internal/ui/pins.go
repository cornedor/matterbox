package ui

import (
	tea "charm.land/bubbletea/v2"
)

// pinCommands returns the pin toggle for the selected message plus "Pinned
// messages" (the channel-info panel), and whether they apply — they do while a
// conversation is open, the same rule as the mute toggle. They stay listed
// regardless of focus: ctrl+p is usually reached from the composer, and a run
// with no message selected reports that, matching "Debug: Copy message ID".
func (m *Model) pinCommands() ([]switcherCommand, bool) {
	if m.findChannel(m.openChannelID) == nil {
		return nil, false
	}
	return []switcherCommand{
		m.pinCommand(),
		{
			name: "Pinned messages",
			desc: "open the channel info panel, which lists this channel's pinned messages",
			run:  func(m *Model, _ string) tea.Cmd { return m.raiseChannelInfo() },
		},
	}, true
}

// pinCommand is the pin/unpin toggle; the label follows the selected
// message's current state.
func (m *Model) pinCommand() switcherCommand {
	if p := m.selectedPost(); p != nil && p.IsPinned {
		return switcherCommand{
			name: "Unpin message",
			desc: "remove the selected message from the channel's pinned messages",
			run:  runTogglePinned,
		}
	}
	return switcherCommand{
		name: "Pin message",
		desc: "pin the selected message to the channel",
		run:  runTogglePinned,
	}
}

// runTogglePinned flips the selected message's pin. The post flips
// optimistically so the header mark and the command label update at once; the
// server call follows and applyPinnedChanged reverts on failure.
func runTogglePinned(m *Model, _ string) tea.Cmd {
	p := m.selectedPost()
	if p == nil || p.Id == "" || p.DeleteAt != 0 {
		m.status = "no message selected"
		return nil
	}
	pinned := !p.IsPinned
	m.setPostPinned(p.Id, pinned)
	if pinned {
		m.status = "pinning message…"
	} else {
		m.status = "unpinning message…"
	}
	channelID, postID := p.ChannelId, p.Id
	client, ctx := m.client, m.ctx
	return m.reportActed(func() tea.Msg {
		var err error
		if pinned {
			err = client.PinPost(ctx, postID)
		} else {
			err = client.UnpinPost(ctx, postID)
		}
		return pinnedChangedMsg{channelID: channelID, postID: postID, pinned: pinned, err: err}
	}, m.actedRecord("pin", p, "palette"))
}

type pinnedChangedMsg struct {
	channelID string
	postID    string
	pinned    bool
	err       error
}

func (m Model) applyPinnedChanged(msg pinnedChangedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setPostPinned(msg.postID, !msg.pinned)
		m.status = oneLine(msg.err.Error())
		return m, nil
	}
	if msg.pinned {
		m.status = "message pinned"
	} else {
		m.status = "message unpinned"
	}
	// The info panel lists pinned posts — refresh it if it's showing this channel.
	if m.infoOpen && m.infoChannelID == msg.channelID {
		return m, m.fetchInfoPinned(msg.channelID)
	}
	return m, nil
}

// setPostPinned updates the local copies of the post (channel list and open
// thread) and repaints so the "· pinned" mark tracks the change. The server's
// post_edited echo lands the same way afterwards.
func (m *Model) setPostPinned(postID string, pinned bool) {
	if postID == "" {
		return
	}
	for _, p := range m.posts {
		if p != nil && p.Id == postID {
			p.IsPinned = pinned
		}
	}
	for _, p := range m.threadPosts {
		if p != nil && p.Id == postID {
			p.IsPinned = pinned
		}
	}
	m.invalidatePostLines(postID)
	m.renderMessages()
	if m.threadOpen {
		m.renderThread()
	}
}
