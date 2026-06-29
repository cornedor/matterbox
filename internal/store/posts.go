package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// SearchHit is one row of a Search result: the matched post plus a
// short window of surrounding posts from the same channel (oldest →
// newest). Before/After never include the match itself.
type SearchHit struct {
	Match  *model.Post
	Before []*model.Post
	After  []*model.Post
}

// ftsQuery turns a free-form user query into an FTS5 expression. Each
// whitespace-separated token becomes a prefix-matched quoted term, so
// `hello wor` matches `hello world`. Embedded double quotes in user
// input are escaped per FTS5 string rules. Returns "" for empty input.
func ftsQuery(input string) string {
	fields := strings.Fields(strings.TrimSpace(input))
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ReplaceAll(f, `"`, `""`)
		parts = append(parts, `"`+f+`"*`)
	}
	return strings.Join(parts, " ")
}

// ftsTerm renders one query item as a single FTS5 token. A one-word item
// becomes a prefix term (`"word"*`, so `wor` matches `world`); a multi-word
// item becomes an exact phrase (`"two words"`, no prefix) so that e.g.
// `"headless cms"` matches the phrase rather than two independently-OR'd
// words. Embedded double quotes are escaped per FTS5 string rules. Returns
// "" for blank input.
func ftsTerm(item string) string {
	fields := strings.Fields(strings.TrimSpace(item))
	switch len(fields) {
	case 0:
		return ""
	case 1:
		return `"` + strings.ReplaceAll(fields[0], `"`, `""`) + `"*`
	default:
		phrase := strings.ReplaceAll(strings.Join(fields, " "), `"`, `""`)
		return `"` + phrase + `"`
	}
}

// orGroup renders items as a parenthesised OR of their terms (`("a"* OR
// "b"*)`), a bare term when only one survives, or "" when none do.
func orGroup(items []string) string {
	var terms []string
	for _, it := range items {
		if t := ftsTerm(it); t != "" {
			terms = append(terms, t)
		}
	}
	switch len(terms) {
	case 0:
		return ""
	case 1:
		return terms[0]
	default:
		return "(" + strings.Join(terms, " OR ") + ")"
	}
}

// ftsSpec compiles a SearchSpec into one fully-parenthesised FTS5 expression:
//
//	(phrase AND all-of-terms… AND (any-of OR-group)) NOT (none-of OR-group)
//
// Positive clauses (phrases, all_of, the any_of group) are AND'd, so each is
// required — adding one narrows the result. any_of contributes a single
// OR-group: at least one of its members must match, which is the high-recall
// starting point. none_of excludes any post containing one of its terms, but
// is dropped when there is no positive clause (FTS5 NOT needs a left operand,
// and "everything except X" is not a useful search). The query is ranked by
// bm25 elsewhere, so the strongest matches still surface first. Returns ""
// when no positive term remains, which the caller treats as "no query".
func ftsSpec(spec SearchSpec) string {
	var positive []string
	for _, p := range spec.Phrases {
		if t := ftsTerm(p); t != "" {
			positive = append(positive, t)
		}
	}
	for _, a := range spec.AllOf {
		if t := ftsTerm(a); t != "" {
			positive = append(positive, t)
		}
	}
	if g := orGroup(spec.AnyOf); g != "" {
		positive = append(positive, g)
	}
	if len(positive) == 0 {
		return ""
	}
	expr := strings.Join(positive, " AND ")
	if neg := orGroup(spec.NoneOf); neg != "" {
		expr = "(" + expr + ") NOT " + neg
	}
	return expr
}

// Search runs an FTS5 query against the persisted message corpus and
// returns up to limit hits, ranked by relevance blended with an age decay
// (see rankByRelevanceAndAge) so recent matches lead but a strong older one
// still surfaces. For each hit, contextN posts before and after (in the same
// channel, ordered oldest→newest) are included so the caller can render the
// match in context. Returns nil with no error for an empty or all-whitespace
// query.
//
// channelIDs is an optional scope: nil = search everywhere; non-nil
// empty slice = "filter active but resolved to no channels" = no hits;
// non-empty = restrict to channel_id IN (...). The caller (UI layer)
// uses this to implement team:/in: modifiers, which it resolves against
// its local channel metadata before issuing the query.
func (s *Store) Search(query string, channelIDs []string, limit, contextN int) ([]SearchHit, error) {
	hits, _, err := s.searchFTS(ftsQuery(query), searchFilter{channelIDs: channelIDs}, limit, 0, contextN, true)
	return hits, err
}

