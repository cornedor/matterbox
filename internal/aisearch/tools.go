package aisearch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/embed"
	"matterbox/internal/semindex"
	"matterbox/internal/store"
)

// aiSearchHitsPerCall is how many matches a single search_messages call feeds
// back to the model. Kept modest so tool results stay cheap in tokens — the
// per-slot context on the local server is the real budget. Results are the top
// of the fused ranking, so these are the best matches, not just the newest;
// 10 rather than 5 because in a known-item eval over the real cache the answer
// landed outside the top 5 but inside the top 10 often enough to matter.
const aiSearchHitsPerCall = 10

// aiSearchMaxTerms bounds how many keyword terms one search_messages call may
// OR together, keeping the FTS expression (and the trace line) sane.
const aiSearchMaxTerms = 16

// aiSearchMaxHits caps how many distinct messages we collect across the whole
// run to render as bubbles. Stays under searchPageSize so the load-more
// pseudo-row (which would re-run FTS on the question text) never appears.
const aiSearchMaxHits = 24

// aiSearchChannelListCap bounds list_channels output rows.
const aiSearchChannelListCap = 20

// aiSearchPeopleCap bounds find_people output rows.
const aiSearchPeopleCap = 8

// aiReadAroundContext is how many posts on each side of a hit read_around
// pulls from the cache.
const aiReadAroundContext = 3

// contextLines is how much surrounding context each search hit carries so the
// rendered bubbles match a normal search (the matched line is still all that's
// shown back to the model).
const contextLines = 2

// TraceStep is one rendered line of the live "working…" trace: which tool the
// agent invoked, the salient argument, an optional scope, and a short result
// summary ("3 hits", "0 hits", "2 channels"). Build the human label with
// Label() and the result summary with Result().
type TraceStep struct {
	tool    string // "search" | "read" | "channels" | "people"
	detail  string // the query + its keyword terms / the ref / the channel filter
	scope   string // channel or team the search was restricted to (optional)
	filters string // author / date narrowing applied (optional) — e.g. "by alice after 2026-01-01"
	result  string // human summary of what came back
}

// Label renders the human-readable description of the tool call (without the
// result summary), e.g. `search the new cms ~storyblok|cms in #frontend by alice`.
func (s TraceStep) Label() string {
	switch s.tool {
	case "search":
		label := "search " + s.detail
		if s.scope != "" {
			label += " in " + s.scope
		}
		if s.filters != "" {
			label += " " + s.filters
		}
		return label
	case "read":
		return "read context " + s.detail
	case "channels":
		if s.detail == "" {
			return "list channels"
		}
		return "list channels \"" + s.detail + "\""
	case "people":
		return "find people \"" + s.detail + "\""
	default:
		return s.tool
	}
}

// Result returns the short summary of what the call turned up (e.g. "3 hits").
func (s TraceStep) Result() string { return s.result }

// ---- tools ---------------------------------------------------------------

