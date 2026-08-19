package ui

import (
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Mouse interaction beyond the wheel (handleMouseWheel lives in update.go):
// clicking a team tab switches to it, clicking a channel row opens it, clicking
// a message selects it, and dragging within a message selects text that's
// copied on release. On the Search / Feed tabs a click selects the bubble under
// it and a click on the already-selected bubble activates it (opens the channel
// / hit, or expands the load-more row) — a deliberate two-step that keeps a
// stray click from yanking you away mid-scroll. Hovering a tab or channel row
// paints a hover highlight. Everything here is gated by mouseEnabled (View only
// requests mouse reporting then) and stands down behind any modal overlay.

// hitZone identifies which clickable region of the UI a screen cell falls in.
type hitZone int

const (
	hitNone hitZone = iota
	hitTab
	hitChannel
	hitMessage
	hitThread
	hitFeed
	hitSearch
	hitRef
	hitInfo
	hitSQL
	hitComposer
	hitJumpBottom
	hitFeedMarkAll
)

// hit is the result of hitTest. idx's meaning depends on zone: a tab index
// (tabAt / gotoTab), a visible-channel index, or a post index. line/col are the
// content coordinates (logical line + display column) within a message/thread
// viewport, used to seed a text selection; they're 0 for non-text zones.
type hit struct {
	zone hitZone
	idx  int
	line int
	col  int
}

// tabZone is one team tab's horizontal extent on the tab strip: columns
// [x0,x1) map to tab index idx. Recorded by renderTeamTabs (see viewCache).
type tabZone struct {
	x0, x1 int
	idx    int
}

// hoverState is the clickable element the pointer is currently over, painted
// with a hover style. zone is hitNone over nothing clickable. Only hitTab,
// hitChannel and hitJumpBottom are tracked — targets whose hover test is a
// rectangle compare — so per-motion hover never drags the message-pane render
// onto the hot path. (The pill does live in the memoized upper box, but its
// state is in that box's fingerprint, so only crossing its edge re-renders.)
// hitFeedMarkAll is tracked the same way, on its own rectangle.
type hoverState struct {
	zone hitZone
	idx  int
}

// selGran is the unit a transcript text selection snaps to, mirroring the
// editor's: char for a plain drag, word for a double-click, line for a triple.
type selGran int

const (
	granChar selGran = iota
	granWord
	granLine
)

// textSel is a click-drag text selection in the message or thread pane. anchor
// is where the drag began, head where it currently ends, both in (logical line,
// display column) content coordinates. dragging is true while the button is
// held; active turns true once the drag spans a non-empty range, so a bare
// click (mousedown+up with no movement) doesn't paint a zero-width highlight.
//
// gran records whether the selection was started by a double- or triple-click;
// anchorLo/anchorHi then hold the display-column bounds of the word/line the
// click landed on (the fixed anchor unit, both == anchorCol for char
// granularity), so a drag grows the moving end a whole word/line at a time.
type textSel struct {
	pane                  focus
	anchorLine, anchorCol int
	headLine, headCol     int
	gran                  selGran
	anchorLo, anchorHi    int
	dragging              bool
	active                bool
}

// normalized returns the selection's bounds ordered so (l0,c0) precedes
// (l1,c1) in reading order, regardless of drag direction.
func (s textSel) normalized() (l0, c0, l1, c1 int) {
	l0, c0, l1, c1 = s.anchorLine, s.anchorCol, s.headLine, s.headCol
	if l1 < l0 || (l1 == l0 && c1 < c0) {
		l0, c0, l1, c1 = l1, c1, l0, c0
	}
	return l0, c0, l1, c1
}

// wrapCache memoizes one pane's content (the viewport's logical lines) and each
// line's first visual row, keyed by pane + content version + width. A drag
// re-reads it every motion event; rebuilding only when the content version
// changes keeps the per-motion cost off the O(content) path most of the time.
type wrapCache struct {
	pane   focus
	ver    uint64
	width  int
	lines  []string
	starts []int // starts[i] = first visual row of logical line i; len = len(lines)+1
}

var (
	// selTextStyle paints the live text-selection highlight. Reverse video
	// reads as "selected" on any palette; StyleRanges drops the underlying
	// syntax colour within the range, which matches how a terminal selection
	// looks anyway.
	selTextStyle = lipgloss.NewStyle().Reverse(true)
	// hoverRowStyle is the channel-row hover highlight: a dim background bar,
	// quieter than the selected row's brighter background.
	hoverRowStyle = lipgloss.NewStyle().Background(hoverRowBg)
)

// mouseBlocked reports when mouse interaction should stand down: the feature is
// off, or a modal/overlay owns the screen (mirrors handleKey's modal guards, so
// a click can't act on a pane hidden behind a popup).
func (m *Model) mouseBlocked() bool {
	return !m.mouseEnabled || m.inModal() || m.keyDebugMode ||
		m.jiraPicker.active || m.jiraPointsActive || m.jiraCommentActive ||
		m.glConfirm.active || m.linkConfirm.active
}

// multiClickInterval is the window within which successive presses at (about)
// the same cell are treated as a double / triple click.
const multiClickInterval = 500 * time.Millisecond

// nextClickCount folds a fresh left-press into the running click count: a press
// landing within multiClickInterval and a cell of the previous one bumps the
// count (2 = double, 3 = triple), a 4th restarts the cycle at 1, and anything
// slower or further away resets to 1. The terminal reports only individual
// presses, so this is how double/triple-clicks are synthesised.
func (m *Model) nextClickCount(x, y int) int {
	now := time.Now()
	if m.clickCount > 0 && now.Sub(m.lastClickAt) <= multiClickInterval &&
		absDiff(x, m.lastClickX) <= 1 && absDiff(y, m.lastClickY) <= 1 {
		m.clickCount++
		if m.clickCount > 3 {
			m.clickCount = 1
		}
	} else {
		m.clickCount = 1
	}
	m.lastClickAt, m.lastClickX, m.lastClickY = now, x, y
	return m.clickCount
}

func absDiff(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}

// handleMouseClick routes a left-button press: switch team / open channel for
// the navigation panes, or select the clicked message and arm a text-drag for
// the message / thread panes. Non-left buttons and clicks over nothing
// actionable are ignored.
func (m Model) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.mouseBlocked() {
		return m, nil
	}
	count := m.nextClickCount(msg.X, msg.Y)
	shift := msg.Mod&tea.ModShift != 0
	h := m.hitTest(msg.X, msg.Y)
	switch h.zone {
	case hitTab:
		m.clearTextSel()
		return m.gotoTab(h.idx)
	case hitChannel:
		m.clearTextSel()
		return m.openVisibleChannel(h.idx)
	case hitMessage:
		m.selectPostAt(h.idx)
		m.armTextSelMulti(focusMessages, h.line, h.col, count, shift)
		m.renderMessages()
		return m, nil
	case hitThread:
		m.selectThreadPostAt(h.idx)
		m.armTextSelMulti(focusThread, h.line, h.col, count, shift)
		m.renderThread()
		return m, nil
	case hitComposer:
		return m.clickComposer(h.line, h.col, count, shift)
	case hitJumpBottom:
		return m.clickJumpBottom()
	case hitFeedMarkAll:
		return m, m.markAllFeedRead()
	case hitFeed:
		return m.clickFeedEntry(h.idx)
	case hitSearch:
		return m.clickSearchHit(h.idx)
	case hitSQL:
		// A click on a link in a result row opens it (mouse capture intercepts the
		// OSC 8 click while reporting is on, like the messages pane); otherwise it
		// selects the row. The link is resolved against the content as it was
		// rendered at click time — before clickSQLRow moves the selection bar.
		if h.idx >= 0 {
			if url, ok := m.linkAt(focusSQLResults, h.line, h.col); ok {
				return m.activateLink(url)
			}
		}
		return m.clickSQLRow(h.idx)
	case hitRef:
		// The reference panel has no post selection or text drag, so a click
		// just opens the link under it (if any) — mirroring what the terminal
		// would do natively via OSC 8 were mouse reporting off.
		if url, ok := m.linkAt(focusRef, h.line, h.col); ok {
			return m.activateLink(url)
		}
		return m, nil
	case hitInfo:
		// A click on a link opens it; otherwise a click within a pinned message
		// selects that target and jumps the main pane to it (the same as ↵ on it).
		if url, ok := m.linkAt(focusInfo, h.line, h.col); ok {
			return m.activateLink(url)
		}
		return m.clickInfoTarget(h.line)
	}
	return m, nil
}

