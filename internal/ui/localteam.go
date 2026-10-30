package ui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/config"
	"matterbox/internal/hidden"
	"matterbox/internal/safeterm"
)

// A local team (local_teams in config.yaml) is a team tab of our own, sorted
// among the real ones by team_order. Its channel list is a view over the real
// buckets — the members' DMs, group DMs of only members, configured channels
// from real teams and combined channels — so loading, unread and mark-read
// work on those channels exactly as they do in their own tab. With hide set,
// the same channels leave their usual tab, and switchToChannelHomeTeam lands
// on the local team instead. The server never sees any of it.

// localTeamIDPrefix marks a local team's synthetic id; real ids are 26
// alphanumerics, so the colon can't collide.
const localTeamIDPrefix = "localteam:"

// localDMIDPrefix marks the row of a member we have no DM with yet; opening it
// creates the DM (see openLocalDM).
const localDMIDPrefix = "localdm:"

// localDMType tags such a row so channelLabel can name it.
const localDMType model.ChannelType = "localdm"

type localTeamChannelRef struct {
	teamChannelRef
	alias string
}

type localTeam struct {
	team      *model.Team
	hide      bool
	members   []string // lowercased usernames, config order
	memberSet map[string]bool
	channels  []localTeamChannelRef
	combined  map[string]bool
	// list is the sidebar for this tab, rebuilt by rebuildLocalTeams.
	list []*model.Channel
	// placeholders keeps one row per DM-less member across rebuilds, so the
	// sidebar cursor and openChannelID stay on the same pointer.
	placeholders map[string]*model.Channel
}

// newLocalTeams builds the local teams from config, skipping nameless entries
// and later duplicates of a name.
func newLocalTeams(cfgs []config.LocalTeamConfig) []*localTeam {
	var out []*localTeam
	seen := map[string]bool{}
	for _, c := range cfgs {
		name := strings.TrimSpace(c.Name)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		lt := &localTeam{
			team: &model.Team{
				Id:          localTeamIDPrefix + name,
				Name:        name,
				DisplayName: name,
			},
			hide:         c.Hide,
			memberSet:    map[string]bool{},
			combined:     map[string]bool{},
			placeholders: map[string]*model.Channel{},
		}
		for _, u := range c.Members {
			u = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(u), "@"))
			if u != "" && !lt.memberSet[u] {
				lt.memberSet[u] = true
				lt.members = append(lt.members, u)
			}
		}
		for _, ch := range c.Channels {
			ref, ok := parseTeamChannelRef(ch.Channel)
			if !ok {
				continue
			}
			lt.channels = append(lt.channels, localTeamChannelRef{teamChannelRef: ref, alias: strings.TrimSpace(ch.Name)})
		}
		for _, n := range c.Combined {
			if n = strings.TrimSpace(n); n != "" {
				lt.combined[n] = true
			}
		}
		out = append(out, lt)
	}
	return out
}

func isLocalTeamID(id string) bool { return strings.HasPrefix(id, localTeamIDPrefix) }

func (m *Model) localTeamByID(id string) *localTeam {
	if !isLocalTeamID(id) {
		return nil
	}
	for _, lt := range m.localTeams {
		if lt.team.Id == id {
			return lt
		}
	}
	return nil
}

// tabTeams is the team part of the tab strip, in tab order: the real teams
// plus the local ones once those exist.
func (m *Model) tabTeams() []*model.Team {
	if len(m.localTeams) == 0 {
		return m.teams
	}
	return m.teamTabs
}

// tabChannels is a tab's sidebar before the f filter, unread-only mode and
// deactivated DMs narrow it: a local team's list, otherwise the bucket minus
// what a hiding local team took.
func (m *Model) tabChannels(teamID string) []*model.Channel {
	if lt := m.localTeamByID(teamID); lt != nil {
		return lt.list
	}
	if v, ok := m.homeVisible[teamID]; ok {
		return v
	}
	return m.channels[teamID]
}

