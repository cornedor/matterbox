package ui

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

func testFrame(w, h int, seed byte) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{byte(x) + seed, byte(y) + seed, byte(x ^ y), 0xff})
		}
	}
	return img
}

// extractPayload strips one APC's framing (\x1b_G<opts>;<payload>\x1b\\) and
// returns the option string and the raw payload, for tests that need to look
// inside a single (non-chunked) transmit.
func extractPayload(t *testing.T, apc string) (opts, payload string) {
	t.Helper()
	if !strings.HasPrefix(apc, "\x1b_G") || !strings.HasSuffix(apc, "\x1b\\") {
		t.Fatalf("not a well-formed Kitty APC: %.40q", apc)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(apc, "\x1b_G"), "\x1b\\")
	i := strings.IndexByte(body, ';')
	if i < 0 {
		return body, ""
	}
	return body[:i], body[i+1:]
}

// TestKittyTransmitFrameRoundTrips checks that kittyTransmitFrame's payload is a
// valid, decodable PNG of the source image — the same guarantee
// TestKittyTransmitProductionEncoderStillDecodes gives the still-image path — and
// that its options carry a=f (not a=T), the target image id, X=1 (replace, not
// blend), and the requested gap in milliseconds.
func TestKittyTransmitFrameRoundTrips(t *testing.T) {
	img := testFrame(40, 30, 10)
	seq, err := kittyTransmitFrame(&kittyPNG, 0xABCDEF, img, 120*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	opts, payload := extractPayload(t, seq)
	for _, want := range []string{"a=f", "i=11259375", "f=100", "X=1", "z=120"} {
		if !strings.Contains(opts, want) {
			t.Errorf("options %q missing %q", opts, want)
		}
	}
	if strings.Contains(opts, "a=t") || strings.Contains(opts, "a=T") {
		t.Errorf("frame transmit must not carry a plain transmit action: %q", opts)
	}

	raw, err := decodeBase64Payload(payload)
	if err != nil {
		t.Fatalf("payload isn't valid base64: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("payload isn't a valid PNG: %v", err)
	}
	if decoded.Bounds() != img.Bounds() {
		t.Errorf("decoded bounds %v != source bounds %v", decoded.Bounds(), img.Bounds())
	}
}

// TestKittyTransmitFrameClampsNonPositiveGap checks the z=0-means-unspecified
// footgun the protocol calls out: a non-positive gap must still produce an
// explicit (if minimal) z, not 0 or a negative number.
func TestKittyTransmitFrameClampsNonPositiveGap(t *testing.T) {
	for _, gap := range []time.Duration{0, -5 * time.Millisecond} {
		seq, err := kittyTransmitFrame(&kittyPNG, 1, testFrame(4, 4, 0), gap)
		if err != nil {
			t.Fatal(err)
		}
		opts, _ := extractPayload(t, seq)
		if strings.Contains(opts, "z=0") || strings.Contains(opts, "z=-") {
			t.Errorf("gap %v produced non-positive z in %q", gap, opts)
		}
		if !strings.Contains(opts, "z=1") {
			t.Errorf("gap %v: want a clamped z=1, got %q", gap, opts)
		}
	}
}

// TestKittySetRootGapAndAnimateStart pin the exact control APCs against the
// protocol's documented keys: r=1 always (the root frame is always frame 1),
// s=3/v=1 for "run, loop forever" — see kittyAnimateStart's doc comment for why
// we always request infinite looping rather than plumbing a GIF's own loop count
// through.
func TestKittySetRootGapAndAnimateStart(t *testing.T) {
	got := kittySetRootGap(42, 48*time.Millisecond)
	want := "\x1b_Ga=a,i=42,r=1,z=48,q=2\x1b\\"
	if got != want {
		t.Errorf("kittySetRootGap:\n got %q\nwant %q", got, want)
	}

	got = kittyAnimateStart(42)
	want = "\x1b_Ga=a,i=42,s=3,v=1,q=2\x1b\\"
	if got != want {
		t.Errorf("kittyAnimateStart:\n got %q\nwant %q", got, want)
	}
}

// TestBuildNativeAnimSetupSendsRemainingFramesAndStarts checks the shape of the
// setup blob for an already-transmitted root: one a=f per frame after the first,
// each carrying that frame's own delay; the root's own gap (delays[0]) set via
// the animation control code, since a=T can't carry it; and the start command
// last, so the terminal has every frame before it's told to loop.
func TestBuildNativeAnimSetupSendsRemainingFramesAndStarts(t *testing.T) {
	frames := []image.Image{testFrame(8, 8, 0), testFrame(8, 8, 1), testFrame(8, 8, 2)}
	delays := []time.Duration{30 * time.Millisecond, 40 * time.Millisecond, 50 * time.Millisecond}
	setup, err := buildNativeAnimSetup(&kittyPNG, 7, frames, delays)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(setup, "a=f"); n != len(frames)-1 {
		t.Errorf("want %d frame appends (frames[1:]), got %d", len(frames)-1, n)
	}
	if n := strings.Count(setup, "f=100"); n != len(frames)-1 {
		t.Errorf("every appended frame must declare f=100 (PNG) or the terminal defaults to "+
			"headerless raw RGBA and rejects it for missing dimensions; want %d, got %d", len(frames)-1, n)
	}
	if !strings.Contains(setup, "z=40") || !strings.Contains(setup, "z=50") {
		t.Errorf("missing per-frame gaps for frames 2/3 in %q", setup)
	}
	rootGap := "a=a,i=7,r=1,z=30"
	if !strings.Contains(setup, rootGap) {
		t.Errorf("missing root gap %q in %q", rootGap, setup)
	}
	start := "a=a,i=7,s=3,v=1"
	if !strings.Contains(setup, start) {
		t.Errorf("missing animate-start %q in %q", start, setup)
	}
	if strings.Index(setup, start) < strings.Index(setup, rootGap) {
		t.Error("animate-start must come after the root gap is set")
	}
	// Every a=f must come before the start command — the terminal shouldn't be
	// told to run before it has all the frames.
	if strings.LastIndex(setup, "a=f") > strings.Index(setup, start) {
		t.Error("a start command was placed before a frame append")
	}
}

// TestBuildNativeAnimSetupRejectsMismatch checks the guard against a caller
// passing mismatched or too-short frames/delays, which would otherwise silently
// build a malformed (or looping-forever-on-one-frame) animation.
func TestBuildNativeAnimSetupRejectsMismatch(t *testing.T) {
	one := []image.Image{testFrame(4, 4, 0)}
	oneDelay := []time.Duration{10 * time.Millisecond}
	if _, err := buildNativeAnimSetup(&kittyPNG, 1, one, oneDelay); err == nil {
		t.Error("want an error for a single frame (nothing to animate)")
	}
	two := []image.Image{testFrame(4, 4, 0), testFrame(4, 4, 1)}
	if _, err := buildNativeAnimSetup(&kittyPNG, 1, two, oneDelay); err == nil {
		t.Error("want an error for mismatched frames/delays lengths")
	}
}

// TestBuildNativeAnimSetupNeverTransmitsARoot checks the invariant every call
// site relies on: buildNativeAnimSetup only ever appends frames and issues
// control commands (a=f / a=a) — it must never itself carry a root transmit
// (a=T/a=t with a placement), since every caller has already sent the root
// separately, on its own, so that the small, fast root gets a chance to
// resolve before this — potentially much larger — follow-up arrives. See its
// doc comment for why bundling the two caused Kitty to warn "missing image for
// virtual placement".
func TestBuildNativeAnimSetupNeverTransmitsARoot(t *testing.T) {
	frames := []image.Image{testFrame(10, 10, 0), testFrame(10, 10, 1), testFrame(10, 10, 2)}
	delays := []time.Duration{25 * time.Millisecond, 35 * time.Millisecond, 45 * time.Millisecond}
	setup, err := buildNativeAnimSetup(&kittyPNG, 99, frames, delays)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(setup, "U=1") {
		t.Error("buildNativeAnimSetup must never carry a placement (U=1) — that belongs to the root, sent separately")
	}
	for _, apc := range strings.Split(setup, "\x1b_G")[1:] {
		opts, _, _ := strings.Cut(apc, ";")
		if strings.Contains(opts, "a=T") || strings.Contains(opts, "a=t") {
			t.Errorf("buildNativeAnimSetup issued a root transmit: %q", opts)
		}
	}
}

// TestKittyChunkRawExactMultipleTerminates covers the same footgun
// TestKittyChunkMatchesReadFullLoop guards for kittyChunk: a payload that is an
// exact multiple of the chunk size must still end in an empty, m=0-terminated
// chunk, or the terminal is left waiting for a continuation that never arrives.
func TestKittyChunkRawExactMultipleTerminates(t *testing.T) {
	const k = 4096 // ansi/kitty.MaxChunkSize
	for _, n := range []int{0, k - 1, k, k + 1, 2 * k} {
		payload := bytes.Repeat([]byte("A"), n)
		got := kittyChunkRaw(payload, []string{"a=f", "i=1"}, 2)
		wantChunks := n/k + 1
		if gotChunks := strings.Count(got, "\x1b_G"); gotChunks != wantChunks {
			t.Errorf("n=%d: want %d chunks, got %d", n, wantChunks, gotChunks)
		}
		if wantChunks > 1 && !strings.Contains(got, "m=0") {
			t.Errorf("n=%d: multi-chunk payload must carry an m=0 terminator, got %.60q", n, got[len(got)-60:])
		}
		// n a multiple of k (and > 0) is exactly the case a naive
		// `for len(payload) > k` loop gets wrong: the last full chunk still
		// needs a trailing *empty* m=0 chunk after it.
		if n > 0 && n%k == 0 && !strings.HasSuffix(got, "m=0\x1b\\") {
			t.Errorf("n=%d: exact multiple of the chunk size must end in an empty m=0 chunk, got %.60q", n, got[len(got)-60:])
		}
		if wantChunks == 1 && !strings.Contains(got, "a=f") {
			t.Errorf("n=%d: single-chunk payload must still carry the first-chunk options", n)
		}
	}
}

// decodeBase64Payload decodes a Kitty APC's semicolon-delimited payload —
// std-base64, no padding stripped, exactly as kittyChunk/kittyChunkRaw encode it.
func decodeBase64Payload(payload string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(payload)
}
