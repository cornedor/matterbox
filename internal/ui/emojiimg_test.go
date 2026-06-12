package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/mattermost/mattermost/server/public/model"
)

// Test 1: the placeholder is exactly the documented byte sequence and
// occupies two display columns (so layout maths sees it as ~one emoji wide).
func TestKittyPlaceholderGolden(t *testing.T) {
	got := kittyPlaceholder(0x123456, 1, 2)
	want := "\x1b[38;2;18;52;86m" +
		string(kitty.Placeholder) + "̅̅" +
		string(kitty.Placeholder) + "̅̍" +
		"\x1b[39m"
	if got != want {
		t.Fatalf("placeholder mismatch\n got %q\nwant %q", got, want)
	}
	if w := lipgloss.Width(got); w != 2 {
		t.Fatalf("placeholder width = %d, want 2", w)
	}
}

// Test 2: the transmit sequence is a chunked Kitty graphics APC carrying the
// virtual-placement + PNG options, ending with the final-chunk marker.
func TestKittyTransmitOptionsAndChunking(t *testing.T) {
	const id = 0x4D2 // 1234
	img, _, derr := image.Decode(bytes.NewReader(noisyPNG(t, 128, 128)))
	if derr != nil {
		t.Fatalf("decode noisy png: %v", derr)
	}
	seq, err := kittyTransmitImage(id, img, emojiPlaceholderRows, emojiPlaceholderCols)
	if err != nil {
		t.Fatalf("kittyTransmitImage: %v", err)
	}
	if !strings.HasPrefix(seq, "\x1b_G") {
		t.Fatalf("transmit does not start with APC G: %q", seq[:min(8, len(seq))])
	}
	// Round-trip the first chunk's options through the kitty parser.
	body := strings.TrimPrefix(seq, "\x1b_G")
	semi := strings.IndexByte(body, ';')
	if semi < 0 {
		t.Fatal("no payload separator in first chunk")
	}
	var opts kitty.Options
	if err := opts.UnmarshalText([]byte(body[:semi])); err != nil {
		t.Fatalf("parse options: %v", err)
	}
	if !opts.VirtualPlacement {
		t.Error("U=1 (virtual placement) missing")
	}
	if opts.Rows != 1 || opts.Columns != 2 {
		t.Errorf("rows/cols = %d/%d, want 1/2", opts.Rows, opts.Columns)
	}
	if opts.Format != kitty.PNG {
		t.Errorf("format = %d, want %d (PNG)", opts.Format, kitty.PNG)
	}
	if opts.ID != id {
		t.Errorf("id = %d, want %d", opts.ID, id)
	}
	if opts.Quite != 2 {
		t.Errorf("q = %d, want 2", opts.Quite)
	}
	// A >4KB PNG must be chunked: at least one m=1 and a closing m=0.
	if !strings.Contains(seq, "m=1") {
		t.Error("expected a continuation chunk (m=1)")
	}
	if !strings.Contains(seq, "m=0") {
		t.Error("expected a final chunk (m=0)")
	}
}

// Test 3: the per-name state machine — pending dedup, gated drain, ready
// placeholders, and failed-forever literals.
func TestEmojiImagesStateMachine(t *testing.T) {
	e := newEmojiImages("auto", true)

	// Before the probe resolves, sightings are recorded but not drained.
	if _, ok := e.inline("foo"); ok {
		t.Fatal("unknown emoji reported ready")
	}
	e.inline("foo") // dedup: a second sighting must not double-enqueue
	if got := e.takePending(); got != nil {
		t.Fatalf("drained %v before probe; want nil", got)
	}

	// Activate (probe OK + truecolor) and drain once.
	e.setColorProfile(true)
	e.setProbeResult(true)
	pend := e.takePending()
	if len(pend) != 1 || pend[0] != "foo" {
		t.Fatalf("takePending = %v, want [foo]", pend)
	}
	if got := e.takePending(); got != nil {
		t.Fatalf("second drain = %v, want nil (already fetching)", got)
	}

	// A ready emoji returns its placeholder.
	id := e.allocID()
	ph := kittyPlaceholder(id, 1, 2)
	e.markReady("foo", id, ph, []string{"TRANSMIT"}, nil)
	if got, ok := e.inline("foo"); !ok || got != ph {
		t.Fatalf("inline(foo) = %q,%v; want placeholder", got, ok)
	}

	// A failed name is literal forever and never re-enqueues.
	e.markFailed("bar")
	if _, ok := e.inline("bar"); ok {
		t.Fatal("failed emoji reported ready")
	}
	if got := e.takePending(); got != nil {
		t.Fatalf("failed emoji re-enqueued: %v", got)
	}
}

