package ui

import (
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Deactivated accounts. Mattermost keeps a deactivated user as a channel
// member and keeps their DM channel in the channel list, so both the sidebar
// and the channel-info member list would otherwise go on listing people who
// can no longer read or answer anything. We drop them from both.
//
// The flag lives in m.deactivatedUsers, filled from the full user records the
// app already fetches (the DM-partner batch behind the sidebar labels, the
// channel-info member list) and refreshed by user_updated events. Nothing here
// costs an extra request.

// deactivatedGlyph marks a dead account wherever one is still listed — the
// profile panel's status line and the ctrl+p picker. It matches the tombstone
// marker used for deleted messages, for the same reason: the thing is still on
// screen but no longer acts.
const deactivatedGlyph = "⊘"

// deactivatedLabel is the picker's suffix, shown behind the name.
const deactivatedLabel = deactivatedGlyph + " deactivated"

var infoDeactivatedStyle = lipgloss.NewStyle().Foreground(dimColor)

// userDeactivated reports whether the user id belongs to a deactivated
// account, per whatever we have learnt so far.
func (m *Model) userDeactivated(id string) bool {
	return id != "" && m.deactivatedUsers[id]
}

// noteDeactivated folds a userID → deactivated map into m.deactivatedUsers.
// The map carries an entry per user the caller resolved, false included, so a
// reactivated account clears its flag instead of lingering hidden.
func (m *Model) noteDeactivated(seen map[string]bool) {
	if len(seen) == 0 {
		return
	}
	if m.deactivatedUsers == nil {
		m.deactivatedUsers = make(map[string]bool, len(seen))
	}
	for id, off := range seen {
		if id == "" {
			continue
		}
		if off {
			m.deactivatedUsers[id] = true
		} else {
			delete(m.deactivatedUsers, id)
		}
	}
}

// keepActive drops deactivated accounts from a freshly-fetched user list,
// recording what it saw on the way through.
func (m *Model) keepActive(us []*model.User) []*model.User {
	seen := make(map[string]bool, len(us))
	out := make([]*model.User, 0, len(us))
	for _, u := range us {
		if u == nil {
			continue
		}
		seen[u.Id] = u.DeleteAt != 0
		if u.DeleteAt == 0 {
			out = append(out, u)
		}
	}
	m.noteDeactivated(seen)
	return out
}

// dmWithDeactivated reports whether c is a DM the sidebar should hide because
// the other party's account is gone. The open conversation and any DM still
// carrying unread activity stay put, so nothing you are reading — or still owe
// a read — vanishes from under you.
func (m *Model) dmWithDeactivated(c *model.Channel) bool {
	if c == nil || c.Type != model.ChannelTypeDirect {
		return false
	}
	if c.Id == m.openChannelID || m.unread[c.Id] > 0 || m.mentions[c.Id] > 0 {
		return false
	}
	id := m.dmPartnerID(c)
	return id != "" && m.deactivatedUsers[id]
}

// withoutDeactivatedDMs is the sidebar's filter. It runs on every View, so it
// scans before it allocates: with nothing to hide (the common case) the input
// slice is handed straight back.
func (m *Model) withoutDeactivatedDMs(all []*model.Channel) []*model.Channel {
	hit := -1
	for i, c := range all {
		if m.dmWithDeactivated(c) {
			hit = i
			break
		}
	}
	if hit < 0 {
		return all
	}
	out := make([]*model.Channel, hit, len(all)-1)
	copy(out, all[:hit])
	for _, c := range all[hit+1:] {
		if !m.dmWithDeactivated(c) {
			out = append(out, c)
		}
	}
	return out
}
