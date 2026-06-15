package aisearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/embed"
	"matterbox/internal/semindex"
	"matterbox/internal/store"
)

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

// aiBroadMatchHint is the match count above which a search is flagged as
// "broad" so the model is nudged to narrow rather than read 10 of hundreds.
const aiBroadMatchHint = 40

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
	tool    string // "search" | "read" | "channels"
	detail  string // keywords / filter
	scope   string // channel or team the search was restricted to (optional)
	filters string // author / date narrowing applied (optional) — e.g. "by alice after 2026-01-01"
	result  string // human summary of what came back
}

// Label renders the human-readable description of the tool call (without the
// result summary), e.g. `search ~storyblok|cms in #frontend by alice`.
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
			Description: "Search the local message archive. Pick a 'mode':\n" +
				"• keyword (default): exact-word full-text search. Best for specific tokens — names, error codes, ticket IDs, URLs, an exact phrase. Drive it with the any_of/all_of/phrase/none_of levers.\n" +
				"• semantic: matches by MEANING using sentence embeddings, so it finds messages that share NO words with your query — paraphrases, synonyms, and even other languages (e.g. an English query finds Dutch messages). It compares the EMBEDDING of your 'query' against the embedding of every message and returns the closest. Example: query \"payment provider\" surfaces \"paypal\", \"creditcard lukte niet\", \"PSP down\". Use it when you know the TOPIC or idea but not the exact words people used. Put a short natural-language description in 'query'; the keyword levers are ignored.\n" +
				"• hybrid: runs keyword AND semantic and fuses the rankings — keyword precision plus semantic recall. A strong default when you're unsure which words appear. Needs 'query' (and may also use the keyword levers).\n" +
				"Results are ranked by relevance and reported with a match count as: ref [Team › #channel] @author (date): text.\n" +
				"Keyword tuning: start broad with any_of (a post matching ANY term is a hit); if the count is large, NARROW with an all_of term, a phrase, or a none_of term; if 0, LOOSEN (drop a term, add synonyms). Semantic/hybrid tuning: rephrase 'query' or describe it differently; add a synonym; the result is recall-oriented so scan the top hits. " +
				"If matches look unrelated but more exist, set 'offset' to page into the SAME query (10 = next 10). " +
				"Do NOT search for a project/team/channel name — it lives in the channel title, not the messages; use 'channel'/'team' or list_channels. Multi-word any_of/all_of/none_of items are exact phrases.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"mode":{"type":"string","enum":["keyword","semantic","hybrid"],"description":"keyword (default): exact words. semantic: by meaning (needs 'query'; finds paraphrases/synonyms/other languages). hybrid: both fused (needs 'query'). Use semantic/hybrid when you know the topic but not the exact wording."},` +
				`"query":{"type":"string","description":"Natural-language description of what you're looking for, for mode semantic/hybrid, e.g. \"complaints about slow checkout\" or \"the deploy broke and was rolled back\"."},` +
				`"any_of":{"type":"array","items":{"type":"string"},"description":"KEYWORD mode: topic words + synonyms; a message matching at least one is a hit. The broad starting point, e.g. [\"storyblok\",\"contentful\",\"headless cms\"]."},` +
				`"all_of":{"type":"array","items":{"type":"string"},"description":"KEYWORD mode: words that must ALL appear. Add one to narrow a broad result, e.g. [\"migration\"]."},` +
				`"phrase":{"type":"string","description":"KEYWORD mode: an exact phrase that must appear, e.g. \"content management system\"."},` +
				`"none_of":{"type":"array","items":{"type":"string"},"description":"KEYWORD mode: exclude messages containing any of these words (denoise), e.g. [\"jira\"]."},` +
				`"channel":{"type":"string","description":"Optional channel name to restrict to (bare name like 'frontend'). Works in every mode."},` +
				`"team":{"type":"string","description":"Optional team name to restrict to. Works in every mode."},` +
				`"author":{"type":"string","description":"Optional username to restrict to (the person who wrote the message). Works in every mode."},` +
				`"after":{"type":"string","description":"Optional lower date bound, YYYY-MM-DD (only messages on/after this day)."},` +
				`"before":{"type":"string","description":"Optional upper date bound, YYYY-MM-DD (only messages before this day)."},` +
				`"offset":{"type":"integer","description":"Skip this many top results to page deeper into the same query (default 0; use 10, 20, … for further pages)."}` +
				`}}`),
		}},
		{Type: "function", Function: functionDef{
			Name:        "read_around",
			Description: "Read the messages surrounding one search hit, to confirm context before answering. Pass the ref shown at the start of a search-result line (e.g. \"m3\").",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"message":{"type":"string","description":"The mN ref from a search result line, e.g. \"m3\"."}` +
				`},"required":["message"]}`),
		}},
		{Type: "function", Function: functionDef{
			Name:        "list_channels",
			Description: "Discover where a topic might live by listing channels whose name or purpose matches a substring. Returns: Team › #channel — purpose. Use this when a keyword search comes back empty, then search again scoped to a likely channel.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"filter":{"type":"string","description":"Substring to match against channel names and purposes, e.g. 'cms' or 'design'."}` +
				`},"required":["filter"]}`),
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

	// Semantic-search support for search_messages' mode=semantic|hybrid. nil
	// embedClient (embeddings unconfigured) makes those modes fall back to
	// keyword. ctx bounds the embedding call (it's the agent run's context).
	embedClient *embed.Client
	embedModel  string
	embedDim    int
	ctx         context.Context
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
		Mode    string   `json:"mode"`  // keyword (default) | semantic | hybrid
		Query   string   `json:"query"` // natural-language query for semantic/hybrid
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

	// Mode picks the ranker: keyword (the structured FTS levers, default),
	// semantic (embedding cosine over a natural-language query), or hybrid
	// (both, rank-fused). semantic/hybrid need an embeddings client; without one
	// they degrade to keyword.
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode != "semantic" && mode != "hybrid" {
		mode = "keyword"
	}
	semantic := mode == "semantic" || mode == "hybrid"
	var note string
	if semantic && t.embedClient == nil {
		note += "(semantic search isn't configured — used keyword instead)\n"
		mode, semantic = "keyword", false
	}
	// Text to embed for semantic/hybrid: the explicit natural-language query, or
	// a best-effort fallback assembled from the keyword terms.
	qtext := strings.TrimSpace(in.Query)
	if qtext == "" {
		qtext = synthesizeQuery(spec)
	}

	detail := summarizeSpec(spec)
	if semantic {
		detail = mode + ": " + qtext
	}
	if offset > 0 {
		detail += fmt.Sprintf(" @%d", offset)
	}
	step := TraceStep{tool: "search", detail: detail}

	hasTerms := len(spec.AnyOf) > 0 || len(spec.AllOf) > 0 || len(spec.Phrases) > 0
	switch {
	case semantic && qtext == "":
		step.result = "no query"
		return "No query. For mode \"semantic\"/\"hybrid\", put a natural-language description in 'query'.", step, nil
	case !semantic && !hasTerms:
		step.result = "no terms"
		return "No search terms. Provide any_of (broad), all_of (required), and/or a phrase — or set mode:\"semantic\" with a 'query'.", step, nil
	}

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

	var hits []store.SearchHit
	var total int
	var err error
	if semantic {
		qvec, eerr := t.embedClient.EmbedOne(t.ctx, embed.QueryText(qtext))
		if eerr != nil {
			step.result = "embed error"
			return note + "Semantic search is unavailable (the embeddings server isn't responding). Retry with mode:\"keyword\".", step, nil
		}
		// Hybrid feeds the query to the keyword side too; pure semantic doesn't.
		kw := ""
		if mode == "hybrid" {
			kw = qtext
		}
		sc := store.HybridScope{ChannelIDs: spec.ChannelIDs, AuthorIDs: spec.AuthorIDs, After: spec.After, Before: spec.Before}
		tag := semindex.ModelTag(t.embedModel, t.embedDim)
		hits, total, err = t.store.SearchHybrid(kw, qvec, tag, sc, aiSearchHitsPerCall, offset, contextLines)
	} else {
		hits, total, err = t.store.SearchSpec(spec, aiSearchHitsPerCall, offset, contextLines)
	}
	if err != nil {
		step.result = "error"
		return "Search failed: " + err.Error(), step, nil
	}
	step.result = fmt.Sprintf("%d hits", len(hits))
	if len(hits) == 0 {
		if offset > 0 {
			return note + fmt.Sprintf("No more matches past offset %d (%s total). Go back to offset 0, or change the query.", offset, formatCount(total)), step, nil
		}
		if semantic {
			return note + "0 matches. Rephrase 'query' with different words, or try mode:\"keyword\"/\"hybrid\", or call list_channels to find where the topic lives and search there scoped.", step, nil
		}
		return note + "0 matches. Loosen the query: drop an all_of term or the phrase, broaden any_of with more synonyms, switch to mode:\"semantic\", or call list_channels to find where the topic lives and search there scoped.", step, nil
	}

	// Window currently shown, and whether more pages remain after it.
	from, to := offset+1, offset+len(hits)
	more := to < total

	var b strings.Builder
	b.WriteString(note)
	switch {
	case semantic:
		// Semantic results are the closest-by-meaning, already ranked — there's
		// no meaningful "match count" to be "broad" about (everything has some
		// similarity), so just present the top window.
		fmt.Fprintf(&b, "Top semantic matches %d–%d (most similar first):\n", from, to)
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

// synthesizeQuery builds a natural-language-ish query from a spec's positive FTS
// terms, for when the model asked for semantic/hybrid mode but didn't supply an
// explicit 'query'. Order: phrases, then all_of, then any_of.
func synthesizeQuery(spec store.SearchSpec) string {
	var parts []string
	parts = append(parts, spec.Phrases...)
	parts = append(parts, spec.AllOf...)
	parts = append(parts, spec.AnyOf...)
	return strings.TrimSpace(strings.Join(parts, " "))
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
func (t Tools) execListChannels(args string) (string, TraceStep) {
	var in struct {
		Filter string `json:"filter"`
	}
	_ = json.Unmarshal([]byte(args), &in)
	filter := strings.TrimSpace(in.Filter)
	step := TraceStep{tool: "channels", detail: filter}

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
