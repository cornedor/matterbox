package ui

import (
	"fmt"
	"testing"
)

// BenchmarkResizeSettle measures the single content re-render the settle tick
// runs after a resize drag (see the WindowSizeMsg / resizeSettleMsg handlers):
// the width-keyed postLineCache is dropped, but the width-independent
// postMarkdownCache stays warm, so the re-render re-wraps cached bodies instead
// of re-styling them. With the debounce, a whole drag pays this exactly once
// instead of once per intermediate size.
func BenchmarkResizeSettle(b *testing.B) {
	for _, n := range []int{60, 240, 600, 1200} {
		b.Run(fmt.Sprintf("posts=%d", n), func(b *testing.B) {
			m := newScrollBenchModel(n)
			m.renderMessages() // warm both caches
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.postLineCache = nil // width changed; markdown cache survives
				m.renderMessages()
			}
		})
	}
}

// BenchmarkResizeSettleCold is the pre-fix baseline: both caches cold, so
// every post is re-styled from scratch. The gap to BenchmarkResizeSettle is
// the styling tax the markdown cache now avoids on resize.
func BenchmarkResizeSettleCold(b *testing.B) {
	for _, n := range []int{60, 240, 600, 1200} {
		b.Run(fmt.Sprintf("posts=%d", n), func(b *testing.B) {
			m := newScrollBenchModel(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.postLineCache = nil
				m.postMarkdownCache = nil
				m.renderMessages()
			}
		})
	}
}
