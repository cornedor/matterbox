package welcome

import (
	"fmt"
	"strings"

	"matterbox/internal/vapor"
)

// rowFn draws one content row of a panel at (x,y) within content width w.
type rowFn func(grid [][]cell, x, y, w int)

// panelBuilder accumulates content rows; the panel's height is derived from how
// many it holds, so steps just append and the frame sizes itself. w is the
// content width, fixed before building so wrapping/clipping can use it.
type panelBuilder struct {
	w    int
	rows []rowFn
}

func (p *panelBuilder) add(fn rowFn) { p.rows = append(p.rows, fn) }
func (p *panelBuilder) blank()       { p.add(func([][]cell, int, int, int) {}) }

// text adds one clipped line in fg, keeping the panel's translucent background.
func (p *panelBuilder) text(s string, fg vapor.RGB) {
	p.add(func(grid [][]cell, x, y, w int) { drawTextOverlay(grid, x, y, clip(s, w), fg) })
}

// wrap adds word-wrapped lines of s in fg.
func (p *panelBuilder) wrap(s string, fg vapor.RGB) {
	for _, ln := range wrapText(s, p.w) {
		p.text(ln, fg)
	}
}

// heading adds a bright title row and a dim cyan sub-label row.
func (p *panelBuilder) heading(title, sub string) {
	p.text(title, titleC)
	p.text(sub, accentCyan)
}

// field adds a single-line input row for f.
func (p *panelBuilder) field(f *textField, focused bool, placeholder string) {
	p.add(func(grid [][]cell, x, y, w int) { drawInput(grid, x, y, w, f, focused, placeholder) })
}

// drawWizard composites the current wizard step's panel onto the background.
func (m *Model) drawWizard(grid [][]cell) {
	panelW := panelWidth(m.width)
	if panelW < 18 {
		return // too narrow to draw anything useful
	}
	p := &panelBuilder{w: panelW - 4}
	m.buildStep(p)
	m.drawPanel(grid, p, panelW)
}

// drawDone composites the closing summary panel.
func (m *Model) drawDone(grid [][]cell) {
	panelW := panelWidth(m.width)
	if panelW < 18 {
		return
	}
	p := &panelBuilder{w: panelW - 4}
	p.heading("✓ You're all set", "Setup complete")
	p.blank()
	p.text("Server", dimC)
	p.text(clip(m.cfg.ServerURL, p.w), valueC)
	p.blank()
	if m.authOK {
		p.text("Signed in as "+m.authUser, goodC)
	} else {
		p.wrap("Not signed in — run `matterbox login` when you're ready.", labelC)
	}
	p.blank()
	p.wrap("Settings saved. Launch Matterbox any time by running `matterbox`.", labelC)
	p.blank()
	p.text("enter  exit", dimC)
	m.drawPanel(grid, p, panelW)
}

// buildStep fills p with the rows for the active wizard step.
func (m *Model) buildStep(p *panelBuilder) {
	switch m.step {
	case stepServer:
		p.heading("✦ Welcome to Matterbox", "Step 1 of 3 · Server")
		p.blank()
		p.wrap("A Mattermost client for your terminal. Let's connect to your server — it only takes a minute.", labelC)
		p.blank()
		p.text("Server URL", dimC)
		p.field(&m.server, true, "https://mattermost.example.com")
		if m.serverMsg != "" {
			p.text(m.serverMsg, badC)
		}
		p.blank()
		p.text("enter  continue        esc  quit", dimC)

	case stepAuth:
		p.heading("✦ Sign in", "Step 2 of 3 · Authentication")
		p.blank()
		p.wrap("Open GitLab SSO, then paste the link from the success page (or a personal access token). Leave blank to skip and sign in later.", labelC)
		p.blank()
		p.text("Token or mmauth:// link", dimC)
		p.field(&m.token, !m.validating, "paste here…")
		if m.authMsg != "" {
			c := accentCyan
			if m.authErr {
				c = badC
			}
			p.wrap(m.authMsg, c)
		}
		p.blank()
		p.text("ctrl+o  browser    enter  verify / skip    esc  back", dimC)

	case stepAdvanced:
		p.heading("✦ Preferences", "Step 3 of 3 · Advanced")
		p.blank()
		p.add(m.advRow("Mark read after", fmt.Sprintf("%ds", m.adv.markRead), accentCyan, 0))
		p.add(m.advRow("SQL tab", onoff(m.adv.sqlTab), chipColor(m.adv.sqlTab), 1))
		p.add(m.advRow("Mouse support", onoff(m.adv.mouse), chipColor(m.adv.mouse), 2))
		p.add(m.advRow("Animations", onoff(m.adv.animations), chipColor(m.adv.animations), 3))
		p.add(m.advRow("Ctrl+arrow navigation", onoff(m.adv.ctrlArrow), chipColor(m.adv.ctrlArrow), 4))
		p.blank()
		p.wrap(m.advHint(), dimC)
		p.blank()
		p.text("↑↓ move   ←→/space change   enter  finish   esc  back", dimC)
	}
}

