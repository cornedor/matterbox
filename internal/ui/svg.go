package ui

import (
	"image"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/svgimg"
)

// SVG previews. A drawing is not decoded, it is rendered — but the result is one
// still image, so unlike the 3D viewer (stlview.go) it needs no surface of its
// own: it joins the image pipeline at the same point a PNG does, and inline
// thumbnails, the space-to-preview modal and the info panel's media list all
// pick it up from there.
//
// Rendering happens at the size the drawing is about to be shown at rather than
// at whatever units the document used, which is the one advantage vector art has
// here: see svgimg.Options.

const (
	// svgFallbackCellW / svgFallbackCellH stand in when the terminal never told us
	// its cell size, matching the assumption fitFrameToCells makes so a raster and
	// its placement agree about how big a cell is.
	svgFallbackCellW = 8
	svgFallbackCellH = 16
	// svgEmojiBox sizes the one drawing path with no destination box to ask —
	// a custom emoji, which is a couple of cells either way.
	svgEmojiBox = 64
	// svgThumbMaxBox and svgPreviewMaxBox cap how large a box we will render into.
	//
	// A thumbnail is bounded by its ten rows long before its width matters, so its
	// ceiling only has to stop an absurdly wide pane. The preview's is what keeps
	// a detailed drawing from costing seconds on a high-density terminal: past
	// this the terminal's own scaling fills the rest of the modal, which is a
	// little softer and enormously cheaper than rasterising four times the pixels.
	// The placement is allowed to enlarge by the display's pixel ratio (see
	// fitImageCells), which is exactly the headroom that makes this free on the
	// dense displays where the box gets big enough to matter.
	svgThumbMaxBox   = 2048
	svgPreviewMaxBox = 1280
)

// svgThumbMaxBytes is the cap on a drawing we render *unasked*. Rendering is the
// expense here, not the download — a densely drawn 390KB flag takes a second at
// thumbnail size, and thumbnails are built several screens ahead of the viewport,
// so one such file in a channel would cost seconds of background CPU.
//
// File size is only a proxy for how much drawing a document asks for, and it is
// the wrong way round for a big-but-simple file, which gets no thumbnail despite
// rendering fast. That is the trade: a rule we can apply before spending anything
// beats one that is right more often but only after the cost is paid. Above the
// cap the file keeps its icon, and space still opens it.
const svgThumbMaxBytes = 256 << 10

// isSVGAttachment reports whether an uploaded file is a drawing we can render.
// By MIME when there is one, and by extension otherwise: Mattermost leaves
// mime_type empty for a fair slice of uploads, so as with STL the filename is
// the more reliable field. The bytes are sniffed again before rendering
// (svgimg.Looks), so a mislabelled file fails cleanly.
func isSVGAttachment(f *model.FileInfo) bool {
	if f == nil {
		return false
	}
	// Extension first: it is a three-byte compare, where recognising the MIME type
	// means splitting a parameter off it. This question is asked of every
	// attachment on every uncached render, so which half runs first is worth the
	// thought — and EqualFold does the case-insensitive compare without scanning a
	// lowercase copy into existence.
	if strings.EqualFold(attachmentExt(f), "svg") {
		return true
	}
	mime, _, _ := strings.Cut(f.MimeType, ";")
	mime = strings.TrimSpace(mime)
	return strings.EqualFold(mime, "image/svg+xml") || strings.EqualFold(mime, "image/svg")
}

// attachmentExt is an upload's extension without its dot, falling back to the
// filename: Mattermost leaves Extension empty for a fair slice of uploads.
// Returns a slice of the existing strings, never a new one.
func attachmentExt(f *model.FileInfo) string {
	if ext := strings.TrimPrefix(f.Extension, "."); ext != "" {
		return ext
	}
	if i := strings.LastIndex(f.Name, "."); i >= 0 {
		return f.Name[i+1:]
	}
	return ""
}

// svgThumbnailable reports whether a drawing gets an inline render in the
// transcript: an SVG cheap enough to be worth rendering unasked. Mirrors
// stlThumbnailable, for the same reason.
func svgThumbnailable(f *model.FileInfo) bool {
	return isSVGAttachment(f) && f.Size <= svgThumbMaxBytes
}

// svgCurrentColor is what `currentColor` resolves to. A symbolic icon set —
// every one of them — draws in currentColor and inherits the colour from its
// surroundings; here the surroundings are the transcript, so the drawing takes
// the text side of the terminal's own background. Without this an icon designed
// to be dark-on-light is invisible in a dark terminal.
//
// Package-level, not a Model method, because the thumbnail decode path is
// package-level too and the two must agree.
func svgCurrentColor() string {
	if lightBackground.Load() {
		return "#303030"
	}
	return "#d7d7d7"
}

// cellPx is the terminal's cell size, with the same fallback the placement code
// uses when the terminal never reported one.
func (m *Model) cellPx() (w, h int) {
	if m.cellPxW > 0 && m.cellPxH > 0 {
		return m.cellPxW, m.cellPxH
	}
	return svgFallbackCellW, svgFallbackCellH
}

// svgThumbBox is the pixel box an inline thumbnail is actually drawn in: as wide
// as the pane allows and at most inlineThumbRows tall. Sizing the render to where
// it is actually going — rather than to a fixed square — is what keeps a drawing
// cheap; svgimg supersamples it from here for the antialiasing.
func (m *Model) svgThumbBox(box int) (w, h int) {
	cw, ch := m.cellPx()
	return clampBox(box*cw, svgThumbMaxBox), clampBox(inlineThumbRows*ch, svgThumbMaxBox)
}

// svgPreviewBox is the pixel box the preview modal will draw into, from the same
// cell box that sizes the placement.
func (m *Model) svgPreviewBox() (w, h int) {
	cols, rows := m.previewMaxBox()
	cw, ch := m.cellPx()
	return clampBox(cols*cw, svgPreviewMaxBox), clampBox(rows*ch, svgPreviewMaxBox)
}

// clampBox keeps a box positive and below the given rasterising ceiling.
func clampBox(px, ceiling int) int {
	if px < 1 {
		return 1
	}
	if px > ceiling {
		return ceiling
	}
	return px
}

// decodeSVGFrames renders a drawing into the single-frame form the transmit
// pipeline expects, so it lands on the same encode-fit-and-place tail as every
// other still. SVG animation (SMIL, CSS) is not rendered, hence never a second
// frame and never a delay.
func decodeSVGFrames(raw []byte, maxW, maxH int) ([]image.Image, []time.Duration, error) {
	res, err := svgimg.Decode(raw, svgimg.Options{MaxW: maxW, MaxH: maxH, CurrentColor: svgCurrentColor()})
	if err != nil {
		return nil, nil, err
	}
	return []image.Image{res.Image}, nil, nil
}

// svgCaption describes a drawing: its own size rather than the raster's, plus a
// note when it carries text. Text is the one thing our renderer does not draw,
// so an unlabelled diagram gets a reason attached instead of just looking wrong.
func svgCaption(name string, raw []byte, size int64) string {
	w, h, textDropped := svgimg.Describe(raw)
	caption := previewCaption(name, w, h, size)
	if textDropped {
		caption += " · text not rendered"
	}
	return caption
}
