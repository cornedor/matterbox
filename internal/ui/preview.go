package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"  // GIF attachments (decoded via decodeImageFrames)
	_ "image/jpeg" // JPEG attachments
	_ "image/png"  // PNG attachments
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// previewHTTPClient fetches external preview images (e.g. GIF-picker ![](…)
// links). The timeout bounds a slow or hung host; the modal surfaces the error
// if it trips.
var previewHTTPClient = &http.Client{Timeout: 30 * time.Second}

// previewMaxImageBytes caps an external image download so a pathological URL
// can't exhaust memory; generous for a GIF.
const previewMaxImageBytes = 32 << 20 // 32 MiB

// The image-preview modal: press space on a message carrying an image — either
// an uploaded attachment or an image URL in the body (e.g. a GIF-picker
// ![](…) link) — to view it inline, rendered via the Kitty Unicode-placeholder
// protocol (the same mechanism custom emoji use — see emojiimg.go — so it
// survives the TUI's per-frame repaints). Closable with space/esc/q; ←/→ cycle
// when a post has several images.
//
// Kitty-only by design: there is no ASCII/half-block fallback, so the modal is
// available only when the custom-emoji graphics probe came back OK on a
// truecolor terminal (m.emojiImg.active()). Animated GIFs cycle through their
// frames (same single-id re-transmit technique custom emoji use) unless the
// animations.image_preview config toggle is off, in which case they show the
// first frame only. See IMAGE_PREVIEW_ANIMATION.md.

// previewItem is one previewable image on a post: either an uploaded attachment
// (downloaded through the Mattermost client) or an external image URL from the
// message body (e.g. a GIF-picker ![](…) link), fetched over HTTP. Exactly one
// of file / url is set.
type previewItem struct {
	file *model.FileInfo
	url  string
	name string
}

// previewState holds the live image-preview modal. The zero value is closed.
type previewState struct {
	active bool

	// items is the post's previewable images (attachments first, then body image
	// URLs); idx is the one on screen. ←/→ move idx when len(items) > 1.
	items []previewItem
	idx   int

	// loading is true while the current file downloads/decodes; err records a
	// failure. Mutually exclusive with a non-nil img.
	loading bool
	err     error

	// img is the first decoded frame (used for sizing + the caption); caption is
	// the line shown beneath the image.
	img     image.Image
	caption string

	// frames/delays hold the decoded image: a still image is a single frame with
	// a nil delays slice; an animated GIF (when animations.image_preview is on)
	// has every composited frame and its per-frame display time. frameIdx is the
	// frame currently transmitted under id; frameStart is when it began showing.
	// All frames share one image id, so animating is a re-transmit, not a
	// re-render (mirrors the custom-emoji loop in emojiimg.go).
	frames     []image.Image
	delays     []time.Duration
	frameIdx   int
	frameStart time.Time

	// rows/cols is the placement size in text cells, recomputed on load and on
	// resize from the image aspect and the terminal size.
	rows, cols int

	// id is the Kitty image id the current frame was transmitted under,
	// allocated from emojiImg.allocID() and freed with kittyDelete on close.
	id uint32
}

// previewImageLoadedMsg carries a finished background decode. gen guards against
// a stale result the user already cycled/closed past (see Model.previewGen).
// frames has one entry for a still image and many for an animated GIF; delays is
// parallel to frames (nil for a still image).
type previewImageLoadedMsg struct {
	gen     int
	frames  []image.Image
	delays  []time.Duration
	caption string
	err     error
}

// previewTickMsg drives the preview modal's GIF animation. gen drops a tick from
// a preview the user has since cycled or closed (see Model.previewGen).
type previewTickMsg struct{ gen int }

// previewAnimMinInterval floors the preview animation cadence, capping the
// wakeup rate for a GIF that claims very short per-frame delays.
const previewAnimMinInterval = 50 * time.Millisecond

