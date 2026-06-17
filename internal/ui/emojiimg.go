package ui

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	_ "image/jpeg" // some servers store emoji as JPEG
	_ "image/png"  // PNG emoji (and the kitty transmit format)
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/mattermost/mattermost/server/public/model"
)

// Custom (server) emoji rendered as inline images via the Kitty graphics
// protocol's Unicode-placeholder variant — the only variant that survives a
// full-screen TUI's repaints and scrolling, because the image is anchored to
// ordinary text cells rather than an absolute screen position. Supported by
// Kitty and Ghostty. Unicode emoji are unaffected (kyokomi font glyphs).
// Animated GIF emoji cycle through their frames in place (see advanceFrame and
// the animation tick in update.go) unless the animations.custom_emoji config
// toggle is off, in which case they freeze on the first frame.
//
// Lifecycle: an emoji image is transmitted to the terminal once per session
// (out of band, via tea.Raw) and thereafter displayed by emitting placeholder
// cells carrying the image id in their truecolor foreground. See
// internal/ui/update.go for the probe/drain/transmit wiring and EMOJI_PLAN.md
// for the design.

// kittyProbeID is the image id used by the startup graphics-support probe — a
// distinctive constant so the terminal's reply is easy to recognise in Update.
const kittyProbeID = 0xB0C5

// emojiPlaceholderRows / emojiPlaceholderCols size every emoji placement: one
// row by two columns is ≈ square at a typical 1:2 cell aspect, and matches the
// width-2 most emoji font glyphs already occupy.
const (
	emojiPlaceholderRows = 1
	emojiPlaceholderCols = 2
)

// kittyProbe builds the graphics-support query: a 1×1 RGBA pixel sent with the
// query action (a=q). A Kitty/Ghostty-class terminal replies with an APC
// `_Gi=<kittyProbeID>;OK` — query replies are sent regardless of quiet mode —
// while terminals without support ignore the APC entirely, so the caller falls
// back to a timeout. No q key: we want the reply.
func kittyProbe() string {
	payload := []byte("AAAAAA==") // base64 of four zero bytes (one RGBA pixel)
	return ansi.KittyGraphics(payload,
		fmt.Sprintf("i=%d", kittyProbeID),
		"s=1", "v=1", "a=q", "t=d", "f=32")
}

// requestCellSize builds the XTWINOPS query (CSI 16 t) asking the terminal to
// report its character-cell size in pixels. A Kitty/Ghostty-class terminal
// replies `CSI 6 ; height ; width t`, which ultraviolet decodes into a
// uv.CellSizeEvent (handled in Update). The image-preview modal uses it to size
// a placement to the image's native pixels rather than upscaling a small image
// to fill the box. Terminals that don't support the query simply stay silent,
// and the modal keeps its box-filling fallback.
func requestCellSize() string {
	return ansi.WindowOp(16)
}

// kittyPlaceholder builds the Unicode-placeholder text that displays a
// previously-transmitted image (see kittyTransmit) anchored to text cells. The
// 24-bit image id rides in the truecolor foreground (\x1b[38;2;R;G;Bm); each
// cell is the placeholder rune U+10EEEE followed by its row and column
// diacritics. The SGR is hand-built rather than routed through lipgloss so the
// colour-profile machinery can never quantise the id away.
//
// The id foreground is (re)opened and closed (\x1b[39m) on *every* row rather
// than spanning the whole block, so a multi-row placement survives being routed
// through lipgloss layout (JoinVertical / a bordered box, as the image-preview
// modal does): lipgloss resets SGR at line boundaries, which would otherwise
// strip the id from every row after the first and collapse the image to a
// single rendered row. For the 1-row emoji case this is byte-identical to a
// single leading SGR.
func kittyPlaceholder(id uint32, rows, cols int) string {
	var sb strings.Builder
	fg := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", byte(id>>16), byte(id>>8), byte(id))
	for row := 0; row < rows; row++ {
		if row > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(fg)
		for col := 0; col < cols; col++ {
			sb.WriteRune(kitty.Placeholder)
			sb.WriteRune(kitty.Diacritic(row))
			sb.WriteRune(kitty.Diacritic(col))
		}
		sb.WriteString("\x1b[39m")
	}
	return sb.String()
}

// emojiIsPlaceholder reports whether a rendered glyph is a Kitty image
// placeholder (vs a font glyph or literal text), so callers that style a
// surrounding pill/row can avoid clobbering the id-bearing foreground.
func emojiIsPlaceholder(s string) bool {
	return strings.ContainsRune(s, kitty.Placeholder)
}

