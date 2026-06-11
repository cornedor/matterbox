package ui

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"image"
	_ "image/gif"  // first-frame decode for animated emoji
	_ "image/jpeg" // some servers store emoji as JPEG
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	emoji "github.com/kyokomi/emoji/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Custom (server) emoji rendered as inline images via the Kitty graphics
// protocol's Unicode-placeholder variant — the only variant that survives a
// full-screen TUI's repaints and scrolling, because the image is anchored to
// ordinary text cells rather than an absolute screen position. Supported by
// Kitty and Ghostty. Unicode emoji are unaffected (kyokomi font glyphs);
// animated GIF emoji render as a static first frame.
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

// kittyPlaceholder builds the Unicode-placeholder text that displays a
// previously-transmitted image (see kittyTransmit) anchored to text cells. The
// 24-bit image id rides in the truecolor foreground (\x1b[38;2;R;G;Bm); each
// cell is the placeholder rune U+10EEEE followed by its row and column
// diacritics. The SGR is hand-built rather than routed through lipgloss so the
// colour-profile machinery can never quantise the id away. A reset (\x1b[39m)
// closes the run so following text keeps its own colour.
func kittyPlaceholder(id uint32, rows, cols int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%dm", byte(id>>16), byte(id>>8), byte(id))
	for row := 0; row < rows; row++ {
		if row > 0 {
			sb.WriteByte('\n')
		}
		for col := 0; col < cols; col++ {
			sb.WriteRune(kitty.Placeholder)
			sb.WriteRune(kitty.Diacritic(row))
			sb.WriteRune(kitty.Diacritic(col))
		}
	}
	sb.WriteString("\x1b[39m")
	return sb.String()
}

// emojiIsPlaceholder reports whether a rendered glyph is a Kitty image
// placeholder (vs a font glyph or literal text), so callers that style a
// surrounding pill/row can avoid clobbering the id-bearing foreground.
func emojiIsPlaceholder(s string) bool {
	return strings.ContainsRune(s, kitty.Placeholder)
}

// kittyTransmit builds the out-of-band APC sequence that uploads a PNG to the
// terminal and registers a virtual placement under id, so the matching
// kittyPlaceholder cells display it. Action TransmitAndPut with U=1 does
// transmit + virtual placement in one go; r/c size the placement to the
// placeholder cells; q=2 suppresses the OK/error replies the terminal would
// otherwise emit for every emoji. Chunked at 4KB.
func kittyTransmit(id uint32, png []byte) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		return "", fmt.Errorf("decode emoji png: %w", err)
	}
	var sb strings.Builder
	opts := &kitty.Options{
		Action:           kitty.TransmitAndPut,
		VirtualPlacement: true,
		ID:               int(id),
		Rows:             emojiPlaceholderRows,
		Columns:          emojiPlaceholderCols,
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

// normalizeEmojiPNG converts raw custom-emoji image bytes to PNG, the form we
// cache on disk and hand to kittyTransmit. PNG passes through untouched; GIF
// (first frame only — we don't animate) and JPEG are decoded and re-encoded.
// Anything stdlib can't decode is an error.
func normalizeEmojiPNG(raw []byte) ([]byte, error) {
	if isPNG(raw) {
		return raw, nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode emoji image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode emoji png: %w", err)
	}
	return buf.Bytes(), nil
}

func isPNG(b []byte) bool {
	const sig = "\x89PNG\r\n\x1a\n"
	return len(b) >= len(sig) && string(b[:len(sig)]) == sig
}

// cachedEmojiPath returns the on-disk PNG path for a custom emoji, keyed by
// name (mirrors cachedFilePath). Re-uploading an emoji under the same name
// shows the stale image until the file is removed — acceptable for emoji.
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
	return filepath.Join(dir, filepath.Base(name)+".png"), nil
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
	transmit    string // prebuilt transmit APC (ready only)
}

// emojiImages manages rendering custom (server) emoji as inline Kitty
// graphics. It is held as a *pointer* on Model (which is value-copied
// throughout this package) and its methods are called from both Update
// (body/pill renders during renderMessages) and View (popup/status renders),
// so every access takes mu.
type emojiImages struct {
	mu   sync.Mutex
	mode string // "auto" | "off"

	// Probe + colour-profile gating. The feature is active only once the
	// graphics probe came back OK *and* the terminal reports a truecolor
	// profile — the id-in-foreground encoding needs 24-bit colour, since the
	// cell renderer would otherwise quantise the id away.
	probeDone    bool
	probeOK      bool
	profileKnown bool
	truecolor    bool

	entries map[string]*emojiImgEntry
	pending map[string]struct{}
	nextID  uint32
}