// Test 3b: once the probe fails (or times out), inline stops recording
// sightings — the feature is permanently off.
func TestEmojiImagesProbeFailedGating(t *testing.T) {
	e := newEmojiImages("auto", true)
	e.setColorProfile(true)
	e.setProbeResult(false) // timeout / unsupported
	if _, ok := e.inline("foo"); ok {
		t.Fatal("inline reported ready while unsupported")
	}
	if got := e.takePending(); got != nil {
		t.Fatalf("recorded pending while unsupported: %v", got)
	}
	// "off" mode behaves the same.
	if got := newEmojiImages("off", true); got.active() {
		t.Fatal(`"off" manager is active`)
	}
}

// Test 4: a still image decodes to a single frame; junk errors.
func TestDecodeEmojiFramesStill(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}

	var pngBuf, jpgBuf, gifBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpgBuf, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifBuf, img, nil); err != nil {
		t.Fatal(err)
	}

	for name, raw := range map[string][]byte{
		"png":  pngBuf.Bytes(),
		"jpeg": jpgBuf.Bytes(),
		"gif":  gifBuf.Bytes(), // single-frame GIF
	} {
		frames, delays, err := decodeImageFrames(raw, true)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(frames) != 1 {
			t.Errorf("%s: got %d frames, want 1 (still image)", name, len(frames))
		}
		if delays != nil {
			t.Errorf("%s: still image carries delays %v, want nil", name, delays)
		}
	}

	if _, _, err := decodeImageFrames([]byte("not an image"), true); err == nil {
		t.Error("expected error for non-image input")
	}
}

// Test 4b: a multi-frame GIF decodes to one composited frame per step with
// per-frame delays when animation is on, and collapses to the first frame when
// it's off.
func TestDecodeEmojiFramesAnimated(t *testing.T) {
	raw := animatedGIF(t)

	frames, delays, err := decodeImageFrames(raw, true)
	if err != nil {
		t.Fatalf("decode animated (on): %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	if len(delays) != 3 {
		t.Fatalf("got %d delays, want 3", len(delays))
	}
	// 0/tiny GIF delays settle to ~100ms (see clampGIFDelay).
	for i, d := range delays {
		if d <= 0 {
			t.Errorf("frame %d delay = %v, want > 0", i, d)
		}
	}
	// Compositing fills the full logical screen, not just a sub-rectangle.
	if b := frames[0].Bounds(); b.Dx() != 4 || b.Dy() != 4 {
		t.Errorf("frame bounds = %v, want 4×4", b)
	}

	off, offDelays, err := decodeImageFrames(raw, false)
	if err != nil {
		t.Fatalf("decode animated (off): %v", err)
	}
	if len(off) != 1 || offDelays != nil {
		t.Errorf("animations off: got %d frames / delays %v, want 1 frame / nil", len(off), offDelays)
	}
}

// animatedGIF builds a 3-frame 4×4 GIF (two palette colours per frame) so the
// frame decode + compositing path has real multi-frame input to chew on.
func animatedGIF(t *testing.T) []byte {
	t.Helper()
	pal := color.Palette{color.RGBA{A: 0xff}, color.RGBA{R: 0xff, A: 0xff}, color.RGBA{G: 0xff, A: 0xff}}
	g := &gif.GIF{}
	for f := 0; f < 3; f++ {
		img := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
		for i := range img.Pix {
			img.Pix[i] = uint8((i + f) % len(pal))
		}
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 0) // exercise the clamp
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encode animated gif: %v", err)
	}
	return buf.Bytes()
}

