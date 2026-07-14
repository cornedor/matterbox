package ui

import (
	"errors"
	"fmt"
	"image"
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

	// maxInlineImages caps how many thumbnails are resident in *terminal* memory
	// at once. Each is a PNG the terminal holds until told otherwise, so a long
	// scroll through an image-heavy channel would otherwise accumulate them for the
	// rest of the session. Past the cap the least-recently-sighted ones are freed
	// with kittyDelete — never one that is on screen (see evictResidentLocked).
	//
	// Freeing an image does NOT discard the built thumbnail: the decoded,
	// downscaled, PNG-encoded frames stay in inlineImgEntry, so scrolling back to it
	// costs one re-transmit of a string we already have, not another decode +
	// re-encode. Conflating the two was what turned an image-heavy channel into a
	// permanent rebuild loop (TestThumbFetchConverges).
	maxInlineImages = 64

	// maxInlineBuiltBytes bounds the built frames we keep in *our* memory, which is
	// the resource maxInlineImages is not. A still thumbnail's PNG is tens of KB; a
	// 30-frame GIF is a few MB, so a count-based cap would be meaningless here.
	// Past the budget the coldest entries are dropped outright and would have to be
	// rebuilt if revisited — the rare case this whole scheme exists to keep rare.
	maxInlineBuiltBytes = 64 << 20

	// inlineFetchMarginScreens is how far beyond the visible rows a thumbnail is
	// still worth fetching, in viewport heights. The render window holds up to
	// maxLoadedPosts (400) posts but the terminal only ever displays one screenful,
	// so building every image in the window means paying for ~20 thumbnails to show
	// ~3. Fetching only what's near the viewport cuts that, and the margin means an
	// image is ready by the time you scroll to it rather than popping in under you.
	inlineFetchMarginScreens = 2

	// nominalBodyImage{W,H} is the size assumed for a body-image URL — a Giphy link
	// and friends — which, unlike an uploaded attachment, carries no dimensions we
	// can read before fetching it. It is only ever used to *reserve* the rows the
	// thumbnail will occupy (see reserveThumbCells); the real bytes decide the final
	// placement. A wrong guess costs at most a cosmetically wrong-width blank
	// holder, never a wrong row count — see that function for why.
	nominalBodyImageW = 480
	nominalBodyImageH = 270

	// thumbOpenChevron / thumbShutChevron prefix an image's indicator — an
	// attachment's "🖼️ name" line, a body image's "🖼️ alt" or link text — with
	// whether its thumbnail is showing. The disclosure-triangle idiom: pointing down
	// at the image, or right at the space it would take. Only ever drawn when a
	// thumbnail is actually being drawn (see Model.thumbChevron), so with
	// image_thumbnails off, or on a terminal that can't paint them, the indicator
	// looks exactly as it always has.
	thumbOpenChevron = "▾"
	thumbShutChevron = "▸"
)

type inlineImgState int

const (
	inlineImgPending  inlineImgState = iota // sighted, awaiting a fetch
	inlineImgFetching                       // fetch in flight
	inlineImgReady                          // built; placeholder usable
	inlineImgFailed                         // undecodable / gone — plain filename line forever
)

type inlineImgEntry struct {
	state inlineImgState
	id    uint32

	rows, cols  int    // placement size in text cells
	box         int    // the max width this placement was fitted to; a narrower pane re-fits
	placeholder string // prebuilt rows×cols placeholder block (ready only)

	// resident is whether the image is currently in *terminal* memory. It is not
	// the same question as state == inlineImgReady, which says we have the built
	// frames in *our* memory. Freeing an image to stay under maxInlineImages clears
	// this and keeps everything else, so a later sighting re-transmits frameSeqs
	// rather than rebuilding them. sight() queues a re-transmit for any ready entry
	// it finds non-resident.
	resident bool

	// reservedRows/reservedCols is the space this thumbnail will occupy, predicted
	// before its bytes arrive (see reserveThumbCells). A not-yet-fetched image draws
	// that many blank rows, so the post is already its final height and loading the
	// image never reflows the transcript under a wheel-scroll — which anchors on an
	// absolute row offset (m.msgFreeOffset) and would otherwise jump.
	reservedRows, reservedCols int

	// Animation (ready only), same shape as emojiImgEntry: one prebuilt transmit
	// APC per frame, every one targeting id, so flipping frames just re-emits the
	// next APC and the placeholder cells already on screen repaint in place — no
	// re-render. A still image has exactly one entry and a nil delays slice.
	//
	// So, for a while, does an animated GIF: encoding a frame costs ~10ms, and a GIF
	// only animates while it is on screen, so a GIF is built as its first frame alone
	// and the rest are encoded only if it is ever actually displayed (inlineImages.
	// deferred → buildVisibleThumbFrames → markFramesBuilt fills these in). Every
	// consumer here already treats len(frameSeqs) <= 1 as "still", so a GIF waiting
	// for its frames simply doesn't animate yet.
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
	// freed; see evictResidentLocked. Recomputed only where eviction can run, so it
	// costs nothing per event.
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
	// pending is every image sighted but not yet built. Unlike the emoji path it is
	// NOT drained wholesale: fetchPendingInlineImages takes only the entries near
	// the viewport and leaves the rest here, so an image deep in the render window
	// is never built until you scroll toward it. Entries therefore persist across
	// renders, which is what lets a post that is *not* re-sighted (its lines are
	// cached) still get fetched later when it comes into reach.
	pending map[string]previewItem
	// deferred is the built-but-still GIFs whose frames past the first have not been
	// encoded yet — the second half of the laziness. `pending` withholds work from
	// images that are far from the viewport; this withholds the *animation* frames
	// from images that were built but are not actually on screen, which is most of
	// them (the fetch margin reaches several screens further than the eye does).
	// buildVisibleThumbFrames drains the ones that make it on screen. It holds the
	// previewItem rather than the bytes: the frames are re-read from the same disk
	// cache the first build populated, so a GIF scrolled past costs no memory beyond
	// its one still frame.
	deferred map[string]previewItem
	// needTransmit is the ready-but-not-resident images sight() has seen this
	// render; the Update wrapper drains it and re-transmits them (flushInlineTransmits).
	needTransmit map[string]struct{}
	// release is the images a just-collapsed post asked us to free from terminal
	// memory (see Model.releaseThumbs). Drained by takeTransmits, which only frees
	// the ones that are not on screen — the same key can be drawn by another post
	// that is still expanded.
	release map[string]struct{}
	// builtBytes tracks the frameSeqs we are holding, against maxInlineBuiltBytes.
	builtBytes int
	// lastScan memoizes the last (viewport offsets, content versions, pending count)
	// the fetch scan ran against. In an image-heavy channel `pending` is never empty
	// — the images beyond the fetch margin sit there indefinitely — so without this
	// every keystroke would re-walk the posts looking for something to fetch. See
	// needsFetchScan.
	lastScan [5]uint64
	tick     uint64 // monotonic sighting counter feeding inlineImgEntry.seen
}