// SearchSpec is a structured query that gives the caller precision/recall
// control instead of a single keyword list. Its FTS5 levers (AllOf / AnyOf /
// Phrases / NoneOf, compiled by ftsSpec) decide which posts match; the
// metadata fields (ChannelIDs / AuthorIDs / After / Before) narrow that set
// without touching the FTS index. The zero value matches nothing.
type SearchSpec struct {
	AllOf   []string // every item must appear (AND)      — precision
	AnyOf   []string // at least one item must appear (OR) — recall
	Phrases []string // exact phrase(s) that must appear
	NoneOf  []string // posts containing any are excluded (NOT)

	ChannelIDs []string // restrict to these channels (see Search for nil vs empty)
	AuthorIDs  []string // restrict to these post authors
	After      int64    // inclusive lower bound on create_at (unix-ms); 0 = none
	Before     int64    // exclusive upper bound on create_at (unix-ms); 0 = none
}

// SearchSpec runs a structured query ranked by a blend of relevance (bm25) and
// recency (see rankByRelevanceAndAge). It is the high-recall-but-tunable
// path used by AI search: the agent starts broad with AnyOf and narrows with
// AllOf / Phrases / NoneOf / metadata filters as the match count tells it to.
// offset skips that many top-ranked hits so the caller can page past results
// it has already seen. Returns the hits (up to limit, each with a contextN
// window) plus total — the number of matching posts, saturated at
// MatchCountCap — so the caller can tell a broad query from a tight one and
// know whether more pages remain. limit / contextN behave as in Search.
func (s *Store) SearchSpec(spec SearchSpec, limit, offset, contextN int) (hits []SearchHit, total int, err error) {
	f := searchFilter{
		channelIDs: spec.ChannelIDs,
		authorIDs:  spec.AuthorIDs,
		after:      spec.After,
		before:     spec.Before,
	}
	return s.searchFTS(ftsSpec(spec), f, limit, offset, contextN, true)
}

// MatchCountCap bounds the total match count returned by searchFTS: counting
// stops here so an enormous result set never forces a full-table scan, and the
// caller renders a saturated total as "MatchCountCap+".
const MatchCountCap = 500

// searchFilter narrows an FTS match by post metadata, applied as SQL WHERE
// clauses on the posts table (not the FTS index). The zero value adds no
// filter. channelIDs / authorIDs carry the nil-vs-empty distinction from
// Search: nil = "no scope", non-nil-empty = "scope resolved to nothing" = no
// hits.
type searchFilter struct {
	channelIDs []string
	authorIDs  []string
	after      int64
	before     int64
}

// scopedOut reports a filter that can match nothing without touching SQLite:
// an explicitly empty (non-nil) channel or author scope.
func (f searchFilter) scopedOut() bool {
	return (f.channelIDs != nil && len(f.channelIDs) == 0) ||
		(f.authorIDs != nil && len(f.authorIDs) == 0)
}

// where appends the metadata predicates (beyond the FTS MATCH and the
// delete_at guard) to b and their bind values to args, returning the extended
// args. Shared by the hit query and the count query so the two always agree.
func (f searchFilter) where(b *strings.Builder, args []any) []any {
	if len(f.channelIDs) > 0 {
		b.WriteString("\n  AND p.channel_id IN (" + inPlaceholders(len(f.channelIDs)) + ")")
		for _, id := range f.channelIDs {
			args = append(args, id)
		}
	}
	if len(f.authorIDs) > 0 {
		b.WriteString("\n  AND p.user_id IN (" + inPlaceholders(len(f.authorIDs)) + ")")
		for _, id := range f.authorIDs {
			args = append(args, id)
		}
	}
	if f.after > 0 {
		b.WriteString("\n  AND p.create_at >= ?")
		args = append(args, f.after)
	}
	if f.before > 0 {
		b.WriteString("\n  AND p.create_at < ?")
		args = append(args, f.before)
	}
	return args
}

