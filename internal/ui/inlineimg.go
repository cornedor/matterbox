package ui

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// Image attachments rendered as inline thumbnails in the transcript, so an image
// is visible without pressing space for the full-size preview (image_thumbnails:
// auto).
//
// The whole reason this works inside a scrolling TUI is the Kitty graphics
// protocol's Unicode-placeholder variant, already used for custom emoji and the
// preview modal: kittyPlaceholder(id, rows, cols) is *ordinary text* — rows lines
// of cols cells, each cell carrying the image id in its truecolor foreground —
// which the terminal paints the image over. So a thumbnail is just extra entries
// in the post's []string of lines (see renderAttachments). It flows through the
// existing wrap → visual-row-count → viewport pipeline untouched: scrolling, the
// msgRowStarts index, the per-post line cache and the 400-post window all keep
// working with no changes, because as far as they're concerned a thumbnail is
// N more lines of text. TestInlineThumbLinesMeasureAsCells guards that invariant.
//
// Lifecycle mirrors emojiImages exactly: an image sighted on screen during a
// render is recorded pending → fetched and decoded off the main goroutine →
// transmitted to the terminal once (out of band, via tea.Raw) → thereafter shown
// by emitting its placeholder lines. Terminal support is *not* probed again: the
// startup graphics probe (emojiImages.graphicsReady) is the same gate, so this
// rides on it — including when custom-emoji images themselves are switched off.

const (
	// inlineThumbRows is the target thumbnail height in text rows. Width follows
	// from the image's real aspect ratio and the terminal's cell pixel size, so
	// nothing is distorted. A fixed height (rather than a share of the pane) keeps
	// the scroll cost of an image message predictable and stops a portrait photo
	// from swallowing the viewport.
	inlineThumbRows = 10

	// inlineThumbMinCols is the narrowest pane (minus the 2-cell gutter) worth
	// drawing into. Below it the thumbnail would be a smear, so we keep the plain
	// filename line.
	inlineThumbMinCols = 8

	// maxInlineImages caps how many thumbnails are held in terminal memory at
	// once. Each is a PNG the terminal keeps until told otherwise, so a long
	// scroll through an image-heavy channel would otherwise accumulate them for
	// the rest of the session. Past the cap the least-recently-sighted images are
	// freed with kittyDelete; because sightings are recorded on every render, the
	// ones on screen always have the freshest stamp and are never the ones evicted.
	// A freed image re-transmits from the disk cache (no HTTP) if scrolled back to.
	maxInlineImages = 64
)

type inlineImgState int

const (
	inlineImgPending  inlineImgState = iota // sighted on screen, awaiting a fetch
	inlineImgFetching                       // fetch in flight
	inlineImgReady                          // transmitted; placeholder usable
	inlineImgFailed                         // undecodable / gone — plain filename line forever
)

type inlineImgEntry struct {
	state inlineImgState
	id    uint32

	rows, cols  int    // placement size in text cells
	box         int    // the max width this placement was fitted to; a narrower pane re-fits
	placeholder string // prebuilt rows×cols placeholder block (ready only)

	// Animation (ready only), same shape as emojiImgEntry: one prebuilt transmit
	// APC per frame, every one targeting id, so flipping frames just re-emits the
	// next APC and the placeholder cells already on screen repaint in place — no
	// re-render. A still image has exactly one entry and a nil delays slice.
	frameSeqs  []string
	delays     []time.Duration
	frameIdx   int
	frameStart time.Time

	// visible is true while this thumbnail is inside a viewport's visible rows.
	// Only visible animated GIFs drive the animation loop — without this, every
	// GIF ever scrolled past would keep ticking (and forcing a re-render) for the
	// rest of the session. Recomputed by refreshAnimVisibility, which only
	// considers animated entries, so a still image's flag is always false — use
	// onScreen, not this, to ask "is it displayed right now".
	visible bool

	// onScreen is true while this thumbnail — animated or still — is inside a
	// viewport's visible rows. It is what protects a displayed image from being
	// evicted; see evictLocked. Recomputed only when a fetched batch is installed
	// (the one place eviction can happen), so it costs nothing per event.
	onScreen bool

	// seen is the sighting stamp used for LRU eviction, bumped by sight(). Note
	// it is NOT bumped on every render: renderPostLines serves an unchanged
	// visible post straight from postLineCache and never reaches sight(), so a
	// displayed image's stamp goes stale. That is exactly why eviction cannot rely
	// on this alone to spare what's on screen, and consults onScreen instead.
	seen uint64
}

