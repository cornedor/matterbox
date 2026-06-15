package ui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"
)

const channelsWidth = 26

// maxInputHeight bounds how many rows the input textarea is allowed to
// expand to as content grows. The textarea scrolls internally once the
// content exceeds this — the messages pane keeps the remaining space.
const maxInputHeight = 6

// Presence + custom-status glyphs. The sidebar/footer use the medium pair
// (filled •, hollow ◦); the header uses the larger, more readable pair
// (filled ●, hollow ○). Filled = online/away/dnd (coloured); hollow = offline
// (grey). customDot is the low-key "has a custom status" hint in the sidebar.
const (
	statusDot       = "•"
	statusHollowDot = "◦"
	headerStatusDot = "●"
	headerHollowDot = "○"
	customDot       = "◦"
)

var (
	border       = lipgloss.NormalBorder()
	focusedColor = lipgloss.Color("12")  // bright blue
	dimColor     = lipgloss.Color("241") // grey

	titleStyle    = lipgloss.NewStyle().Bold(true)
	userStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	timeStyle     = lipgloss.NewStyle().Foreground(dimColor)
	selectedRow   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("8"))
	unselectedRow = lipgloss.NewStyle()
	// Tab styling adapted from charmbracelet/lipgloss tabs example.
	inactiveTabBorder = tabBorderWithBottom("┴", "─", "┴")
	activeTabBorder   = tabBorderWithBottom("┘", " ", "└")
	tabBase           = lipgloss.NewStyle().Padding(0, 1)
	dmTabColor        = lipgloss.Color("5")
	mentionTabColor   = lipgloss.Color("9")  // red when there are mentions
	searchTabColor    = lipgloss.Color("6")  // cyan to set Search apart from teams
	feedTabColor      = lipgloss.Color("10") // green to set the Feed tab apart
	footerStyle       = lipgloss.NewStyle().Foreground(dimColor)
	filterStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	unreadStyle       = lipgloss.NewStyle().Bold(true)
	mentionStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true) // red

	// Presence-dot colours, the grey offline/hollow dot, and the dim
	// custom-status hint.
	statusOnlineStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))   // green
	statusAwayStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))   // yellow
	statusDndStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))   // red
	offlineStatusStyle = lipgloss.NewStyle().Foreground(dimColor)              // grey hollow dot
	customStatusStyle  = lipgloss.NewStyle().Foreground(dimColor)              // dim hollow dot
	attachmentStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))   // cyan
	selectedBarStyle   = lipgloss.NewStyle().Foreground(focusedColor)          // selected-post left bar
	replyHintStyle     = lipgloss.NewStyle().Foreground(dimColor)              // ↳ reply, ↪ N replies
	editedStyle        = lipgloss.NewStyle().Foreground(dimColor).Italic(true) // right-aligned "edited" tag
)

// tabBorderWithBottom returns a rounded border with the bottom row
// overridden to the given left/middle/right characters. Used to make
// the lipgloss-style tabs join cleanly along their bottom rule.
func tabBorderWithBottom(left, middle, right string) lipgloss.Border {
	b := lipgloss.RoundedBorder()
	b.BottomLeft = left
	b.Bottom = middle
	b.BottomRight = right
	return b
}

// tabsHeight is the vertical space the team-tab strip consumes
// (top border + label row + bottom rule).
const tabsHeight = 3

// resizeMessagesViewport re-lays-out every pane and repaints its content.
// Use it for one-shot layout changes (thread open/close, attachment-bar
// growth, input height). A live resize *drag* must NOT use it per frame —
// re-rendering every loaded post on each intermediate size is what made
// dragging lag; that path calls layoutPanes() each frame and defers
// renderAllPanes() to a settle tick (see the WindowSizeMsg handler).
func (m *Model) resizeMessagesViewport() {
	m.layoutPanes()
	m.renderAllPanes()
}

// resizeSettleDelay is how long a resize must be quiet before the deferred
// content re-render runs — long enough to coalesce a drag's WindowSizeMsg
// storm into one re-render, short enough to feel instant once the drag stops.
const resizeSettleDelay = 120 * time.Millisecond

// resizeSettleMsg fires resizeSettleDelay after a WindowSizeMsg. gen lets the
// handler ignore all but the most recent: a drag schedules one per frame and
// only the final one should trigger the expensive content re-render.
type resizeSettleMsg struct{ gen int }

func resizeSettleCmd(gen int) tea.Cmd {
	return tea.Tick(resizeSettleDelay, func(time.Time) tea.Msg {
		return resizeSettleMsg{gen: gen}
	})
}

// layoutPanes recomputes pane geometry and resizes every viewport/textarea to
// the current terminal size, without rendering any content. It's cheap enough
// to run on every frame of a resize drag: View() derives borders/scrollbars
// from these sizes, and the viewports soft-wrap their existing content until
// the deferred renderAllPanes() repaints it.
func (m *Model) layoutPanes() {
	// Match View()'s body sizing: subtract the rendered footer height
	// (1 line normally, several when full help is open) plus the tab strip.
	// The extra -1 accounts for the body pane's bottom border row (top
	// border is omitted so the pane connects to the tab strip).
	footerH := lipgloss.Height(m.renderFooter())
	bodyH := m.height - footerH - tabsHeight - 1
	if bodyH < 5 {
		bodyH = 5
	}
	rightW := m.width - channelsWidth - 2
	if rightW < 10 {
		rightW = 10
	}
	msgsW := rightW
	threadW := 0
	jiraW := 0
	// The thread sidebar and the Jira panel share the single right slot — at
	// most one is ever open — so each splits rightW the same way.
	if m.threadOpen {
		threadW = splitRightPane(rightW)
		msgsW = rightW - threadW
	} else if m.jiraOpen {
		jiraW = splitRightPane(rightW)
		msgsW = rightW - jiraW
	}
	// The scrollbar overlays the right border column (rendered by
	// renderMessagesPane / renderThreadPane), so the viewport fills the
	// inner pane width up to that column without reserving extra space.
	m.msgsView.SetWidth(msgsW - 2)
	tw := threadW - 4
	if tw < 1 {
		tw = 1
	}
	m.threadView.SetWidth(tw)
	jw := jiraW - 4
	if jw < 1 {
		jw = 1
	}
	m.jiraView.SetWidth(jw)
	// When the thread sidebar is open the compose textarea moves into
	// the thread pane; the messages pane only needs room for its title
	// + viewport, while the thread pane has to make room for the input
	// (and its attachment chip strip).
	if m.threadOpen {
		mh := bodyH - 3 - 2
		if mh < 1 {
			mh = 1
		}
		m.msgsView.SetHeight(mh)
		attBarH := m.attachmentBarHeight(threadW - 2)
		// The compose input lives in the thread pane. Cap its growth so the
		// thread view keeps at least one row, then size the view to fill the
		// space above it — the input stays pinned just above the bottom
		// border instead of floating with blank rows beneath it.
		m.capInputHeight(bodyH - 3 - attBarH)
		th := bodyH - 2 - m.input.Height() - attBarH
		if th < 1 {
			th = 1
		}
		m.threadView.SetHeight(th)
	} else {
		attBarH := m.attachmentBarHeight(msgsW - 2)
		// bodyH already excludes the tab strip + footer. Reserve the title
		// row (1) and the input's top-border row (1); cap the input so the
		// viewport keeps at least one row even on a short terminal, then give
		// the viewport every remaining row so the input sits flush above the
		// bottom border (bottom-aligned) rather than floating with a gap.
		m.capInputHeight(bodyH - 3 - attBarH)
		mh := bodyH - 2 - m.input.Height() - attBarH
		if mh < 1 {
			mh = 1
		}
		m.msgsView.SetHeight(mh)
		th := bodyH - 3 - 2
		if th < 1 {
			th = 1
		}
		m.threadView.SetHeight(th)
		// The Jira panel is read-only (no composer), so its viewport fills the
		// whole body below its title row: bodyH minus the title and the pane's
		// bottom border.
		jh := bodyH - 2
		if jh < 1 {
			jh = 1
		}
		m.jiraView.SetHeight(jh)
	}
	if m.historyMode {
		m.sizeHistoryView()
	}
	if m.keysSheetMode {
		m.sizeKeysSheetView()
	}
	if m.summary.phase == summaryStreaming || m.summary.phase == summaryDone {
		m.sizeSummaryView()
	}
	m.sizeSearchView(m.width, bodyH)
	m.sizeFeedView(m.width, bodyH)
}