// kittyTransmitImage builds the out-of-band APC that uploads img to the terminal
// under id and registers a virtual placement sized to rows×cols text cells, so a
// matching kittyPlaceholder(id, rows, cols) displays it. Action TransmitAndPut
// with U=1 does transmit + virtual placement in one go; r/c size the placement;
// q=2 suppresses the OK/error replies the terminal would otherwise emit. Chunked
// at 4KB. Shared by the emoji path (1×2) and the image-preview modal (large
// boxes); see preview.go.
func kittyTransmitImage(id uint32, img image.Image, rows, cols int) (string, error) {
	var sb strings.Builder
	opts := &kitty.Options{
		Action:           kitty.TransmitAndPut,
		VirtualPlacement: true,
		ID:               int(id),
		Rows:             rows,
		Columns:          cols,
		Format:           kitty.PNG,
		Transmission:     kitty.Direct,
		Quite:            2,
		Chunk:            true,
	}
	if err := kitty.EncodeGraphics(&sb, img, opts); err != nil {
		return "", fmt.Errorf("encode kitty graphics: %w", err)
	}
	return sb.String(), nil
}

// kittyDelete builds the APC that frees image id from terminal memory — both the
// image data and any placements (d=I, the capital variant). Used when the
// preview modal closes or cycles so a session that previews many images doesn't
// accumulate them in the terminal. q=2 suppresses the reply.
func kittyDelete(id uint32) string {
	return ansi.KittyGraphics(nil, "a=d", "d=I", fmt.Sprintf("i=%d", id), "q=2")
}

func isGIF(b []byte) bool {
	return len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a")
}

// decodeImageFrames turns raw image bytes into the frames we transmit. With
// animation enabled it returns every composited frame of a multi-frame GIF plus
// per-frame delays; otherwise (a still image, a single-frame GIF, or animation
// disabled) it returns one frame and a nil delay slice. Anything stdlib can't
// decode is an error. Shared by the custom-emoji and image-preview paths.
func decodeImageFrames(raw []byte, animate bool) (frames []image.Image, delays []time.Duration, err error) {
	if animate && isGIF(raw) {
		if g, derr := gif.DecodeAll(bytes.NewReader(raw)); derr == nil && len(g.Image) > 1 {
			return compositeGIF(g)
		}
		// Single-frame GIF or a decode error: fall through to the still path,
		// which yields the first frame via the general image decoder.
	}
	img, _, derr := image.Decode(bytes.NewReader(raw))
	if derr != nil {
		return nil, nil, fmt.Errorf("decode emoji image: %w", derr)
	}
	return []image.Image{img}, nil, nil
}

// compositeGIF flattens an animated GIF into one fully-painted RGBA frame per
// step, honouring the disposal methods (gif.DecodeAll hands back only the
// changed sub-rectangle of each step, layered on whatever the disposal left
// behind). Delays come from gif.GIF.Delay (hundredths of a second), clamped so
// a 0/absurd delay can't busy-loop the render.
func compositeGIF(g *gif.GIF) (frames []image.Image, delays []time.Duration, err error) {
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	if bounds.Empty() {
		bounds = g.Image[0].Bounds()
	}
	canvas := image.NewRGBA(bounds)
	frames = make([]image.Image, 0, len(g.Image))
	delays = make([]time.Duration, 0, len(g.Image))
	var restore *image.RGBA // canvas snapshot for a DisposalPrevious rollback
	for i, src := range g.Image {
		disposal := byte(0)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		// DisposalPrevious means "after this frame, roll back to what was here
		// before it" — so snapshot the canvas before painting.
		if disposal == gif.DisposalPrevious {
			restore = cloneRGBA(canvas)
		}
		draw.Draw(canvas, src.Bounds(), src, src.Bounds().Min, draw.Over)
		frames = append(frames, cloneRGBA(canvas))

		d := 100 * time.Millisecond
		if i < len(g.Delay) {
			d = clampGIFDelay(g.Delay[i])
		}
		delays = append(delays, d)

		switch disposal {
		case gif.DisposalBackground:
			// Clear this frame's rectangle back to transparent.
			r := src.Bounds().Intersect(canvas.Rect)
			draw.Draw(canvas, r, image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			if restore != nil {
				copy(canvas.Pix, restore.Pix)
			}
		}
	}
	return frames, delays, nil
}

// cloneRGBA returns an independent copy of src, used to snapshot each
// composited GIF frame (the shared canvas keeps mutating).
func cloneRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Rect)
	copy(dst.Pix, src.Pix)
	return dst
}