// inPlaceholders returns "?,?,…" with n placeholders for an IN (...) clause.
func inPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// searchFTS is the shared engine behind Search and SearchSpec: it runs a
// prebuilt FTS5 MATCH expression, optionally narrowed by f, pages by offset,
// and attaches a context window to each returned hit. byRank blends relevance
// and recency (rankByRelevanceAndAge) instead of the default
// most-recent-first ordering. total is the match count capped at
// MatchCountCap. An empty expression, a non-positive limit, or a scoped-out
// filter all yield no hits without touching SQLite.
func (s *Store) searchFTS(fts string, f searchFilter, limit, offset, contextN int, byRank bool) (hits []SearchHit, total int, err error) {
	if s == nil || limit <= 0 || fts == "" || f.scopedOut() {
		return nil, 0, nil
	}
	if offset < 0 {
		offset = 0
	}

	// Count first (capped) so the caller can report how broad the match is.
	total, err = s.countFTS(fts, f)
	if err != nil {
		return nil, 0, err
	}

	var b strings.Builder
	if byRank {
		// Rank on metadata only — create_at (recency decay) and bm25 — plus the
		// channel_id the context window needs. The full body is loaded later for
		// just the paged winners, so a broad query no longer unmarshals the whole
		// ~rankPoolSize candidate pool only to discard all but `limit` of it.
		b.WriteString("\nSELECT p.id, p.channel_id, p.create_at, bm25(posts_fts)")
	} else {
		b.WriteString("\nSELECT p.raw_json")
	}
	b.WriteString(`
FROM posts_fts
JOIN posts p ON p.rowid = posts_fts.rowid
WHERE posts_fts MATCH ?
  AND p.delete_at = 0`)
	args := f.where(&b, []any{fts})
	if byRank {
		// Pull a candidate pool by raw relevance, then re-rank it (with
		// recency) and page in Go — so a recent, slightly-weaker match can
		// surface above an older, stronger one. The pool spans offset so paging
		// stays within one blended ordering.
		b.WriteString("\nORDER BY bm25(posts_fts)") // smaller is more relevant
		b.WriteString("\nLIMIT ?")
		args = append(args, rankPoolSize(limit, offset))
	} else {
		b.WriteString("\nORDER BY p.create_at DESC")
		b.WriteString("\nLIMIT ? OFFSET ?")
		args = append(args, limit, offset)
	}

	rows, err := s.db.Query(b.String(), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query search: %w", err)
	}
	defer rows.Close()
	var pool []scoredPost
	for rows.Next() {
		if byRank {
			// Lightweight stub: enough to rank, page, and locate the context
			// window; the body is filled in below for the winners only.
			var stub model.Post
			var rank float64
			if err := rows.Scan(&stub.Id, &stub.ChannelId, &stub.CreateAt, &rank); err != nil {
				return nil, 0, fmt.Errorf("scan search: %w", err)
			}
			pool = append(pool, scoredPost{post: &stub, bm25: rank})
			continue
		}
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, fmt.Errorf("scan search: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		pool = append(pool, scoredPost{post: &p})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("rows: %w", err)
	}

	var matches []*model.Post
	if byRank {
		// Rank + page the stubs, then load full bodies for only the page shown.
		ranked := page(rankByRelevanceAndAge(pool, time.Now().UnixMilli(), s.recencyHalfLife), offset, limit)
		ids := make([]string, len(ranked))
		for i, p := range ranked {
			ids[i] = p.Id
		}
		full, err := s.postsByIDs(ids)
		if err != nil {
			return nil, 0, err
		}
		matches = make([]*model.Post, 0, len(ranked))
		for _, stub := range ranked {
			if p := full[stub.Id]; p != nil { // absent only if deleted since the pool scan
				matches = append(matches, p)
			}
		}
	} else {
		matches = make([]*model.Post, len(pool))
		for i, sp := range pool {
			matches[i] = sp.post
		}
	}

	out := make([]SearchHit, 0, len(matches))
	for _, mp := range matches {
		before, after, _ := s.contextWindow(mp.ChannelId, mp.CreateAt, contextN)
		out = append(out, SearchHit{Match: mp, Before: before, After: after})
	}
	return out, total, nil
}

// page returns posts[offset:offset+limit], clamped to the slice bounds.
func page(posts []*model.Post, offset, limit int) []*model.Post {
	if offset >= len(posts) {
		return nil
	}
	end := offset + limit
	if end > len(posts) {
		end = len(posts)
	}
	return posts[offset:end]
}

// scoredPost couples a matched post with its raw bm25 score (more negative is
// more relevant) for the blended re-ranking.
type scoredPost struct {
	post *model.Post
	bm25 float64
}

const (
	// relevanceDamp shapes how a candidate's relevance weight falls off with its
	// bm25 rank: weight = 1/(relevanceDamp+rank), so the top hit scores 1.0 and
	// the leading hits are widely separated while the long tail compresses. That
	// weight is then scaled by the age decay (see rankByRelevanceAndAge), so a
	// result has to be both relevant and recent to rank high. A larger value
	// flattens the relevance differences (letting recency dominate sooner).
	relevanceDamp = 1.0

	// rankPoolBase is the candidate pool re-ranked per page: large enough that
	// a recent, only-moderately-relevant message can be lifted into view, but
	// capped so ranking stays cheap and recent noise can't flood in.
	rankPoolBase = 300
)

// rankPoolSize is how many bm25-ordered candidates to pull for one page: a
// base pool big enough for recency fusion to matter, plus offset so paging
// stays within a single fused ordering.
func rankPoolSize(limit, offset int) int {
	base := rankPoolBase
	if limit > base {
		base = limit
	}
	return offset + base
}

// rankByRelevanceAndAge re-ranks a bm25-ordered candidate pool by relevance
// scaled by an absolute age decay, so recent matches outrank stale ones unless
// an older message is markedly more relevant. The input is assumed already
// sorted best-bm25-first, so a candidate's index IS its relevance rank, giving
// it a weight of 1/(relevanceDamp+rank) — using the rank (not the raw bm25)
// keeps an outlier score (e.g. a code snippet that repeats a term) from
// dominating. That weight is multiplied by a half-life decay of the message's
// age measured from now (unix-ms): the weight halves for every halfLife of age.
// Because the decay is a function of absolute age, a three-month-old message is
// discounted the same regardless of how many other matches exist — which is
// what "older chat may be out of date" calls for, unlike a relative recency
// rank. A non-positive halfLife disables the decay (pure relevance order).
// Ties break newest-first.
func rankByRelevanceAndAge(pool []scoredPost, now int64, halfLife time.Duration) []*model.Post {
	if len(pool) <= 1 {
		out := make([]*model.Post, len(pool))
		for i, m := range pool {
			out[i] = m.post
		}
		return out
	}
	hlMs := halfLife.Milliseconds()
	decay := func(createAt int64) float64 {
		if hlMs <= 0 {
			return 1 // decay disabled → rank by relevance alone
		}
		ageMs := now - createAt
		if ageMs <= 0 {
			return 1 // future/just-now timestamps get no penalty
		}
		return math.Exp2(-float64(ageMs) / float64(hlMs))
	}
	// Precompute each candidate's blended score once. The comparator below runs
	// O(n log n) times, and evaluating math.Exp2 inside it (the decay) dominated
	// the sort; a candidate's index is its bm25 rank (pool is best-first), so its
	// relevance weight is the fixed 1/(relevanceDamp+rank).
	scores := make([]float64, len(pool))
	for i := range pool {
		scores[i] = decay(pool[i].post.CreateAt) / (relevanceDamp + float64(i))
	}
	order := make([]int, len(pool))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if scores[ia] != scores[ib] {
			return scores[ia] > scores[ib]
		}
		return pool[ia].post.CreateAt > pool[ib].post.CreateAt
	})
	out := make([]*model.Post, len(pool))
	for i, idx := range order {
		out[i] = pool[idx].post
	}
	return out
}

