package ui

import (
	"encoding/json"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

// Server-side edits to things we already hold: a channel renamed, made private,
// muted from another client, a team renamed, a message marked unread elsewhere.
// None of these change *what* the user belongs to — membership.go owns that,
// and routes through a refetch. These are patched in place instead, because
// refetching three lists to learn a new display name would be silly.
//
// The structural ones among them (a team archived or restored, which takes its
// channels with it) do go to membership.go's resync: that is a membership
// change wearing a team event's clothes.

// wsJSON pulls a JSON-encoded object out of an event's data. The server sends
// these as strings rather than nested objects — `channel`, `team`,
// `channelMember` are all marshalled before being added.
func wsJSON(ev *model.WebSocketEvent, key string, into any) bool {
	raw, ok := ev.GetData()[key].(string)
	if !ok || raw == "" {
		return false
	}
	return json.Unmarshal([]byte(raw), into) == nil
}

// applyChannelUpdated folds an edited channel — renamed, new header or purpose,
// converted — back into the sidebar. Copied over the existing struct rather
// than swapped for the new pointer, so that everything already holding it (the
// open conversation, the info panel, a render in flight) sees the new name too.
func (m *Model) applyChannelUpdated(ev *model.WebSocketEvent) tea.Cmd {
	var updated model.Channel
	if !wsJSON(ev, "channel", &updated) || updated.Id == "" {
		return nil
	}
	existing := m.findChannel(updated.Id)
	if existing == nil {
		return nil // not ours; nothing on screen to correct
	}
	*existing = updated
	return m.resortAfterRename(existing)
}

// applyChannelConverted reacts to a channel flipping between public and
// private. Only the id and the new type are sent, not the channel.
func (m *Model) applyChannelConverted(ev *model.WebSocketEvent) tea.Cmd {
	data := ev.GetData()
	id, _ := data["channel_id"].(string)
	typ, _ := data["channel_type"].(string)
	if id == "" || typ == "" {
		return nil
	}
	existing := m.findChannel(id)
	if existing == nil {
		return nil
	}
	existing.Type = model.ChannelType(typ)
	// The type is part of the label (the 🔒 vs # prefix), so the row's sort
	// position can move even though the display name didn't.
	return m.resortAfterRename(existing)
}

// resortAfterRename puts a channel back in its sorted position after something
// changed its label, keeping the sidebar cursor on the row it was on.
func (m *Model) resortAfterRename(c *model.Channel) tea.Cmd {
	if c.Type == model.ChannelTypeDirect || c.Type == model.ChannelTypeGroup || c.TeamId == "" {
		return nil // the DM bucket sorts by activity, which a rename doesn't touch
	}
	var cursorID string
	if vis := m.visibleChannels(); m.channelIdx >= 0 && m.channelIdx < len(vis) {
		cursorID = vis[m.channelIdx].Id
	}
	m.sortTeamBucket(c.TeamId)
	if cursorID != "" {
		for i, ch := range m.visibleChannels() {
			if ch.Id == cursorID {
				m.channelIdx = i
				break
			}
		}
	}
	return nil
}

// applyChannelMemberUpdated reacts to our own membership row changing on
// another client — muting a channel is the one that matters here, since muted
// channels are kept out of the feed.
//
// The badge counters are deliberately left alone. Deriving them needs the
// channel's TotalMsgCount, which only a channel-list fetch refreshes, while
// m.unread has been counting live `posted` events since; re-deriving from the
// stale total here would quietly discard them. The next resync does both
// together, which is when re-deriving is safe.
func (m *Model) applyChannelMemberUpdated(ev *model.WebSocketEvent) tea.Cmd {
	var updated model.ChannelMember
	if !wsJSON(ev, "channelMember", &updated) || updated.ChannelId == "" {
		return nil
	}
	if m.me != nil && updated.UserId != "" && updated.UserId != m.me.Id {
		return nil
	}
	m.upsertMember(updated)
	m.rebuildMutedChannels()
	if m.feed.built {
		return m.buildFeed()
	}
	return nil
}

// upsertMember replaces our stored membership row for a channel, appending it
// if we had none. setMembers is not used: it rebuilds the muted set from the
// whole slice, and callers here have their own follow-up work to do.
func (m *Model) upsertMember(mb model.ChannelMember) {
	for i := range m.members {
		if m.members[i].ChannelId == mb.ChannelId {
			m.members[i].ChannelMember = mb
			return
		}
	}
	m.members = append(m.members, model.ChannelMemberWithTeamData{ChannelMember: mb})
}

// applyTeamUpdated folds a renamed team back into the tab strip. Copied over
// the existing struct for the same reason applyChannelUpdated does.
func (m *Model) applyTeamUpdated(ev *model.WebSocketEvent) tea.Cmd {
	var updated model.Team
	if !wsJSON(ev, "team", &updated) || updated.Id == "" {
		return nil
	}
	for _, t := range m.teams {
		if t.Id != updated.Id {
			continue
		}
		*t = updated
		// The tab order is by display name, so a rename can move the tab.
		kind, id := m.teamTabAnchor()
		m.applyTeamOrder()
		m.restoreTeamTab(kind, id)
		return nil
	}
	return nil
}

// applyPostUnread reacts to a message being marked unread on another client.
// The server sends the member's rewound counters, which is the same shape the
// badges are normally derived from — so store them and re-derive this one
// channel, rather than inventing a number.
func (m *Model) applyPostUnread(ev *model.WebSocketEvent) tea.Cmd {
	b := ev.GetBroadcast()
	if b == nil || b.ChannelId == "" {
		return nil
	}
	ch := m.findChannel(b.ChannelId)
	if ch == nil {
		return nil
	}
	data := ev.GetData()
	num := func(key string) int64 {
		f, _ := data[key].(float64)
		return int64(f)
	}
	mb := model.ChannelMember{
		ChannelId:        b.ChannelId,
		UserId:           b.UserId,
		MsgCount:         num("msg_count"),
		MsgCountRoot:     num("msg_count_root"),
		MentionCount:     num("mention_count"),
		MentionCountRoot: num("mention_count_root"),
		LastViewedAt:     num("last_viewed_at"),
	}
	m.upsertMember(mb)

	unread, mentions := mm.UnreadCounts(ch, &mb)
	// TotalMsgCount is only refreshed by a channel-list fetch, so it can sit
	// behind the post that was just marked unread and compute zero. The user
	// deliberately marked something unread on another client; showing the
	// channel as read would be the one unacceptable answer.
	if unread < 1 {
		unread = 1
	}
	m.unread[b.ChannelId] = unread
	if mentions > 0 {
		m.mentions[b.ChannelId] = mentions
	} else {
		delete(m.mentions, b.ChannelId)
	}
	if m.feed.built {
		return m.buildFeed()
	}
	return nil
}