type tool struct {
	Type     string      `json:"type"`
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// toolDefs is the tool set offered to the model. The schemas double as the
// agent's instructions, so the descriptions carry the "how to use it" guidance
// a small model needs.
func toolDefs() []tool {
	return []tool{
		{Type: "function", Function: functionDef{
			Name: "search_messages",
			Description: "Search the chat archive. Always searches two ways at once and merges the rankings: by MEANING (your 'query', which matches paraphrases and synonyms, and works across languages — a query in one language finds messages written in another) and by WORD (your 'terms', where a message containing ANY term is a candidate). You do not choose a method; give both and the best matches come back first.\n" +
				"'query' is a short description of what you want. 'terms' are the words people would actually have typed — including jargon, product names, error text, and the same words in any other language this team writes in. Example: query \"the free gift is missing from the cart\", terms [\"free gift\",\"gift\",\"cart\",\"basket\"].\n" +
				"Results come back as: ref [Team › #channel] @author (date): text.\n" +
				"NAMES ARE FILTERS, NOT SEARCH WORDS. A person's name, and a client/project/team name, almost never appear inside the message text, so putting one in 'query' or 'terms' finds nothing. Pass a person as 'author' (any spelling — username, real name or nickname), a client or project as 'team', and a channel or a one-to-one conversation as 'channel'.\n" +
				"Most real work talk happens in direct messages, and a DM belongs to no team — so a 'team' scope cannot see any DM. When you name both an author and a team, this tool automatically also searches your DMs with that person, so combining them is safe. To read a whole conversation with someone (their side and yours), use channel:\"@username\" and no team.\n" +
				"The archive goes back years, so the best-matching message is often an old one. Leave 'sort' alone unless the question is about a current or recent situation — what someone is doing now, when they are back, whether something is still broken — in which case set sort:\"recent\".\n" +
				"If the hits are off-target, search again with different terms or a rephrased query — do not repeat the same call. Use 'offset' to page deeper into the same search (10 = next 10).",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"query":{"type":"string","description":"Short natural-language description of what you're looking for, e.g. \"complaints about slow checkout\" or \"the deploy broke and was rolled back\"."},` +
				`"terms":{"type":"array","items":{"type":"string"},"description":"Words likely to appear in the messages themselves, plus synonyms and their equivalents in any other language this team writes in, e.g. [\"checkout\",\"payment\",\"slow\",\"timeout\"]. A multi-word item is matched as an exact phrase."},` +
				`"channel":{"type":"string","description":"Optional: restrict to one conversation — a channel by bare name ('frontend'), or your direct messages with one person as '@username'."},` +
				`"team":{"type":"string","description":"Optional: restrict to one team (a client or project). Covers that team's channels only; DMs belong to no team."},` +
				`"author":{"type":"string","description":"Optional: only messages written by this person. Give the name however the question spells it — username, real name ('Stijn Bernards') or nickname. Searches everywhere they wrote, DMs included."},` +
				`"after":{"type":"string","description":"Optional lower date bound, YYYY-MM-DD (only messages on/after this day)."},` +
				`"before":{"type":"string","description":"Optional upper date bound, YYYY-MM-DD (only messages before this day)."},` +
				`"sort":{"type":"string","enum":["relevance","recent"],"description":"relevance (default, and almost always the right choice): best match first, from anywhere in the archive's history. recent: the same matches ordered newest first — only for questions about what is happening now or lately, since it buries older evidence."},` +
				`"offset":{"type":"integer","description":"Skip this many top results to page deeper into the same search (default 0; use 10, 20, …)."}` +
				`},"required":["query","terms"]}`),
		}},
		{Type: "function", Function: functionDef{
			Name:        "read_around",
			Description: "Read the messages surrounding one search hit, to confirm context before answering. Pass the ref shown at the start of a search-result line (e.g. \"m3\").",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"message":{"type":"string","description":"The mN ref from a search result line, e.g. \"m3\"."}` +
				`},"required":["message"]}`),
		}},
		{Type: "function", Function: functionDef{
			Name: "list_channels",
			Description: "Discover where a topic or a person's conversation lives. Matches the substring against channel names and purposes, TEAM names (so a client name finds that client's channels), and the people you have direct messages with. Busiest conversations first, with how many messages of each are cached.\n" +
				"Returns: Team › #channel — purpose (N messages). A line starting 'DMs › @someone' is your one-to-one conversation with them; search it with channel:\"@someone\".\n" +
				"Call it with an empty filter to see the teams and your biggest conversations. Use this when a search comes back empty or off-target, then search again scoped to a likely place.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"filter":{"type":"string","description":"Substring to match against channel names, purposes, team names and DM partners, e.g. 'cms', 'acme' or 'stijn'. Empty lists the teams and your busiest conversations."}` +
				`},"required":["filter"]}`),
		}},
		{Type: "function", Function: functionDef{
			Name: "find_people",
			Description: "Look up who a name in the question refers to. Give the name however it was written — a real name, a nickname, part of either, or a username — and get back the matching people with the username to pass as 'author', plus whether you have a direct-message conversation with them and how big it is.\n" +
				"Use this whenever the question names a person and you are not certain of their username, before searching.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"name":{"type":"string","description":"The person's name as the question spelled it, e.g. 'Stijn Bernards', 'stijn' or 'sbernards'."}` +
				`},"required":["name"]}`),
		}},
		{Type: "function", Function: functionDef{
			Name:        "finish",
			Description: "Call this when you have gathered enough to answer. Provide a one- or two-sentence answer for the user, naming the channel(s) where the information was found. If nothing relevant turned up, say so.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"answer":{"type":"string","description":"The final answer for the user, with channel citations."}` +
				`},"required":["answer"]}`),
		}},
	}
}

