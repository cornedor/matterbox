package ui

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/stl"
)

// Inline 3D preview for .stl attachments — the format 3D printing runs on, and
// the one thing people post in a hardware channel that every chat client shows
// as a paperclip and a filename.
//
// Two halves, sharing one renderer (internal/stl):
//
//   - A still three-quarter view drawn *in the transcript*, riding the existing
//     inline-thumbnail machinery (inlineimg.go): same `image_thumbnails` setting,
//     same Kitty image ids, same lazy fetch, same z to collapse. An STL is simply
//     a file whose "decode" is a render — see buildInlineThumb.
//
//   - An interactive viewer (this file): space on the message, or a click on the
//     thumbnail, opens a modal you can orbit with the mouse or the keyboard.
//
// The viewer follows the pattern the games established (kurve.go): the model is a
// grid of Kitty placeholder cells and the pixels arrive out of band under a fixed
// image id, so a frame repaints what is already on screen without the TUI
// re-rendering at all. Spinning a 200k-triangle mesh therefore costs the View()
// hot path exactly nothing, which is the only reason a software rasterizer in a
// terminal is a reasonable idea.
//
// It is deliberately *not* a mode inside the image-preview modal. That modal is
// already three state machines in a trench coat (stills, native-animation GIFs,
// streaming video) and all of them are about frames that were decoded elsewhere;
// this is one frame that we generate, on demand, from a camera. Sharing the box
// would mean interleaving those.

// stlMaxBytes caps an STL we will fetch and parse. 96MB is a ~2M-facet binary
// file — past MaxTriangles, so anything this admits and the parser then refuses
// gets refused for a reason we can explain, and anything bigger is not something
// to pull down a chat connection to look at in a terminal.
const stlMaxBytes = 96 << 20

// stlThumbMaxBytes is the tighter cap on the *inline* render. A thumbnail is
// drawn unasked, for every STL near the viewport, so the bar for spending a
// download on one is much higher than for a file the user pressed space on.
// 24MB still covers essentially every part anyone posts; above it the file keeps
// its icon and the viewer is a keypress away.
const stlThumbMaxBytes = 24 << 20

// stlSettleDelay is how long after the last camera change the crisp
// (supersampled) frame is rendered. Long enough that a continuous drag never
// pays for one, short enough to feel immediate when you let go.
const stlSettleDelay = 90 * time.Millisecond

// stlSpinDelay is one frame of the auto-spin turntable.
const stlSpinDelay = 50 * time.Millisecond

// stlSpinStep is how far the turntable turns per frame — a full revolution in
// about eight seconds, which is slow enough to actually read the shape.
const stlSpinStep = float32(2 * math.Pi / 160)

// Orbit sensitivities. The keyboard step is a visible nudge rather than a nudge
// you have to hold; the mouse figures are radians per cell dragged, tuned so a
// drag across half the box is about a quarter turn.
const (
	stlKeyOrbit  = float32(0.12)
	stlKeyPan    = float32(0.04)
	stlKeyZoom   = float32(1.25)
	stlDragOrbit = float32(0.020)
	stlDragPan   = float32(0.010)
	stlWheelZoom = float32(1.12)
)

// stlState is the open 3D viewer. Zero value = closed.
type stlState struct {
	active bool
	// gen invalidates in-flight loads, renders and ticks the user has since
	// cycled or closed past — the same guard every async modal here uses.
	gen int

	// items is every STL attachment on the post; idx is the one on screen.
	// n/p cycle them, matching the image modal's ←/→ gallery.
	items []previewItem
	idx   int

	name string
	size int64

	mesh *stl.Mesh
	cam  stl.Camera
	// rend keeps its pixel and depth buffers across frames; a drag redraws ~30
	// times a second and they are megabytes. Only ever touched by the render
	// Cmd, one at a time (see rendering).
	//
	// A pointer, not a value, and that is load-bearing rather than a size
	// micro-optimisation: ui.Model is copied on every event, so a Renderer
	// stored inline would have the buffers the render goroutine allocates
	// written into a copy that has already been superseded — every frame would
	// reallocate and the reuse would silently buy nothing. Behind a pointer the
	// buffers are shared by every copy, which is what "keeps them across
	// frames" actually requires.
	rend *stl.Renderer

	imgID      uint32
	rows, cols int

	// --- frame double buffering ---
	//
	// Re-transmitting an image id replaces the image it names, and a terminal is
	// entitled to drop the old pixels the moment the new transmission *starts*
	// rather than when it finishes — Ghostty does exactly that (the streaming
	// video player hit the same wall; see preview_stream.go's advanceStream). A
	// drag frame is ~100KB of base64 in 4KB chunks, so that is most of a frame
	// interval during which the placeholder cells point at an image that no
	// longer exists, and the model strobes.
	//
	// The video player works around it by rotating image ids, which costs a
	// View() re-render per frame to repaint the cells with the new id, and needs
	// four slots of slack because the cell switch and the upload reach the
	// terminal in separate flushes. This viewer does the double buffering inside
	// the terminal instead: the image carries two spare animation frames, each
	// rendered frame is uploaded into whichever spare is *not* on screen (a=f,
	// r=) — invisible, however long it takes — and one tiny a=a,c= then switches
	// the placement onto it. The id and the cells never change, so nothing
	// re-renders and there is no window in which the id names no pixels.
	//
	// Gated on the terminal actually implementing frame edits: the arming a=f
	// goes out with q=0 and swap is set only if the reply says OK (see
	// applySTLFrameReply). Until it does — and forever on a terminal that stays
	// silent — frames go out as plain re-transmits, exactly as before.
	swap      bool
	swapAsked bool
	// curFrame is the animation frame the placement is showing: 1 is the root
	// (what a plain transmit establishes), 2 and 3 are the spares swap mode
	// alternates between.
	curFrame int
	// frameW/frameH is the pixel box the spares were created at. A frame is
	// composed into a canvas of the image's own size, so a resize invalidates
	// them and costs one root re-transmit to rebuild.
	frameW, frameH int

	loading bool
	err     error

	// rendering / pending coalesce frames: at most one render is in flight and a
	// camera change during it queues exactly one more, so a fast drag drops the
	// intermediate frames instead of queueing a backlog behind them.
	rendering bool
	pending   bool

	// moving is set while the camera is being changed and cleared by the settle
	// tick. Frames rendered while it is set skip supersampling — a 200k-facet
	// mesh is a third of the frame budget with it and a fifth without — and the
	// settle re-render brings the crisp frame back once the user stops. settleSeq
	// drops all but the newest pending settle, exactly as resizeGen does for the
	// resize storm.
	moving    bool
	settleSeq int

	// spin is the auto-rotate turntable (space). Off by default: it is a timer
	// that renders, and nothing should be burning frames unasked.
	spin bool

	// drag is a mouse orbit (or, with shift, a pan) in progress; lastX/lastY is
	// the cell the pointer was last seen at.
	drag         bool
	panning      bool
	lastX, lastY int
}

