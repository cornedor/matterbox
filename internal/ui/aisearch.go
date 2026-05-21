package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
)

// AI search turns a natural-language question typed into the Search box
// (anything ending in "?") into an agentic loop: the model is given a small
// set of tools and drives them itself — searching the local message cache,
// discovering which channels a topic lives in, and narrowing down — until it
// can answer. The messages it surfaces are collected and rendered as the same
// clickable hit bubbles as a normal search, with the model's answer on top.
//
// The loop runs on a goroutine and reports back over a channel, mirroring the
// streaming pattern in summary.go. Tool execution must not touch live Model
// state from that goroutine, so the channel/team/user metadata it needs is
// snapshotted up front (searchCatalog); the SQLite store is concurrency-safe
// and shared directly.

// aiSearchHTTPTimeout is the fallback bound on the whole agentic loop (all tool
// rounds, not a single request) used when config.yaml sets no timeout_minutes.
// Generous because each round waits on a local model. See AISearchConfig.
const aiSearchHTTPTimeout = 4 * time.Minute

// aiSearchHitsPerCall is how many matches a single search_messages call feeds
// back to the model. Kept modest so tool results stay cheap in tokens — the
// per-slot context on the local server is the real budget. Results are
// relevance-ranked (bm25) across the union of queries, so these are the best
// matches, not just the newest.
const aiSearchHitsPerCall = 10

// aiSearchMaxQueries bounds how many alternative queries one search_messages
// call may OR together, keeping the FTS expression (and the trace line) sane.
const aiSearchMaxQueries = 16

// aiSearchMaxHits caps how many distinct messages we collect across the whole
// run to render as bubbles. Stays under searchPageSize so the load-more
// pseudo-row (which would re-run FTS on the question text) never appears.
const aiSearchMaxHits = 24

// aiSearchChannelListCap bounds list_channels output rows.
const aiSearchChannelListCap = 14

type aiSearchPhase int

const (
	aiSearchOff     aiSearchPhase = iota // not active
	aiSearchRunning                      // agent loop in flight
	aiSearchDone                         // final answer (or error) shown
)

// aiTraceStep is one rendered line of the live "working…" trace: which tool
// the agent invoked, the salient argument, an optional scope, and a short
// result summary ("3 hits", "0 hits", "2 channels").
type aiTraceStep struct {
	tool    string // "search" | "channels"
	detail  string // keywords / filter
	scope   string // channel or team the search was restricted to (optional)
	filters string // author / date narrowing applied (optional) — e.g. "by alice after 2026-01-01"
	result  string // human summary of what came back
}

// aiSearchState owns the agentic-search run on the Search tab. Only one runs
// at a time; starting a new one resets this wholesale. The collected hits are
// installed into m.search.hits when the run finishes, so the existing bubble
// navigation (up/down/enter) works on them unchanged.
type aiSearchState struct {
	phase     aiSearchPhase
	seq       int // bumps per run so stale goroutine messages are dropped
	query     string
	trace     []aiTraceStep
	answer    string
	tentative bool // answer is an unconfirmed best guess, not a confirmed one
	spinner   spinner.Model

	stream chan aiSearchUpdate
	cancel context.CancelFunc
	err    error
}

// newAISearchState builds a fresh, inactive state.
func newAISearchState() aiSearchState { return aiSearchState{} }

// active reports whether AI search is running or showing a result, i.e.
// whether it currently owns the Search viewport.
func (s aiSearchState) active() bool { return s.phase != aiSearchOff }

// ---- catalog snapshot ----------------------------------------------------

// catChannel is a race-free, value-typed copy of the channel metadata the
// search tools need. Built on the main thread; read freely on the worker.
type catChannel struct {
	id          string
	name        string
	displayName string
	purpose     string
	header      string
	typ         model.ChannelType
	teamID      string
	dmPartner   string // resolved DM partner username, "" for non-DMs
}

// searchCatalog is the immutable snapshot the agent's tools resolve against:
// every channel the user can see plus the lookups needed to name them.
type searchCatalog struct {
	channels  []catChannel
	byID      map[string]catChannel
	teams     []*model.Team
	teamNames map[string]string // teamID → display name
	userNames map[string]string // userID → username (for author lines)
}

// buildSearchCatalog snapshots the current channel/team/user metadata into a
// form the worker goroutine can read without racing the update loop.
func (m Model) buildSearchCatalog() searchCatalog {
	cat := searchCatalog{
		byID:      map[string]catChannel{},
		teamNames: map[string]string{},
		userNames: make(map[string]string, len(m.userNames)),
	}
	for id, name := range m.userNames {
		cat.userNames[id] = name
	}
	meID := ""
	if m.me != nil {
		meID = m.me.Id
	}
	for _, t := range m.teams {
		cat.teams = append(cat.teams, t)
		cat.teamNames[t.Id] = displayTeam(t)
	}
	seen := map[string]struct{}{}
	for _, list := range m.channels {
		for _, c := range list {
			if _, dup := seen[c.Id]; dup {
				continue
			}
			seen[c.Id] = struct{}{}
			cc := catChannel{
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
					if n, ok := m.userNames[id]; ok && n != "" {
						cc.dmPartner = n
						break
					}
				}
			}
			cat.channels = append(cat.channels, cc)
			cat.byID[c.Id] = cc
		}
	}
	return cat
}

