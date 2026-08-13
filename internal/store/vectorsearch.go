package store

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

// hybridPool bounds how many candidates each ranker (keyword, semantic)
// contributes before fusion. Large enough that a strong match from either side
// reaches the merge, small enough to keep the fuse + context-window work cheap.
const hybridPool = 300

// rrfK is the Reciprocal Rank Fusion constant. RRF scores a document by
// Σ 1/(rrfK + rank) over the lists it appears in, so a high rank in either the
// keyword or the semantic list lifts it without the two incomparable score
// scales (bm25 vs cosine) ever being added directly. 60 is the value from the
// original RRF paper and is a fine default at this scale.
const rrfK = 60.0

// HybridScope narrows a hybrid search by post metadata — the non-FTS filters
// shared by both rankers (it mirrors the metadata half of SearchSpec). The zero
// value imposes no restriction. ChannelIDs carries the nil-vs-empty convention
// from Search: nil = no scope, non-nil-empty = scope resolved to nothing = no
// hits.
type HybridScope struct {
	ChannelIDs []string
	AuthorIDs  []string
	After      int64 // inclusive lower bound on create_at (unix-ms); 0 = none
	Before     int64 // exclusive upper bound on create_at (unix-ms); 0 = none
}

// SearchHybrid blends keyword (FTS5/bm25) and semantic (embedding cosine)
// rankings into one result set via Reciprocal Rank Fusion, ordered by relevance
// with recency only as a tiebreaker. (Unlike keyword-only Search it does NOT
// decay by age: RRF deliberately compresses scores into a narrow band, so an
// age-decay multiplier would swamp relevance — burying a strong older match
// under recent chatter. Recency genuinely helps keyword search, where bm25 has
// real spread; it hurts here.) queryVec is the query's embedding (already
// unit-normalized); modelTag scopes the vectors considered to the current
// model@dim so a model change in progress can't mix vector spaces. With an empty
// queryVec it degrades to pure keyword search, and with an empty queryText to
// pure semantic — so a caller can request keyword-only, semantic-only, or hybrid
// just by which of the two it supplies. scope narrows both rankers. offset skips
// that many top-ranked results (paging). Returns up to limit hits each with a
// contextN window, plus the total number of fused candidates (so a caller can
// tell whether more pages remain).
func (s *Store) SearchHybrid(queryText string, queryVec []float32, modelTag string, scope HybridScope, limit, offset, contextN int) ([]SearchHit, int, error) {
	return s.SearchFused(ftsQuery(queryText), queryVec, modelTag, scope, SortRelevance, limit, offset, contextN)
}

// SortOrder selects how SearchFused orders the candidates it has gathered.
type SortOrder int

const (
	// SortRelevance ranks by the fused RRF score, newest first among ties.
	SortRelevance SortOrder = iota
	// SortRecent ranks the same candidate set newest-first instead, with the
	// fused score only breaking ties. Every candidate already matched the query
	// on one side or the other, so this reads as "the latest messages about
	// this" — the ordering a question about a current situation ("is X still
	// broken", "when is Y back") wants, where the best-matching message may be
	// years old.
	SortRecent
)

// OrTerms compiles a list of keyword terms into an FTS5 OR expression suitable
// for SearchFused's keyword side: a post matching ANY term is a candidate. A
// multi-word term becomes an exact phrase, a single word a prefix term. Returns
// "" when nothing usable remains, which SearchFused reads as "no keyword side".
//
// This is the recall-oriented counterpart to the implicit AND that ftsQuery
// builds from free-form text: AND is right for a short query a human typed into
// a search box, but it collapses to zero matches on a sentence, so anything
// feeding a natural-language question to the keyword ranker wants this instead.
func OrTerms(terms []string) string { return orGroup(terms) }