type (
	// stlLoadedMsg carries a parsed mesh back from the background load.
	stlLoadedMsg struct {
		gen  int
		mesh *stl.Mesh
		err  error
	}
	// stlFrameMsg carries an encoded Kitty frame back from the render Cmd.
	// frame/w/h describe what the sequence leaves on screen — which animation
	// frame the placement ends up on, and the pixel box it was drawn for — and
	// are applied only once the frame has actually been built (see
	// applySTLFrame), so a failed render doesn't move the state.
	stlFrameMsg struct {
		gen   int
		seq   string
		frame int
		w, h  int
		err   error
	}
	// stlSettleMsg fires once the camera has been still for stlSettleDelay; seq
	// identifies which settle it is, so an older one is ignored.
	stlSettleMsg struct{ gen, seq int }
	// stlSpinMsg advances the turntable one frame.
	stlSpinMsg struct{ gen int }
)

// --- recognising an STL ---------------------------------------------------

// isSTLAttachment reports whether an uploaded file is one we can render. By
// extension first: Mattermost has no registered MIME type for STL, so uploads
// arrive as application/octet-stream, as model/stl, as application/sla, or with
// mime_type empty altogether — the filename is the only field that is reliably
// right. The bytes are sniffed again before parsing (stl.Looks), so a mislabelled
// file fails cleanly rather than drawing nonsense.
func isSTLAttachment(f *model.FileInfo) bool {
	if f == nil {
		return false
	}
	ext := strings.ToLower(strings.TrimPrefix(f.Extension, "."))
	if ext == "" {
		if i := strings.LastIndex(f.Name, "."); i >= 0 {
			ext = strings.ToLower(f.Name[i+1:])
		}
	}
	if ext == "stl" {
		return true
	}
	// A correct MIME type is a bonus, not the test.
	mime, _, _ := strings.Cut(strings.ToLower(f.MimeType), ";")
	switch strings.TrimSpace(mime) {
	case "model/stl", "model/x.stl-binary", "model/x.stl-ascii", "application/sla", "application/vnd.ms-pki.stl":
		return true
	}
	return false
}

// stlRenderable reports whether this session can show an STL at all: the
// terminal has to be able to draw a picture. Everything else about the file is
// the load path's problem.
func (m *Model) stlRenderable() bool {
	return m.emojiImg != nil && m.emojiImg.graphicsReady()
}

// stlThumbnailable reports whether an attachment gets a *still* render in the
// transcript: an STL small enough to be worth downloading unasked, with
// thumbnails on and a terminal that can draw one.
func (m *Model) stlThumbnailable(f *model.FileInfo) bool {
	return isSTLAttachment(f) && f.Size <= stlThumbMaxBytes && m.inlineImagesActive()
}

// stlItems enumerates the STL attachments on a post, in metadata order — the
// viewer's gallery, and (with the images) what the transcript draws thumbnails
// for. Kept separate from previewImages rather than folded into it because the
// two feed different modals: an STL in the image preview's gallery would be an
// item it cannot decode.
func stlItems(p *model.Post) []previewItem {
	if p == nil || p.Metadata == nil {
		return nil
	}
	var out []previewItem
	for _, f := range p.Metadata.Files {
		if isSTLAttachment(f) {
			out = append(out, previewItem{file: f, name: f.Name})
		}
	}
	return out
}

