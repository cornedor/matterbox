package ui

import (
	"fmt"
	"image/color"
	"os"
	"testing"

	"matterbox/internal/stl"
)

// Real-file profiling harness. Point MB_STL_DIR at a directory of .stl files:
//
//	MB_STL_DIR=/tmp/stl go test ./internal/ui -run xxx -bench STLReal -benchtime 20x
func realMesh(tb testing.TB, name string) *stl.Mesh {
	dir := os.Getenv("MB_STL_DIR")
	if dir == "" {
		tb.Skip("set MB_STL_DIR")
	}
	raw, err := os.ReadFile(dir + "/" + name)
	if err != nil {
		tb.Fatal(err)
	}
	m, err := stl.Decode(raw)
	if err != nil {
		tb.Fatal(err)
	}
	return m
}

func BenchmarkSTLRealParse(b *testing.B) {
	dir := os.Getenv("MB_STL_DIR")
	if dir == "" {
		b.Skip("set MB_STL_DIR")
	}
	for _, name := range []string{"eiffel.stl", "suzanne.stl"} {
		raw, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := stl.Decode(raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSTLReal(b *testing.B) {
	for _, name := range []string{"eiffel.stl", "suzanne.stl"} {
		mesh := realMesh(b, name)
		b.Logf("%s: %d tris closed=%v", name, len(mesh.Tris), mesh.Closed)
		for _, box := range [][2]int{{872, 656}, {1200, 900}, {1560, 1170}} {
			for _, ssaa := range []int{1, 0} {
				var r stl.Renderer
				st := stl.Style{Base: color.RGBA{0x93, 0xa7, 0xcc, 0xff}, SSAA: ssaa}
				cam := stl.DefaultCamera()
				b.Run(fmt.Sprintf("%s/%dx%d/ssaa=%d/render", name, box[0], box[1], ssaa), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						cam.Yaw += 0.02
						r.Render(mesh, cam, st, box[0], box[1])
					}
				})
				img := r.Render(mesh, cam, st, box[0], box[1])
				var n int
				b.Run(fmt.Sprintf("%s/%dx%d/ssaa=%d/encode", name, box[0], box[1], ssaa), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						seq, err := kittyEditFrameRaw(9, 2, img)
						if err != nil {
							b.Fatal(err)
						}
						n = len(seq)
					}
					b.ReportMetric(float64(n)/1024, "KiB/frame")
				})
			}
		}
	}
}

// realBox is the pixel box the modal actually gets here: ghostty at 215x52 cells
// with 16x40 px cells => sizeSTLView picks 143x43 cells => 2288x1720 px, i.e.
// 3.9 Mpx a frame. Override with MB_STL_BOX=WxH.
func realBox() (int, int) {
	if v := os.Getenv("MB_STL_BOX"); v != "" {
		var w, h int
		if _, err := fmt.Sscanf(v, "%dx%d", &w, &h); err == nil && w > 0 && h > 0 {
			return w, h
		}
	}
	return 2288, 1720
}

// BenchmarkSTLRealBox is one drag frame — rasterize then encode — at that box.
func BenchmarkSTLRealBox(b *testing.B) {
	w, h := realBox()
	for _, name := range []string{"eiffel.stl", "suzanne.stl"} {
		mesh := realMesh(b, name)
		b.Logf("%s: %d tris closed=%v at %dx%d", name, len(mesh.Tris), mesh.Closed, w, h)
		var r stl.Renderer
		st := stl.Style{Base: color.RGBA{0x93, 0xa7, 0xcc, 0xff}, SSAA: 1}
		cam := stl.DefaultCamera()
		b.Run(name+"/render", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				cam.Yaw += 0.02
				r.Render(mesh, cam, st, w, h)
			}
		})
		img := r.Render(mesh, cam, st, w, h)
		var n int
		b.Run(name+"/encode", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				seq, err := kittyEditFrameRaw(9, 2, img)
				if err != nil {
					b.Fatal(err)
				}
				n = len(seq)
			}
			b.ReportMetric(float64(n)/1024, "KiB/frame")
		})
	}
}
