package svgimg

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"os/exec"
	"sort"
	"testing"

	xdraw "golang.org/x/image/draw"
)

// TestCorpus scores our rendering against librsvg over a list of real SVG files,
// which is how all four of the vendored renderer's bugs were found: a synthetic
// test says whether we draw what we meant to, and this says whether what we meant
// to matches what a browser would show.
//
// Opt-in, because it needs both a corpus and an ImageMagick built against
// librsvg, neither of which is a dependency of the project:
//
//	find /usr/share -name '*.svg' | shuf -n 600 > /tmp/corpus.txt
//	SVG_CORPUS=/tmp/corpus.txt go test ./internal/svgimg -run TestCorpus -v
//
// It reports rather than asserts. The buckets are mean absolute difference per
// channel; the last measurement was 97 near-identical, 357 close, 130 poor of
// 585. Most of the poor tail is documents whose colour comes from a CSS class the
// renderer does not resolve, so the shapes are right and the palette is not.
func TestCorpus(t *testing.T) {
	list := os.Getenv("SVG_CORPUS")
	if list == "" {
		t.Skip("set SVG_CORPUS to a file listing .svg paths")
	}
	data, _ := os.ReadFile(list)
	type entry struct {
		path   string
		d      float64
		stroke bool
	}
	var worst []entry
	var good, fair, poor, failed int
	for _, p := range bytes.Fields(data) {
		path := string(p)
		raw, err := os.ReadFile(path)
		if err != nil || !Looks(raw) {
			continue
		}
		res, err := Decode(raw, Options{MaxW: 128, MaxH: 128, CurrentColor: "#000000"})
		if err != nil {
			failed++
			continue
		}
		b := res.Image.Bounds()
		ref, err := librsvg(path, b.Dx(), b.Dy())
		if err != nil {
			continue
		}
		d := mad(flatten(res.Image, b.Dx(), b.Dy()), flatten(ref, b.Dx(), b.Dy()))
		switch {
		case d < 0.02:
			good++
		case d < 0.08:
			fair++
		default:
			poor++
			worst = append(worst, entry{path, d, bytes.Contains(raw, []byte("stroke"))})
		}
	}
	sort.Slice(worst, func(i, j int) bool { return worst[i].d > worst[j].d })
	var stroked int
	for _, w := range worst {
		if w.stroke {
			stroked++
		}
	}
	for i, w := range worst {
		if i >= 10 {
			break
		}
		t.Logf("  worst %.3f stroke=%-5v %s", w.d, w.stroke, w.path)
	}
	t.Logf("  of %d poor, %d mention stroke", len(worst), stroked)
	t.Logf("vs librsvg: near-identical(<2%%)=%d close(<8%%)=%d poor=%d failed=%d total=%d",
		good, fair, poor, failed, good+fair+poor+failed)
}

func flatten(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return dst
}

func librsvg(path string, w, h int) (image.Image, error) {
	cmd := exec.Command("magick", "-background", "white", "-density", "300", path,
		"-resize", fmt.Sprintf("%dx%d!", w, h), "-flatten", "png:-")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return png.Decode(&buf)
}

func mad(a, b *image.RGBA) float64 {
	var sum float64
	for i := 0; i < len(a.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			sum += math.Abs(float64(a.Pix[i+c]) - float64(b.Pix[i+c]))
		}
	}
	return sum / float64(len(a.Pix)/4*3) / 255
}