// label renders a channel for the model-facing tool text: "#general",
// "🔒private", "·group-dm", or "@partner".
func (c catChannel) label() string {
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
func (c catChannel) slug() string {
	if c.name != "" {
		return c.name
	}
	return c.displayName
}

// breadcrumb renders "Team › #channel" (or "DMs › @partner") for tool text.
func (cat searchCatalog) breadcrumb(channelID string) string {
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
func (cat searchCatalog) resolveScope(team, channel string) (ids []string, requested, matched bool) {
	team = strings.TrimSpace(team)
	channel = normalizeChannelArg(channel)
	if team == "" && channel == "" {
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

	inTeam := func(c catChannel) bool {
		if teamIDs == nil {
			return true
		}
		_, ok := teamIDs[c.teamID]
		return ok
	}
	collect := func(pred func(catChannel) bool) []string {
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
	case channel == "":
		ids = collect(func(catChannel) bool { return true }) // team-only scope
	default:
		ids = collect(func(c catChannel) bool { return c.matchesExact(channel) })
		if len(ids) == 0 {
			ids = collect(func(c catChannel) bool { return c.matchesSub(channel) })
		}
	}
	return ids, requested, len(ids) > 0
}

// resolveAuthor maps a username (a leading "@" is tolerated) to user IDs:
// exact case-insensitive match first, else substring, so a slightly-off name
// still filters. Returns nil when nothing matches, so the caller can drop the
// filter and tell the model.
func (cat searchCatalog) resolveAuthor(name string) []string {
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

func (c catChannel) matchesExact(q string) bool {
	return strings.EqualFold(c.name, q) ||
		strings.EqualFold(c.displayName, q) ||
		(c.dmPartner != "" && strings.EqualFold(c.dmPartner, q))
}

func (c catChannel) matchesSub(q string) bool {
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

// ---- tools ---------------------------------------------------------------

type aiTool struct {
	Type     string        `json:"type"`
	Function aiFunctionDef `json:"function"`
}

type aiFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// aiSearchToolDefs is the tool set offered to the model. The schemas double
// as the agent's instructions, so the descriptions carry the "how to use it"
// guidance a small model needs.
func aiSearchToolDefs() []aiTool {
	return []aiTool{
		{Type: "function", Function: aiFunctionDef{
			Name: "search_messages",
			Description: "Keyword search over the local message archive (not semantic — use words you'd expect IN the messages). Results are ranked by relevance and recency and reported with a match count, as: ref [Team › #channel] @author (date): text. " +
				"Tune precision vs recall: start broad with 'any_of' (a post matching ANY of those words is a hit). If the count is large, NARROW by adding an 'all_of' term (every one must appear), a 'phrase' (exact wording), or a 'none_of' term (excluded). If you get 0, LOOSEN: drop an all_of/phrase term or add synonyms to any_of. " +
				"If the matches shown look unrelated but the count says there are more, set 'offset' to page further into the SAME query (offset 10 = the next 10) instead of guessing new terms. " +
				"Do NOT search for a project/team/channel name — it lives in the channel title, not the messages; use the 'channel'/'team' args or list_channels for that. Multi-word items in any_of/all_of/none_of are treated as exact phrases.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"any_of":{"type":"array","items":{"type":"string"},"description":"Topic words + synonyms; a message matching at least one is a hit. The broad starting point, e.g. [\"storyblok\",\"contentful\",\"headless cms\"]."},` +
				`"all_of":{"type":"array","items":{"type":"string"},"description":"Words that must ALL appear. Add one to narrow a broad result, e.g. [\"migration\"]."},` +
				`"phrase":{"type":"string","description":"An exact phrase that must appear, e.g. \"content management system\"."},` +
				`"none_of":{"type":"array","items":{"type":"string"},"description":"Exclude messages containing any of these words (denoise), e.g. [\"jira\"]."},` +
				`"channel":{"type":"string","description":"Optional channel name to restrict to (bare name like 'frontend')."},` +
				`"team":{"type":"string","description":"Optional team name to restrict to."},` +
				`"author":{"type":"string","description":"Optional username to restrict to (the person who wrote the message)."},` +
				`"after":{"type":"string","description":"Optional lower date bound, YYYY-MM-DD (only messages on/after this day)."},` +
				`"before":{"type":"string","description":"Optional upper date bound, YYYY-MM-DD (only messages before this day)."},` +
				`"offset":{"type":"integer","description":"Skip this many top results to page deeper into the same query (default 0; use 10, 20, … for further pages)."}` +
				`}}`),
		}},
		{Type: "function", Function: aiFunctionDef{
			Name:        "read_around",
			Description: "Read the messages surrounding one search hit, to confirm context before answering. Pass the ref shown at the start of a search-result line (e.g. \"m3\").",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"message":{"type":"string","description":"The mN ref from a search result line, e.g. \"m3\"."}` +
				`},"required":["message"]}`),
		}},
		{Type: "function", Function: aiFunctionDef{
			Name:        "list_channels",
			Description: "Discover where a topic might live by listing channels whose name or purpose matches a substring. Returns: Team › #channel — purpose. Use this when a keyword search comes back empty, then search again scoped to a likely channel.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"filter":{"type":"string","description":"Substring to match against channel names and purposes, e.g. 'cms' or 'design'."}` +
				`},"required":["filter"]}`),
		}},
		{Type: "function", Function: aiFunctionDef{
			Name:        "finish",
			Description: "Call this when you have gathered enough to answer. Provide a one- or two-sentence answer for the user, naming the channel(s) where the information was found. If nothing relevant turned up, say so.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"answer":{"type":"string","description":"The final answer for the user, with channel citations."}` +
				`},"required":["answer"]}`),
		}},
	}
}

