package ui

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"strconv"
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

// --- strip-parallel frame edits -------------------------------------------

// kittyCmd is one reassembled APC: its first chunk's keys and the payload of
// every chunk joined back together.
type kittyCmd struct {
	opts    map[string]string
	payload []byte
}

// parseKittySeq splits a string of Kitty graphics commands back into commands,
// rejoining the m=1/m=0 chunk runs the transmit is cut into.
func parseKittySeq(t *testing.T, seq string) []kittyCmd {
	t.Helper()
	var out []kittyCmd
	var cur *kittyCmd
	for _, block := range strings.Split(seq, "\x1b_G")[1:] {
		body, ok := strings.CutSuffix(block, "\x1b\\")
		if !ok {
			t.Fatalf("APC block not terminated: %q", block)
		}
		keys, payload, _ := strings.Cut(body, ";")
		opts := map[string]string{}
		for _, kv := range strings.Split(keys, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				opts[k] = v
			}
		}
		if cur == nil {
			out = append(out, kittyCmd{opts: opts})
			cur = &out[len(out)-1]
		}
		cur.payload = append(cur.payload, payload...)
		if opts["m"] != "1" {
			cur = nil
		}
	}
	if cur != nil {
		t.Fatal("last command never closed with m=0")
	}
	return out
}

// A striped frame has to be the same pixels as an unstriped one, laid into the
// same frame: every row written exactly once, in the right place. Anything else
// and the model comes out sheared, doubled, or with a band of the frame before it
// still showing.
func TestKittyEditFrameRawStripsCoverTheFrameExactly(t *testing.T) {
	const w, h = 1024, 512
	if n := rawStrips(w, h); n < 2 {
		t.Skipf("machine encodes %dx%d in %d strip(s)", w, h, n)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 0xff})
		}
	}
	seq, err := kittyEditFrameRaw(77, 3, img)
	if err != nil {
		t.Fatal(err)
	}
	cmds := parseKittySeq(t, seq)
	if len(cmds) != rawStrips(w, h) {
		t.Fatalf("got %d commands, want %d strips", len(cmds), rawStrips(w, h))
	}

	got := make([]byte, 4*w*h)
	covered := make([]int, h)
	for i, c := range cmds {
		for k, want := range map[string]string{
			"a": "f", "i": "77", "r": "3", "f": "32", "o": "z", "X": "1", "q": "1",
			"x": "0", "s": "1024",
		} {
			if c.opts[k] != want {
				t.Errorf("strip %d: %s=%q, want %q", i, k, c.opts[k], want)
			}
		}
		y0, err := strconv.Atoi(c.opts["y"])
		if err != nil {
			t.Fatalf("strip %d: y=%q: %v", i, c.opts["y"], err)
		}
		rows, err := strconv.Atoi(c.opts["v"])
		if err != nil {
			t.Fatalf("strip %d: v=%q: %v", i, c.opts["v"], err)
		}
		pix := decodeRawPayload(t, c.payload)
		if len(pix) != 4*w*rows {
			t.Fatalf("strip %d: %d bytes for %d rows of %d", i, len(pix), rows, w)
		}
		copy(got[4*w*y0:], pix)
		for y := y0; y < y0+rows; y++ {
			covered[y]++
		}
	}
	for y, n := range covered {
		if n != 1 {
			t.Fatalf("row %d written %d times", y, n)
		}
	}
	// The reference is what a single stream sends for the same image.
	if want := decodeRawPayload(t, mustRawPayload(t, img)); !bytes.Equal(got, want) {
		t.Error("reassembled strips differ from the whole-frame encoding")
	}
}

// A frame small enough not to be cut up must go out as exactly the command it
// went out as before there were strips — the fallback path is the proven one.
func TestKittyEditFrameRawSmallFrameIsUnstriped(t *testing.T) {
	seq, err := kittyEditFrameRaw(4242, 3, alphaFrame())
	if err != nil {
		t.Fatal(err)
	}
	cmds := parseKittySeq(t, seq)
	if len(cmds) != 1 {
		t.Fatalf("got %d commands for a 4x2 frame, want 1", len(cmds))
	}
	for _, k := range []string{"x", "y"} {
		if v, ok := cmds[0].opts[k]; ok {
			t.Errorf("unstriped frame carries %s=%s", k, v)
		}
	}
}