// renderAllPanes repaints the content of every pane after a layout change.
// This is the expensive half of a resize — renderMessages re-renders every
// loaded post — so the live-drag path defers it to a settle tick rather than
// running it on each intermediate size.
func (m *Model) renderAllPanes() {
	if m.historyMode {
		m.renderHistory()
	}
	if m.keysSheetMode {
		m.renderKeysSheet()
	}
	if m.summary.phase == summaryStreaming || m.summary.phase == summaryDone {
		m.renderSummaryViewBody()
	}
	m.renderSearchResults()
	m.renderFeedResults()
	m.renderMessages()
	m.renderThread()
	m.renderJira()
}

const threadPaneMinWidth = 24

// splitRightPane returns the width of the right detail pane (thread or Jira)
// when the right area is rightW wide: half, clamped so neither the detail pane
// nor the messages pane drops below threadPaneMinWidth.
func splitRightPane(rightW int) int {
	w := rightW / 2
	if w < threadPaneMinWidth {
		w = threadPaneMinWidth
	}
	if w > rightW-threadPaneMinWidth {
		w = rightW - threadPaneMinWidth
	}
	return w
}

func (m *Model) resizeInput() {
	if m.threadOpen {
		// Input lives under the thread pane when it's open. Mirror the
		// thread-width split used in resizeMessagesViewport so the
		// textarea spans the same inner width as the chip strip above it.
		rightW := m.width - channelsWidth - 2
		if rightW < 10 {
			rightW = 10
		}
		threadW := rightW / 2
		if threadW < threadPaneMinWidth {
			threadW = threadPaneMinWidth
		}
		if threadW > rightW-threadPaneMinWidth {
			threadW = rightW - threadPaneMinWidth
		}
		w := threadW - 2
		if w < 10 {
			w = 10
		}
		m.input.SetWidth(w)
		return
	}
	if m.jiraOpen {
		// The composer stays under the (now narrower) messages pane; match its
		// width to the Jira split so the input doesn't overhang the pane above.
		rightW := m.width - channelsWidth - 2
		if rightW < 10 {
			rightW = 10
		}
		w := rightW - splitRightPane(rightW) - 2
		if w < 10 {
			w = 10
		}
		m.input.SetWidth(w)
		return
	}
	w := m.width - channelsWidth - 4
	if w < 10 {
		w = 10
	}
	m.input.SetWidth(w)
}

// syncInputHeight reflows the messages viewport when the input textarea's
// height has changed. The textarea sizes itself (1..maxInputHeight rows) via
// v2's DynamicHeight, which counts *wrapped* visual rows — so this must not
// re-derive the height from LineCount() (logical lines), or a soft-wrapped
// line would be forced back to a single row and its first line would scroll
// out of view. We only keep the messages pane sized to the space the input
// leaves behind. Safe to call after every keystroke; it short-circuits when
// the height is unchanged so renderMessages doesn't churn for every character.
func (m *Model) syncInputHeight() {
	h := m.input.Height()
	if h == m.lastInputHeight {
		return
	}
	m.lastInputHeight = h
	m.resizeMessagesViewport()
}

// capInputHeight bounds how tall the compose textarea may grow to the space
// actually available in its pane, clamped to [1, maxInputHeight]. Without
// this the input would keep growing on a short terminal until it pushed the
// footer — and then its own content — off-screen. DynamicHeight honours the
// new MaxHeight (scrolling internally past it), and we re-clamp the current
// height so the same resize that shrank the cap doesn't leave a stale taller
// input behind.
func (m *Model) capInputHeight(avail int) {
	if avail < 1 {
		avail = 1
	}
	if avail > maxInputHeight {
		avail = maxInputHeight
	}
	m.input.MaxHeight = avail
	if m.input.Height() > avail {
		m.input.SetHeight(avail)
	}
}

func (m *Model) renderMessages() {
	// New content generation: invalidates the messages scroll-geometry cache
	// (see scrollcache.go). Bump unconditionally — every path below resets the
	// viewport content.
	m.msgsContentVer++
	if len(m.posts) == 0 {
		m.msgsView.SetContent(lipgloss.NewStyle().Foreground(dimColor).Render("no messages"))
		return
	}
	// Clamp the selection in case posts were deleted out from under it.
	if m.postIdx >= len(m.posts) {
		m.postIdx = len(m.posts) - 1
	}
	if m.postIdx < 0 {
		m.postIdx = 0
	}

	// Only show the selection bar when the messages pane is focused —
	// otherwise the bar would mislead about what keys would act on.
	decorate := m.focus == focusMessages
	bar := selectedBarStyle.Render("▎")
	width := m.msgsView.Width()
	var allLines []string
	// The viewport has SoftWrap on, so YOffset is in visual rows
	// (post-wrap). Accumulate each post's cached visual-row count as we go
	// so we can place the selection without re-measuring every line on
	// each keystroke (visAcc is the running visual-row offset).
	selVisStart, selVisRows, visAcc := -1, 0, 0
	for i, p := range m.posts {
		var prev *model.Post
		if i > 0 {
			prev = m.posts[i-1]
		}
		chunk, rows := m.renderPostLines(p, m.groupWithPrev(p, prev, false))
		if i == m.postIdx {
			selVisStart = visAcc
			if decorate {
				// Don't mutate the cached slice; copy before decorating.
				decorated := make([]string, len(chunk))
				for j, l := range chunk {
					// Replace the two-space gutter with the bar so the
					// selected post's lines stay at the same x-position.
					if strings.HasPrefix(l, "  ") {
						decorated[j] = bar + " " + l[2:]
					} else {
						decorated[j] = bar + " " + l
					}
				}
				chunk = decorated
				// The bar widens non-gutter (header) lines by two cells, so
				// recount the decorated chunk rather than trust the cached
				// undecorated row count.
				rows = postVisualRows(chunk, width)
			}
			selVisRows = rows
		}
		allLines = append(allLines, chunk...)
		visAcc += rows
	}
	m.msgsView.SetContent(strings.Join(allLines, "\n"))

	if h := m.msgsView.Height(); h > 0 && selVisStart >= 0 {
		visStart := selVisStart
		visEnd := selVisStart + selVisRows
		off := m.msgsView.YOffset()
		switch {
		case m.anchorMsgSelTop:
			off = visStart
		case m.anchorMsgSelBottom:
			off = visEnd - h
		case visStart < off:
			off = visStart
		case visEnd > off+h:
			off = visEnd - h
		}
		if off < 0 {
			off = 0
		}
		m.msgsView.SetYOffset(off)
	}
	m.anchorMsgSelTop = false
	m.anchorMsgSelBottom = false
}