// clampGIFDelay converts a GIF per-frame delay (hundredths of a second) to a
// duration, settling a 0 or absurdly small delay to ~100ms — what browsers do,
// and enough to keep a pathological GIF from hammering the terminal.
func clampGIFDelay(hundredths int) time.Duration {
	d := time.Duration(hundredths) * 10 * time.Millisecond
	if d < 20*time.Millisecond {
		d = 100 * time.Millisecond
	}
	return d
}

// cachedEmojiPath returns the on-disk cache path for a custom emoji, keyed by
// name (mirrors cachedFilePath). The file holds the *original* downloaded bytes
// (PNG/JPEG/GIF), so a warm restart can still decode every GIF frame and
// animate — the format is sniffed on read, the extension is omitted on purpose.
// Re-uploading an emoji under the same name shows the stale image until the file
// is removed — acceptable for emoji.
func cachedEmojiPath(name string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "matterbox", "emoji")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// filepath.Base guards against a stray separator in the name; the
	// shortcode regex already constrains body/picker names to a safe class.
	return filepath.Join(dir, filepath.Base(name)), nil
}

// emojiImgState is the per-name lifecycle in the custom-emoji image manager.
type emojiImgState int

const (
	emojiPending  emojiImgState = iota // sighted on screen, awaiting a fetch
	emojiFetching                      // fetch in flight
	emojiReady                         // transmitted; placeholder usable
	emojiFailed                        // not a custom emoji / failed — literal forever
)

type emojiImgEntry struct {
	state       emojiImgState
	id          uint32
	placeholder string // prebuilt placeholder run (ready only)

	// Animation (ready only). frameSeqs holds one prebuilt transmit APC per
	// frame, every one targeting id, so flipping frames is just re-emitting the
	// next APC — the placeholder cells already on screen keep pointing at the
	// same id and the terminal repaints them. A still emoji has exactly one
	// entry and a nil delays slice; an animated GIF has len(frameSeqs) > 1.
	frameSeqs  []string
	delays     []time.Duration // per-frame display time, parallel to frameSeqs
	frameIdx   int             // frame currently transmitted under id
	frameStart time.Time       // when frameIdx began showing (zero until first tick)

	// visible is true while this emoji appears in an on-screen post (main
	// window or open thread); only visible animated emoji drive the animation
	// loop. Without this, advanceFrame would keep ticking — and forcing a full
	// re-render per tick — for every animated emoji ever cached this session,
	// even after the user navigated away, pegging the CPU for the rest of the
	// session. Recomputed by recomputeVisibleAnimatedEmoji on content changes.
	visible bool
}

// emojiImages manages rendering custom (server) emoji as inline Kitty
// graphics. It is held as a *pointer* on Model (which is value-copied
// throughout this package) and its methods are called from both Update
// (body/pill renders during renderMessages) and View (popup/status renders),
// so every access takes mu.
type emojiImages struct {
	mu      sync.Mutex
	mode    string // "auto" | "off"
	animate bool   // animations.custom_emoji: cycle GIF emoji frames in place

	// Probe + colour-profile gating. The feature is active only once the
	// graphics probe came back OK *and* the terminal reports a truecolor
	// profile — the id-in-foreground encoding needs 24-bit colour, since the
	// cell renderer would otherwise quantise the id away.
	probeDone    bool
	probeOK      bool
	probeReplied bool   // a KittyGraphicsEvent reply actually arrived (vs timeout)
	probePayload string // raw probe reply payload, for statusReason diagnostics
	profileKnown bool
	truecolor    bool

	entries map[string]*emojiImgEntry
	pending map[string]struct{}
	nextID  uint32
}

func newEmojiImages(mode string, animate bool) *emojiImages {
	if mode != "off" {
		mode = "auto"
	}
	return &emojiImages{
		mode:    mode,
		animate: animate,
		entries: map[string]*emojiImgEntry{},
		pending: map[string]struct{}{},
		nextID:  randomEmojiIDSeed(),
	}
}

// animationsEnabled reports whether GIF custom emoji should be decoded to all
// frames and animated. Read off the main goroutine (the background fetch), so
// it takes the lock.
func (e *emojiImages) animationsEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.animate
}

// randomEmojiIDSeed picks a non-zero 24-bit starting id. A random per-session
// seed makes a fresh transmit unlikely to alias an image the terminal still
// holds from a previous run.
func randomEmojiIDSeed() uint32 {
	var b [3]byte
	_, _ = rand.Read(b[:])
	id := uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
	if id == 0 {
		id = 1
	}
	return id
}