// SearchFused is SearchHybrid with the keyword side given as a ready-made FTS5
// MATCH expression (build one with OrTerms) rather than compiled from free text,
// and with a choice of ordering. Everything else — the RRF blend, the scope,
// paging, the returned total — is identical; see SearchHybrid. An empty fts runs
// semantic-only, an empty queryVec keyword-only.
func (s *Store) SearchFused(fts string, queryVec []float32, modelTag string, scope HybridScope, order SortOrder, limit, offset, contextN int) ([]SearchHit, int, error) {
	if s == nil || limit <= 0 {
		return nil, 0, nil
	}
	if offset < 0 {
		offset = 0
	}
	f := searchFilter{
		channelIDs: scope.ChannelIDs,
		authorIDs:  scope.AuthorIDs,
		after:      scope.After,
		before:     scope.Before,
	}
	if f.scopedOut() {
		return nil, 0, nil
	}

	// Two independent rankings over the same scope.
	semOrder, semPosts, err := s.semanticRank(queryVec, modelTag, f, hybridPool)
	if err != nil {
		return nil, 0, err
	}
	ftsOrder, ftsPosts, err := s.keywordRank(fts, f, hybridPool)
	if err != nil {
		return nil, 0, err
	}

	// Fuse the two rank lists with RRF.
	fused := map[string]float64{}
	addRanks(fused, semOrder)
	addRanks(fused, ftsOrder)
	if len(fused) == 0 {
		return nil, 0, nil
	}

	// Resolve each fused id to a post (preferring whichever ranker already
	// loaded it). Rank by the fused relevance score alone; recency is only a
	// tiebreaker (see the doc comment for why age decay is deliberately absent).
	type cand struct {
		post  *model.Post
		score float64
	}
	cands := make([]cand, 0, len(fused))
	for id, rrf := range fused {
		p := ftsPosts[id]
		if p == nil {
			p = semPosts[id]
		}
		if p == nil {
			// Neither ranker carried the body (shouldn't happen — both load what
			// they rank — but stay defensive rather than fetch row-by-row).
			continue
		}
		cands = append(cands, cand{post: p, score: rrf})
	}
	sort.SliceStable(cands, func(a, b int) bool {
		if order == SortRecent {
			if cands[a].post.CreateAt != cands[b].post.CreateAt {
				return cands[a].post.CreateAt > cands[b].post.CreateAt
			}
			return cands[a].score > cands[b].score // tie: stronger match first
		}
		if cands[a].score != cands[b].score {
			return cands[a].score > cands[b].score
		}
		return cands[a].post.CreateAt > cands[b].post.CreateAt // tie: newest first
	})
	total := len(cands)

	// Page: skip offset, keep limit.
	if offset >= len(cands) {
		return nil, total, nil
	}
	cands = cands[offset:]
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]SearchHit, 0, len(cands))
	for _, c := range cands {
		before, after, _ := s.contextWindow(c.post.ChannelId, c.post.CreateAt, contextN)
		out = append(out, SearchHit{Match: c.post, Before: before, After: after})
	}
	return out, total, nil
}

// addRanks folds a ranked id list (best first) into the RRF score map.
func addRanks(into map[string]float64, order []string) {
	for i, id := range order {
		into[id] += 1.0 / (rrfK + float64(i+1))
	}
}