// Test 5: a post cached while its custom emoji was literal is invalidated
// when the image lands, and re-renders with the image placeholder.
func TestEmojiImageInvalidationAndRender(t *testing.T) {
	m := Model{
		emojiImg:  newEmojiImages("auto", true),
		posts:     []*model.Post{{Id: "p1", Message: "hi :party_parrot: there"}},
		userNames: map[string]string{},
	}
	m.emojiImg.setColorProfile(true)
	m.emojiImg.setProbeResult(true)
	m.msgsView = viewport.New()
	m.msgsView.SetWidth(40)
	m.msgsView.SetHeight(10)

	// First render: emoji not ready → literal text, and the post is cached.
	lines, _ := m.renderPostLines(m.posts[0], false)
	if strings.ContainsRune(strings.Join(lines, "\n"), kitty.Placeholder) {
		t.Fatal("placeholder shown before the image was ready")
	}
	if _, ok := m.postLineCache["p1"]; !ok {
		t.Fatal("post not cached after first render")
	}

	// The image lands: mark ready and invalidate the referencing post.
	id := m.emojiImg.allocID()
	m.emojiImg.markReady("party_parrot", id, kittyPlaceholder(id, 1, 2), []string{"TRANSMIT"}, nil)
	m.invalidatePostsForEmoji(map[string]struct{}{"party_parrot": {}})
	if _, ok := m.postLineCache["p1"]; ok {
		t.Fatal("stale cache entry not dropped on invalidation")
	}

	// Second render: now the placeholder appears.
	lines, _ = m.renderPostLines(m.posts[0], false)
	if !strings.ContainsRune(strings.Join(lines, "\n"), kitty.Placeholder) {
		t.Fatal("placeholder not shown after the image was ready")
	}
}

// Test 5b: handleEmojiImagesFetched installs a batch end-to-end — the emoji
// becomes ready and a raw transmit command is returned.
func TestHandleEmojiImagesFetched(t *testing.T) {
	m := Model{
		emojiImg:  newEmojiImages("auto", true),
		posts:     []*model.Post{{Id: "p1", Message: ":party_parrot:"}},
		userNames: map[string]string{},
	}
	m.emojiImg.setColorProfile(true)
	m.emojiImg.setProbeResult(true)
	m.msgsView = viewport.New()
	m.msgsView.SetWidth(40)
	m.msgsView.SetHeight(10)

	re, err := m.buildReadyEmoji(noisyPNG(t, 8, 8))
	if err != nil {
		t.Fatalf("buildReadyEmoji: %v", err)
	}
	updated, cmd := m.handleEmojiImagesFetched(emojiImagesFetchedMsg{
		ready:  map[string]readyEmoji{"party_parrot": re},
		failed: []string{"definitely_not_an_emoji"},
	})
	m = updated

	if _, ok := m.emojiImg.inline("party_parrot"); !ok {
		t.Error("party_parrot not ready after fetch")
	}
	if _, ok := m.emojiImg.inline("definitely_not_an_emoji"); ok {
		t.Error("failed emoji reported ready")
	}
	if cmd == nil {
		t.Fatal("expected a transmit command")
	}
	raw, ok := cmd().(tea.RawMsg)
	if !ok {
		t.Fatalf("expected tea.RawMsg, got %T", cmd())
	}
	if s, _ := raw.Msg.(string); !strings.HasPrefix(s, "\x1b_G") {
		t.Errorf("transmit command is not a Kitty graphics APC: %q", s)
	}
}

// noisyPNG builds a high-entropy PNG (via a small LCG) that resists
// compression, so its encoding exceeds the 4KB chunk size.
func noisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	state := uint32(0x12345678)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			state = state*1664525 + 1013904223
			img.Set(x, y, color.RGBA{
				R: byte(state >> 24),
				G: byte(state >> 16),
				B: byte(state >> 8),
				A: 0xff,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode noisy png: %v", err)
	}
	return buf.Bytes()
}

