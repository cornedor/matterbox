// Package svgimg rasterises SVG attachments so the terminal can draw them.
//
// SVG is the odd one out among the formats matterbox previews: there is nothing
// to decode, only a document to draw, and no pure-Go renderer covers all of it.
// oksvg — the one we use — is BSD-licensed, pulls in nothing we do not already
// have, and gets shapes, transforms, strokes and gradients right; measured
// against librsvg over a corpus of real-world files it is pixel-close on the
// large majority once path data is normalised (see path.go, which fixes the one
// class of failure that mattered).
//
// What it does not do is text: a <text> element draws nothing. That is the whole
// of the known gap, so Decode reports it rather than leaving the caller to
// wonder why a diagram came out unlabelled.
package svgimg

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

const (
	// MaxBytes caps the document we are willing to parse. Generous for a drawing
	// (they are text, and compress); a bigger one is refused rather than turned
	// into an unbounded amount of rasterising.
	MaxBytes = 8 << 20
	// maxPathPixels bounds the drawing itself. Rasterising costs roughly one unit
	// per shape per pixel of the target, and both numbers are known after parsing
	// and before a single pixel is filled — which is the one moment a pathological
	// document can be turned away for free.
	//
	// The measured rate is 80–200M units/sec, so this is a ceiling of a second or
	// two on the slowest case we let through. It is deliberately generous: a
	// detailed illustration is worth waiting for on a keypress, and the tighter
	// bar for work nobody asked for is a separate matter (see svgThumbMaxBytes in
	// the UI, which turns big drawings away before they are even fetched).
	maxPathPixels = 200_000_000
	// maxPixels caps the raster, whatever aspect ratio asks for.
	maxPixels = 16 << 20
	// defaultW/defaultH size a document that declares no intrinsic size at all,
	// matching what a browser gives a replaced element with none (CSS 2.1 §10.3.2).
	defaultW, defaultH = 300.0, 150.0
	// defaultBox is the fallback destination box, for a caller that named none.
	defaultBox = 512
)

// Options controls one rasterisation.
type Options struct {
	// MaxW, MaxH are the box the drawing is rendered to fill, in pixels. It is
	// scaled — up for a small icon, down for a large drawing — to fit inside them
	// with its aspect kept, so the raster is sized for where it is going rather
	// than for whatever units the document happened to use.
	//
	// Passing the real destination box matters for more than sharpness: nothing
	// downstream upscales a placement past its natural size, so the raster's own
	// dimensions decide how large the drawing appears. A box twice the size it
	// will be shown at is four times the rasterising for the same picture.
	MaxW, MaxH int
	// CurrentColor is what `currentColor` resolves to — in a document that is one
	// colour driven from outside (the shape every symbolic icon set takes), this
	// is the whole of its colour. Callers pass the colour the surrounding text is
	// drawn in. Empty means black.
	CurrentColor string
}

// Result is a rasterised document.
type Result struct {
	// Image is the drawing, fitted to the requested box, transparency intact.
	Image image.Image
	// W, H is the document's own size (its viewBox, or its width/height), which is
	// what a caption should report — not the size we happened to draw it at.
	W, H int
	// TextDropped records that the document contained text we did not draw, so
	// the caller can say so instead of showing a silently incomplete picture.
	TextDropped bool
}

// Looks reports whether these bytes are an SVG document. Used to route bytes to
// this package rather than to the image decoders, so it has to be certain: it
// wants a real root element, not the word "svg" somewhere in a file.
func Looks(b []byte) bool {
	head := b
	if len(head) > 4096 {
		head = head[:4096]
	}
	head = bytes.TrimLeft(head, "\xef\xbb\xbf \t\r\n")
	if len(head) == 0 || head[0] != '<' {
		return false
	}
	// An SVG can open with an XML declaration, comments, or a DOCTYPE; skip past
	// anything of that shape and require <svg to be the element we land on.
	for {
		switch {
		case bytes.HasPrefix(head, []byte("<svg")), bytes.HasPrefix(head, []byte("<svg:svg")):
			return true
		case bytes.HasPrefix(head, []byte("<?")):
			head = skipPast(head, "?>")
		case bytes.HasPrefix(head, []byte("<!--")):
			head = skipPast(head, "-->")
		case bytes.HasPrefix(head, []byte("<!")):
			head = skipDeclaration(head)
		default:
			return false
		}
		head = bytes.TrimLeft(head, " \t\r\n")
		if len(head) == 0 {
			return false
		}
	}
}

