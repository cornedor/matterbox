package aisearch

import (
	"sort"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// ---- people --------------------------------------------------------------

// Person is the searchable identity of one user: the username messages are
// attributed to, plus the real name and nickname a question is far more likely
// to use ("did Stijn Bernards …" resolves to @sbernards). Built by
// PeopleFromUsers; PeopleFromUsernames covers callers that only know usernames.
type Person struct {
	ID       string
	Username string
	FullName string // "First Last", empty when the server exposes neither
	Nickname string
}

// names returns every string this person can be addressed by, for matching.
func (p Person) names() []string {
	out := make([]string, 0, 3)
	for _, n := range []string{p.Username, p.FullName, p.Nickname} {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// PeopleFromUsers builds the people directory from full user records, so real
// names and nicknames are searchable.
func PeopleFromUsers(users []*model.User) map[string]Person {
	out := make(map[string]Person, len(users))
	for _, u := range users {
		if u == nil || u.Id == "" {
			continue
		}
		out[u.Id] = Person{
			ID:       u.Id,
			Username: u.Username,
			FullName: strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName)),
			Nickname: u.Nickname,
		}
	}
	return out
}

// PeopleFromUsernames builds a directory from a bare userID→username map, for
// callers that never fetched full profiles. Real-name matching is unavailable
// for these entries — they still resolve by username.
func PeopleFromUsernames(names map[string]string) map[string]Person {
	out := make(map[string]Person, len(names))
	for id, name := range names {
		if id == "" || name == "" {
			continue
		}
		out[id] = Person{ID: id, Username: name}
	}
	return out
}

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
	dmPartner   string   // resolved DM partner username, "" for non-DMs
	dmPartnerID string   // resolved DM partner user id, "" for non-DMs
	members     []string // group-DM member usernames; empty for every other type
	posts       int      // cached posts in this channel (0 until WithVolumes)
}

// isDM reports a one-to-one or group direct message — a conversation that
// belongs to no team, and so falls outside every team-scoped search.
func (c channel) isDM() bool {
	return c.typ == model.ChannelTypeDirect || c.typ == model.ChannelTypeGroup
}

// Catalog is the immutable snapshot the agent's tools resolve against: every
// channel the user can see plus the lookups needed to name them. Build it with
// BuildCatalog and hand it to Run/Ask; it is safe to read concurrently.
type Catalog struct {
	channels  []channel
	byID      map[string]channel
	teams     []*model.Team
	teamNames map[string]string // teamID → display name
	people    map[string]Person // userID → identity
	userNames map[string]string // userID → username (for author lines)
}

// BuildCatalog snapshots channel/team/user metadata into a form the worker
// goroutine can read without racing the caller. meID is the current user's id
// (used to pick the partner out of a DM channel's "id__id" name); channels is a
// flat list (callers flatten their own per-team maps); people is the directory
// used to name authors and to resolve a person named in a question — build it
// with PeopleFromUsers (real names included) or PeopleFromUsernames.
func BuildCatalog(meID string, teams []*model.Team, channels []*model.Channel, people map[string]Person) Catalog {
	cat := Catalog{
		byID:      map[string]channel{},
		teamNames: map[string]string{},
		people:    make(map[string]Person, len(people)),
		userNames: make(map[string]string, len(people)),
	}
	for id, p := range people {
		cat.people[id] = p
		if p.Username != "" {
			cat.userNames[id] = p.Username
		}
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
		switch c.Type {
		case model.ChannelTypeDirect:
			for _, id := range strings.Split(c.Name, "__") {
				if id == "" || id == meID {
					continue
				}
				if p, ok := cat.people[id]; ok && p.Username != "" {
					cc.dmPartner = p.Username
					cc.dmPartnerID = id
					break
				}
			}
		case model.ChannelTypeGroup:
			// A group DM's name is an opaque hash; its display name is the
			// member usernames, which is the only handle we get.
			for _, n := range strings.Split(c.DisplayName, ",") {
				if n = strings.TrimSpace(n); n != "" {
					cc.members = append(cc.members, n)
				}
			}
		}
		cat.channels = append(cat.channels, cc)
		cat.byID[c.Id] = cc
	}
	return cat
}