// rebuildLocalTeams re-derives every local team's list, the channels a hiding
// team takes out of their usual tab, and the local channel names. Called
// wherever a bucket changes or is re-sorted, so the views never go stale.
func (m *Model) rebuildLocalTeams() {
	if len(m.localTeams) == 0 {
		return
	}
	m.localHome = map[string]string{}
	m.channelAlias = map[string]string{}
	for _, lt := range m.localTeams {
		var combined, dms, chans, missing []*model.Channel
		haveDM := map[string]bool{}
		for _, c := range m.channels[dmTeamID] {
			switch c.Type {
			case model.ChannelTypeDirect:
				name := strings.ToLower(m.userNames[m.dmPartnerID(c)])
				if name != "" && lt.memberSet[name] {
					dms = append(dms, c)
					haveDM[name] = true
				}
			case model.ChannelTypeGroup:
				if m.groupOfMembers(c, lt.memberSet) {
					dms = append(dms, c)
				}
			case combinedChannelType:
				if lt.combined[c.DisplayName] {
					combined = append(combined, c)
				}
			}
		}
		for _, ref := range lt.channels {
			if c := m.teamChannel(ref.teamChannelRef); c != nil {
				chans = append(chans, c)
				if ref.alias != "" {
					m.channelAlias[c.Id] = ref.alias
				}
			}
		}
		for _, u := range lt.members {
			if haveDM[u] || (m.me != nil && strings.EqualFold(m.me.Username, u)) {
				continue
			}
			p := lt.placeholders[u]
			if p == nil {
				p = &model.Channel{Id: localDMIDPrefix + u, Name: u, DisplayName: u, Type: localDMType}
				lt.placeholders[u] = p
			}
			missing = append(missing, p)
		}
		lt.list = slices.Concat(combined, dms, chans, missing)
		if !lt.hide {
			continue
		}
		for _, c := range lt.list {
			if _, taken := m.localHome[c.Id]; !taken && c.Type != localDMType {
				m.localHome[c.Id] = lt.team.Id
			}
		}
	}
	m.homeVisible = map[string][]*model.Channel{}
	for k, list := range m.channels {
		var kept []*model.Channel
		hid := false
		for _, c := range list {
			if _, gone := m.localHome[c.Id]; gone {
				hid = true
				continue
			}
			kept = append(kept, c)
		}
		if hid {
			m.homeVisible[k] = kept
		}
	}
}

// groupOfMembers reports whether every other participant of group DM c is a
// member. Mattermost names a group DM after its participants' sorted
// usernames, ourselves included (model.GetGroupDisplayNameFromUsers).
func (m *Model) groupOfMembers(c *model.Channel, members map[string]bool) bool {
	others := 0
	for _, u := range strings.Split(hidden.Strip(c.DisplayName), ", ") {
		u = strings.ToLower(strings.TrimSpace(u))
		if u == "" || (m.me != nil && strings.EqualFold(m.me.Username, u)) {
			continue
		}
		if !members[u] {
			return false
		}
		others++
	}
	return others > 0
}

// homeTabOf returns the tab id a channel lives on: the hiding local team that
// took it, else its team, else the DMs tab.
func (m *Model) homeTabOf(ch *model.Channel) string {
	if id, ok := m.localHome[ch.Id]; ok {
		return id
	}
	if ch.Type == model.ChannelTypeDirect || ch.Type == model.ChannelTypeGroup || ch.TeamId == "" {
		return dmTeamID
	}
	return ch.TeamId
}

// localDMLabel names a DM-less member's row.
func localDMLabel(c *model.Channel) string { return "@" + safeterm.Line(c.DisplayName) }

// openLocalDM creates (or finds) the DM with a DM-less member. The
// groupDMResolvedMsg it ends in switches to it like "Start group DM" does,
// landing on its home tab — this local team when it hides its channels.
func (m *Model) openLocalDM(channelID string) tea.Cmd {
	return runStartGroupDM(m, "@"+strings.TrimPrefix(channelID, localDMIDPrefix))
}