// inlineImages manages the images drawn as inline thumbnails. Held as a *pointer*
// on Model (which is value-copied throughout this package) and touched from both
// Update and the render helpers, so every access takes mu.
//
// Keyed by thumbKey — a Mattermost file id for an uploaded attachment, the URL
// itself for an image linked in the message body. Both kinds are drawn, because
// both kinds open in the space-to-preview modal (previewImages), and a feature
// whose whole point is "see the image without pressing space" would be a poor
// joke if it covered fewer images than space does. A Giphy link posted by the
// GIF picker is a body URL, not an attachment, and is the common case.
//
// Whether GIF thumbnails animate is Model.animateInline, not a field here —
// mirroring animatePreview, which the preview modal reads the same way.
type inlineImages struct {
	mu   sync.Mutex
	mode string // "auto" | "off"

	entries map[string]*inlineImgEntry
	pending map[string]previewItem
	tick    uint64 // monotonic sighting counter feeding inlineImgEntry.seen
}

func newInlineImages(mode string) *inlineImages {
	if mode != "auto" {
		mode = "off"
	}
	return &inlineImages{
		mode:    mode,
		entries: map[string]*inlineImgEntry{},
		pending: map[string]previewItem{},
	}
}

// thumbKey identifies a thumbnail: the file id for an uploaded attachment, the
// URL for an image linked in the message body. Empty for anything else, which
// callers treat as "not drawable".
func thumbKey(it previewItem) string {
	if it.file != nil {
		return it.file.Id
	}
	return it.url
}

// enabled reports the config toggle alone. Terminal support is a separate gate
// (emojiImages.graphicsReady) checked by Model.inlineImagesActive. nil-safe.
func (ii *inlineImages) enabled() bool {
	if ii == nil {
		return false
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	return ii.mode == "auto"
}

// sight records that image it is on screen and returns its ready placement, if it
// has one. box is the widest the thumbnail may be (the pane minus the gutter);
// a ready image fitted to a wider box than the pane now allows is re-fetched at
// the new size rather than being drawn too wide and wrapped. Everything else
// (pending, fetching, failed) draws nothing, and the caller falls back to
// whatever it showed before (a filename line, or just the body text).
func (ii *inlineImages) sight(it previewItem, box int) (placeholder string, rows int, ok bool) {
	key := thumbKey(it)
	if key == "" {
		return "", 0, false
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	ii.tick++
	ent := ii.entries[key]
	if ent == nil {
		ii.entries[key] = &inlineImgEntry{state: inlineImgPending, seen: ii.tick}
		ii.pending[key] = it
		return "", 0, false
	}
	ent.seen = ii.tick
	if ent.state != inlineImgReady {
		return "", 0, false
	}
	// Re-fit when the pane no longer matches what this placement was sized for:
	// either it narrowed past the thumbnail's width (it would wrap), or it widened
	// and the thumbnail is still shorter than the target height only because the
	// old, narrower pane clamped it. The bytes are on disk, so a re-fit costs a
	// decode + re-encode, not a download.
	tooWide := ent.cols > box
	couldGrow := box > ent.box && ent.rows < inlineThumbRows
	if tooWide || couldGrow {
		ent.state = inlineImgPending
		ii.pending[key] = it
		return "", 0, false
	}
	return ent.placeholder, ent.rows, true
}

// takePending drains the images sighted since the last call and marks them
// fetching, returning them for a background fetch. Mirrors emojiImages.takePending.
func (ii *inlineImages) takePending() []previewItem {
	ii.mu.Lock()
	defer ii.mu.Unlock()
	if ii.mode != "auto" || len(ii.pending) == 0 {
		return nil
	}
	out := make([]previewItem, 0, len(ii.pending))
	for key, it := range ii.pending {
		out = append(out, it)
		delete(ii.pending, key)
		if ent := ii.entries[key]; ent != nil {
			ent.state = inlineImgFetching
		}
	}
	return out
}

// markReady installs a fetched thumbnail and returns the ids of any images
// evicted to stay under maxInlineImages — the caller frees those in the terminal
// with kittyDelete. Eviction is least-recently-sighted, which never picks an
// on-screen image because sight() re-stamps those on every render.
func (ii *inlineImages) markReady(fileID string, r readyInlineImg) (evicted []uint32) {
	ii.mu.Lock()
	defer ii.mu.Unlock()
	ii.tick++
	// A re-fit (the pane changed width) replaces an entry that already owned a
	// transmitted image under a different id. Free the old one, or it would sit in
	// terminal memory with nothing pointing at it.
	if prev := ii.entries[fileID]; prev != nil && prev.id != 0 && prev.id != r.id {
		evicted = append(evicted, prev.id)
	}
	ii.entries[fileID] = &inlineImgEntry{
		state:       inlineImgReady,
		id:          r.id,
		rows:        r.rows,
		cols:        r.cols,
		box:         r.box,
		placeholder: r.placeholder,
		frameSeqs:   r.frameSeqs,
		delays:      r.delays,
		seen:        ii.tick,
	}
	return append(evicted, ii.evictLocked()...)
}

// evictLocked drops the least-recently-sighted ready images until at most
// maxInlineImages remain, returning their terminal ids. Callers hold mu.
//
// An image that is currently on screen is never a candidate, however old its
// stamp. Freeing one would kittyDelete it out from under the placeholder cells
// still displaying it — and because those cells live in the post's cached lines,
// nothing would re-sight it, so it would stay blank for good. The stamps alone
// can't prevent that: a visible post renders from postLineCache without being
// re-sighted, so its stamp is stale precisely when it matters (see
// inlineImgEntry.seen). Sparing them can leave us a little over the cap when a
// screenful of images exceeds it; a few extra PNGs in terminal memory beats a
// blank hole in the transcript.
func (ii *inlineImages) evictLocked() (evicted []uint32) {
	type aged struct {
		id   string
		seen uint64
	}
	var ready []aged
	onScreen := 0
	for id, ent := range ii.entries {
		if ent.state != inlineImgReady {
			continue
		}
		if ent.onScreen {
			onScreen++
			continue
		}
		ready = append(ready, aged{id, ent.seen})
	}
	over := len(ready) + onScreen - maxInlineImages
	if over <= 0 {
		return nil
	}
	if over > len(ready) {
		over = len(ready) // everything else is displayed; keep it
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].seen < ready[j].seen })
	for _, a := range ready[:over] {
		if ent := ii.entries[a.id]; ent != nil {
			evicted = append(evicted, ent.id)
		}
		// Drop the entry entirely rather than marking it failed: a later sighting
		// re-fetches (from the disk cache) and re-transmits.
		delete(ii.entries, a.id)
	}
	return evicted
}