// renderThread populates the thread viewport with the loaded thread
// posts, mirroring renderMessages' selection-bar behaviour for the
// focused row.
func (m *Model) renderThread() {
	if !m.threadOpen {
		return
	}
	// New content generation: invalidates the thread scroll-geometry cache.
	m.threadContentVer++
	if m.threadLoading && len(m.threadPosts) == 0 {
		m.threadView.SetContent(lipgloss.NewStyle().Foreground(dimColor).Render("loading…"))
		return
	}
	if len(m.threadPosts) == 0 {
		m.threadView.SetContent(lipgloss.NewStyle().Foreground(dimColor).Render("no messages"))
		return
	}
	if m.threadIdx >= len(m.threadPosts) {
		m.threadIdx = len(m.threadPosts) - 1
	}
	if m.threadIdx < 0 {
		m.threadIdx = 0
	}
	decorate := m.focus == focusThread
	bar := selectedBarStyle.Render("▎")
	width := m.threadView.Width()
	var allLines []string
	selVisStart, selVisRows, visAcc := -1, 0, 0
	for i, p := range m.threadPosts {
		var prev *model.Post
		if i > 0 {
			prev = m.threadPosts[i-1]
		}
		chunk, rows := m.renderThreadPostLines(p, i == 0, m.groupWithPrev(p, prev, true))
		if i == m.threadIdx {
			selVisStart = visAcc
			if decorate {
				// Don't mutate the cached slice; copy before decorating.
				decorated := make([]string, len(chunk))
				for j, l := range chunk {
					if strings.HasPrefix(l, "  ") {
						decorated[j] = bar + " " + l[2:]
					} else {
						decorated[j] = bar + " " + l
					}
				}
				chunk = decorated
				rows = postVisualRows(chunk, width)
			}
			selVisRows = rows
		}
		allLines = append(allLines, chunk...)
		visAcc += rows
	}
	m.threadView.SetContent(strings.Join(allLines, "\n"))

	if h := m.threadView.Height(); h > 0 && selVisStart >= 0 {
		visStart := selVisStart
		visEnd := selVisStart + selVisRows
		off := m.threadView.YOffset()
		switch {
		case visStart < off:
			off = visStart
		case visEnd > off+h:
			off = visEnd - h
		}
		if off < 0 {
			off = 0
		}
		m.threadView.SetYOffset(off)
	}
}

// wrapBodyLine wraps a single rendered line to fit within width while
// preserving its two-space left gutter on wrapped continuation rows.
// ANSI escape codes are preserved across the wrap. Lines without the
// gutter (headers) or that already fit are returned as a single element.
func wrapBodyLine(line string, width int) []string {
	if width < 4 || lipgloss.Width(line) <= width {
		return []string{line}
	}
	const indent = "  "
	if !strings.HasPrefix(line, indent) {
		return []string{line}
	}
	wrapped := ansi.Wrap(line[len(indent):], width-len(indent), "")
	parts := strings.Split(wrapped, "\n")
	for i, p := range parts {
		parts[i] = indent + p
	}
	return parts
}

// visualRowsBefore returns the number of visual rows the first n lines
// of `lines` take up when soft-wrapped at maxWidth — mirroring how
// bubbles/viewport counts rows under SoftWrap. It is used to translate
// logical line indices into the visual-row coordinate space that
// YOffset operates in. Returns n unchanged when maxWidth is not yet
// known (e.g. before the first resize), so callers fall back to the
// pre-soft-wrap behaviour rather than collapsing scroll to 0.
func visualRowsBefore(lines []string, n, maxWidth int) int {
	if n <= 0 {
		return 0
	}
	if n > len(lines) {
		n = len(lines)
	}
	if maxWidth <= 0 {
		return n
	}
	rows := 0
	for i := 0; i < n; i++ {
		w := lipgloss.Width(lines[i])
		if w <= maxWidth {
			rows++
			continue
		}
		rows += (w + maxWidth - 1) / maxWidth
	}
	return rows
}

// postVisualRows is visualRowsBefore over a whole post chunk: the number
// of soft-wrapped visual rows its lines occupy at the given width. Cached
// per post (postLineCacheEntry.rows) so renderMessages can sum prefix
// heights without re-measuring every line's width on each keystroke.
func postVisualRows(lines []string, width int) int {
	return visualRowsBefore(lines, len(lines), width)
}

var scrollbarThumbStyle = lipgloss.NewStyle().Foreground(focusedColor)

// renderRightBorder builds a 1-column wide string `outerH` rows tall
// that serves as the merged right border + scrollbar for a bordered
// pane rendered with .UnsetBorderRight(). The first outerH-1 rows are
// the content-area rows, the last row is the bottom-right corner so it
// aligns with the box's bottom border row.
//
// When showScrollbar is true and the viewport content overflows, a
// proportional thumb is painted at the rows corresponding to the
// viewport (mapped via vpTop / vpHeight). Otherwise every content row
// renders as the regular border `│` in borderColor, so visually the
// scrollbar is hidden — the pane looks like it has a normal right
// border.
func renderRightBorder(outerH, vpTop, vpHeight int, totalRows int, percent float64, borderColor color.Color, showScrollbar bool) string {
	if outerH < 1 {
		outerH = 1
	}
	borderStyle := lipgloss.NewStyle().Foreground(borderColor)
	track := borderStyle.Render("│")
	corner := borderStyle.Render("┘")

	lines := make([]string, outerH)
	for i := 0; i < outerH-1; i++ {
		lines[i] = track
	}
	lines[outerH-1] = corner

	if showScrollbar && vpHeight > 0 && totalRows > vpHeight {
		thumb := vpHeight * vpHeight / totalRows
		if thumb < 1 {
			thumb = 1
		}
		if thumb > vpHeight {
			thumb = vpHeight
		}
		avail := vpHeight - thumb
		pos := int(float64(avail)*percent + 0.5)
		if pos < 0 {
			pos = 0
		}
		if pos > avail {
			pos = avail
		}
		thumbCell := scrollbarThumbStyle.Render("█")
		for i := pos; i < pos+thumb; i++ {
			idx := vpTop + i
			if idx >= 0 && idx < outerH-1 {
				lines[idx] = thumbCell
			}
		}
	}
	return strings.Join(lines, "\n")
}

