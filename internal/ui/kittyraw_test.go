package ui

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"
)

// alphaFrame has one pixel of each kind a supersampled render produces:
// transparent, opaque, and partially covered.
func alphaFrame() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	img.SetRGBA(1, 0, color.RGBA{0xc8, 0x64, 0x32, 0xff}) // opaque
	img.SetRGBA(2, 0, color.RGBA{0x64, 0x32, 0x19, 0x80}) // half covered, premultiplied
	img.SetRGBA(3, 0, color.RGBA{0x08, 0x04, 0x02, 0x10}) // barely covered
	return img
}

func decodeRawPayload(t *testing.T, payload []byte) []byte {
	t.Helper()
	raw := make([]byte, base64.StdEncoding.DecodedLen(len(payload)))
	n, err := base64.StdEncoding.Decode(raw, payload)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw[:n]))
	if err != nil {
		t.Fatalf("zlib: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("inflate: %v", err)
	}
	return out
}

// The protocol wants straight alpha and image.RGBA holds premultiplied, so the
// conversion is the whole job — and the reference for it is what the PNG path
// this replaces already sends.
func TestKittyRawMatchesThePNGItReplaces(t *testing.T) {
	img := alphaFrame()
	payload, err := kittyRawPayload(img)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeRawPayload(t, payload)
	if want := 4 * 4 * 2; len(got) != want {
		t.Fatalf("decoded %d bytes, want %d", len(got), want)
	}

	var buf bytes.Buffer
	if err := kittyPNG.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	ref, err := png.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	nrgba, ok := ref.(*image.NRGBA)
	if !ok {
		t.Fatalf("png decoded to %T, want *image.NRGBA", ref)
	}
	for i := range got {
		if got[i] != nrgba.Pix[i] {
			t.Fatalf("byte %d (pixel %d, channel %d) = %#x, PNG sends %#x",
				i, i/4, i%4, got[i], nrgba.Pix[i])
		}
	}
}

// A frame without supersampling is all opaque-or-nothing, which is the fast path
// through the conversion; it still has to come out byte for byte right.
func TestKittyRawOpaqueAndTransparent(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 1))
	img.SetRGBA(0, 0, color.RGBA{0x11, 0x22, 0x33, 0xff})
	img.SetRGBA(2, 0, color.RGBA{0x44, 0x55, 0x66, 0xff})
	got := decodeRawPayload(t, mustRawPayload(t, img))
	want := []byte{0x11, 0x22, 0x33, 0xff, 0, 0, 0, 0, 0x44, 0x55, 0x66, 0xff}
	if !bytes.Equal(got, want) {
		t.Errorf("got % x, want % x", got, want)
	}
}

func mustRawPayload(t *testing.T, img *image.RGBA) []byte {
	t.Helper()
	p, err := kittyRawPayload(img)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Raw pixels carry no header, so the command has to say how big they are and how
// they were compressed — omit either and the terminal rejects the frame.
func TestKittyEditFrameRawOptions(t *testing.T) {
	seq, err := kittyEditFrameRaw(4242, 3, alphaFrame())
	if err != nil {
		t.Fatal(err)
	}
	opts, _, _ := strings.Cut(strings.TrimPrefix(seq, "\x1b_G"), ";")
	for _, want := range []string{"a=f", "i=4242", "r=3", "f=32", "s=4", "v=2", "o=z", "X=1", "q=1"} {
		if !strings.Contains(opts, want) {
			t.Errorf("missing %q in %q", want, opts)
		}
	}
}

// An empty box is a real state (a viewer sized before it has a mesh); it must
// come back as an error rather than a zero-length frame the terminal chokes on.
func TestKittyRawRejectsEmpty(t *testing.T) {
	if _, err := kittyRawPayload(image.NewRGBA(image.Rect(0, 0, 0, 0))); err == nil {
		t.Error("encoded an empty image without complaint")
	}
}

// The pools have to survive being reused at different widths — a resize hands
// the scratch row a bigger frame than the one before it.
func TestKittyRawReusesAcrossSizes(t *testing.T) {
	for _, w := range []int{4, 64, 8, 128, 2} {
		img := image.NewRGBA(image.Rect(0, 0, w, 3))
		img.SetRGBA(w-1, 2, color.RGBA{9, 9, 9, 0xff})
		got := decodeRawPayload(t, mustRawPayload(t, img))
		if len(got) != w*3*4 {
			t.Fatalf("width %d decoded to %d bytes, want %d", w, len(got), w*3*4)
		}
		if last := got[len(got)-4:]; last[0] != 9 || last[3] != 0xff {
			t.Errorf("width %d: last pixel = % x", w, last)
		}
	}
}