// countFTS returns the number of posts matching fts (after f), saturated at
// MatchCountCap via a LIMIT'd subquery so a huge corpus can't turn this into a
// full scan. It runs the same MATCH + WHERE as searchFTS so the two agree.
func (s *Store) countFTS(fts string, f searchFilter) (int, error) {
	var b strings.Builder
	b.WriteString(`
SELECT count(*) FROM (
  SELECT 1
  FROM posts_fts
  JOIN posts p ON p.rowid = posts_fts.rowid
  WHERE posts_fts MATCH ?
    AND p.delete_at = 0`)
	args := f.where(&b, []any{fts})
	b.WriteString("\n  LIMIT ?\n)")
	args = append(args, MatchCountCap)
	var n int
	if err := s.db.QueryRow(b.String(), args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count search: %w", err)
	}
	return n, nil
}

// contextWindowSQL fetches the `limit` posts just before and just after a pivot
// in one statement (see contextWindow). Pure create_at range comparisons keep it
// on a clean index range-seek: an earlier version added an
// `OR (create_at = ? AND id < ?)` tiebreak to order posts sharing an exact
// millisecond, but that forced a MULTI-INDEX OR + temp-btree sort that ran ~40ms
// per call (≈1s for a 30-hit page). The tradeoff of dropping it — a post sharing
// the pivot's exact create_at is omitted from the window — is negligible for a
// context preview and ~1000× faster.
const contextWindowSQL = `
SELECT raw_json, side FROM (
    SELECT raw_json, 'B' AS side
    FROM posts
    WHERE channel_id = ? AND delete_at = 0 AND create_at < ?
    ORDER BY create_at DESC
    LIMIT ?
)
UNION ALL
SELECT raw_json, side FROM (
    SELECT raw_json, 'A' AS side
    FROM posts
    WHERE channel_id = ? AND delete_at = 0 AND create_at > ?
    ORDER BY create_at ASC
    LIMIT ?
)`

