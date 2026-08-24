package ui

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"io"
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
// scratch row per frame. A zlib writer is ~1MB of window and tables.
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
	out.Grow(len(img.Pix) / 8)

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

// kittyEditFrameRaw is kittyEditFrame's payload as raw zlib'd RGBA rather than a
// PNG. Same command otherwise — see that function for what r=, X=1 and q=1 are
// doing — plus the s=/v= the protocol needs to make sense of pixels that carry
// no header of their own.
func kittyEditFrameRaw(id uint32, frame int, img *image.RGBA) (string, error) {
	payload, err := kittyRawPayload(img)
	if err != nil {
		return "", err
	}
	opts := []string{
		"a=f", fmt.Sprintf("i=%d", id), fmt.Sprintf("r=%d", frame),
		fmt.Sprintf("f=%d", kitty.RGBA),
		fmt.Sprintf("s=%d", img.Rect.Dx()), fmt.Sprintf("v=%d", img.Rect.Dy()),
		fmt.Sprintf("o=%c", kitty.Zlib), "X=1", "q=1",
	}
	return kittyChunkRaw(payload, opts, 1), nil
}
