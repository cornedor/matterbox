package ui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/forge"
	"matterbox/internal/jira"
)

// maxParkedPanels caps how many channels keep a parked panel; the least
// recently parked one is dropped first.
const maxParkedPanels = 8

// panelKind names which panel a channel had in the right slot.
type panelKind int

const (
	panelThread panelKind = iota + 1
	panelRef
	panelInfo
)

// panelMemo is the right-slot panel a channel had open when the user left it,
// with what it had loaded. Returning to the channel puts it back as it was.
// Session-only.
type panelMemo struct {
	kind panelKind
	yOff int

	// thread
	rootID   string
	selectID string
	posts    []*model.Post

	// ref
	refs            []reference
	refIdx          int
	refLoading      bool
	refErr          error
	refJobsExpanded bool
	jiraIssue       *jira.Issue
	refChange       *forge.Change
	refThreads      []forge.Thread

	// info
	infoMode           infoMode
	infoMainIdx        int
	infoIdx            int
	infoMembers        []*model.User
	infoMembersLoaded  bool
	infoMembersErr     error
	infoPinned         []*model.Post
	infoPinnedLoaded   bool
	infoPinnedErr      error
	infoMedia          []*model.FileInfo
	infoMediaLoaded    bool
	infoMediaTruncated bool
	infoMediaErr       error
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
		memo = panelMemo{
			kind: panelRef, yOff: m.refView.YOffset(),
			refs: m.refs, refIdx: m.refIdx, refLoading: m.refLoading, refErr: m.refErr,
			refJobsExpanded: m.refJobsExpanded, jiraIssue: m.jiraIssue,
			refChange: m.refChange, refThreads: m.refThreads,
		}
	case m.infoOpen:
		owner = m.infoChannelID
		// A profile is a detour, not the channel's panel: close it unremembered.
		if m.infoMode != infoModeProfile {
			memo = panelMemo{
				kind: panelInfo, yOff: m.infoView.YOffset(),
				infoMode: m.infoMode, infoMainIdx: m.infoMainIdx, infoIdx: m.infoIdx,
				infoMembers: m.infoMembers, infoMembersLoaded: m.infoMembersLoaded, infoMembersErr: m.infoMembersErr,
				infoPinned: m.infoPinned, infoPinnedLoaded: m.infoPinnedLoaded, infoPinnedErr: m.infoPinnedErr,
				infoMedia: m.infoMedia, infoMediaLoaded: m.infoMediaLoaded,
				infoMediaTruncated: m.infoMediaTruncated, infoMediaErr: m.infoMediaErr,
			}
		}
	default:
		return nil
	}
	if owner == channelID {
		return nil
	}
	if owner != "" && memo.kind != 0 {
		m.rememberPanel(owner, memo)
	}
	switch {
	case m.threadOpen:
		return m.closeThread()
	case m.refOpen:
		m.closeRef()
	default:
		m.closeInfo()
	}
	return nil
}

// rememberPanel stores memo for channelID, dropping the least recently parked
// channel past maxParkedPanels.
func (m *Model) rememberPanel(channelID string, memo panelMemo) {
	if m.channelPanels == nil {
		m.channelPanels = map[string]panelMemo{}
	}
	m.channelPanels[channelID] = memo
	m.panelOrder = append(slices.DeleteFunc(m.panelOrder, func(id string) bool { return id == channelID }), channelID)
	for len(m.panelOrder) > maxParkedPanels {
		delete(m.channelPanels, m.panelOrder[0])
		m.panelOrder = m.panelOrder[1:]
	}
}

// restorePanel reopens the panel channelID had when the user left it. Focus
// stays where the caller put it. No-op while another panel holds the slot.
func (m *Model) restorePanel(channelID string) tea.Cmd {
	memo, ok := m.channelPanels[channelID]
	if !ok || m.threadOpen || m.refOpen || m.infoOpen {
		return nil
	}
	delete(m.channelPanels, channelID)
	m.panelOrder = slices.DeleteFunc(m.panelOrder, func(id string) bool { return id == channelID })
	switch memo.kind {
	case panelThread:
		// Replies that arrived while parked weren't applied, so refresh behind
		// the posts already on screen.
		return m.showThread(channelID, memo.rootID, memo.selectID, "", memo.posts)
	case panelRef:
		return m.restoreRef(channelID, memo)
	case panelInfo:
		return m.restoreInfo(channelID, memo)
	}
	return nil
}

func (m *Model) restoreRef(channelID string, memo panelMemo) tea.Cmd {
	m.refOpen = true
	m.refChannelID = channelID
	m.refs = memo.refs
	m.refIdx = memo.refIdx
	m.setPanelHint(m.refStatusHint(m.refs[m.refIdx], len(m.refs)))
	m.resizeMessagesViewport()
	// Parked mid-fetch: that result was dropped, so fetch again.
	if memo.refLoading {
		return m.loadCurrentRef()
	}
	m.refErr = memo.refErr
	m.refJobsExpanded = memo.refJobsExpanded
	m.jiraIssue = memo.jiraIssue
	m.refChange = memo.refChange
	m.refThreads = memo.refThreads
	m.renderRef()
	m.refView.SetYOffset(memo.yOff)
	return nil
}

func (m *Model) restoreInfo(channelID string, memo panelMemo) tea.Cmd {
	m.infoOpen = true
	m.infoChannelID = channelID
	m.infoMode = memo.infoMode
	m.infoMainIdx = memo.infoMainIdx
	m.infoIdx = memo.infoIdx
	m.infoHoverIdx = -1
	m.infoScrollFree = false
	m.infoMembers, m.infoMembersLoaded, m.infoMembersErr = memo.infoMembers, memo.infoMembersLoaded, memo.infoMembersErr
	m.infoPinned, m.infoPinnedLoaded, m.infoPinnedErr = memo.infoPinned, memo.infoPinnedLoaded, memo.infoPinnedErr
	m.infoMedia, m.infoMediaLoaded, m.infoMediaTruncated, m.infoMediaErr =
		memo.infoMedia, memo.infoMediaLoaded, memo.infoMediaTruncated, memo.infoMediaErr
	m.setPanelHint("channel info · ↑/↓ select · ↵ open/jump/DM · esc closes")
	m.resizeMessagesViewport()
	m.renderMessages()
	m.renderInfo()
	m.infoView.SetYOffset(memo.yOff)
	// Fetches still in flight when it was parked were dropped on arrival.
	var cmds []tea.Cmd
	if !m.infoMembersLoaded {
		cmds = append(cmds, m.fetchInfoMembers(channelID))
	}
	if !m.infoPinnedLoaded {
		cmds = append(cmds, m.fetchInfoPinned(channelID))
	}
	if !m.infoMediaLoaded {
		cmds = append(cmds, m.fetchInfoMedia(channelID))
	}
	return tea.Batch(cmds...)
}
