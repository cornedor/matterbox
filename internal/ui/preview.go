package ui

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // first-frame decode for animated GIF attachments
	_ "image/jpeg" // JPEG attachments
	_ "image/png"  // PNG attachments
	"math"
	"os"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// The image-preview modal: press space on a message carrying an image
// attachment to view it inline, rendered via the Kitty Unicode-placeholder
// protocol (the same mechanism custom emoji use — see emojiimg.go — so it
// survives the TUI's per-frame repaints). Closable with space/esc/q; ←/→ cycle
// when a post has several images.
//
// Kitty-only by design: there is no ASCII/half-block fallback, so the modal is
// available only when the custom-emoji graphics probe came back OK on a
// truecolor terminal (m.emojiImg.active()). Animated GIFs render as a static
// first frame for now; see IMAGE_PREVIEW_ANIMATION.md for the deferred
// animation design.

// previewState holds the live image-preview modal. The zero value is closed.
type previewState struct {
	active bool

	// files is the post's previewable image attachments (metadata order); idx
	// is the one on screen. ←/→ move idx when len(files) > 1.
	files []*model.FileInfo
	idx   int

	// loading is true while the current file downloads/decodes; err records a
	// failure. Mutually exclusive with a non-nil img.
	loading bool
	err     error

	// img is the decoded image (first frame for GIFs); caption is the line shown
	// beneath it.
	img     image.Image
	caption string

	// rows/cols is the placement size in text cells, recomputed on load and on
	// resize from the image aspect and the terminal size.
	rows, cols int

	// id is the Kitty image id the current frame was transmitted under,
	// allocated from emojiImg.allocID() and freed with kittyDelete on close.
	id uint32
}

// previewImageLoadedMsg carries a finished background decode. gen guards against
// a stale result the user already cycled/closed past (see Model.previewGen).
type previewImageLoadedMsg struct {
	gen     int
	img     image.Image
	caption string
	err     error
}

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

// imageAttachments returns the post's previewable image files in metadata order.
func imageAttachments(p *model.Post) []*model.FileInfo {
	if p == nil || p.Metadata == nil {
		return nil
	}
	var out []*model.FileInfo
	for _, f := range p.Metadata.Files {
		if f != nil && previewableMIME(f.MimeType) {
			out = append(out, f)
		}
	}
	return out
}

// openImagePreview raises the preview modal for the first previewable image on
// p (←/→ cycle the rest). No image → a status hint; no terminal graphics → a
// hint to use `o`, since we render Kitty-only.
func (m Model) openImagePreview(p *model.Post) (tea.Model, tea.Cmd) {
	files := imageAttachments(p)
	if len(files) == 0 {
		m.status = "no image to preview on this message"
		return m, nil
	}
	if m.emojiImg == nil || !m.emojiImg.active() {
		m.status = "image preview needs a Kitty-capable terminal — press o to open"
		return m, nil
	}
	m.preview = previewState{active: true, files: files, idx: 0, loading: true}
	m.previewGen++
	return m, m.loadPreviewImage(m.previewGen, files[0])
}

// loadPreviewImage downloads (reusing the file cache) and decodes f in the
// background, returning a previewImageLoadedMsg tagged with gen.
func (m Model) loadPreviewImage(gen int, f *model.FileInfo) tea.Cmd {
	return func() tea.Msg {
		path, _ := m.cachedFilePath(f)
		data, err := m.readOrDownloadFile(path, f)
		if err != nil {
			return previewImageLoadedMsg{gen: gen, err: err}
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return previewImageLoadedMsg{gen: gen, err: fmt.Errorf("decode image: %w", err)}
		}
		return previewImageLoadedMsg{gen: gen, img: img, caption: previewCaption(f, img)}
	}
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

// previewCaption builds the line under the image: name · W×H · size.
func previewCaption(f *model.FileInfo, img image.Image) string {
	name := normalizeFilename(f.Name)
	b := img.Bounds()
	return fmt.Sprintf("%s · %d×%d · %s", name, b.Dx(), b.Dy(), humanSize(f.Size))
}

// handlePreviewLoaded installs a finished decode: it sizes the placement to the
// terminal, allocates an image id, and transmits the image out of band so the
// placeholder resolves. Stale results (the user cycled/closed) are dropped.
func (m Model) handlePreviewLoaded(msg previewImageLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.preview.active || msg.gen != m.previewGen {
		return m, nil
	}
	m.preview.loading = false
	if msg.err != nil {
		m.preview.err = msg.err
		return m, nil
	}
	m.preview.img = msg.img
	m.preview.caption = msg.caption
	m.sizePreview()
	if m.preview.id == 0 {
		m.preview.id = m.emojiImg.allocID()
	}
	seq, err := kittyTransmitImage(m.preview.id, m.preview.img, m.preview.rows, m.preview.cols)
	if err != nil {
		m.preview.err = err
		return m, nil
	}
	return m, tea.Raw(seq)
}

// cyclePreview moves to the previous/next image on the post, freeing the current
// id and kicking off a fresh load. No-op when the post has a single image.
func (m Model) cyclePreview(delta int) (tea.Model, tea.Cmd) {
	if len(m.preview.files) <= 1 {
		return m, nil
	}
	free := m.freePreviewID()
	n := len(m.preview.files)
	m.preview.idx = ((m.preview.idx+delta)%n + n) % n
	m.preview.loading = true
	m.preview.err = nil
	m.preview.img = nil
	m.preview.id = 0
	m.previewGen++
	return m, tea.Batch(free, m.loadPreviewImage(m.previewGen, m.preview.files[m.preview.idx]))
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

// resizePreview re-fits and re-transmits the current image after a terminal
// resize so it keeps filling the modal. No-op unless a loaded preview is open.
func (m *Model) resizePreview() tea.Cmd {
	if !m.preview.active || m.preview.img == nil || m.preview.id == 0 {
		return nil
	}
	m.sizePreview()
	seq, err := kittyTransmitImage(m.preview.id, m.preview.img, m.preview.rows, m.preview.cols)
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
	m.preview.cols, m.preview.rows = fitImageCells(b.Dx(), b.Dy(), maxC, maxR)
}

// fitImageCells picks the rows×cols cell box that best fits a wPx×hPx image
// within (maxCols, maxRows) while preserving aspect, assuming a ~1:2 cell aspect
// (terminal cells are about twice as tall as wide). The Kitty protocol scales
// the image to fill the chosen box, so matching the box aspect to the image is
// what keeps it undistorted.
func fitImageCells(wPx, hPx, maxCols, maxRows int) (cols, rows int) {
	if wPx <= 0 || hPx <= 0 {
		return maxCols, maxRows
	}
	// (cols·cellW)/(rows·cellH) = wPx/hPx, with cellH ≈ 2·cellW ⇒ cols/rows = 2·wPx/hPx.
	ratio := 2.0 * float64(wPx) / float64(hPx)
	cols = maxCols
	rows = int(math.Round(float64(cols) / ratio))
	if rows > maxRows {
		rows = maxRows
		cols = int(math.Round(float64(rows) * ratio))
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
	if len(m.preview.files) > 1 {
		hint = fmt.Sprintf("%d/%d · ←/→ next · space/esc/q close",
			m.preview.idx+1, len(m.preview.files))
	}
	hintLine := lipgloss.NewStyle().Foreground(dimColor).Italic(true).Render(hint)

	content := lipgloss.JoinVertical(lipgloss.Center, body, "", hintLine)

	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(focusedColor).
		Padding(0, 1).
		Render(content)
}
