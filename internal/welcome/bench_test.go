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
	m := New(&config.Config{}, false)
	m.width, m.height = cols, rows
	m.rend.Resize(cols, rows)
	m.phase, m.step = phaseWizard, stepAdvanced
	m.t = 3.0
	m.View() // prime the scene cache
	return m
}

// benchWizardDemo is benchWizard for `--demo`: the scene carries the extra
// per-frame work the flag adds — the per-letter title bob/flips (Text.Demo) and
// the soundtrack-driven mountain height-scale (mountainScale, fed by m.pulse).
// pulse is seeded mid-level so SetHeightScale runs a representative (non-1.0)
// multiplier, and t is past the demo intro-settle so the title is in its steady
// bobbing regime. This is the frame the demo paints 30 times a second.
func benchWizardDemo(cols, rows int) *Model {
	m := New(&config.Config{}, true)
	m.width, m.height = cols, rows
	m.rend.Resize(cols, rows)
	m.phase, m.step = phaseWizard, stepAdvanced
	m.t = 12.0
	m.pulse = 0.5
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

// BenchmarkViewKeystrokeDemo is BenchmarkViewKeystroke for `--demo`: m.t is
// unchanged, so the scene cache (height-scale and all) is reused and only the
// overlay + serialize run. The demo flag costs nothing on this path — the cached
// frame already holds the bobbing title — so it should track the non-demo number.
func BenchmarkViewKeystrokeDemo(b *testing.B) {
	for _, sz := range welcomeSizes {
		b.Run(sz.name, func(b *testing.B) {
			m := benchWizardDemo(sz.cols, sz.rows)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.View()
			}
		})
	}
}

// BenchmarkViewFrameDemo is BenchmarkViewFrame for `--demo`: each frame advances
// m.t, so the scene re-renders with the animated title and recomputes the
// mountain height-scale. Demo holds 30fps throughout (frameRate), so this frame
// fires 2.5× more often than the plain wizard's 12fps — its per-frame cost is the
// one that sets the demo's steady-state CPU.
func BenchmarkViewFrameDemo(b *testing.B) {
	for _, sz := range welcomeSizes {
		b.Run(sz.name, func(b *testing.B) {
			m := benchWizardDemo(sz.cols, sz.rows)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.t += 0.05
				_ = m.View()
			}
		})
	}
}