// clickFeedEntry handles a left click on a Feed bubble: the first click selects
// it (and focuses the feed), a click on the already-selected bubble opens its
// channel — the same action Enter performs.
func (m Model) clickFeedEntry(idx int) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.feed.entries) {
		return m, nil
	}
	activate := m.focus == focusFeed && m.feed.idx == idx
	m.focus = focusFeed
	m.feed.idx = idx
	if activate {
		return m.openFeedEntry()
	}
	m.renderFeedResults()
	return m, nil
}

// clickSearchHit handles a left click on a Search bubble. idx carries the
// list's selection semantics: -1 is the AI answer box (clicking it focuses the
// follow-up field), len(hits) is the load-more row, and 0..len(hits)-1 are
// hits. The first click selects; a click on the already-selected row activates
// it (open the hit's channel, or expand the load-more row).
func (m Model) clickSearchHit(idx int) (tea.Model, tea.Cmd) {
	aiDone := m.aiSearch.phase == aiSearchDone && m.aiSearch.err == nil
	wasFocused := m.focus == focusSearch
	m.focus = focusSearch

	// AI answer box: select it and (on a finished run) focus the follow-up
	// field, mirroring arrowing up onto it in handleAIDoneKey.
	if idx < 0 {
		m.search.idx = -1
		var cmd tea.Cmd
		if aiDone {
			cmd = m.aiSearch.followup.Focus()
		}
		m.renderSearchResults()
		return m, cmd
	}

	activate := wasFocused && m.search.idx == idx
	// Leaving the answer box for a hit blurs its follow-up; on a plain search
	// the main input stays focused so typing keeps editing the query.
	if aiDone {
		m.aiSearch.followup.Blur()
	} else {
		m.search.input.Focus()
	}
	m.search.idx = idx

	if !aiDone && idx == len(m.search.hits) { // load-more pseudo-row
		if activate {
			return m.expandSearch()
		}
		m.renderSearchResults()
		return m, nil
	}
	if idx >= len(m.search.hits) {
		return m, nil
	}
	if activate {
		return m.openHitChannel(m.search.hits[idx])
	}
	m.renderSearchResults()
	return m, nil
}

// handleMouseRelease finishes a text drag: a real range is copied to the
// clipboard (the highlight stays until the next interaction); a release with no
// movement was just a click — the message was already selected on mousedown, so
// the armed selection is cleared and, if the click landed on a link, it opens
// (the terminal would do this itself via OSC 8, but mouse capture intercepts the
// click while mouseEnabled — see linkclick.go).
func (m Model) handleMouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	if m.composerDrag {
		// Leave the selection live so backspace/delete removes it and typing
		// replaces it — the point of selecting in an editable field. No clipboard
		// copy here, unlike the read-only transcript panes below.
		m.composerDrag = false
		return m, nil
	}
	if !m.textSel.dragging {
		return m, nil
	}
	m.textSel.dragging = false
	if !m.textSel.active {
		pane, line, col := m.textSel.pane, m.textSel.anchorLine, m.textSel.anchorCol
		m.clearTextSel()
		if url, ok := m.linkAt(pane, line, col); ok {
			return m.activateLink(url)
		}
		return m, nil
	}
	text := m.selectedText()
	if strings.TrimSpace(text) == "" {
		return m, nil
	}
	return m, m.copyText(text, "selection")
}

// handleMouseMotion extends an in-progress text drag, or — with no button held
// — updates which tab / channel row the pointer hovers. Hover changes that
// don't move between elements are dropped so the (always-on) re-render after
// each motion stays a cache hit.
func (m Model) handleMouseMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	if m.mouseBlocked() {
		if m.hover.zone != hitNone {
			m.hover = hoverState{}
		}
		m.setHoverLink(hoverLink{})
		m.setInfoHover(-1)
		return m, nil
	}
	if m.composerDrag && msg.Button == tea.MouseLeft {
		return m.dragComposerSel(msg.X, msg.Y)
	}
	if m.textSel.dragging && msg.Button == tea.MouseLeft {
		return m.dragTextSel(msg.X, msg.Y)
	}
	next := m.hoverAt(msg.X, msg.Y)
	// The pill hides the cells it covers, so a link under it must not light up.
	var hl hoverLink
	if next.zone != hitJumpBottom {
		hl = m.hoverLinkAt(msg.X, msg.Y)
	}
	ih := m.infoHoverAt(msg.X, msg.Y)
	if m.hover == next && m.hoverLink == hl && m.infoHoverIdx == ih {
		return m, nil
	}
	m.hover = next
	// Re-renders the affected transcript pane(s) only when the hovered link
	// actually changes, so a move within one link (or over plain text) stays a
	// cache-hit re-render of the unchanged content.
	m.setHoverLink(hl)
	// The channel-info member rows aren't OSC 8 links, so they hover separately.
	m.setInfoHover(ih)
	return m, nil
}

