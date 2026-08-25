package ui

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"io"
	"runtime"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi/kitty"
)

// Raw RGBA behind zlib (f=32, o=z) instead of PNG, for the frames the 3D viewer
// pushes out during a drag.
//
// PNG is the right default everywhere else in this package — a photograph or a
// screenshot compresses far better with a per-row filter in front of the
// deflate. A rendered frame is the opposite kind of picture: flat shaded, mostly
// transparent background, no noise. The filter search finds nothing to predict
// and costs a pass over every row trying, which on the real files this was
// measured against is most of the encode:
//
//	1200×900 frame   PNG 25ms / 83 KiB   ·   zlib-raw 6.4ms / 80 KiB
//
// Faster *and* slightly smaller, so there is no trade to weigh. Only the viewer's
// per-frame edits use it; the transmit that establishes the image stays on the
// PNG path every other image in the app already goes through, so the one thing
// standing between the user and an empty modal is not also the new thing.

// zlibWriters and rawRows keep a drag from handing the GC a deflate state and a
// scratch row per frame. A zlib writer is ~1MB of window and tables, and a frame
// is encoded in as many strips as there are cores (see rawStrips), so an open
// viewer on a 12-core machine holds ~11MB of them between frames.
var (
	zlibWriters = sync.Pool{New: func() any {
		w, _ := zlib.NewWriterLevel(io.Discard, flate.BestSpeed)
		return w
	}}
	rawRows sync.Pool
)

// kittyRawPayload encodes a frame as the protocol's f=32,o=z wants it: straight
// alpha pixel bytes, one zlib stream, base64.
//
// image.RGBA is alpha-*pre*multiplied and the protocol is not, so the conversion
// can't be skipped — it is what png.Encode does on the way out too. It only
// costs anything on partially covered pixels, which at SSAA 1 (every frame of a
// drag) is none of them: the rasterizer writes opaque or leaves background.
func kittyRawPayload(img *image.RGBA) ([]byte, error) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("encode kitty raw frame: empty image %v", img.Rect)
	}
	var out bytes.Buffer
	// A frame this flat deflates to roughly an eighth. Sized from the rectangle
	// rather than len(img.Pix), which on a strip (a SubImage) is everything from
	// the strip's first pixel to the end of the whole frame's buffer — reserving
	// on that would have the first strip of a 16MB frame ask for 2MB.
	out.Grow(4 * w * h / 8)

	zw := zlibWriters.Get().(*zlib.Writer)
	defer zlibWriters.Put(zw)
	zw.Reset(&out)

	line, _ := rawRows.Get().(*[]byte)
	if line == nil || cap(*line) < 4*w {
		buf := make([]byte, 4*w)
		line = &buf
	}
	defer rawRows.Put(line)
	row := (*line)[:4*w]

	for y := range h {
		unpremultiply(row, img.Pix[y*img.Stride:][:4*w])
		if _, err := zw.Write(row); err != nil {
			return nil, fmt.Errorf("encode kitty raw frame: %w", err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("encode kitty raw frame: %w", err)
	}

	payload := make([]byte, base64.StdEncoding.EncodedLen(out.Len()))
	base64.StdEncoding.Encode(payload, out.Bytes())
	return payload, nil
}

// unpremultiply divides the colour back out by the alpha it was multiplied into.
// Fully opaque and fully transparent pixels — everything a frame without
// supersampling contains — are the same either way and are copied straight
// through, so the divide is paid only along a supersampled silhouette.
func unpremultiply(dst, src []byte) {
	for i := 0; i < len(src); i += 4 {
		a := src[i+3]
		if a == 0xff || a == 0 {
			copy(dst[i:i+4], src[i:i+4])
			continue
		}
		d := uint32(a)
		dst[i] = uint8(uint32(src[i]) * 0xff / d)
		dst[i+1] = uint8(uint32(src[i+1]) * 0xff / d)
		dst[i+2] = uint8(uint32(src[i+2]) * 0xff / d)
		dst[i+3] = a
	}
}

// rawStrips picks how many horizontal strips a frame is encoded in — see
// kittyEditFrameRaw for why it is cut up at all.
//
// One core is left free, for the same reason renderWorkers leaves one: the drag
// that asked for the frame is still being handled while this runs. Strips under
// 64 rows are not worth having; below a quarter of a megapixel the whole encode
// is a millisecond or two and the goroutines would be most of it.
func rawStrips(w, h int) int {
	n := runtime.GOMAXPROCS(0) - 1
	if n <= 1 || w*h < 1<<18 {
		return 1
	}
	return min(n, max(h/64, 1))
}

// kittyEditFrameRaw is kittyEditFrame's payload as raw zlib'd RGBA rather than a
// PNG. Same command otherwise — see that function for what r=, X=1 and q=1 are
// doing — plus the s=/v= the protocol needs to make sense of pixels that carry
// no header of their own.
//
// A large frame goes out as several horizontal strips, each its own zlib stream,
// each placed with y=. Deflate is the single most expensive thing in a drag
// frame — 20ms of a 28ms encode at 2288×1720 — and one stream is inherently one
// core, so cutting the frame up is the only way to spread it. Measured on a real
// 140k-facet part at that size: 27.6ms → 5.3ms across 12 strips, for 0.3% more
// bytes (a fresh deflate window per strip is all it costs).
//
// This stays atomic on screen. The protocol takes x=/y=/s=/v= as the rectangle
// of the frame being written, so every strip lands in the same not-currently-
// displayed spare frame, and the a=a that switches the placement onto it comes
// after all of them (see stlFrameSeq). The viewer never shows a half-written
// frame — which is the entire point of the spare (see stlState).
func kittyEditFrameRaw(id uint32, frame int, img *image.RGBA) (string, error) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w <= 0 || h <= 0 {
		return "", fmt.Errorf("encode kitty raw frame: empty image %v", img.Rect)
	}
	n := rawStrips(w, h)
	if n <= 1 {
		payload, err := kittyRawPayload(img)
		if err != nil {
			return "", err
		}
		return kittyChunkRaw(payload, kittyRawFrameOpts(id, frame, w, h, -1), 1), nil
	}
	parts := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			y0, y1 := i*h/n, (i+1)*h/n
			r := image.Rect(img.Rect.Min.X, img.Rect.Min.Y+y0, img.Rect.Max.X, img.Rect.Min.Y+y1)
			payload, err := kittyRawPayload(img.SubImage(r).(*image.RGBA))
			if err != nil {
				errs[i] = err
				return
			}
			parts[i] = kittyChunkRaw(payload, kittyRawFrameOpts(id, frame, w, y1-y0, y0), 1)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return "", err
		}
	}
	return strings.Join(parts, ""), nil
}

// kittyRawFrameOpts is the first-chunk key list for one frame edit. A negative y
// means the whole frame, which leaves x=/y= off entirely: the protocol defaults
// both to 0, and a frame that isn't being cut up should go out as exactly the
// command it always did.
func kittyRawFrameOpts(id uint32, frame, w, rows, y int) []string {
	opts := []string{
		"a=f", fmt.Sprintf("i=%d", id), fmt.Sprintf("r=%d", frame),
		fmt.Sprintf("f=%d", kitty.RGBA),
		fmt.Sprintf("s=%d", w), fmt.Sprintf("v=%d", rows),
	}
	if y >= 0 {
		opts = append(opts, "x=0", fmt.Sprintf("y=%d", y))
	}
	return append(opts, fmt.Sprintf("o=%c", kitty.Zlib), "X=1", "q=1")
}