func newInlineImages(mode string) *inlineImages {
	if mode != "auto" {
		mode = "off"
	}
	return &inlineImages{
		mode:         mode,
		entries:      map[string]*inlineImgEntry{},
		pending:      map[string]previewItem{},
		deferred:     map[string]previewItem{},
		needTransmit: map[string]struct{}{},
		release:      map[string]struct{}{},
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

// sightResult is what a render learns about one image: either draw its
// placeholder, or hold rows×cols of blank space for the thumbnail that is coming,
// or draw nothing at all because there will never be one.
type sightResult struct {
	placeholder string // ready: the rows×cols placeholder block
	rows, cols  int    // the space it occupies — reserved size until it is ready
	ready       bool   // draw the placeholder
	reserve     bool   // draw blanks: not fetched yet, but it will be
}

// sight records that image it was rendered, and says what to draw for it. box is
// the widest the thumbnail may be (the pane minus the gutter); resRows/resCols is
// the space to hold for it until its bytes arrive (see Model.reserveThumbCells).
//
// A first sighting records the image as pending — it is NOT fetched here, and may
// not be fetched for a long time: fetchPendingInlineImages only takes the ones near
// the viewport. Until then the post reserves the thumbnail's height, so the image
// appearing later never changes the post's size and never reflows the transcript.
func (ii *inlineImages) sight(it previewItem, box, resRows, resCols int) sightResult {
	key := thumbKey(it)
	if key == "" {
		return sightResult{}
	}
	reserved := sightResult{rows: resRows, cols: resCols, reserve: resRows > 0}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	ii.tick++
	ent := ii.entries[key]
	if ent == nil {
		ii.entries[key] = &inlineImgEntry{
			state: inlineImgPending, seen: ii.tick,
			reservedRows: resRows, reservedCols: resCols,
		}
		ii.pending[key] = it
		return reserved
	}
	ent.seen = ii.tick
	switch ent.state {
	case inlineImgFailed:
		return sightResult{} // no image is coming; don't hold space for one
	case inlineImgPending, inlineImgFetching:
		// Keep the size we first reserved rather than resRows/resCols. The reservation
		// is what the post's cached lines were built against, and re-predicting it
		// mid-flight (a resize changed the box) would resize the post out from under
		// them. A real resize drops the line cache and re-sights anyway.
		return sightResult{rows: ent.reservedRows, cols: ent.reservedCols, reserve: ent.reservedRows > 0}
	}
	// Ready. Re-fit when the pane no longer matches what this placement was sized
	// for: either it narrowed past the thumbnail's width (it would wrap), or it
	// widened and the thumbnail is still shorter than the target height only because
	// the old, narrower pane clamped it.
	tooWide := ent.cols > box
	couldGrow := box > ent.box && ent.rows < inlineThumbRows
	if tooWide || couldGrow {
		ent.state = inlineImgPending
		ent.reservedRows, ent.reservedCols = resRows, resCols
		ii.pending[key] = it
		delete(ii.deferred, key) // it rebuilds from scratch, under a fresh id
		return reserved
	}
	// Built, but the terminal may no longer hold it — we free images to stay under
	// maxInlineImages without discarding the frames. Queue a re-transmit (a string
	// we already have) rather than rebuilding it.
	if !ent.resident {
		ii.needTransmit[key] = struct{}{}
	}
	return sightResult{placeholder: ent.placeholder, rows: ent.rows, cols: ent.cols, ready: true}
}

// needsFetchScan reports whether anything could have changed which pending images
// are within fetching reach: a viewport moved, its content was re-rendered, or a
// new image was sighted. It is the guard that keeps the fetch scan off the typing
// path — in an image-heavy channel `pending` is *never* empty (everything beyond
// the fetch margin parks there indefinitely), so "is anything pending?" is not the
// cheap early-out it looks like, and without this every keystroke would re-walk the
// posts to answer a question whose inputs hadn't moved.
func (ii *inlineImages) needsFetchScan(key [5]uint64) bool {
	if ii == nil {
		return false
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	if ii.mode != "auto" || len(ii.pending) == 0 || key == ii.lastScan {
		return false
	}
	ii.lastScan = key
	return true
}

// pendingKeys lists the images sighted but not yet built, so the Model can work
// out which of them are near enough to the viewport to be worth fetching.
func (ii *inlineImages) pendingKeys() []string {
	if ii == nil {
		return nil
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	if ii.mode != "auto" || len(ii.pending) == 0 {
		return nil
	}
	out := make([]string, 0, len(ii.pending))
	for key := range ii.pending {
		out = append(out, key)
	}
	return out
}

// takePending drains the pending images named in want, marking them fetching.
// Anything not in want stays pending — it is too far from the viewport to be worth
// building, and will be picked up if it ever comes into reach.
func (ii *inlineImages) takePending(want map[string]struct{}) []previewItem {
	if ii == nil || len(want) == 0 {
		return nil
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	if ii.mode != "auto" {
		return nil
	}
	out := make([]previewItem, 0, len(want))
	for key := range want {
		it, ok := ii.pending[key]
		if !ok {
			continue
		}
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
// markReady installs a freshly built thumbnail as ready and resident (the caller
// transmits its first frame). It returns the terminal id of any image this one
// replaced — a re-fit at a new pane width builds under a fresh id, and the old one
// would otherwise sit in terminal memory with nothing pointing at it.
//
// It does not evict: that is enforceCapsLocked's job, run once per event from
// flushInlineTransmits rather than once per installed image.
func (ii *inlineImages) markReady(fileID string, r readyInlineImg) (replaced []uint32) {
	ii.mu.Lock()
	defer ii.mu.Unlock()
	ii.tick++
	if prev := ii.entries[fileID]; prev != nil {
		if prev.id != 0 && prev.id != r.id && prev.resident {
			replaced = append(replaced, prev.id)
		}
		ii.builtBytes -= prev.builtSize()
	}
	ent := &inlineImgEntry{
		state:       inlineImgReady,
		id:          r.id,
		rows:        r.rows,
		cols:        r.cols,
		box:         r.box,
		placeholder: r.placeholder,
		frameSeqs:   r.frameSeqs,
		delays:      r.delays,
		resident:    true,
		seen:        ii.tick,
	}
	ii.entries[fileID] = ent
	ii.builtBytes += ent.builtSize()
	// A GIF arrives as its first frame only; the rest are encoded if and when it is
	// displayed. Until then it is an ordinary still, and this is the note to come back.
	if r.deferredFrames {
		ii.deferred[fileID] = r.item
	} else {
		delete(ii.deferred, fileID)
	}
	return replaced
}

// takeDeferredFrames hands back the GIFs whose remaining frames are now worth
// encoding: built, and on screen. Each is taken out of ii.deferred, so at most one
// build is ever in flight per thumbnail; if that build fails, the thumbnail stays
// the still it already is (which is what the user is looking at anyway) rather than
// being retried on every event.
func (ii *inlineImages) takeDeferredFrames() []thumbFramesJob {
	if ii == nil {
		return nil
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	if ii.mode != "auto" || len(ii.deferred) == 0 {
		return nil
	}
	var out []thumbFramesJob
	for key, it := range ii.deferred {
		ent := ii.entries[key]
		if ent == nil || ent.state != inlineImgReady || !ent.onScreen {
			continue
		}
		delete(ii.deferred, key)
		out = append(out, thumbFramesJob{key: key, item: it, id: ent.id, box: ent.box})
	}
	return out
}

// markFramesBuilt fills in the animation frames of a GIF that was built as a still,
// leaving everything else about the entry — its id, its placement, the placeholder
// on screen — exactly as it was. The frames all target the same id as the still, so
// there is nothing to re-transmit and nothing to re-render: the next animation tick
// simply has somewhere to go. The caller need not even arm the loop, since the
// per-event kicker (maybeStartImageAnim) sees the entry become animated.
//
// It is a no-op unless this is still the entry the frames were built for. A re-fit
// at a new pane width rebuilds under a fresh id and evictBuiltLocked can drop the
// entry outright, either of which makes these frames garbage; and a cell-size change
// under the build would land them on a different cell box, which is the one thing
// that could reflow the transcript.
func (ii *inlineImages) markFramesBuilt(b builtThumbFrames) bool {
	if ii == nil {
		return false
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	ent := ii.entries[b.key]
	if ent == nil || ent.state != inlineImgReady || ent.id != b.id ||
		ent.rows != b.rows || ent.cols != b.cols || len(b.seqs) <= 1 {
		return false
	}
	ii.builtBytes -= ent.builtSize()
	ent.frameSeqs = b.seqs
	ent.delays = b.delays
	ent.frameIdx = 0 // the still already on screen is frame 0
	ent.frameStart = time.Time{}
	ii.builtBytes += ent.builtSize()
	return true
}

// builtSize is the memory this entry's built frames occupy, for the
// maxInlineBuiltBytes budget.
func (e *inlineImgEntry) builtSize() int {
	n := len(e.placeholder)
	for _, s := range e.frameSeqs {
		n += len(s)
	}
	return n
}

// evictResidentLocked frees the least-recently-sighted images from *terminal*
// memory until at most maxInlineImages remain resident, returning their ids for
// kittyDelete. The built frames stay in ii.entries, so a later sighting re-transmits
// a string we already have instead of decoding and re-encoding the image again —
// which is what turned an image-heavy channel into a permanent rebuild loop.
//
// An image that is on screen is never a candidate, however old its stamp. Freeing
// one would kittyDelete it out from under the placeholder cells still displaying
// it, and those cells live in the post's cached lines, so nothing would re-sight it
// and it would stay blank. Stamps alone can't prevent that: a visible post renders
// from postLineCache without being re-sighted, so its stamp is stale precisely when
// it matters (see inlineImgEntry.seen). Sparing them can leave us a little over the
// cap when a screenful of images exceeds it; a few extra PNGs in terminal memory
// beats a blank hole in the transcript. Callers hold mu.
func (ii *inlineImages) evictResidentLocked() (freed []uint32) {
	type aged struct {
		key  string
		seen uint64
	}
	var cand []aged
	resident, pinned := 0, 0
	for key, ent := range ii.entries {
		if ent.state != inlineImgReady || !ent.resident {
			continue
		}
		resident++
		if ent.onScreen {
			pinned++
			continue
		}
		cand = append(cand, aged{key, ent.seen})
	}
	over := resident - maxInlineImages
	if over <= 0 {
		return nil
	}
	if over > len(cand) {
		over = len(cand) // the rest are displayed; keep them resident
	}
	sort.Slice(cand, func(i, j int) bool { return cand[i].seen < cand[j].seen })
	for _, a := range cand[:over] {
		ent := ii.entries[a.key]
		if ent == nil {
			continue
		}
		freed = append(freed, ent.id)
		ent.resident = false // the frames stay; only the terminal copy goes
		delete(ii.needTransmit, a.key)
	}
	return freed
}

// evictBuiltLocked drops the coldest entries outright once the built frames exceed
// maxInlineBuiltBytes — the only place a thumbnail's expensive work is thrown away,
// and the only case that ever pays for a rebuild. Never touches an on-screen image.
// Callers hold mu.
func (ii *inlineImages) evictBuiltLocked() (freed []uint32) {
	if ii.builtBytes <= maxInlineBuiltBytes {
		return nil
	}
	type aged struct {
		key  string
		seen uint64
	}
	var cand []aged
	for key, ent := range ii.entries {
		if ent.state == inlineImgReady && !ent.onScreen {
			cand = append(cand, aged{key, ent.seen})
		}
	}
	sort.Slice(cand, func(i, j int) bool { return cand[i].seen < cand[j].seen })
	for _, a := range cand {
		if ii.builtBytes <= maxInlineBuiltBytes {
			break
		}
		ent := ii.entries[a.key]
		if ent == nil {
			continue
		}
		if ent.resident {
			freed = append(freed, ent.id)
		}
		ii.builtBytes -= ent.builtSize()
		delete(ii.entries, a.key)
		delete(ii.needTransmit, a.key)
		delete(ii.deferred, a.key)
	}
	return freed
}

// queueRelease asks for these images to be freed from terminal memory at the end of
// the event — the post drawing them was just collapsed. Whether each may actually go
// is decided in takeTransmits, once the render has settled which images are on
// screen.
func (ii *inlineImages) queueRelease(keys []string) {
	if ii == nil || len(keys) == 0 {
		return
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	for _, key := range keys {
		ii.release[key] = struct{}{}
	}
}

// releaseCollapsedLocked frees the queued images that are no longer on screen,
// returning their ids for kittyDelete. An image still drawn *somewhere* — the same
// Giphy URL posted twice, one copy collapsed and one not — is left alone; releasing
// it would kittyDelete it out from under the placeholder cells still showing it.
//
// Only the terminal copy goes. The built frames stay in ii.entries, so expanding the
// post again costs the re-transmit sight() queues, not a rebuild. Callers hold mu.
func (ii *inlineImages) releaseCollapsedLocked() (freed []uint32) {
	for key := range ii.release {
		delete(ii.release, key)
		ent := ii.entries[key]
		if ent == nil || ent.onScreen {
			continue
		}
		// Whatever the state, it is not being drawn: stop it animating, and drop any
		// re-transmit queued for it (sight() queues a fresh one if it is ever drawn
		// again). A GIF collapsed mid-play resumes from the frame it stopped on
		// (frameIdx survives), which is the one that re-transmit carries.
		//
		// Its ii.deferred entry, though, stays: that is the note saying this GIF was
		// built as a still and still owes its animation frames (see buildInlineThumb).
		// Dropping it would leave a GIF collapsed before those frames were encoded
		// frozen for the rest of the session — takeDeferredFrames already declines to
		// build for anything off screen, so there is nothing to suppress here.
		ent.visible = false
		delete(ii.needTransmit, key)
		if ent.state != inlineImgReady || !ent.resident {
			continue
		}
		ent.resident = false
		freed = append(freed, ent.id)
	}
	return freed
}

// takeTransmits drains the re-transmits sight() queued for ready images the
// terminal no longer holds, frees the thumbnails of any post just collapsed, then
// enforces both caps — returning everything to write out of band in one string: the
// re-transmit APCs followed by the kittyDeletes.
func (ii *inlineImages) takeTransmits() string {
	if ii == nil {
		return ""
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	var sb strings.Builder
	for key := range ii.needTransmit {
		delete(ii.needTransmit, key)
		ent := ii.entries[key]
		if ent == nil || ent.state != inlineImgReady || ent.resident || len(ent.frameSeqs) == 0 {
			continue
		}
		sb.WriteString(ent.frameSeqs[ent.frameIdx])
		ent.resident = true
	}
	for _, id := range ii.releaseCollapsedLocked() {
		sb.WriteString(kittyDelete(id))
	}
	for _, id := range ii.evictResidentLocked() {
		sb.WriteString(kittyDelete(id))
	}
	for _, id := range ii.evictBuiltLocked() {
		sb.WriteString(kittyDelete(id))
	}
	return sb.String()
}

// setOnScreen marks exactly the named thumbnails as displayed and clears the flag
// on all others, so eviction can spare them.
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
		delete(ii.deferred, id)
	}
}

// markUnresolved forgets the given ids so a later sighting retries them — used
// after a transient fetch error.
func (ii *inlineImages) markUnresolved(fileIDs ...string) {
	ii.mu.Lock()
	defer ii.mu.Unlock()
	for _, id := range fileIDs {
		delete(ii.entries, id)
		delete(ii.deferred, id)
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
		// !resident: the terminal no longer holds this image, so re-transmitting a
		// frame would paint nothing. sight() re-transmits it if it is drawn again.
		if ent.state != inlineImgReady || len(ent.frameSeqs) <= 1 || !ent.visible || !ent.resident {
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

// reserveThumbCells predicts the cell box a thumbnail will occupy *before* its
// bytes have been fetched, so the post can hold that space from its very first
// render and the image appearing later never changes the post's height.
//
// That matters because thumbnails are now fetched lazily, only for posts near the
// viewport. A post that grew when its image arrived would reflow the transcript —
// and a wheel scroll anchors on an absolute row offset (m.msgFreeOffset), so
// content above the viewport growing would jump the page under the cursor. Reserved
// rows mean there is nothing to grow.
//
// Only the row count has to be right; cols is cosmetic (it sizes the blank holder,
// and a wrong guess is invisible against the background). An uploaded attachment
// carries its real dimensions in FileInfo, so its placement is predicted exactly.
// A body-image URL — a Giphy link — carries none, so it is assumed to be
// nominalBodyImage{W,H}: any image tall enough to hit the height cap lands on
// exactly inlineThumbRows whatever its aspect, and that is essentially all of them.
// Only a genuinely tiny image (shorter than the cap) reserves wrong, and it merely
// shrinks by a row or two when it loads.
func (m *Model) reserveThumbCells(it previewItem, box int) (cols, rows int) {
	w, h := nominalBodyImageW, nominalBodyImageH
	if it.file != nil && it.file.Width > 0 && it.file.Height > 0 {
		w, h = it.file.Width, it.file.Height
	}
	return inlineThumbCells(w, h, box, m.cellPxW, m.cellPxH)
}

// inlineThumbLines returns the rows an image contributes to its post: the Kitty
// placeholder block once the image is built, or that many blank rows while it is
// still coming (see reserveThumbCells — the post is its final height either way).
// nil only when there will never be an image here: thumbnails are off, the terminal
// can't draw them, the pane is too narrow, or the image failed to decode.
//
// Every row is indented by the standard two-cell gutter so it lines up with the
// message body and survives wrapBodyLine untouched (the placement is fitted to the
// pane width, so it never needs wrapping).
//
// The placeholder rows are emitted raw, never through a lipgloss style: the image
// id rides in each cell's truecolor foreground, and a style would overwrite it and
// collapse the image.
func (m *Model) inlineThumbLines(it previewItem, paneWidth int) []string {
	if !m.inlineImagesActive() {
		return nil
	}
	box := inlineThumbBox(paneWidth)
	if box == 0 {
		return nil
	}
	resCols, resRows := m.reserveThumbCells(it, box)
	r := m.inlineImg.sight(it, box, resRows, resCols)
	switch {
	case r.ready:
		rows := strings.Split(r.placeholder, "\n")
		out := make([]string, len(rows))
		for i, row := range rows {
			out[i] = "  " + row
		}
		return out
	case r.reserve:
		// Hold the space the thumbnail will fill. Plain spaces: the image lands on
		// exactly these cells, so anything drawn here would only flash and vanish.
		out := make([]string, r.rows)
		blank := "  " + strings.Repeat(" ", r.cols)
		for i := range out {
			out[i] = blank
		}
		return out
	}
	return nil
}

// inlineFileThumbLines draws an uploaded image attachment (used by
// renderAttachments, above the file's own filename line). Nothing while the post's
// thumbnails are collapsed — and nothing means *nothing*: the image is never
// sighted, so it is never fetched, never built and never animated (see sight).
func (m *Model) inlineFileThumbLines(p *model.Post, f *model.FileInfo, paneWidth int) []string {
	if f == nil || !previewableMIME(f.MimeType) || m.thumbsHidden(p) {
		return nil
	}
	return m.inlineThumbLines(previewItem{file: f, name: f.Name}, paneWidth)
}

// --- collapsing a post's thumbnails (z) -----------------------------------

// thumbsHidden reports whether the user has collapsed post p's thumbnails. The
// single question every draw, fetch, animate and evict path asks; a post with no
// id (an optimistic stub that hasn't landed) can't be collapsed, since there is no
// stable key to remember it by.
func (m *Model) thumbsHidden(p *model.Post) bool {
	return p != nil && p.Id != "" && m.thumbsCollapsed[p.Id]
}

// thumbChevron is the disclosure chevron for post id's image indicators, or "" when
// no thumbnail is drawn for this post anyway — thumbnails are off, or the terminal
// can't paint them — in which case the indicator keeps the plain look it has always
// had. The chevron is per *post*, not per image: z collapses everything a post
// draws, so every indicator in it points the same way.
func (m *Model) thumbChevron(postID string) string {
	if !m.inlineImagesActive() || postID == "" {
		return ""
	}
	if m.thumbsCollapsed[postID] {
		return thumbShutChevron
	}
	return thumbOpenChevron
}

// imgChevrons resolves the body-image markers renderInline planted (see
// imgIndicatorMark) into post p's collapse chevron — the transcript's view of a
// rendered body, and the only place one is drawn, since the transcript is the only
// place the thumbnail it describes exists.
//
// The Contains gate is what keeps this off the render path's back: a body with no
// image at all — very nearly all of them — leaves without touching the string.
func (m *Model) imgChevrons(body string, p *model.Post) string {
	if !strings.Contains(body, imgIndicatorMark) {
		return body
	}
	var chev string
	if m.hasBodyThumbnail(p) {
		chev = m.thumbChevron(p.Id)
	}
	if chev == "" {
		// Marked, but nothing will be drawn under it: thumbnails are off, or the
		// image isn't one we can decode. Leave the indicator bare rather than
		// promising a thumbnail that never comes.
		return stripImgMarks(body)
	}
	return strings.ReplaceAll(body, imgIndicatorMark, chev+" ")
}

// hasBodyThumbnail reports whether the transcript draws a thumbnail for something
// *in p's body* — as opposed to an uploaded attachment, whose chevron rides on its
// own filename line in renderAttachments.
func (m *Model) hasBodyThumbnail(p *model.Post) bool {
	for _, it := range previewImages(p) {
		if it.file == nil {
			return true
		}
	}
	return false
}

// postThumbKeys is every thumbnail post p draws — what z collapses, and what
// releaseThumbs frees when it does.
func (m *Model) postThumbKeys(p *model.Post) []string {
	items := previewImages(p)
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if key := thumbKey(it); key != "" {
			out = append(out, key)
		}
	}
	return out
}

// releaseThumbs hands back the terminal memory held by the thumbnails of a post the
// user just collapsed, rather than waiting for the LRU to notice they went cold.
// The built frames stay in our memory, so expanding again is a re-transmit of a
// string we already have — not a second decode.
//
// The frees are *queued*, not done here: whether an image may be freed depends on
// whether it is still on screen somewhere else (the same Giphy URL posted twice, one
// copy collapsed and one not), and that is only known after the render that follows
// this event. flushInlineTransmits recomputes on-screen and then drains the queue.
func (m *Model) releaseThumbs(p *model.Post) {
	if m.inlineImg == nil {
		return
	}
	m.inlineImg.queueRelease(m.postThumbKeys(p))
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
	if !m.inlineImagesActive() || p == nil || m.thumbsHidden(p) {
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

	// deferredFrames says this is an animatable GIF built as its first frame only,
	// so markReady knows to remember (via item) that the rest are still owed. See
	// buildInlineThumb.
	deferredFrames bool
	item           previewItem
}

// thumbFramesJob is one deferred GIF whose frames are now worth encoding, and
// everything the background build needs to slot them into the still that is already
// on screen: the same terminal id, and the same box it was fitted to.
type thumbFramesJob struct {
	key  string
	item previewItem
	id   uint32
	box  int
}

// builtThumbFrames is the outcome of one such build. rows/cols come back with it so
// markFramesBuilt can refuse anything that would land on a different cell box than
// the still it is completing.
type builtThumbFrames struct {
	key        string
	id         uint32
	rows, cols int
	seqs       []string
	delays     []time.Duration
}

// inlineThumbFramesMsg carries finished GIF frames back to the main goroutine.
type inlineThumbFramesMsg struct {
	built []builtThumbFrames
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

// fetchPendingInlineImages builds the sighted thumbnails that are near enough to
// the viewport to be worth having, and returns a Cmd that downloads, decodes and
// encodes them in the background. Run from Update after each event; returns nil
// cheaply when nothing is pending or the feature is inactive.
//
// The "near enough" is the whole point. renderMessages renders every post in the
// render window (up to maxLoadedPosts — 400), so *every* image in the window gets
// sighted, while the terminal only ever displays one screenful. Building all of
// them meant paying for ~20 thumbnails to show ~3, and — because the window can
// hold more images than fit in terminal memory — evicting and rebuilding them
// forever (TestThumbFetchConverges). Images beyond the margin stay pending and are
// picked up if you ever scroll toward them; because sight() reserved their rows,
// arriving late costs no reflow.
func (m *Model) fetchPendingInlineImages() tea.Cmd {
	if !m.inlineImagesActive() {
		return nil
	}
	box := inlineThumbBox(m.msgsView.Width())
	if box == 0 {
		return nil // pane too narrow to place any; they stay pending for a resize
	}
	// Nothing that decides which images are in reach has moved since the last scan,
	// so there is nothing new to fetch. Keeps a channel full of parked pending images
	// from re-walking the posts on every keystroke.
	if !m.inlineImg.needsFetchScan([5]uint64{
		uint64(m.msgsView.YOffset()), m.msgsContentVer,
		uint64(m.threadView.YOffset()), m.threadContentVer,
		uint64(len(m.posts)),
	}) {
		return nil
	}
	want := m.thumbKeysNearViewport(m.inlineImg.pendingKeys())
	items := m.inlineImg.takePending(want)
	if len(items) == 0 {
		return nil
	}
	snap := m // value copy: the Cmd runs on another goroutine
	return func() tea.Msg {
		return snap.loadInlineImages(items, box)
	}
}

// flushInlineTransmits writes out of band whatever the last render made necessary:
// re-transmits for ready images the terminal no longer holds (freed to stay under
// maxInlineImages, their frames kept — see evictResidentLocked), followed by the
// kittyDeletes for anything now over a cap. Run from Update after each event, so a
// re-transmit queued while rendering goes out in the same event that rendered it.
func (m *Model) flushInlineTransmits() tea.Cmd {
	if m.inlineImg == nil {
		return nil
	}
	m.inlineImg.setOnScreen(m.visibleInlineImageKeys())
	seq := m.inlineImg.takeTransmits()
	if seq == "" {
		return nil
	}
	return tea.Raw(seq)
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
// decodes it, right-sizes it to the thumbnail's cell box, and prebuilds the
// transmit sequences and placeholder. All of it off the render loop.
//
// A GIF is built as a *still* — its first frame and nothing else. Encoding a frame
// costs ~10ms, so a 90-frame GIF is ~700ms of PNG encoding here, and the fetch
// margin (inlineFetchMarginScreens) deliberately builds several screens' worth of
// images to display one screenful: nearly all of that animation work would be for
// GIFs nobody ever looks at, since a GIF only animates while it is on screen. So the
// frames past the first are left to buildVisibleThumbFrames, which encodes them if
// and when the thumbnail is actually displayed. decodeFirstGIFFrame is bit-identical
// to a full decode's frame 0, so the still lands on precisely the cell box the frames
// will need, and completing it later moves nothing.
func (m Model) buildInlineThumb(it previewItem, box int) (readyInlineImg, error) {
	raw, err := m.readThumbBytes(it)
	if err != nil {
		return readyInlineImg{}, err
	}
	// Animate any GIF, whether it arrived as an attachment or a body link — a
	// Giphy link is the latter, and a frozen Giphy would be a strange thing to ship.
	// The bytes are sniffed, so a mislabelled MIME can't fool it either way.
	if m.animateInline && isGIF(raw) {
		first, err := decodeFirstGIFFrame(raw)
		if err != nil {
			return readyInlineImg{}, decodeFailure{err}
		}
		r, err := m.encodeInlineThumb(0, []image.Image{first}, box)
		if err != nil {
			return readyInlineImg{}, err
		}
		r.deferredFrames, r.item = true, it
		return r, nil
	}
	frames, _, err := decodeImageFrames(raw, false)
	if err != nil {
		return readyInlineImg{}, decodeFailure{err}
	}
	if len(frames) == 0 {
		return readyInlineImg{}, decodeFailure{fmt.Errorf("no frames")}
	}
	return m.encodeInlineThumb(0, frames, box)
}

// encodeInlineThumb fits every frame to the thumbnail's cell box and prebuilds one
// Kitty transmit APC per frame, all under id — 0 allocates a fresh one, and the
// deferred-frames build passes the id of the still it is completing so the frames
// repaint the placeholder already on screen.
//
// This is the expensive half of a thumbnail (~10ms/frame, dominated by the PNG
// encode; see PERF_NOTES) and the reason a GIF's frames are not all encoded up
// front. The placement is sized from the decoded frame rather than
// FileInfo.Width/Height: a server preview rendition is already downscaled, so its
// real bounds are what the placement must match.
func (m Model) encodeInlineThumb(id uint32, frames []image.Image, box int) (readyInlineImg, error) {
	b := frames[0].Bounds()
	cols, rows := inlineThumbCells(b.Dx(), b.Dy(), box, m.cellPxW, m.cellPxH)
	if id == 0 {
		id = m.emojiImg.allocID() // one shared 24-bit id space with emoji + preview
	}
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
	}, nil
}

// buildVisibleThumbFrames encodes the animation frames of every GIF that was built
// as a still and has since come on screen — the deferred half of buildInlineThumb.
// Run from Update after each event, right after flushInlineTransmits has refreshed
// which thumbnails are displayed; returns nil cheaply (an empty map) when no GIF is
// waiting, which is the common case.
//
// The bytes are re-read from the disk cache the first build populated, so this costs
// no resident memory per scrolled-past GIF and no second download.
func (m *Model) buildVisibleThumbFrames() tea.Cmd {
	if !m.inlineImagesActive() {
		return nil
	}
	jobs := m.inlineImg.takeDeferredFrames()
	if len(jobs) == 0 {
		return nil
	}
	snap := m // the Cmd runs on another goroutine
	return func() tea.Msg {
		return snap.loadInlineThumbFrames(jobs)
	}
}

// loadInlineThumbFrames re-reads and fully decodes each job's GIF, encoding every
// frame under the id of the still already on screen. Runs on a background goroutine.
//
// A failure here is silent by design: the thumbnail keeps the still it already has,
// which is exactly what it looks like now, and the job is gone from ii.deferred so
// nothing retries it. There is little left to fail — the same bytes decoded once
// already, to build that still.
func (m Model) loadInlineThumbFrames(jobs []thumbFramesJob) tea.Msg {
	built := make([]builtThumbFrames, 0, len(jobs))
	for _, j := range jobs {
		raw, err := m.readThumbBytes(j.item)
		if err != nil {
			continue
		}
		frames, delays, err := decodeImageFrames(raw, true)
		if err != nil || len(frames) <= 1 {
			continue // a single-frame GIF: the still we built is the whole image
		}
		r, err := m.encodeInlineThumb(j.id, frames, j.box)
		if err != nil {
			continue
		}
		built = append(built, builtThumbFrames{
			key: j.key, id: j.id, rows: r.rows, cols: r.cols,
			seqs: r.frameSeqs, delays: delays,
		})
	}
	if len(built) == 0 {
		return nil
	}
	return inlineThumbFramesMsg{built: built}
}

// handleInlineThumbFrames installs finished GIF frames. Nothing on screen changes —
// the frames carry the still's own id and cell box, so the placeholder cells and the
// post's cached lines are all still correct — which is why this neither invalidates
// nor re-renders anything. The Update wrapper's maybeStartImageAnim sees the entry
// become animated on this same event and starts the loop.
func (m Model) handleInlineThumbFrames(msg inlineThumbFramesMsg) (Model, tea.Cmd) {
	for _, b := range msg.built {
		m.inlineImg.markFramesBuilt(b)
	}
	return m, nil
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
// is recorded and transmitted raw (out of band) so its placeholder resolves, and
// the posts that own it have their cached lines dropped so the next render swaps
// the reserved blank rows for the placeholder — same height, so nothing reflows. If
// any installed thumbnail is animated and on screen, the animation tick is armed.
//
// It does not evict. Staying under the caps is flushInlineTransmits' job, which the
// Update wrapper runs right after this, once per event rather than once per
// installed image.
func (m Model) handleInlineImagesFetched(msg inlineImagesFetchedMsg) (Model, tea.Cmd) {
	if m.inlineImg == nil {
		return m, nil
	}
	var transmit strings.Builder
	readyKeys := make(map[string]struct{}, len(msg.ready))
	animated := false
	for key, r := range msg.ready {
		if len(r.frameSeqs) == 0 {
			m.inlineImg.markFailed(key)
			continue
		}
		for _, id := range m.inlineImg.markReady(key, r) {
			transmit.WriteString(kittyDelete(id)) // an image this one re-fitted over
		}
		transmit.WriteString(r.frameSeqs[0])
		readyKeys[key] = struct{}{}
		if len(r.frameSeqs) > 1 {
			animated = true
		}
	}
	m.inlineImg.markFailed(msg.failed...)
	m.inlineImg.markUnresolved(msg.retry...)

	// Every outcome changes what the owning post should draw, so every outcome has
	// to drop its cached lines — not just the happy one. A ready image swaps its
	// reserved blank rows for the placeholder. A *failed* one must give the rows
	// back, or the reservation it was granted at first sight stays behind as a
	// permanent blank hole in the transcript. A *retry* was forgotten by
	// markUnresolved, so its post has to be re-rendered to sight it afresh —
	// otherwise the cached lines mean it is never asked for again and never arrives.
	touched := make(map[string]struct{}, len(readyKeys)+len(msg.failed)+len(msg.retry))
	for key := range readyKeys {
		touched[key] = struct{}{}
	}
	for _, key := range msg.failed {
		touched[key] = struct{}{}
	}
	for _, key := range msg.retry {
		touched[key] = struct{}{}
	}
	if len(touched) > 0 {
		m.invalidatePostsForThumbs(touched)
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
// still — the set eviction must spare.
func (m *Model) visibleInlineImageKeys() map[string]struct{} {
	if m.inlineImg == nil {
		return nil
	}
	return m.visibleThumbKeys(m.inlineImg.readyIDs())
}

// thumbKeysNearViewport returns which of keys belong to a post within
// inlineFetchMarginScreens viewport-heights of the visible rows — the images worth
// building. The margin is what makes lazy fetching invisible: an image is built
// while it is still a couple of screens away, so it is ready by the time you reach
// it rather than popping in under you.
func (m *Model) thumbKeysNearViewport(keys []string) map[string]struct{} {
	return m.thumbKeysInRows(keys, inlineFetchMarginScreens)
}

// visibleThumbKeys narrows keys to those owned by a post currently inside a
// viewport's visible rows.
func (m *Model) visibleThumbKeys(keys []string) map[string]struct{} {
	return m.thumbKeysInRows(keys, 0)
}

// thumbKeysInRows narrows keys to those owned by a post inside a viewport's visible
// rows, widened by margin viewport-heights at each end — the main message pane plus
// the open thread. It maps the live YOffset back to posts through the row spans
// captured by renderMessages/renderThread, so it tracks scrolling without a
// re-render.
//
// A post whose thumbnails the user collapsed owns none of them, however visible the
// post itself is. This one line is what makes collapsing *free* rather than merely
// invisible: the three questions asked through here are "which images are worth
// fetching", "which are on screen (so must not be evicted)" and "which animate", and
// a collapsed post now answers none of them. Its GIF stops ticking, stops holding a
// slot in terminal memory, and — if it was never built — is never built at all.
func (m *Model) thumbKeysInRows(keys []string, margin int) map[string]struct{} {
	if len(keys) == 0 {
		return nil
	}
	visible := make(map[string]struct{}, len(keys))
	scan := func(posts []*model.Post, starts []int, top, height int) {
		if height <= 0 || len(starts) != len(posts)+1 {
			return
		}
		bot := top + height + margin*height
		top -= margin * height
		for i, p := range posts {
			if starts[i] >= bot {
				break // this post and all later ones start below the viewport
			}
			if starts[i+1] <= top {
				continue // entirely scrolled above the viewport
			}
			if m.thumbsHidden(p) {
				continue // collapsed: it draws no thumbnail, so it owns none
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