// permanentlyOff reports whether the feature can never activate this session —
// so inline() should stop recording sightings. Distinct from "not yet active"
// (probe still pending), where sightings keep being recorded.
func (e *emojiImages) permanentlyOff() bool {
	switch {
	case e.mode == "off":
		return true
	case e.probeDone && !e.probeOK:
		return true
	case e.profileKnown && !e.truecolor:
		return true
	}
	return false
}

// active reports whether images can be fetched and transmitted: probe OK and a
// known truecolor profile.
func (e *emojiImages) active() bool {
	return e.mode != "off" && e.probeDone && e.probeOK && e.profileKnown && e.truecolor
}

// inline returns the prebuilt placeholder for a ready custom emoji and true;
// for anything else it returns ("", false). An as-yet-unseen name is recorded
// as pending (unless the feature is permanently off) so the next Update tail
// fetches it. Called from Update and View — hence the lock.
func (e *emojiImages) inline(name string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ent := e.entries[name]; ent != nil {
		if ent.state == emojiReady {
			return ent.placeholder, true
		}
		return "", false // pending / fetching / failed
	}
	if e.permanentlyOff() {
		return "", false
	}
	e.entries[name] = &emojiImgEntry{state: emojiPending}
	e.pending[name] = struct{}{}
	return "", false
}

// setProbeResult records the graphics-probe outcome (OK reply, or timeout).
// Gating is recomputed implicitly by active()/permanentlyOff().
func (e *emojiImages) setProbeResult(ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.probeDone {
		return
	}
	e.probeDone = true
	e.probeOK = ok
}

// setColorProfile records whether the terminal is truecolor. May arrive before
// or after the graphics probe; active() reads both.
func (e *emojiImages) setColorProfile(truecolor bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.profileKnown = true
	e.truecolor = truecolor
}

// markUnsupported permanently disables the feature without sending a probe —
// used when mode is non-auto or under tmux, where the probe reply is unreliable.
func (e *emojiImages) markUnsupported() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.probeDone = true
	e.probeOK = false
}

// setProbeReply records that the terminal answered the graphics probe and with
// what payload, so statusReason can tell a silent terminal (timeout) apart from
// one that replied something other than OK. Call before setProbeResult.
func (e *emojiImages) setProbeReply(payload string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.probeReplied = true
	e.probePayload = payload
}

// statusReason names the specific gate keeping image features inactive, for a
// human-facing status line — so a user whose terminal "should" work can see
// which check failed (config / probe / tmux / truecolor) rather than a generic
// "needs a Kitty-capable terminal". Returns "" when active. nil-safe.
func (e *emojiImages) statusReason() string {
	if e == nil {
		return "disabled in this build"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case e.mode == "off":
		return "turned off in config (emoji_images: off)"
	case !e.probeDone:
		return "still probing the terminal — try again in a moment"
	case !e.probeOK:
		if e.probeReplied {
			return fmt.Sprintf("terminal answered the graphics probe but not with OK (%q)", e.probePayload)
		}
		return "terminal didn't answer the Kitty graphics probe (tmux, or no Kitty graphics support)"
	case !e.profileKnown:
		return "terminal color profile not reported — can't confirm truecolor"
	case !e.truecolor:
		return "terminal not detected as truecolor (set COLORTERM=truecolor)"
	}
	return ""
}

// takePending drains the names sighted since the last call and marks them
// fetching, returning them for a background fetch. Returns nil until the
// feature is active, so sightings recorded before the probe resolved are held
// (not dropped) and drained on the first Update after activation.
func (e *emojiImages) takePending() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.active() || len(e.pending) == 0 {
		return nil
	}
	names := make([]string, 0, len(e.pending))
	for n := range e.pending {
		names = append(names, n)
		delete(e.pending, n)
		if ent := e.entries[n]; ent != nil {
			ent.state = emojiFetching
		}
	}
	return names
}

// allocID returns the next non-zero 24-bit image id. Sequential from the
// random seed; wraps within 24 bits so the id always fits the truecolor
// foreground encoding.
func (e *emojiImages) allocID() uint32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := e.nextID
	e.nextID++
	if e.nextID >= 1<<24 {
		e.nextID = 1
	}
	return id
}

