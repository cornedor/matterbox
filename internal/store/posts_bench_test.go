package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// benchRankPool builds a bm25-ordered candidate pool of n scored posts whose
// CreateAt is spread back from `now` (so the age-decay path does real work). The
// pool mirrors what searchFTS hands rankByRelevanceAndAge: best-bm25-first, up to
// MatchCountCap entries.
func benchRankPool(n int, now int64) []scoredPost {
	pool := make([]scoredPost, n)
	for i := 0; i < n; i++ {
		pool[i] = scoredPost{
			// Spread ages across ~6 months so the half-life decay separates rows.
			post: &model.Post{
				Id:       fmt.Sprintf("post-%06d", i),
				CreateAt: now - int64(i)*int64(45*time.Minute/time.Millisecond),
			},
			bm25: -1.0 - float64(i)*0.01, // more negative = better, already sorted
		}
	}
	return pool
}

// BenchmarkRankByRelevanceAndAge measures the relevance×age re-rank that runs on
// every local keyword search: a stable sort of the bm25 pool whose comparator
// calls math.Exp2 for each operand, so the cost is the exp evaluations times the
// O(n log n) comparisons. The "decay" case is the real path (a configured
// half-life); "no-decay" (halfLife 0) is the early-return-1 fast path, isolating
// the sort overhead from the exp math. Pools run up to MatchCountCap (500), the
// real ceiling searchFTS feeds in.
func BenchmarkRankByRelevanceAndAge(b *testing.B) {
	const now = int64(1_700_000_000_000)
	for _, n := range []int{50, 300, 500} {
		pool := benchRankPool(n, now)
		b.Run(fmt.Sprintf("decay/pool=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = rankByRelevanceAndAge(pool, now, 30*24*time.Hour)
			}
		})
		b.Run(fmt.Sprintf("no-decay/pool=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = rankByRelevanceAndAge(pool, now, 0)
			}
		})
	}
}
