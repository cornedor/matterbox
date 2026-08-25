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
// here: see svgimg.Options.MaxSide.

const (
	// svgThumbSide is the raster size for an inline thumbnail. Larger than any
	// thumbnail box, because everything downstream only scales down and
	// downscaling is what antialiases well — a 16px icon drawn at 16px and then
	// stretched would look far worse than this costs.
	svgThumbSide = 512
	// svgPreviewMinSide / svgPreviewMaxSide bound the modal's raster. The real
	// figure tracks the terminal's own pixel width (svgPreviewSide), so a drawing
	// is as crisp as the screen can show; these only stop a tiny or enormous
	// terminal from asking for something silly. The ceiling matters: rasterising
	// is quadratic in the side, and the placement never shows more than the
	// terminal's own pixels anyway (fitImageCells caps at natural logical size).
	svgPreviewMinSide = 640
	svgPreviewMaxSide = 1280
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
	mime, _, _ := strings.Cut(strings.ToLower(f.MimeType), ";")
	switch strings.TrimSpace(mime) {
	case "image/svg+xml", "image/svg":
		return true
	}
	ext := strings.ToLower(strings.TrimPrefix(f.Extension, "."))
	if ext == "" {
		if i := strings.LastIndex(f.Name, "."); i >= 0 {
			ext = strings.ToLower(f.Name[i+1:])
		}
	}
	return ext == "svg"
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

// svgPreviewSide is the raster size for the preview modal: the terminal's pixel
// width, bounded. Mirrors what previewProfile does for video, and for the same
// reason — the modal is worth rendering at the resolution it will be shown at.
func (m *Model) svgPreviewSide() int {
	side := m.width * m.cellPxW
	if side < svgPreviewMinSide {
		return svgPreviewMinSide
	}
	if side > svgPreviewMaxSide {
		return svgPreviewMaxSide
	}
	return side
}

// decodeSVGFrames renders a drawing into the single-frame form the transmit
// pipeline expects, so it lands on the same encode-fit-and-place tail as every
// other still. SVG animation (SMIL, CSS) is not rendered, hence never a second
// frame and never a delay.
func decodeSVGFrames(raw []byte, side int) ([]image.Image, []time.Duration, error) {
	res, err := svgimg.Decode(raw, svgimg.Options{MaxSide: side, CurrentColor: svgCurrentColor()})
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