// aiSearchTools binds the tool implementations to their data: the snapshot
// catalog and the (concurrency-safe) store. refs maps the short message refs
// handed to the model in search results back to real posts, so read_around
// can resolve them; it is shared (pointer) across all tool calls in one run.
type aiSearchTools struct {
	store   *store.Store
	catalog searchCatalog
	refs    *hitRefTable
}

// hitRef is the (channel, post) pair behind a short message ref.
type hitRef struct{ channelID, postID string }

// hitRefTable assigns stable short refs ("m1", "m2", …) to the posts surfaced
// in search results, so the model can name one for read_around without echoing
// a full 26-char id. The same post always maps to the same ref within a run.
type hitRefTable struct {
	byRef  map[string]hitRef
	byPost map[string]string
	n      int
}

func newHitRefTable() *hitRefTable {
	return &hitRefTable{byRef: map[string]hitRef{}, byPost: map[string]string{}}
}

// ref returns the post's ref, assigning a new one the first time it is seen.
func (h *hitRefTable) ref(channelID, postID string) string {
	if r, ok := h.byPost[postID]; ok {
		return r
	}
	h.n++
	r := fmt.Sprintf("m%d", h.n)
	h.byRef[r] = hitRef{channelID: channelID, postID: postID}
	h.byPost[postID] = r
	return r
}

// lookup resolves a ref (tolerating surrounding spaces) back to its post.
func (h *hitRefTable) lookup(ref string) (hitRef, bool) {
	r, ok := h.byRef[strings.TrimSpace(ref)]
	return r, ok
}

// aiBroadMatchHint is the match count above which a search is flagged as
// "broad" so the model is nudged to narrow rather than read 10 of hundreds.
const aiBroadMatchHint = 40

// execSearch runs search_messages. It returns the model-facing result text, a
// short trace step, and the full hits (with context windows) for the bubble
// view. Hits are fetched with context so the rendered bubbles match a normal
// search; only the matched line is shown back to the model.
func (t aiSearchTools) execSearch(args string) (string, aiTraceStep, []store.SearchHit) {
	var in struct {
		AnyOf   []string `json:"any_of"`
		AllOf   []string `json:"all_of"`
		Phrase  string   `json:"phrase"`
		NoneOf  []string `json:"none_of"`
		Channel string   `json:"channel"`
		Team    string   `json:"team"`
		Author  string   `json:"author"`
		After   string   `json:"after"`
		Before  string   `json:"before"`
		Offset  int      `json:"offset"`
		// Tolerated fallbacks for an older prompt that still emits these.
		Queries  []string `json:"queries"`
		Keywords string   `json:"keywords"`
	}
	_ = json.Unmarshal([]byte(args), &in)

	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > store.MatchCountCap {
		offset = store.MatchCountCap
	}

	spec := store.SearchSpec{
		AnyOf:  cleanQueries(in.AnyOf),
		AllOf:  cleanQueries(in.AllOf),
		NoneOf: cleanQueries(in.NoneOf),
	}
	if p := strings.TrimSpace(in.Phrase); p != "" {
		spec.Phrases = []string{p}
	}
	// Back-compat: an older prompt may still send queries/keywords. Fold them
	// into any_of so the call still works rather than dead-ending.
	if len(spec.AnyOf) == 0 && len(spec.AllOf) == 0 && len(spec.Phrases) == 0 {
		spec.AnyOf = cleanQueries(in.Queries)
		if len(spec.AnyOf) == 0 && strings.TrimSpace(in.Keywords) != "" {
			spec.AnyOf = []string{strings.TrimSpace(in.Keywords)}
		}
	}

	detail := summarizeSpec(spec)
	if offset > 0 {
		detail += fmt.Sprintf(" @%d", offset)
	}
	step := aiTraceStep{tool: "search", detail: detail}
	if len(spec.AnyOf) == 0 && len(spec.AllOf) == 0 && len(spec.Phrases) == 0 {
		step.result = "no terms"
		return "No search terms. Provide any_of (broad), all_of (required), and/or a phrase.", step, nil
	}

	var note string
	scope, requested, matched := t.catalog.resolveScope(in.Team, in.Channel)
	if requested {
		step.scope = scopeLabel(in.Team, in.Channel)
		if matched {
			spec.ChannelIDs = scope
		} else {
			// Fall back to a global search rather than dead-ending, and make
			// the trace say so instead of looking falsely scoped.
			miss := strings.TrimSpace(in.Channel)
			if miss == "" {
				miss = strings.TrimSpace(in.Team)
			}
			note += fmt.Sprintf("(no channel/team matched %q — searched everywhere instead)\n", miss)
			step.scope += " (no match → all)"
		}
	}
	var filters []string
	if a := strings.TrimSpace(in.Author); a != "" {
		name := strings.TrimLeft(a, "@")
		if ids := t.catalog.resolveAuthor(a); len(ids) > 0 {
			spec.AuthorIDs = ids
			filters = append(filters, "by "+name)
		} else {
			note += fmt.Sprintf("(no author matched %q — ignored that filter)\n", a)
			filters = append(filters, "by "+name+" (no match)")
		}
	}
	if after, ok := parseSearchDate(in.After, false); ok {
		spec.After = after
		filters = append(filters, "after "+strings.TrimSpace(in.After))
	}
	if before, ok := parseSearchDate(in.Before, true); ok {
		spec.Before = before
		filters = append(filters, "before "+strings.TrimSpace(in.Before))
	}
	step.filters = strings.Join(filters, " ")

	if t.store == nil {
		step.result = "no store"
		return "The local message cache is unavailable, so search can't run.", step, nil
	}
	hits, total, err := t.store.SearchSpec(spec, aiSearchHitsPerCall, offset, searchContextLines)
	if err != nil {
		step.result = "error"
		return "Search failed: " + err.Error(), step, nil
	}
	step.result = fmt.Sprintf("%d hits", len(hits))
	if len(hits) == 0 {
		if offset > 0 {
			return note + fmt.Sprintf("No more matches past offset %d (%s total). Go back to offset 0, or change the query.", offset, formatCount(total)), step, nil
		}
		return note + "0 matches. Loosen the query: drop an all_of term or the phrase, broaden any_of with more synonyms, or call list_channels to find where the topic lives and search there scoped.", step, nil
	}

	// Window currently shown, and whether more pages remain after it.
	from, to := offset+1, offset+len(hits)
	more := to < total

	var b strings.Builder
	b.WriteString(note)
	switch {
	case total > aiBroadMatchHint && offset == 0:
		fmt.Fprintf(&b, "%s matches — broad. Narrow with an all_of term, a phrase, or a none_of term, or page on with offset. Showing %d–%d:\n", formatCount(total), from, to)
	default:
		fmt.Fprintf(&b, "Showing matches %d–%d of %s (ranked by relevance + recency):\n", from, to, formatCount(total))
	}
	for _, h := range hits {
		if h.Match == nil {
			continue
		}
		b.WriteString(t.formatHit(h.Match))
		b.WriteByte('\n')
	}
	if more {
		fmt.Fprintf(&b, "(more available — pass offset:%d for the next page)", to)
	}
	return strings.TrimRight(b.String(), "\n"), step, hits
}