// thumbItems is everything a post draws an inline thumbnail for: the images and
// short-clip videos previewImages finds, plus the STL attachments this session
// will render. The single enumeration the collapse chevron, the release-on-
// collapse bookkeeping and the thumbnail click lookup all share, so none of them
// can disagree about which files own a thumbnail.
func (m *Model) thumbItems(p *model.Post) []previewItem {
	items := previewImages(p, m.videoPlayable())
	for _, it := range stlItems(p) {
		if m.stlThumbnailable(it.file) {
			items = append(items, it)
		}
	}
	return items
}

// --- opening and closing --------------------------------------------------

// openSTLView raises the 3D viewer on the post's STL attachments, starting at
// index start. No STL, or no terminal graphics, and it says so instead.
func (m Model) openSTLView(items []previewItem, start int) (tea.Model, tea.Cmd) {
	if len(items) == 0 {
		m.status = "no 3D model on this message"
		return m, nil
	}
	if !m.stlRenderable() {
		m.status = "3D preview unavailable: " + m.emojiImg.statusReason() + " — press o to open"
		return m, nil
	}
	if start < 0 || start >= len(items) {
		start = 0
	}
	m.stl = stlState{
		active: true,
		gen:    m.stl.gen + 1,
		items:  items,
		imgID:  m.emojiImg.allocID(),
		cam:    stl.DefaultCamera(),
		rend:   &stl.Renderer{},
	}
	return m, (&m).loadSTLItem(start)
}

// loadSTLItem points the viewer at items[idx] and starts the background load.
// Also the cycle path (n/p), so the mesh, error and camera are all reset here in
// one place.
func (m *Model) loadSTLItem(idx int) tea.Cmd {
	g := &m.stl
	g.idx = idx
	it := g.items[idx]
	g.mesh, g.err = nil, nil
	g.loading = true
	// A fresh model gets a fresh camera: carrying a zoomed-in, panned-off view
	// onto a differently-sized part would show the next one edge-on or empty.
	g.cam = stl.DefaultCamera()
	g.name = it.name
	if it.file != nil {
		g.name = normalizeFilename(it.file.Name)
		g.size = it.file.Size
	}
	m.sizeSTLView()

	gen := m.stl.gen
	mm := *m
	return func() tea.Msg {
		mesh, err := mm.loadSTLMesh(it)
		return stlLoadedMsg{gen: gen, mesh: mesh, err: err}
	}
}

// loadSTLMesh fetches (through the same on-disk cache the image thumbnails and
// the preview modal use, so a model you already have a thumbnail for costs no
// second download) and parses one STL. Runs on a background Cmd: a 10MB binary
// STL is ~6ms of parsing and a big ASCII one considerably more, neither of which
// belongs on the render loop.
func (m Model) loadSTLMesh(it previewItem) (*stl.Mesh, error) {
	raw, err := m.readSTLBytes(it, stlMaxBytes)
	if err != nil {
		return nil, err
	}
	return parseSTL(raw)
}

// readSTLBytes fetches an STL's bytes, through the same on-disk cache the image
// thumbnails and the preview modal use — so a model you already have a
// thumbnail for costs no second download. The server's preview rendition is
// never consulted (unlike readThumbBytes): there isn't one for an STL, and the
// original is the only thing we can render from.
//
// Errors here are transient by construction (a failed download, a bad path),
// which is what the thumbnail path needs: only a parse failure is permanent and
// worth remembering as such.
func (m Model) readSTLBytes(it previewItem, maxBytes int64) ([]byte, error) {
	if it.file == nil {
		return nil, errors.New("not an uploaded file")
	}
	if it.file.Size > maxBytes {
		return nil, fmt.Errorf("file is %s — too big to render", humanSize(it.file.Size))
	}
	path, err := m.cachedFilePath(it.file)
	if err != nil {
		return nil, err
	}
	return m.readOrDownloadFile(path, it.file)
}

// parseSTL sniffs and parses. Split from the fetch so a caller can tell a
// download failure (worth retrying) from a file that will never be a mesh.
func parseSTL(raw []byte) (*stl.Mesh, error) {
	if !stl.Looks(raw) {
		return nil, errors.New("not an STL file")
	}
	return stl.Decode(raw)
}

// --- the inline thumbnail -------------------------------------------------

// stlThumbCells is the cell box an inline STL render occupies: the full
// thumbnail height, and the width a 4:3 picture needs at it.
//
// Chosen up front rather than measured from the rendered image, which is what
// every other thumbnail does — and the difference matters. A photo has a natural
// size, so its placement is derived from its pixels and the reservation made
// before the download is a *prediction* that can be wrong. A model has no
// natural size at all: we choose the camera, so we choose the aspect. Deciding
// it here means the rows reserved before the fetch and the rows the render
// finally occupies are the same by construction — no reflow when it lands, and
// none of the re-fit cycle sight() has to guard against, since the height is
// always exactly inlineThumbRows and so can never "grow into" a wider pane.
func stlThumbCells(box, cellPxW, cellPxH int) (cols, rows int) {
	rows = inlineThumbRows
	cw, ch := cellPxW, cellPxH
	if cw <= 0 || ch <= 0 {
		cw, ch = 8, 16 // the usual ~1:2 cell, when the terminal didn't say
	}
	const aspect = 4.0 / 3.0
	cols = int(float64(rows) * aspect * float64(ch) / float64(cw))
	cols = max(min(cols, box), 1)
	return cols, rows
}