// hoverAt resolves the pointer to a hoverable element — only team tabs, channel
// rows, the jump-to-bottom pill and the Feed tab's mark-all-read button are
// tracked, so it skips the message/thread content-coordinate work hitTest does
// for clicks, keeping the per-motion hover path cheap.
func (m *Model) hoverAt(x, y int) hoverState {
	if y < tabsHeight {
		if m.vcache != nil {
			for _, z := range m.vcache.tabZones {
				if x >= z.x0 && x < z.x1 {
					return hoverState{zone: hitTab, idx: z.idx}
				}
			}
		}
		return hoverState{}
	}
	if x < channelsWidth && !m.onSearchTab() && !m.onFeedTab() && !m.onSQLTab() {
		if h := m.hitChannel(y); h.zone == hitChannel {
			return hoverState{zone: hitChannel, idx: h.idx}
		}
	}
	// Both zones are disarmed on any tab that doesn't draw their label, so
	// neither needs a tab guard of its own.
	if m.vcache != nil {
		if m.vcache.jumpZone.contains(x, y) {
			return hoverState{zone: hitJumpBottom}
		}
		if m.vcache.feedBtnZone.contains(x, y) {
			return hoverState{zone: hitFeedMarkAll}
		}
	}
	return hoverState{}
}

// dragTextSel extends the selection to the dragged-to cell (clamped into the
// pane), snapping to its granularity, and re-renders so the highlight tracks
// the pointer.
func (m Model) dragTextSel(x, y int) (tea.Model, tea.Cmd) {
	pane := m.textSel.pane
	line, col, ok := m.cellToContent(pane, x, y)
	if !ok {
		return m, nil
	}
	m.extendTextSelTo(line, col)
	if pane == focusThread {
		m.renderThread()
	} else {
		m.renderMessages()
	}
	return m, nil
}

// clearTextSel drops any selection (highlight + armed drag). Called on a
// keypress (see handleKey) and when a click lands on a navigation target.
func (m *Model) clearTextSel() { m.textSel = textSel{} }

// armTextSelMulti arms a transcript selection by click count: a single click
// starts a character drag, a double-click selects the word under the cell, a
// triple-click the whole line. A shift-click instead extends the existing
// selection (keeping its granularity) to the cell.
func (m *Model) armTextSelMulti(pane focus, line, col, count int, shift bool) {
	if shift && m.textSel.active && m.textSel.pane == pane {
		m.textSel.dragging = true
		m.extendTextSelTo(line, col)
		return
	}
	switch {
	case count >= 3:
		m.armLineSel(pane, line)
	case count == 2:
		m.armWordSel(pane, line, col)
	default:
		m.armTextSel(pane, line, col)
	}
}

// armTextSel begins a potential character-granular text selection at the clicked
// cell. It stays inactive (no highlight) until a drag moves the head off the
// anchor.
func (m *Model) armTextSel(pane focus, line, col int) {
	m.textSel = textSel{
		pane:       pane,
		anchorLine: line, anchorCol: col,
		anchorLo: col, anchorHi: col,
		headLine: line, headCol: col,
		dragging: true,
	}
}

// armWordSel selects the word under the clicked cell (double-click) and arms a
// word-granular drag.
func (m *Model) armWordSel(pane focus, line, col int) {
	lo, hi := m.wordColBounds(pane, line, col)
	m.textSel = textSel{
		pane:       pane,
		gran:       granWord,
		anchorLine: line, anchorCol: lo,
		anchorLo: lo, anchorHi: hi,
		headLine: line, headCol: hi,
		dragging: true,
		active:   hi > lo,
	}
}

// armLineSel selects the whole logical line under the clicked cell
// (triple-click) and arms a line-granular drag.
func (m *Model) armLineSel(pane focus, line int) {
	lo, hi := m.lineColBounds(pane, line)
	m.textSel = textSel{
		pane:       pane,
		gran:       granLine,
		anchorLine: line, anchorCol: lo,
		anchorLo: lo, anchorHi: hi,
		headLine: line, headCol: hi,
		dragging: true,
		active:   hi > lo,
	}
}

// extendTextSelTo moves the selection's moving end (head) to content cell
// (line, col), snapping to the selection's granularity. The anchor unit — on
// anchorLine, columns [anchorLo, anchorHi) — stays covered: dragging past it
// pins the fixed end to its start and grows the head to the dragged word/line's
// far edge; dragging before it pins the fixed end to its end.
func (m *Model) extendTextSelTo(line, col int) {
	s := &m.textSel
	lo, hi := col, col
	switch s.gran {
	case granWord:
		lo, hi = m.wordColBounds(s.pane, line, col)
	case granLine:
		lo, hi = m.lineColBounds(s.pane, line)
	}
	switch {
	case line > s.anchorLine || (line == s.anchorLine && lo >= s.anchorHi):
		s.anchorCol = s.anchorLo
		s.headLine, s.headCol = line, hi
	case line < s.anchorLine || (line == s.anchorLine && hi <= s.anchorLo):
		s.anchorCol = s.anchorHi
		s.headLine, s.headCol = line, lo
	default: // overlapping the anchor unit (same line, same word/line)
		s.anchorCol = min(lo, s.anchorLo)
		s.headLine, s.headCol = line, max(hi, s.anchorHi)
	}
	s.active = s.anchorLine != s.headLine || s.anchorCol != s.headCol
}

// wordColBounds returns the [start, end) display-column range of the word — the
// run of one rune class (see runeClass) — at display column col on logical line
// `line` of the pane. The left edge is pulled in to the line's content start so
// a word selection never reaches into the two-space gutter. An empty range is
// returned for a click past the line's content.
func (m *Model) wordColBounds(pane focus, line, col int) (int, int) {
	lines := m.paneLines(pane)
	if line < 0 || line >= len(lines) {
		return col, col
	}
	plain := []rune(ansi.Strip(lines[line]))
	starts := make([]int, len(plain)+1)
	w := 0
	for i, r := range plain {
		starts[i] = w
		w += lipgloss.Width(string(r))
	}
	starts[len(plain)] = w
	idx := -1
	for i := range plain {
		if col >= starts[i] && col < starts[i+1] {
			idx = i
			break
		}
	}
	if idx < 0 {
		return col, col
	}
	cls := runeClass(plain[idx])
	a, b := idx, idx+1
	for a > 0 && runeClass(plain[a-1]) == cls {
		a--
	}
	for b < len(plain) && runeClass(plain[b]) == cls {
		b++
	}
	lo, hi := starts[a], starts[b]
	if cl := contentLeft(lines[line]); lo < cl {
		lo = cl
	}
	return lo, hi
}