// formatCount renders a (possibly saturated) match count as "N" or "500+".
func formatCount(total int) string {
	if total >= store.MatchCountCap {
		return fmt.Sprintf("%d+", store.MatchCountCap)
	}
	return fmt.Sprintf("%d", total)
}

// scopeLabel renders the requested search scope for the trace, preferring the
// team: "Team › #channel" when both are given, the bare team name when only a
// team, and "#channel" when only a channel. A leading "#" marks a channel and
// a bare name is a team, so the line is unambiguous about what was searched in.
func scopeLabel(team, channel string) string {
	team = strings.TrimSpace(team)
	channel = normalizeChannelArg(channel)
	switch {
	case team != "" && channel != "":
		return team + " › #" + channel
	case channel != "":
		return "#" + channel
	default:
		return team
	}
}

// summarizeSpec renders a SearchSpec as a compact one-liner for the live
// trace, e.g. `~storyblok|contentful +cms "headless cms" -jira`.
func summarizeSpec(spec store.SearchSpec) string {
	var parts []string
	if len(spec.AnyOf) > 0 {
		parts = append(parts, "~"+strings.Join(spec.AnyOf, "|"))
	}
	for _, a := range spec.AllOf {
		parts = append(parts, "+"+a)
	}
	for _, p := range spec.Phrases {
		parts = append(parts, `"`+p+`"`)
	}
	for _, n := range spec.NoneOf {
		parts = append(parts, "-"+n)
	}
	return strings.Join(parts, " ")
}

// parseSearchDate parses a YYYY-MM-DD date (local zone) into a unix-ms bound.
// nextDay=true returns the start of the following day (the exclusive upper
// bound used for 'before'); otherwise the start of the given day (the
// inclusive lower bound used for 'after'). ok is false for blank/unparseable
// input, so a bad date simply drops the filter rather than failing the search.
func parseSearchDate(s string, nextDay bool) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	d, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return 0, false
	}
	if nextDay {
		d = d.AddDate(0, 0, 1)
	}
	return d.UnixMilli(), true
}

// cleanQueries trims, drops empties, de-duplicates (case-insensitively), and
// caps the model's query list so the OR expression and the trace line stay
// bounded.
func cleanQueries(qs []string) []string {
	seen := make(map[string]struct{}, len(qs))
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		key := strings.ToLower(q)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, q)
		if len(out) >= aiSearchMaxQueries {
			break
		}
	}
	return out
}

// formatHit renders one matched post as a compact single line for the model,
// prefixed with a short ref (when a ref table is present) so the model can
// pass it to read_around: `m3 [Team › #channel] @author (date): text`.
func (t aiSearchTools) formatHit(p *model.Post) string {
	author := t.catalog.userNames[p.UserId]
	if author == "" {
		author = shortAuthor(p.UserId)
	}
	text := strings.ReplaceAll(p.Message, "\n", " ")
	text = strings.TrimSpace(text)
	if len(text) > 140 {
		text = text[:140] + "…"
	}
	when := time.UnixMilli(p.CreateAt).Local().Format("Jan 2")
	line := fmt.Sprintf("[%s] @%s (%s): %s", t.catalog.breadcrumb(p.ChannelId), author, when, text)
	if t.refs != nil {
		line = t.refs.ref(p.ChannelId, p.Id) + " " + line
	}
	return line
}