// thumbPixelBox is the pixel size to render for a cols×rows placement, so the
// image maps 1:1 onto the cells and the terminal never rescales it. Mirrors
// padImageForCells' device-pixel-ratio arithmetic: on a HiDPI terminal the
// reported cell is physical pixels, and rendering at the full figure would
// produce an image at twice its natural logical size.
func thumbPixelBox(cols, rows, cellPxW, cellPxH int) (w, h int) {
	cw, ch := cellPxW, cellPxH
	if cw <= 0 || ch <= 0 {
		cw, ch = 8, 16
	}
	dpr := math.Max(1, math.Floor(float64(cw)/7))
	return max(int(float64(cols*cw)/dpr), 1), max(int(float64(rows*ch)/dpr), 1)
}

// buildSTLThumb renders one still three-quarter view for the transcript and
// encodes it as a one-frame thumbnail. Runs on the same background Cmd every
// other thumbnail build does (loadInlineImages), which is what keeps a
// multi-megabyte parse and a rasterizer pass off the render loop.
//
// The renderer is local and thrown away: a thumbnail is one frame, so there is
// nothing to keep buffers for — unlike the viewer, which orbits.
func (m Model) buildSTLThumb(it previewItem, box int) (readyInlineImg, error) {
	raw, err := m.readSTLBytes(it, stlThumbMaxBytes)
	if err != nil {
		return readyInlineImg{}, err
	}
	mesh, err := parseSTL(raw)
	if err != nil {
		// Permanent: these bytes will never be a mesh, so don't fetch them again.
		return readyInlineImg{}, decodeFailure{err}
	}
	cols, rows := stlThumbCells(box, m.cellPxW, m.cellPxH)
	pxW, pxH := thumbPixelBox(cols, rows, m.cellPxW, m.cellPxH)

	var r stl.Renderer
	// Supersampled: a thumbnail is a couple of dozen cells across, so its
	// silhouette *is* the picture, and it is rendered once rather than 30 times a
	// second.
	img := r.Render(mesh, stl.DefaultCamera(), stlStyle(2), pxW, pxH)

	id := m.emojiImg.allocID() // one shared id space with emoji, preview and the viewer
	seq, err := kittyTransmitImage(id, img, rows, cols)
	if err != nil {
		return readyInlineImg{}, decodeFailure{err}
	}
	return readyInlineImg{
		id:          id,
		rows:        rows,
		cols:        cols,
		box:         box,
		placeholder: kittyPlaceholder(id, rows, cols),
		frameSeqs:   []string{seq},
	}, nil
}

// closeSTLView shuts the viewer and hands the image id back to the terminal.
// Bumping gen strands every in-flight load, frame and tick.
func (m *Model) closeSTLView() tea.Cmd {
	g := &m.stl
	if !g.active {
		return nil
	}
	id, gen := g.imgID, g.gen
	m.stl = stlState{gen: gen + 1}
	if id != 0 {
		return tea.Raw(kittyDelete(id))
	}
	return nil
}

// cycleSTL moves to the next/previous model on the post.
func (m *Model) cycleSTL(delta int) tea.Cmd {
	g := &m.stl
	if len(g.items) < 2 {
		return nil
	}
	g.gen++
	next := (g.idx + delta + len(g.items)) % len(g.items)
	// The old mesh's pixels are still on screen under imgID; the load replaces
	// them when it lands, so there is nothing to delete and no flicker.
	return m.loadSTLItem(next)
}

// --- sizing and rendering -------------------------------------------------

// sizeSTLView fits the viewport to the terminal, in cells. The target is a 4:3
// *pixel* box — the shape a single object photographs well in — which measured
// in cells is nothing like 4:3, since a terminal cell is about twice as tall as
// it is wide.
func (m *Model) sizeSTLView() {
	g := &m.stl
	// Border (2), padding (2), and a column of margin each side; vertically the
	// border, the caption, a blank, the hint, and the tab strip.
	maxCols := max(m.width-8, 20)
	maxRows := max(m.height-9, 8)

	cw, ch := m.cellPxOr(8), m.cellPxHOr(16)
	const aspect = 4.0 / 3.0
	cols := maxCols
	rows := int(float64(cols) * float64(cw) / (aspect * float64(ch)))
	if rows > maxRows {
		rows = maxRows
		cols = int(float64(rows) * aspect * float64(ch) / float64(cw))
	}
	g.cols = max(min(cols, maxCols), 16)
	g.rows = max(min(rows, maxRows), 6)
}

// stlMaterial is the colour the model is made of. Transparent background (see
// stlStyle) means the only thing it has to contrast with is the terminal's own,
// so it comes in both flavours: a light background needs a deeper material or
// the lit faces wash out to white, a dark one needs a brighter material or the
// shaded faces disappear.
var stlMaterial = adaptiveColor{
	light: lipgloss.Color("#5f7396"),
	dark:  lipgloss.Color("#93a7cc"),
}