// lineColBounds returns the [start, end) display-column range of one logical
// line's content (past the gutter, up to its rendered width).
func (m *Model) lineColBounds(pane focus, line int) (int, int) {
	lines := m.paneLines(pane)
	if line < 0 || line >= len(lines) {
		return 0, 0
	}
	return contentLeft(lines[line]), lipgloss.Width(lines[line])
}

// paneLines returns the pane's logical lines at its current width (the same
// cached split selectedText and contentCoord read).
func (m *Model) paneLines(pane focus) []string {
	width := m.msgsView.Width()
	if pane == focusThread {
		width = m.threadView.Width()
	}
	lines, _ := m.ensureWrapIndex(pane, width)
	return lines
}

// runeClass buckets a rune for word selection: whitespace, word (letters,
// digits, underscore) and "other" (punctuation/symbols) each form their own
// contiguous run, mirroring the editor's classification.
func runeClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 1
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 2
	default:
		return 3
	}
}

// selectPostAt moves the message selection to post idx and focuses the messages
// pane, clearing any wheel free-scroll so the selection follows again. Render
// is left to the caller. Blurs the composer so its cursor stops rendering once
// focus has left it — mirrors the keyboard escape-to-transcript path.
func (m *Model) selectPostAt(idx int) {
	if idx < 0 || idx >= len(m.posts) {
		return
	}
	m.postIdx = idx
	m.focus = focusMessages
	m.input.Blur()
	m.msgScrollFree = false
}

// selectThreadPostAt is the thread-pane mirror of selectPostAt.
func (m *Model) selectThreadPostAt(idx int) {
	if !m.threadOpen || idx < 0 || idx >= len(m.threadPosts) {
		return
	}
	m.threadIdx = idx
	m.focus = focusThread
	m.input.Blur()
	m.threadScrollFree = false
}

// openVisibleChannel opens the channel at visible index idx, mirroring
// navChannel: it moves the sidebar cursor and loads the channel, leaving the
// current focus alone so a draft in the composer survives the jump. No-op when
// the channel is already open.
func (m Model) openVisibleChannel(idx int) (tea.Model, tea.Cmd) {
	vis := m.visibleChannels()
	if idx < 0 || idx >= len(vis) {
		return m, nil
	}
	m.channelIdx = idx
	ch := vis[idx]
	if ch.Id == m.openChannelID {
		return m, nil
	}
	return m, tea.Batch(m.openChannelLoadCmd(ch.Id), m.bumpChannelStat(ch.Id))
}

// hitTest resolves a screen cell to the clickable element under it. The tab
// strip reads the zones recorded by the last render; the body recomputes its
// pane split and channel scroll window live (clicks are rare, so there's no
// need to cache that). Returns hitNone over nothing actionable.
func (m *Model) hitTest(x, y int) hit {
	// Team tab strip occupies the top tabsHeight rows.
	if y < tabsHeight {
		if m.vcache != nil {
			for _, z := range m.vcache.tabZones {
				if x >= z.x0 && x < z.x1 {
					return hit{zone: hitTab, idx: z.idx}
				}
			}
		}
		return hit{zone: hitNone}
	}
	// Below the strip. The Search / Feed panes own the whole body on their
	// synthetic tabs; map the click to the bubble under it — or, on the Feed
	// tab's title row, to the mark-all-read button sitting at its right end.
	if m.onFeedTab() {
		if m.vcache != nil && m.vcache.feedBtnZone.contains(x, y) {
			return hit{zone: hitFeedMarkAll}
		}
		return m.hitFeedBubble(y)
	}
	if m.onSearchTab() {
		return m.hitSearchBubble(y)
	}
	if m.onSQLTab() {
		return m.hitSQLRow(x, y)
	}
	// The compose box sits at the bottom of the messages / thread pane on a
	// normal channel view (the Feed / Search / SQL tabs returned above). A click
	// in it focuses the editor and seeds a drag-select.
	if m.inComposer(x, y) {
		vrow, vcol := m.composerCell(x, y)
		return hit{zone: hitComposer, line: vrow, col: vcol}
	}
	// The jump-to-bottom pill is painted over the transcript's last row, so it
	// wins over the message underneath it.
	if m.vcache != nil && m.vcache.jumpZone.contains(x, y) {
		return hit{zone: hitJumpBottom}
	}
	if x < channelsWidth {
		return m.hitChannel(y)
	}
	rightW := m.width - channelsWidth
	if rightW < 10 {
		rightW = 10
	}
	msgsW := rightW
	if m.threadOpen {
		threadW := splitRightPane(rightW)
		msgsW = rightW - threadW
		if x >= channelsWidth+msgsW {
			tx0, top, w, h, yoff := m.threadGeom()
			return m.hitViewportPost(x, y, top, tx0, w, h, yoff, focusThread, m.threadRowStarts, len(m.threadPosts))
		}
	} else if m.refOpen {
		refW := splitRightPane(rightW)
		msgsW = rightW - refW
		if x >= channelsWidth+msgsW {
			return m.hitRefContent(x, y)
		}
	} else if m.infoOpen {
		infoW := splitRightPane(rightW)
		msgsW = rightW - infoW
		if x >= channelsWidth+msgsW {
			return m.hitInfoContent(x, y)
		}
	}
	mx0, top, w, h, yoff := m.messagesGeom()
	return m.hitViewportPost(x, y, top, mx0, w, h, yoff, focusMessages, m.msgRowStarts, len(m.posts))
}

// hitChannel maps a screen row in the sidebar column to a visible-channel
// index, reproducing renderChannelsPane's scroll window so the row the user
// sees is the one selected.
func (m *Model) hitChannel(y int) hit {
	bodyH := m.bodyHeight()
	innerH := bodyH - 1
	if innerH < 1 {
		innerH = 1
	}
	listH := innerH - 1
	if listH < 1 {
		listH = 1
	}
	vis := m.visibleChannels()
	off := m.channelScrollOff(len(vis), listH)
	// Header sits on the body's top row; channel rows start one below it.
	row := y - (tabsHeight + 1)
	if row < 0 || row >= listH {
		return hit{zone: hitNone}
	}
	idx := off + row
	if idx < 0 || idx >= len(vis) {
		return hit{zone: hitNone}
	}
	return hit{zone: hitChannel, idx: idx}
}

// bubbleZone maps the first viewport visual row of one selectable item in a
// bubble list (a Feed entry or a Search hit) to that item's index. The item's
// span runs from row0 up to the next zone's row0 (or the list's total height
// for the last item, so a click in the empty space below the last bubble is a
// no-op). idx carries the list's own selection semantics: a Feed entry / Search
// hit index, -1 for the AI answer box, or len(hits) for the load-more row.
type bubbleZone struct {
	row0 int
	idx  int
}