// markReady installs a fetched emoji: subsequent inline() calls return its
// placeholder. The caller has already allocated the id and prebuilt the
// placeholder + per-frame transmit sequences (frameSeqs[0] is the still/first
// frame, already transmitted out of band; delays is nil for a still emoji).
func (e *emojiImages) markReady(name string, id uint32, placeholder string, frameSeqs []string, delays []time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries[name] = &emojiImgEntry{
		state:       emojiReady,
		id:          id,
		placeholder: placeholder,
		frameSeqs:   frameSeqs,
		delays:      delays,
	}
}

// advanceFrame steps every animated emoji whose current frame's delay has
// elapsed at time now, and returns the concatenated transmit APCs for those
// that moved (each re-transmits to the emoji's fixed id, so the placeholder
// cells already on screen repaint without any re-render). next is the soonest
// any animated emoji is next due — the caller schedules the following tick from
// it. animating is false when nothing is left to animate, so the loop can stop.
func (e *emojiImages) advanceFrame(now time.Time) (seq string, next time.Duration, animating bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var sb strings.Builder
	next = -1
	for _, ent := range e.entries {
		if ent.state != emojiReady || len(ent.frameSeqs) <= 1 {
			continue
		}
		// Only animate emoji that are actually on screen. An off-screen
		// animated emoji keeps its cached frame; re-transmitting it would be
		// invisible work and, worse, each tick forces a full re-render.
		if !ent.visible {
			continue
		}
		animating = true
		if ent.frameStart.IsZero() {
			ent.frameStart = now
		}
		advanced := false
		// Catch up across as many frames as the elapsed time covers, so a late
		// tick (a long sleep, a busy main loop) doesn't play in slow motion.
		for now.Sub(ent.frameStart) >= ent.delays[ent.frameIdx] {
			ent.frameStart = ent.frameStart.Add(ent.delays[ent.frameIdx])
			ent.frameIdx = (ent.frameIdx + 1) % len(ent.frameSeqs)
			advanced = true
		}
		if advanced {
			sb.WriteString(ent.frameSeqs[ent.frameIdx])
		}
		rem := ent.delays[ent.frameIdx] - now.Sub(ent.frameStart)
		if rem < 0 {
			rem = 0
		}
		if next < 0 || rem < next {
			next = rem
		}
	}
	return sb.String(), next, animating
}

// readyAnimatedNames returns the names of every ready emoji with more than one
// frame (i.e. the candidates the animation loop would drive). Used to scope the
// on-screen visibility scan to just the animated emoji.
func (e *emojiImages) readyAnimatedNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for n, ent := range e.entries {
		if ent.state == emojiReady && len(ent.frameSeqs) > 1 {
			out = append(out, n)
		}
	}
	return out
}

// setVisibleAnimated marks exactly the named entries visible and clears the
// flag on all others, so advanceFrame animates only on-screen emoji.
func (e *emojiImages) setVisibleAnimated(names map[string]struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for n, ent := range e.entries {
		_, ok := names[n]
		ent.visible = ok
	}
}

// hasVisibleAnimated reports whether any ready animated emoji is currently on
// screen — the cheap check used to (re-)arm the animation loop.
func (e *emojiImages) hasVisibleAnimated() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ent := range e.entries {
		if ent.visible && ent.state == emojiReady && len(ent.frameSeqs) > 1 {
			return true
		}
	}
	return false
}

// markFailed records names that aren't custom emoji (or whose fetch failed):
// they render as literal :name: text for the rest of the session.
func (e *emojiImages) markFailed(names ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, n := range names {
		e.entries[n] = &emojiImgEntry{state: emojiFailed}
	}
}

// renderEmojiGlyph resolves a single emoji shortcode (no colons) for display:
// a kyokomi font glyph for a unicode emoji, the inline-image placeholder for a
// ready custom emoji (recording a sighting when not yet ready), or the literal
// :name: as a last resort. Used by the reaction pills/picker, the emoji popup,
// and the custom-status surfaces; the message body resolves via renderInline.
func (m Model) renderEmojiGlyph(name string) string {
	if g := unicodeEmojiGlyph(name); g != "" {
		return g
	}
	if m.emojiImg != nil {
		if ph, ok := m.emojiImg.inline(name); ok {
			return ph
		}
	}
	return ":" + name + ":"
}

// --- background fetch + Update wiring -------------------------------------

// emojiProbeTimeoutMsg fires if the terminal hasn't answered the graphics
// probe within emojiProbeTimeout: the feature settles to "unsupported".
type emojiProbeTimeoutMsg struct{}

// customEmojiListMsg carries the server's full custom-emoji shortcode list
// (sorted) used to seed the :-picker, or the error that fetching it hit.
type customEmojiListMsg struct {
	names []string
	err   error
}

