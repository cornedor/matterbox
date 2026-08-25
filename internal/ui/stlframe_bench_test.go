package ui

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/color"
	"math"
	"testing"

	"matterbox/internal/stl"
)

// One frame of the interactive 3D viewer, end to end — what stlFrameCmd's
// closure does: rasterize, then encode and chunk it for the terminal. ssaa=1 is
// the drag case (the camera is moving), ssaa=0 is auto, which is what a settled
// camera renders at.
//
// Both halves are now spread over the cores (renderWorkers, rawStrips), so these
// measure a frame as the viewer actually pays for it. The split benchmark below
// separates them, and stlreal_bench_test.go runs the same thing on real files at
// the pixel box a HiDPI terminal really hands the modal — which is where the
// numbers that mattered came from: a tessellated sphere is not a downloaded part.

// benchMesh builds a tessellated sphere of roughly n triangles, through the real
// decoder so the Mesh is bounded exactly as a loaded file would be.
func benchMesh(tris int) *stl.Mesh {
	seg := int(math.Sqrt(float64(tris) / 2))
	type v3 struct{ x, y, z float32 }
	at := func(i, j int) v3 {
		u := float64(i) / float64(seg) * 2 * math.Pi
		w := float64(j) / float64(seg) * math.Pi
		return v3{
			float32(math.Sin(w) * math.Cos(u) * 30),
			float32(math.Cos(w) * 30),
			float32(math.Sin(w) * math.Sin(u) * 30),
		}
	}
	var facets []v3
	for i := range seg {
		for j := range seg {
			a, b, c, d := at(i, j), at(i+1, j), at(i+1, j+1), at(i, j+1)
			facets = append(facets, a, b, c, a, c, d)
		}
	}
	var buf bytes.Buffer
	buf.Write(make([]byte, 80))
	binary.Write(&buf, binary.LittleEndian, uint32(len(facets)/3))
	for i := 0; i < len(facets); i += 3 {
		binary.Write(&buf, binary.LittleEndian, [3]float32{}) // normal, recomputed on draw
		for _, v := range facets[i : i+3] {
			binary.Write(&buf, binary.LittleEndian, [3]float32{v.x, v.y, v.z})
		}
		binary.Write(&buf, binary.LittleEndian, uint16(0))
	}
	m, err := stl.Decode(buf.Bytes())
	if err != nil {
		panic(err)
	}
	return m
}

func BenchmarkSTLDragFrame(b *testing.B) {
	for _, tris := range []int{5_000, 50_000, 200_000} {
		for _, box := range [][2]int{{872, 656}, {1200, 900}} {
			for _, ssaa := range []int{1, 0} {
				name := fmt.Sprintf("tris=%d/%dx%d/ssaa=%d", tris, box[0], box[1], ssaa)
				b.Run(name, func(b *testing.B) {
					mesh := benchMesh(tris)
					var r stl.Renderer
					st := stl.Style{Base: color.RGBA{0x93, 0xa7, 0xcc, 0xff}, SSAA: ssaa}
					cam := stl.DefaultCamera()
					var out int
					b.ReportAllocs()
					for b.Loop() {
						cam.Yaw += 0.02
						img := r.Render(mesh, cam, st, box[0], box[1])
						seq, err := kittyEditFrameRaw(9, 2, img)
						if err != nil {
							b.Fatal(err)
						}
						out = len(seq)
					}
					b.ReportMetric(float64(out)/1024, "KiB/frame")
				})
			}
		}
	}
}

// BenchmarkSTLFrameSplit separates the rasterizer from the encode, since they
// are fixed by completely different things — mesh size versus pixel box.
func BenchmarkSTLFrameSplit(b *testing.B) {
	mesh := benchMesh(50_000)
	var r stl.Renderer
	st := stl.Style{Base: color.RGBA{0x93, 0xa7, 0xcc, 0xff}, SSAA: 1}
	cam := stl.DefaultCamera()
	const w, h = 872, 656
	b.Run("render", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			cam.Yaw += 0.02
			r.Render(mesh, cam, st, w, h)
		}
	})
	img := r.Render(mesh, cam, st, w, h)
	b.Run("encode/raw", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := kittyEditFrameRaw(9, 2, img); err != nil {
				b.Fatal(err)
			}
		}
	})
	// What it used to cost, kept as the comparison the switch was made on.
	b.Run("encode/png", func(b *testing.B) {
		b.ReportAllocs()
		var buf bytes.Buffer
		for b.Loop() {
			buf.Reset()
			if err := kittyPNG.Encode(&buf, img); err != nil {
				b.Fatal(err)
			}
		}
	})
}
