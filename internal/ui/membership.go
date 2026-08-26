package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Learning about a team or channel the user gained access to *while the app was
// already running*. Until this existed the tab strip and the sidebar were built
// once, at startup, so being added to either left it invisible — along with
// every message in it — until the next restart. The reconnect catch-up
// (resyncAfterReconnect) covers being added while the socket was down; these
// handlers cover being added while it was up.

// membershipResyncDebounce coalesces a burst of membership events into a single
// pair of refetches. A team join is the loud case: one added_to_team plus a
// user_added per default channel, each of which would otherwise cost a round
// trip of its own.
const membershipResyncDebounce = time.Second

// scheduleMembershipResync opens the debounce window, or does nothing if one is
// already open — what lands at the end of it is the full team and channel list,
// so it covers every event that arrived meanwhile.
func (m *Model) scheduleMembershipResync() tea.Cmd {
	if m.me == nil || m.membershipResyncQueued {
		return nil
	}
	m.membershipResyncQueued = true
	return tea.Tick(membershipResyncDebounce, func(time.Time) tea.Msg { return membershipResyncMsg{} })
}

// applyMembershipResyncDue runs the coalesced refetch. All three lists go
// together: a channel in a team with no tab is as invisible as no channel at
// all, and a channel with no member row gets neither an unread badge nor its
// mute state, since both are read from m.members (see applyUnreadFromMembers,
// setMembers).
func (m *Model) applyMembershipResyncDue() tea.Cmd {
	m.membershipResyncQueued = false
	if m.me == nil {
		return nil
	}
	return tea.Batch(
		m.fetchTeams(m.me.Id, true),
		m.fetchAllChannels(m.me.Id, true),
		m.fetchChannelMembers(m.me.Id),
	)
}

// applyUserAdded reacts to `user_added`. The server sends two of them per add:
// one broadcast to the channel (which omits the person added) and one addressed
// straight to that person, so the added user hears about it even though the
// hub's own membership cache is a moment stale. We want only the second, hence
// the user_id check — the rest are somebody else joining a channel we are
// already in, and refetching for those would be a request per join.
func (m *Model) applyUserAdded(ev *model.WebSocketEvent) tea.Cmd {
	if m.me == nil {
		return nil
	}
	if id, _ := ev.GetData()["user_id"].(string); id != m.me.Id {
		return nil
	}
	return m.scheduleMembershipResync()
}

// teamTabAnchor identifies the focused tab by what it *is* rather than where it
// sits. Both resyncs can insert or drop a tab under the cursor — a team the
// user was added to, or the DM tab appearing with their first DM — which would
// otherwise slide the selection onto a different conversation.
func (m *Model) teamTabAnchor() (tabKind, string) {
	kind, id, _ := m.tabAt(m.teamIdx)
	return kind, id
}

// restoreTeamTab points teamIdx back at the anchored tab, or clamps if it is
// gone (the team the user was just removed from).
func (m *Model) restoreTeamTab(kind tabKind, id string) {
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if k, tid, _ := m.tabAt(i); k == kind && tid == id {
			m.teamIdx = i
			return
		}
	}
	if m.teamIdx > m.maxTeamIdx() {
		m.teamIdx = m.maxTeamIdx()
	}
}

// applyTeamsResynced folds a mid-session team list into the tab strip, without
// the once-per-launch work the startup load does around it (the splash, the
// launch telemetry, opening the restored conversation, reloading drafts).
func (m *Model) applyTeamsResynced(msg teamsLoadedMsg) tea.Cmd {
	kind, id := m.teamTabAnchor()
	m.teams = msg.teams
	m.applyTeamOrder()
	m.teamsLoaded = true
	m.restoreTeamTab(kind, id)
	return nil
}

// applyChannelsResynced folds a mid-session channel list into the sidebar, on
// the same terms as applyTeamsResynced.
func (m *Model) applyChannelsResynced(msg channelsLoadedMsg) tea.Cmd {
	// Both cursors are positional, so both need pinning across the rebuild: the
	// tab strip because a first DM inserts the DMs tab ahead of every other, and
	// the sidebar because a channel can sort in above the selected row.
	kind, teamID := m.teamTabAnchor()
	var cursorID string
	if vis := m.visibleChannels(); m.channelIdx >= 0 && m.channelIdx < len(vis) {
		cursorID = vis[m.channelIdx].Id
	}
	m.bucketChannels(msg.channels)
	m.channelsLoaded = true
	m.restoreTeamTab(kind, teamID)
	if cursorID != "" {
		for i, ch := range m.visibleChannels() {
			if ch.Id == cursorID {
				m.channelIdx = i
				break
			}
		}
	}
	// A channel that arrived with unread messages needs its badge derived from
	// the member row rather than from the `posted` events we never heard.
	m.applyUnreadFromMembers()
	// An already-built feed would otherwise keep showing the pre-resync set.
	if m.feed.built {
		return m.buildFeed()
	}
	return nil
}
