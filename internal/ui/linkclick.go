package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"
)

// Clicking a link: when mouse reporting is on (View requests MouseModeAllMotion,
// see view.go) the terminal hands plain clicks to us instead of opening the OSC 8
// hyperlink itself, so a click on a rendered link would do nothing. We close that
// gap here — a no-drag click in the message / thread transcript is mapped to the
// URL under it (linkAt) and opened, mirroring what the terminal does natively when
// mouse reporting is off. http(s) targets open straight away; any other scheme is
// gated behind a warning modal first, since handing e.g. a file: or custom-scheme
// URL to the OS launcher can do more than open a browser tab.

// linkConfirmState is the warning modal shown before opening a clicked link whose
// scheme isn't http(s). It owns every keystroke while active (dispatched in
// update.go before the focus routing) and overlays the screen (view.go).
type linkConfirmState struct {
	active bool
	url    string
}

// isWebURL reports whether u is an http(s) URL — the only schemes opened without
// a confirmation prompt. Everything else (mailto:, file:, tel:, custom app
// schemes, a bare relative target, …) is handed to the OS only after the user
// confirms in the warning modal.
func isWebURL(u string) bool {
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// activateLink opens a clicked link, gating a non-web scheme behind the warning
// modal — the mouse-click entry point to openTarget.
func (m Model) activateLink(url string) (tea.Model, tea.Cmd) {
	return m, m.openTarget(openable{name: url, url: url})
}

// openTarget opens one openable, the single place link-scheme policy lives.
// Attachments (a downloaded local file) and http(s) URLs open straight away via
// the OS handler; any other URL scheme — mailto:/file:/tel:/a custom app scheme
// — raises the warning modal first, since handing such a target to the launcher
// can do more than open a browser tab. Used by both the mouse click (activateLink)
// and the keyboard `o` / open-picker paths (openpicker.go).
func (m *Model) openTarget(o openable) tea.Cmd {
	if o.file == nil && !isWebURL(o.url) {
		m.linkConfirm = linkConfirmState{active: true, url: o.url}
		return nil
	}
	m.status = "opening " + o.name + "…"
	return m.openOpenable(o)
}

// handleLinkConfirmKey owns every keystroke while the link warning is open:
// y/enter opens the link, n/esc dismisses it.
func (m Model) handleLinkConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "y", "Y", "enter":
		url := m.linkConfirm.url
		m.linkConfirm = linkConfirmState{}
		m.status = "opening " + url + "…"
		return m, m.openOpenable(openable{name: url, url: url})
	case "n", "N", "esc":
		m.linkConfirm = linkConfirmState{}
		return m, nil
	}
	return m, nil
}

// renderLinkConfirm draws the centred warning shown before a non-web link is
// handed to the OS. Mirrors the GitLab/delete confirm dialogs but flags the
// scheme in warning yellow.
func (m *Model) renderLinkConfirm() string {
	if !m.linkConfirm.active {
		return ""
	}
	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 30 {
		outerW = 30
	}
	inner := outerW - 8 // border (2) + padding (6: 3 each side)
	if inner < 1 {
		inner = 1
	}

	centred := lipgloss.NewStyle().Width(inner).Align(lipgloss.Center)
	header := centred.Bold(true).Render("Open this link?")
	warn := centred.Foreground(lipgloss.Color("3")).Render("⚠ not a web (http/https) address")
	target := centred.Foreground(dimColor).Italic(true).Render(ansi.Truncate(m.linkConfirm.url, inner, "…"))
	hint := centred.Foreground(dimColor).Italic(true).Render("y open · n cancel")

	body := lipgloss.JoinVertical(lipgloss.Center, header, warn, target, "", hint)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("3")).
		Padding(1, 3).
		Render(body)
}

// linkAt returns the OSC 8 hyperlink URL at content coordinate (line, col) in the
// given pane's rendered transcript, if a link covers that cell. The rendered lines
// already carry each link's URL as an OSC 8 escape (osc8Link), so a click resolves
// to a URL straight off the painted output — no re-parsing of the post body, and
// it works for markdown [text](url) links whose URL isn't in the visible text.
//
// A link that wraps is hard-wrapped (wrapBodyLine) into several logical lines: the
// OSC 8 open sits on the first, the close on the last, and the continuation rows
// carry no marker — the terminal keeps the hyperlink open across them.
//
// Runs on every mouse motion (hover), so it avoids replaying the whole transcript:
// most rows resolve from their own markers in one scan. Only a miss — possibly a
// wrapped continuation row — pays to find the carried-in state, and even then just
// scans back to the nearest row that holds any marker. That row's last marker
// fixes its end state regardless of what preceded it (each marker overwrites the
// active link), and the marker-less rows between it and here preserve that state.
func (m *Model) linkAt(pane focus, line, col int) (string, bool) {
	width := m.msgsView.Width()
	switch pane {
	case focusThread:
		width = m.threadView.Width()
	case focusRef:
		width = m.refView.Width()
	case focusSQLResults:
		width = m.sql.view.Width()
	}
	lines, _ := m.ensureWrapIndex(pane, width)
	if line < 0 || line >= len(lines) {
		return "", false
	}
	if url, ok, _ := linkScan(lines[line], col, ""); ok {
		return url, true
	}
	carried := ""
	for i := line - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "\x1b]8;;") {
			_, _, carried = linkScan(lines[i], -1, "")
			break
		}
	}
	if carried == "" { // no open link carried in: the cell isn't on a link
		return "", false
	}
	url, ok, _ := linkScan(lines[line], col, carried)
	return url, ok
}