func skipPast(b []byte, delim string) []byte {
	if i := bytes.Index(b, []byte(delim)); i >= 0 {
		return b[i+len(delim):]
	}
	return nil
}

// skipDeclaration steps over a <!DOCTYPE …>, whose own internal subset may hold
// '>' characters of its own — "<!DOCTYPE svg [<!ENTITY ns_svg "…">]>" is one
// declaration, not two, and is exactly how Illustrator opens a file.
func skipDeclaration(b []byte) []byte {
	depth := 0
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		case '>':
			if depth == 0 {
				return b[i+1:]
			}
		}
	}
	return nil
}

// Decode draws the document. The error is what to show the user: a document we
// cannot parse, or one too large to be worth drawing.
func Decode(raw []byte, opt Options) (Result, error) {
	if len(raw) == 0 {
		return Result{}, fmt.Errorf("empty svg")
	}
	if len(raw) > MaxBytes {
		return Result{}, fmt.Errorf("svg too large (%d bytes, max %d)", len(raw), MaxBytes)
	}
	maxW, maxH := opt.MaxW, opt.MaxH
	if maxW <= 0 {
		maxW = defaultBox
	}
	if maxH <= 0 {
		maxH = defaultBox
	}
	color := opt.CurrentColor
	if color == "" {
		color = "#000000"
	}
	return rasterize(raw, maxW, maxH, color)
}

// rasterize is Decode's body, split out so the recover covers exactly the parse
// and the drawing. Neither library promises anything about a malformed document,
// and this runs on a background goroutine where a panic would take the whole app
// down with it — so a bad file has to come back as an error, not a crash.
func rasterize(raw []byte, maxW, maxH int, currentColor string) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = Result{}, fmt.Errorf("svg render failed: %v", r)
		}
	}()

	icon, err := parse(normalizeTransformAttrs(normalizePathAttrs(raw)), currentColor)
	if err != nil {
		return Result{}, err
	}
	// Size the drawing from our own read of the root element, not the renderer's:
	// it abandons the whole element if any one attribute will not parse, and a
	// width="100%" alongside a perfectly good viewBox is enough to do that.
	vw, vh := intrinsic(raw)
	if icon.ViewBox.W <= 0 || icon.ViewBox.H <= 0 {
		icon.ViewBox.W, icon.ViewBox.H = vw, vh
	}

	w, h := fit(icon.ViewBox.W, icon.ViewBox.H, maxW, maxH)
	if n := len(icon.SVGPaths); n*w*h > maxPathPixels {
		return Result{}, fmt.Errorf("svg too complex to draw (%d shapes at %d×%d)", n, w, h)
	}

	icon.SetTarget(0, 0, float64(w), float64(h))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	scanner := rasterx.NewScannerGV(w, h, img, img.Bounds())
	icon.Draw(rasterx.NewDasher(w, h, scanner), 1)

	return Result{
		Image:       img,
		W:           int(math.Round(vw)),
		H:           int(math.Round(vh)),
		TextDropped: hasText(raw),
	}, nil
}

// Describe reports what a caption needs — the document's own size, and whether
// it carries text we will not draw — without rasterising it. Answers with the
// same size Decode uses, so the two can never disagree.
func Describe(raw []byte) (w, h int, textDropped bool) {
	vw, vh := intrinsic(raw)
	return int(math.Round(vw)), int(math.Round(vh)), hasText(raw)
}

// intrinsic is the document's own size, falling back to the size a browser gives
// a replaced element that declares none.
func intrinsic(raw []byte) (w, h float64) {
	w, h = rootSize(raw)
	if w <= 0 || h <= 0 {
		return defaultW, defaultH
	}
	return w, h
}

// parse reads the document, retrying once on a sanitised copy: files exported by
// Illustrator declare their own XML entities, which Go's strict XML parser
// rejects outright, and dropping the declarations costs nothing we draw.
func parse(raw []byte, currentColor string) (*oksvg.SvgIcon, error) {
	icon, err := oksvg.ReadReplacingCurrentColor(bytes.NewReader(raw), currentColor, oksvg.IgnoreErrorMode)
	if err == nil {
		return icon, nil
	}
	icon, err2 := oksvg.ReadReplacingCurrentColor(bytes.NewReader(stripEntities(raw)), currentColor, oksvg.IgnoreErrorMode)
	if err2 != nil {
		return nil, fmt.Errorf("parse svg: %w", err)
	}
	return icon, nil
}

