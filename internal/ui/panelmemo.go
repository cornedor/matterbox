package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// panelKind names which panel a channel had in the right slot.
type panelKind int

const (
	panelThread panelKind = iota + 1
	panelRef
	panelInfo
)

// panelMemo is the right-slot panel a channel had open when the user left it.
// Returning to the channel reopens it where it was. Session-only.
type panelMemo struct {
	kind panelKind
	// thread
	rootID   string
	selectID string
	posts    []*model.Post
	// ref
	refs   []reference
	refIdx int
}

// parkPanel closes the right-slot panel unless it belongs to channelID, the
// channel being entered, and remembers it under the channel it belongs to.
// Called by enterChannel before openChannelID moves.
func (m *Model) parkPanel(channelID string) tea.Cmd {
	var owner string
	var memo panelMemo
	switch {
	case m.threadOpen:
		owner = m.threadChannelID
		memo = panelMemo{kind: panelThread, rootID: m.threadRootID, posts: m.threadPosts}
		if m.threadIdx >= 0 && m.threadIdx < len(m.threadPosts) {
			memo.selectID = m.threadPosts[m.threadIdx].Id
		}
	case m.refOpen:
		owner = m.refChannelID
		memo = panelMemo{kind: panelRef, refs: m.refs, refIdx: m.refIdx}
	case m.infoOpen:
		owner = m.infoChannelID
		memo = panelMemo{kind: panelInfo}
		// A profile is a detour, not the channel's panel: close it unremembered.
		if m.infoMode == infoModeProfile {
			memo.kind = 0
		}
	default:
		return nil
	}
	if owner == channelID {
		return nil
	}
	if owner != "" && memo.kind != 0 {
		if m.channelPanels == nil {
			m.channelPanels = map[string]panelMemo{}
		}
		m.channelPanels[owner] = memo
	}
	switch memo.kind {
	case panelThread:
		return m.closeThread()
	case panelRef:
		m.closeRef()
	default:
		m.closeInfo()
	}
	return nil
}

// restorePanel reopens the panel channelID had when the user left it. Focus
// stays where the caller put it. No-op while another panel holds the slot.
func (m *Model) restorePanel(channelID string) tea.Cmd {
	memo, ok := m.channelPanels[channelID]
	if !ok || m.threadOpen || m.refOpen || m.infoOpen {
		return nil
	}
	delete(m.channelPanels, channelID)
	switch memo.kind {
	case panelThread:
		return m.showThread(channelID, memo.rootID, memo.selectID, "", memo.posts)
	case panelRef:
		m.refOpen = true
		m.refChannelID = channelID
		m.refs = memo.refs
		m.refIdx = memo.refIdx
		m.setPanelHint(m.refStatusHint(m.refs[m.refIdx], len(m.refs)))
		cmd := m.loadCurrentRef()
		m.resizeMessagesViewport()
		return cmd
	case panelInfo:
		focus := m.focus
		cmd := m.raiseChannelInfo()
		m.focus = focus
		return cmd
	}
	return nil
}
