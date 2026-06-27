package ui

import "testing"

// BenchmarkUpdatePostDispatch measures the three post-dispatch methods Update()
// runs on every event. With value receivers each copies the whole ~102KB Model
// (even to just read a few fields and return nil, the common case); with
// pointer receivers they copy nothing. This isolates PERF #1's win.
func BenchmarkUpdatePostDispatch(b *testing.B) {
	m := benchSwitcherModel(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.resolveUnknownSenders()
		_ = m.fetchPendingEmoji()
		_ = m.fetchPendingMRStatus()
	}
}