// linkAtDisplayCol reports the OSC 8 hyperlink URL whose visible run covers
// display column target in a single rendered line, with no hyperlink carried in
// from an earlier wrapped row.
func linkAtDisplayCol(line string, target int) (string, bool) {
	url, ok, _ := linkScan(line, target, "")
	return url, ok
}

// linkScan walks one rendered line's OSC 8 markers, starting from the carried-in
// URL `in` (non-empty when a hyperlink opened on an earlier wrapped row is still
// active here — its continuation rows hold no open marker of their own). Runs
// between markers are measured with lipgloss.Width so wide glyphs and inner SGR
// styling stay aligned with the click column (target, 0-based). When target >= 0
// and a hyperlink covers it, found/ok report that URL; out is the hyperlink state
// at the line's end, to carry to the next wrapped row. A negative target only
// computes out.
func linkScan(line string, target int, in string) (found string, ok bool, out string) {
	const open = "\x1b]8;;"
	col := 0
	cur := in // URL active for the current run, "" when none
	rest := line
	for {
		i := strings.Index(rest, open)
		seg := rest
		if i >= 0 {
			seg = rest[:i]
		}
		if w := lipgloss.Width(seg); w > 0 {
			if !ok && target >= 0 && cur != "" && target >= col && target < col+w {
				found, ok = cur, true
			}
			col += w
		}
		if i < 0 {
			return found, ok, cur
		}
		rest = rest[i+len(open):]
		end, adv := osc8URLEnd(rest)
		cur = rest[:end] // empty on the close marker (\x1b]8;;\x1b\\)
		rest = rest[adv:]
	}
}

// hoverLink identifies the link the pointer is over: the post that owns it, the
// target URL, and which transcript pane it's in. The zero value (empty url) means
// nothing is hovered. Comparable, so a motion event can cheaply detect a change.
type hoverLink struct {
	postID string
	url    string
	pane   focus
}

// hoverLinkAt resolves the pointer to the link under it in the message or thread
// transcript, or the zero hoverLink over anything else. Mirrors handleMouseClick's
// content-coordinate path (hitTest → linkAt) but for a button-less move.
func (m *Model) hoverLinkAt(x, y int) hoverLink {
	h := m.hitTest(x, y)
	var pane focus
	var posts []*model.Post
	switch h.zone {
	case hitMessage:
		pane, posts = focusMessages, m.posts
	case hitThread:
		pane, posts = focusThread, m.threadPosts
	case hitRef:
		// The reference panel is a single document with no owning post, so the
		// hover is keyed by pane alone (postID stays empty).
		if url, ok := m.linkAt(focusRef, h.line, h.col); ok {
			return hoverLink{url: url, pane: focusRef}
		}
		return hoverLink{}
	case hitSQL:
		pane, posts = focusSQLResults, m.sql.posts
	default:
		return hoverLink{}
	}
	if h.idx < 0 || h.idx >= len(posts) {
		return hoverLink{}
	}
	url, ok := m.linkAt(pane, h.line, h.col)
	if !ok {
		return hoverLink{}
	}
	return hoverLink{postID: posts[h.idx].Id, url: url, pane: pane}
}

// setHoverLink installs a new hovered link, re-rendering only the transcript
// pane(s) whose highlight could have changed (the old link's pane to drop its
// highlight, the new link's pane to paint it). A no-op when unchanged, so a move
// within one link doesn't re-render. The footer reads m.hoverLink directly.
func (m *Model) setHoverLink(hl hoverLink) {
	if m.hoverLink == hl {
		return
	}
	old := m.hoverLink
	m.hoverLink = hl
	oldRendered := false
	if old.url != "" {
		m.renderHoverPane(old.pane)
		oldRendered = old.pane == hl.pane
	}
	if hl.url != "" && !oldRendered {
		m.renderHoverPane(hl.pane)
	}
}

// renderHoverPane re-renders one transcript pane so a hover-highlight change takes
// effect (markdownBody paints the hovered post's link; the rest are cache hits).
func (m *Model) renderHoverPane(pane focus) {
	switch pane {
	case focusThread:
		m.renderThread()
	case focusRef:
		m.renderRef()
	case focusSQLResults:
		m.renderSQLResults()
	default:
		m.renderMessages()
	}
}

// highlightLink restyles every occurrence of the OSC 8 link to url in body — the
// unwrapped markdown body, where each link's open/inner/close is still contiguous
// — with style, keeping the hyperlink markers so the link stays clickable and
// keeps the same display width (wrapping happens afterwards). This is the
// OSC 8-safe alternative to lipgloss.StyleRanges, which would split the hyperlink.
func highlightLink(body, url string, style lipgloss.Style) string {
	open := "\x1b]8;;" + url + "\x1b\\"
	const closeSeq = "\x1b]8;;\x1b\\"
	var b strings.Builder
	rest := body
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		rest = rest[i+len(open):]
		j := strings.Index(rest, closeSeq)
		if j < 0 { // malformed (no close): leave the remainder untouched
			b.WriteString(open)
			b.WriteString(rest)
			break
		}
		b.WriteString(osc8Link(url, style.Render(ansi.Strip(rest[:j]))))
		rest = rest[j+len(closeSeq):]
	}
	return b.String()
}

// osc8URLEnd finds the OSC 8 string terminator (ST "ESC \" or BEL) that follows
// the hyperlink's URL in s, returning the URL's end index and the index to resume
// scanning past the terminator. An unterminated sequence treats the rest as URL.
func osc8URLEnd(s string) (end, adv int) {
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\x07':
			return i, i + 1
		case s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\':
			return i, i + 2
		}
	}
	return len(s), len(s)
}