// formatThreadLine renders one post in a read_around transcript: like
// formatHit but with a clock time and a longer text budget, and no ref prefix.
func (t aiSearchTools) formatThreadLine(p *model.Post) string {
	author := t.catalog.userNames[p.UserId]
	if author == "" {
		author = shortAuthor(p.UserId)
	}
	text := strings.TrimSpace(strings.ReplaceAll(p.Message, "\n", " "))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	when := time.UnixMilli(p.CreateAt).Local().Format("Jan 2 15:04")
	return fmt.Sprintf("@%s (%s): %s", author, when, text)
}

// aiReadAroundContext is how many posts on each side of a hit read_around
// pulls from the cache.
const aiReadAroundContext = 3

// execReadAround runs read_around: it resolves the model's message ref to a
// cached post and returns a short transcript of the posts around it, with the
// pivot marked "›". Used to confirm a hit's context before answering.
func (t aiSearchTools) execReadAround(args string) (string, aiTraceStep) {
	var in struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal([]byte(args), &in)
	ref := strings.TrimSpace(in.Message)
	step := aiTraceStep{tool: "read", detail: ref}

	if t.refs == nil {
		step.result = "no refs"
		return "No messages have been searched yet — call search_messages first.", step
	}
	hr, ok := t.refs.lookup(ref)
	if !ok {
		step.result = "unknown ref"
		return fmt.Sprintf("Unknown message ref %q. Use one of the mN refs shown in search results.", ref), step
	}
	if t.store == nil {
		step.result = "no store"
		return "The local message cache is unavailable.", step
	}
	posts, err := t.store.PostsAround(hr.channelID, hr.postID, aiReadAroundContext, aiReadAroundContext)
	if err != nil {
		step.result = "error"
		return "Read failed: " + err.Error(), step
	}
	if len(posts) == 0 {
		step.result = "not cached"
		return "That message isn't in the local cache, so its surrounding context is unavailable.", step
	}
	step.result = fmt.Sprintf("%d posts", len(posts))

	var b strings.Builder
	fmt.Fprintf(&b, "Context in %s:\n", t.catalog.breadcrumb(hr.channelID))
	for _, p := range posts {
		marker := "  "
		if p.Id == hr.postID {
			marker = "› "
		}
		b.WriteString(marker + t.formatThreadLine(p) + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), step
}

func shortAuthor(userID string) string {
	if len(userID) > 8 {
		return userID[:8]
	}
	if userID == "" {
		return "?"
	}
	return userID
}

// execListChannels runs list_channels: channels whose name, display name, or
// purpose contains the filter substring.
func (t aiSearchTools) execListChannels(args string) (string, aiTraceStep) {
	var in struct {
		Filter string `json:"filter"`
	}
	_ = json.Unmarshal([]byte(args), &in)
	filter := strings.TrimSpace(in.Filter)
	step := aiTraceStep{tool: "channels", detail: filter}

	var rows []string
	for _, c := range t.catalog.channels {
		if filter != "" &&
			!containsFold(c.name, filter) && !containsFold(c.displayName, filter) &&
			!containsFold(c.purpose, filter) && !containsFold(c.header, filter) {
			continue
		}
		row := t.catalog.breadcrumb(c.id)
		if p := strings.TrimSpace(c.purpose); p != "" {
			if len(p) > 60 {
				p = p[:60] + "…"
			}
			row += " — " + p
		}
		rows = append(rows, row)
		if len(rows) >= aiSearchChannelListCap {
			break
		}
	}
	step.result = fmt.Sprintf("%d channels", len(rows))
	if len(rows) == 0 {
		return fmt.Sprintf("No channels match %q. Try a shorter or different substring.", filter), step
	}
	return strings.Join(rows, "\n"), step
}

// ---- chat wire types -----------------------------------------------------

type aiToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type aiToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function aiToolCallFn `json:"function"`
}

type aiMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []aiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type aiChatRequest struct {
	Model       string      `json:"model"`
	Messages    []aiMessage `json:"messages"`
	Tools       []aiTool    `json:"tools,omitempty"`
	Temperature float64     `json:"temperature"`
	Stream      bool        `json:"stream"`
}