// advRow renders one advanced setting: a focus marker, the label, and a
// right-aligned value chip.
func (m *Model) advRow(label, value string, chipC vapor.RGB, idx int) rowFn {
	focused := m.adv.focus == idx
	return func(grid [][]cell, x, y, w int) {
		marker, lblC := "  ", labelC
		if focused {
			marker, lblC = "› ", valueC
		}
		drawTextOverlay(grid, x, y, marker+label, lblC)
		chip := "[ " + value + " ]"
		vx := x + w - len([]rune(chip))
		if minX := x + len([]rune(marker+label)) + 1; vx < minX {
			vx = minX
		}
		drawTextOverlay(grid, vx, y, chip, chipC)
	}
}

// advHint describes the focused advanced field.
func (m *Model) advHint() string {
	switch m.adv.focus {
	case 0:
		return "Seconds a channel must stay open before it's marked read (0 = instantly)."
	case 1:
		return "Show the read-only SQL tab over your local message cache."
	case 2:
		return "Wheel-scroll and click to navigate; off keeps native terminal selection."
	case 3:
		return "Animate GIF custom emoji and image previews."
	case 4:
		return "Switch team/channel with ctrl+arrow keys (off frees them for word-jump)."
	}
	return ""
}

// drawPanel sizes, fills, frames, and renders a built panel, centred (and nudged
// slightly down so the flying title peeks above it).
func (m *Model) drawPanel(grid [][]cell, p *panelBuilder, panelW int) {
	panelH := len(p.rows) + 2
	if maxH := m.height - 2; panelH > maxH {
		panelH = maxH
	}
	if panelH < 3 {
		return
	}
	panelX := (m.width - panelW) / 2
	panelY := (m.height-panelH)/2 + 1
	if panelY+panelH > m.height {
		panelY = m.height - panelH
	}
	if panelY < 0 {
		panelY = 0
	}

	fillPanel(grid, panelX, panelY, panelW, panelH, panelBg, 0.86)
	roundedBorder(grid, panelX, panelY, panelW, panelH, borderC, panelBg)

	contentX := panelX + 2
	for i, fn := range p.rows {
		if i >= panelH-2 {
			break
		}
		fn(grid, contentX, panelY+1+i, contentW(panelW))
	}
}

// drawIntroHint floats a faint "press any key to skip" near the bottom during
// the intro, once the title has begun settling in.
func (m *Model) drawIntroHint(grid [][]cell) {
	if m.t < 1.5 {
		return
	}
	hint := "press any key to skip"
	x := (m.width - len([]rune(hint))) / 2
	drawTextOverlay(grid, x, m.height-2, hint, blend(dimC, valueC, 0.35))
}

// drawInput renders f as a single-line input box with a block cursor and simple
// horizontal scroll so the caret stays visible in a field narrower than the text.
func drawInput(grid [][]cell, x, y, w int, f *textField, focused bool, placeholder string) {
	if w <= 0 {
		return
	}
	bg := fieldBg
	if focused {
		bg = fieldFocus
	}
	runes := f.runes
	cursor := f.cursor
	start := 0
	if cursor > w-1 {
		start = cursor - (w - 1)
	}
	empty := len(runes) == 0
	ph := []rune(placeholder)

	for i := 0; i < w; i++ {
		idx := start + i
		r := ' '
		fg := valueC
		switch {
		case empty && i < len(ph):
			r, fg = ph[i], dimC
		case idx < len(runes):
			r = runes[idx]
		}
		cbg := bg
		if focused && idx == cursor {
			cbg, fg = cursorBg, cursorFg
		}
		setCell(grid, x+i, y, cell{R: r, Fg: fg, Bg: cbg, HasBg: true})
	}
}

// panelWidth picks a comfortable panel width for the terminal (capped at 64).
func panelWidth(termW int) int {
	w := termW - 6
	if w > 64 {
		w = 64
	}
	return w
}

func contentW(panelW int) int { return panelW - 4 }

func onoff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func chipColor(b bool) vapor.RGB {
	if b {
		return goodC
	}
	return dimC
}

// clip truncates s to w cells, marking a cut with an ellipsis.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// wrapText greedily word-wraps s to width w, hard-breaking any word longer than w.
func wrapText(s string, w int) []string {
	if w <= 0 {
		return []string{""}
	}
	var lines []string
	line, lineLen := "", 0
	flush := func() { lines = append(lines, line); line, lineLen = "", 0 }
	for _, word := range strings.Fields(s) {
		wl := len([]rune(word))
		for wl > w { // hard-break an over-long word
			if lineLen > 0 {
				flush()
			}
			r := []rune(word)
			lines = append(lines, string(r[:w]))
			word = string(r[w:])
			wl = len([]rune(word))
		}
		switch {
		case lineLen == 0:
			line, lineLen = word, wl
		case lineLen+1+wl <= w:
			line += " " + word
			lineLen += 1 + wl
		default:
			flush()
			line, lineLen = word, wl
		}
	}
	if lineLen > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}