// stlStyle is the render style for the current terminal background. The
// background is left fully transparent so the terminal's own shows through and
// the model sits in the transcript (and in the modal) without a rectangle of
// almost-the-right-colour around it.
func stlStyle(ssaa int) stl.Style {
	r, g, b, _ := stlMaterial.RGBA()
	return stl.Style{
		Base: color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 0xff},
		SSAA: ssaa,
	}
}

// stlFrameCmd renders one frame and encodes it for the terminal, off the main
// goroutine. Nothing in the TUI re-renders when it lands (see applySTLFrame), so
// the cost of a frame is entirely in here.
//
// At most one is in flight: a camera change during a render sets pending and the
// frame that finishes launches the next, which is what keeps a fast drag from
// queueing a hundred stale frames behind the one on screen.
func (m *Model) stlFrameCmd() tea.Cmd {
	g := &m.stl
	if !g.active || g.mesh == nil || g.rend == nil || g.rows == 0 || g.cols == 0 {
		return nil
	}
	if g.rendering {
		g.pending = true
		return nil
	}
	g.rendering = true

	gen, id := g.gen, g.imgID
	rows, cols := g.rows, g.cols
	pxW, pxH := cols*m.cellPxOr(8), rows*m.cellPxHOr(16)
	// SSAA 1 while the camera is moving, auto (which supersamples when the frame
	// is small enough to afford it) once it settles.
	ssaa := 0
	if g.moving {
		ssaa = 1
	}
	st := stlStyle(ssaa)
	// The Renderer and the mesh are captured by pointer: the Model is copied per
	// event, and the buffers must be the same ones next frame or reusing them
	// buys nothing.
	rend, mesh, cam := g.rend, g.mesh, g.cam

	// Which spare to upload into — the one the placement isn't showing. Zero
	// means "re-transmit the image instead": either the terminal hasn't confirmed
	// it understands frame edits, or a resize has left the spares at the wrong
	// pixel size and they have to be rebuilt.
	target := 0
	if g.swap && pxW == g.frameW && pxH == g.frameH {
		target = 2
		if g.curFrame == 2 {
			target = 3
		}
	}
	// One q=0 per viewer: the reply to that arming a=f is the only way to learn
	// whether the terminal implements frame edits at all, since one that doesn't
	// ignores the APC silently. Every later transmit stays quiet.
	quiet := 2
	if target == 0 && !g.swapAsked {
		quiet = 0
		g.swapAsked = true
	}

	return func() tea.Msg {
		img := rend.Render(mesh, cam, st, pxW, pxH)
		seq, err := stlFrameSeq(id, img, rows, cols, target, quiet)
		return stlFrameMsg{gen: gen, seq: seq, frame: max(target, 1), w: pxW, h: pxH, err: err}
	}
}

// stlFrameSeq encodes one rendered frame for the terminal.
//
// target 0 re-establishes everything: the image, its virtual placement, and the
// two spare animation frames — which have to be recreated every time, since a
// re-transmit drops whatever frames the id previously carried. target 2 or 3
// uploads into that spare and then switches the placement onto it, which is the
// steady state and the whole point (see stlState's frame-double-buffering note).
//
// The two take different routes through the encoder on purpose: the steady state
// goes out as zlib'd raw pixels, which for a flat-shaded render is both quicker
// and smaller than PNG (see kittyraw.go), while the transmit that establishes
// the image stays on the PNG path the rest of the app already exercises.
func stlFrameSeq(id uint32, img *image.RGBA, rows, cols, target, quiet int) (string, error) {
	if target == 0 {
		root, err := kittyTransmitImage(id, img, rows, cols)
		if err != nil {
			return "", err
		}
		return root + kittyCreateBlankFrame(id, quiet) + kittyCreateBlankFrame(id, 2), nil
	}
	edit, err := kittyEditFrameRaw(id, target, img)
	if err != nil {
		return "", err
	}
	return edit + kittyShowFrame(id, target), nil
}

// applySTLFrame writes the encoded frame out of band. Nothing re-renders:
// re-transmitting under the same image id repaints the placeholder cells already
// on screen, so orbiting costs the View() hot path nothing.
func (m *Model) applySTLFrame(msg stlFrameMsg) tea.Cmd {
	g := &m.stl
	if !g.active || msg.gen != g.gen {
		return nil
	}
	g.rendering = false
	if msg.err != nil {
		g.err = msg.err
		return nil
	}
	g.curFrame, g.frameW, g.frameH = msg.frame, msg.w, msg.h
	var next tea.Cmd
	if g.pending {
		g.pending = false
		next = m.stlFrameCmd()
	}
	return tea.Batch(tea.Raw(msg.seq), next)
}