// previewableMIME reports whether we can decode and preview a file inline. We
// render Kitty-only and decode with the stdlib image package (+gif/jpeg/png),
// so those qualify; anything else (webp, svg, tiff, …) is left to `o`.
func previewableMIME(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/jpg", "image/gif":
		return true
	}
	return false
}

// previewImages enumerates a post's previewable images: uploaded attachments
// (previewable MIME, metadata order) followed by image URLs in the body — the
// ![](…) links a GIF picker posts, plus any bare/linked URL whose path looks
// like an image. Reuses collectOpenables so the extraction + dedup matches what
// `o` opens. URLs are previewable only if their extension is one stdlib decodes.
func previewImages(p *model.Post) []previewItem {
	if p == nil {
		return nil
	}
	var out []previewItem
	for _, o := range collectOpenables(p) {
		switch {
		case o.file != nil:
			if previewableMIME(o.file.MimeType) {
				out = append(out, previewItem{file: o.file, name: o.file.Name})
			}
		case isPreviewableImageURL(o.url):
			out = append(out, previewItem{url: o.url, name: o.name})
		}
	}
	return out
}

// isPreviewableImageURL reports whether a URL's path ends in an extension we can
// decode and render (Kitty-only, stdlib decoders). The query string — which a
// CDN like Giphy stuffs with cache keys — is ignored.
func isPreviewableImageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".png", ".jpg", ".jpeg", ".gif":
		return true
	}
	return false
}

// openImagePreview raises the preview modal for the first previewable image on
// p (←/→ cycle the rest). No image → a status hint; no terminal graphics → a
// hint to use `o`, since we render Kitty-only.
func (m Model) openImagePreview(p *model.Post) (tea.Model, tea.Cmd) {
	items := previewImages(p)
	if len(items) == 0 {
		m.status = "no image to preview on this message"
		return m, nil
	}
	if m.emojiImg == nil || !m.emojiImg.active() {
		m.status = "image preview needs a Kitty-capable terminal — press o to open"
		return m, nil
	}
	m.preview = previewState{active: true, items: items, idx: 0, loading: true}
	m.previewGen++
	return m, m.loadPreviewImage(m.previewGen, items[0])
}

// loadPreviewImage fetches (reusing the on-disk cache) and decodes it in the
// background, returning a previewImageLoadedMsg tagged with gen.
func (m Model) loadPreviewImage(gen int, it previewItem) tea.Cmd {
	return func() tea.Msg {
		data, err := m.readPreviewBytes(it)
		if err != nil {
			return previewImageLoadedMsg{gen: gen, err: err}
		}
		frames, delays, derr := decodeImageFrames(data, m.animatePreview)
		if derr != nil {
			return previewImageLoadedMsg{gen: gen, err: fmt.Errorf("decode image: %w", derr)}
		}
		size := int64(len(data))
		if it.file != nil && it.file.Size > 0 {
			size = it.file.Size
		}
		return previewImageLoadedMsg{gen: gen, frames: frames, delays: delays, caption: previewCaption(it.name, frames[0], size)}
	}
}

// readPreviewBytes returns the item's raw bytes: an attachment via the
// Mattermost client (cached by file id), or an external URL over HTTP (cached
// by URL hash).
func (m Model) readPreviewBytes(it previewItem) ([]byte, error) {
	if it.file != nil {
		path, _ := m.cachedFilePath(it.file)
		return m.readOrDownloadFile(path, it.file)
	}
	path, _ := cachedURLPath(it.url)
	return m.readOrDownloadURL(path, it.url)
}

// readOrDownloadFile returns the file's bytes from the on-disk cache, falling
// back to a download (which is then cached). Mirrors openOpenable's caching.
func (m Model) readOrDownloadFile(path string, f *model.FileInfo) ([]byte, error) {
	if path != "" {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data, nil
		}
	}
	data, err := m.client.DownloadFile(m.ctx, f.Id)
	if err != nil {
		return nil, err
	}
	if path != "" {
		_ = os.WriteFile(path, data, 0o644) // best effort
	}
	return data, nil
}

