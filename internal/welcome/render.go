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
	p.add(func(grid [][]cell, x, y, w int) { drawInput(grid, x, y, w, f, focused, placeholder, false) })
}

// secret adds a single-line input row that masks its contents — for passwords.
func (p *panelBuilder) secret(f *textField, focused bool, placeholder string) {
	p.add(func(grid [][]cell, x, y, w int) { drawInput(grid, x, y, w, f, focused, placeholder, true) })
}

// button adds a focusable action button row (e.g. "Open GitLab SSO").
func (p *panelBuilder) button(label string, focused bool) {
	p.add(func(grid [][]cell, x, y, w int) { drawButton(grid, x, y, w, label, focused) })
}

// textU adds one clipped line in fg, underlining the first occurrence of the
// word `u` — used to point at the copyable link in the SSO snippet.
func (p *panelBuilder) textU(s, u string, fg vapor.RGB) {
	p.add(func(grid [][]cell, x, y, w int) { drawTextUnderlined(grid, x, y, clip(s, w), u, fg) })
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
	if m.cfg.TelemetryEnabled() {
		p.wrap("Anonymous telemetry is on — thank you. Turn it off any time with "+
			"`telemetry.enabled: false` in your config.", labelC)
	} else {
		p.wrap("Telemetry is off. Nothing is reported.", labelC)
	}
	p.blank()
	p.wrap("Settings saved. Launch Matterbox any time by running `matterbox`.", labelC)
	p.blank()

	if m.demo {
		p.text("                     ✦ Music by Dubmood ✦", labelC)
		p.blank()
		p.text("enter  exit        space  hide (keep the demo playing)", dimC)
	} else {
		p.text("enter  exit", dimC)
	}
	m.drawPanel(grid, p, panelW)
}

// wizardSteps is how many steps the headings count off (stepSub).
const wizardSteps = 4

// stepSub is the sub-heading for step n ("Step 2 of 4 · Authentication").
func stepSub(n int, name string) string {
	return fmt.Sprintf("Step %d of %d · %s", n, wizardSteps, name)
}

// buildStep fills p with the rows for the active wizard step.
func (m *Model) buildStep(p *panelBuilder) {
	switch m.step {
	case stepServer:
		p.heading("✦ Welcome to Matterbox", stepSub(1, "Server"))
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
		p.heading("✦ Sign in", stepSub(2, "Authentication"))
		p.blank()
		p.text("Username or email", dimC)
		p.field(&m.user, m.authFocus == authFocusUser && !m.validating, "you@example.com")
		p.text("Password", dimC)
		p.secret(&m.password, m.authFocus == authFocusPassword && !m.validating, "••••••••")
		if m.mfaRequired {
			p.text("Two-factor code", dimC)
			p.field(&m.mfa, m.authFocus == authFocusMFA && !m.validating, "123456")
		}
		p.blank()
		p.text("— or use single sign-on —", dimC)
		p.button("Open GitLab SSO in your browser", m.authFocus == authFocusSSO)
		p.wrap("On the success page, copy the link from the message:", labelC)
		p.textU(`  "please click the link"`, "link", accentCyan)
		p.text("Token or mmauth:// link", dimC)
		p.field(&m.token, m.authFocus == authFocusToken && !m.validating, "paste here, or leave blank to skip…")
		if m.authMsg != "" {
			c := accentCyan
			if m.authErr {
				c = badC
			}
			p.wrap(m.authMsg, c)
		}
		p.blank()
		p.text("↑↓ move    enter  select    esc  back", dimC)

	case stepAdvanced:
		p.heading("✦ Preferences", stepSub(3, "Advanced"))
		p.blank()
		p.add(m.advRow("Mark read after", fmt.Sprintf("%ds", m.adv.markRead), accentCyan, 0))
		p.add(m.advRow("SQL tab", onoff(m.adv.sqlTab), chipColor(m.adv.sqlTab), 1))
		p.add(m.advRow("Mouse support", onoff(m.adv.mouse), chipColor(m.adv.mouse), 2))
		p.add(m.advRow("Image thumbnails", onoff(m.adv.thumbnails), chipColor(m.adv.thumbnails), 3))
		p.add(m.advRow("Animations", onoff(m.adv.animations), chipColor(m.adv.animations), 4))
		p.add(m.advRow("Ctrl+arrow navigation", onoff(m.adv.ctrlArrow), chipColor(m.adv.ctrlArrow), 5))
		p.add(m.advRow("Code theme", m.currentThemeName(), accentCyan, 6))
		p.blank()
		p.wrap(m.advHint(), dimC)
		p.blank()
		m.addCodePreview(p)
		p.blank()
		p.text("↑↓ move   ←→/space change   enter  continue   esc  back", dimC)

	case stepTelemetry:
		p.heading("✦ Help improve Matterbox", stepSub(4, "Telemetry"))
		p.blank()
		p.wrap("May we collect anonymous usage telemetry and error reports?", valueC)
		p.blank()
		// Says what is sent rather than asserting "it is anonymous", and names
		// the one thing that isn't in our gift: PostHog sees the sender's IP.
		// docs/telemetry.md has always said so, but this is the moment someone
		// decides, and the caveat belongs beside the claim it qualifies.
		//
		// The wording is tight because the row budget is: at 60x20 this step
		// fills the panel exactly, and drawPanel clips from the bottom, so a
		// longer sentence takes the answers off the screen rather than
		// wrapping. TestTelemetryStepFitsSmallTerminal is the guard.
		p.wrap("A random id, never your name, your server, or anything you "+
			"type — just what gets used and what breaks. PostHog does see "+
			"the IP it arrives from.", labelC)
		p.blank()
		p.text("What we collect, and how to turn it off later:", dimC)
		p.textU("  "+telemetryDocsURL, telemetryDocsURL, accentCyan)
		// No blank before the buttons: the step has to keep its two answers AND
		// the key hint inside an 80x24 — and a row shorter than that — panel,
		// which drawPanel clips from the bottom.
		p.button("Yes, share anonymous telemetry", m.telemetryFocus == telemetryFocusYes)
		p.button("No thanks", m.telemetryFocus == telemetryFocusNo)
		// Same channel the other steps use for a failed write: without this the
		// answer would appear to be ignored when the config can't be saved.
		if m.authMsg != "" {
			p.wrap(m.authMsg, badC)
		}
		p.blank()
		p.text("↑↓ move   enter  choose   esc  back", dimC)
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
		return "Draw image attachments right in the transcript, no need to press space. Needs a Kitty/Ghostty-class terminal."
	case 4:
		return "Animate GIF custom emoji, image previews and thumbnails."
	case 5:
		return "Switch team/channel with ctrl+arrow keys (off frees them for word-jump)."
	case 6:
		return fmt.Sprintf("Syntax-highlight palette for code blocks — ←→ cycles %d themes.", len(m.themeNames))
	}
	return ""
}

