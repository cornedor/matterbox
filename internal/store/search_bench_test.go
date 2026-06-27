package store

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// benchStore opens a throwaway on-disk store (FTS5 + triggers all live), cleaned
// up when the benchmark ends. On-disk (not :memory:) matches production: the
// cache is a file at ~/.config/matterbox/messages.db.
func benchStore(b *testing.B) *Store {
	b.Helper()
	s, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

// benchBodies is a pool of realistic chat messages; ~1/3 mention "deploy"/
// "deployment" so a `deploy` query matches a large fraction of the corpus —
// the broad query that most exercises the bm25 scan + rank pool.
var benchBodies = []string{
	"can you take a look at the deployment pipeline when you get a chance?",
	"the deploy to staging failed again, rolling back now",
	"lunch plans? thinking about the new ramen place down the street",
	"PR is up for review, mostly a refactor of the auth middleware",
	"deployment finished, metrics look healthy so far 🎉",
	"anyone seen the flaky test in the billing suite? it fails ~1/10 runs",
	"meeting moved to 3pm, calendar invite updated",
	"we should deploy the hotfix before the weekend freeze",
	"docs updated for the new search API, link in the channel topic",
	"incident postmortem scheduled for tomorrow, please add notes",
	"the deploy script needs a longer health-check timeout I think",
	"great work everyone, shipping this felt smooth",
}

// benchSeedPosts builds n posts spread across 10 channels and over time, drawing
// bodies from benchBodies so the FTS index holds varied, realistic vocabulary.
func benchSeedPosts(n int) []*model.Post {
	posts := make([]*model.Post, n)
	for i := 0; i < n; i++ {
		posts[i] = &model.Post{
			Id:        fmt.Sprintf("post-%07d", i),
			ChannelId: fmt.Sprintf("chan%d", i%10),
			UserId:    fmt.Sprintf("user%d", i%25),
			Message:   benchBodies[i%len(benchBodies)],
			CreateAt:  int64(1_700_000_000_000 + i*60_000),
			UpdateAt:  int64(1_700_000_000_000 + i*60_000),
		}
	}
	return posts
}

// BenchmarkSearchFTS measures one local keyword search end to end — the FTS5
// MATCH, the bm25 + recency re-rank (rankByRelevanceAndAge), and the per-hit
// context-window fetch — exactly as the search pane runs it (limit
// searchPageSize=30, contextN=2). It's the I/O-touching companion to the
// pure-CPU BenchmarkRankByRelevanceAndAge / BenchmarkSemanticScore, showing how
// keyword-search latency grows with the number of cached posts. The query is
// broad ("deploy" matches ~1/3 of the corpus) so the rank pool fills.
func BenchmarkSearchFTS(b *testing.B) {
	const limit, contextN = 30, 2
	for _, n := range []int{1_000, 10_000, 50_000} {
		s := benchStore(b)
		if err := s.UpsertMany(benchSeedPosts(n)); err != nil {
			b.Fatalf("seed: %v", err)
		}
		b.Run(fmt.Sprintf("posts=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				hits, err := s.Search("deploy", nil, limit, contextN)
				if err != nil {
					b.Fatal(err)
				}
				_ = hits
			}
		})
	}
}

// BenchmarkUpsertMany measures one transactional batch write — the JSON marshal,
// the prepared upsert, and the FTS-index trigger maintenance — for the batch
// sizes the cache-warmer and listen daemon push (≈60 = a live WS page, 600 = a
// backfill chunk). It re-upserts the same batch each iteration, mirroring the
// warmer's steady state where fetched pages overlap what's already cached (first
// iteration inserts, the rest take the ON CONFLICT update path; both write the
// row and re-index it). The number is per-batch, so divide by the batch size for
// per-post ingest cost.
func BenchmarkUpsertMany(b *testing.B) {
	for _, batch := range []int{60, 600} {
		posts := benchSeedPosts(batch)
		b.Run(fmt.Sprintf("batch=%d", batch), func(b *testing.B) {
			s := benchStore(b)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := s.UpsertMany(posts); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