// contextWindow returns the limit newest posts older than the pivot
// (oldest→newest) and the limit oldest posts newer than the pivot
// (oldest→newest), both in the same channel. It's a one-statement
// equivalent of two contextPosts calls, halving the per-search-hit
// statement count — which is the hot path during interactive search.
func (s *Store) contextWindow(channelID string, createAt int64, limit int) (before, after []*model.Post, err error) {
	if s == nil || limit <= 0 || channelID == "" {
		return nil, nil, nil
	}
	rows, err := s.db.Query(contextWindowSQL,
		channelID, createAt, limit,
		channelID, createAt, limit,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("query context window: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var side string
		if err := rows.Scan(&raw, &side); err != nil {
			continue
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if side == "B" {
			before = append(before, &p)
		} else {
			after = append(after, &p)
		}
	}
	// The Before subquery is sorted newest→oldest so SQLite can apply
	// LIMIT against the index in DESC order; reverse here so the caller
	// sees oldest→newest, matching contextPosts(before=true) semantics.
	for i, j := 0, len(before)-1; i < j; i, j = i+1, j-1 {
		before[i], before[j] = before[j], before[i]
	}
	return before, after, nil
}

// PostsAround returns up to beforeN posts older than pivotPostID plus
// the pivot itself plus up to afterN posts newer, all from the same
// channel, ordered oldest→newest. Returns nil if the pivot post isn't
// in the cache (so the caller can fall back to a normal channel open).
func (s *Store) PostsAround(channelID, pivotPostID string, beforeN, afterN int) ([]*model.Post, error) {
	if s == nil || channelID == "" || pivotPostID == "" {
		return nil, nil
	}
	pivot, err := s.lookupPost(pivotPostID)
	if err != nil {
		return nil, err
	}
	if pivot == nil {
		return nil, nil
	}
	before, err := s.contextPosts(channelID, pivot.CreateAt, pivot.Id, beforeN, true)
	if err != nil {
		return nil, err
	}
	after, err := s.contextPosts(channelID, pivot.CreateAt, pivot.Id, afterN, false)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Post, 0, len(before)+1+len(after))
	out = append(out, before...)
	out = append(out, pivot)
	out = append(out, after...)
	return out, nil
}

// ChannelOfPost returns the channel id of the cached post with the given id.
// ok is false when the post isn't cached. Used to resolve a permalink to its
// channel without an API round-trip when the target post is already local.
func (s *Store) ChannelOfPost(id string) (channelID string, ok bool, err error) {
	if s == nil || id == "" {
		return "", false, nil
	}
	err = s.db.QueryRow(`SELECT channel_id FROM posts WHERE id = ?`, id).Scan(&channelID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("channel of post: %w", err)
	}
	return channelID, true, nil
}

// lookupPost fetches a single post by Id. Returns (nil, nil) when the
// id isn't in the cache.
func (s *Store) lookupPost(id string) (*model.Post, error) {
	if s == nil || id == "" {
		return nil, nil
	}
	var raw []byte
	err := s.db.QueryRow(`SELECT raw_json FROM posts WHERE id = ?`, id).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup post: %w", err)
	}
	var p model.Post
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("unmarshal post: %w", err)
	}
	return &p, nil
}

