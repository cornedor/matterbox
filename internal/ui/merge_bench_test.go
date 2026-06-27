package ui

import (
	"fmt"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// BenchmarkMergePostsByTime measures the warm-open reconciliation: when a
// channel reopens, fetchRecent's freshly-fetched newest posts are folded into the
// cached slice via mergePostsByTime (dedup by id, then a full time sort). The
// cache is non-contiguous, so this runs on every warm open and again on live-WS
// merges — its cost is dominated by sorting the whole merged set. existing is the
// cached history of size n; incoming is the newest ~60 (mostly already cached,
// the realistic overlap) plus a handful of genuinely new posts, mirroring what a
// reopen actually merges.
func BenchmarkMergePostsByTime(b *testing.B) {
	for _, n := range []int{60, 240, 600, 1200, 3000} {
		existing, _ := benchPosts(n)
		// A fetchRecent-style window: the newest 60 cached posts (overlap, so the
		// dedup map does real work) plus 4 brand-new ids appended after them.
		const fresh = 4
		overlap := 60
		if overlap > n {
			overlap = n
		}
		incoming := make([]*model.Post, 0, overlap+fresh)
		incoming = append(incoming, existing[n-overlap:]...)
		base := existing[n-1].CreateAt
		for i := 0; i < fresh; i++ {
			incoming = append(incoming, &model.Post{
				Id:       fmt.Sprintf("new-%06d", i),
				UserId:   "user0",
				Message:  "a freshly arrived message not yet in the cache",
				CreateAt: base + int64((i+1)*60_000),
				UpdateAt: base + int64((i+1)*60_000),
			})
		}
		b.Run(fmt.Sprintf("posts=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = mergePostsByTime(existing, incoming)
			}
		})
	}
}