// applySTLFrameReply arms — or disarms — frame-swap mode from whatever the
// terminal has to say about a frame edit (see stlState).
//
// An OK is the answer to the arming a=f: the terminal composed the spare frame,
// so the next render can be a swap. Anything else is a refusal, and it works in
// both directions: the arming edit failing keeps the viewer on plain
// re-transmits, and a later edit failing — the spares evicted from the
// terminal's image storage, say — puts it back on them, permanently for this
// viewer, since the question is only ever asked once (see stlFrameCmd's quiet).
// Closing and reopening asks again. The third case — a terminal that ignores the
// APC without a word — never reaches here at all, which is why silence has to
// mean no.
func (m *Model) applySTLFrameReply(payload string) {
	g := &m.stl
	if !g.active {
		return
	}
	g.swap = strings.HasPrefix(payload, "OK")
}

// applySTLLoaded installs a parsed mesh and draws the first frame.
func (m *Model) applySTLLoaded(msg stlLoadedMsg) tea.Cmd {
	g := &m.stl
	if !g.active || msg.gen != g.gen {
		return nil
	}
	g.loading = false
	if msg.err != nil {
		g.err = msg.err
		return nil
	}
	g.mesh = msg.mesh
	return m.stlFrameCmd()
}

// nudgeSTLCamera applies a camera change: clamp it, mark the camera as moving so
// the frame skips supersampling, draw, and arm the settle that brings the crisp
// frame back. Every keyboard and mouse path funnels through here, so none of
// them has to remember the clamp or the settle.
func (m *Model) nudgeSTLCamera(cam stl.Camera) tea.Cmd {
	g := &m.stl
	g.cam = cam.Clamp()
	g.moving = true
	g.settleSeq++
	gen, seq := g.gen, g.settleSeq
	return tea.Batch(m.stlFrameCmd(), tea.Tick(stlSettleDelay, func(time.Time) tea.Msg {
		return stlSettleMsg{gen: gen, seq: seq}
	}))
}

// applySTLSettle re-renders at full quality once the camera has been still.
// Ignores every settle but the newest, so a drag arms one per motion event and
// pays for exactly one crisp frame at the end of it.
func (m *Model) applySTLSettle(msg stlSettleMsg) tea.Cmd {
	g := &m.stl
	if !g.active || msg.gen != g.gen || msg.seq != g.settleSeq || !g.moving {
		return nil
	}
	// A spinning turntable never settles; it is always moving, and a crisp frame
	// it immediately replaces would be pure waste.
	if g.spin {
		return nil
	}
	g.moving = false
	return m.stlFrameCmd()
}

// applySTLSpin turns the turntable one step and re-arms itself.
func (m *Model) applySTLSpin(msg stlSpinMsg) tea.Cmd {
	g := &m.stl
	if !g.active || msg.gen != g.gen || !g.spin {
		return nil
	}
	g.cam.Yaw += stlSpinStep
	g.cam = g.cam.Clamp()
	return tea.Batch(m.stlFrameCmd(), stlSpinCmd(g.gen))
}

func stlSpinCmd(gen int) tea.Cmd {
	return tea.Tick(stlSpinDelay, func(time.Time) tea.Msg { return stlSpinMsg{gen: gen} })
}

// resizeSTLView re-fits the viewport and redraws at the new size. Called from
// the resize *settle*, not from every frame of a drag, so dragging the window
// edge doesn't re-render the mesh dozens of times.
func (m *Model) resizeSTLView() tea.Cmd {
	if !m.stl.active {
		return nil
	}
	m.sizeSTLView()
	return m.stlFrameCmd()
}

// --- keyboard -------------------------------------------------------------

// handleSTLKey owns every keystroke while the viewer is up. The controls are
// drawn on the viewer itself rather than listed in the cheatsheet, the same
// choice the games make: they are the modal's whole content, not a binding you
// look up.
//
// Orbit is on the arrows and hjkl, pan on their shifted forms, zoom on +/-, and
// the axis keys snap to the six orthographic views a CAD tool puts on its
// toolbar. Nothing here is rebindable, deliberately — a 3D viewport's controls
// are a convention older than this program.
func (m Model) handleSTLKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	cam := m.stl.cam
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		return m, (&m).closeSTLView()

	// Orbit. Same direction as a mouse drag, deliberately: the viewer's hint
	// presents "drag/↑↓←→" as one action, and two inputs under one label that
	// turn the model opposite ways is a bug however each is defended
	// individually. So an arrow means "as if you had dragged that way" — the
	// model follows the key, and ↓ tips its top toward you so you look down on
	// it. (A CAD package would more often orbit the *camera* here, which is the
	// same motion with every sign flipped; the mouse is the primary interaction
	// in a modal this size, so it sets the convention.) See
	// TestYawTurnsModelLeft in internal/stl for what the signs do on screen.
	case "left", "h":
		cam.Yaw += stlKeyOrbit
	case "right", "l":
		cam.Yaw -= stlKeyOrbit
	case "up", "k":
		cam.Pitch -= stlKeyOrbit
	case "down", "j":
		cam.Pitch += stlKeyOrbit

	// Pan. Shifted arrows, and the shifted vim keys for the same reason the
	// unshifted ones orbit.
	case "shift+left", "H":
		cam.PanX -= stlKeyPan
	case "shift+right", "L":
		cam.PanX += stlKeyPan
	case "shift+up", "K":
		cam.PanY += stlKeyPan
	case "shift+down", "J":
		cam.PanY -= stlKeyPan

	// Zoom. "+" needs its shifted and unshifted spellings both, since which one
	// the terminal reports depends on the layout.
	case "+", "=", "pgup":
		cam.Zoom *= stlKeyZoom
	case "-", "_", "pgdown":
		cam.Zoom /= stlKeyZoom

	// Standard views. Pressing the same key again flips to the opposite side,
	// which is how you get at the back of a part without orbiting all the way
	// round it.
	case "x":
		cam.Yaw, cam.Pitch = flipView(cam, math.Pi/2, 0)
	case "y":
		cam.Yaw, cam.Pitch = flipView(cam, 0, 0)
	case "z":
		cam.Yaw, cam.Pitch = flipView(cam, 0, stl.PitchLimit)

	case "r", "0":
		cam = stl.DefaultCamera()
	case "f":
		// Re-fit without losing the angle you found.
		cam.Zoom, cam.PanX, cam.PanY = 1, 0, 0

	case "s":
		return m, (&m).toggleSTLSpin()

	case "n", "tab":
		return m, (&m).cycleSTL(1)
	case "p", "shift+tab":
		return m, (&m).cycleSTL(-1)

	default:
		// The key that opened the viewer closes it, the way it does for the
		// image modal — and it goes through the binding, so a rebound preview
		// key still works. Checked here rather than in the switch above because
		// it is configurable and the rest of these are not.
		if key.Matches(msg, m.keys.Preview) {
			return m, (&m).closeSTLView()
		}
		return m, nil
	}
	return m, (&m).nudgeSTLCamera(cam)
}