// readyEmoji is a fetched, decoded, and fully-built emoji image: an allocated
// id, its placeholder run, and one transmit APC per frame (still emoji have
// exactly one and a nil delays slice). Built off the main goroutine in
// loadEmojiImages so no decode/encode work lands on the render loop.
type readyEmoji struct {
	id          uint32
	placeholder string
	frameSeqs   []string
	delays      []time.Duration
}

// emojiImagesFetchedMsg is the result of a background image batch. ready maps
// shortcode → its built image; failed names are not custom emoji (or
// unrecoverable) and settle to literal text; retry names hit a transient error
// and are forgotten so a later sighting tries again.
type emojiImagesFetchedMsg struct {
	ready  map[string]readyEmoji
	failed []string
	retry  []string
}

// fetchCustomEmojiList loads every custom-emoji shortcode once (after channels
// load) to populate the :-picker. Skipped when images are configured off.
func (m Model) fetchCustomEmojiList() tea.Cmd {
	if m.emojiImg == nil || m.emojiImg.mode != "auto" {
		return nil
	}
	return func() tea.Msg {
		names, err := m.client.AllCustomEmoji(m.ctx)
		if err != nil {
			return customEmojiListMsg{err: err}
		}
		sort.Strings(names)
		return customEmojiListMsg{names: names}
	}
}

// fetchPendingEmoji drains custom-emoji names sighted during the last render
// (body, pill, popup, or status) and, if any, returns a Cmd that resolves and
// downloads their images in the background. Mirrors resolveUnknownSenders: run
// from Update after each event, returns nil cheaply once nothing is pending or
// the feature is inactive. takePending gates on probe+profile, so View-time
// sightings recorded before activation drain on the first Update afterwards.
func (m Model) fetchPendingEmoji() tea.Cmd {
	if m.emojiImg == nil {
		return nil
	}
	names := m.emojiImg.takePending()
	if len(names) == 0 {
		return nil
	}
	return func() tea.Msg {
		return m.loadEmojiImages(names)
	}
}

// loadEmojiImages resolves a batch of sighted shortcodes to built images. Disk
// cache first (a warm restart costs no HTTP); the rest are bulk-resolved to
// server emoji records and downloaded. Raw bytes are decoded into frames and
// turned into transmit sequences here, off the render loop. Names the server
// doesn't return are failed (not custom emoji); a transport error on the bulk
// resolve marks the whole miss-set for retry rather than burning them.
func (m Model) loadEmojiImages(names []string) tea.Msg {
	raws := map[string][]byte{}
	var failed, retry, misses []string
	for _, name := range names {
		if p, err := cachedEmojiPath(name); err == nil {
			if data, rerr := os.ReadFile(p); rerr == nil && len(data) > 0 {
				raws[name] = data
				continue
			}
		}
		misses = append(misses, name)
	}
	if len(misses) > 0 {
		emojis, err := m.client.CustomEmojisByNames(m.ctx, misses)
		if err != nil {
			// Transient (or old server without the bulk endpoint): forget the
			// misses so a later sighting retries. Cache hits still build below.
			retry = misses
		} else {
			byName := make(map[string]*model.Emoji, len(emojis))
			for _, e := range emojis {
				if e != nil {
					byName[e.Name] = e
				}
			}
			for _, name := range misses {
				e := byName[name]
				if e == nil {
					failed = append(failed, name) // server doesn't know it → literal
					continue
				}
				raw, err := m.client.CustomEmojiImage(m.ctx, e.Id)
				if err != nil {
					failed = append(failed, name)
					continue
				}
				raws[name] = raw
				if p, perr := cachedEmojiPath(name); perr == nil {
					_ = os.WriteFile(p, raw, 0o644) // best effort; original bytes
				}
			}
		}
	}
	ready := make(map[string]readyEmoji, len(raws))
	for name, raw := range raws {
		re, err := m.buildReadyEmoji(raw)
		if err != nil {
			failed = append(failed, name)
			continue
		}
		ready[name] = re
	}
	return emojiImagesFetchedMsg{ready: ready, failed: failed, retry: retry}
}