// addCodePreview appends a label and the syntax-highlighted snippet for the
// currently-selected code theme, so cycling themes shows their colours live.
func (m *Model) addCodePreview(p *panelBuilder) {
	bg, width, lines := buildCodePreview(m.currentThemeName())
	p.text("Preview", dimC)
	for _, segs := range lines {
		segs := segs
		p.add(func(grid [][]cell, x, y, w int) { drawCodeLine(grid, x, y, w, bg, segs, width) })
	}
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
// When mask is set the typed runes draw as bullets (the placeholder still shows
// in clear) so passwords don't appear on screen.
func drawInput(grid [][]cell, x, y, w int, f *textField, focused bool, placeholder string, mask bool) {
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
			if mask {
				r = '•'
			}
		}
		cbg := bg
		if focused && idx == cursor {
			cbg, fg = cursorBg, cursorFg
		}
		setCell(grid, x+i, y, cell{R: r, Fg: fg, Bg: cbg, HasBg: true})
	}
}

// drawButton renders a bracketed action button. Focused, it gets a filled
// high-contrast bar (the cursor palette) and a "›" marker so it clearly reads as
// the active control; otherwise it sits quietly as a dim chip. Both states keep
// a 2-cell left prefix so the button doesn't shift when focus moves.
func drawButton(grid [][]cell, x, y, w int, label string, focused bool) {
	if w <= 0 {
		return
	}
	marker := "  "
	fg, bg := labelC, fieldBg
	if focused {
		marker = "› "
		fg, bg = cursorFg, cursorBg
	}
	for i, r := range []rune(clip(marker+"[ "+label+" ]", w)) {
		setCell(grid, x+i, y, cell{R: r, Fg: fg, Bg: bg, HasBg: true})
	}
}

// drawTextUnderlined writes s in fg over the existing background, underlining the
// first occurrence of sub. Used for the SSO success-page snippet so the word the
// user must copy ("link") stands out.
func drawTextUnderlined(grid [][]cell, x, y int, s, sub string, fg vapor.RGB) {
	lo, hi := -1, -1
	if sub != "" {
		if idx := strings.Index(s, sub); idx >= 0 {
			lo = len([]rune(s[:idx]))
			hi = lo + len([]rune(sub))
		}
	}
	for i, r := range []rune(s) {
		cx := x + i
		if !inBounds(grid, cx, y) {
			continue
		}
		under := grid[y][cx]
		under.R = r
		under.Fg = fg
		under.Underline = i >= lo && i < hi
		grid[y][cx] = under
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