// viewportVisualRows counts the visual rows of a soft-wrapped viewport's
// current content. The viewport's TotalLineCount is in logical lines, so
// we re-walk the lines here applying the same wrap math used elsewhere.
func viewportVisualRows(content string, width int) int {
	if content == "" {
		return 0
	}
	lines := strings.Split(content, "\n")
	return visualRowsBefore(lines, len(lines), width)
}

// withEditedTag right-aligns a dim "edited" tag on the same line as
// the rendered header, padding with spaces to the available width.
// Returns the header unchanged when the post wasn't edited or there's
// no room left after the existing header content.
//
// We pad to width-2 rather than width because renderMessages prepends a
// 2-col selection bar ("▎ ") to header lines of the focused post — if
// we used the full width here, the bar would push the line 2 cols past
// the viewport edge and "edited" would wrap to the next line.
func withEditedTag(header string, p *model.Post, width int) string {
	if p.EditAt == 0 || width <= 0 {
		return header
	}
	const tag = "edited"
	target := width - 2 // reserve room for the potential selection bar
	used := lipgloss.Width(header)
	// Need at least one space between the existing content and the tag.
	if used+1+len(tag) > target {
		return header
	}
	pad := target - used - len(tag)
	return header + strings.Repeat(" ", pad) + editedStyle.Render(tag)
}

// formatPostTime returns a header timestamp for a post. Posts created
// within the last 24 hours show only the time ("15:04"); older posts
// include a short date in front so the user can tell at a glance how
// far back they're reading. Cross-year posts include the year too so
// "Jan 5 15:04" can't quietly mean either of two different Januaries.
func formatPostTime(createAtMillis int64) string {
	t := time.UnixMilli(createAtMillis).Local()
	now := time.Now()
	if now.Sub(t) < 24*time.Hour {
		return t.Format("15:04")
	}
	if t.Year() != now.Year() {
		return t.Format("Jan 2 2006 15:04")
	}
	return t.Format("Jan 2 15:04")
}

// renderThreadPostLines is the thread-sidebar variant of
// renderPostLines: the root post gets no ↳/↪ hint (it IS the root),
// replies omit the reply hint since context makes it obvious. grouped
// suppresses the name/time header for a reply continuing the author run above
// it (the root, isRoot, always keeps its header — see groupWithPrev).
func (m *Model) renderThreadPostLines(p *model.Post, isRoot, grouped bool) ([]string, int) {
	width := m.threadView.Width()
	poll := isPoll(p)
	var fp string
	if !poll && p.Id != "" {
		fp = m.postLineFingerprint(p, width, true, isRoot, grouped)
		if cached, rows, ok := m.cachedPostLines(p, fp); ok {
			return cached, rows
		}
	}
	var lines []string
	if !grouped {
		name := m.postAuthorName(p)
		ts := formatPostTime(p.CreateAt)
		header := userStyle.Render(name) + "  " + timeStyle.Render(ts)
		if isRoot {
			header += "  " + replyHintStyle.Render("· root")
		}
		header = withEditedTag(header, p, width)
		lines = append(lines, header)
	}
	if body := m.markdownBody(p); body != "" {
		for _, l := range strings.Split(body, "\n") {
			lines = append(lines, wrapBodyLine(l, width)...)
		}
	}
	if poll {
		selected := m.focus == focusThread && m.threadIdx >= 0 && m.threadIdx < len(m.threadPosts) && m.threadPosts[m.threadIdx] == p
		for _, l := range m.renderPoll(p, width, selected) {
			lines = append(lines, wrapBodyLine(l, width)...)
		}
	}
	if att := renderAttachments(p, width); att != "" {
		for _, l := range strings.Split(att, "\n") {
			lines = append(lines, wrapBodyLine(l, width)...)
		}
	}
	if rx := m.renderReactions(p); rx != "" {
		lines = append(lines, wrapBodyLine(rx, width)...)
	}
	// See renderPostLines: keep a grouped, otherwise-empty post visible.
	if len(lines) == 0 {
		lines = append(lines, "  ")
	}
	rows := postVisualRows(lines, width)
	if !poll && p.Id != "" {
		m.putPostLines(p.Id, fp, lines, rows)
	}
	return lines, rows
}

// groupWithPrev reports whether post cur should render as a bare continuation
// of prev — without repeating the author name and timestamp — because it
// belongs to the same uninterrupted run: same author, sent within
// m.groupWindow of prev, and carrying no thread or edited affordance that the
// header alone conveys. prev is the post rendered immediately above cur (nil
// at the top of the loaded window, so the run always starts with a header).
// inThread relaxes the inline-reply guard: in the thread sidebar every post
// but the root is a reply and shows no ↳ hint, so replies there may still
// group under one another (the root, handled by the caller, always keeps its
// header).
func (m *Model) groupWithPrev(cur, prev *model.Post, inThread bool) bool {
	if m.groupWindow <= 0 || cur == nil || prev == nil {
		return false
	}
	// Same person — compare the resolved display name too, so a human and a
	// bot posting under one UserId with different override_username names stay
	// visually separate.
	if cur.UserId != prev.UserId || m.postAuthorName(cur) != m.postAuthorName(prev) {
		return false
	}
	// Within the window, and not out of order: a clock-skewed older post keeps
	// its own header rather than hiding under a newer one.
	gap := cur.CreateAt - prev.CreateAt
	if gap < 0 || time.Duration(gap)*time.Millisecond > m.groupWindow {
		return false
	}
	// An edited message keeps its header so the right-aligned "edited" tag
	// (which lives on the header line) stays visible.
	if cur.EditAt != 0 {
		return false
	}
	if inThread {
		return true
	}
	// In the main pane, never merge across a thread affordance: a reply shows a
	// leading ↳ and a thread root a trailing ↪ N, both anchored on the header.
	// Keep the header whenever either post carries one.
	if cur.RootId != "" || prev.RootId != "" || cur.ReplyCount > 0 {
		return false
	}
	return true
}