// buildReadyEmoji decodes raw emoji bytes into frames (all of them for an
// animated GIF when animations are on, else just the first) and prebuilds the
// id, placeholder, and one transmit APC per frame. Runs on the fetch goroutine,
// so every PNG encode stays off the render loop.
func (m Model) buildReadyEmoji(raw []byte) (readyEmoji, error) {
	frames, delays, err := decodeImageFrames(raw, m.emojiImg.animationsEnabled())
	if err != nil {
		return readyEmoji{}, err
	}
	id := m.emojiImg.allocID()
	seqs := make([]string, len(frames))
	for i, f := range frames {
		seq, err := kittyTransmitImage(id, f, emojiPlaceholderRows, emojiPlaceholderCols)
		if err != nil {
			return readyEmoji{}, err
		}
		seqs[i] = seq
	}
	return readyEmoji{
		id:          id,
		placeholder: kittyPlaceholder(id, emojiPlaceholderRows, emojiPlaceholderCols),
		frameSeqs:   seqs,
		delays:      delays,
	}, nil
}

// handleEmojiImagesFetched installs a finished image batch: each ready emoji is
// recorded with its placeholder + frames; failed ones settle to literal;
// retried ones are forgotten. Cached post lines that reference a newly-ready
// emoji are invalidated and re-rendered, and each emoji's first frame is sent
// raw (out of band) so its placeholders resolve. If any installed emoji is
// animated, the animation tick is armed (once — emojiAnimating guards it).
func (m Model) handleEmojiImagesFetched(msg emojiImagesFetchedMsg) (Model, tea.Cmd) {
	if m.emojiImg == nil {
		return m, nil
	}
	var transmit strings.Builder
	readyNames := make(map[string]struct{}, len(msg.ready))
	animated := false
	for name, re := range msg.ready {
		if len(re.frameSeqs) == 0 {
			m.emojiImg.markFailed(name)
			continue
		}
		m.emojiImg.markReady(name, re.id, re.placeholder, re.frameSeqs, re.delays)
		transmit.WriteString(re.frameSeqs[0])
		readyNames[name] = struct{}{}
		if len(re.frameSeqs) > 1 {
			animated = true
		}
	}
	m.emojiImg.markFailed(msg.failed...)
	m.emojiImg.markUnresolved(msg.retry...)
	if len(readyNames) > 0 {
		m.invalidatePostsForEmoji(readyNames)
		m.renderMessages()
		m.renderThread()
	}
	var cmds []tea.Cmd
	if transmit.Len() > 0 {
		cmds = append(cmds, tea.Raw(transmit.String()))
	}
	// renderMessages above refreshed the visibility set; only arm the loop when
	// a newly-ready animated emoji is actually on screen. The Update-level
	// kicker (maybeStartEmojiAnim) handles the scroll/switch-in case.
	if animated && !m.emojiAnimating && m.emojiImg.hasVisibleAnimated() {
		m.emojiAnimating = true
		cmds = append(cmds, emojiAnimTickCmd(0))
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// invalidatePostsForEmoji drops the cached rendered lines of every on-screen
// post (main feed + open thread, already bounded by the render window) whose
// body or reactions mention one of the named emoji, so the next render picks
// up the now-ready placeholder. The fingerprint doesn't track emoji readiness,
// so this explicit invalidation is what makes a just-readied image appear.
func (m *Model) invalidatePostsForEmoji(names map[string]struct{}) {
	check := func(p *model.Post) {
		if p == nil || p.Id == "" {
			return
		}
		hit := false
		for name := range names {
			if strings.Contains(p.Message, ":"+name+":") {
				hit = true
				break
			}
		}
		if !hit && p.Metadata != nil {
			for _, r := range p.Metadata.Reactions {
				if r == nil {
					continue
				}
				if _, ok := names[r.EmojiName]; ok {
					hit = true
					break
				}
			}
		}
		if hit {
			m.invalidatePostLines(p.Id)
		}
	}
	for _, p := range m.posts {
		check(p)
	}
	for _, p := range m.threadPosts {
		check(p)
	}
}

// markUnresolved forgets the given names (deletes their entries) so a later
// on-screen sighting re-enqueues them — used after a transient fetch error.
func (e *emojiImages) markUnresolved(names ...string) {
	if len(names) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, n := range names {
		delete(e.entries, n)
	}
}

// --- GIF emoji animation tick ---------------------------------------------

// emojiAnimMinInterval floors the tick cadence: it caps the wakeup rate (so a
// fast GIF can't spin the loop at hundreds of Hz) and stands in for a 0 initial
// delay. Per-frame GIF delays below it are honoured loosely — the catch-up loop
// in advanceFrame skips frames rather than playing in slow motion.
const emojiAnimMinInterval = 50 * time.Millisecond

// emojiAnimTickMsg drives the single GIF-emoji animation loop. There is at most
// one in flight (guarded by Model.emojiAnimating); it reschedules itself from
// the soonest next-due frame until nothing is left to animate.
type emojiAnimTickMsg struct{}

// emojiAnimTickCmd schedules the next animation tick after d, floored to
// emojiAnimMinInterval.
func emojiAnimTickCmd(d time.Duration) tea.Cmd {
	if d < emojiAnimMinInterval {
		d = emojiAnimMinInterval
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return emojiAnimTickMsg{} })
}

// advanceEmojiAnim steps every animated emoji whose frame is due, emits the
// resulting re-transmits out of band (no re-render — the on-screen placeholders
// keep their id and the terminal repaints them), and reschedules itself. It
// clears emojiAnimating and stops once nothing animates.
func (m *Model) advanceEmojiAnim() tea.Cmd {
	if m.emojiImg == nil || !m.emojiAnimating {
		return nil
	}
	// Re-check visibility each tick so an emoji scrolled out of view stops the
	// loop (the YOffset can move without a content re-render).
	m.refreshAnimVisibility()
	seq, next, animating := m.emojiImg.advanceFrame(time.Now())
	if !animating {
		m.emojiAnimating = false
		return nil
	}
	if seq == "" {
		return emojiAnimTickCmd(next)
	}
	return tea.Batch(tea.Raw(seq), emojiAnimTickCmd(next))
}

// viewportVisibleAnimatedEmoji returns the ready animated emoji whose posts are
// currently within a viewport's visible rows — the main message pane plus the
// open thread. It maps the live YOffset back to on-screen posts through the row
// spans captured by renderMessages/renderThread, so it tracks scrolling without
// a re-render. Short-circuits to nil (the overwhelmingly common case) when no
// animated emoji are cached, and only string-scans posts actually in view.
func (m *Model) viewportVisibleAnimatedEmoji() map[string]struct{} {
	if m.emojiImg == nil {
		return nil
	}
	names := m.emojiImg.readyAnimatedNames()
	if len(names) == 0 {
		return nil
	}
	visible := make(map[string]struct{}, len(names))
	scan := func(posts []*model.Post, starts []int, top, height int) {
		if height <= 0 || len(starts) != len(posts)+1 {
			return
		}
		bot := top + height
		for i, p := range posts {
			if starts[i] >= bot {
				break // this post and all later ones start below the viewport
			}
			if starts[i+1] <= top {
				continue // entirely scrolled above the viewport
			}
			if p == nil {
				continue
			}
			for _, name := range names {
				if _, done := visible[name]; done {
					continue
				}
				if strings.Contains(p.Message, ":"+name+":") {
					visible[name] = struct{}{}
					continue
				}
				if p.Metadata != nil {
					for _, r := range p.Metadata.Reactions {
						if r != nil && r.EmojiName == name {
							visible[name] = struct{}{}
							break
						}
					}
				}
			}
			if len(visible) == len(names) {
				return
			}
		}
	}
	scan(m.posts, m.msgRowStarts, m.msgsView.YOffset(), m.msgsView.Height())
	if m.threadOpen && len(visible) < len(names) {
		scan(m.threadPosts, m.threadRowStarts, m.threadView.YOffset(), m.threadView.Height())
	}
	return visible
}

// refreshAnimVisibility recomputes the on-screen animated-emoji set and applies
// it, returning whether any animated emoji is visible. Cheap unless animated
// emoji are cached. Called wherever scrolling or content may have changed what's
// on screen: renderMessages/renderThread, the animation tick, and the per-event
// kicker.
func (m *Model) refreshAnimVisibility() bool {
	if m.emojiImg == nil {
		return false
	}
	visible := m.viewportVisibleAnimatedEmoji()
	m.emojiImg.setVisibleAnimated(visible)
	return len(visible) > 0
}

// maybeStartEmojiAnim (re-)arms the GIF-emoji animation loop when an animated
// emoji is on screen but the loop is stopped. The loop self-stops via
// advanceFrame whenever nothing animated is visible (switching channels,
// scrolling the emoji out of view), so this is what restarts it on the way back.
// Batched from Update after every event, mirroring the other pending-work
// kickers; refreshAnimVisibility short-circuits when no animated emoji exist, so
// the common typing path stays cheap.
func (m *Model) maybeStartEmojiAnim() tea.Cmd {
	if m.emojiImg == nil || m.emojiAnimating {
		return nil
	}
	if !m.refreshAnimVisibility() {
		return nil
	}
	m.emojiAnimating = true
	return emojiAnimTickCmd(0)
}
