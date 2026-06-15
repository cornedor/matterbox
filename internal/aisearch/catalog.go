package aisearch

import (
	"sort"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// ---- catalog snapshot ----------------------------------------------------

// channel is a race-free, value-typed copy of the channel metadata the search
// tools need. Built once by BuildCatalog; read freely on the worker goroutine.
type channel struct {
	id          string
	name        string
	displayName string
	purpose     string
	header      string
	typ         model.ChannelType
	teamID      string
	dmPartner   string // resolved DM partner username, "" for non-DMs
}

// Catalog is the immutable snapshot the agent's tools resolve against: every
// channel the user can see plus the lookups needed to name them. Build it with
// BuildCatalog and hand it to Run/Ask; it is safe to read concurrently.
type Catalog struct {
	channels  []channel
	byID      map[string]channel
	teams     []*model.Team
	teamNames map[string]string // teamID → display name
	userNames map[string]string // userID → username (for author lines)
}

// BuildCatalog snapshots channel/team/user metadata into a form the worker
// goroutine can read without racing the caller. meID is the current user's id
// (used to pick the partner out of a DM channel's "id__id" name); channels is a
// flat list (callers flatten their own per-team maps); userNames maps user ids
// to usernames for author resolution and citation lines.
func BuildCatalog(meID string, teams []*model.Team, channels []*model.Channel, userNames map[string]string) Catalog {
	cat := Catalog{
		byID:      map[string]channel{},
		teamNames: map[string]string{},
		userNames: make(map[string]string, len(userNames)),
	}
	for id, name := range userNames {
		cat.userNames[id] = name
	}
	for _, t := range teams {
		cat.teams = append(cat.teams, t)
		cat.teamNames[t.Id] = displayTeam(t)
	}
	seen := map[string]struct{}{}
	for _, c := range channels {
		if _, dup := seen[c.Id]; dup {
			continue
		}
		seen[c.Id] = struct{}{}
		cc := channel{
			id:          c.Id,
			name:        c.Name,
			displayName: c.DisplayName,
			purpose:     c.Purpose,
			header:      c.Header,
			typ:         c.Type,
			teamID:      c.TeamId,
		}
		if c.Type == model.ChannelTypeDirect {
			for _, id := range strings.Split(c.Name, "__") {
				if id == "" || id == meID {
					continue
				}
				if n, ok := userNames[id]; ok && n != "" {
					cc.dmPartner = n
					break
				}
			}
		}
		cat.channels = append(cat.channels, cc)
		cat.byID[c.Id] = cc
	}
	return cat
}

// TeamNames returns the display names of the teams in the catalog, sorted, for
// orienting the agent in the system prompt ("Teams you can search: …").
func (cat Catalog) TeamNames() []string {
	var names []string
	for _, t := range cat.teams {
		if n := displayTeam(t); n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// displayTeam picks the most human name for a team (mirrors the UI helper of the
// same name, kept here so the package has no UI dependency).
func displayTeam(t *model.Team) string {
	if t.DisplayName != "" {
		return t.DisplayName
	}
	return t.Name
}

// label renders a channel for the model-facing tool text: "#general",
// "🔒private", "·group-dm", or "@partner".
func (c channel) label() string {
	switch c.typ {
	case model.ChannelTypeDirect:
		if c.dmPartner != "" {
			return "@" + c.dmPartner
		}
		return "@?"
	case model.ChannelTypeGroup:
		if c.displayName != "" {
			return "·" + c.displayName
		}
		return "·group"
	case model.ChannelTypePrivate:
		return "🔒" + c.slug()
	default:
		return "#" + c.slug()
	}
}

// slug picks the most human name for a public/private channel.
func (c channel) slug() string {
	if c.name != "" {
		return c.name
	}
	return c.displayName
}

// breadcrumb renders "Team › #channel" (or "DMs › @partner") for tool text.
func (cat Catalog) breadcrumb(channelID string) string {
	c, ok := cat.byID[channelID]
	if !ok {
		return "?"
	}
	if c.typ == model.ChannelTypeDirect || c.typ == model.ChannelTypeGroup {
		return "DMs › " + c.label()
	}
	if name := cat.teamNames[c.teamID]; name != "" {
		return name + " › " + c.label()
	}
	return c.label()
}

// resolveScope turns optional team/channel arguments into a channel-id scope
// for store.Search. requested reports whether any filter was asked for;
// matched reports whether it resolved to at least one channel (so the caller
// can fall back to a global search and tell the model when a name missed).
// Matching is exact (case-insensitive) first, then a substring fallback so a
// slightly-off name from the model still narrows usefully.
func (cat Catalog) resolveScope(team, channelArg string) (ids []string, requested, matched bool) {
	team = strings.TrimSpace(team)
	channelArg = normalizeChannelArg(channelArg)
	if team == "" && channelArg == "" {
		return nil, false, false
	}
	requested = true

	var teamIDs map[string]struct{}
	if team != "" {
		teamIDs = map[string]struct{}{}
		for _, t := range cat.teams {
			if strings.EqualFold(t.DisplayName, team) || strings.EqualFold(t.Name, team) {
				teamIDs[t.Id] = struct{}{}
			}
		}
		if len(teamIDs) == 0 {
			for _, t := range cat.teams {
				if containsFold(t.DisplayName, team) || containsFold(t.Name, team) {
					teamIDs[t.Id] = struct{}{}
				}
			}
		}
	}

	inTeam := func(c channel) bool {
		if teamIDs == nil {
			return true
		}
		_, ok := teamIDs[c.teamID]
		return ok
	}
	collect := func(pred func(channel) bool) []string {
		seen := map[string]struct{}{}
		var out []string
		for _, c := range cat.channels {
			if !inTeam(c) || !pred(c) {
				continue
			}
			if _, dup := seen[c.id]; dup {
				continue
			}
			seen[c.id] = struct{}{}
			out = append(out, c.id)
		}
		return out
	}

	switch {
	case channelArg == "":
		ids = collect(func(channel) bool { return true }) // team-only scope
	default:
		ids = collect(func(c channel) bool { return c.matchesExact(channelArg) })
		if len(ids) == 0 {
			ids = collect(func(c channel) bool { return c.matchesSub(channelArg) })
		}
	}
	return ids, requested, len(ids) > 0
}

// resolveAuthor maps a username (a leading "@" is tolerated) to user IDs:
// exact case-insensitive match first, else substring, so a slightly-off name
// still filters. Returns nil when nothing matches, so the caller can drop the
// filter and tell the model.
func (cat Catalog) resolveAuthor(name string) []string {
	name = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(name), "@"))
	if name == "" {
		return nil
	}
	var exact, sub []string
	for id, uname := range cat.userNames {
		switch {
		case strings.EqualFold(uname, name):
			exact = append(exact, id)
		case containsFold(uname, name):
			sub = append(sub, id)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return sub
}

func (c channel) matchesExact(q string) bool {
	return strings.EqualFold(c.name, q) ||
		strings.EqualFold(c.displayName, q) ||
		(c.dmPartner != "" && strings.EqualFold(c.dmPartner, q))
}

func (c channel) matchesSub(q string) bool {
	return containsFold(c.name, q) || containsFold(c.displayName, q) ||
		(c.dmPartner != "" && containsFold(c.dmPartner, q))
}

func containsFold(s, sub string) bool {
	return sub != "" && strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// normalizeChannelArg cleans a channel name the model passed: it may include
// a breadcrumb prefix ("Team › #chan"), a leading "#"/"🔒"/"·"/"@", or
// surrounding spaces. We keep only the last breadcrumb segment and strip the
// decorations so it can be matched against a bare channel name.
func normalizeChannelArg(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, sep := range []string{"›", "»", ">", "/"} {
		if i := strings.LastIndex(s, sep); i >= 0 {
			s = s[i+len(sep):]
		}
	}
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "#🔒·@ ")
	return strings.TrimSpace(s)
}