// Test 6: advanceFrame steps an animated emoji only once its frame delay has
// elapsed, emitting the next frame's transmit and wrapping at the end; a still
// emoji never moves.
func TestAdvanceFrame(t *testing.T) {
	e := newEmojiImages("auto", true)
	const d = 100 * time.Millisecond
	e.markReady("anim", 7, "PH", []string{"F0", "F1"}, []time.Duration{d, d})
	e.markReady("still", 8, "PH2", []string{"S0"}, nil)

	base := time.Unix(0, 0)

	// First tick anchors frameStart; nothing is due yet.
	seq, next, animating := e.advanceFrame(base)
	if !animating {
		t.Fatal("advanceFrame reported not animating with a ready animated emoji")
	}
	if seq != "" {
		t.Errorf("first tick emitted %q, want nothing (not yet due)", seq)
	}
	if next <= 0 || next > d {
		t.Errorf("next-due = %v, want (0, %v]", next, d)
	}

	// One delay later, frame 1 is transmitted (the still emoji contributes nothing).
	if seq, _, _ = e.advanceFrame(base.Add(d)); seq != "F1" {
		t.Errorf("after one delay seq = %q, want F1", seq)
	}
	// Another delay later it wraps back to frame 0.
	if seq, _, _ = e.advanceFrame(base.Add(2 * d)); seq != "F0" {
		t.Errorf("after two delays seq = %q, want F0", seq)
	}
}

// Test 6b: a late tick (a long sleep) catches up across multiple frames in one
// step rather than playing in slow motion.
func TestAdvanceFrameCatchUp(t *testing.T) {
	e := newEmojiImages("auto", true)
	const d = 50 * time.Millisecond
	e.markReady("anim", 1, "PH", []string{"F0", "F1", "F2", "F3"}, []time.Duration{d, d, d, d})

	base := time.Unix(0, 0)
	e.advanceFrame(base) // anchor

	// 175ms ≈ 3.5 frames elapsed → land on frame 3.
	if seq, _, _ := e.advanceFrame(base.Add(175 * time.Millisecond)); seq != "F3" {
		t.Errorf("catch-up seq = %q, want F3", seq)
	}
}

// Test 6c: an animated emoji arms the animation loop exactly once; a still
// emoji leaves it dormant.
func TestHandleEmojiImagesFetchedArmsAnimation(t *testing.T) {
	newModel := func() Model {
		m := Model{emojiImg: newEmojiImages("auto", true), userNames: map[string]string{}}
		m.emojiImg.setColorProfile(true)
		m.emojiImg.setProbeResult(true)
		m.msgsView = viewport.New()
		m.msgsView.SetWidth(40)
		m.msgsView.SetHeight(10)
		return m
	}

	// A still emoji must not arm the loop.
	still := newModel()
	stillRE, err := still.buildReadyEmoji(noisyPNG(t, 8, 8))
	if err != nil {
		t.Fatalf("buildReadyEmoji(still): %v", err)
	}
	still, _ = still.handleEmojiImagesFetched(emojiImagesFetchedMsg{ready: map[string]readyEmoji{"p": stillRE}})
	if still.emojiAnimating {
		t.Error("still emoji armed the animation loop")
	}

	// An animated GIF must arm it.
	anim := newModel()
	animRE, err := anim.buildReadyEmoji(animatedGIF(t))
	if err != nil {
		t.Fatalf("buildReadyEmoji(anim): %v", err)
	}
	if len(animRE.frameSeqs) <= 1 {
		t.Fatalf("animated GIF built %d frame(s), want > 1", len(animRE.frameSeqs))
	}
	anim, cmd := anim.handleEmojiImagesFetched(emojiImagesFetchedMsg{ready: map[string]readyEmoji{"a": animRE}})
	if !anim.emojiAnimating {
		t.Error("animated emoji did not arm the animation loop")
	}
	if cmd == nil {
		t.Fatal("expected a command (transmit + tick)")
	}
}