// WithVolumes returns a copy of the catalog with each channel's cached-post
// count attached (from store.ChannelPostCounts), so channel listings can be
// ranked by how much conversation actually lives there.
func (cat Catalog) WithVolumes(counts map[string]int) Catalog {
	if len(counts) == 0 {
		return cat
	}
	out := cat
	out.channels = make([]channel, len(cat.channels))
	copy(out.channels, cat.channels)
	out.byID = make(map[string]channel, len(cat.byID))
	for i := range out.channels {
		out.channels[i].posts = counts[out.channels[i].id]
		out.byID[out.channels[i].id] = out.channels[i]
	}
	return out
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
	if c.isDM() {
		return "DMs › " + c.label()
	}
	if name := cat.teamNames[c.teamID]; name != "" {
		return name + " › " + c.label()
	}
	return c.label()
}

// ---- resolution ----------------------------------------------------------

// resolvePeople maps a name from the question — a username, a real name, or a
// nickname, with or without a leading "@" — to the people it can mean. Exact
// matches (on any of the three) win outright; only when there are none does it
// fall back to substring, so "bram" doesn't drag in "bramvandenberg" when a
// @bram exists. Returns nil when nothing matches.
func (cat Catalog) resolvePeople(name string) []Person {
	name = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(name), "@"))
	if name == "" {
		return nil
	}
	var exact, sub []Person
	for _, p := range cat.people {
		hitExact, hitSub := false, false
		for _, n := range p.names() {
			switch {
			case strings.EqualFold(n, name):
				hitExact = true
			case containsFold(n, name):
				hitSub = true
			}
		}
		switch {
		case hitExact:
			exact = append(exact, p)
		case hitSub:
			sub = append(sub, p)
		}
	}
	out := exact
	if len(out) == 0 {
		out = sub
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// resolveAuthor maps a name to user IDs for an author filter. See resolvePeople
// for the matching rules.
func (cat Catalog) resolveAuthor(name string) []string {
	people := cat.resolvePeople(name)
	ids := make([]string, 0, len(people))
	for _, p := range people {
		ids = append(ids, p.ID)
	}
	return ids
}

// dmChannelsWith returns the direct and group-DM channels that involve any of
// the given people — the conversations a team scope can never reach, because a
// DM belongs to no team. This is what keeps "did X do Y for ACME" from
// searching ACME's channels alone and missing the DM where X actually said it.
func (cat Catalog) dmChannelsWith(people []Person) []string {
	if len(people) == 0 {
		return nil
	}
	byID := make(map[string]struct{}, len(people))
	byName := make(map[string]struct{}, len(people))
	for _, p := range people {
		byID[p.ID] = struct{}{}
		if p.Username != "" {
			byName[strings.ToLower(p.Username)] = struct{}{}
		}
	}
	var out []string
	for _, c := range cat.channels {
		if !c.isDM() {
			continue
		}
		hit := false
		if c.dmPartnerID != "" {
			_, hit = byID[c.dmPartnerID]
		}
		if !hit {
			for _, m := range c.members {
				if _, ok := byName[strings.ToLower(m)]; ok {
					hit = true
					break
				}
			}
		}
		if hit {
			out = append(out, c.id)
		}
	}
	return out
}

// resolveScope turns optional team/channel arguments into a channel-id scope
// for store.Search. requested reports whether any filter was asked for;
// matched reports whether it resolved to at least one channel (so the caller
// can fall back to a global search and tell the model when a name missed).
// Matching is exact (case-insensitive) first, then a substring fallback so a
// slightly-off name from the model still narrows usefully.
//
// A bare team name scopes to that team's own channels only — DMs belong to no
// team. Callers that also know a person should union in dmChannelsWith, or the
// scope silently excludes every direct message.
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

// teamMatches reports whether a filter substring names one of the teams, so
// list_channels can answer "zitmaxx" with the Zitmaxx team's channels even
// though no channel is called that.
func (cat Catalog) teamMatches(filter string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range cat.teams {
		if containsFold(t.DisplayName, filter) || containsFold(t.Name, filter) {
			out[t.Id] = struct{}{}
		}
	}
	return out
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
