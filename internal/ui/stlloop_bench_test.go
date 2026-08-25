package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// What one drag event actually costs the program, as opposed to what the
// rasterizer and the encoder cost. bubbletea calls View() synchronously after
// every Update — a mouse motion, the frame that motion asked for, and the RawMsg
// that writes it are three separate messages — so if View() is expensive with the
// viewer open, it is paid several times per frame no matter how fast a frame
// renders.
//
// Sized to the box a HiDPI ghostty really gives it: 215x52 cells, 16x40px each,
// which sizeSTLView turns into a 143x43 placeholder grid.
func stlLoopModel(b *testing.B) *Model {
	b.Helper()
	m := thumbModel()
	m.cellPxW, m.cellPxH = 16, 40
	m.width, m.height = 215, 52
	next, _ := m.openSTLView([]previewItem{
		{file: stlFile("f1", "part.stl", "", ".stl", 4096), name: "part.stl"},
	}, 0)
	out := next.(Model)
	out.stl.loading = false
	out.stl.mesh = benchMesh(140_000)
	out.sizeSTLView()
	return &out
}

func BenchmarkSTLViewerLoop(b *testing.B) {
	m := stlLoopModel(b)
	b.Logf("placeholder grid %dx%d cells", m.stl.cols, m.stl.rows)

	b.Run("kittyPlaceholder", func(b *testing.B) {
		b.ReportAllocs()
		var n int
		for b.Loop() {
			n = len(kittyPlaceholder(m.stl.imgID, m.stl.rows, m.stl.cols))
		}
		b.ReportMetric(float64(n)/1024, "KiB")
	})
	b.Run("renderSTLView", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			m.renderSTLView()
		}
	})
	b.Run("View", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			m.View()
		}
	})
	// The three messages one dragged frame actually costs, each followed by the
	// View() bubbletea runs the moment Update returns.
	b.Run("drag/frame-of-messages", func(b *testing.B) {
		b.ReportAllocs()
		cur := *m
		cur.stl.drag = true
		cur.vcache = &viewCache{}
		msgs := []tea.Msg{
			tea.MouseMotionMsg{X: 40, Y: 20, Button: tea.MouseLeft},
			stlFrameMsg{},
			tea.RawMsg{Msg: "\x1b_Ga=a\x1b\\"},
		}
		for b.Loop() {
			for _, msg := range msgs {
				next, _ := cur.Update(msg)
				cur = next.(Model)
				cur.View()
			}
		}
	})
}