// flipView returns the requested orthographic angle, or the one opposite it when
// the camera is already there — so tapping x twice shows both sides.
func flipView(cam stl.Camera, yaw, pitch float32) (float32, float32) {
	const eps = 0.01
	near := func(a, b float32) bool { return math.Abs(float64(a-b)) < eps }
	if near(cam.Pitch, pitch) && near(normYaw(cam.Yaw), normYaw(yaw)) {
		if pitch != 0 {
			return yaw, -pitch // top ↔ bottom
		}
		return yaw + math.Pi, pitch // front ↔ back, left ↔ right
	}
	return yaw, pitch
}

// normYaw folds a yaw into [0, 2π) so two angles a full turn apart compare equal.
func normYaw(y float32) float32 {
	const twoPi = float32(2 * math.Pi)
	y = float32(math.Mod(float64(y), float64(twoPi)))
	if y < 0 {
		y += twoPi
	}
	return y
}

// toggleSTLSpin starts or stops the turntable.
func (m *Model) toggleSTLSpin() tea.Cmd {
	g := &m.stl
	g.spin = !g.spin
	if !g.spin {
		// Stopping is a settle: render the crisp frame of wherever it stopped.
		g.moving = false
		return m.stlFrameCmd()
	}
	g.moving = true
	return stlSpinCmd(g.gen)
}

// --- mouse ----------------------------------------------------------------

// stlMouseClick handles a press while the viewer is up: outside the box it
// closes (matching every other modal here), inside it starts an orbit — or a pan
// with shift held, or with the right button, which is what a 3D application
// puts it on.
func (m Model) stlMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseLeft, tea.MouseRight, tea.MouseMiddle:
	default:
		return m, nil
	}
	if (&m).clickOutsideSTL(msg.X, msg.Y) {
		return m, (&m).closeSTLView()
	}
	g := &m.stl
	g.drag = true
	g.panning = msg.Button != tea.MouseLeft || msg.Mod&tea.ModShift != 0
	g.lastX, g.lastY = msg.X, msg.Y
	return m, nil
}

// stlMouseMotion turns a drag into camera movement: cells moved since the last
// event, scaled to radians (orbit) or screen fractions (pan). Pan is divided by
// the viewport size so a drag keeps the model under the pointer regardless of
// how big the box is.
func (m Model) stlMouseMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	g := &m.stl
	if !g.drag {
		return m, nil
	}
	dx, dy := msg.X-g.lastX, msg.Y-g.lastY
	g.lastX, g.lastY = msg.X, msg.Y
	if dx == 0 && dy == 0 {
		return m, nil
	}
	cam := g.cam
	if g.panning {
		span := float32(max(min(g.cols, g.rows*2), 1))
		cam.PanX += float32(dx) / span
		// Two cells vertically to one horizontally: the pointer moves in cells,
		// the model in pixels, and a cell is about twice as tall as it is wide.
		cam.PanY -= float32(dy) * 2 / span
	} else {
		// Note the sign: a drag *grabs the model*, so the surface follows the
		// pointer — drag right and the face you are looking at travels right.
		// That is the opposite of the arrow keys, which orbit the *camera*
		// (press right, the camera goes right and the model appears to turn
		// left), and both conventions are what a 3D application uses for those
		// two inputs. TestYawTurnsModelLeft in internal/stl pins what the sign
		// of Yaw does on screen; getting this backwards is immediately obvious
		// to anyone using it and invisible to a test of the field alone.
		cam.Yaw -= float32(dx) * stlDragOrbit
		// Drag down and the top of the model rotates toward you, i.e. you end up
		// looking down on it — same grab, in the other axis.
		cam.Pitch += float32(dy) * stlDragOrbit * 2
	}
	return m, (&m).nudgeSTLCamera(cam)
}

