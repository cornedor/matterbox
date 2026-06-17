package ui

import (
	"sort"
	"strings"

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
// with a hover style. zone is hitNone over nothing clickable. Only hitTab and
// hitChannel are tracked — the cheap navigational targets — so per-motion
// hover never drags the message-pane render onto the hot path.
type hoverState struct {
	zone hitZone
	idx  int
}

// textSel is a click-drag text selection in the message or thread pane. anchor
// is where the drag began, head where it currently ends, both in (logical line,
// display column) content coordinates. dragging is true while the button is
// held; active turns true once the drag spans a non-empty range, so a bare
// click (mousedown+up with no movement) doesn't paint a zero-width highlight.
type textSel struct {
	pane                  focus
	anchorLine, anchorCol int
	headLine, headCol     int
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
	hoverRowStyle = lipgloss.NewStyle().Background(lipgloss.Color("237"))
)

// mouseBlocked reports when mouse interaction should stand down: the feature is
// off, or a modal/overlay owns the screen (mirrors handleKey's modal guards, so
// a click can't act on a pane hidden behind a popup).
func (m *Model) mouseBlocked() bool {
	return !m.mouseEnabled || m.inModal() || m.keyDebugMode ||
		m.jiraPicker.active || m.jiraPointsActive || m.glConfirm.active
}

// handleMouseClick routes a left-button press: switch team / open channel for
// the navigation panes, or select the clicked message and arm a text-drag for
// the message / thread panes. Non-left buttons and clicks over nothing
// actionable are ignored.
func (m Model) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.mouseBlocked() {
		return m, nil
	}
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
		m.armTextSel(focusMessages, h.line, h.col)
		m.renderMessages()
		return m, nil
	case hitThread:
		m.selectThreadPostAt(h.idx)
		m.armTextSel(focusThread, h.line, h.col)
		m.renderThread()
		return m, nil
	case hitFeed:
		return m.clickFeedEntry(h.idx)
	case hitSearch:
		return m.clickSearchHit(h.idx)
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
// only the armed selection is cleared.
func (m Model) handleMouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || !m.textSel.dragging {
		return m, nil
	}
	m.textSel.dragging = false
	if !m.textSel.active {
		m.clearTextSel()
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
		return m, nil
	}
	if m.textSel.dragging && msg.Button == tea.MouseLeft {
		return m.dragTextSel(msg.X, msg.Y)
	}
	next := m.hoverAt(msg.X, msg.Y)
	if m.hover == next {
		return m, nil
	}
	m.hover = next
	return m, nil
}

// hoverAt resolves the pointer to a hoverable element — only team tabs and
// channel rows are tracked, so it skips the message/thread content-coordinate
// work hitTest does for clicks, keeping the per-motion hover path cheap.
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
	if x < channelsWidth && !m.onSearchTab() && !m.onFeedTab() {
		if h := m.hitChannel(y); h.zone == hitChannel {
			return hoverState{zone: hitChannel, idx: h.idx}
		}
	}
	return hoverState{}
}

// dragTextSel updates the selection head to the dragged-to cell (clamped into
// the pane) and re-renders so the highlight tracks the pointer.
func (m Model) dragTextSel(x, y int) (tea.Model, tea.Cmd) {
	pane := m.textSel.pane
	line, col, ok := m.cellToContent(pane, x, y)
	if !ok {
		return m, nil
	}
	m.textSel.headLine, m.textSel.headCol = line, col
	m.textSel.active = line != m.textSel.anchorLine || col != m.textSel.anchorCol
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

// armTextSel begins a potential text selection at the clicked cell. It stays
// inactive (no highlight) until a drag moves the head off the anchor.
func (m *Model) armTextSel(pane focus, line, col int) {
	m.textSel = textSel{
		pane:       pane,
		anchorLine: line, anchorCol: col,
		headLine: line, headCol: col,
		dragging: true,
	}
}

// selectPostAt moves the message selection to post idx and focuses the messages
// pane, clearing any wheel free-scroll so the selection follows again. Render
// is left to the caller.
func (m *Model) selectPostAt(idx int) {
	if idx < 0 || idx >= len(m.posts) {
		return
	}
	m.postIdx = idx
	m.focus = focusMessages
	m.msgScrollFree = false
}

// selectThreadPostAt is the thread-pane mirror of selectPostAt.
func (m *Model) selectThreadPostAt(idx int) {
	if !m.threadOpen || idx < 0 || idx >= len(m.threadPosts) {
		return
	}
	m.threadIdx = idx
	m.focus = focusThread
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
	// synthetic tabs; map the click to the bubble under it.
	if m.onFeedTab() {
		return m.hitFeedBubble(y)
	}
	if m.onSearchTab() {
		return m.hitSearchBubble(y)
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
			return hit{zone: hitNone} // reference panel is read-only
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
	var content string
	var ver uint64
	if pane == focusThread {
		content, ver = m.threadView.GetContent(), m.threadContentVer
	} else {
		content, ver = m.msgsView.GetContent(), m.msgsContentVer
	}
	if m.wrapIdx.pane == pane && m.wrapIdx.ver == ver && m.wrapIdx.width == width && m.wrapIdx.lines != nil {
		return m.wrapIdx.lines, m.wrapIdx.starts
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
		seg := ansi.Strip(ansi.Cut(lines[li], start, end))
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