// readOrDownloadURL returns the external image's bytes from the on-disk cache,
// falling back to an HTTP GET (capped at previewMaxImageBytes, then cached).
func (m Model) readOrDownloadURL(path, rawURL string) ([]byte, error) {
	if path != "" {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data, nil
		}
	}
	req, err := http.NewRequestWithContext(m.ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := previewHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch image: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, previewMaxImageBytes))
	if err != nil {
		return nil, err
	}
	if path != "" {
		_ = os.WriteFile(path, data, 0o644) // best effort
	}
	return data, nil
}

// cachedURLPath returns the on-disk cache path for an external preview image,
// keyed by a hash of the URL (mirrors cachedFilePath/cachedEmojiPath). Holds the
// original downloaded bytes so a warm reopen costs no HTTP.
func cachedURLPath(rawURL string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "matterbox", "urlimg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(dir, hex.EncodeToString(sum[:])), nil
}

// previewCaption builds the line under the image: name · W×H · size (the size is
// omitted when unknown).
func previewCaption(name string, img image.Image, size int64) string {
	b := img.Bounds()
	if size > 0 {
		return fmt.Sprintf("%s · %d×%d · %s", normalizeFilename(name), b.Dx(), b.Dy(), humanSize(size))
	}
	return fmt.Sprintf("%s · %d×%d", normalizeFilename(name), b.Dx(), b.Dy())
}

// handlePreviewLoaded installs a finished decode: it sizes the placement to the
// terminal, allocates an image id, and transmits the first frame out of band so
// the placeholder resolves. A multi-frame GIF (animations on) arms the
// animation tick. Stale results (the user cycled/closed) are dropped.
func (m Model) handlePreviewLoaded(msg previewImageLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.preview.active || msg.gen != m.previewGen {
		return m, nil
	}
	m.preview.loading = false
	if msg.err != nil {
		m.preview.err = msg.err
		return m, nil
	}
	m.preview.frames = msg.frames
	m.preview.delays = msg.delays
	m.preview.frameIdx = 0
	m.preview.frameStart = time.Time{}
	m.preview.img = msg.frames[0]
	m.preview.caption = msg.caption
	m.sizePreview()
	if m.preview.id == 0 {
		m.preview.id = m.emojiImg.allocID()
	}
	seq, err := kittyTransmitImage(m.preview.id, m.preview.frames[0], m.preview.rows, m.preview.cols)
	if err != nil {
		m.preview.err = err
		return m, nil
	}
	if len(m.preview.frames) > 1 {
		m.preview.frameStart = time.Now()
		return m, tea.Batch(tea.Raw(seq), previewTickCmd(m.previewGen, m.preview.delays[0]))
	}
	return m, tea.Raw(seq)
}

// handlePreviewTick advances the open preview's GIF to the frame due at this
// tick and re-transmits it under the same image id (so the on-screen
// placeholder repaints with no re-render), then reschedules from the next
// frame's delay. A tick from a cycled/closed preview, or for a still image, is
// dropped.
func (m Model) handlePreviewTick(msg previewTickMsg) (tea.Model, tea.Cmd) {
	if !m.preview.active || msg.gen != m.previewGen || len(m.preview.frames) <= 1 || m.preview.id == 0 {
		return m, nil
	}
	now := time.Now()
	// Catch up across as many frames as the elapsed time covers, so a late tick
	// doesn't play in slow motion.
	for now.Sub(m.preview.frameStart) >= m.preview.delays[m.preview.frameIdx] {
		m.preview.frameStart = m.preview.frameStart.Add(m.preview.delays[m.preview.frameIdx])
		m.preview.frameIdx = (m.preview.frameIdx + 1) % len(m.preview.frames)
	}
	rem := m.preview.delays[m.preview.frameIdx] - now.Sub(m.preview.frameStart)
	if rem < 0 {
		rem = 0
	}
	seq, err := kittyTransmitImage(m.preview.id, m.preview.frames[m.preview.frameIdx], m.preview.rows, m.preview.cols)
	if err != nil {
		// Transient encode failure: skip this frame, keep the loop alive.
		return m, previewTickCmd(m.previewGen, rem)
	}
	return m, tea.Batch(tea.Raw(seq), previewTickCmd(m.previewGen, rem))
}

