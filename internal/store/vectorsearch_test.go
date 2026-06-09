package store

import (
	"testing"
)

const testTag = "m@4"

// seedHybrid inserts two posts with orthogonal unit vectors:
//
//	A "deployment pipeline broke"  → x-axis
//	B "where shall we eat lunch"   → y-axis
//
// so a query can target one by vector (cosine) and the other by keyword (FTS),
// letting each ranker and their fusion be checked independently.
func seedHybrid(t *testing.T) *Store {
	t.Helper()
	s := tempStore(t)
	s.mustPost(t, "A", "c1", "deployment pipeline broke", 1000)
	s.mustPost(t, "B", "c1", "where shall we eat lunch", 1000)
	if err := s.UpsertVector("A", []float32{1, 0, 0, 0}, testTag, 1); err != nil {
		t.Fatalf("vec A: %v", err)
	}
	if err := s.UpsertVector("B", []float32{0, 1, 0, 0}, testTag, 1); err != nil {
		t.Fatalf("vec B: %v", err)
	}
	return s
}

func ids(hits []SearchHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Match.Id
	}
	return out
}

func TestHybridSemanticRecallNoKeywordOverlap(t *testing.T) {
	s := seedHybrid(t)
	// Query vector points at A; query text shares NO words with A's message, so
	// only the semantic side can find it. A must come back, ranked first.
	hits, _, err := s.SearchHybrid("", []float32{1, 0, 0, 0}, testTag, HybridScope{}, 10, 0, 0)
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	if len(hits) == 0 || hits[0].Match.Id != "A" {
		t.Fatalf("want A first by vector, got %v", ids(hits))
	}
}

func TestHybridDegradesToKeyword(t *testing.T) {
	s := seedHybrid(t)
	// No query vector → pure keyword. "lunch" matches only B.
	hits, _, err := s.SearchHybrid("lunch", nil, testTag, HybridScope{}, 10, 0, 0)
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	if got := ids(hits); len(got) != 1 || got[0] != "B" {
		t.Fatalf("want [B] by keyword, got %v", got)
	}
}

func TestHybridFusesBothSides(t *testing.T) {
	s := seedHybrid(t)
	// Keyword "deployment" matches A; vector points at B. Fusion must surface
	// BOTH — the keyword-only hit and the vector-only hit.
	hits, _, err := s.SearchHybrid("deployment", []float32{0, 1, 0, 0}, testTag, HybridScope{}, 10, 0, 0)
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	got := map[string]bool{}
	for _, id := range ids(hits) {
		got[id] = true
	}
	if !got["A"] || !got["B"] {
		t.Fatalf("want both A and B from fusion, got %v", ids(hits))
	}
}

func TestHybridModelTagScoping(t *testing.T) {
	s := seedHybrid(t)
	// A query under a different model tag sees no vectors, so semantic
	// contributes nothing; only keyword matches remain.
	hits, _, err := s.SearchHybrid("deployment", []float32{1, 0, 0, 0}, "other@4", HybridScope{}, 10, 0, 0)
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	if got := ids(hits); len(got) != 1 || got[0] != "A" {
		t.Fatalf("want only keyword hit [A] under wrong tag, got %v", got)
	}
}

func TestHybridChannelScope(t *testing.T) {
	s := seedHybrid(t)
	// Scope to a channel with no posts → no hits, without touching the rankers.
	hits, _, err := s.SearchHybrid("deployment", []float32{1, 0, 0, 0}, testTag, HybridScope{ChannelIDs: []string{"nope"}}, 10, 0, 0)
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("want no hits out of scope, got %v", ids(hits))
	}
}

func TestHybridEmptyQuery(t *testing.T) {
	s := seedHybrid(t)
	// No text and no vector → nothing to rank.
	hits, _, err := s.SearchHybrid("", nil, testTag, HybridScope{}, 10, 0, 0)
	if err != nil {
		t.Fatalf("hybrid: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("want no hits, got %v", ids(hits))
	}
}