// semanticRank scores every in-scope vector under modelTag against queryVec by
// cosine similarity (a dot product, since both are unit-normalized) and returns
// the top `pool` post ids best-first, plus the posts for those winners. A
// brute-force scan is intentional: at a personal archive's size it's well under
// a millisecond and needs no ANN index. Crucially it scores on (id, vec) ALONE
// — the post bodies are loaded only for the top `pool` afterward, so the cost is
// independent of how big the unmarshalled corpus would be. An empty queryVec
// returns no ranking so SearchHybrid degrades to keyword-only.
func (s *Store) semanticRank(queryVec []float32, modelTag string, f searchFilter, pool int) (order []string, posts map[string]*model.Post, err error) {
	posts = map[string]*model.Post{}
	if len(queryVec) == 0 {
		return nil, posts, nil
	}
	// Quantize the query to int8 once so each row is scored by an integer dot
	// product straight off the stored bytes — no per-row []float32 allocation.
	qq := quantizeQuery(queryVec)

	// Without a metadata filter, scan post_vectors alone: embedded posts are
	// never hard-deleted (the delete trigger drops their vector), and any
	// soft-deleted straggler is dropped when postsByIDs loads bodies. The JOIN
	// to posts is only needed to apply channel/author/time predicates.
	var b strings.Builder
	var args []any
	if filtered := len(f.channelIDs) > 0 || len(f.authorIDs) > 0 || f.after > 0 || f.before > 0; filtered {
		b.WriteString(`
SELECT v.post_id, v.vec
FROM post_vectors v
JOIN posts p ON p.id = v.post_id
WHERE v.model = ?
  AND p.delete_at = 0`)
		args = f.where(&b, []any{modelTag})
	} else {
		b.WriteString("SELECT post_id, vec FROM post_vectors WHERE model = ?")
		args = []any{modelTag}
	}

	rows, err := s.db.Query(b.String(), args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query semantic: %w", err)
	}
	defer rows.Close()

	type scored struct {
		id  string
		cos int64
	}
	var all []scored
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, nil, fmt.Errorf("scan semantic: %w", err)
		}
		if len(blob) != len(qq) {
			continue // dim mismatch (mid-migration) — skip rather than misscore
		}
		all = append(all, scored{id: id, cos: dotInt8(qq, blob)})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("rows: %w", err)
	}

	sort.SliceStable(all, func(a, b int) bool { return all[a].cos > all[b].cos })
	if len(all) > pool {
		all = all[:pool]
	}
	order = make([]string, len(all))
	for i, sc := range all {
		order[i] = sc.id
	}
	// Load bodies only for the winners.
	posts, err = s.postsByIDs(order)
	if err != nil {
		return nil, nil, err
	}
	return order, posts, nil
}

// postsByIDs loads the given posts by id into a map, skipping ids not present.
// Used to fetch only the small set of ranked winners after scoring.
func (s *Store) postsByIDs(ids []string) (map[string]*model.Post, error) {
	out := make(map[string]*model.Post, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	q := "SELECT id, raw_json FROM posts WHERE delete_at = 0 AND id IN (" + inPlaceholders(len(ids)) + ")"
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query posts by id: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("scan post: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		out[id] = &p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// keywordRank runs the FTS5 MATCH ordered by bm25 and returns the top `pool`
// post ids best-first plus the posts loaded. It mirrors searchFTS's candidate
// query but yields a rank list for fusion rather than a finished result set. An
// empty expression returns no ranking.
func (s *Store) keywordRank(fts string, f searchFilter, pool int) (order []string, posts map[string]*model.Post, err error) {
	posts = map[string]*model.Post{}
	if fts == "" {
		return nil, posts, nil
	}
	var b strings.Builder
	b.WriteString(`
SELECT p.id, p.raw_json
FROM posts_fts
JOIN posts p ON p.rowid = posts_fts.rowid
WHERE posts_fts MATCH ?
  AND p.delete_at = 0`)
	args := f.where(&b, []any{fts})
	b.WriteString("\nORDER BY bm25(posts_fts)\nLIMIT ?") // smaller bm25 = more relevant
	args = append(args, pool)

	rows, err := s.db.Query(b.String(), args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query keyword: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, nil, fmt.Errorf("scan keyword: %w", err)
		}
		var p model.Post
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		order = append(order, id)
		posts[id] = &p
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("rows: %w", err)
	}
	return order, posts, nil
}

// quantizeQuery encodes a query vector to int8 bytes the same way encodeVector
// stores post vectors (L2-normalize, then scale to ±127), so a byte-wise dot
// product against a stored vector is proportional to their cosine similarity —
// which is all ranking needs. Doing this once per search lets the hot loop score
// each row with integer math straight off the stored blob.
func quantizeQuery(vec []float32) []byte {
	out := make([]byte, len(vec))
	var sum float64
	for _, x := range vec {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return out
	}
	scale := quantScale / norm
	for i, x := range vec {
		q := math.Round(float64(x) * scale)
		if q > 127 {
			q = 127
		} else if q < -127 {
			q = -127
		}
		out[i] = byte(int8(q))
	}
	return out
}

// dotInt8 is the dot product of two equal-length int8-encoded byte slices. The
// result is a monotonic stand-in for cosine similarity (both operands are
// quantized unit vectors), suitable for ranking.
func dotInt8(a, b []byte) int64 {
	var s int64
	for i := range a {
		s += int64(int8(a[i])) * int64(int8(b[i]))
	}
	return s
}