// contextPosts returns up to limit posts adjacent to (channelID, createAt,
// postID) in the same channel, ordered oldest→newest. When before==true
// the window is the limit newest posts strictly older than the pivot;
// when before==false it's the limit oldest posts strictly newer than the
// pivot. The (create_at, id) tuple breaks ties when two posts share the
// same create_at.
func (s *Store) contextPosts(channelID string, createAt int64, postID string, limit int, before bool) ([]*model.Post, error) {
	if s == nil || limit <= 0 || channelID == "" {
		return nil, nil
	}
	var q string
	if before {
		q = `
SELECT raw_json FROM (
    SELECT raw_json, create_at, id
    FROM posts
    WHERE channel_id = ? AND delete_at = 0 AND id != ?
      AND (create_at < ? OR (create_at = ? AND id < ?))
    ORDER BY create_at DESC, id DESC
    LIMIT ?
) ORDER BY create_at ASC, id ASC`
	} else {
		q = `
SELECT raw_json
FROM posts
WHERE channel_id = ? AND delete_at = 0 AND id != ?
  AND (create_at > ? OR (create_at = ? AND id > ?))
ORDER BY create_at ASC, id ASC
LIMIT ?`
	}
	rows, err := s.db.Query(q, channelID, postID, createAt, createAt, postID, limit)
	if err != nil {
		return nil, fmt.Errorf("query context: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	return out, nil
}

// upsertSQL inserts a post, or updates the stored row on id conflict — but
// only when the incoming row actually differs (the trailing WHERE). raw_json
// is the full serialized post, so an identical blob means nothing changed and
// the UPDATE is skipped entirely. That matters because the posts_au FTS
// trigger re-tokenizes message on every UPDATE: without the guard, re-opening
// a channel re-fetches ~a page of unchanged posts and needlessly re-indexes
// each one. When the WHERE is false SQLite treats the conflict as DO NOTHING
// (no row write, no triggers), so a warm refetch costs nothing.
//
// The delete_at half makes a soft-deleted row terminal: once Delete has
// tombstoned a post, a later refetch carrying its pre-delete content can't
// resurrect it. A first INSERT (no conflict) is unaffected, and the normal
// live→deleted transition still applies because the guard tests the existing
// row's delete_at, not the incoming one's.
const upsertSQL = `
INSERT INTO posts (id, channel_id, user_id, root_id, create_at, update_at, edit_at, delete_at, message, raw_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    channel_id = excluded.channel_id,
    user_id    = excluded.user_id,
    root_id    = excluded.root_id,
    create_at  = excluded.create_at,
    update_at  = excluded.update_at,
    edit_at    = excluded.edit_at,
    delete_at  = excluded.delete_at,
    message    = excluded.message,
    raw_json   = excluded.raw_json
WHERE posts.raw_json IS NOT excluded.raw_json
  AND posts.delete_at = 0
`

// Upsert inserts or updates a single post by Id. Posts with an empty Id
// (i.e. optimistic UI stubs) are silently skipped.
func (s *Store) Upsert(p *model.Post) error {
	if s == nil || p == nil || p.Id == "" {
		return nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal post: %w", err)
	}
	_, err = s.db.Exec(upsertSQL,
		p.Id, p.ChannelId, p.UserId, p.RootId,
		p.CreateAt, p.UpdateAt, p.EditAt, p.DeleteAt,
		p.Message, raw,
	)
	if err != nil {
		return fmt.Errorf("upsert post: %w", err)
	}
	return nil
}

// UpsertMany runs every Upsert inside a single transaction. Posts with
// empty Ids are skipped (see Upsert).
func (s *Store) UpsertMany(posts []*model.Post) error {
	if s == nil || len(posts) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.Prepare(upsertSQL)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	for _, p := range posts {
		if p == nil || p.Id == "" {
			continue
		}
		raw, err := json.Marshal(p)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("marshal post %s: %w", p.Id, err)
		}
		if _, err := stmt.Exec(
			p.Id, p.ChannelId, p.UserId, p.RootId,
			p.CreateAt, p.UpdateAt, p.EditAt, p.DeleteAt,
			p.Message, raw,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("upsert post %s: %w", p.Id, err)
		}
	}
	return tx.Commit()
}

// StripTombstoneContent clears the content a deleted post must never retain —
// the message body, rich metadata, and file attachments — while leaving Props
// intact so a webhook post keeps its override_username for the tombstone's
// author line. It is the single source of truth for *what* a tombstone drops,
// shared by tombstonePost (persisted tombstones) and the UI's markPostDeleted
// (the live, in-memory ones), so a new content-bearing field can't be stripped
// from one path and forgotten in the other.
func StripTombstoneContent(p *model.Post) {
	p.Message = ""
	p.Metadata = nil
	p.FileIds = nil
}

// tombstonePost strips a post down to a tombstone: its content is cleared (see
// StripTombstoneContent) and a nonzero DeleteAt is stamped so the row reads as
// removed. Mirrors the in-memory markPostDeleted in internal/ui so the
// persisted and live tombstones render identically.
func tombstonePost(p *model.Post) {
	if p.DeleteAt == 0 {
		p.DeleteAt = time.Now().UnixMilli()
	}
	StripTombstoneContent(p)
}

// deleteSQL writes a tombstone, inserting the row when the post was never
// cached (a delete that races ahead of the post's own persist) and otherwise
// overwriting the live row. Unlike upsertSQL there is no delete_at guard — the
// delete must always win; the guard on upsertSQL then keeps a later refetch
// from resurrecting it. message is forced empty so the posts_au trigger rebuilds
// an empty FTS shadow row and the removed text stops being searchable.
const deleteSQL = `
INSERT INTO posts (id, channel_id, user_id, root_id, create_at, update_at, edit_at, delete_at, message, raw_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?)
ON CONFLICT(id) DO UPDATE SET
    delete_at = excluded.delete_at,
    update_at = excluded.update_at,
    message   = '',
    raw_json  = excluded.raw_json
`

// Delete soft-deletes a post: the row is kept but flagged (delete_at set) and
// its content stripped, so the transcript can show a persistent "message
// deleted" tombstone across restarts while search, digests, and the embedding
// index stop surfacing the removed text. The transcript loaders opt back in to
// these rows via includeDeleted; every other query keeps its delete_at = 0
// guard.
//
// p is the post from the post_deleted event. When the post is already cached
// the richer stored copy is reused (keeping Props/override_username for the
// tombstone's author line); when the delete arrives before the post was ever
// persisted, p seeds a fresh tombstone row so the marker still survives.
func (s *Store) Delete(p *model.Post) error {
	if s == nil || p == nil || p.Id == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	// base is a private clone we can strip without touching the caller's post.
	// Prefer the cached copy when present — it carries fuller fields (e.g.
	// Props/override_username for the tombstone's author line).
	base := p.Clone()
	var (
		storedDeleteAt int64
		raw            []byte
	)
	switch err := tx.QueryRow(`SELECT delete_at, raw_json FROM posts WHERE id = ?`, p.Id).Scan(&storedDeleteAt, &raw); {
	case err == nil:
		if storedDeleteAt != 0 {
			// Already a tombstone — the offline-deletion sync re-reports the same
			// delete on every catch-up, so don't rewrite (and re-churn FTS) here.
			return tx.Commit()
		}
		var stored model.Post
		if json.Unmarshal(raw, &stored) == nil {
			base = &stored
		}
	case errors.Is(err, sql.ErrNoRows):
		// Not cached yet — fall through with base seeded from the event.
	default:
		return fmt.Errorf("load post for delete: %w", err)
	}
	// Carry the authoritative delete time from the event / since-response: the
	// cached copy we just preferred still has delete_at = 0, so without this the
	// tombstone would be stamped with now() instead of when it was really removed.
	if p.DeleteAt != 0 {
		base.DeleteAt = p.DeleteAt
	}
	tombstonePost(base)
	stripped, err := json.Marshal(base)
	if err != nil {
		return fmt.Errorf("marshal tombstone: %w", err)
	}
	// Stamp update_at with the delete time so MaxUpdateAt (the offline-deletion
	// sync cursor) advances past this deletion and won't re-report it forever.
	updateAt := base.UpdateAt
	if base.DeleteAt > updateAt {
		updateAt = base.DeleteAt
	}
	if _, err := tx.Exec(deleteSQL,
		base.Id, base.ChannelId, base.UserId, base.RootId,
		base.CreateAt, updateAt, base.EditAt, base.DeleteAt, stripped,
	); err != nil {
		return fmt.Errorf("soft-delete post: %w", err)
	}
	// The posts_capture_revision trigger archived the pre-delete row during the
	// overwrite above; drop it (and any earlier edit history) so the removed
	// content doesn't linger in post_revisions. The posts_delete_vector trigger
	// only fires on a row DELETE, never an UPDATE, so clear the embedding here.
	if _, err := tx.Exec(`DELETE FROM post_revisions WHERE post_id = ?`, p.Id); err != nil {
		return fmt.Errorf("purge revisions: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM post_vectors WHERE post_id = ?`, p.Id); err != nil {
		return fmt.Errorf("purge vector: %w", err)
	}
	return tx.Commit()
}

// deletePredicate returns the SQL fragment that filters out soft-deleted rows,
// or "" to keep them. It always begins with " AND " so it can be spliced
// straight after an existing WHERE clause. The transcript loaders pass
// includeDeleted=true to surface tombstones; everyone else keeps the guard.
func deletePredicate(includeDeleted bool) string {
	if includeDeleted {
		return ""
	}
	return " AND delete_at = 0"
}

// RecentForChannel returns up to limit most-recent posts for the channel,
// ordered oldest→newest (i.e. ready to assign to the UI's m.posts slice
// without reversal). includeDeleted keeps soft-deleted rows in the result so
// the transcript can render their tombstones; pass false to get live posts
// only.
func (s *Store) RecentForChannel(channelID string, limit int, includeDeleted bool) ([]*model.Post, error) {
	if s == nil || channelID == "" || limit <= 0 {
		return nil, nil
	}
	// Two-step sort: pick the newest `limit` by create_at DESC, then
	// re-sort the result ascending so callers can append directly.
	q := `
SELECT raw_json FROM (
    SELECT rowid, raw_json, create_at
    FROM posts
    WHERE channel_id = ?` + deletePredicate(includeDeleted) + `
    ORDER BY create_at DESC
    LIMIT ?
) ORDER BY create_at ASC`
	rows, err := s.db.Query(q, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			// One bad row shouldn't poison the rest — skip and continue.
			continue
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// AfterInChannel returns up to limit posts in the channel strictly newer than
// afterCreateAt, ordered oldest→newest. Mirror of BeforeInChannel — used to
// page forward into a channel's history when the user scrolls past the last
// currently-rendered post (e.g. after opening a search hit centred on an older
// message). includeDeleted keeps soft-deleted rows so the transcript can render
// their tombstones.
func (s *Store) AfterInChannel(channelID string, afterCreateAt int64, limit int, includeDeleted bool) ([]*model.Post, error) {
	if s == nil || channelID == "" || limit <= 0 {
		return nil, nil
	}
	q := `
SELECT raw_json FROM posts
WHERE channel_id = ?` + deletePredicate(includeDeleted) + ` AND create_at > ?
ORDER BY create_at ASC
LIMIT ?`
	rows, err := s.db.Query(q, channelID, afterCreateAt, limit)
	if err != nil {
		return nil, fmt.Errorf("query after: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// BeforeInChannel returns up to limit posts in the channel strictly older than
// beforeCreateAt, ordered oldest→newest. Used to page further back into a
// channel's history when the user scrolls past the top of what's currently
// rendered. includeDeleted keeps soft-deleted rows so the transcript can render
// their tombstones; the feed's context lines pass false to stay tombstone-free.
func (s *Store) BeforeInChannel(channelID string, beforeCreateAt int64, limit int, includeDeleted bool) ([]*model.Post, error) {
	if s == nil || channelID == "" || limit <= 0 {
		return nil, nil
	}
	q := `
SELECT raw_json FROM (
    SELECT raw_json, create_at
    FROM posts
    WHERE channel_id = ?` + deletePredicate(includeDeleted) + ` AND create_at < ?
    ORDER BY create_at DESC
    LIMIT ?
) ORDER BY create_at ASC`
	rows, err := s.db.Query(q, channelID, beforeCreateAt, limit)
	if err != nil {
		return nil, fmt.Errorf("query before: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// AuthoredBetween returns up to limit non-deleted posts written by authorID
// whose create_at falls in [after, before), ordered oldest→newest. A zero
// after or before disables that bound; a non-positive limit returns every
// match in the range. When limit caps the result, the most recent `limit`
// posts are kept (a wide range degrades to recent activity rather than the
// oldest sliver). System posts (Type != "") are dropped after decode — the
// posts table has no type column, so it can't be a SQL predicate.
//
// This is the empty-MATCH counterpart to SearchSpec, which bails on a blank
// FTS expression. `matterbox digest` lists one's own posts across channels
// for a time window with no keyword, so it can't route through searchFTS.
func (s *Store) AuthoredBetween(authorID string, after, before int64, limit int) ([]*model.Post, error) {
	if s == nil || authorID == "" {
		return nil, nil
	}
	var b strings.Builder
	b.WriteString("SELECT raw_json FROM posts\nWHERE user_id = ? AND delete_at = 0")
	args := []any{authorID}
	if after > 0 {
		b.WriteString("\n  AND create_at >= ?")
		args = append(args, after)
	}
	if before > 0 {
		b.WriteString("\n  AND create_at < ?")
		args = append(args, before)
	}
	// With a cap, pull the newest `limit` (DESC + LIMIT); we re-sort
	// ascending in Go below so system-post skipping can't disturb the order.
	if limit > 0 {
		b.WriteString("\nORDER BY create_at DESC\nLIMIT ?")
		args = append(args, limit)
	}
	rows, err := s.db.Query(b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query authored: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if p.Type != "" {
			continue // system message (join/leave/header change/…)
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreateAt != out[j].CreateAt {
			return out[i].CreateAt < out[j].CreateAt
		}
		return out[i].Id < out[j].Id
	})
	return out, nil
}

// Revisions returns the archived prior versions of a post, oldest→newest
// by edit_at. Returns nil if the post has no recorded edit history (or
// has only ever been seen in its current form). Note: only versions
// observed by this matterbox install are present — Mattermost's server
// API does not expose edit history.
func (s *Store) Revisions(postID string) ([]*model.Post, error) {
	if s == nil || postID == "" {
		return nil, nil
	}
	const q = `
SELECT raw_json FROM post_revisions
WHERE post_id = ?
ORDER BY edit_at ASC, id ASC`
	rows, err := s.db.Query(q, postID)
	if err != nil {
		return nil, fmt.Errorf("query revisions: %w", err)
	}
	defer rows.Close()
	var out []*model.Post
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan revision: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// LatestPostID returns the most recent (by create_at) non-deleted post
// id stored for the channel, or "" if none. Used as the cursor for
// Client.PostsAfter when filling the gap on channel reopen.
func (s *Store) LatestPostID(channelID string) (string, error) {
	if s == nil || channelID == "" {
		return "", nil
	}
	const q = `
SELECT id FROM posts
WHERE channel_id = ? AND delete_at = 0
ORDER BY create_at DESC
LIMIT 1`
	var id string
	err := s.db.QueryRow(q, channelID).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("latest id: %w", err)
	}
	return id, nil
}

// MaxUpdateAt returns the largest update_at (unix-ms) stored for the channel
// across all rows — deleted ones included — or 0 if none. It's the cursor for
// the offline-deletion sync: a deletion that happened while matterbox was away
// bumps the server post's update_at past this watermark, so PostsSince(MaxUpdateAt)
// surfaces it (see internal/ui syncChannelDeletions). Delete stamps a tombstone's
// update_at with its delete time so this advances and a deletion isn't re-reported.
func (s *Store) MaxUpdateAt(channelID string) (int64, error) {
	if s == nil || channelID == "" {
		return 0, nil
	}
	var max sql.NullInt64
	err := s.db.QueryRow(
		`SELECT MAX(update_at) FROM posts WHERE channel_id = ?`, channelID,
	).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("max update_at: %w", err)
	}
	return max.Int64, nil // NullInt64 zero-values to 0 when no rows match
}

// OldestPost returns the oldest (by create_at) non-deleted post id +
// its create_at (unix-ms) stored for the channel, or ("", 0) if none.
// Used as the cursor for Client.PostsBefore when extending a channel's
// cached history backward.
func (s *Store) OldestPost(channelID string) (string, int64, error) {
	if s == nil || channelID == "" {
		return "", 0, nil
	}
	const q = `
SELECT id, create_at FROM posts
WHERE channel_id = ? AND delete_at = 0
ORDER BY create_at ASC
LIMIT 1`
	var id string
	var createAt int64
	err := s.db.QueryRow(q, channelID).Scan(&id, &createAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, nil
		}
		return "", 0, fmt.Errorf("oldest post: %w", err)
	}
	return id, createAt, nil
}

// DistinctUserIDs returns up to limit distinct author ids from cached posts,
// most-recently-active first, so a caller can resolve usernames for the people
// who actually appear in the cache (e.g. to label agentic-search results). A
// limit <= 0 returns every distinct author.
func (s *Store) DistinctUserIDs(limit int) ([]string, error) {
	if s == nil {
		return nil, nil
	}
	q := `
SELECT user_id FROM posts
WHERE user_id <> '' AND delete_at = 0
GROUP BY user_id
ORDER BY MAX(create_at) DESC`
	if limit > 0 {
		q += fmt.Sprintf("\nLIMIT %d", limit)
	}
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("distinct user ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan user id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
