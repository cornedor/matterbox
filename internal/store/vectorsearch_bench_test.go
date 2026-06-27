package store

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// benchVec returns a deterministic pseudo-random float32 vector of length dim.
// encodeVector / quantizeQuery normalize, so the components needn't be unit
// length — only varied and reproducible (fixed-seed rand keeps runs comparable).
func benchVec(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	return v
}

// benchDims brackets the embedded vector width: 256 is the default on-disk dim
// (config truncates EmbeddingGemma's 768 → 256 to quarter the cache), 768 is the
// model's native size if a user disables truncation. dotInt8 and encodeVector
// both scale linearly with this.
var benchDims = []int{256, 768}

// BenchmarkDotInt8 measures one integer dot product over two stored int8
// vectors — the inner loop semanticRank runs once per embedded post per query.
// Its per-call cost multiplies by the corpus size (see BenchmarkSemanticScore),
// so the dim sweep here is the per-row tax that whole scan pays.
func BenchmarkDotInt8(b *testing.B) {
	for _, dim := range benchDims {
		r := rand.New(rand.NewSource(1))
		x, _ := encodeVector(benchVec(r, dim))
		y, _ := encodeVector(benchVec(r, dim))
		b.Run(fmt.Sprintf("dim=%d", dim), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			var s int64
			for i := 0; i < b.N; i++ {
				s = dotInt8(x, y)
			}
			_ = s
		})
	}
}

// BenchmarkSemanticScore measures the CPU-bound core of semanticRank: quantize
// the query once, score every stored vector with dotInt8, then sort and truncate
// to the rank pool. This is the in-memory work the ranker does after the rows are
// read from SQLite (the I/O is excluded so the number is stable), so it shows how
// semantic-search latency grows with the number of embedded messages in the
// cache. dim is the default 256.
func BenchmarkSemanticScore(b *testing.B) {
	const dim, pool = 256, 100
	for _, n := range []int{1_000, 10_000, 50_000} {
		r := rand.New(rand.NewSource(1))
		stored := make([][]byte, n)
		for i := range stored {
			stored[i], _ = encodeVector(benchVec(r, dim))
		}
		queryVec := benchVec(r, dim)
		b.Run(fmt.Sprintf("vecs=%d", n), func(b *testing.B) {
			type scored struct {
				idx int
				cos int64
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				qq := quantizeQuery(queryVec)
				all := make([]scored, len(stored))
				for j, blob := range stored {
					all[j] = scored{idx: j, cos: dotInt8(qq, blob)}
				}
				sort.SliceStable(all, func(a, b int) bool { return all[a].cos > all[b].cos })
				if len(all) > pool {
					all = all[:pool]
				}
				_ = all
			}
		})
	}
}

// BenchmarkEncodeVector measures one float32→int8 quantization — the per-post
// cost the indexer pays for every message it embeds. A backfill of a large cache
// calls this thousands of times, so its allocation (one dim-byte blob) and the
// norm+round pass are the indexing throughput ceiling. quantizeQuery is the
// query-side twin (same work, no error return), called once per search.
func BenchmarkEncodeVector(b *testing.B) {
	for _, dim := range benchDims {
		r := rand.New(rand.NewSource(1))
		v := benchVec(r, dim)
		b.Run(fmt.Sprintf("dim=%d", dim), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = encodeVector(v)
			}
		})
	}
}