// renderPostLines returns one rendered line per visual row of a post:
// the header line, the (possibly multi-line) body, and one line per
// attachment. Existing styles already include a two-space left gutter
// on body and attachment lines. When grouped is true the post continues
// the author run above it, so the name/time header is omitted and the body
// starts straight away (see groupWithPrev).
func (m *Model) renderPostLines(p *model.Post, grouped bool) ([]string, int) {
	width := m.msgsView.Width()
	poll := isPoll(p)
	var fp string
	if !poll && p.Id != "" {
		fp = m.postLineFingerprint(p, width, false, false, grouped)
		if cached, rows, ok := m.cachedPostLines(p, fp); ok {
			return cached, rows
		}
	}
	var lines []string
	if !grouped {
		name := m.postAuthorName(p)
		ts := formatPostTime(p.CreateAt)
		header := userStyle.Render(name) + "  " + timeStyle.Render(ts)
		if p.RootId != "" {
			header = replyHintStyle.Render("↳ ") + header
		} else if p.ReplyCount > 0 {
			header += "  " + replyHintStyle.Render(fmt.Sprintf("↪ %d", p.ReplyCount))
		}
		header = withEditedTag(header, p, width)
		lines = append(lines, header)
	}
	if body := m.markdownBody(p); body != "" {
		for _, l := range strings.Split(body, "\n") {
			lines = append(lines, wrapBodyLine(l, width)...)
		}
	}
	if poll {
		selected := m.focus == focusMessages && m.postIdx >= 0 && m.postIdx < len(m.posts) && m.posts[m.postIdx] == p
		for _, l := range m.renderPoll(p, width, selected) {
			lines = append(lines, wrapBodyLine(l, width)...)
		}
	}
	if att := renderAttachments(p, width); att != "" {
		for _, l := range strings.Split(att, "\n") {
			lines = append(lines, wrapBodyLine(l, width)...)
		}
	}
	if rx := m.renderReactions(p); rx != "" {
		lines = append(lines, wrapBodyLine(rx, width)...)
	}
	// A grouped post with no body, attachments, reactions, or poll would render
	// as zero lines and silently vanish (breaking selection geometry). Keep one
	// blank continuation row so it stays visible and selectable, matching the
	// single header line it would otherwise have shown.
	if len(lines) == 0 {
		lines = append(lines, "  ")
	}
	rows := postVisualRows(lines, width)
	if !poll && p.Id != "" {
		m.putPostLines(p.Id, fp, lines, rows)
	}
	return lines, rows
}

// normalizeFilename strips zero-width hint characters that some terminals
// render as visible cells (notably U+00AD SOFT HYPHEN, which macOS injects
// into screenshot filenames as "Scherm­afbeelding"). lipgloss reports
// width 0 for these, but if the terminal renders them at width 1 the
// attachment line silently overflows the viewport and the bordered
// layout shifts.
func normalizeFilename(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case 0x00AD, // SOFT HYPHEN
			0x200B, // ZERO WIDTH SPACE
			0x200C, // ZERO WIDTH NON-JOINER
			0x200D, // ZERO WIDTH JOINER
			0xFEFF: // ZERO WIDTH NO-BREAK SPACE (BOM)
			return -1
		}
		return r
	}, s)
}

// renderAttachments returns one indented line per file attached to the
// post (icon + name + size, plus dimensions for images). Empty string
// if the post has no files in its metadata. maxWidth bounds each
// rendered line so the terminal never wraps it past the viewport.
func renderAttachments(p *model.Post, maxWidth int) string {
	if p.Metadata == nil || len(p.Metadata.Files) == 0 {
		return ""
	}
	lines := make([]string, 0, len(p.Metadata.Files))
	for _, f := range p.Metadata.Files {
		icon := "📎"
		var info string
		if strings.HasPrefix(f.MimeType, "image/") {
			icon = "🖼️"
			if f.Width > 0 && f.Height > 0 {
				info = fmt.Sprintf(" (%d×%d, %s)", f.Width, f.Height, humanSize(f.Size))
			} else {
				info = " (" + humanSize(f.Size) + ")"
			}
		} else {
			info = " (" + humanSize(f.Size) + ")"
		}
		name := normalizeFilename(f.Name)
		// Reserve room for the two-space gutter, the icon+space prefix,
		// and the trailing info; truncate the name so the whole line
		// fits within maxWidth.
		fixed := lipgloss.Width("  ") + lipgloss.Width(icon+" ") + lipgloss.Width(info)
		if maxWidth > fixed {
			name = truncate(name, maxWidth-fixed)
		}
		lines = append(lines, "  "+attachmentStyle.Render(icon+" "+name+info))
	}
	return strings.Join(lines, "\n")
}

func humanSize(n int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1fGB", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.1fMB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1fKB", float64(n)/kb)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// View implements the v2 tea.Model interface. Content is built as a
// styled string the same way it was in v1; AltScreen is set per-frame
// (v2 replaces the v1 tea.WithAltScreen() startup option with this
// per-View field).
//
// v2 always requests the kitty "disambiguate escape codes" flag, which
// is enough to make shift+enter arrive as a distinct keypress on
// capable terminals while leaving normal text input (including shifted
// characters like '?' from shift+/) untouched.
func (m Model) View() tea.View {
	v := tea.NewView(m.viewContent())
	v.AltScreen = true
	return v
}

func (m *Model) viewContent() string {
	if m.width == 0 || m.height == 0 {
		return "starting…"
	}

	// Render footer first so we know its height — full-help mode expands
	// it from a single line to several, and the body has to shrink to fit.
	footer := m.renderFooter()
	footerH := lipgloss.Height(footer)
	bodyH := m.height - footerH - tabsHeight
	if bodyH < 5 {
		bodyH = 5
	}

	var body string
	if m.onSearchTab() {
		body = m.renderSearchPane(bodyH, m.width)
	} else if m.onFeedTab() {
		body = m.renderFeedPane(bodyH, m.width)
	} else {
		channelsPane := m.renderChannelsPane(bodyH)
		rightW := m.width - channelsWidth
		if rightW < 10 {
			rightW = 10
		}
		msgsW := rightW
		threadW := 0
		jiraW := 0
		if m.threadOpen {
			threadW = splitRightPane(rightW)
			msgsW = rightW - threadW
		} else if m.jiraOpen {
			jiraW = splitRightPane(rightW)
			msgsW = rightW - jiraW
		}
		messagesPane := m.renderMessagesPane(bodyH, msgsW)
		panes := []string{channelsPane, messagesPane}
		if m.threadOpen {
			panes = append(panes, m.renderThreadPane(bodyH, threadW))
		} else if m.jiraOpen {
			panes = append(panes, m.renderJiraPane(bodyH, jiraW))
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, panes...)
	}
	if m.switcherMode {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderSwitcher(bodyH))
	}
	if m.historyMode {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderHistoryPopup())
	}
	if m.keysSheetMode {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderKeysSheetPopup())
	}
	if m.keyDebugMode {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderKeyDebugPopup())
	}
	if m.deleteConfirmPostID != "" {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderDeleteConfirm())
	}
	if m.reactionPickerPostID != "" {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderReactionPicker())
	}
	if m.jiraPicker.active {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderJiraPicker(bodyH))
	}
	if m.jiraPointsActive {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderJiraPointsInput())
	}
	if m.openPickerActive() {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderOpenPicker())
	}
	if m.pollDialog.open {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderPollDialog())
	}
	if m.summary.active() {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderSummaryPopup())
	}
	if m.preview.active {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderPreviewPopup())
	}

	tabs := m.renderTeamTabs()

	return lipgloss.JoinVertical(lipgloss.Left, tabs, body, footer)
}

// statusGlyph maps a presence string to a glyph + style: the filled glyph in
// the status colour for online/away/dnd, or the hollow glyph in grey for
// offline/unknown. The caller supplies the glyph pair so the sidebar/footer
// (medium) and header (large) can size it differently.
func statusGlyph(status, filled, hollow string) (string, lipgloss.Style) {
	switch status {
	case model.StatusOnline:
		return filled, statusOnlineStyle
	case model.StatusAway:
		return filled, statusAwayStyle
	case model.StatusDnd:
		return filled, statusDndStyle
	default:
		return hollow, offlineStatusStyle
	}
}