// setOnScreen marks exactly the named thumbnails as displayed and clears the flag
// on all others, so evictLocked can spare them. Called just before a fetched batch
// is installed — the only point at which eviction runs.
func (ii *inlineImages) setOnScreen(keys map[string]struct{}) {
	if ii == nil {
		return
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	for key, ent := range ii.entries {
		_, ok := keys[key]
		ent.onScreen = ok
	}
}

// readyIDs returns the keys of every ready thumbnail, animated or still.
func (ii *inlineImages) readyIDs() []string {
	if ii == nil {
		return nil
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	out := make([]string, 0, len(ii.entries))
	for id, ent := range ii.entries {
		if ent.state == inlineImgReady {
			out = append(out, id)
		}
	}
	return out
}

func (ii *inlineImages) markFailed(fileIDs ...string) {
	ii.mu.Lock()
	defer ii.mu.Unlock()
	for _, id := range fileIDs {
		ii.entries[id] = &inlineImgEntry{state: inlineImgFailed}
	}
}

// markUnresolved forgets the given ids so a later sighting retries them — used
// after a transient fetch error.
func (ii *inlineImages) markUnresolved(fileIDs ...string) {
	ii.mu.Lock()
	defer ii.mu.Unlock()
	for _, id := range fileIDs {
		delete(ii.entries, id)
	}
}

// readyAnimatedIDs returns the file ids of every ready thumbnail with more than
// one frame — the candidates the animation loop drives.
func (ii *inlineImages) readyAnimatedIDs() []string {
	ii.mu.Lock()
	defer ii.mu.Unlock()
	var out []string
	for id, ent := range ii.entries {
		if ent.state == inlineImgReady && len(ent.frameSeqs) > 1 {
			out = append(out, id)
		}
	}
	return out
}

// setVisibleAnimated marks exactly the named ids visible and clears the flag on
// all others, so advanceFrame animates only on-screen thumbnails.
func (ii *inlineImages) setVisibleAnimated(ids map[string]struct{}) {
	ii.mu.Lock()
	defer ii.mu.Unlock()
	for id, ent := range ii.entries {
		_, ok := ids[id]
		ent.visible = ok
	}
}

// hasVisibleAnimated reports whether any ready animated thumbnail is on screen —
// the cheap check used to (re-)arm the animation loop.
func (ii *inlineImages) hasVisibleAnimated() bool {
	if ii == nil {
		return false
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	for _, ent := range ii.entries {
		if ent.visible && ent.state == inlineImgReady && len(ent.frameSeqs) > 1 {
			return true
		}
	}
	return false
}

// advanceFrame steps every visible animated thumbnail whose current frame's delay
// has elapsed at now, returning the concatenated transmit APCs for those that
// moved. Each re-transmits under the thumbnail's fixed id, so the placeholder
// cells already on screen repaint without any re-render. next is the soonest any
// of them is due again. Byte-for-byte the same scheme as emojiImages.advanceFrame.
func (ii *inlineImages) advanceFrame(now time.Time) (seq string, next time.Duration, animating bool) {
	if ii == nil {
		return "", -1, false
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	var sb strings.Builder
	next = -1
	for _, ent := range ii.entries {
		if ent.state != inlineImgReady || len(ent.frameSeqs) <= 1 || !ent.visible {
			continue
		}
		animating = true
		if ent.frameStart.IsZero() {
			ent.frameStart = now
		}
		advanced := false
		// Catch up across as many frames as the elapsed time covers, so a late tick
		// skips frames rather than playing in slow motion.
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

// --- Model-side gating, sizing and render ---------------------------------

// inlineImagesActive reports whether thumbnails can be drawn: the config toggle
// is on *and* the terminal cleared the graphics probe (Kitty-class, truecolor,
// not tmux). There is deliberately no second probe — the placeholder encoding is
// identical, so what works for emoji works here. It gates on graphicsReady rather
// than emojiImages.active so that `emoji_images: off` with `image_thumbnails:
// auto` is a working combination.
func (m *Model) inlineImagesActive() bool {
	return m.inlineImg.enabled() && m.emojiImg.graphicsReady()
}

// inlineThumbBox is the widest a thumbnail may be at the given pane width: the
// pane minus the two-cell gutter every body line is indented by. Returns 0 when
// the pane is too narrow to be worth it.
func inlineThumbBox(paneWidth int) int {
	box := paneWidth - 2
	if box < inlineThumbMinCols {
		return 0
	}
	return box
}

// inlineThumbCells picks the cell box for a wPx×hPx image: aspect-correct, at
// most inlineThumbRows tall and box wide. fitImageCells (shared with the preview
// modal) will not upscale past the image's natural size, so a small image stays
// small instead of being blown up to fill ten rows.
func inlineThumbCells(wPx, hPx, box, cellPxW, cellPxH int) (cols, rows int) {
	return fitImageCells(wPx, hPx, box, inlineThumbRows, cellPxW, cellPxH)
}

// inlineThumbLines returns the placeholder rows for a ready image, each indented
// by the standard two-cell gutter so it lines up with the message body and
// survives wrapBodyLine untouched (the placement is fitted to the pane width, so
// it never needs wrapping). Returns nil when thumbnails are off, the terminal
// can't do them, the pane is too narrow, or the image's bytes haven't arrived
// yet — in every one of those cases the caller falls back to what it drew before.
//
// The rows are emitted raw, never through a lipgloss style: the image id rides in
// each cell's truecolor foreground, and a style would overwrite it and collapse
// the image.
func (m *Model) inlineThumbLines(it previewItem, paneWidth int) []string {
	if !m.inlineImagesActive() {
		return nil
	}
	box := inlineThumbBox(paneWidth)
	if box == 0 {
		return nil
	}
	ph, _, ok := m.inlineImg.sight(it, box)
	if !ok {
		return nil
	}
	rows := strings.Split(ph, "\n")
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = "  " + r
	}
	return out
}

// inlineFileThumbLines draws an uploaded image attachment (used by
// renderAttachments, above the file's own filename line).
func (m *Model) inlineFileThumbLines(f *model.FileInfo, paneWidth int) []string {
	if f == nil || !previewableMIME(f.MimeType) {
		return nil
	}
	return m.inlineThumbLines(previewItem{file: f, name: f.Name}, paneWidth)
}

// inlineBodyImageLines draws every image *linked in the message body* — the
// ![](…) a GIF picker posts, and any bare/linked URL whose path looks like an
// image. These are not attachments (they carry no FileInfo), so renderAttachments
// never sees them; without this the common case of a pasted Giphy link would show
// no thumbnail at all while space-to-preview happily opened it.
//
// Appended after the body lines, in the order previewImages enumerates them, so
// the thumbnail sits directly under the text that links it.
func (m *Model) inlineBodyImageLines(p *model.Post, width int) []string {
	if !m.inlineImagesActive() || p == nil {
		return nil
	}
	var out []string
	for _, it := range previewImages(p) {
		if it.file != nil {
			continue // an attachment: renderAttachments draws it, above its chip
		}
		out = append(out, m.inlineThumbLines(it, width)...)
	}
	return out
}

// --- background fetch + Update wiring -------------------------------------

// readyInlineImg is a fetched, decoded, right-sized and encoded thumbnail: an
// allocated terminal id, its placeholder block, and one transmit APC per frame
// (a still image has exactly one and a nil delays slice). Built off the main
// goroutine so no decode/PNG-encode work lands on the render loop.
type readyInlineImg struct {
	id          uint32
	rows, cols  int
	box         int
	placeholder string
	frameSeqs   []string
	delays      []time.Duration
}

// inlineImagesFetchedMsg is the result of a background thumbnail batch. ready maps
// thumbKey → its built image; failed keys are undecodable and stop being asked
// for; retry keys hit a transient error and are forgotten so a later sighting
// tries again.
type inlineImagesFetchedMsg struct {
	ready  map[string]readyInlineImg
	failed []string
	retry  []string
}

// fetchPendingInlineImages drains the images sighted during the last render and,
// if any, returns a Cmd that downloads, decodes and encodes them in the
// background. Mirrors fetchPendingEmoji: run from Update after each event,
// returns nil cheaply when nothing is pending or the feature is inactive.
func (m *Model) fetchPendingInlineImages() tea.Cmd {
	if !m.inlineImagesActive() {
		return nil
	}
	items := m.inlineImg.takePending()
	if len(items) == 0 {
		return nil
	}
	box := inlineThumbBox(m.msgsView.Width())
	if box == 0 {
		// Pane too narrow to place them; forget the sightings so a resize retries.
		keys := make([]string, len(items))
		for i, it := range items {
			keys[i] = thumbKey(it)
		}
		m.inlineImg.markUnresolved(keys...)
		return nil
	}
	snap := m // value copy: the Cmd runs on another goroutine
	return func() tea.Msg {
		return snap.loadInlineImages(items, box)
	}
}

// loadInlineImages fetches and builds a batch of sighted thumbnails. Runs on a
// background goroutine.
func (m Model) loadInlineImages(items []previewItem, box int) tea.Msg {
	ready := make(map[string]readyInlineImg, len(items))
	var failed, retry []string
	for _, it := range items {
		key := thumbKey(it)
		r, err := m.buildInlineThumb(it, box)
		switch {
		case err == nil:
			ready[key] = r
		case isDecodeFailure(err):
			failed = append(failed, key) // we'll never decode it — stop asking
		default:
			retry = append(retry, key) // network etc: a later sighting retries
		}
	}
	return inlineImagesFetchedMsg{ready: ready, failed: failed, retry: retry}
}

// buildInlineThumb downloads (or reads from the disk cache) an image attachment,
// decodes its frames, right-sizes each to the thumbnail's cell box, and prebuilds
// the transmit sequences and placeholder. All of it off the render loop.
func (m Model) buildInlineThumb(it previewItem, box int) (readyInlineImg, error) {
	raw, err := m.readThumbBytes(it)
	if err != nil {
		return readyInlineImg{}, err
	}
	// Animate any GIF, whether it arrived as an attachment or a body link — a
	// Giphy link is the latter, and a frozen Giphy would be a strange thing to ship.
	// decodeImageFrames sniffs the bytes, so a mislabelled MIME can't fool it.
	frames, delays, err := decodeImageFrames(raw, m.animateInline)
	if err != nil {
		return readyInlineImg{}, decodeFailure{err}
	}
	if len(frames) == 0 {
		return readyInlineImg{}, decodeFailure{fmt.Errorf("no frames")}
	}

	// Size from the decoded frame rather than FileInfo.Width/Height: a server
	// preview rendition is already downscaled, so its real bounds are what the
	// placement must match.
	b := frames[0].Bounds()
	cols, rows := inlineThumbCells(b.Dx(), b.Dy(), box, m.cellPxW, m.cellPxH)

	id := m.emojiImg.allocID() // one shared 24-bit id space with emoji + preview
	seqs := make([]string, len(frames))
	for i, fr := range frames {
		fitted := fitFrameToCells(fr, cols, rows, m.cellPxW, m.cellPxH)
		seq, err := kittyTransmitImage(id, fitted, rows, cols)
		if err != nil {
			return readyInlineImg{}, decodeFailure{err}
		}
		seqs[i] = seq
	}
	return readyInlineImg{
		id:          id,
		rows:        rows,
		cols:        cols,
		box:         box,
		placeholder: kittyPlaceholder(id, rows, cols),
		frameSeqs:   seqs,
		delays:      delays,
	}, nil
}

// readThumbBytes returns the bytes to build a thumbnail from.
//
// For an *attachment* it prefers the server's downscaled preview rendition
// (≤~1MP) over the original — a phone photo's original is many megabytes at 12MP+
// and a thumbnail is ~10 rows tall, so the original would be paid for in
// transfer, decode and PNG re-encode for nothing. An animated GIF we intend to
// animate is the exception: its rendition is a single flattened frame, so the
// original is needed to play it.
//
// For a *body image URL* (a Giphy link and friends) there is no rendition; the
// bytes come over HTTP, size-capped, and are cached by URL hash.
//
// Deliberately the same disk-cache paths as the preview modal (cachedPreviewPath /
// cachedFilePath / cachedURLPath), so opening the full preview of an image you
// already have a thumbnail for costs no second download.
func (m Model) readThumbBytes(it previewItem) ([]byte, error) {
	if it.file == nil {
		path, _ := cachedURLPath(it.url)
		return m.readOrDownloadURL(path, it.url)
	}
	f := it.file
	animating := m.animateInline && f.MimeType == "image/gif"
	if !animating && f.HasPreviewImage {
		if data, err := m.readOrDownloadFilePreview(f); err == nil && len(data) > 0 {
			return data, nil
		}
		// Any preview failure (server lacks it, network) falls through to the
		// original so we still show something.
	}
	path, _ := m.cachedFilePath(f)
	data, err := m.readOrDownloadFile(path, f)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, decodeFailure{fmt.Errorf("empty file")}
	}
	return data, nil
}

// decodeFailure marks an error as permanent — the bytes arrived but we can't turn
// them into an image, so retrying would just fail again. Anything else (a
// download error) is treated as transient.
type decodeFailure struct{ err error }

func (d decodeFailure) Error() string { return d.err.Error() }
func (d decodeFailure) Unwrap() error { return d.err }

func isDecodeFailure(err error) bool {
	var d decodeFailure
	return errors.As(err, &d)
}

// handleInlineImagesFetched installs a finished thumbnail batch: each ready image
// is recorded, the posts that own it have their cached lines dropped so the next
// render picks up the placeholder, and the image is transmitted raw (out of band)
// so those placeholders resolve. Images evicted to stay under the terminal-memory
// cap are freed in the same batch. If any installed thumbnail is animated and on
// screen, the animation tick is armed.
func (m Model) handleInlineImagesFetched(msg inlineImagesFetchedMsg) (Model, tea.Cmd) {
	if m.inlineImg == nil {
		return m, nil
	}
	var transmit strings.Builder
	readyKeys := make(map[string]struct{}, len(msg.ready))
	animated := false
	// Installing these is what can push us over maxInlineImages and trigger
	// eviction, so tell the cache what is displayed right now first — an on-screen
	// image must never be the one freed, and its LRU stamp can't be trusted to say
	// so (see inlineImgEntry.seen).
	m.inlineImg.setOnScreen(m.visibleInlineImageKeys())
	for key, r := range msg.ready {
		if len(r.frameSeqs) == 0 {
			m.inlineImg.markFailed(key)
			continue
		}
		for _, id := range m.inlineImg.markReady(key, r) {
			transmit.WriteString(kittyDelete(id)) // freed to stay under the cap
		}
		transmit.WriteString(r.frameSeqs[0])
		readyKeys[key] = struct{}{}
		if len(r.frameSeqs) > 1 {
			animated = true
		}
	}
	m.inlineImg.markFailed(msg.failed...)
	m.inlineImg.markUnresolved(msg.retry...)

	if len(readyKeys) > 0 {
		m.invalidatePostsForThumbs(readyKeys)
		m.renderMessages()
		m.renderThread()
	}
	var cmds []tea.Cmd
	if transmit.Len() > 0 {
		cmds = append(cmds, tea.Raw(transmit.String()))
	}
	if animated && !m.imgAnimating && m.inlineImg.hasVisibleAnimated() {
		m.imgAnimating = true
		cmds = append(cmds, imgAnimTickCmd(0))
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// postOwnsThumb reports whether post p is where thumbnail key would be drawn: an
// attachment key matches one of the post's files; a body-image key is the URL
// itself, so it matches when the body links it. The http prefix keeps a 26-char
// file id from being substring-matched against message text.
func postOwnsThumb(p *model.Post, key string) bool {
	if p == nil || key == "" {
		return false
	}
	if strings.HasPrefix(key, "http") {
		return strings.Contains(p.Message, key)
	}
	if p.Metadata == nil {
		return false
	}
	for _, f := range p.Metadata.Files {
		if f != nil && f.Id == key {
			return true
		}
	}
	return false
}

// invalidatePostsForThumbs drops the cached rendered lines of every on-screen post
// (main feed + open thread, already bounded by the render window) that owns one of
// the named thumbnails, so the next render picks up the now-ready placeholder. The
// post fingerprint doesn't track image readiness, so this explicit invalidation is
// what makes a just-fetched thumbnail appear — the same gap invalidatePostsForEmoji
// fills for custom emoji.
func (m *Model) invalidatePostsForThumbs(keys map[string]struct{}) {
	check := func(p *model.Post) {
		if p == nil || p.Id == "" {
			return
		}
		for key := range keys {
			if postOwnsThumb(p, key) {
				m.invalidatePostLines(p.Id)
				return
			}
		}
	}
	for _, p := range m.posts {
		check(p)
	}
	for _, p := range m.threadPosts {
		check(p)
	}
}

// viewportVisibleInlineImages returns the ready *animated* thumbnails currently
// on screen — the set that drives the animation loop. Short-circuits to nil (the
// overwhelmingly common case) when no animated thumbnail is cached, which is what
// keeps it off the per-event path's back. Mirrors viewportVisibleAnimatedEmoji.
func (m *Model) viewportVisibleInlineImages() map[string]struct{} {
	if m.inlineImg == nil {
		return nil
	}
	return m.visibleThumbKeys(m.inlineImg.readyAnimatedIDs())
}

// visibleInlineImageKeys returns every ready thumbnail on screen, animated or
// still — the set eviction must spare. Deliberately *not* on the per-event path:
// it is computed only when a fetched batch is installed, the one place eviction
// can run.
func (m *Model) visibleInlineImageKeys() map[string]struct{} {
	if m.inlineImg == nil {
		return nil
	}
	return m.visibleThumbKeys(m.inlineImg.readyIDs())
}

// visibleThumbKeys narrows keys to those owned by a post currently inside a
// viewport's visible rows — the main message pane plus the open thread. It maps
// the live YOffset back to on-screen posts through the row spans captured by
// renderMessages/renderThread, so it tracks scrolling without a re-render.
func (m *Model) visibleThumbKeys(keys []string) map[string]struct{} {
	if len(keys) == 0 {
		return nil
	}
	visible := make(map[string]struct{}, len(keys))
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
			for _, key := range keys {
				if _, done := visible[key]; done {
					continue
				}
				if postOwnsThumb(p, key) {
					visible[key] = struct{}{}
				}
			}
			if len(visible) == len(keys) {
				return
			}
		}
	}
	scan(m.posts, m.msgRowStarts, m.msgsView.YOffset(), m.msgsView.Height())
	if m.threadOpen && len(visible) < len(keys) {
		scan(m.threadPosts, m.threadRowStarts, m.threadView.YOffset(), m.threadView.Height())
	}
	return visible
}