// stlMouseRelease ends a drag.
func (m Model) stlMouseRelease() (tea.Model, tea.Cmd) {
	m.stl.drag, m.stl.panning = false, false
	return m, nil
}

// stlMouseWheel zooms about the centre of the viewport.
func (m Model) stlMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	cam := m.stl.cam
	switch msg.Button {
	case tea.MouseWheelUp:
		cam.Zoom *= stlWheelZoom
	case tea.MouseWheelDown:
		cam.Zoom /= stlWheelZoom
	default:
		// The key that opened the viewer closes it, the way it does for the
		// image modal — and it goes through the binding, so a rebound preview
		// key still works. Checked here rather than in the switch above because
		// it is configurable and the rest of these are not.
		if key.Matches(msg, m.keys.Preview) {
			return m, (&m).closeSTLView()
		}
		return m, nil
	}
	return m, (&m).nudgeSTLCamera(cam)
}

// clickOutsideSTL reports whether a screen cell falls outside the viewer's box.
func (m *Model) clickOutsideSTL(x, y int) bool {
	x0, y0, x1, y1, ok := m.stlBoxBounds()
	if !ok {
		return true
	}
	return x < x0 || x >= x1 || y < y0 || y >= y1
}

// stlBoxBounds returns the screen-absolute rectangle of the viewer, matching the
// lipgloss.Place centering in renderViewContent — the same reconstruction
// previewBoxBounds does for the image modal.
func (m *Model) stlBoxBounds() (x0, y0, x1, y1 int, ok bool) {
	if !m.stl.active {
		return 0, 0, 0, 0, false
	}
	bodyH := 0
	if m.vcache != nil {
		bodyH = m.vcache.bodyH
	}
	if bodyH <= 0 || m.width <= 0 {
		return 0, 0, 0, 0, false
	}
	box := m.renderSTLView()
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	if bw <= 0 || bh <= 0 {
		return 0, 0, 0, 0, false
	}
	x0 = placeOffset(m.width, bw)
	y0 = tabsHeight + placeOffset(bodyH, bh)
	return x0, y0, x0 + bw, y0 + bh, true
}

// --- rendering the modal --------------------------------------------------

// renderSTLView draws the viewer. The model itself is a grid of Kitty
// placeholder cells — the pixels arrive out of band — so this stays cheap no
// matter what the mesh is doing.
func (m *Model) renderSTLView() string {
	g := &m.stl
	if !g.active {
		return ""
	}
	inner := max(g.cols, 24)

	var body string
	switch {
	case g.err != nil:
		body = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).
			Render(truncate("couldn't render "+g.name+": "+g.err.Error(), inner))
	case g.loading || g.mesh == nil:
		body = lipgloss.NewStyle().Foreground(dimColor).Render("loading " + truncate(g.name, inner-9) + "…")
	default:
		body = kittyPlaceholder(g.imgID, g.rows, g.cols)
	}

	caption := lipgloss.NewStyle().Foreground(dimColor).Render(truncate(m.stlCaption(), inner))
	hint := lipgloss.NewStyle().Foreground(dimColor).Italic(true).
		Render(truncate(m.stlHint(), inner))

	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Render(lipgloss.JoinVertical(lipgloss.Center, body, caption, "", hint))
}

// stlCaption names the model and states what it is: facet count, bounding-box
// dimensions, file size. The dimensions carry no unit because STL carries no
// unit — every slicer in the world assumes millimetres, and the file does not
// say so.
func (m *Model) stlCaption() string {
	g := &m.stl
	parts := []string{g.name}
	if g.mesh != nil {
		s := g.mesh.Size()
		parts = append(parts,
			fmt.Sprintf("%s facets", withThousands(len(g.mesh.Tris))),
			fmt.Sprintf("%.1f × %.1f × %.1f", s.X, s.Y, s.Z))
	}
	if g.size > 0 {
		parts = append(parts, humanSize(g.size))
	}
	if len(g.items) > 1 {
		parts = append(parts, fmt.Sprintf("%d/%d", g.idx+1, len(g.items)))
	}
	return strings.Join(parts, " · ")
}

// stlHint is the control legend. Two lines' worth of controls in one line's
// worth of space, so it lists what you cannot guess (the axis views, the spin
// toggle) alongside what you would try first.
func (m *Model) stlHint() string {
	g := &m.stl
	spin := "s spin"
	if g.spin {
		spin = "s stop"
	}
	parts := []string{
		"drag/↑↓←→ orbit",
		"shift+drag pan",
		"wheel/+- zoom",
		"xyz views",
		spin,
		"r reset",
	}
	if len(g.items) > 1 {
		parts = append(parts, "n/p next")
	}
	parts = append(parts, "esc/q close")
	return strings.Join(parts, " · ")
}

// withThousands groups an integer for reading: a facet count is the one number
// in the caption where the order of magnitude is the point.
func withThousands(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