// dmStatusDot returns the presence glyph + style for a DM channel's partner.
// ok is false only for non-DM channels and unresolvable partners; offline
// partners get the grey hollow dot.
func (m Model) dmStatusDot(c *model.Channel, filled, hollow string) (string, lipgloss.Style, bool) {
	id := m.dmPartnerID(c)
	if id == "" {
		return "", lipgloss.Style{}, false
	}
	glyph, st := statusGlyph(m.statuses[id], filled, hollow)
	return glyph, st, true
}

// myStatusDot returns the logged-in user's own presence glyph + style for the
// footer. ok is false until the current user is known.
func (m Model) myStatusDot(filled, hollow string) (string, lipgloss.Style, bool) {
	if m.me == nil {
		return "", lipgloss.Style{}, false
	}
	glyph, st := statusGlyph(m.statuses[m.me.Id], filled, hollow)
	return glyph, st, true
}

// dmCustomStatus returns a DM partner's custom status when the feature is
// enabled, the partner has one set, and it hasn't expired. ok is false
// otherwise.
func (m Model) dmCustomStatus(c *model.Channel) (model.CustomStatus, bool) {
	if !m.showCustomStatus {
		return model.CustomStatus{}, false
	}
	id := m.dmPartnerID(c)
	if id == "" {
		return model.CustomStatus{}, false
	}
	cs, ok := m.customStatuses[id]
	if !ok {
		return model.CustomStatus{}, false
	}
	// A zero ExpiresAt means "no expiry"; otherwise drop it once past due.
	if !cs.ExpiresAt.IsZero() && !cs.ExpiresAt.After(time.Now()) {
		return model.CustomStatus{}, false
	}
	return cs, true
}

func (m *Model) renderChannelsPane(height int) string {
	innerH := height - 1 // bottom border row (top connects to tab strip)
	if innerH < 1 {
		innerH = 1
	}

	vis := m.visibleChannels()

	// Header line: title or filter input.
	var header string
	if m.filterMode {
		header = filterStyle.Render(m.filter.View())
	} else if m.filterValue != "" {
		header = filterStyle.Render("f " + m.filterValue)
	} else {
		title := "Channels"
		if m.currentTeamID() == dmTeamID {
			title = "DMs"
		}
		header = titleStyle.Render(title)
	}

	listH := innerH - 1
	if listH < 1 {
		listH = 1
	}

	// scroll window
	off := m.chanOff
	if m.channelIdx < off {
		off = m.channelIdx
	}
	if m.channelIdx >= off+listH {
		off = m.channelIdx - listH + 1
	}
	if off < 0 {
		off = 0
	}
	m.chanOff = off // safe: m is a value copy, no observable effect; keeps local math tidy

	// The sidebar is fully re-styled below (grapheme-width measurement per row);
	// skip it when nothing it reads has changed since the last render — the
	// common case while typing in the composer. The fingerprint covers every
	// per-row input (see channelsFingerprint).
	fp := m.channelsFingerprint(vis, off, listH, innerH, header)
	if c := m.vcache; c != nil && c.sidebar.valid && c.sidebar.fp == fp {
		return c.sidebar.rendered
	}

	rows := []string{header}
	for i := off; i < len(vis) && len(rows) <= listH; i++ {
		ch := vis[i]
		mentionN := m.mentions[ch.Id]
		unreadN := m.unread[ch.Id]
		var suffix, badgeText string
		var badgeStyle lipgloss.Style
		switch {
		case mentionN > 0:
			badgeText = " " + strconv.Itoa(mentionN) + "!"
			badgeStyle = mentionStyle
		case unreadN > 0:
			badgeText = " " + strconv.Itoa(unreadN)
			badgeStyle = unreadStyle
		}
		// Custom-status hint: a dim hollow dot after the name (DMs only). It
		// eats into the label-truncation budget so the row width is unchanged.
		mark := ""
		if _, ok := m.dmCustomStatus(ch); ok {
			mark = " " + customStatusStyle.Render(customDot)
		}
		labelText := truncate(m.channelLabel(ch), channelsWidth-4-lipgloss.Width(badgeText)-lipgloss.Width(mark))
		switch {
		case mentionN > 0:
			suffix = mentionStyle.Render(labelText) + mark + badgeStyle.Render(badgeText)
		case unreadN > 0:
			suffix = unreadStyle.Render(labelText) + mark + badgeStyle.Render(badgeText)
		default:
			suffix = labelText + mark
		}
		// Presence dot in the left gutter (DMs only): filled+coloured when
		// active, grey hollow when offline. The dot is 1 cell, so the
		// two-column gutter and the truncation math above are unaffected.
		gutter := "  "
		if glyph, st, ok := m.dmStatusDot(ch, statusDot, statusHollowDot); ok {
			gutter = st.Render(glyph) + " "
		}
		row := gutter + suffix
		// The sidebar isn't focusable; always mark the current channel so the
		// user can see where ctrl-nav (and the open transcript) is pointing.
		// The "> " cursor overlays the presence dot on the selected row.
		if i == m.channelIdx {
			row = selectedRow.Width(channelsWidth - 2).Render("> " + suffix)
		}
		rows = append(rows, row)
	}
	if len(vis) == 0 {
		rows = append(rows, footerStyle.Render("  (none)"))
	}
	for len(rows) < innerH {
		rows = append(rows, "")
	}

	// Border stays dim: the pane is a ctrl-driven selector, never a Tab focus.
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().
		Width(channelsWidth).Height(innerH).BorderForeground(dimColor)
	out := style.Render(strings.Join(rows, "\n"))
	if c := m.vcache; c != nil {
		c.sidebar = sidebarCache{fp: fp, rendered: out, valid: true}
	}
	return out
}