func newEmojiImages(mode string) *emojiImages {
	if mode != "off" {
		mode = "auto"
	}
	return &emojiImages{
		mode:    mode,
		entries: map[string]*emojiImgEntry{},
		pending: map[string]struct{}{},
		nextID:  randomEmojiIDSeed(),
	}
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

// markReady installs a fetched-and-transmitted emoji: subsequent inline()
// calls return its placeholder. The caller has already allocated the id and
// built the placeholder + transmit sequences.
func (e *emojiImages) markReady(name string, id uint32, placeholder, transmit string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries[name] = &emojiImgEntry{
		state:       emojiReady,
		id:          id,
		placeholder: placeholder,
		transmit:    transmit,
	}
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
	code := ":" + name + ":"
	if g := emoji.CodeMap()[code]; g != "" {
		return g
	}
	if m.emojiImg != nil {
		if ph, ok := m.emojiImg.inline(name); ok {
			return ph
		}
	}
	return code
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

// emojiImagesFetchedMsg is the result of a background image batch. ready maps
// shortcode → normalised PNG bytes (to transmit); failed names are not custom
// emoji (or unrecoverable) and settle to literal text; retry names hit a
// transient error and are forgotten so a later sighting tries again.
type emojiImagesFetchedMsg struct {
	ready  map[string][]byte
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

// loadEmojiImages resolves a batch of sighted shortcodes to PNG bytes. Disk
// cache first (a warm restart costs no HTTP); the rest are bulk-resolved to
// server emoji records, downloaded, and normalised to PNG. Names the server
// doesn't return are failed (not custom emoji); a transport error on the bulk
// resolve marks the whole miss-set for retry rather than burning them.
func (m Model) loadEmojiImages(names []string) tea.Msg {
	ready := map[string][]byte{}
	var failed, misses []string
	for _, name := range names {
		if p, err := cachedEmojiPath(name); err == nil {
			if data, rerr := os.ReadFile(p); rerr == nil && len(data) > 0 {
				ready[name] = data
				continue
			}
		}
		misses = append(misses, name)
	}
	if len(misses) > 0 {
		emojis, err := m.client.CustomEmojisByNames(m.ctx, misses)
		if err != nil {
			// Transient (or old server without the bulk endpoint): retry later.
			return emojiImagesFetchedMsg{ready: ready, retry: misses}
		}
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
			pngBytes, err := normalizeEmojiPNG(raw)
			if err != nil {
				failed = append(failed, name)
				continue
			}
			ready[name] = pngBytes
			if p, perr := cachedEmojiPath(name); perr == nil {
				_ = os.WriteFile(p, pngBytes, 0o644) // best effort
			}
		}
	}
	return emojiImagesFetchedMsg{ready: ready, failed: failed, retry: nil}
}

// handleEmojiImagesFetched installs a finished image batch: each ready emoji
// gets an id, a built transmit sequence, and a placeholder; failed ones settle
// to literal; retried ones are forgotten. Cached post lines that reference a
// newly-ready emoji are invalidated and re-rendered, and the concatenated
// transmit sequences are sent raw (out of band) so the placeholders resolve.
func (m Model) handleEmojiImagesFetched(msg emojiImagesFetchedMsg) (Model, tea.Cmd) {
	if m.emojiImg == nil {
		return m, nil
	}
	var transmit strings.Builder
	readyNames := make(map[string]struct{}, len(msg.ready))
	for name, png := range msg.ready {
		id := m.emojiImg.allocID()
		seq, err := kittyTransmit(id, png)
		if err != nil {
			m.emojiImg.markFailed(name)
			continue
		}
		ph := kittyPlaceholder(id, emojiPlaceholderRows, emojiPlaceholderCols)
		m.emojiImg.markReady(name, id, ph, seq)
		transmit.WriteString(seq)
		readyNames[name] = struct{}{}
	}
	m.emojiImg.markFailed(msg.failed...)
	m.emojiImg.markUnresolved(msg.retry...)
	if len(readyNames) > 0 {
		m.invalidatePostsForEmoji(readyNames)
		m.renderMessages()
		m.renderThread()
	}
	if transmit.Len() == 0 {
		return m, nil
	}
	return m, tea.Raw(transmit.String())
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