type aiChatResponse struct {
	Choices []struct {
		FinishReason string    `json:"finish_reason"`
		Message      aiMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// postChat sends one non-streaming chat-completions request with tools and
// returns the parsed response.
func postChat(ctx context.Context, endpoint, apiKey, mdl string, messages []aiMessage, tools []aiTool) (*aiChatResponse, error) {
	body := aiChatRequest{
		Model:       mdl,
		Messages:    messages,
		Tools:       tools,
		Temperature: 0.2,
		Stream:      false,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	url := chatCompletionsURL(endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, msg)
	}
	var out aiChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, errors.New(out.Error.Message)
	}
	return &out, nil
}

// ---- the agent loop ------------------------------------------------------

// aiSearchUpdate is one message from the worker goroutine: either a trace
// step, or a terminal result (answer + collected hits, or an error).
type aiSearchUpdate struct {
	step      aiTraceStep
	hasStep   bool
	done      bool
	answer    string
	hits      []store.SearchHit
	tentative bool // answer is an unconfirmed best guess (step budget ran out)
	err       error
}

// runAISearchLoop drives the bounded tool-call loop and pushes updates onto
// ch. It closes ch on return and stops early if ctx is cancelled.
func runAISearchLoop(ctx context.Context, endpoint, apiKey, mdl, system, query string, maxSteps int, tools aiSearchTools, ch chan<- aiSearchUpdate) {
	defer close(ch)
	send := func(u aiSearchUpdate) bool {
		select {
		case ch <- u:
			return true
		case <-ctx.Done():
			return false
		}
	}

	toolDefs := aiSearchToolDefs()
	messages := []aiMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: query},
	}
	var collected []store.SearchHit
	seenPost := map[string]struct{}{}
	addHits := func(hits []store.SearchHit) {
		for _, h := range hits {
			if h.Match == nil || len(collected) >= aiSearchMaxHits {
				continue
			}
			if _, dup := seenPost[h.Match.Id]; dup {
				continue
			}
			seenPost[h.Match.Id] = struct{}{}
			collected = append(collected, h)
		}
	}

	for step := 0; step < maxSteps; step++ {
		resp, err := postChat(ctx, endpoint, apiKey, mdl, messages, toolDefs)
		if err != nil {
			if ctx.Err() != nil {
				return // cancelled — the reader stopped caring
			}
			send(aiSearchUpdate{done: true, err: err, hits: collected})
			return
		}
		if len(resp.Choices) == 0 {
			send(aiSearchUpdate{done: true, err: errors.New("model returned no choices"), hits: collected})
			return
		}
		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			// The model answered in prose without calling finish — accept it.
			answer := strings.TrimSpace(msg.Content)
			if answer == "" {
				answer = "(the model returned an empty answer)"
			}
			send(aiSearchUpdate{done: true, answer: answer, hits: collected})
			return
		}

		// Echo the assistant turn (with its tool_calls) back into the history.
		messages = append(messages, aiMessage{Role: "assistant", Content: msg.Content, ToolCalls: msg.ToolCalls})

		for _, tc := range msg.ToolCalls {
			switch tc.Function.Name {
			case "finish":
				var fin struct {
					Answer string `json:"answer"`
				}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &fin)
				answer := strings.TrimSpace(fin.Answer)
				if answer == "" {
					answer = "(no answer text provided)"
				}
				send(aiSearchUpdate{done: true, answer: answer, hits: collected})
				return
			case "search_messages":
				result, ts, hits := tools.execSearch(tc.Function.Arguments)
				addHits(hits)
				if !send(aiSearchUpdate{step: ts, hasStep: true}) {
					return
				}
				messages = append(messages, aiMessage{Role: "tool", ToolCallID: tc.ID, Content: result})
			case "read_around":
				result, ts := tools.execReadAround(tc.Function.Arguments)
				if !send(aiSearchUpdate{step: ts, hasStep: true}) {
					return
				}
				messages = append(messages, aiMessage{Role: "tool", ToolCallID: tc.ID, Content: result})
			case "list_channels":
				result, ts := tools.execListChannels(tc.Function.Arguments)
				if !send(aiSearchUpdate{step: ts, hasStep: true}) {
					return
				}
				messages = append(messages, aiMessage{Role: "tool", ToolCallID: tc.ID, Content: result})
			default:
				messages = append(messages, aiMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    "Unknown tool. Use search_messages, read_around, list_channels, or finish.",
				})
			}
		}
	}

	// Hit the step cap without a finish. If we gathered anything, give the
	// model one last (tool-less) turn to commit to a best-guess answer from
	// what it has so far, flagged as unconfirmed — better than bailing with a
	// canned line when the evidence is probably enough. With nothing gathered
	// there's nothing to guess from, so keep the honest "ran out" note.
	if len(collected) > 0 {
		if guess, err := finalGuess(ctx, endpoint, apiKey, mdl, messages); err == nil {
			send(aiSearchUpdate{done: true, answer: guess, hits: collected, tentative: true})
			return
		} else if ctx.Err() != nil {
			return // cancelled while asking for the guess
		}
		send(aiSearchUpdate{done: true, hits: collected,
			answer: "I gathered some possibly-relevant messages but ran out of search steps before confirming an answer — see the matches below."})
		return
	}
	send(aiSearchUpdate{done: true, hits: collected,
		answer: "I ran out of search steps before reaching a confident answer."})
}

// aiFinalGuessNudge is the message appended when the step budget is spent: it
// tells the model it can't search again and asks it to commit to a best-effort
// answer from what it already found, while forbidding invention. The "✨ AI
// answer — best guess" banner header carries the uncertainty warning visually,
// so the prose itself doesn't need to hedge.
const aiFinalGuessNudge = "You've used all your search steps and cannot search again. " +
	"Using only what the searches above turned up, give the user your best-effort answer now, in one or two sentences, naming the channel(s) the evidence came from. " +
	"It's fine to be uncertain — say what the messages suggest even if you couldn't fully confirm it — but do not invent facts the messages don't support."