// bubbleAt returns the idx of the zone whose visual-row span contains vrow. ok
// is false when vrow sits above the first zone or at/below total (the empty
// rows under the last bubble), so the click does nothing.
func bubbleAt(zones []bubbleZone, total, vrow int) (idx int, ok bool) {
	if len(zones) == 0 || vrow < zones[0].row0 || vrow >= total {
		return 0, false
	}
	for i := len(zones) - 1; i >= 0; i-- {
		if vrow >= zones[i].row0 {
			return zones[i].idx, true
		}
	}
	return 0, false
}

// feedGeom / searchGeom give a bubble viewport's top screen row, height and
// y-offset, mirroring messagesGeom. The Feed pane stacks a title + rule above
// its viewport (2 rows); the Search pane stacks a title, a 2-row input box and
// a rule (4 rows). Both panes fill the whole body width on their tab.
func (m *Model) feedGeom() (top, height, yoff int) {
	return tabsHeight + 2, m.feed.view.Height(), m.feed.view.YOffset()
}

func (m *Model) searchGeom() (top, height, yoff int) {
	return tabsHeight + 4, m.search.view.Height(), m.search.view.YOffset()
}

// hitFeedBubble maps a screen row on the Feed tab to the feed entry under it,
// via the per-bubble visual-row zones the last render recorded.
func (m *Model) hitFeedBubble(y int) hit {
	top, height, yoff := m.feedGeom()
	row := y - top
	if row < 0 || row >= height {
		return hit{zone: hitNone}
	}
	if idx, ok := bubbleAt(m.feed.zones, m.feed.zonesTotal, yoff+row); ok {
		return hit{zone: hitFeed, idx: idx}
	}
	return hit{zone: hitNone}
}

// hitSearchBubble is the Search-tab mirror of hitFeedBubble.
func (m *Model) hitSearchBubble(y int) hit {
	top, height, yoff := m.searchGeom()
	row := y - top
	if row < 0 || row >= height {
		return hit{zone: hitNone}
	}
	if idx, ok := bubbleAt(m.search.zones, m.search.zonesTotal, yoff+row); ok {
		return hit{zone: hitSearch, idx: idx}
	}
	return hit{zone: hitNone}
}

// sqlGeom gives the SQL result viewport's content-left column, top screen row,
// width, height and y-offset (mirroring messagesGeom). The pane fills the body
// width with a single left border, so content starts at column 1; it stacks a
// title (1), an input box (1-row top border + the editor's current height) and
// a rule (1) above the viewport — so the header height tracks the dynamic editor
// height, mirroring sizeSQLView.
func (m *Model) sqlGeom() (x0, top, width, height, yoff int) {
	return 1, tabsHeight + 3 + m.sql.input.Height(), m.sql.view.Width(), m.sql.view.Height(), m.sql.view.YOffset()
}

// sqlRowAt returns the result row whose visual-row span contains vrow, using the
// per-row offsets the last render recorded (rowStarts[i]..rowStarts[i+1]). ok is
// false above the first row or at/below the total (empty space under the rows).
func sqlRowAt(rowStarts []int, vrow int) (idx int, ok bool) {
	n := len(rowStarts) - 1
	if n <= 0 || vrow < rowStarts[0] || vrow >= rowStarts[n] {
		return 0, false
	}
	for i := n - 1; i >= 0; i-- {
		if vrow >= rowStarts[i] {
			return i, true
		}
	}
	return 0, false
}

// hitSQLRow maps a screen cell on the SQL tab to the result row under it (idx >=
// 0, with line/col content coordinates so a click can resolve a link), or to the
// editor region above the results (idx -1, so a click there focuses the query
// editor). Empty space below the rows is a no-op.
func (m *Model) hitSQLRow(x, y int) hit {
	x0, top, width, height, yoff := m.sqlGeom()
	if y < top {
		// Title / editor / rule region: a click here focuses the editor.
		return hit{zone: hitSQL, idx: -1}
	}
	row := y - top
	if row >= 0 && row < height {
		vrow := yoff + row
		if idx, ok := sqlRowAt(m.sql.rowStarts, vrow); ok {
			line, col := m.contentCoord(focusSQLResults, x, x0, width, vrow)
			return hit{zone: hitSQL, idx: idx, line: line, col: col}
		}
	}
	return hit{zone: hitNone}
}

// channelScrollOff reproduces renderChannelsPane's scroll-window math: start
// from the persisted offset, then slide just enough to keep the selected row on
// screen. Reading the same persisted m.chanOff the last render read makes this
// match what's drawn.
func (m *Model) channelScrollOff(visLen, listH int) int {
	off := m.chanOff
	ci := m.channelIdx
	if ci > visLen-1 {
		ci = visLen - 1
	}
	if ci < off {
		off = ci
	}
	if ci >= off+listH {
		off = ci - listH + 1
	}
	if off < 0 {
		off = 0
	}
	return off
}

// hitViewportPost maps a cell inside a message/thread viewport to the post
// under it (via the pane's per-post visual-row starts) plus the content
// coordinate for text selection. top is the viewport's top screen row, x0 its
// content origin column.
func (m *Model) hitViewportPost(x, y, top, x0, width, height, yoff int, pane focus, rowStarts []int, n int) hit {
	if n == 0 || len(rowStarts) == 0 || width <= 0 || height <= 0 {
		return hit{zone: hitNone}
	}
	row := y - top
	if row < 0 || row >= height {
		return hit{zone: hitNone}
	}
	vrow := yoff + row
	if vrow < 0 || vrow >= rowStarts[len(rowStarts)-1] {
		return hit{zone: hitNone}
	}
	idx := postAtVisualRow(rowStarts, vrow)
	if idx < 0 || idx >= n {
		return hit{zone: hitNone}
	}
	zone := hitMessage
	if pane == focusThread {
		zone = hitThread
	}
	line, col := m.contentCoord(pane, x, x0, width, vrow)
	return hit{zone: zone, idx: idx, line: line, col: col}
}

// hitRefContent maps a cell inside the reference panel's viewport to the
// content coordinate under it, so a click / hover can resolve the link there.
// Unlike the message and thread panes the panel has no per-post structure — it
// is a single scrollable document — so the hit carries only (line, col), with
// no post index. Returns hitNone over the title row, the empty rows below the
// content, or before the panel has any geometry.
func (m *Model) hitRefContent(x, y int) hit {
	x0, top, width, height, yoff := m.refGeom()
	if width <= 0 || height <= 0 {
		return hit{zone: hitNone}
	}
	row := y - top
	if row < 0 || row >= height {
		return hit{zone: hitNone}
	}
	vrow := yoff + row
	_, starts := m.ensureWrapIndex(focusRef, width)
	total := 0
	if len(starts) > 0 {
		total = starts[len(starts)-1]
	}
	if vrow < 0 || vrow >= total {
		return hit{zone: hitNone}
	}
	line, col := m.contentCoord(focusRef, x, x0, width, vrow)
	return hit{zone: hitRef, line: line, col: col}
}

