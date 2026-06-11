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
	seq, err := kittyTransmit(id, noisyPNG(t, 128, 128))
	if err != nil {
		t.Fatalf("kittyTransmit: %v", err)
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
	e := newEmojiImages("auto")

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
	e.markReady("foo", id, ph, "TRANSMIT")
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
	e := newEmojiImages("auto")
	e.setColorProfile(true)
	e.setProbeResult(false) // timeout / unsupported
	if _, ok := e.inline("foo"); ok {
		t.Fatal("inline reported ready while unsupported")
	}
	if got := e.takePending(); got != nil {
		t.Fatalf("recorded pending while unsupported: %v", got)
	}
	// "off" mode behaves the same.
	if got := newEmojiImages("off"); got.active() {
		t.Fatal(`"off" manager is active`)
	}
}

// Test 4: every supported input is normalised to a PNG; junk errors.
func TestNormalizeEmojiPNG(t *testing.T) {
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
		"gif":  gifBuf.Bytes(),
	} {
		out, err := normalizeEmojiPNG(raw)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !isPNG(out) {
			t.Errorf("%s: output is not PNG", name)
		}
	}

	if _, err := normalizeEmojiPNG([]byte("not an image")); err == nil {
		t.Error("expected error for non-image input")
	}
}

// Test 5: a post cached while its custom emoji was literal is invalidated
// when the image lands, and re-renders with the image placeholder.
func TestEmojiImageInvalidationAndRender(t *testing.T) {
	m := Model{
		emojiImg:  newEmojiImages("auto"),
		posts:     []*model.Post{{Id: "p1", Message: "hi :party_parrot: there"}},
		userNames: map[string]string{},
	}
	m.emojiImg.setColorProfile(true)
	m.emojiImg.setProbeResult(true)
	m.msgsView = viewport.New()
	m.msgsView.SetWidth(40)
	m.msgsView.SetHeight(10)

	// First render: emoji not ready → literal text, and the post is cached.
	lines, _ := m.renderPostLines(m.posts[0])
	if strings.ContainsRune(strings.Join(lines, "\n"), kitty.Placeholder) {
		t.Fatal("placeholder shown before the image was ready")
	}
	if _, ok := m.postLineCache["p1"]; !ok {
		t.Fatal("post not cached after first render")
	}

	// The image lands: mark ready and invalidate the referencing post.
	id := m.emojiImg.allocID()
	m.emojiImg.markReady("party_parrot", id, kittyPlaceholder(id, 1, 2), "TRANSMIT")
	m.invalidatePostsForEmoji(map[string]struct{}{"party_parrot": {}})
	if _, ok := m.postLineCache["p1"]; ok {
		t.Fatal("stale cache entry not dropped on invalidation")
	}

	// Second render: now the placeholder appears.
	lines, _ = m.renderPostLines(m.posts[0])
	if !strings.ContainsRune(strings.Join(lines, "\n"), kitty.Placeholder) {
		t.Fatal("placeholder not shown after the image was ready")
	}
}

// Test 5b: handleEmojiImagesFetched installs a batch end-to-end — the emoji
// becomes ready and a raw transmit command is returned.
func TestHandleEmojiImagesFetched(t *testing.T) {
	m := Model{
		emojiImg:  newEmojiImages("auto"),
		posts:     []*model.Post{{Id: "p1", Message: ":party_parrot:"}},
		userNames: map[string]string{},
	}
	m.emojiImg.setColorProfile(true)
	m.emojiImg.setProbeResult(true)
	m.msgsView = viewport.New()
	m.msgsView.SetWidth(40)
	m.msgsView.SetHeight(10)

	updated, cmd := m.handleEmojiImagesFetched(emojiImagesFetchedMsg{
		ready:  map[string][]byte{"party_parrot": noisyPNG(t, 8, 8)},
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