// previewTickCmd schedules the next preview animation tick after d (floored to
// previewAnimMinInterval), tagged with gen so a stale tick is dropped.
func previewTickCmd(gen int, d time.Duration) tea.Cmd {
	if d < previewAnimMinInterval {
		d = previewAnimMinInterval
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return previewTickMsg{gen: gen} })
}

// cyclePreview moves to the previous/next image on the post, freeing the current
// id and kicking off a fresh load. No-op when the post has a single image.
func (m Model) cyclePreview(delta int) (tea.Model, tea.Cmd) {
	if len(m.preview.items) <= 1 {
		return m, nil
	}
	free := m.freePreviewID()
	n := len(m.preview.items)
	m.preview.idx = ((m.preview.idx+delta)%n + n) % n
	m.preview.loading = true
	m.preview.err = nil
	m.preview.img = nil
	m.preview.id = 0
	m.preview.frames = nil
	m.preview.delays = nil
	m.preview.frameIdx = 0
	m.preview.frameStart = time.Time{}
	m.previewGen++ // also drops any in-flight animation tick for the old image
	return m, tea.Batch(free, m.loadPreviewImage(m.previewGen, m.preview.items[m.preview.idx]))
}

// closeImagePreview tears down the modal and frees the image id from terminal
// memory. previewGen is bumped so any in-flight load is ignored on arrival.
func (m *Model) closeImagePreview() tea.Cmd {
	cmd := m.freePreviewID()
	m.preview = previewState{}
	m.previewGen++
	return cmd
}

// freePreviewID returns the out-of-band delete for the current image id (nil if
// none allocated yet). Does not touch other preview state.
func (m *Model) freePreviewID() tea.Cmd {
	if m.preview.id == 0 {
		return nil
	}
	return tea.Raw(kittyDelete(m.preview.id))
}

// handlePreviewKey owns every keystroke while the modal is up. The configurable
// bindings drive it — the preview key (space by default) toggles it shut, and
// the list-left/right keys (←/h, →/l) cycle images — so a user's rebindings and
// vim_nav flow through. esc/q are the conventional modal dismiss (hardwired like
// the cheatsheet / open-picker modals); ctrl+c still quits.
func (m Model) handlePreviewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		return m, m.closeImagePreview()
	}
	switch {
	case key.Matches(msg, m.keys.Preview): // same key that opened it closes it
		return m, m.closeImagePreview()
	case key.Matches(msg, m.keys.Right):
		return m.cyclePreview(1)
	case key.Matches(msg, m.keys.Left):
		return m.cyclePreview(-1)
	}
	return m, nil
}

// resizePreview re-fits and re-transmits the current frame after a terminal
// resize so it keeps filling the modal. No-op unless a loaded preview is open.
// The animation tick (if any) keeps running independently and will transmit
// subsequent frames at the new size.
func (m *Model) resizePreview() tea.Cmd {
	if !m.preview.active || len(m.preview.frames) == 0 || m.preview.id == 0 {
		return nil
	}
	m.sizePreview()
	seq, err := kittyTransmitImage(m.preview.id, m.preview.frames[m.preview.frameIdx], m.preview.rows, m.preview.cols)
	if err != nil {
		return nil
	}
	return tea.Raw(seq)
}

// previewMaxBox returns the largest cell box (cols × rows) the modal image may
// occupy, leaving room for the border, caption, hint, and a screen margin.
func (m *Model) previewMaxBox() (cols, rows int) {
	cols = m.width*9/10 - 4  // border (2) + padding (2)
	rows = m.height*9/10 - 6 // border (2) + caption + blank + hint + margin
	if cols < 4 {
		cols = 4
	}
	if rows < 2 {
		rows = 2
	}
	return cols, rows
}