// Tools binds the tool implementations to their data: the snapshot catalog and
// the (concurrency-safe) store. refs maps the short message refs handed to the
// model in search results back to real posts, so read_around can resolve them;
// it is shared (pointer) across all tool calls in one run. Run builds this from
// a Config and Catalog — callers don't construct it directly.
type Tools struct {
	store   *store.Store
	catalog Catalog
	refs    *hitRefTable
	memo    *callMemo

	// Semantic-search support for search_messages' mode=semantic|hybrid. nil
	// embedClient (embeddings unconfigured) makes those modes fall back to
	// keyword. ctx bounds the embedding call (it's the agent run's context).
	embedClient *embed.Client
	embedModel  string
	embedDim    int
	ctx         context.Context
}

// callMemo remembers which tool calls a run has already made, so an identical
// repeat can be answered from the transcript instead of re-running. A small
// model that isn't sure what to do next tends to re-issue the call it just
// made; without this it burns the whole step budget on two or three distinct
// searches. Shared (pointer) across all tool calls in one run.
type callMemo struct{ seen map[string]int }

func newCallMemo() *callMemo { return &callMemo{seen: map[string]int{}} }

// mark records one call by signature and reports how many times it had already
// been made (0 the first time).
func (m *callMemo) mark(sig string) int {
	if m == nil {
		return 0
	}
	n := m.seen[sig]
	m.seen[sig] = n + 1
	return n
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

// execSearch runs search_messages. It returns the model-facing result text, a
// short trace step, and the full hits (with context windows) for the bubble
// view. Hits are fetched with context so the rendered bubbles match a normal
// search; only the matched line is shown back to the model.
func (t Tools) execSearch(args string) (string, TraceStep, []store.SearchHit) {
	var in struct {
		Query   string   `json:"query"` // natural-language description → semantic side
		Terms   []string `json:"terms"` // literal words → keyword side
		Channel string   `json:"channel"`
		Team    string   `json:"team"`
		Author  string   `json:"author"`
		After   string   `json:"after"`
		Before  string   `json:"before"`
		Sort    string   `json:"sort"` // "relevance" (default) | "recent"
		Offset  int      `json:"offset"`
		// Tolerated shapes from earlier tool schemas (and from models that
		// pattern-match on other search APIs), folded into query/terms rather
		// than dead-ending the call.
		AnyOf    []string `json:"any_of"`
		AllOf    []string `json:"all_of"`
		NoneOf   []string `json:"none_of"`
		Phrase   string   `json:"phrase"`
		Queries  []string `json:"queries"`
		Keywords string   `json:"keywords"`
		Text     string   `json:"text"`
	}
	_ = json.Unmarshal([]byte(args), &in)

	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > store.MatchCountCap {
		offset = store.MatchCountCap
	}

	// Fold every accepted spelling into the two inputs that matter.
	terms := cleanQueries(concatNonEmpty(in.Terms, in.AnyOf, in.AllOf, in.Queries,
		splitWords(in.Phrase), splitWords(in.Keywords)))
	qtext := strings.TrimSpace(in.Query)
	if qtext == "" {
		qtext = strings.TrimSpace(in.Text)
	}
	if qtext == "" {
		qtext = strings.Join(terms, " ")
	}
	if len(terms) == 0 {
		// No literal words given: fall back to the query's own content words so
		// the keyword ranker still contributes something.
		terms = contentTerms(qtext)
	}

	// Anything but an explicit "recent" keeps the default relevance ordering, so
	// a model that invents a sort value gets the safe one.
	order := store.SortRelevance
	if strings.EqualFold(strings.TrimSpace(in.Sort), "recent") {
		order = store.SortRecent
	}

	detail := qtext
	if len(terms) > 0 {
		detail += " ~" + strings.Join(terms, "|")
	}
	if order == store.SortRecent {
		detail += " (newest first)"
	}
	if offset > 0 {
		detail += fmt.Sprintf(" @%d", offset)
	}
	step := TraceStep{tool: "search", detail: detail}

	var note string
	if qtext == "" && len(terms) == 0 {
		step.result = "no query"
		return "Empty search. Give 'query' (a short description of what you're looking for) and 'terms' (words likely to appear in the messages).", step, nil
	}
	spec := store.SearchSpec{NoneOf: cleanQueries(in.NoneOf)}

	// The author is resolved first: a named person widens a place scope (see
	// below), so we need to know who they are before deciding what to search.
	var filters []string
	var people []Person
	if a := strings.TrimSpace(in.Author); a != "" {
		name := strings.TrimLeft(a, "@")
		people = t.catalog.resolvePeople(a)
		if len(people) > 0 {
			for _, p := range people {
				spec.AuthorIDs = append(spec.AuthorIDs, p.ID)
			}
			label := "by " + name
			// Say who a real name resolved to, so the trace (and the model)
			// can tell "by Stijn Bernards" actually filtered on @sbernards.
			if len(people) == 1 && !strings.EqualFold(people[0].Username, name) {
				label = "by @" + people[0].Username
			}
			filters = append(filters, label)
		} else {
			note += fmt.Sprintf("(nobody is called %q — ignored that filter. Call find_people to get the right name.)\n", a)
			filters = append(filters, "by "+name+" (no match)")
		}
	}

	scope, requested, matched := t.catalog.resolveScope(in.Team, in.Channel)
	if requested {
		step.scope = scopeLabel(in.Team, in.Channel)
		if matched {
			// A team holds no DMs, so scoping "did X do Y for ACME" to ACME
			// would drop the very conversation X most likely said it in. When
			// a person is named too, widen the scope to include the DMs with
			// them rather than silently searching half the archive.
			if dms := t.catalog.dmChannelsWith(people); len(dms) > 0 {
				before := len(scope)
				scope = unionIDs(scope, dms)
				if len(scope) > before {
					note += fmt.Sprintf("(a team holds no direct messages, so your DMs with %s were searched too)\n",
						personList(people))
					step.scope += " + DMs"
				}
			}
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
	if after, ok := parseSearchDate(in.After, false); ok {
		spec.After = after
		filters = append(filters, "after "+strings.TrimSpace(in.After))
	}
	if before, ok := parseSearchDate(in.Before, true); ok {
		spec.Before = before
		filters = append(filters, "before "+strings.TrimSpace(in.Before))
	}
	step.filters = strings.Join(filters, " ")

	// Checked here, once the trace line is fully built, so a suppressed repeat
	// still renders the same way in the live trace as the call it duplicates.
	// The signature covers everything that changes the result set — the label,
	// the scope, and the filters — so only a truly identical call is dropped.
	if t.memo.mark(step.Label()+"|"+step.filters) > 0 {
		step.result = "repeat"
		return "You already ran this exact search — its results are above. Answer from what you have, or search again with different 'terms' or a rephrased 'query'.", step, nil
	}

	if t.store == nil {
		step.result = "no store"
		return "The local message cache is unavailable, so search can't run.", step, nil
	}

	// One ranking, always: the semantic list (from 'query') and the keyword list
	// (from 'terms', OR'd so any term is a candidate) fused by RRF. Either side
	// may be missing — no embeddings configured, or no usable terms — and the
	// fuse degrades to whichever is left.
	var qvec []float32
	if t.embedClient != nil && qtext != "" {
		v, eerr := t.embedClient.EmbedOne(t.ctx, embed.QueryText(qtext))
		if eerr != nil {
			// Not fatal: the keyword half still works, so say so and carry on
			// rather than making the model retry a call it can't fix.
			note += "(the meaning-based half is unavailable — matched on words only)\n"
		} else {
			qvec = v
		}
	}
	fts := store.OrTerms(terms)
	if fts == "" && qvec == nil {
		step.result = "no query"
		return note + "Nothing to search on. Give 'query' (a short description) and 'terms' (words likely to appear in the messages).", step, nil
	}
	sc := store.HybridScope{ChannelIDs: spec.ChannelIDs, AuthorIDs: spec.AuthorIDs, After: spec.After, Before: spec.Before}
	hits, total, err := t.store.SearchFused(fts, qvec, semindex.ModelTag(t.embedModel, t.embedDim),
		sc, order, aiSearchHitsPerCall, offset, contextLines)
	if err != nil {
		step.result = "error"
		return "Search failed: " + err.Error(), step, nil
	}
	step.result = fmt.Sprintf("%d hits", len(hits))
	if len(hits) == 0 {
		if offset > 0 {
			return note + fmt.Sprintf("No more matches past offset %d. Go back to offset 0, or search for something else.", offset), step, nil
		}
		// The most valuable moment for the hint: the search came back empty
		// because it was pointed somewhere the answer isn't.
		empty := note + "0 matches. Try different 'terms' (synonyms, another language's word for it, a product or error name) or a rephrased 'query'. If you don't know where the topic lives, call list_channels and search that channel."
		if h := t.dmHint(fts, spec); h != "" {
			empty += "\n" + h
		}
		return empty, step, nil
	}

	// Window currently shown, and whether more pages remain after it.
	from, to := offset+1, offset+len(hits)
	more := to < total

	var b strings.Builder
	b.WriteString(note)
	if order == store.SortRecent {
		fmt.Fprintf(&b, "Matches %d–%d, newest first:\n", from, to)
	} else {
		fmt.Fprintf(&b, "Best matches %d–%d (strongest first):\n", from, to)
	}
	for _, h := range hits {
		if h.Match == nil {
			continue
		}
		b.WriteString(t.formatHit(h.Match))
		b.WriteByte('\n')
	}
	if more {
		fmt.Fprintf(&b, "(more available — pass offset:%d for the next page)\n", to)
	}
	if h := t.dmHint(fts, spec); h != "" {
		b.WriteString(h)
	}
	return strings.TrimRight(b.String(), "\n"), step, hits
}

// aiSearchDMHints is how many direct messages the "also in your DMs" line names.
const aiSearchDMHints = 3

// dmHintPool is how deep into the ranking the hint looks for direct messages.
// Ranked rather than counted on purpose: a raw match count per channel is
// dominated by whichever term is most common ("server"), so it points at the
// chattiest DM instead of the relevant one. bm25 weights the rare, discriminating
// term ("m2-graphql-proxy"), which is the whole point of the hint.
const dmHintPool = 150

// dmHint reports which of the reader's direct messages carry the same terms,
// so the agent can find the conversation an answer actually lives in. It is the
// counterweight to a place-scoped search: nearly half a real archive sits in
// DMs, which belong to no team and so are invisible to any 'team' scope — and
// even an unscoped search can bury a DM under channel chatter it out-ranks.
// Skipped when the search was already confined to DMs, or when there is nothing
// to point at.
func (t Tools) dmHint(fts string, spec store.SearchSpec) string {
	if t.store == nil || fts == "" {
		return ""
	}
	// Deliberately unscoped by channel: the point is to look where the search
	// itself could not. The author and date filters still apply, so the hint
	// describes the same question that was asked. No embedding vector and no
	// context windows — this is a cheap "where else" probe, not a second search.
	hits, _, err := t.store.SearchFused(fts, nil, "",
		store.HybridScope{AuthorIDs: spec.AuthorIDs, After: spec.After, Before: spec.Before},
		store.SortRelevance, dmHintPool, 0, 0)
	if err != nil {
		return ""
	}
	inScope := make(map[string]struct{}, len(spec.ChannelIDs))
	for _, id := range spec.ChannelIDs {
		inScope[id] = struct{}{}
	}
	counts := map[string]int{}
	var order []string
	for _, h := range hits {
		if h.Match == nil {
			continue
		}
		id := h.Match.ChannelId
		if _, dup := inScope[id]; dup {
			continue
		}
		c, ok := t.catalog.byID[id]
		if !ok || !c.isDM() {
			continue
		}
		if counts[id] == 0 {
			order = append(order, id) // first appearance = best rank
		}
		counts[id]++
	}
	var rows []string
	for _, id := range order {
		rows = append(rows, fmt.Sprintf("%s (%d)", t.catalog.byID[id].label(), counts[id]))
		if len(rows) >= aiSearchDMHints {
			break
		}
	}
	if len(rows) == 0 {
		return ""
	}
	return "Also in your direct messages, not searched above: " + strings.Join(rows, ", ") +
		". Search one with channel:\"" + firstLabel(rows) + "\"."
}

// firstLabel pulls the bare handle out of the first hint row ("@bram (37)" →
// "@bram"), for the worked example in the hint line.
func firstLabel(rows []string) string {
	if len(rows) == 0 {
		return ""
	}
	if i := strings.LastIndex(rows[0], " ("); i > 0 {
		return rows[0][:i]
	}
	return rows[0]
}

// unionIDs appends the ids in extra that a is missing, preserving a's order.
func unionIDs(a, extra []string) []string {
	seen := make(map[string]struct{}, len(a))
	for _, id := range a {
		seen[id] = struct{}{}
	}
	for _, id := range extra {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		a = append(a, id)
	}
	return a
}

// personList renders resolved people as "@alice and @bob" for a tool note.
func personList(people []Person) string {
	var names []string
	for _, p := range people {
		names = append(names, "@"+p.Username)
		if len(names) >= 3 {
			break
		}
	}
	switch len(names) {
	case 0:
		return "them"
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// scopeLabel renders the requested search scope for the trace, preferring the
// team: "Team › #channel" when both are given, the bare team name when only a
// team, and "#channel" when only a channel. A leading "#" marks a channel and
// a bare name is a team, so the line is unambiguous about what was searched in.
func scopeLabel(team, channelArg string) string {
	team = strings.TrimSpace(team)
	channelArg = normalizeChannelArg(channelArg)
	switch {
	case team != "" && channelArg != "":
		return team + " › #" + channelArg
	case channelArg != "":
		return "#" + channelArg
	default:
		return team
	}
}

// concatNonEmpty appends every list in order, skipping nils. Used to fold the
// several accepted spellings of "the words to match" into one list.
func concatNonEmpty(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// splitWords turns a free-text argument into a one-element list (or none when
// blank), so a string-shaped alias can join the terms list.
func splitWords(s string) []string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return []string{s}
}

// contentTerms derives keyword terms from a natural-language question, for when
// the model gave a 'query' but no 'terms'. A poor substitute for terms the model
// picked itself (it can only reuse the asker's words, never the jargon the
// messages actually use), but far better than leaving the keyword half empty.
//
// There is deliberately no stop-word list. Any such list is a bet on which
// languages the archive is written in, and the words are not even separable
// across languages — "die", "over" and "van" are ordinary English words as well
// as grammatical Dutch ones, so dropping them would silently break searches for
// whoever's archive this is. It isn't needed either: the terms are OR'd and
// ranked by bm25, which scores a term by how rare it is, so words that appear
// in most messages contribute almost nothing to the ranking on their own.
func contentTerms(q string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(q)) {
		f = strings.Trim(f, ".,?!:;\"'()[]")
		if len([]rune(f)) < 3 {
			continue
		}
		out = append(out, f)
	}
	return cleanQueries(out)
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
		if len(out) >= aiSearchMaxTerms {
			break
		}
	}
	return out
}

// formatHit renders one matched post as a compact single line for the model,
// prefixed with a short ref (when a ref table is present) so the model can
// pass it to read_around: `m3 [Team › #channel] @author (date): text`.
func (t Tools) formatHit(p *model.Post) string {
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
func (t Tools) formatThreadLine(p *model.Post) string {
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

// execReadAround runs read_around: it resolves the model's message ref to a
// cached post and returns a short transcript of the posts around it, with the
// pivot marked "›". Used to confirm a hit's context before answering.
func (t Tools) execReadAround(args string) (string, TraceStep) {
	var in struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal([]byte(args), &in)
	ref := strings.TrimSpace(in.Message)
	step := TraceStep{tool: "read", detail: ref}

	if t.refs == nil {
		step.result = "no refs"
		return "No messages have been searched yet — call search_messages first.", step
	}
	hr, ok := t.refs.lookup(ref)
	if !ok {
		step.result = "unknown ref"
		return fmt.Sprintf("Unknown message ref %q. Use one of the mN refs shown in search results.", ref), step
	}
	if t.memo.mark("read|"+ref) > 0 {
		step.result = "repeat"
		return fmt.Sprintf("You already read the context around %s — it's above. Move on: search for something else, or answer from what you have.", ref), step
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

// execListChannels runs list_channels: the conversations whose name, purpose,
// team or DM partner matches the filter substring, busiest first.
//
// Matching a TEAM name matters as much as matching a channel name — a client is
// usually a team, and its channels ("town-square", "devtalk") carry the client's
// name nowhere. So does matching a DM partner: a one-to-one channel is named
// "userid__userid" and has no display name, which used to make every direct
// message — nearly half the archive — invisible to this tool.
func (t Tools) execListChannels(args string) (string, TraceStep) {
	var in struct {
		Filter string `json:"filter"`
	}
	_ = json.Unmarshal([]byte(args), &in)
	filter := strings.TrimSpace(in.Filter)
	step := TraceStep{tool: "channels", detail: filter}

	teamIDs := t.catalog.teamMatches(filter)
	people := t.catalog.resolvePeople(filter)
	partners := make(map[string]struct{}, len(people))
	for _, p := range people {
		partners[p.ID] = struct{}{}
	}

	matches := func(c channel) bool {
		if filter == "" {
			return true
		}
		if containsFold(c.name, filter) || containsFold(c.displayName, filter) ||
			containsFold(c.purpose, filter) || containsFold(c.header, filter) {
			return true
		}
		if _, ok := teamIDs[c.teamID]; ok && c.teamID != "" {
			return true
		}
		if c.dmPartnerID != "" {
			if _, ok := partners[c.dmPartnerID]; ok {
				return true
			}
		}
		return containsFold(c.dmPartner, filter)
	}

	var hits []channel
	for _, c := range t.catalog.channels {
		if matches(c) {
			hits = append(hits, c)
		}
	}
	// Busiest first: a conversation with thousands of cached messages is a far
	// likelier home for a topic than an empty one, and the cap below means the
	// ordering decides what the model gets to see at all.
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].posts > hits[j].posts })

	var rows []string
	for _, c := range hits {
		row := t.catalog.breadcrumb(c.id)
		if p := strings.TrimSpace(c.purpose); p != "" {
			if len(p) > 60 {
				p = p[:60] + "…"
			}
			row += " — " + p
		}
		if c.posts > 0 {
			row += fmt.Sprintf(" (%d messages)", c.posts)
		}
		rows = append(rows, row)
		if len(rows) >= aiSearchChannelListCap {
			break
		}
	}
	step.result = fmt.Sprintf("%d channels", len(rows))
	if len(rows) == 0 {
		return fmt.Sprintf("Nothing matches %q — not a channel, a team, or someone you have direct messages with. Try a shorter substring, or find_people for a person.", filter), step
	}

	var b strings.Builder
	if filter == "" {
		b.WriteString("Teams: " + strings.Join(t.catalog.TeamNames(), ", ") + "\n")
		b.WriteString("Your busiest conversations:\n")
	}
	b.WriteString(strings.Join(rows, "\n"))
	if len(hits) > len(rows) {
		fmt.Fprintf(&b, "\n(%d more not shown — narrow the filter)", len(hits)-len(rows))
	}
	return b.String(), step
}

// execFindPeople runs find_people: who a name in the question refers to, the
// username to filter by, and whether there is a direct-message conversation
// with them worth searching. Without it the agent has no directory at all — it
// can only guess that "Stijn Bernards" is spelled @sbernards, and guessing
// wrong silently drops the author filter.
func (t Tools) execFindPeople(args string) (string, TraceStep) {
	var in struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(args), &in)
	name := strings.TrimSpace(in.Name)
	step := TraceStep{tool: "people", detail: name}

	people := t.catalog.resolvePeople(name)
	if len(people) == 0 {
		step.result = "0 people"
		return fmt.Sprintf("Nobody matches %q. Try just the first or last name.", name), step
	}

	// A DM's size is the useful signal: it tells the agent whether searching
	// that conversation is worth a call.
	dmOf := map[string]channel{}
	for _, c := range t.catalog.channels {
		if c.typ == model.ChannelTypeDirect && c.dmPartnerID != "" {
			dmOf[c.dmPartnerID] = c
		}
	}
	sort.SliceStable(people, func(i, j int) bool {
		return dmOf[people[i].ID].posts > dmOf[people[j].ID].posts
	})

	var rows []string
	for _, p := range people {
		row := "@" + p.Username
		if full := strings.TrimSpace(p.FullName); full != "" && !strings.EqualFold(full, p.Username) {
			row += " — " + full
		}
		if c, ok := dmOf[p.ID]; ok {
			if c.posts > 0 {
				row += fmt.Sprintf(" — direct messages: %d cached, search with channel:%q", c.posts, "@"+p.Username)
			} else {
				row += fmt.Sprintf(" — direct messages: search with channel:%q", "@"+p.Username)
			}
		} else {
			row += " — no direct messages with them"
		}
		rows = append(rows, row)
		if len(rows) >= aiSearchPeopleCap {
			break
		}
	}
	step.result = fmt.Sprintf("%d people", len(rows))
	out := strings.Join(rows, "\n")
	if len(people) > len(rows) {
		out += fmt.Sprintf("\n(%d more — give a fuller name)", len(people)-len(rows))
	}
	return out, step
}