// fit scales the drawing to fill a maxW×maxH box without distorting it or
// exceeding maxPixels. A small icon is scaled up on purpose — it is vector art,
// and the placement will not enlarge it later.
func fit(vw, vh float64, maxW, maxH int) (w, h int) {
	if vw <= 0 || vh <= 0 {
		return 1, 1
	}
	scale := math.Min(float64(maxW)/vw, float64(maxH)/vh)
	w = int(math.Round(vw * scale))
	h = int(math.Round(vh * scale))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if w*h > maxPixels {
		shrink := math.Sqrt(float64(maxPixels) / float64(w*h))
		w = max(1, int(float64(w)*shrink))
		h = max(1, int(float64(h)*shrink))
	}
	return w, h
}

var dAttrRe = regexp.MustCompile(`(?s)(\sd\s*=\s*)("[^"]*"|'[^']*')`)

// normalizePathAttrs rewrites every path's data into the one-set-per-command
// form the rasteriser handles correctly. See path.go for why.
func normalizePathAttrs(raw []byte) []byte {
	return dAttrRe.ReplaceAllFunc(raw, func(m []byte) []byte {
		i := bytes.IndexAny(m, `"'`)
		if i < 0 || len(m) < i+2 {
			return m
		}
		quote, val := m[i], string(m[i+1:len(m)-1])
		if !looksLikePathData(val) {
			return m
		}
		out := make([]byte, 0, len(m)+len(m)/4)
		out = append(out, m[:i+1]...)
		out = append(out, normalizePath(val)...)
		return append(out, quote)
	})
}

var (
	doctypeRe = regexp.MustCompile(`(?s)<!DOCTYPE[^\[>]*(\[.*?\])?\s*>`)
	entityRe  = regexp.MustCompile(`&[A-Za-z_][A-Za-z0-9_.:-]*;`)
)

// stripEntities drops an internal DTD and any entity reference it defined, so a
// document carrying custom entities parses. The five XML built-ins stay.
func stripEntities(raw []byte) []byte {
	out := doctypeRe.ReplaceAll(raw, nil)
	return entityRe.ReplaceAllFunc(out, func(m []byte) []byte {
		switch string(m) {
		case "&amp;", "&lt;", "&gt;", "&quot;", "&apos;":
			return m
		}
		return nil
	})
}

// hasText reports whether the document draws text, which we do not render. A
// substring test rather than a parse: it only has to be right about whether to
// warn, and erring towards warning is the safe direction.
func hasText(raw []byte) bool {
	return bytes.Contains(raw, []byte("<text")) || bytes.Contains(raw, []byte("<tspan")) ||
		bytes.Contains(raw, []byte(":text"))
}

// rootSize reads the root element's own size: its viewBox if it has one, else
// its width and height. Returns zeroes when the document declares neither in
// absolute terms (a percentage size has no intrinsic length).
func rootSize(raw []byte) (w, h float64) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return 0, 0
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "svg" {
			return 0, 0 // the root is not an <svg>; nothing to read
		}
		var vbW, vbH, aw, ah float64
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "viewBox":
				if f := strings.FieldsFunc(a.Value, isSepRune); len(f) == 4 {
					vbW, _ = strconv.ParseFloat(f[2], 64)
					vbH, _ = strconv.ParseFloat(f[3], 64)
				}
			case "width":
				aw = parseLength(a.Value)
			case "height":
				ah = parseLength(a.Value)
			}
		}
		if vbW > 0 && vbH > 0 {
			return vbW, vbH
		}
		return aw, ah
	}
}

func isSepRune(r rune) bool {
	return r == ' ' || r == ',' || r == '\t' || r == '\n' || r == '\r'
}

// unitsPerPx converts the absolute CSS units a width/height may carry into
// pixels, at the 96dpi the SVG spec assumes.
var unitsPerPx = []struct {
	suffix string
	factor float64
}{
	{"px", 1}, {"pt", 96.0 / 72}, {"pc", 16}, {"mm", 96 / 25.4}, {"cm", 96 / 2.54}, {"in", 96},
}

// parseLength reads a width/height. A percentage is not a length — it is a
// fraction of a container we do not have — so it reads as no size at all.
func parseLength(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, "%") {
		return 0
	}
	for _, u := range unitsPerPx {
		if rest, ok := strings.CutSuffix(s, u.suffix); ok {
			v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
			if err != nil {
				return 0
			}
			return v * u.factor
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
