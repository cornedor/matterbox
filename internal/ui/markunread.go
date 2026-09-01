package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Marking things unread again, from the > palette (F1).
//
// Mattermost has one endpoint for this: set_unread, which rewinds a channel's
// read marker to just before a post (see mm.SetPostUnread). So the entry reads
// "from this message" — there is no way to unread one message in the middle of
// a channel and leave the ones after it read, and we don't pretend otherwise.
//
// The local half matters as much as the call. The channel is usually the open
// one, and the open channel is exactly what the mark-read dwell is about to
// mark read again (see scheduleMarkViewed) — so a hand-made unread is held:
// markUnreadHold pins it until the user leaves the channel, the same way the
// web client keeps a channel unread while you're still looking at it.

// markUnreadCommand returns the mark-unread palette entry, and whether it
// applies — it does while a conversation is open, the same rule as the pin
// toggle. It stays listed regardless of focus and reports "no message selected"
// when run without one, for the same reason the pin toggle does.
func (m *Model) markUnreadCommand() (switcherCommand, bool) {
	if m.findChannel(m.openChannelID) == nil {
		return switcherCommand{}, false
	}
	return switcherCommand{
		name: "Mark unread from this message",
		tid:  "mark_unread_post",
		desc: "the selected message and everything after it count as unread again",
		run:  runMarkPostUnread,
	}, true
}

// runMarkPostUnread rewinds the read marker to the selected message.
func runMarkPostUnread(m *Model, _ string) tea.Cmd {
	p := m.selectedPost()
	if p == nil || p.Id == "" || p.DeleteAt != 0 {
		m.status = "no message selected"
		return nil
	}
	return m.markUnreadFrom(p)
}

// markUnreadFrom fires the set_unread call for p and applies the local half
// optimistically: badges, feed bubble, the "new messages" divider, and the hold
// that stops the dwell marking it read again. The WS post_unread the server
// echoes back carries the authoritative counters and overwrites our estimate
// (see applyPostUnread).
func (m *Model) markUnreadFrom(p *model.Post) tea.Cmd {
	if m.me == nil {
		return nil
	}
	m.applyLocalUnread(p)
	m.status = "marked unread from here"
	userID, postID := m.me.Id, p.Id
	client, ctx := m.client, m.ctx
	feedCmd := tea.Cmd(nil)
	if m.feed.built {
		feedCmd = m.buildFeed()
	}
	return tea.Batch(feedCmd, func() tea.Msg {
		if err := client.SetPostUnread(ctx, userID, postID); err != nil {
			return errMsg{err}
		}
		return nil
	})
}

// applyLocalUnread rewinds the local read state of p's channel to just before
// p: the member's LastViewedAt and message counters (so a later re-derive from
// m.members agrees), the badge, and — when the channel is the open one — the
// divider and the mark-read hold.
func (m *Model) applyLocalUnread(p *model.Post) {
	channelID := p.ChannelId
	ch := m.findChannel(channelID)
	if ch == nil {
		return
	}
	// Count what the rewind makes unread out of the loaded window. It's an
	// estimate — the window is capped and the cache has gaps (see the
	// non-contiguous note in CLAUDE.md) — so it only has to be honest about
	// "at least this many", which the badge shows until the server's counters
	// arrive.
	unread := 0
	for _, q := range m.posts {
		if q != nil && q.ChannelId == channelID && q.DeleteAt == 0 && q.CreateAt >= p.CreateAt {
			unread++
		}
	}
	unread = max(unread, 1)
	for i := range m.members {
		if m.members[i].ChannelId != channelID {
			continue
		}
		mb := &m.members[i]
		mb.LastViewedAt = p.CreateAt - 1
		mb.MsgCount = max(ch.TotalMsgCount-int64(unread), 0)
		mb.MsgCountRoot = max(ch.TotalMsgCountRoot-int64(unread), 0)
		break
	}
	m.unread[channelID] = unread
	if channelID != m.openChannelID {
		return
	}
	// Draw the divider above the post the user just unread, and stop the dwell
	// (and the next live post, and a refocus) from undoing the whole thing.
	m.unreadBoundary = p.CreateAt - 1
	m.unreadDividerID = ""
	m.unreadDividerResolved = false
	m.viewGen++ // invalidate any dwell tick already in flight
	m.viewSettled = false
	m.markUnreadHold = channelID
	m.renderMessages()
}

// markReadHeld reports whether channelID was marked unread by hand and must
// stay that way. The hold is dropped when the channel is next entered (see
// enterChannel), so leaving and coming back reads it normally.
func (m *Model) markReadHeld(channelID string) bool {
	return m.markUnreadHold != "" && m.markUnreadHold == channelID
}
