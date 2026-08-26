package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Learning about a channel the user gained access to *while the app was
// already running*. Until this existed the sidebar was only ever built once,
// at startup, so being added to a channel — by a colleague, or by joining a
// team — left it invisible, along with every message posted in it, until the
// next restart. The reconnect catch-up (resyncAfterReconnect) covers the
// asleep-through-the-add case; these handlers cover the awake one.

// channelResyncDebounce coalesces a burst of membership events into a single
// refetch. One add is one event, but a team join arrives as several at once,
// and each would otherwise cost a full channel-list round trip.
const channelResyncDebounce = time.Second

// scheduleChannelResync opens the debounce window, or does nothing if one is
// already open — the refetch that lands at the end of it is a full list, so it
// covers every event that arrived meanwhile.
func (m *Model) scheduleChannelResync() tea.Cmd {
	if m.me == nil || m.channelResyncQueued {
		return nil
	}
	m.channelResyncQueued = true
	return tea.Tick(channelResyncDebounce, func(time.Time) tea.Msg { return channelResyncMsg{} })
}

// applyChannelResyncDue runs the coalesced refetch. Members ride along: a
// channel with no member row gets no unread badge and no mute state, since
// both are read from m.members (see applyUnreadFromMembers, setMembers).
func (m *Model) applyChannelResyncDue() tea.Cmd {
	m.channelResyncQueued = false
	if m.me == nil {
		return nil
	}
	return tea.Batch(m.fetchAllChannels(m.me.Id, true), m.fetchChannelMembers(m.me.Id))
}

// applyUserAdded reacts to `user_added`. The event is broadcast to the whole
// channel, so nearly all of them are somebody else joining a channel we are
// already in — only our own addition changes the sidebar.
func (m *Model) applyUserAdded(ev *model.WebSocketEvent) tea.Cmd {
	if m.me == nil {
		return nil
	}
	if id, _ := ev.GetData()["user_id"].(string); id != m.me.Id {
		return nil
	}
	return m.scheduleChannelResync()
}

// applyChannelsResynced folds a mid-session channel list into the sidebar,
// without any of the once-per-launch work the startup load does around it (the
// splash, the launch telemetry, opening the restored conversation, starting
// the presence poll).
func (m *Model) applyChannelsResynced(msg channelsLoadedMsg) tea.Cmd {
	// The sidebar cursor is positional, so a channel appearing above it would
	// slide the selection onto a different row. Pin it to the channel it points
	// at across the re-bucket, the way touchChannelActivity does across a sort.
	var cursorID string
	if vis := m.visibleChannels(); m.channelIdx >= 0 && m.channelIdx < len(vis) {
		cursorID = vis[m.channelIdx].Id
	}
	m.bucketChannels(msg.channels)
	m.channelsLoaded = true
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