// cellToContent maps a screen cell to (logical line, display column) within a
// pane's content, clamping out-of-bounds cells into the viewport so a drag that
// leaves the pane still extends to its edge. ok is false when the pane has no
// content / geometry yet.
func (m *Model) cellToContent(pane focus, x, y int) (line, col int, ok bool) {
	var x0, top, width, height, yoff int
	if pane == focusThread {
		x0, top, width, height, yoff = m.threadGeom()
	} else {
		x0, top, width, height, yoff = m.messagesGeom()
	}
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	row := y - top
	if row < 0 {
		row = 0
	}
	if row >= height {
		row = height - 1
	}
	vrow := yoff + row
	_, starts := m.ensureWrapIndex(pane, width)
	total := 0
	if len(starts) > 0 {
		total = starts[len(starts)-1]
	}
	if total == 0 {
		return 0, 0, false
	}
	if vrow >= total {
		vrow = total - 1
	}
	if vrow < 0 {
		vrow = 0
	}
	line, col = m.contentCoord(pane, x, x0, width, vrow)
	return line, col, true
}

// contentCoord converts a visual row + screen column to a (logical line,
// display column) coordinate in the pane's content, accounting for soft-wrap:
// the column folds in which wrap segment of the line the row is.
func (m *Model) contentCoord(pane focus, x, x0, width, vrow int) (int, int) {
	lines, starts := m.ensureWrapIndex(pane, width)
	if len(lines) == 0 {
		return 0, 0
	}
	li := lineAtVisualRow(starts, vrow)
	if li < 0 {
		li = 0
	}
	if li >= len(lines) {
		li = len(lines) - 1
	}
	seg := vrow - starts[li]
	if seg < 0 {
		seg = 0
	}
	xc := x - x0
	if xc < 0 {
		xc = 0
	}
	if width > 0 && xc > width {
		xc = width
	}
	col := seg*width + xc
	if lw := lipgloss.Width(lines[li]); col > lw {
		col = lw
	}
	if col < 0 {
		col = 0
	}
	return li, col
}

// ensureWrapIndex returns the pane's logical lines and per-line visual-row
// starts, rebuilding the cache when the content version or width changed.
func (m *Model) ensureWrapIndex(pane focus, width int) ([]string, []int) {
	// Check the cache key (content version, not the content itself) before
	// fetching the viewport content — GetContent copies the whole transcript, and
	// the hover path hits this every mouse-motion, so skip it on a cache hit.
	ver := m.msgsContentVer
	switch pane {
	case focusThread:
		ver = m.threadContentVer
	case focusRef:
		ver = m.refContentVer
	case focusInfo:
		ver = m.infoContentVer
	case focusSQLResults:
		ver = m.sqlContentVer
	}
	if m.wrapIdx.pane == pane && m.wrapIdx.ver == ver && m.wrapIdx.width == width && m.wrapIdx.lines != nil {
		return m.wrapIdx.lines, m.wrapIdx.starts
	}
	content := m.msgsView.GetContent()
	switch pane {
	case focusThread:
		content = m.threadView.GetContent()
	case focusRef:
		content = m.refView.GetContent()
	case focusInfo:
		content = m.infoView.GetContent()
	case focusSQLResults:
		content = m.sql.view.GetContent()
	}
	lines := strings.Split(content, "\n")
	starts := make([]int, len(lines)+1)
	acc := 0
	for i, ln := range lines {
		starts[i] = acc
		acc += visualHeight(ln, width)
	}
	starts[len(lines)] = acc
	m.wrapIdx = wrapCache{pane: pane, ver: ver, width: width, lines: lines, starts: starts}
	return lines, starts
}

// gutterWidth is the two-space left gutter that body, attachment, reaction and
// poll lines carry as UI chrome (the same columns the selected-post bar ▎
// occupies). It isn't message content, so text selection skips it.
const gutterWidth = 2

// contentLeft is the first content column of a rendered line: past the gutter
// on body lines, or 0 on header lines (which carry no gutter). A selection
// whose left edge falls inside the gutter is pulled in to here so the chrome
// indent is neither highlighted nor copied.
func contentLeft(line string) int {
	if strings.HasPrefix(line, strings.Repeat(" ", gutterWidth)) {
		return gutterWidth
	}
	return 0
}

// selectedText extracts the plain (ANSI-stripped) text of the current
// selection, column-sliced per line and trimmed of trailing padding on the
// lines that run to their end. The two-space gutter is excluded (see
// contentLeft) so copied text isn't indented by the chrome.
func (m *Model) selectedText() string {
	width := m.msgsView.Width()
	if m.textSel.pane == focusThread {
		width = m.threadView.Width()
	}
	lines, _ := m.ensureWrapIndex(m.textSel.pane, width)
	l0, c0, l1, c1 := m.textSel.normalized()
	if l0 < 0 {
		l0 = 0
	}
	if l1 >= len(lines) {
		l1 = len(lines) - 1
	}
	var b strings.Builder
	for li := l0; li <= l1; li++ {
		start, end := contentLeft(lines[li]), lipgloss.Width(lines[li])
		if li == l0 && c0 > start {
			start = c0
		}
		if li == l1 {
			end = c1
		}
		if end < start {
			end = start
		}
		// ansi.Strip drops the escape sequences; the effect sentinels are ordinary
		// (invisible) runes and would otherwise be copied to the clipboard.
		seg := stripEffectSentinels(ansi.Strip(ansi.Cut(lines[li], start, end)))
		if li != l1 {
			seg = strings.TrimRight(seg, " ")
			b.WriteString(seg)
			b.WriteByte('\n')
		} else {
			b.WriteString(seg)
		}
	}
	return b.String()
}

// applyTextSelHighlight reverse-videos the selected column range on each logical
// line of `lines` (the freshly assembled, not-yet-cached content) for the given
// pane. No-op unless an active selection belongs to that pane.
func (m *Model) applyTextSelHighlight(pane focus, lines []string) {
	if !m.textSel.active || m.textSel.pane != pane {
		return
	}
	l0, c0, l1, c1 := m.textSel.normalized()
	for li := l0; li <= l1 && li < len(lines); li++ {
		if li < 0 {
			continue
		}
		start, end := contentLeft(lines[li]), lipgloss.Width(lines[li])
		if li == l0 && c0 > start {
			start = c0
		}
		if li == l1 {
			end = c1
		}
		if end <= start {
			continue
		}
		lines[li] = lipgloss.StyleRanges(lines[li], lipgloss.NewRange(start, end, selTextStyle))
	}
}