func (m *Model) renderMessagesPane(height, width int) string {
	// renderChannelsPane pads its content to height rows, which overflows
	// the inner area (height-1) and makes lipgloss extend the box by one
	// row; that pane ends up `height` rows tall. We need to match that so
	// the bottom borders align, hence Height(height) here (not height-1).
	innerH := height
	if innerH < 1 {
		innerH = 1
	}
	if width < 10 {
		width = 10
	}

	// Title reflects the open channel (what m.posts holds), not the
	// sidebar cursor — they diverge when the selection moves without
	// opening a new channel.
	title := "Messages"
	titleRendered := ""
	if ch := m.findChannel(m.openChannelID); ch != nil {
		title = m.channelLabel(ch)
		titleRendered = titleStyle.Render(title)
		// Presence dot after the username (the larger glyph reads better
		// here), then the full custom status (emoji + text) when set.
		if glyph, st, ok := m.dmStatusDot(ch, headerStatusDot, headerHollowDot); ok {
			titleRendered += " " + st.Render(glyph)
		}
		if cs, ok := m.dmCustomStatus(ch); ok {
			if cs.Emoji != "" {
				titleRendered += " " + m.renderEmojiGlyph(cs.Emoji)
			}
			if cs.Text != "" {
				titleRendered += " " + cs.Text
			}
		}
	}
	if titleRendered == "" {
		titleRendered = titleStyle.Render(title)
	}

	// Shrink the messages viewport (on this local copy of m) to make
	// room for the @-mention / :emoji popup when it's open. The mutation
	// is scoped to this render call — no side effect on the real model.
	// Only one trigger is ever active at a time, so prefer whichever has
	// candidates.
	popup := m.renderMentionPopup()
	if popup == "" {
		popup = m.renderEmojiPopup()
	}
	if popup != "" {
		popupH := lipgloss.Height(popup)
		h := m.msgsView.Height() - popupH
		if h < 1 {
			h = 1
		}
		m.msgsView.SetHeight(h)
	}
	// Scrollbar geometry (total wrapped rows + scroll percent) is an O(content)
	// width-measuring walk; cache it across renders so typing in the composer
	// doesn't re-measure unchanged content every keystroke. One call replaces
	// the previous viewportVisualRows + two ScrollPercent passes.
	totalRows, scrollPct := m.msgsScrollGeom()
	showScrollbar := totalRows > m.msgsView.Height() && scrollPct < 1.0

	// Clamp the header to the pane's inner width so a long custom status can't
	// wrap to a second row (which would offset the scrollbar's row math).
	parts := []string{ansi.Truncate(titleRendered, width-2, "…"), m.msgsView.View()}
	if popup != "" {
		parts = append(parts, popup)
	}
	// The compose textarea + attachment chip strip live in whichever pane
	// is currently accepting replies: the thread sidebar when it's open,
	// the messages pane otherwise.
	if !m.threadOpen {
		if bar := m.renderAttachmentBar(width - 2); bar != "" {
			parts = append(parts, bar)
		}
		parts = append(parts, m.renderInputBox(width-2))
	}
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	highlighted := m.focus == focusMessages
	if !m.threadOpen && (m.focus == focusInput || m.focus == focusAttachments) {
		highlighted = true
	}
	borderColor := dimColor
	if highlighted {
		borderColor = focusedColor
	}

	// lipgloss v2 changed Width() semantics: it now sets the OUTER box
	// (border included) instead of the content area. The right edge is
	// painted by renderRightBorder (so the scrollbar can replace the
	// regular `│` when scrolled), so we omit the right border here and
	// pass width-1 — JoinHorizontal with the 1-col right border brings
	// the total back to `width`.
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().
		Width(width - 1).Height(innerH).BorderForeground(borderColor)
	box := style.Render(content)

	// Title row is at index 0, viewport at index 1.
	rightBorder := renderRightBorder(innerH, 1, m.msgsView.Height(), totalRows, scrollPct, borderColor, showScrollbar)
	return lipgloss.JoinHorizontal(lipgloss.Top, box, rightBorder)
}

