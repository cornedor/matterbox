package ui

import (
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/config"
)

// A combined channel is a local, read-only sidebar entry (combined_channels in
// config.yaml) that merges the DMs with a set of users — typically bots — into
// one timeline, together with any configured channels from real teams. The
// server never sees it: its synthetic row sits at the top of the DM bucket, and
// everything that would touch the server fans out to the member channels
// instead — loading, paging, live posts, badges and mark-read. Posts keep their
// own ChannelId, so reactions, edits and thread replies go to the channel the
// message came from; only a top-level send has no target (see
// combinedSendBlocked).

// combinedIDPrefix marks a synthetic combined-channel id. Real ids are 26
// alphanumerics, so the colon can't collide.
const combinedIDPrefix = "combined:"

// combinedChannelType tags the synthetic row so the channel-type switches
// (label, breadcrumb, presence) can tell it apart from a real channel.
const combinedChannelType model.ChannelType = "combined"

type combinedChannel struct {
	ch        *model.Channel
	usernames map[string]bool  // lowercased, no leading @
	channels  []teamChannelRef // channels from real teams
	// memberIDs is the resolved set of channel ids, cached against the inputs
	// it was derived from: the DM bucket's length, the number of known
	// usernames and the referenced team buckets' total length. Joining or
	// leaving a channel, or a new DM or username, changes one of them.
	memberIDs   []string
	resolvedFor [3]int
	resolved    bool
}

// teamChannelRef names a channel of a real team by URL names, lowercased.
type teamChannelRef struct{ team, channel string }

// parseTeamChannelRef reads "team/channel"; ok is false for anything else.
func parseTeamChannelRef(s string) (ref teamChannelRef, ok bool) {
	team, channel, ok := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "/")
	if !ok || team == "" || channel == "" {
		return teamChannelRef{}, false
	}
	return teamChannelRef{team: team, channel: channel}, true
}

// teamBucket returns the channels we're in on the team named team.
func (m *Model) teamBucket(team string) []*model.Channel {
	for _, t := range m.teams {
		if strings.EqualFold(t.Name, team) {
			return m.channels[t.Id]
		}
	}
	return nil
}

// teamChannel finds the channel ref points at among the ones we're in.
func (m *Model) teamChannel(ref teamChannelRef) *model.Channel {
	for _, c := range m.teamBucket(ref.team) {
		if strings.EqualFold(c.Name, ref.channel) {
			return c
		}
	}
	return nil
}

// newCombinedChannels builds the synthetic rows from config, skipping entries
// without a name, without members and channels, and later duplicates of a name.
func newCombinedChannels(cfgs []config.CombinedChannelConfig) []*combinedChannel {
	var out []*combinedChannel
	seen := map[string]bool{}
	for _, c := range cfgs {
		name := strings.TrimSpace(c.Name)
		if name == "" || seen[name] {
			continue
		}
		users := map[string]bool{}
		for _, u := range c.Members {
			if u = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(u), "@")); u != "" {
				users[u] = true
			}
		}
		var refs []teamChannelRef
		for _, ch := range c.Channels {
			if ref, ok := parseTeamChannelRef(ch); ok {
				refs = append(refs, ref)
			}
		}
		if len(users) == 0 && len(refs) == 0 {
			continue
		}
		seen[name] = true
		out = append(out, &combinedChannel{
			ch: &model.Channel{
				Id:          combinedIDPrefix + name,
				Name:        name,
				DisplayName: name,
				Type:        combinedChannelType,
			},
			usernames: users,
			channels:  refs,
		})
	}
	return out
}

func isCombinedID(id string) bool { return strings.HasPrefix(id, combinedIDPrefix) }

func isCombined(c *model.Channel) bool { return c != nil && c.Type == combinedChannelType }

func (m *Model) combinedByID(id string) *combinedChannel {
	if !isCombinedID(id) {
		return nil
	}
	for _, cc := range m.combined {
		if cc.ch.Id == id {
			return cc
		}
	}
	return nil
}

