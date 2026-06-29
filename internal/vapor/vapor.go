// Package vapor is a self-contained port of the vaporascii renderer: it draws
// the driving-through-neon-mountains "vaporwave" scene — a scan-lined sun, a
// faceted cyan wireframe terrain, twinkling stars, and an optional extruded 3D
// title — into a grid of terminal cells. It carries no terminal I/O of its own;
// a caller (e.g. the welcome wizard) renders frames and composites its own UI on
// top before printing. Adapted from github-local vaporascii (color/scene/
// render3d/text3d/glyph-fit pipeline) with the CLI, PNG, and terminal-control
// code dropped and the presenters reworked to emit cells instead of ANSI.
package vapor

// TextOpts configures the floating extruded 3D title. The zero value (empty
// Text) disables it. X/Y are the title's centre in world units and Z is its
// depth ahead of the camera; Scale sizes it, Depth extrudes it, and the Rot/Spin
// triples rotate/animate it about its own centre (degrees, degrees-per-second).
type TextOpts struct {
	Text                string
	X, Y, Z             float64
	Scale, Depth        float64
	RotX, RotY, RotZ    float64
	SpinX, SpinY, SpinZ float64
	Stops               []RGB // vertical colour gradient, top row to bottom (empty = built-in cyan→pink)
	Demo                bool  // bob each letter up and down on a per-letter sine wave
}

// Options configures a Renderer. Mode/Coverage default to "glyph"/"octant";
// the remaining scene knobs mirror the vaporascii flags of the same name and
// have no implicit defaults — a caller sets them explicitly (Height 0 renders a
// flat road, Speed 0 stops the drive, etc.).
type Options struct {
	Mode         string  // "glyph" (default), "blocks", or "ascii"
	Coverage     string  // glyph candidate set: "quad", "sextant", or "octant" (default)
	Speed        float64 // scroll-speed multiplier
	Height       float64 // mountain-height multiplier
	Valley       float64 // valley-width multiplier
	ValleyHeight float64 // valley-floor undulation multiplier
	SunY         float64 // sun elevation multiplier (1 = default, 0 = on the horizon)
	SunStops     []RGB   // sun gradient, top to bottom (empty = built-in magenta)
	Text         *TextOpts
	Anim         *Animation
}

// Renderer holds a Scene sized to a terminal and renders frames into a reused
// cell grid. Resize must be called (with the terminal's cell dimensions) before
// the first Render.
type Renderer struct {
	opts     Options
	families []glyphFamily
	scene    *Scene
	buf      []RGB
	cols     int // terminal columns
	rows     int // terminal rows
	w, h     int // sub-pixel framebuffer dimensions
	grid     [][]Cell
}

// New builds a Renderer from opts. Call Resize before rendering.
func New(opts Options) *Renderer {
	if opts.Mode == "" {
		opts.Mode = "glyph"
	}
	if opts.Coverage == "" {
		opts.Coverage = "octant"
	}
	return &Renderer{opts: opts, families: coverageFamilies(opts.Coverage)}
}

// Cols and Rows report the current terminal cell dimensions.
func (r *Renderer) Cols() int { return r.cols }
func (r *Renderer) Rows() int { return r.rows }

// Resize sizes the scene to a terminal of cols×rows cells, (re)allocating the
// framebuffer and output grid. Tiny sizes are clamped so projection math stays
// well-defined.
func (r *Renderer) Resize(cols, rows int) {
	if cols < 10 {
		cols = 10
	}
	if rows < 6 {
		rows = 6
	}
	if cols == r.cols && rows == r.rows && r.scene != nil {
		return
	}
	r.cols, r.rows = cols, rows
	cdiv, rdiv := subdiv(r.opts.Mode)
	r.w, r.h = cols*cdiv, rows*rdiv

	if r.scene == nil {
		r.scene = NewScene(r.w, r.h, aspectY(r.opts.Mode), r.opts.Speed, r.opts.Height, r.opts.Valley, r.opts.ValleyHeight, r.opts.SunStops)
		r.scene.SetText(r.opts.Text.toInternal())
		r.scene.applySunY(r.opts.SunY)
		r.scene.SetAnimation(r.opts.Anim)
	} else {
		r.scene.Resize(r.w, r.h)
	}

	r.buf = make([]RGB, r.w*r.h)
	r.grid = make([][]Cell, rows)
	for i := range r.grid {
		r.grid[i] = make([]Cell, cols)
	}
}

// Render paints the scene at time t (seconds) and returns the cell grid
// (indexed [row][col]). The grid is reused between calls — copy it (or serialize
// it) before the next Render if you need to retain it. Callers may overwrite
// cells (e.g. to composite an overlay) before passing the grid to Serialize.
func (r *Renderer) Render(t float64) [][]Cell {
	r.scene.Render(r.buf, t)
	switch r.opts.Mode {
	case "blocks":
		presentBlocksCells(r.grid, r.buf, r.w, r.rows)
	case "ascii":
		presentAsciiCells(r.grid, r.buf, r.w, r.rows)
	default:
		presentGlyphCells(r.grid, r.buf, r.w, r.rows, r.families)
	}
	return r.grid
}

// Frame renders the scene at time t directly to a printable ANSI string.
func (r *Renderer) Frame(t float64) string { return serialize(r.Render(t)) }

// Serialize turns a (possibly overlaid) cell grid into a printable ANSI string.
func Serialize(grid [][]Cell) string { return serialize(grid) }

// ParseHexStops parses a comma-separated list of "#rrggbb" / "#rgb" colours into
// gradient stops (top to bottom), matching vaporascii's -sun-color / -text-color
// flags.
func ParseHexStops(s string) ([]RGB, error) { return parseSunStops(s) }

func (t *TextOpts) toInternal() *textOpts {
	if t == nil || t.Text == "" {
		return nil
	}
	return &textOpts{
		runes: []rune(t.Text),
		x:     t.X, y: t.Y, z: t.Z,
		scale: t.Scale, depth: t.Depth,
		rotX: t.RotX, rotY: t.RotY, rotZ: t.RotZ,
		spinX: t.SpinX, spinY: t.SpinY, spinZ: t.SpinZ,
		stops: t.Stops,
		demo:  t.Demo,
	}
}

// subdiv reports how many sub-pixels a cell is split into per mode: columns ×
// rows. ascii is 1×1, blocks is 1×2 (half-blocks), glyph is 2×12 (the LCM grid
// the quadrant/sextant/octant families all tile evenly).
func subdiv(mode string) (cols, rows int) {
	switch mode {
	case "blocks":
		return 1, 2
	case "glyph":
		return glyphCols, glyphRows
	default: // ascii
		return 1, 1
	}
}

// aspectY squashes the projection vertically so sub-pixels stay roughly square.
// A terminal cell is ~twice as tall as wide; a sub-pixel's aspect is that scaled
// by the rows/cols of subdivision. ascii→0.5, blocks→1.0, glyph→3.0.
func aspectY(mode string) float64 {
	c, r := subdiv(mode)
	return 0.5 * float64(r) / float64(c)
}