// finalGuess makes one last chat call with the tools withheld (so the model
// must reply in prose rather than searching again) to coax a best-effort answer
// out of what it has gathered. Returns an error if the call fails or comes back
// empty, so the caller can fall back to the canned note.
func finalGuess(ctx context.Context, endpoint, apiKey, mdl string, messages []aiMessage) (string, error) {
	msgs := append(messages[:len(messages):len(messages)], aiMessage{Role: "user", Content: aiFinalGuessNudge})
	resp, err := postChat(ctx, endpoint, apiKey, mdl, msgs, nil)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("model returned no choices")
	}
	answer := strings.TrimSpace(resp.Choices[0].Message.Content)
	if answer == "" {
		return "", errors.New("empty answer")
	}
	return answer, nil
}

// ---- bubbletea wiring ----------------------------------------------------

// startAISearch kicks off an agentic search for the given raw query (which
// still has its trailing "?"). Returns a Cmd that starts the worker, or sets
// a status and returns nil if prerequisites are missing.
func (m *Model) startAISearch(rawQuery string) tea.Cmd {
	query := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rawQuery), "?"))
	if query == "" {
		return nil
	}
	if m.store == nil {
		m.status = "AI search needs the local message cache"
		return nil
	}
	if strings.TrimSpace(m.summaryEndpoint) == "" || strings.TrimSpace(m.summaryModel) == "" {
		m.status = "AI search: no model endpoint configured"
		return nil
	}
	maxSteps := m.aiSearchMaxSteps
	if maxSteps <= 0 {
		maxSteps = 32
	}

	prev := m.aiSearch.seq
	m.aiSearch = newAISearchState()
	m.aiSearch.seq = prev + 1
	m.aiSearch.phase = aiSearchRunning
	m.aiSearch.query = query
	m.beginAISearchSpinner()

	// Clear any FTS results behind us so the trace owns the viewport.
	m.search.hits = nil
	m.search.idx = 0
	m.search.query = ""
	m.search.err = ""
	m.search.loading = false
	m.renderSearchResults()

	system := m.buildAISearchSystem()
	catalog := m.buildSearchCatalog()
	return tea.Batch(m.aiSearch.spinner.Tick, m.openAISearchCmd(m.aiSearch.seq, system, query, maxSteps, catalog))
}

// beginAISearchSpinner installs a fresh focused-colour dot spinner.
func (m *Model) beginAISearchSpinner() {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(focusedColor)
	m.aiSearch.spinner = sp
}

// buildAISearchSystem appends a tiny orientation (team names, current scope)
// to the configured agent prompt. We deliberately do NOT dump the channel
// catalog — the model discovers channels via list_channels, which keeps the
// prompt small for the local model's limited per-slot context.
func (m Model) buildAISearchSystem() string {
	system := m.aiSearchPrompt
	var teamNames []string
	for _, t := range m.teams {
		if n := displayTeam(t); n != "" {
			teamNames = append(teamNames, n)
		}
	}
	if len(teamNames) > 0 {
		system += "\n\nTeams you can search: " + strings.Join(teamNames, ", ") + "."
	}
	system += "\nToday is " + time.Now().Local().Format("Monday, January 2, 2006") + "."
	return system
}

// openAISearchCmd starts the worker goroutine and hands the UI the update
// channel + cancel handle.
func (m Model) openAISearchCmd(seq int, system, query string, maxSteps int, catalog searchCatalog) tea.Cmd {
	endpoint := m.summaryEndpoint
	apiKey := m.summaryAPIKey
	mdl := m.summaryModel
	st := m.store
	timeout := m.aiSearchTimeout
	if timeout <= 0 {
		timeout = aiSearchHTTPTimeout
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		ch := make(chan aiSearchUpdate, 8)
		tools := aiSearchTools{store: st, catalog: catalog, refs: newHitRefTable()}
		go runAISearchLoop(ctx, endpoint, apiKey, mdl, system, query, maxSteps, tools, ch)
		return aiSearchOpenedMsg{seq: seq, ch: ch, cancel: cancel}
	}
}

// waitAISearchUpdate blocks for the next worker update. A closed channel
// (goroutine returned without a terminal message) yields a done update.
func waitAISearchUpdate(seq int, ch <-chan aiSearchUpdate) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return aiSearchUpdateMsg{seq: seq, u: aiSearchUpdate{done: true}}
		}
		return aiSearchUpdateMsg{seq: seq, u: u}
	}
}

// applyAISearchOpened stores the channel + cancel and schedules the first
// read. A stale open is cancelled immediately.
func (m *Model) applyAISearchOpened(msg aiSearchOpenedMsg) tea.Cmd {
	if msg.seq != m.aiSearch.seq || m.aiSearch.phase != aiSearchRunning {
		msg.cancel()
		return nil
	}
	m.aiSearch.stream = msg.ch
	m.aiSearch.cancel = msg.cancel
	return waitAISearchUpdate(msg.seq, msg.ch)
}

