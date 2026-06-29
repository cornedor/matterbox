package welcome

import (
	"testing"

	"matterbox/internal/config"
)

var welcomeSizes = []struct {
	name       string
	cols, rows int
}{
	{"80x24", 80, 24},
	{"120x40", 120, 40},
	{"200x50", 200, 50},
}

// benchWizard builds a wizard-phase model sized to cols×rows with the scene
// cache primed at t=3 (mid-intro, the heaviest steady frame).
func benchWizard(cols, rows int) *Model {
	m := New(&config.Config{})
	m.width, m.height = cols, rows
	m.rend.Resize(cols, rows)
	m.phase, m.step = phaseWizard, stepAdvanced
	m.t = 3.0
	m.View() // prime the scene cache
	return m
}

// BenchmarkViewKeystroke is the cost of a View triggered by a keystroke: m.t is
// unchanged, so the scene cache is reused and only the overlay + serialize run.
// This is the path that previously re-rendered the whole scene on every keypress.
func BenchmarkViewKeystroke(b *testing.B) {
	for _, sz := range welcomeSizes {
		b.Run(sz.name, func(b *testing.B) {
			m := benchWizard(sz.cols, sz.rows)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.View()
			}
		})
	}
}

// BenchmarkViewFrame is the cost of a View triggered by an animation frameMsg:
// m.t advances, so the scene is re-rendered. This is the per-tick cost (12fps in
// the wizard phase, 30fps during the intro).
func BenchmarkViewFrame(b *testing.B) {
	for _, sz := range welcomeSizes {
		b.Run(sz.name, func(b *testing.B) {
			m := benchWizard(sz.cols, sz.rows)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.t += 0.05
				_ = m.View()
			}
		})
	}
}