// messagesGeom returns the messages viewport's content-origin column, top
// screen row, width, height and y-offset. The title sits on the body's top row,
// the viewport one below; the content begins one column past the left border.
func (m *Model) messagesGeom() (x0, top, width, height, yoff int) {
	return channelsWidth + 1, tabsHeight + 1, m.msgsView.Width(), m.msgsView.Height(), m.msgsView.YOffset()
}

// composerGeom returns the compose editor's on-screen geometry: x0 is its left
// edge (where the prompt gutter begins), top its first visible visual row, width
// the editor's total inner width (prompt + content), height its visible rows,
// and yoff its scroll offset. The composer sits at the bottom of whichever pane
// is accepting replies — the thread pane when one is open, else the messages
// pane — so it's anchored from the bottom of the body: the pane's bottom border
// is the body's last row, and the editor's rows sit just above it. Anchoring from
// the bottom keeps this correct no matter how tall the attachment chip strip or a
// mention / emoji / slash popup grows, since those stack above the box.
//
// The body height comes from the cache the last render wrote (see renderViewContent),
// not a fresh footer render: this runs on the per-motion hover path (via hitTest),
// which must not allocate. Before the first render there's no geometry, so width
// is 0 and inComposer reports false.
func (m *Model) composerGeom() (x0, top, width, height, yoff int) {
	bodyH := 0
	if m.vcache != nil {
		bodyH = m.vcache.bodyH
	}
	if bodyH <= 0 {
		return 0, 0, 0, 0, 0
	}
	height = m.input.Height()
	top = tabsHeight + bodyH - 1 - height
	if m.threadOpen {
		rightW := m.width - channelsWidth
		if rightW < 10 {
			rightW = 10
		}
		msgsW := rightW - splitRightPane(rightW)
		x0 = channelsWidth + msgsW + 1
	} else {
		x0 = channelsWidth + 1
	}
	return x0, top, m.input.Width(), height, m.input.ScrollYOffset()
}

// editorCursor returns the absolute screen cell where the real terminal cursor
// should sit for whichever text-input surface is currently focused and
// on-screen, or ok == false when none is (so View leaves the cursor hidden).
// Overlays that carry their own input win first (the channel switcher, then the
// jira-comment overlay), since they render over any tab; any other body-covering
// overlay suppresses the cursor; then the Search and SQL tabs, else the composer.
func (m *Model) editorCursor() (col, row int, ok bool) {
	switch {
	case m.switcherMode:
		return m.switcherCursor()
	case m.jiraCommentActive:
		return m.jiraCommentCursor()
	case m.bodyOverlayActive():
		return 0, 0, false
	case m.onSearchTab():
		return m.searchCursor()
	case m.onSQLTab():
		return m.sqlCursor()
	default:
		return m.composerCursor()
	}
}

// bodyOverlayActive reports whether a centered overlay has replaced the body,
// so the editing surface beneath it must not place a cursor. Excludes the
// jira-comment overlay, which carries its own editor and places its own cursor
// (see jiraCommentCursor) — editorCursor handles it ahead of this check.
func (m *Model) bodyOverlayActive() bool {
	return m.inModal() || m.jiraPointsActive || m.jiraPicker.active ||
		m.glConfirm.active || m.linkConfirm.active
}

// composerCursor maps the compose editor's own (col,row) — past the prompt
// gutter and scroll — onto composerGeom's on-screen origin. ok is false unless
// the composer is focused with a rendered geometry. Mirrors composerGeom's
// reliance on the cached body height.
func (m *Model) composerCursor() (col, row int, ok bool) {
	if m.focus != focusInput {
		return 0, 0, false
	}
	cx, cy, okPos := m.input.CursorViewPos()
	if !okPos {
		return 0, 0, false
	}
	x0, top, width, _, _ := m.composerGeom()
	if width <= 0 {
		return 0, 0, false
	}
	return x0 + cx, top + cy, true
}

// sqlCursor maps the SQL query editor's own (col,row) onto its screen origin.
// The SQL pane is the full-width body beneath the tab strip: its left border
// sits at column 0 (content at 1), and the title row then the input box's top
// border sit above the editor's first row (so it begins two rows into the body).
// ok is false when the editor is unfocused or scrolled out (CursorViewPos).
func (m *Model) sqlCursor() (col, row int, ok bool) {
	cx, cy, okPos := m.sql.input.CursorViewPos()
	if !okPos {
		return 0, 0, false
	}
	return 1 + cx, tabsHeight + 2 + cy, true
}

// searchCursor maps the Search tab's text input onto its screen origin. The
// pane mirrors the SQL pane exactly (full-width body, left border at column 0,
// title row + input-box top border above the input), so the editor's first
// cell is the same fixed offset. The bubbles input reports its own caret column
// (prompt width included) via Cursor, or nil when it's blurred or scrolled out.
func (m *Model) searchCursor() (col, row int, ok bool) {
	c := m.search.input.Cursor()
	if c == nil {
		return 0, 0, false
	}
	return 1 + c.X, tabsHeight + 2 + c.Y, true
}

// switcherCursor maps the channel switcher's text input onto its screen origin
// inside the centered popup. The popup's height varies with the result list and
// sub-mode, so rather than recompute it we re-render the box and measure it,
// then apply the same lipgloss.Place centering renderViewContent uses. The
// input always sits at the same spot inside the box: one row below the title
// (past the top border), two columns in (border + padding).
func (m *Model) switcherCursor() (col, row int, ok bool) {
	c := m.switcher.Cursor()
	if c == nil {
		return 0, 0, false
	}
	bodyH := 0
	if m.vcache != nil {
		bodyH = m.vcache.bodyH
	}
	if bodyH <= 0 {
		return 0, 0, false
	}
	box := m.renderSwitcher(bodyH)
	boxLeft := placeOffset(m.width, lipgloss.Width(box))
	boxTop := tabsHeight + placeOffset(bodyH, lipgloss.Height(box))
	return boxLeft + 2 + c.X, boxTop + 2 + c.Y, true
}