// applyAISearchUpdate folds one worker update into the state: append a trace
// step and keep reading, or finalize on a terminal update.
func (m *Model) applyAISearchUpdate(msg aiSearchUpdateMsg) tea.Cmd {
	if msg.seq != m.aiSearch.seq || m.aiSearch.phase != aiSearchRunning {
		return nil
	}
	u := msg.u
	if u.hasStep {
		m.aiSearch.trace = append(m.aiSearch.trace, u.step)
		m.renderSearchResults()
		return waitAISearchUpdate(m.aiSearch.seq, m.aiSearch.stream)
	}
	// Terminal update.
	m.finishAISearch()
	m.aiSearch.answer = u.answer
	m.aiSearch.tentative = u.tentative
	m.aiSearch.err = u.err
	// Install the agent's hits as the search result set so the existing
	// bubble navigation (up/down/enter to jump to a message) works on them.
	m.search.hits = u.hits
	m.search.query = m.aiSearch.query
	m.search.idx = 0
	m.search.view.GotoTop()
	m.renderSearchResults()
	return nil
}

// finishAISearch releases the request and moves to the done phase.
func (m *Model) finishAISearch() {
	if m.aiSearch.cancel != nil {
		m.aiSearch.cancel()
		m.aiSearch.cancel = nil
	}
	m.aiSearch.stream = nil
	m.aiSearch.phase = aiSearchDone
}

// cancelAISearch tears down an in-flight or finished AI run and returns the
// Search tab to plain FTS. Safe to call when nothing is active.
func (m *Model) cancelAISearch() {
	if m.aiSearch.cancel != nil {
		m.aiSearch.cancel()
	}
	prev := m.aiSearch.seq
	m.aiSearch = newAISearchState()
	m.aiSearch.seq = prev + 1 // invalidate any in-flight goroutine messages
}

// ---- rendering -----------------------------------------------------------

// renderAIWorking draws the live trace into the Search viewport while the
// agent runs: a spinner header plus one line per tool call so far.
func (m *Model) renderAIWorking() string {
	dim := lipgloss.NewStyle().Foreground(dimColor)
	accent := lipgloss.NewStyle().Foreground(focusedColor)
	var lines []string
	lines = append(lines, accent.Render("✨ "+m.aiSearch.spinner.View())+" searching: "+
		lipgloss.NewStyle().Italic(true).Render(truncate(m.aiSearch.query, maxInt(10, m.search.view.Width()-16))))
	lines = append(lines, "")
	for _, s := range m.aiSearch.trace {
		var label string
		switch s.tool {
		case "search":
			label = "search " + s.detail
			if s.scope != "" {
				label += " in " + s.scope
			}
			if s.filters != "" {
				label += " " + s.filters
			}
		case "read":
			label = "read context " + s.detail
		case "channels":
			label = "list channels " + quoteForTrace(s.detail)
		default:
			label = s.tool
		}
		row := "  " + accent.Render("▸") + " " + label
		if s.result != "" {
			row += dim.Render("  → " + s.result)
		}
		// Don't truncate: the row carries ANSI styling (truncate is not
		// escape-aware), and the viewport soft-wraps long lines anyway.
		lines = append(lines, row)
	}
	lines = append(lines, "", dim.Render("  (esc to cancel)"))
	return strings.Join(lines, "\n")
}

func quoteForTrace(s string) string {
	if s == "" {
		return ""
	}
	return "\"" + s + "\""
}

// renderAIBanner builds the "✨ AI answer" box, sized to match a hit bubble's
// outer width so it stacks flush above the bubbles.
func (m *Model) renderAIBanner(innerW int) []string {
	outerW := innerW - 2
	if outerW < 8 {
		outerW = 8
	}
	inner := outerW - 2
	contentW := inner - 2
	if contentW < 1 {
		contentW = 1
	}

	header := "✨ AI answer"
	body := m.aiSearch.answer
	borderColor := focusedColor
	switch {
	case m.aiSearch.err != nil:
		header = "✨ AI search — error"
		body = m.aiSearch.err.Error()
		borderColor = lipgloss.Color("9") // red
	case m.aiSearch.tentative:
		// Ran out of search steps: the answer is a best guess, not confirmed.
		header = "✨ AI answer — best guess (unconfirmed)"
		borderColor = lipgloss.Color("11") // yellow
	}
	wrapped := lipgloss.NewStyle().Width(contentW).Render(strings.TrimSpace(body))
	bodyLines := strings.Split(wrapped, "\n")
	return strings.Split(bubbleBox(inner, header, bodyLines, borderColor), "\n")
}

// renderAIResults draws the finished AI run: the answer banner, then the
// collected messages as clickable hit bubbles (or a "found nothing" note).
func (m *Model) renderAIResults() {
	innerW := m.search.view.Width()
	if innerW < 10 {
		innerW = 10
	}
	banner := m.renderAIBanner(innerW)
	if m.aiSearch.err != nil {
		m.search.view.SetContent(strings.Join(banner, "\n"))
		m.search.view.GotoTop()
		return
	}
	if len(m.search.hits) == 0 {
		note := lipgloss.NewStyle().Foreground(dimColor).Render("  no matching messages to show")
		m.search.view.SetContent(strings.Join(append(banner, "", note), "\n"))
		m.search.view.GotoTop()
		return
	}
	if m.search.idx < 0 {
		m.search.idx = 0
	}
	if m.search.idx >= len(m.search.hits) {
		m.search.idx = len(m.search.hits) - 1
	}
	m.setBubbleViewport(append(banner, ""), m.search.hits, m.search.idx, false)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