// renderInputBox renders the compose textarea with a top rule, sized to
// fit `width` columns. Border colour mirrors focus. Used by both the
// messages pane (thread closed) and the thread pane (thread open).
func (m *Model) renderInputBox(width int) string {
	inputBorder := dimColor
	if m.focus == focusInput {
		inputBorder = focusedColor
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(inputBorder).
		Width(width).
		Render(m.input.View())
}

// attachmentBarHeight returns the rendered height of the chip strip
// (0 when empty). Used by resizeMessagesViewport so the messages
// viewport shrinks to make room for chips.
func (m *Model) attachmentBarHeight(width int) int {
	bar := m.renderAttachmentBar(width)
	if bar == "" {
		return 0
	}
	return lipgloss.Height(bar)
}

func (m *Model) renderThreadPane(height, width int) string {
	// Match renderChannelsPane's effective outer height (it overflows its
	// padding by 1, extending the box) so bottom borders align.
	innerH := height
	if innerH < 1 {
		innerH = 1
	}
	if width < threadPaneMinWidth {
		width = threadPaneMinWidth
	}

	title := "Thread"
	if m.threadLoading {
		title = "Thread (loading…)"
	} else if n := len(m.threadPosts) - 1; n > 0 {
		title = fmt.Sprintf("Thread · %d %s", n, replyWord(n))
	}

	threadTotal, threadPct := m.threadScrollGeom()
	showScrollbar := threadTotal > m.threadView.Height() && threadPct < 1.0

	parts := []string{titleStyle.Render(title), m.threadView.View()}
	if bar := m.renderAttachmentBar(width - 2); bar != "" {
		parts = append(parts, bar)
	}
	parts = append(parts, m.renderInputBox(width-2))
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	borderColor := dimColor
	if m.focus == focusThread || m.focus == focusInput || m.focus == focusAttachments {
		borderColor = focusedColor
	}

	// Right edge handled by renderRightBorder so the scrollbar can
	// overlay it (see renderMessagesPane for details).
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().
		Width(width - 1).Height(innerH).BorderForeground(borderColor)
	box := style.Render(content)

	rightBorder := renderRightBorder(innerH, 1, m.threadView.Height(), threadTotal, threadPct, borderColor, showScrollbar)
	return lipgloss.JoinHorizontal(lipgloss.Top, box, rightBorder)
}

func replyWord(n int) string {
	if n == 1 {
		return "reply"
	}
	return "replies"
}

func (m *Model) renderTeamTabs() string {
	if len(m.teams) == 0 && !m.hasDMs {
		// Reserve the same vertical space so body math stays consistent.
		blank := strings.Repeat("\n", tabsHeight-1)
		return footerStyle.Render(" (no teams) ") + blank
	}
	// Pre-compute distinct counts for the Feed tab badge.
	unreadCh, mentionCh := 0, 0
	for _, n := range m.unread {
		if n > 0 {
			unreadCh++
		}
	}
	for _, n := range m.mentions {
		if n > 0 {
			mentionCh++
		}
	}

	maxIdx := m.maxTeamIdx()

	// The tab strip is always navigable (ctrl-←/→ from any focus), so the
	// active tab is always highlighted rather than tracking a focus.
	activeColor := focusedColor

	// Tabs split into a sticky synthetic prefix (DMs/Feed/Search,
	// always shown) and a scrollable team suffix. teamTabs collects the
	// latter so a window can be chosen around the active team when they
	// don't all fit; activeTeamPos is the active team's index within it.
	var sticky []string
	stickyW := 0
	type teamTab struct {
		s string
		w int
	}
	var teamTabs []teamTab
	activeTeamPos := -1

	// teamNum tracks the 1-based position among real teams so each of the
	// first nine gets a "[N]" hint matching its ",N" jump shortcut.
	teamNum := 0
	for i := 0; i <= maxIdx; i++ {
		kind, _, name := m.tabAt(i)
		label := name
		if kind == tabTeam {
			teamNum++
			if teamNum <= 9 {
				label = label + " [" + strconv.Itoa(teamNum) + "]"
			}
		}
		if kind == tabFeed {
			switch {
			case mentionCh > 0:
				label = "Feed " + strconv.Itoa(mentionCh) + "!"
			case unreadCh > 0:
				label = "Feed " + strconv.Itoa(unreadCh)
			}
		}

		isFirst := i == 0
		isActive := i == m.teamIdx

		var style lipgloss.Style
		if isActive {
			style = tabBase.
				Border(activeTabBorder, true).
				BorderForeground(activeColor).
				Foreground(activeColor).
				Bold(true)
		} else {
			style = tabBase.
				Border(inactiveTabBorder, true).
				BorderForeground(dimColor)
			switch {
			case kind == tabDM:
				style = style.Foreground(dmTabColor)
			case kind == tabSearch:
				style = style.Foreground(searchTabColor)
			case kind == tabFeed && mentionCh > 0:
				style = style.Foreground(mentionTabColor).Bold(true)
			case kind == tabFeed && unreadCh > 0:
				style = style.Foreground(feedTabColor).Bold(true)
			case kind == tabFeed:
				style = style.Foreground(feedTabColor)
			default:
				style = style.Foreground(dimColor)
			}
		}

		// Fix up the leftmost tab's bottom-left so the rule starts cleanly.
		// The rightmost tab keeps its default ┴ / └ so it flows into the
		// fill block's continuing horizontal rule.
		b, _, _, _, _ := style.GetBorder()
		switch {
		case isFirst && isActive:
			b.BottomLeft = "│"
		case isFirst && !isActive:
			b.BottomLeft = "├"
		}
		style = style.Border(b)

		rendered := style.Render(label)
		if kind == tabTeam {
			if isActive {
				activeTeamPos = len(teamTabs)
			}
			teamTabs = append(teamTabs, teamTab{s: rendered, w: lipgloss.Width(rendered)})
		} else {
			sticky = append(sticky, rendered)
			stickyW += lipgloss.Width(rendered)
		}
	}

	// Choose which team tabs are visible. Sticky tabs always show; the team
	// suffix scrolls horizontally to keep the active team on screen.
	widths := make([]int, len(teamTabs))
	for i, t := range teamTabs {
		widths[i] = t.w
	}
	start, end, leftClip, rightClip := teamTabWindow(widths, activeTeamPos, m.width-stickyW)

	pieces := make([]string, 0, len(sticky)+(end-start)+2)
	pieces = append(pieces, sticky...)
	if leftClip {
		pieces = append(pieces, scrollArrow("‹"))
	}
	for i := start; i < end; i++ {
		pieces = append(pieces, teamTabs[i].s)
	}
	if rightClip {
		pieces = append(pieces, scrollArrow("›"))
	}

	row := lipgloss.JoinHorizontal(lipgloss.Top, pieces...)

	// Extend a horizontal rule across the remaining width so the tab
	// bar reads as a single header strip instead of a floating widget.
	if fill := m.width - lipgloss.Width(row); fill > 0 {
		blank := strings.Repeat(" ", fill)
		rule := lipgloss.NewStyle().Foreground(dimColor).Render(strings.Repeat("─", fill))
		fillBlock := blank + "\n" + blank + "\n" + rule
		row = lipgloss.JoinHorizontal(lipgloss.Top, row, fillBlock)
	}
	return row
}

// teamTabWindow picks the visible [start,end) slice of the scrollable team
// tabs (widths in column order) that keeps the active team on screen within
// `avail` columns. activePos is the active team's index, or -1 when a sticky
// tab is active (then teams page from the left). When everything fits the
// full range is returned with no clipping; otherwise a column is reserved
// for each scroll arrow and the active team is pinned toward the right edge
// (fill leftward, then extend right) — the horizontal analogue of the
// channel list's bottom-pin. leftClip/rightClip report hidden tabs per side.
func teamTabWindow(widths []int, activePos, avail int) (start, end int, leftClip, rightClip bool) {
	n := len(widths)
	if n == 0 || avail < 1 {
		return 0, 0, false, false
	}
	total := 0
	for _, w := range widths {
		total += w
	}
	if total <= avail {
		return 0, n, false, false
	}
	// Reserve a column for each potential arrow so the strip never overflows.
	budget := avail - 2
	if budget < 1 {
		budget = 1
	}
	anchor := activePos
	if anchor < 0 || anchor >= n {
		anchor = 0
	}
	w := widths[anchor]
	start = anchor
	for start > 0 && w+widths[start-1] <= budget {
		w += widths[start-1]
		start--
	}
	end = anchor + 1
	for end < n && w+widths[end] <= budget {
		w += widths[end]
		end++
	}
	return start, end, start > 0, end < n
}

// scrollArrow renders a one-column, tab-height marker showing the team
// strip is clipped in that direction. Its bottom cell continues the tab
// strip's horizontal rule so the arrow sits flush in the bar.
func scrollArrow(glyph string) string {
	arrow := lipgloss.NewStyle().Foreground(focusedColor).Render(glyph)
	rule := lipgloss.NewStyle().Foreground(dimColor).Render("─")
	return " \n" + arrow + "\n" + rule
}

func (m *Model) renderFooter() string {
	right := m.status
	// While the indexer is running its progress takes over the right-
	// hand status slot. Final / error states fall through to m.status
	// after applyIndexResult sets it on completion.
	if m.indexer.active {
		right = m.indexerProgressStatus()
	}
	// rightDot is my own presence dot, shown just left of my username (and
	// only when the right slot is my username, not while a status/indexer
	// message occupies it). Rendered separately so its colour survives
	// footerStyle's dim wrap.
	rightDot := ""
	if right == "" && m.me != nil {
		right = m.me.Username
		if glyph, st, ok := m.myStatusDot(statusDot, statusHollowDot); ok {
			rightDot = st.Render(glyph) + " "
		}
	}

	// Leave room for the right-hand status and a one-cell gutter so the
	// help bubble can ellipsize cleanly if the bindings don't all fit.
	avail := m.width - lipgloss.Width(right) - lipgloss.Width(rightDot) - 1
	if avail < 0 {
		avail = 0
	}
	m.help.SetWidth(avail)

	// Prefix the input mode with a quick hint about what typing does — the
	// help bubble only renders bindings, but this state-mode context used
	// to ride along in the old footer prompt.
	var prefix string
	switch {
	case m.filterMode:
		prefix = "type to filter  "
	case m.focus == focusInput && m.editingPostID != "":
		prefix = "✎ editing message  "
	case m.focus == focusInput && m.threadOpen:
		prefix = "↳ replying in thread  "
	case m.focus == focusInput:
		prefix = "type to send  "
	}

	left := footerStyle.Render(prefix) + m.help.View(m)
	if m.help.ShowAll {
		// Full help is multi-line; right-align the status on the last row.
		gap := m.width - lipgloss.Width(lastLine(left)) - lipgloss.Width(right) - lipgloss.Width(rightDot)
		if gap < 1 {
			gap = 1
		}
		return left + strings.Repeat(" ", gap) + rightDot + footerStyle.Render(right)
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - lipgloss.Width(rightDot)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + rightDot + footerStyle.Render(right)
}

// lastLine returns the final line of s (everything after the last "\n").
// Used to right-align the status block when the help bubble renders
// multiple rows in full-help mode.
func lastLine(s string) string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	// Cut by display width, not rune count: wide runes (emoji, CJK) occupy
	// two cells, so slicing n-1 runes can exceed n cells and overflow the
	// pane, making lipgloss wrap the row onto a second line.
	budget := n - 1 // reserve one cell for the ellipsis
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > budget {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}