// jiraCommentCursor maps the jira-comment editor's own (col,row) onto its screen
// origin inside the centered modal. It reconstructs the box geometry the way
// renderJiraCommentInput builds it (the outerW clamp, a rounded border, 1×3
// padding, a header+blank and an optional reply+blank above the editor) and the
// lipgloss.Place centering renderViewContent applies, so the cell tracks the
// editor even as the box grows or the reply line appears.
func (m *Model) jiraCommentCursor() (col, row int, ok bool) {
	cx, cy, okPos := m.jiraCommentInput.CursorViewPos()
	if !okPos {
		return 0, 0, false
	}
	bodyH := 0
	if m.vcache != nil {
		bodyH = m.vcache.bodyH
	}
	if bodyH <= 0 {
		return 0, 0, false
	}
	// Box outer width — same clamp as renderJiraCommentInput.
	outerW := confirmDialogMaxWidth
	if outerW > m.width-4 {
		outerW = m.width - 4
	}
	if outerW < 40 {
		outerW = 40
	}
	// Lines stacked above the editor inside the box: header + blank, then an
	// optional "replying to" line + blank.
	aboveEditor := 2
	if m.jiraCommentReplyTo != "" {
		aboveEditor += 2
	}
	// Box outer height: rounded border (2) + padding (2 top/bottom) + content
	// (the lines above the editor, the editor itself, then a blank + the hint).
	boxH := 4 + aboveEditor + m.jiraCommentInput.Height() + 2

	boxLeft := placeOffset(m.width, outerW)
	boxTop := tabsHeight + placeOffset(bodyH, boxH)
	// Editor origin within the box: left border (1) + left padding (3); top
	// border (1) + top padding (1) + the lines above the editor.
	return boxLeft + 4 + cx, boxTop + 2 + aboveEditor + cy, true
}

// placeOffset returns the leading pad lipgloss.Place puts before a box of size
// box centered in total. lipgloss splits the gap as left = gap - round(gap/2),
// which for a non-negative gap is exactly gap/2 (integer). No padding when the
// box meets or exceeds the available space.
func placeOffset(total, box int) int {
	gap := total - box
	if gap <= 0 {
		return 0
	}
	return gap / 2
}

// inComposer reports whether a screen cell lands within the compose editor's
// rows. Strict bounds (no clamping); used to route a click to the editor.
func (m *Model) inComposer(x, y int) bool {
	x0, top, width, height, _ := m.composerGeom()
	return height > 0 && width > 0 && y >= top && y < top+height && x >= x0 && x < x0+width
}

// composerCell maps a screen cell to an editor (visual row, visual column),
// clamping out-of-box cells to the nearest edge so a drag that leaves the box
// still extends the selection to it. The column is measured from the content
// start, past the prompt gutter — the coordinate space the editor's selection
// API expects.
func (m *Model) composerCell(x, y int) (vrow, vcol int) {
	x0, top, width, height, yoff := m.composerGeom()
	row := y - top
	if row < 0 {
		row = 0
	}
	if row >= height {
		row = height - 1
	}
	vrow = yoff + row
	pw := m.input.PromptWidth()
	vcol = x - x0 - pw
	if vcol < 0 {
		vcol = 0
	}
	if cw := width - pw; cw >= 1 && vcol > cw {
		vcol = cw
	}
	return vrow, vcol
}

// clickComposer focuses the compose editor and arms a drag-select at the
// clicked cell: a single click places the caret, a double-click selects the
// word, a triple-click the line, and a shift-click extends the selection from
// the caret. Mirrors the keyboard paths that move focus into the composer;
// clearing any transcript selection matches a nav click.
func (m Model) clickComposer(vrow, vcol, count int, shift bool) (tea.Model, tea.Cmd) {
	m.clearTextSel()
	m.focus = focusInput
	cmd := m.input.Focus()
	switch {
	case shift:
		m.input.ExtendSelectionFromCaret(vrow, vcol)
	case count >= 3:
		m.input.SelectLineAtVisual(vrow, vcol)
	case count == 2:
		m.input.SelectWordAtVisual(vrow, vcol)
	default:
		m.input.BeginSelection(vrow, vcol)
	}
	m.composerDrag = true
	return m, cmd
}

// dragComposerSel extends the in-flight compose selection to the dragged-to cell.
func (m Model) dragComposerSel(x, y int) (tea.Model, tea.Cmd) {
	vrow, vcol := m.composerCell(x, y)
	m.input.ExtendSelectionToVisual(vrow, vcol)
	return m, nil
}

// threadGeom mirrors messagesGeom for the thread pane, which sits to the right
// of the messages pane (same horizontal split renderMessagesPane / viewContent
// use when the thread is open).
func (m *Model) threadGeom() (x0, top, width, height, yoff int) {
	rightW := m.width - channelsWidth
	if rightW < 10 {
		rightW = 10
	}
	msgsW := rightW - splitRightPane(rightW)
	return channelsWidth + msgsW + 1, tabsHeight + 1, m.threadView.Width(), m.threadView.Height(), m.threadView.YOffset()
}

// refGeom mirrors threadGeom for the reference panel, which shares the same
// right-side slot (only one of the thread sidebar and the panel is ever open).
func (m *Model) refGeom() (x0, top, width, height, yoff int) {
	rightW := m.width - channelsWidth
	if rightW < 10 {
		rightW = 10
	}
	msgsW := rightW - splitRightPane(rightW)
	return channelsWidth + msgsW + 1, tabsHeight + 1, m.refView.Width(), m.refView.Height(), m.refView.YOffset()
}

// infoGeom mirrors refGeom for the channel-info panel, which shares the same
// right-side slot (only one of the thread sidebar, reference and info panels is
// ever open).
func (m *Model) infoGeom() (x0, top, width, height, yoff int) {
	rightW := m.width - channelsWidth
	if rightW < 10 {
		rightW = 10
	}
	msgsW := rightW - splitRightPane(rightW)
	return channelsWidth + msgsW + 1, tabsHeight + 1, m.infoView.Width(), m.infoView.Height(), m.infoView.YOffset()
}

// bodyHeight reproduces viewContent's body-height calculation (terminal height
// minus the rendered footer and tab strip, floored at 5).
func (m *Model) bodyHeight() int {
	bodyH := m.height - lipgloss.Height(m.renderFooter()) - tabsHeight
	if bodyH < 5 {
		bodyH = 5
	}
	return bodyH
}

// visualHeight is how many soft-wrapped visual rows a line occupies at width —
// the same ceil(width/maxWidth) math the viewport and msgRowStarts use.
func visualHeight(line string, width int) int {
	if width <= 0 {
		return 1
	}
	w := lipgloss.Width(line)
	if w <= width {
		return 1
	}
	return (w + width - 1) / width
}

// postAtVisualRow returns the index of the post whose visual-row span contains
// vrow: the largest i with rowStarts[i] <= vrow.
func postAtVisualRow(rowStarts []int, vrow int) int {
	// rowStarts has len(posts)+1 entries; search the post entries only.
	i := sort.Search(len(rowStarts), func(i int) bool { return rowStarts[i] > vrow })
	return i - 1
}

// lineAtVisualRow is postAtVisualRow over the per-line starts: the largest i
// with starts[i] <= vrow.
func lineAtVisualRow(starts []int, vrow int) int {
	i := sort.Search(len(starts), func(i int) bool { return starts[i] > vrow })
	return i - 1
}