// combinedMemberIDs returns the channel ids a combined channel merges, or nil
// when id isn't a combined channel. A member whose DM doesn't exist yet, whose
// username isn't resolved yet, or a channel we aren't in, is simply absent
// until it is.
func (m *Model) combinedMemberIDs(id string) []string {
	cc := m.combinedByID(id)
	if cc == nil {
		return nil
	}
	dms := m.channels[dmTeamID]
	teamLen := 0
	for _, ref := range cc.channels {
		teamLen += len(m.teamBucket(ref.team))
	}
	key := [3]int{len(dms), len(m.userNames), teamLen}
	if cc.resolved && cc.resolvedFor == key {
		return cc.memberIDs
	}
	cc.memberIDs = cc.memberIDs[:0]
	for _, c := range dms {
		if name := m.userNames[m.dmPartnerID(c)]; name != "" && cc.usernames[strings.ToLower(name)] {
			cc.memberIDs = append(cc.memberIDs, c.Id)
		}
	}
	for _, ref := range cc.channels {
		if c := m.teamChannel(ref); c != nil && !slices.Contains(cc.memberIDs, c.Id) {
			cc.memberIDs = append(cc.memberIDs, c.Id)
		}
	}
	cc.resolvedFor, cc.resolved = key, true
	return cc.memberIDs
}

// postChannelIDs is the set of real channels whose posts make up the
// conversation id: its members for a combined channel, otherwise itself.
func (m *Model) postChannelIDs(id string) []string {
	if isCombinedID(id) {
		return m.combinedMemberIDs(id)
	}
	return []string{id}
}

// openCovers reports whether channelID's posts belong in the open
// conversation: it is the open channel, or a member of the open combined one.
func (m *Model) openCovers(channelID string) bool {
	if channelID == "" {
		return false
	}
	if channelID == m.openChannelID {
		return true
	}
	if !isCombinedID(m.openChannelID) {
		return false
	}
	for _, id := range m.combinedMemberIDs(m.openChannelID) {
		if id == channelID {
			return true
		}
	}
	return false
}

// channelBadges returns the unread and mention counts the sidebar shows for c:
// its own, or for a combined channel the sum over its members.
func (m *Model) channelBadges(c *model.Channel) (unread, mentions int) {
	if !isCombined(c) {
		return m.unread[c.Id], m.mentions[c.Id]
	}
	for _, id := range m.combinedMemberIDs(c.Id) {
		unread += m.unread[id]
		mentions += m.mentions[id]
	}
	return unread, mentions
}

// pinCombinedChannels puts the combined rows at the top of the DM bucket, in
// config order, and drops their resolved members so they re-resolve against
// the rebuilt bucket.
func (m *Model) pinCombinedChannels() {
	if len(m.combined) == 0 {
		return
	}
	rows := make([]*model.Channel, 0, len(m.combined)+len(m.channels[dmTeamID]))
	for _, cc := range m.combined {
		cc.resolved = false
		rows = append(rows, cc.ch)
	}
	m.channels[dmTeamID] = append(rows, m.channels[dmTeamID]...)
}

// sortDMs orders the DM bucket: combined channels first (stable, so in config
// order), then most recently chatted in, then by label.
func (m *Model) sortDMs(list []*model.Channel) {
	sort.SliceStable(list, func(i, j int) bool {
		ci, cj := isCombined(list[i]), isCombined(list[j])
		if ci || cj {
			return ci && !cj
		}
		ai, aj := list[i].LastPostAt, list[j].LastPostAt
		if ai != aj {
			return ai > aj
		}
		return strings.ToLower(m.channelLabel(list[i])) < strings.ToLower(m.channelLabel(list[j]))
	})
}

// combinedUnreadBoundary is the read/unread boundary for opening a combined
// channel: the oldest LastViewedAt among its unread members, so the divider
// sits above the first message any of them hasn't seen.
func (m *Model) combinedUnreadBoundary(id string) int64 {
	var boundary int64
	for _, mid := range m.combinedMemberIDs(id) {
		if m.unread[mid] == 0 {
			continue
		}
		for _, mb := range m.members {
			if mb.ChannelId == mid && (boundary == 0 || mb.LastViewedAt < boundary) {
				boundary = mb.LastViewedAt
			}
		}
	}
	return boundary
}

// combinedSendBlocked reports whether the composer has nowhere to post: a
// combined channel has no channel of its own, so only a thread reply (which
// targets the message's own DM) can be sent.
func (m *Model) combinedSendBlocked() bool {
	return !m.threadOpen && isCombinedID(m.openChannelID)
}

