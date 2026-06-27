package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

var modelSink tea.Model

//go:noinline
func boxModel(m Model) tea.Model { return m }

// BenchmarkModelBoundaryCopy isolates the per-event cost paid at bubbletea's
// value-receiver boundary: the whole Model is copied into the call (the Update
// receiver) and heap-boxed into the tea.Model interface on return. This happens
// on every Update (per keystroke) and every View (per render). B/op ≈
// sizeof(Model); allocs/op = 1 (the box). This is the number the Model-shrink
// directly moves.
func BenchmarkModelBoundaryCopy(b *testing.B) {
	m := benchSwitcherModel(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		modelSink = boxModel(m)
	}
}