// sizePreview sets preview.rows/cols from the image aspect and the terminal size.
func (m *Model) sizePreview() {
	if m.preview.img == nil {
		return
	}
	b := m.preview.img.Bounds()
	maxC, maxR := m.previewMaxBox()
	m.preview.cols, m.preview.rows = fitImageCells(b.Dx(), b.Dy(), maxC, maxR, m.cellPxW, m.cellPxH)
}

// fitImageCells picks the rows×cols cell box for a wPx×hPx image within
// (maxCols, maxRows), preserving aspect. The Kitty protocol scales the image to
// fill the chosen box, so the box aspect is what keeps it undistorted.
//
// When the terminal reported its cell size (cellPxW, cellPxH both > 0) we work
// in real pixels and cap the scale at 1, so an image smaller than the box keeps
// its native size instead of being blown up to fill it. Without that figure we
// fall back to filling the box at an assumed ~1:2 cell aspect (cells are about
// twice as tall as wide) — which can upscale, but it's the best we can do blind.
func fitImageCells(wPx, hPx, maxCols, maxRows, cellPxW, cellPxH int) (cols, rows int) {
	if wPx <= 0 || hPx <= 0 {
		return maxCols, maxRows
	}
	if cellPxW > 0 && cellPxH > 0 {
		// Scale to fit the box, but never above 1:1 — that cap is what stops a
		// small image from being upsized when the modal is larger than it.
		scale := math.Min(
			float64(maxCols*cellPxW)/float64(wPx),
			float64(maxRows*cellPxH)/float64(hPx),
		)
		if scale > 1 {
			scale = 1
		}
		cols = int(math.Round(float64(wPx) * scale / float64(cellPxW)))
		rows = int(math.Round(float64(hPx) * scale / float64(cellPxH)))
	} else {
		// (cols·cellW)/(rows·cellH) = wPx/hPx, with cellH ≈ 2·cellW ⇒ cols/rows = 2·wPx/hPx.
		ratio := 2.0 * float64(wPx) / float64(hPx)
		cols = maxCols
		rows = int(math.Round(float64(cols) / ratio))
		if rows > maxRows {
			rows = maxRows
			cols = int(math.Round(float64(rows) * ratio))
		}
	}
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	if cols > maxCols {
		cols = maxCols
	}
	if rows > maxRows {
		rows = maxRows
	}
	return cols, rows
}

// previewImageBlock builds the placeholder grid (rows×cols cells pointing at the
// transmitted image id). Empty until the image is loaded and sized.
func (m *Model) previewImageBlock() string {
	if m.preview.img == nil || m.preview.id == 0 || m.preview.rows <= 0 || m.preview.cols <= 0 {
		return ""
	}
	return kittyPlaceholder(m.preview.id, m.preview.rows, m.preview.cols)
}

// renderPreviewPopup composes the bordered modal: the image (or a loading/error
// line), the caption, and a hint line. Centered by viewContent via lipgloss.Place.
func (m *Model) renderPreviewPopup() string {
	if !m.preview.active {
		return ""
	}
	maxC, _ := m.previewMaxBox()

	var body string
	switch {
	case m.preview.err != nil:
		body = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).
			Render(truncate("couldn't load image: "+m.preview.err.Error(), maxC))
	case m.preview.loading || m.preview.img == nil:
		body = lipgloss.NewStyle().Foreground(dimColor).Render("loading image…")
	default:
		caption := lipgloss.NewStyle().Foreground(dimColor).
			Render(truncate(m.preview.caption, maxC))
		body = lipgloss.JoinVertical(lipgloss.Center, m.previewImageBlock(), caption)
	}

	hint := "space/esc/q close"
	if len(m.preview.items) > 1 {
		hint = fmt.Sprintf("%d/%d · ←/→ next · space/esc/q close",
			m.preview.idx+1, len(m.preview.items))
	}
	hintLine := lipgloss.NewStyle().Foreground(dimColor).Italic(true).Render(hint)

	content := lipgloss.JoinVertical(lipgloss.Center, body, "", hintLine)

	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Render(content)
}