// fetchRecentCombined reconciles each member's recent window, one
// postsGapFilledMsg per member; the handler merges every one that the open
// conversation covers.
func (m *Model) fetchRecentCombined(id string) tea.Cmd {
	var cmds []tea.Cmd
	for _, mid := range m.combinedMemberIDs(id) {
		cmds = append(cmds, m.fetchRecent(mid))
	}
	return tea.Batch(cmds...)
}

// syncCombinedDeletions runs syncChannelDeletions per member.
func (m *Model) syncCombinedDeletions(id string) tea.Cmd {
	if m.store == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, mid := range m.combinedMemberIDs(id) {
		since, _ := m.store.MaxUpdateAt(mid)
		cmds = append(cmds, m.syncChannelDeletions(mid, since))
	}
	return tea.Batch(cmds...)
}

// combinedPageAnchors maps each member of the open combined channel to the
// id of its oldest (older=true) or newest loaded post, the per-channel anchor
// a server page needs. Members with nothing loaded are left out.
func (m *Model) combinedPageAnchors(older bool) map[string]string {
	anchors := map[string]string{}
	members := map[string]bool{}
	for _, id := range m.combinedMemberIDs(m.openChannelID) {
		members[id] = true
	}
	for i := range m.posts {
		p := m.posts[i]
		if !older {
			p = m.posts[len(m.posts)-1-i]
		}
		if p == nil || p.Id == "" || !members[p.ChannelId] {
			continue
		}
		if _, ok := anchors[p.ChannelId]; !ok {
			anchors[p.ChannelId] = p.Id
		}
	}
	return anchors
}

// fetchCombinedPage pulls a page of older (or newer) history for every member
// of the open combined channel and returns it as one olderPostsMsg /
// newerPostsMsg keyed on the combined id. Pages from different DMs cover
// different time spans, so the merge is cut where the shortest full page ends:
// past that point a member may have posts this round didn't fetch, and showing
// the others there would leave a hole that later pages fill out of order.
func (m *Model) fetchCombinedPage(older bool) tea.Cmd {
	id := m.openChannelID
	anchors := m.combinedPageAnchors(older)
	if len(anchors) == 0 {
		return nil
	}
	known := snapshotNames(m.userNames)
	client, ctx := m.client, m.ctx
	resolve := m.resolveSenderNames
	return func() tea.Msg {
		var posts []*model.Post
		var cut int64
		exhausted := true
		for cid, pid := range anchors {
			var pl *model.PostList
			var err error
			if older {
				pl, err = client.PostsBefore(ctx, cid, pid, historyPageSize)
			} else {
				pl, err = client.PostsAfter(ctx, cid, pid, historyPageSize)
			}
			if err != nil {
				return errMsg{err}
			}
			page := orderOldestFirst(pl)
			more := pl.PrevPostId != ""
			if !older {
				more = pl.NextPostId != ""
			}
			if more && len(page) > 0 {
				exhausted = false
				edge := page[0].CreateAt
				if !older {
					edge = page[len(page)-1].CreateAt
				}
				if cut == 0 || (older && edge > cut) || (!older && edge < cut) {
					cut = edge
				}
			}
			posts = append(posts, page...)
		}
		kept := posts[:0]
		for _, p := range posts {
			if cut == 0 || (older && p.CreateAt >= cut) || (!older && p.CreateAt <= cut) {
				kept = append(kept, p)
			}
		}
		sort.SliceStable(kept, func(i, j int) bool { return kept[i].CreateAt < kept[j].CreateAt })
		users, err := resolve(known, kept)
		if err != nil {
			return errMsg{err}
		}
		if older {
			return olderPostsMsg{channelID: id, posts: kept, users: users, atChannelStart: exhausted}
		}
		return newerPostsMsg{channelID: id, posts: kept, users: users, atChannelEnd: exhausted}
	}
}

// fetchOlderOpen pages older server history into the open conversation,
// anchored on the oldest loaded post.
func (m *Model) fetchOlderOpen(oldestID string) tea.Cmd {
	if isCombinedID(m.openChannelID) {
		return m.fetchCombinedPage(true)
	}
	return m.fetchOlder(m.openChannelID, oldestID)
}

// fetchNewerOpen is fetchOlderOpen's forward mirror.
func (m *Model) fetchNewerOpen(newestID string) tea.Cmd {
	if isCombinedID(m.openChannelID) {
		return m.fetchCombinedPage(false)
	}
	return m.fetchNewer(m.openChannelID, newestID)
}
