package ui

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"image/color"
	"io"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/telemetry"
	"matterbox/internal/textwidth"
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
	unreadColor  = lipgloss.Color("67")  // muted steel blue — the "new messages" divider
	dateSepColor = lipgloss.Color("240") // dim grey — the (more subtle) date divider

	titleStyle = lipgloss.NewStyle().Bold(true)
	// titleHeaderStyle dims the channel header text shown after the channel name
	// on the messages-pane title line.
	titleHeaderStyle = lipgloss.NewStyle().Foreground(dimColor)
	userStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	timeStyle        = lipgloss.NewStyle().Foreground(dimColor)
	// meMarkerStyle paints the leading "*" of a /me emote line ("* alice waves").
	meMarkerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
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
	sqlTabColor       = lipgloss.Color("3")  // yellow to set the SQL tab apart
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
	replyTargetStyle   = lipgloss.NewStyle().Foreground(focusedColor)          // "↪ replying to" above the composer
	editedStyle        = lipgloss.NewStyle().Foreground(dimColor).Italic(true) // right-aligned "edited" tag
	deletedStyle       = lipgloss.NewStyle().Foreground(dimColor).Italic(true) // "⊘ message deleted" tombstone
	collapsedFootStyle = lipgloss.NewStyle().Foreground(dimColor).Italic(true) // "┄┄ N more lines ┄┄" fold footer
)

// tabBorderWithBottom returns a rounded border with the bottom row
// overridden to the given left/middle/right characters. Used to make
// the lipgloss-style tabs join cleanly along their bottom rule.
// dividedBorder is the pane border for a box whose left edge is a divider it
// shares with the pane before it (the messages pane against the sidebar): its
// bottom-left corner tees into that pane's bottom rule instead of starting a
// second box.
var dividedBorder = func() lipgloss.Border {
	b := lipgloss.NormalBorder()
	b.BottomLeft = "┴"
	return b
}()

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
	// Keep the composer width in sync with the messages/thread/ref/info split —
	// otherwise opening the reference panel leaves the input's old full-width
	// hit box overhanging the side panel and stealing clicks from its bottom rows.
	m.resizeInput()
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
	// The extra -1 accounts for the body pane's bottom border row (the top
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
	refW := 0
	infoW := 0
	// The thread sidebar, reference panel and channel-info panel share the single
	// right slot — at most one is ever open — so each splits rightW the same way.
	if m.threadOpen {
		threadW = splitRightPane(rightW)
		msgsW = rightW - threadW
	} else if m.refOpen {
		refW = splitRightPane(rightW)
		msgsW = rightW - refW
	} else if m.infoOpen {
		infoW = splitRightPane(rightW)
		msgsW = rightW - infoW
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
	rw := refW - 4
	if rw < 1 {
		rw = 1
	}
	m.refView.SetWidth(rw)
	iw := infoW - 4
	if iw < 1 {
		iw = 1
	}
	m.infoView.SetWidth(iw)
	// When the thread sidebar is open the compose textarea moves into
	// the thread pane; the messages pane only needs room for its title
	// + viewport, while the thread pane has to make room for the input
	// (and its attachment chip strip).
	if m.threadOpen {
		// The composer has moved into the thread pane, so the messages pane is
		// just its title row plus the viewport. The -2 is that title and the one
		// content row renderMessagesPane's lower box can't render without (it
		// carries the bottom border); anything more is transcript the pane has
		// room for but wouldn't show.
		mh := bodyH - 2
		if mh < 1 {
			mh = 1
		}
		m.msgsView.SetHeight(mh)
		// Both strips that can sit between the transcript and the composer —
		// the attachment chips and the nested-reply target — cost the thread
		// view a row each.
		attBarH := m.attachmentBarHeight(threadW-2) + m.replyBarHeight()
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
		// row (1) and the input's top-rule row (1); cap the input so the
		// viewport keeps at least one row even on a short terminal, then give
		// the viewport every remaining row so the input sits flush on the body's
		// last row rather than floating with a gap beneath it.
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
		// The reference and channel-info panels are read-only (no composer), so
		// their viewport fills the whole body below the title row.
		rh := bodyH - 1
		if rh < 1 {
			rh = 1
		}
		m.refView.SetHeight(rh)
		m.infoView.SetHeight(rh)
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
	m.sizeSQLView(m.width, bodyH)
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
	m.renderRef()
	m.renderInfo()
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
	if m.refOpen || m.infoOpen {
		// The composer stays under the (now narrower) messages pane; match its
		// width to the right-slot split so the input doesn't overhang the pane above.
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

// unreadDivider renders a full-width separator with a centered label,
// drawn above the first unread post when a channel is opened with unread
// messages. Uses a subtle colour so it reads as a marker without competing
// with the message text.
func unreadDivider(width int) string {
	style := lipgloss.NewStyle().Foreground(unreadColor)
	const label = " unread messages "
	lw := lipgloss.Width(label)
	if width <= lw {
		if width < 1 {
			width = 1
		}
		return style.Render(strings.Repeat("─", width))
	}
	left := (width - lw) / 2
	right := width - lw - left
	return style.Render(strings.Repeat("─", left) + label + strings.Repeat("─", right))
}

// dateDivider renders a full-width rule with a centered calendar-date label,
// drawn above the first message of each local day so a long scroll-back stays
// anchored in time. Mirrors unreadDivider's shape (a centered label in a ─
// rule) but in a dimmer colour so it recedes behind the conversation.
func dateDivider(width int, createAtMillis int64) string {
	style := lipgloss.NewStyle().Foreground(dateSepColor)
	label := " " + formatDividerDate(createAtMillis) + " "
	lw := lipgloss.Width(label)
	if width <= lw {
		if width < 1 {
			width = 1
		}
		return style.Render(strings.Repeat("─", width))
	}
	left := (width - lw) / 2
	right := width - lw - left
	return style.Render(strings.Repeat("─", left) + label + strings.Repeat("─", right))
}

// formatDividerDate labels a date separator: "Today" / "Yesterday" for the two
// most recent local days, otherwise the weekday and date ("Monday, January 2"),
// with the year appended when it differs from the current one.
func formatDividerDate(createAtMillis int64) string {
	t := time.UnixMilli(createAtMillis).Local()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	switch {
	case day.Equal(today):
		return "Today"
	case day.Equal(today.AddDate(0, 0, -1)):
		return "Yesterday"
	case t.Year() != now.Year():
		return t.Format("Monday, January 2, 2006")
	default:
		return t.Format("Monday, January 2")
	}
}

// crossesLocalDay reports whether cur begins a new local calendar day relative
// to prev — i.e. a date separator belongs above cur. The first loaded post
// (prev == nil) starts a day, so the visible window always opens with a label.
func crossesLocalDay(prev, cur *model.Post) bool {
	if cur == nil {
		return false
	}
	if prev == nil {
		return true
	}
	ct := time.UnixMilli(cur.CreateAt).Local()
	pt := time.UnixMilli(prev.CreateAt).Local()
	return ct.Year() != pt.Year() || ct.YearDay() != pt.YearDay()
}

// resolveUnreadDivider picks the post the "new messages" divider sits above,
// once, from the posts loaded for a freshly-opened channel — then freezes it
// so a message sent or received while the channel is open never moves or
// creates the divider. It waits until the loaded window actually reaches past
// the boundary (the unread posts may arrive a beat later via the recent
// merge), and never anchors to the user's own message: a divider reading
// "unread messages" above something you just sent is the bug this guards.
func (m *Model) resolveUnreadDivider() {
	if m.unreadDividerResolved || m.unreadBoundary <= 0 || len(m.posts) == 0 {
		return
	}
	if m.posts[len(m.posts)-1].CreateAt <= m.unreadBoundary {
		return // window hasn't reached the unread region yet
	}
	m.unreadDividerResolved = true
	for i := 1; i < len(m.posts); i++ {
		prev, cur := m.posts[i-1], m.posts[i]
		if prev.CreateAt <= m.unreadBoundary && cur.CreateAt > m.unreadBoundary {
			if m.me != nil && cur.UserId == m.me.Id {
				return // first unread is the user's own post — no divider
			}
			m.unreadDividerID = cur.Id
			return
		}
	}
}

// keyClaim is everything a rendered transcript pane asserts about its keys.
// owns is whether keystrokes currently land there at all — what the bright pane
// frame and a poll's key hints promise. bar is the same minus one carve-out,
// and drives the selection bar. Each render records the claim it drew (see
// syncSelBarFocus), so a claim left over from a focus that has moved on can be
// spotted and repainted.
type keyClaim struct{ owns, bar bool }

// paneClaim computes what `pane` (focusMessages or focusThread) may currently
// claim. Focus alone doesn't decide it: the channel filter (f) is a captive
// overlay that swallows every key while it's open without moving m.focus off
// the transcript, so during it none of the pane's own actions are reachable.
// The bar additionally stands down while a mouse text selection is being drawn
// in that pane — it shifts a header line by two cells, which would skew the
// cell→content mapping the drag relies on.
func (m *Model) paneClaim(pane focus) keyClaim {
	owns := m.focus == pane && !m.filterMode
	return keyClaim{owns: owns, bar: owns && !(m.textSel.active && m.textSel.pane == pane)}
}

// paneOwnsKeys reports whether keystrokes currently reach `pane`.
func (m *Model) paneOwnsKeys(pane focus) bool { return m.paneClaim(pane).owns }

// selBarWanted reports whether `pane` should be drawing the selection bar — the
// only on-screen answer to "which message will this keystroke act on", so it
// belongs to the pane keys actually reach and nowhere else.
func (m *Model) selBarWanted(pane focus) bool { return m.paneClaim(pane).bar }

func (m *Model) renderMessages() {
	// New content generation: invalidates the messages scroll-geometry cache
	// (see scrollcache.go). Bump unconditionally — every path below resets the
	// viewport content.
	m.msgsContentVer++
	// Record the claim this render is about to bake into the content, before
	// the early returns — an empty pane claims nothing visually, but storing
	// the live value is what keeps syncSelBarFocus from repainting it forever.
	m.msgsClaim = m.paneClaim(focusMessages)
	m.refreshTailBehind()
	if len(m.posts) == 0 {
		m.msgsView.SetContent(lipgloss.NewStyle().Foreground(dimColor).Render("no messages"))
		m.msgRowStarts = nil
		m.msgsRenderTail, m.msgsRenderSel = "", ""
		m.refreshAnimVisibility() // nothing on screen → stop any animation
		return
	}
	// Clamp the selection in case posts were deleted out from under it.
	if m.postIdx >= len(m.posts) {
		m.postIdx = len(m.posts) - 1
	}
	if m.postIdx < 0 {
		m.postIdx = 0
	}
	// Did the previous render leave this same window parked on its newest
	// message? Answered before the layout below overwrites msgRowStarts, and
	// applied to the offset once the new heights are known.
	stayAtBottom := m.msgsStayAtBottom()

	decorate := m.msgsClaim.bar
	bar := selectedBarStyle.Render("▎")
	width := m.msgsView.Width()
	var allLines []string
	// The viewport has SoftWrap on, so YOffset is in visual rows
	// (post-wrap). Accumulate each post's cached visual-row count as we go
	// so we can place the selection without re-measuring every line on
	// each keystroke (visAcc is the running visual-row offset).
	m.resolveUnreadDivider()
	selVisStart, selVisRows, visAcc := -1, 0, 0
	rowStarts := make([]int, len(m.posts)+1)
	dividerDrawn := false
	// Which posts answer another post rather than the thread as a whole. nil
	// (nothing nested in the loaded window) is the fast path.
	nests := m.nestInfos(m.posts, false, 0)
	for i, p := range m.posts {
		var prev *model.Post
		if i > 0 {
			prev = m.posts[i-1]
		}
		// A divider introduces the post under it, so the post's row span starts
		// at the divider: a click on the "new messages" rule selects the first
		// unread message rather than the last read one above it, and landing on
		// a post top-anchored keeps its rule on screen.
		rowStarts[i] = visAcc
		// Insert a subtle date separator above the first message of each local
		// day. Drawn inline (unlike the frozen unread divider) by comparing
		// adjacent posts' local dates. A post that opens a new day also keeps
		// its own header, so a grouped continuation line never renders bare
		// beneath the rule.
		crossDay := m.showDateSeparators && crossesLocalDay(prev, p)
		if crossDay {
			allLines = append(allLines, dateDivider(width, p.CreateAt))
			visAcc++
		}
		// Insert the "new messages" divider above its frozen anchor post.
		if !dividerDrawn && p.Id != "" && p.Id == m.unreadDividerID {
			allLines = append(allLines, unreadDivider(width))
			visAcc++
			dividerDrawn = true
		}
		grouped := m.groupWithPrev(p, prev, false)
		if crossDay {
			grouped = false
		}
		var nest nestInfo
		if nests != nil {
			nest = nests[i]
		}
		chunk, rows := m.renderPostLines(p, grouped, nest)
		if i == m.postIdx {
			selVisStart = rowStarts[i]
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
			// Any divider rows drawn above sit inside this post's span (see
			// rowStarts), so they count toward the height the scroll anchoring
			// below works with — visAcc hasn't taken the chunk itself yet.
			selVisRows = visAcc - rowStarts[i] + rows
		}
		allLines = append(allLines, chunk...)
		visAcc += rows
	}
	rowStarts[len(m.posts)] = visAcc
	m.msgRowStarts = rowStarts
	m.applyTextSelHighlight(focusMessages, allLines)
	// Hand the viewport the line slice directly: SetContent would join these
	// into one big string only for the viewport to split them straight back.
	m.msgsView.SetContentLines(allLines)
	// We just summed every post's wrapped height into visAcc, which is exactly
	// the viewport's total visual rows — seed the geometry cache with it so the
	// View that follows reads a hit instead of re-walking the whole window.
	if m.vcache != nil {
		primeScrollGeom(&m.vcache.msgs, m.msgsContentVer, width, visAcc)
	}

	if h := m.msgsView.Height(); h > 0 && selVisStart >= 0 {
		visStart := selVisStart
		visEnd := selVisStart + selVisRows
		off := m.msgsView.YOffset()
		switch {
		case m.msgScrollFree:
			// Mouse free-scroll: keep the wheel's offset, clamped to content,
			// rather than anchoring to the selection. Sticky until a keypress
			// (see handleKey).
			off = m.msgFreeOffset
			if max := m.msgRowStarts[len(m.msgRowStarts)-1] - h; off > max {
				off = max
			}
		case m.keepMsgOffset:
			// Intra-message scroll set the offset explicitly; keep it, clamped
			// to the selected post's scrollable range below.
			off = m.pendingMsgOffset
			if off > visEnd-h {
				off = visEnd - h
			}
			if off < visStart {
				off = visStart
			}
		case m.anchorMsgSelTop:
			off = visStart
		case m.anchorMsgSelBottom:
			off = visEnd - h
		case m.anchorMsgSelContext:
			off = contextAnchorOffset(rowStarts, m.postIdx, h)
		case visStart < off:
			off = visStart
		case visEnd > off+h:
			off = visEnd - h
		}
		// A transcript that was already parked on the newest message stays
		// parked, whatever the rules above made of an unchanged selection: the
		// reaction chip that just appeared on the last message grew the content
		// under a reader who never scrolled, and without this the newest line
		// slides under the fold and summons the jump pill. The one-shot anchors
		// and the intra-message scroll asked for a specific offset, so they win.
		pinBottom := stayAtBottom && !m.anchorMsgSelTop && !m.anchorMsgSelBottom && !m.anchorMsgSelContext && !m.keepMsgOffset
		if pinBottom {
			off = visAcc - h
		}
		// A message just landed under a reader who could see the bottom: keep
		// the bottom on screen so it shows without a scroll. The selection may
		// sit a few posts up (it stays there and slides up with the rest); only
		// its top row is defended, so a burst of arrivals parks it at the top
		// edge and the rest wait under the fold for the pill. A wheel offset
		// parked at the bottom follows too — the free-scroll case above only
		// meant to protect a reader who had scrolled *up*.
		if m.msgsFollowTail {
			off = visAcc - h
			if !m.msgScrollFree && off > visStart {
				off = visStart
			}
		}
		if off < 0 {
			off = 0
		}
		m.msgsView.SetYOffset(off)
		// Free-scroll reads its offset back on the next render; leave it on the
		// bottom we just pinned rather than the pre-growth row.
		if (pinBottom || m.msgsFollowTail) && m.msgScrollFree {
			m.msgFreeOffset = off
		}
	}
	m.msgsRenderTail = m.posts[len(m.posts)-1].Id
	m.msgsRenderSel = m.posts[m.postIdx].Id
	m.anchorMsgSelTop = false
	m.anchorMsgSelBottom = false
	m.anchorMsgSelContext = false
	m.msgsFollowTail = false
	m.keepMsgOffset = false
	// YOffset is final; refresh which animated emoji are actually in view.
	m.refreshAnimVisibility()
}

// unreadContextPosts is how many read messages stay visible above the first
// unread one when a channel is opened from the feed: enough to show where the
// reader left off, and that the divider under them is the start of the unread
// block rather than the top of the transcript.
const unreadContextPosts = 4

// contextAnchorOffset is the viewport offset for anchorMsgSelContext. The
// selected post — the first unread, its divider included — starts right under
// its unreadContextPosts predecessors, so every unread message that fits shares
// the pane with them and the ones that don't wait under the fold, where the
// scrollbar announces them. Tall context is cut to half the pane so the unread
// block always gets the other half; content that would leave blank rows under
// the newest message pins to the bottom instead: the same unread messages,
// more context. rowStarts is renderMessages' per-post visual-row table (one
// extra entry holding the total), sel the selected index, h the pane height.
func contextAnchorOffset(rowStarts []int, sel, h int) int {
	ctx := sel - unreadContextPosts
	if ctx < 0 {
		ctx = 0
	}
	off := rowStarts[ctx]
	if lo := rowStarts[sel] - h/2; off < lo {
		off = lo
	}
	if hi := rowStarts[len(rowStarts)-1] - h; off > hi {
		off = hi
	}
	if off < 0 {
		off = 0
	}
	return off
}

// renderThread populates the thread viewport with the loaded thread
// posts, mirroring renderMessages' selection-bar behaviour for the
// focused row.
func (m *Model) renderThread() {
	// As in renderMessages: record the claim first, so the early returns below
	// leave syncSelBarFocus with nothing to reconcile.
	m.threadClaim = m.paneClaim(focusThread)
	if !m.threadOpen {
		return
	}
	// New content generation: invalidates the thread scroll-geometry cache.
	m.threadContentVer++
	if m.threadLoading && len(m.threadPosts) == 0 {
		m.threadView.SetContent(lipgloss.NewStyle().Foreground(dimColor).Render("loading…"))
		m.threadRowStarts = nil
		m.refreshAnimVisibility()
		return
	}
	if len(m.threadPosts) == 0 {
		m.threadView.SetContent(lipgloss.NewStyle().Foreground(dimColor).Render("no messages"))
		m.threadRowStarts = nil
		m.refreshAnimVisibility()
		return
	}
	if m.threadIdx >= len(m.threadPosts) {
		m.threadIdx = len(m.threadPosts) - 1
	}
	if m.threadIdx < 0 {
		m.threadIdx = 0
	}
	decorate := m.threadClaim.bar
	bar := selectedBarStyle.Render("▎")
	width := m.threadView.Width()
	var allLines []string
	selVisStart, selVisRows, visAcc := -1, 0, 0
	rowStarts := make([]int, len(m.threadPosts)+1)
	// Where each reply sits in the tree the hidden parent references describe.
	// nil for the flat threads that are still the vast majority. m.threadPosts is
	// already in tree order (nestSortThread), so the indent carries the meaning
	// and only a nest too deep for this width still needs a quote line.
	maxDepth := maxNestDepth(width)
	nests := m.nestInfos(m.threadPosts, true, maxDepth)
	for i, p := range m.threadPosts {
		rowStarts[i] = visAcc
		var prev *model.Post
		if i > 0 {
			prev = m.threadPosts[i-1]
		}
		var nest, prevNest nestInfo
		if nests != nil {
			nest = nests[i]
			if i > 0 {
				prevNest = nests[i-1]
			}
		}
		// Only a reply continuing the run *under the same parent* may fold into the
		// author line above it. A step in or out of the tree, a move to another
		// parent, or a quote line of its own all carry structure that grouping
		// would swallow.
		grouped := m.groupWithPrev(p, prev, true) &&
			nest.depth == prevNest.depth && nest.parentID == prevNest.parentID && !nest.quote
		chunk, rows := m.renderThreadPostLines(p, i == 0, grouped, nest)
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
	rowStarts[len(m.threadPosts)] = visAcc
	m.threadRowStarts = rowStarts
	m.applyTextSelHighlight(focusThread, allLines)
	m.threadView.SetContentLines(allLines)
	if m.vcache != nil {
		primeScrollGeom(&m.vcache.thread, m.threadContentVer, width, visAcc)
	}

	if h := m.threadView.Height(); h > 0 && selVisStart >= 0 {
		visStart := selVisStart
		visEnd := selVisStart + selVisRows
		off := m.threadView.YOffset()
		switch {
		case m.threadScrollFree:
			// Mouse free-scroll: keep the wheel's offset, clamped to content.
			off = m.threadFreeOffset
			if max := m.threadRowStarts[len(m.threadRowStarts)-1] - h; off > max {
				off = max
			}
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
	m.refreshAnimVisibility()
}

// wrapBodyLine wraps a single rendered line to fit within width while
// preserving its two-space left gutter on wrapped continuation rows.
// ANSI styling is carried across the wrap (see carryStyle): a colour open
// where the break falls is re-emitted on the next row, so a long span doesn't
// lose its colour when it wraps. Lines without the gutter (headers) or that
// already fit are returned as a single element.
// appendBodyLines appends a styled markdown body to dst, soft-wrapping ordinary
// lines via wrapBodyLine and laying out any encoded table line (see table.go) to
// fit width. Used by the transcript panes that wrap line-by-line themselves.
func appendBodyLines(dst []string, body string, width int) []string {
	for _, l := range strings.Split(body, "\n") {
		if tl, ok := tableLines(l, width); ok {
			dst = append(dst, tl...)
			continue
		}
		dst = append(dst, wrapBodyLine(l, width)...)
	}
	return dst
}

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
	carryStyle(parts)
	for i, p := range parts {
		parts[i] = indent + p
	}
	return parts
}

// carryStyle fixes up the rows produced by an ANSI-aware soft-wrap so colour
// survives the break. ansi.Wrap keeps escape codes where they sit but never
// re-opens a style on the continuation row, so a single coloured span (e.g. a
// long code comment) loses its colour the moment it wraps — only the visual row
// that physically contains the opening SGR is painted. carryStyle re-emits the
// style left open at each break at the start of the next row, and reset-
// terminates every row but the last so the colour can't bleed into the gutter
// below. Rows whose span already started on a fresh line (a string literal, an
// identifier) are unaffected — they carry their own opening SGR.
func carryStyle(parts []string) {
	var active string // SGR sequence(s) still open at the end of the previous row
	for i := range parts {
		if active != "" {
			parts[i] = active + parts[i]
		}
		active = sgrState(parts[i])
		if active != "" && i < len(parts)-1 {
			parts[i] += "\x1b[0m"
		}
	}
}

// sgrState returns the SGR escape sequence(s) left open at the end of s — the
// styling a continuation row must re-emit to keep painting. It tracks only SGR
// (ESC[…m) sequences: a reset (ESC[0m, ESC[m, or any params beginning "0;")
// clears the accumulated state; every other SGR is appended, so re-emitting the
// result reproduces the live style. chroma and lipgloss reset between tokens, so
// the accumulator stays short. Non-SGR escapes (cursor moves, links) are ignored.
func sgrState(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			i++
			continue
		}
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) { // CSI final byte is @–~
			j++
		}
		if j >= len(s) {
			break // truncated sequence; nothing more to track
		}
		if s[j] == 'm' { // an SGR sequence
			params := s[i+2 : j]
			if params == "" || params == "0" || strings.HasPrefix(params, "0;") {
				b.Reset()
			}
			if params != "" && params != "0" {
				b.WriteString(s[i : j+1])
			}
		}
		i = j + 1
	}
	return b.String()
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
		w := visualWidth(lines[i])
		if w <= maxWidth {
			rows++
			continue
		}
		rows += (w + maxWidth - 1) / maxWidth
	}
	return rows
}

// visualWidth returns the rendered cell width of a single line, identical to
// ansi.StringWidth for every input. textwidth.Width fast-paths the styled ASCII
// and box-drawing lines that make up nearly all rendered rows (the old inline
// fast path bailed to the grapheme-cluster walk on the first ANSI escape, so a
// styled line — i.e. almost every line — got no benefit).
func visualWidth(s string) int {
	return textwidth.Width(s)
}

// postVisualRows is visualRowsBefore over a whole post chunk: the number
// of soft-wrapped visual rows its lines occupy at the given width. Cached
// per post (postLineCacheEntry.rows) so renderMessages can sum prefix
// heights without re-measuring every line's width on each keystroke.
func postVisualRows(lines []string, width int) int {
	return visualRowsBefore(lines, len(lines), width)
}

// collapseBody folds a tall message body down to a short preview. body holds
// the post's soft-wrapped body lines (gutter-prefixed, as produced by
// appendBodyLines); width is the pane width those lines were wrapped to. When
// the body occupies more than threshold visual rows, collapseBody keeps the
// leading whole lines that fit within show visual rows — always at least the
// first line — and appends a dim footer summarizing the hidden remainder, e.g.
// "  ┄┄ 38 more lines · z to expand ┄┄". Otherwise — a body that fits, or
// collapsing disabled (threshold <= 0) — it returns body unchanged. keyHint is
// the expand/collapse key's label shown in the footer. The footer is folded
// into the returned lines so the caller's row accounting (postVisualRows,
// msgRowStarts) measures the collapsed height directly.
func collapseBody(body []string, width, threshold, show int, keyHint string) []string {
	if threshold <= 0 || len(body) == 0 {
		return body
	}
	total := postVisualRows(body, width)
	if total <= threshold {
		return body
	}
	kept, rows := 0, 0
	for kept < len(body) {
		r := visualRowsBefore(body[kept:kept+1], 1, width)
		// Always keep the first line; stop once another would overflow the
		// preview budget.
		if kept > 0 && rows+r > show {
			break
		}
		rows += r
		kept++
	}
	if kept >= len(body) {
		// A single line taller than the whole budget: nothing left to hide.
		return body
	}
	hidden := total - rows
	noun := "lines"
	if hidden == 1 {
		noun = "line"
	}
	footer := "  " + collapsedFootStyle.Render(fmt.Sprintf("┄┄ %d more %s · %s to expand ┄┄", hidden, noun, keyHint))
	out := make([]string, 0, kept+1)
	out = append(out, body[:kept]...)
	out = append(out, footer)
	return out
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
//
// joinRow is the row where a full-width rule inside the pane meets this edge
// (the composer separator); it gets a ┤ instead of a │. Pass -1 for none.
func renderRightBorder(outerH, vpTop, vpHeight int, totalRows int, percent float64, borderColor color.Color, showScrollbar bool, joinRow int) string {
	if outerH < 1 {
		outerH = 1
	}
	borderStyle := lipgloss.NewStyle().Foreground(borderColor)
	track := borderStyle.Render("│")

	lines := make([]string, outerH)
	for i := 0; i < outerH-1; i++ {
		lines[i] = track
	}
	if joinRow >= 0 && joinRow < outerH-1 {
		lines[joinRow] = borderStyle.Render("┤")
	}
	lines[outerH-1] = borderStyle.Render("┘")

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

// joinRuleLine turns one rendered box line whose content is a horizontal rule
// into a section divider: the box's own side borders become ├ and ┤ so the rule
// meets the frame instead of floating between two walls. It is safe only on a
// rule row, which holds no │ of its own — it is all ─. A box drawn without a
// right border (the messages pane, whose right edge is the scrollbar column)
// simply has no second │ to convert; renderRightBorder places that ┤ instead.
func joinRuleLine(line string) string {
	if i := strings.Index(line, "│"); i >= 0 {
		line = line[:i] + "├" + line[i+len("│"):]
	}
	if i := strings.LastIndex(line, "│"); i >= 0 {
		line = line[:i] + "┤" + line[i+len("│"):]
	}
	return line
}

// joinRuleRows applies joinRuleLine to the named rows of a rendered block.
// Rows that are negative or past the end are skipped, so a caller with no rule
// to join can pass -1 and get the block back untouched.
func joinRuleRows(block string, rows ...int) string {
	lines := strings.Split(block, "\n")
	touched := false
	for _, row := range rows {
		if row >= 0 && row < len(lines) {
			lines[row] = joinRuleLine(lines[row])
			touched = true
		}
	}
	if !touched {
		return block
	}
	return strings.Join(lines, "\n")
}

// contentRows is the rendered height of a stack of blocks — where the next one
// appended would start.
func contentRows(parts []string) int {
	n := 0
	for _, p := range parts {
		n += lipgloss.Height(p)
	}
	return n
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

// deletedPostLines renders the tombstone shown in place of a post whose author
// removed it: the name/time header keeps the gap in the transcript
// attributable, and a single dim placeholder stands in for the vanished
// content. We deliberately render neither the original message, attachments,
// reactions, nor poll — the whole point of a deletion is that the content is
// gone. isRoot tags a removed thread root in the sidebar so it still reads as
// the anchor of the conversation.
func (m *Model) deletedPostLines(p *model.Post, isRoot bool) []string {
	header := userStyle.Render(m.postAuthorName(p)) + "  " + timeStyle.Render(formatPostTime(p.CreateAt))
	if isRoot {
		header += "  " + replyHintStyle.Render("· root")
	}
	// "  " matches the two-space body gutter so the placeholder lines up with
	// where the message text would have been.
	return []string{header, "  " + deletedStyle.Render("⊘ message deleted")}
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
//
// nest is where the post sits in the reply tree (see nestedreply.go): a reply to
// another reply is wrapped narrower, shifted right under what it answers, and
// introduced by a dim quote of it. A flat reply — the zero nestInfo — renders
// exactly as it always did, at the full pane width.
func (m *Model) renderThreadPostLines(p *model.Post, isRoot, grouped bool, nest nestInfo) ([]string, int) {
	paneWidth := m.threadView.Width()
	indent := nest.indent(maxNestDepth(paneWidth))
	width := paneWidth - indent
	if width < 8 {
		// Too narrow to nest into: draw the post flat rather than shred it into
		// one word per row. The quote line still names what it answers.
		indent, width = 0, paneWidth
	}
	deleted := p.DeleteAt != 0
	poll := !deleted && isPoll(p)
	var fp string
	if !poll && p.Id != "" {
		fp = m.postLineFingerprint(p, width, true, isRoot, grouped) + nestFingerprint(nest, indent, nest.quote)
		if cached, rows, ok := m.cachedPostLines(p, fp); ok {
			return cached, rows
		}
	}
	var lines []string
	switch {
	case deleted:
		lines = m.deletedPostLines(p, isRoot)
	default:
		if !grouped {
			name := m.postAuthorName(p)
			ts := formatPostTime(p.CreateAt)
			header := userStyle.Render(name) + "  " + timeStyle.Render(ts)
			if isRoot {
				header += "  " + replyHintStyle.Render("· root")
			}
			header += m.postMarks(p)
			header = withEditedTag(header, p, width)
			lines = append(lines, header)
		}
		if body := m.markdownBody(p); body != "" {
			bodyLines := appendBodyLines(nil, body, width)
			if m.collapseRows > 0 && !m.expandedPosts[p.Id] {
				bodyLines = collapseBody(bodyLines, width, m.collapseRows, m.collapseShow, m.collapseKeyHint)
			}
			lines = append(lines, bodyLines...)
		}
		// See renderPostLines: body-linked images draw under the text that links them.
		lines = append(lines, m.inlineBodyImageLines(p, width)...)
		if poll {
			selected := m.paneOwnsKeys(focusThread) && m.threadIdx >= 0 && m.threadIdx < len(m.threadPosts) && m.threadPosts[m.threadIdx] == p
			for _, l := range m.renderPoll(p, width, selected) {
				lines = append(lines, wrapBodyLine(l, width)...)
			}
		}
		if att := m.renderAttachments(p, width); att != "" {
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
	}
	if nest.nested() {
		lines = indentLines(lines, indent)
		if nest.quote {
			if q := m.nestQuoteLine(nest, width); q != "" {
				lines = append([]string{strings.Repeat(" ", indent) + q}, lines...)
			}
		}
	}
	// Text-effect spans are deliberately left unresolved here: the lines cached
	// below keep their invisible sentinels, which are width-0 and carry no
	// colour, so this cache (and every measurement taken from it) is
	// animation-phase-independent. The spans are painted at the very end of the
	// render, on the rows actually on screen — see paintEffects / effectsanim.go.
	rows := postVisualRows(lines, paneWidth)
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
	// A pinned or saved message keeps its header so its mark stays visible.
	if cur.IsPinned || m.isSaved(cur.Id) {
		return false
	}
	// A removed message renders as a tombstone with its own header, and the
	// post below it shouldn't fold up into that tombstone either.
	if cur.DeleteAt != 0 || prev.DeleteAt != 0 {
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

// postMarks returns the header tags for a pinned and/or saved message ("·
// pinned", "· saved"), each in the dim reply-hint style, or "" for neither.
func (m *Model) postMarks(p *model.Post) string {
	if p == nil {
		return ""
	}
	var out string
	if p.IsPinned {
		out += "  " + replyHintStyle.Render("· pinned")
	}
	if m.isSaved(p.Id) {
		out += "  " + replyHintStyle.Render("· saved")
	}
	return out
}

// renderPostLines returns one rendered line per visual row of a post:
// the header line, the (possibly multi-line) body, and one line per
// attachment. Existing styles already include a two-space left gutter
// on body and attachment lines. When grouped is true the post continues
// the author run above it, so the name/time header is omitted and the body
// starts straight away (see groupWithPrev).
//
// nest names the message this one answers, when it answers one (see
// nestedreply.go). The channel transcript quotes that message in a dim line
// above the reply but does not indent: this pane interleaves whole
// conversations, and a staircase running through them would obscure the
// chronology it exists to show. The indented tree lives in the thread pane.
func (m *Model) renderPostLines(p *model.Post, grouped bool, nest nestInfo) ([]string, int) {
	width := m.msgsView.Width()
	deleted := p.DeleteAt != 0
	poll := !deleted && isPoll(p)
	var fp string
	if !poll && p.Id != "" {
		fp = m.postLineFingerprint(p, width, false, false, grouped) + nestFingerprint(nest, 0, nest.quote)
		if cached, rows, ok := m.cachedPostLines(p, fp); ok {
			return cached, rows
		}
	}
	var lines []string
	switch {
	case deleted:
		lines = m.deletedPostLines(p, false)
	case p.Type == model.PostTypeMe:
		// IRC-style emote: "* alice waves" on one line (the author is part of the
		// content, so it ignores grouping). Reactions still render below it.
		lines = append(lines, m.meEmoteLine(p))
		if rx := m.renderReactions(p); rx != "" {
			lines = append(lines, wrapBodyLine(rx, width)...)
		}
	default:
		if !grouped {
			name := m.postAuthorName(p)
			ts := formatPostTime(p.CreateAt)
			header := userStyle.Render(name) + "  " + timeStyle.Render(ts)
			if p.RootId != "" {
				header = replyHintStyle.Render("↳ ") + header
			} else if p.ReplyCount > 0 {
				header += "  " + replyHintStyle.Render(fmt.Sprintf("↪ %d", p.ReplyCount))
			}
			header += m.postMarks(p)
			header = withEditedTag(header, p, width)
			lines = append(lines, header)
		}
		if body := m.markdownBody(p); body != "" {
			bodyLines := appendBodyLines(nil, body, width)
			if m.collapseRows > 0 && !m.expandedPosts[p.Id] {
				bodyLines = collapseBody(bodyLines, width, m.collapseRows, m.collapseShow, m.collapseKeyHint)
			}
			lines = append(lines, bodyLines...)
		}
		// Images linked in the body (a Giphy ![](…), any image URL) draw under the
		// text that links them. Attachments are handled by renderAttachments below,
		// each above its own filename line.
		lines = append(lines, m.inlineBodyImageLines(p, width)...)
		if poll {
			// Deliberately paneOwnsKeys and not selBarWanted: an active mouse
			// text selection hides the bar for drag-geometry reasons, but the
			// poll's keys do still work (a keypress drops the selection first),
			// and dropping the hint row mid-drag would slide the content out
			// from under the pointer.
			selected := m.paneOwnsKeys(focusMessages) && m.postIdx >= 0 && m.postIdx < len(m.posts) && m.posts[m.postIdx] == p
			for _, l := range m.renderPoll(p, width, selected) {
				lines = append(lines, wrapBodyLine(l, width)...)
			}
		}
		if att := m.renderAttachments(p, width); att != "" {
			for _, l := range strings.Split(att, "\n") {
				lines = append(lines, wrapBodyLine(l, width)...)
			}
		}
		if rx := m.renderReactions(p); rx != "" {
			lines = append(lines, wrapBodyLine(rx, width)...)
		}
		// A grouped post with no body, attachments, reactions, or poll would
		// render as zero lines and silently vanish (breaking selection
		// geometry). Keep one blank continuation row so it stays visible and
		// selectable, matching the single header line it would otherwise have
		// shown.
		if len(lines) == 0 {
			lines = append(lines, "  ")
		}
	}
	if nest.quote {
		if q := m.nestQuoteLine(nest, width); q != "" {
			lines = append([]string{q}, lines...)
		}
	}
	// Text-effect spans are deliberately left unresolved here: the lines cached
	// below keep their invisible sentinels, which are width-0 and carry no
	// colour, so this cache (and every measurement taken from it) is
	// animation-phase-independent. The spans are painted at the very end of the
	// render, on the rows actually on screen — see paintEffects / effectsanim.go.
	rows := postVisualRows(lines, width)
	if !poll && p.Id != "" {
		m.putPostLines(p.Id, fp, lines, rows)
	}
	return lines, rows
}

// meEmoteLine renders a /me post as an IRC-style emote: a magenta "*" marker,
// the author, then the action and the timestamp ("* alice waves   10:30"). The
// action reuses markdownBody (cached, with emoji/mentions and the server's
// italic asterisks already applied) with its body indent stripped so it sits
// inline after the name. Like a normal header line it isn't soft-wrapped — emotes
// are short.
func (m *Model) meEmoteLine(p *model.Post) string {
	name := m.postAuthorName(p)
	// Plain: an emote draws no thumbnail, so an image in it gets no chevron.
	action := strings.TrimRight(m.markdownBodyPlain(p), "\n")
	// markdownBody indents every line by two spaces; drop the first line's indent
	// and flatten any continuation lines so the whole emote reads on one line.
	action = strings.TrimPrefix(action, "  ")
	action = strings.ReplaceAll(action, "\n  ", " ")
	line := meMarkerStyle.Render("*") + " " + userStyle.Render(name)
	if action != "" {
		line += " " + action
	}
	return line + "  " + timeStyle.Render(formatPostTime(p.CreateAt))
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
//
// With image_thumbnails on, an image attachment whose thumbnail is ready also
// contributes the thumbnail's placeholder rows, above its filename line. Those
// rows are plain text cells (see inlineimg.go), so they simply become more lines
// here and the rest of the pipeline is none the wiser. Its filename line then leads
// with the disclosure chevron (▾ showing / ▸ collapsed) — only there, and only when
// a thumbnail is actually drawn: with thumbnails off the line is exactly what it
// always was.
func (m *Model) renderAttachments(p *model.Post, maxWidth int) string {
	if p.Metadata == nil || len(p.Metadata.Files) == 0 {
		return ""
	}
	lines := make([]string, 0, len(p.Metadata.Files))
	for _, f := range p.Metadata.Files {
		// Emitted raw: each cell carries the image id in its truecolor foreground,
		// and running it through attachmentStyle (or any lipgloss style) would
		// overwrite that and collapse the image to blank cells.
		lines = append(lines, m.inlineFileThumbLines(p, f, maxWidth)...)
		// A text/table preview for the files no image decoder can help with (a log,
		// a diff, a CSV). Mutually exclusive with the thumbnail above in practice:
		// filePreviewKindOf refuses anything another renderer claims. The shape is
		// resolved once here and reused by the icon and the chevron below.
		prevKind, prevLexer := m.filePreviewShape(f)
		lines = append(lines, m.filePreviewFileLines(p, f, prevKind, prevLexer, maxWidth)...)

		icon := "📎"
		var info, chev string
		if strings.HasPrefix(f.MimeType, "image/") {
			icon = "🖼️"
			if f.Width > 0 && f.Height > 0 {
				info = fmt.Sprintf(" (%d×%d, %s)", f.Width, f.Height, humanSize(f.Size))
			} else {
				info = " (" + humanSize(f.Size) + ")"
			}
		} else {
			switch {
			case m.videoPlayable() && isVideoAttachment(f):
				icon = "🎬" // a clip we're about to draw a thumbnail for, not a plain file
			case isSTLAttachment(f):
				icon = "🧊" // a 3D model: rendered inline, and orbitable with space
			case isSVGAttachment(f):
				icon = "🖼️" // a drawing the server left untyped, but still an image
			case isStillImageAttachment(f):
				icon = "🖼️" // an image the server left untyped (a HEIC, usually)
			case prevKind != filePreviewNone:
				icon = "📄" // a log/diff/CSV whose head we're drawing above
			}
			info = " (" + humanSize(f.Size) + ")"
		}
		// Only a file we actually draw something for gets a chevron — a thumbnail or
		// a text/table preview. A format we can't decode (a HEIC without the video
		// tag, or a video in a build that can't play one) draws neither, so there is
		// nothing for the chevron to describe and z has nothing to hide.
		if (m.inlineImagesActive() && m.drawsFileThumb(f)) || prevKind != filePreviewNone {
			if c := m.collapseChevron(p.Id); c != "" {
				chev = c + " "
			}
		}
		name := normalizeFilename(f.Name)
		// Reserve room for the two-space gutter, the chevron and icon prefixes,
		// and the trailing info; truncate the name so the whole line
		// fits within maxWidth.
		fixed := lipgloss.Width("  ") + lipgloss.Width(chev+icon+" ") + lipgloss.Width(info)
		if maxWidth > fixed {
			name = truncate(name, maxWidth-fixed)
		}
		lines = append(lines, "  "+attachmentStyle.Render(chev+icon+" "+name+info))
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
	// The render path is where this codebase's complexity lives, so a panic
	// here is the one most worth hearing about. Same shape as Update: report
	// and re-panic, guarded so the disabled path stays a single atomic load.
	if telemetry.Enabled() {
		defer telemetry.Crash("render")
	}
	v := tea.NewView(m.timedViewContent())
	v.AltScreen = true
	// Ask the terminal to report focus/blur (DECSET 1004). Two things ride on
	// knowing whether you're actually looking: the channel isn't marked read
	// while the window is buried, and `matterbox listen` skips the desktop
	// notification for the conversation you're reading. Terminals that don't
	// support it simply never send the events — see focus.go for that fallback.
	v.ReportFocus = true
	// All-motion mouse reporting: the wheel scrolls, clicks switch/select,
	// drags select text, and the button-less motion events drive the tab /
	// channel hover highlight. Gated by config because capturing the mouse
	// disables the terminal's native text selection. See mouse.go.
	if m.mouseEnabled {
		v.MouseMode = tea.MouseModeAllMotion
	}
	// Place the real terminal cursor in the focused editing surface (composer,
	// SQL editor, or jira-comment overlay), computed after viewContent so the
	// geometry caches those helpers read are fresh. Leave Color nil so the
	// cursor keeps the terminal's own colour; bubbletea always emits a shape, so
	// we ask for the blinking block that matches the prior drawn caret and is the
	// terminal's conventional default.
	if cx, cy, ok := m.editorCursor(); ok {
		v.Cursor = tea.NewCursor(cx, cy)
	}
	return v
}

// timedViewContent builds the screen, timing it when telemetry is on so a
// perceptibly slow frame can be reported. Render cost is the recurring
// performance problem in this codebase and has only ever been measured locally,
// on one machine, against one cache; this is the field version. The content
// build is what is timed — the rest of View is a handful of field assignments.
//
// An opted-out session takes the nil branch and pays one pointer comparison.
func (m *Model) timedViewContent() string {
	if m.tel == nil {
		return m.viewContent()
	}
	started := time.Now()
	s := m.viewContent()
	m.recordFrame(time.Since(started))
	return s
}

func (m *Model) viewContent() string {
	if m.width == 0 || m.height == 0 {
		return "starting…"
	}
	// Serve the memoized screen when nothing visible changed since the last
	// render (see viewCache.view). update() clears viewValid on every msg except
	// a wheel-accumulate, so a hit here only ever happens when the frame is
	// genuinely identical — chiefly a trackpad wheel flood between flush ticks.
	if m.vcache != nil && m.vcache.viewValid {
		return m.vcache.view
	}
	s := m.renderViewContent()
	if m.vcache != nil {
		m.vcache.view = s
		m.vcache.viewValid = true
	}
	return s
}

func (m *Model) renderViewContent() string {
	// Startup: the panes have nothing in them yet, so draw the progress splash
	// instead of an empty frame that pops into shape one fetch at a time (see
	// splash.go).
	if m.splash.active {
		return m.renderSplash()
	}
	// Render footer first so we know its height — full-help mode expands
	// it from a single line to several, and the body has to shrink to fit.
	footer := m.renderFooter()
	footerH := lipgloss.Height(footer)
	bodyH := m.height - footerH - tabsHeight
	if bodyH < 5 {
		bodyH = 5
	}
	// Record the body height for the mouse layer: composerGeom anchors the
	// compose box from the bottom of the body and must do so without re-rendering
	// the footer (the hover path is alloc-free). Persists via the vcache pointer.
	// The jump-to-bottom target is disarmed here and re-armed by renderMessagesPane,
	// which the Search / Feed / SQL tabs below never reach; the Feed tab's
	// mark-all-read button is disarmed the same way and re-armed by
	// renderFeedPane, so neither leaves a stale target on another tab.
	if m.vcache != nil {
		m.vcache.bodyH = bodyH
		m.vcache.jumpZone = rectZone{}
		m.vcache.feedBtnZone = rectZone{}
	}

	// joins collects the columns where a pane's vertical border lands on the
	// body's first row, so the tab strip's bottom rule can join it there rather
	// than running past it (see renderTeamTabs). Each pane contributes its left
	// and right border column.
	var joins []int
	pane := func(x, w int) {
		if w > 0 {
			joins = append(joins, x, x+w-1)
		}
	}

	var body string
	if m.onSearchTab() {
		body = m.renderSearchPane(bodyH, m.width)
		pane(0, m.width)
	} else if m.onFeedTab() {
		body = m.renderFeedPane(bodyH, m.width)
		pane(0, m.width)
	} else if m.onSQLTab() {
		body = m.renderSQLPane(bodyH, m.width)
		pane(0, m.width)
	} else {
		channelsPane := m.renderChannelsPane(bodyH)
		// One column, not two: the sidebar draws a left border only, and the
		// divider it used to draw belongs to the messages pane below.
		joins = append(joins, 0)
		rightW := m.width - channelsWidth
		if rightW < 10 {
			rightW = 10
		}
		msgsW := rightW
		threadW := 0
		refW := 0
		infoW := 0
		if m.threadOpen {
			threadW = splitRightPane(rightW)
			msgsW = rightW - threadW
		} else if m.refOpen {
			refW = splitRightPane(rightW)
			msgsW = rightW - refW
		} else if m.infoOpen {
			infoW = splitRightPane(rightW)
			msgsW = rightW - infoW
		}
		messagesPane := m.renderMessagesPane(bodyH, msgsW)
		pane(channelsWidth, msgsW)
		panes := []string{channelsPane, messagesPane}
		if m.threadOpen {
			panes = append(panes, m.renderThreadPane(bodyH, threadW))
			pane(channelsWidth+msgsW, threadW)
		} else if m.refOpen {
			panes = append(panes, m.renderRefPane(bodyH, refW))
			pane(channelsWidth+msgsW, refW)
		} else if m.infoOpen {
			panes = append(panes, m.renderInfoPane(bodyH, infoW))
			pane(channelsWidth+msgsW, infoW)
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top, panes...)
	}
	// A full-body overlay replaces everything above it — see bodyOverlays.
	ov := m.activeBodyOverlay()
	if ov != nil {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, ov.render(m, bodyH))
		joins = nil // nothing but padding under the strip while a popup is up
	}
	// The toast floats over the body's top-right corner (see toast.go). A popup
	// owns the whole body while it is open, so the box waits under it rather than
	// being stamped across its border — the timer runs either way, which is the
	// right trade for a notice nobody is waiting on.
	box := ""
	if ov == nil {
		if box = m.renderToast(bodyH); box != "" {
			body = stampBlock(body, box, toastTop, m.toastCol(box))
		}
	}
	m.armToastZone(box)

	tabs := m.renderTeamTabs(joins)

	if screen, ok := joinVerticalLeft(tabs, body, footer); ok {
		return screen
	}
	return lipgloss.JoinVertical(lipgloss.Left, tabs, body, footer)
}

// bodyOverlay is one full-body popup: the state that puts it up, and how to draw
// it. Every overlay covers the whole body — the panes (or whichever tab owns the
// body) are simply not in the frame while one is open.
type bodyOverlay struct {
	active func(*Model) bool
	render func(*Model, int) string // arg: the body height
}

// bodyOverlays is every full-body popup, in the order they used to be applied as
// a chain of ifs — the last active one wins, and it is the only one rendered.
//
// One table rather than one if per popup so that "is a popup covering the body?"
// has a single answer: transcriptHidden asks this same list, and the animation
// gate hangs off it (see refreshAnimVisibility). Two lists would drift, and the
// symptom of drift is an image being animated into a frame that doesn't show it.
//
// Filled in init() rather than as a var initializer only to break an
// initialization cycle the compiler sees but the program can't have: the
// renderers below reach — via the command registries — all the way back to
// renderMessages, and so to transcriptHidden, and so to this table.
var bodyOverlays []bodyOverlay

func init() {
	bodyOverlays = []bodyOverlay{
		{func(m *Model) bool { return m.switcherMode }, func(m *Model, h int) string { return m.renderSwitcher(h) }},
		{func(m *Model) bool { return m.gorillas.active }, func(m *Model, h int) string { return m.renderGorillas(h) }},
		{func(m *Model) bool { return m.kurve.active }, func(m *Model, h int) string { return m.renderKurve(h) }},
		{func(m *Model) bool { return m.stl.active }, func(m *Model, _ int) string { return m.renderSTLView() }},
		{func(m *Model) bool { return m.historyMode }, func(m *Model, _ int) string { return m.renderHistoryPopup() }},
		{func(m *Model) bool { return m.keysSheetMode }, func(m *Model, _ int) string { return m.renderKeysSheetPopup() }},
		{func(m *Model) bool { return m.keyDebugMode }, func(m *Model, _ int) string { return m.renderKeyDebugPopup() }},
		{func(m *Model) bool { return m.textPopup.active }, func(m *Model, _ int) string { return m.renderTextPopup() }},
		{func(m *Model) bool { return m.templatePicker.active }, func(m *Model, _ int) string { return m.renderTemplatePicker() }},
		{func(m *Model) bool { return m.savedPosts.active }, func(m *Model, _ int) string { return m.renderSavedPosts() }},
		{func(m *Model) bool { return m.deleteConfirmPostID != "" }, func(m *Model, _ int) string { return m.renderDeleteConfirm() }},
		{func(m *Model) bool { return m.reactionPickerPostID != "" }, func(m *Model, _ int) string { return m.renderReactionPicker() }},
		{func(m *Model) bool { return m.kaomojiPicker.active }, func(m *Model, _ int) string { return m.renderKaomojiPicker() }},
		{func(m *Model) bool { return m.jiraPicker.active }, func(m *Model, h int) string { return m.renderJiraPicker(h) }},
		{func(m *Model) bool { return m.jiraPointsActive }, func(m *Model, _ int) string { return m.renderJiraPointsInput() }},
		{func(m *Model) bool { return m.jiraCommentActive }, func(m *Model, _ int) string { return m.renderJiraCommentInput() }},
		{func(m *Model) bool { return m.refConfirm.active }, func(m *Model, _ int) string { return m.renderRefConfirm() }},
		{func(m *Model) bool { return m.linkConfirm.active }, func(m *Model, _ int) string { return m.renderLinkConfirm() }},
		{(*Model).openPickerActive, func(m *Model, _ int) string { return m.renderOpenPicker() }},
		{(*Model).codePickerActive, func(m *Model, _ int) string { return m.renderCodePicker() }},
		{func(m *Model) bool { return m.pollDialog.open }, func(m *Model, _ int) string { return m.renderPollDialog() }},
		{func(m *Model) bool { return m.createChan != nil }, func(m *Model, _ int) string { return m.renderCreateChannel() }},
		{func(m *Model) bool { return m.chanEdit != nil }, func(m *Model, _ int) string { return m.renderEditChannel() }},
		{func(m *Model) bool { return m.joinChan != nil }, func(m *Model, _ int) string { return m.renderJoinChannel() }},
		{func(m *Model) bool { return m.chanConfirm != nil }, func(m *Model, _ int) string { return m.renderChannelConfirm() }},
		{func(m *Model) bool { return m.summary.active() }, func(m *Model, _ int) string { return m.renderSummaryPopup() }},
		{func(m *Model) bool { return m.preview.active }, func(m *Model, _ int) string { return m.renderPreviewPopup() }},
	}
}

// activeBodyOverlay returns the popup covering the body — the last active entry,
// matching the old chain, where each if overwrote the one before it — or nil when
// the body is the panes themselves.
func (m *Model) activeBodyOverlay() *bodyOverlay {
	for i := len(bodyOverlays) - 1; i >= 0; i-- {
		if bodyOverlays[i].active(m) {
			return &bodyOverlays[i]
		}
	}
	return nil
}

// transcriptHidden reports whether this frame shows neither the message pane nor
// the thread pane: another tab owns the body, or a popup covers it. The viewport
// geometry can't answer that — it keeps the offsets and row spans of the last
// transcript render either way — so anything asking "is this post's image on the
// screen right now" has to consult this too.
func (m *Model) transcriptHidden() bool {
	return m.onSearchTab() || m.onFeedTab() || m.onSQLTab() || m.activeBodyOverlay() != nil
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
func (m *Model) dmStatusDot(c *model.Channel, filled, hollow string) (string, lipgloss.Style, bool) {
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
func (m *Model) dmCustomStatus(c *model.Channel) (model.CustomStatus, bool) {
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

// rowBar renders one sidebar row inside a full-width bar of st's styling.
// lipgloss wraps a render in a single SGR pair, so any reset the content brings
// itself closes the bar there and every cell after it draws unhighlighted — a
// DM's coloured presence dot cut the hover highlight off before the username,
// and an unread label dropped it before the badge. Re-emitting st's own opening
// sequence after each inner reset keeps the bar continuous; the reset that ends
// the row is left alone so the background can't bleed into the border column.
func rowBar(st lipgloss.Style, width int, content string) string {
	// Probed from st, not from the rendered row: the row's leading escapes may
	// belong to the content (the presence dot's colour), which must not be
	// re-opened over the rest of the bar.
	open := leadingSGR(st.Render("x"))
	out := st.Width(width).Render(content)
	if open == "" {
		return out
	}
	out = strings.ReplaceAll(out, "\x1b[m", "\x1b[m"+open)
	out = strings.ReplaceAll(out, "\x1b[0m", "\x1b[0m"+open)
	return strings.TrimSuffix(out, open)
}

// leadingSGR returns the run of SGR sequences s opens with — the styling a
// lipgloss render puts in front of its content.
func leadingSGR(s string) string {
	i := 0
	for strings.HasPrefix(s[i:], "\x1b[") {
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) { // CSI final byte is @–~
			j++
		}
		if j >= len(s) || s[j] != 'm' {
			break
		}
		i = j + 1
	}
	return s[:i]
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
		prefix := ""
		if m.sidebarUnreadOnly {
			prefix = "unread "
		}
		header = filterStyle.Render("f " + prefix + m.filterValue)
	} else if m.sidebarUnreadOnly {
		header = filterStyle.Render("f unread")
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

	// The unread-only list shrinks under the cursor when another session reads
	// a channel above it; keep the cursor on the last row rather than pointing
	// past the end (which would scroll the whole list out of view). Same kind
	// of bookkeeping as the chanOff write below.
	if m.sidebarUnreadOnly && m.channelIdx >= len(vis) {
		m.channelIdx = max(len(vis)-1, 0)
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
		labelBudget := channelsWidth - 3 - lipgloss.Width(badgeText) - lipgloss.Width(mark)
		labelText := truncate(m.channelLabel(ch), labelBudget)
		switch {
		case mentionN > 0:
			suffix = mentionStyle.Render(labelText) + mark + badgeStyle.Render(badgeText)
		case unreadN > 0:
			suffix = unreadStyle.Render(labelText) + mark + badgeStyle.Render(badgeText)
		default:
			suffix = labelText + mark
		}
		// A display name carrying text effects paints them — statically, colours
		// baked into this cached render (see resolveStaticLine) — but only on a
		// plain row: mention/unread colouring above and the selected/hover bars
		// below all show the plain name, so state styling keeps its meaning.
		// The sentinels are zero-width, so the truncation budget is unchanged.
		fxSuffix := suffix
		if mentionN == 0 && unreadN == 0 && hasEffectPayload(ch.DisplayName) {
			fxSuffix = resolveStaticLine(truncate(m.channelLabelFX(ch), labelBudget)) + mark
		}
		// Presence dot in the left gutter (DMs only): filled+coloured when
		// active, grey hollow when offline. The dot is 1 cell, so the
		// two-column gutter and the truncation math above are unaffected.
		gutter := "  "
		if glyph, st, ok := m.dmStatusDot(ch, statusDot, statusHollowDot); ok {
			gutter = st.Render(glyph) + " "
		}
		row := gutter + fxSuffix
		// The sidebar isn't focusable; always mark the current channel so the
		// user can see where ctrl-nav (and the open transcript) is pointing.
		// The "> " cursor overlays the presence dot on the selected row. A
		// hovered (but unselected) row gets a quieter background bar.
		switch {
		case i == m.channelIdx:
			row = rowBar(selectedRow, channelsWidth-1, "> "+suffix)
		case m.hover.zone == hitChannel && m.hover.idx == i:
			row = rowBar(hoverRowStyle, channelsWidth-1, gutter+suffix)
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
	// No right border: the messages pane's left border is the divider between
	// the two, so the seam is one column instead of two. That leaves this box
	// without a bottom-right corner — the messages pane tees into it (see
	// dividedBorder).
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().
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
		// The name's own effects resolve statically here — a steady colour, not
		// an animation (see resolveStaticLine); the header appended below keeps
		// its sentinels and animates through the per-frame paint pass.
		titleRendered = titleStyle.Render(resolveStaticLine(m.channelLabelFX(ch)))
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
		// The channel header rides the title line, dimmed, after the name and
		// status — with its effect spans marked as sentinels, so the paint pass
		// that colours the message rows (paintEffects on the upper box) animates
		// a \rainbow{…} header for free.
		if h := headerTitleInline(ch.Header); h != "" {
			titleRendered += " " + titleHeaderStyle.Render("· "+h)
		}
	}
	if titleRendered == "" {
		titleRendered = titleStyle.Render(title)
	}

	// Whether the channel's newest message is below the fold, measured before a
	// popup shrinks the viewport below (see msgsMoreBelow).
	moreBelow := m.msgsMoreBelow()

	// Shrink the messages viewport (on this local copy of m) to make
	// room for the @-mention / :emoji popup when it's open. The mutation
	// is scoped to this render call — no side effect on the real model.
	// Only one trigger is ever active at a time, so prefer whichever has
	// candidates.
	popup := m.renderMentionPopup()
	if popup == "" {
		popup = m.renderEmojiPopup()
	}
	if popup == "" {
		popup = m.renderSlashPopup(width)
	}
	if popup == "" {
		popup = m.renderLangPopup()
	}
	if popup == "" {
		popup = m.renderEffectPopup(width)
	}
	if popup == "" {
		popup = m.renderGrammarPopup()
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
	// wrap to a second row (which would offset the scrollbar's row math). The
	// truncation can cut an effect span's end sentinel off; re-close it, or the
	// open span would bleed its colour into every message row below the title.
	titleLine := closeEffectSpans(ansi.Truncate(titleRendered, width-2, "…"))

	// The pane frame lights up only while the message list itself is the active
	// reading target. Focusing the composer (or the attachment chips inside this
	// box) deliberately dims the frame so the reading area visibly blurs — the
	// composer's own top rule / the chip highlight carry the focus instead.
	highlighted := m.paneOwnsKeys(focusMessages)
	borderColor := dimColor
	if highlighted {
		borderColor = focusedColor
	}

	// Split the bordered pane into an upper half (title + message viewport) and a
	// lower half (mention/emoji popup + attachment chips + compose box). Only the
	// lower half changes while you type, so the upper half — whose styling
	// re-measures the display width of every visible message row, the dominant
	// render cost — is memoized and reused verbatim across keystrokes (see
	// renderMsgsUpper / scrollbackCache). The viewport always renders exactly its
	// Height() rows, so title (1 row) + viewport occupy a fixed upperRows; the
	// lower box takes the rest and carries the pane's bottom border. The two left
	// borders abut into one continuous column and any bottom padding lands in the
	// same place, so upper+"\n"+lower is byte-identical to rendering one box.
	upperRows := 1 + m.msgsView.Height()
	lowerH := innerH - upperRows

	// The jump-to-bottom pill rides the viewport's last row, which is only a row
	// the user can see (and click) when the pane splits — the degenerate branch
	// below lets the box clip the viewport instead. Its screen rect is recorded
	// here rather than inside renderMsgsUpper, whose body a cache hit skips.
	var pill jumpPill
	if lowerH >= 1 {
		pill = m.jumpPillFor(moreBelow)
	}
	m.armJumpZone(pill)

	var upper string
	var lowerParts []string
	if lowerH >= 1 {
		upper = m.renderMsgsUpper(width, upperRows, titleLine, borderColor, highlighted, pill)
	} else {
		// Degenerate (very short terminal): no room to split. Fold the title and
		// viewport into the single lower box — byte-identical to the pre-split,
		// uncached render.
		lowerH = innerH
		// Painted before the box here (the border isn't on these lines yet), so no
		// chrome to skip — unlike the cached upper box above. The title line is
		// painted separately: its spans are balanced (closeEffectSpans), so the
		// two calls can't leak state into each other.
		lowerParts = append(lowerParts, m.paintEffects(titleLine, 0), m.paintEffects(m.msgsView.View(), 0))
	}
	if popup != "" {
		lowerParts = append(lowerParts, popup)
	}
	// The compose textarea + attachment chip strip live in whichever pane is
	// currently accepting replies: the thread sidebar when it's open, the
	// messages pane otherwise. ruleRow is where the composer's separator lands
	// inside the lower box, so both edges can meet it (├ here, ┤ in the right
	// border below); the popup and chip strip stack above the input, so it has
	// to be measured rather than assumed.
	ruleRow := -1
	if !m.threadOpen {
		if bar := m.renderAttachmentBar(width - 2); bar != "" {
			lowerParts = append(lowerParts, bar)
		}
		ruleRow = contentRows(lowerParts) // the input box's first line is its top rule
		lowerParts = append(lowerParts, m.renderInputBox(width-2))
	}

	// lipgloss v2 changed Width() semantics: it now sets the OUTER box
	// (border included) instead of the content area. The right edge is
	// painted by renderRightBorder (so the scrollbar can replace the
	// regular `│` when scrolled), so we omit the right border here and
	// pass width-1 — JoinHorizontal with the 1-col right border brings
	// the total back to `width`. The lower box keeps the left+bottom border;
	// Height(lowerH) includes that bottom rule.
	lower := lipgloss.NewStyle().Border(dividedBorder).UnsetBorderTop().UnsetBorderRight().
		Width(width - 1).Height(lowerH).BorderForeground(borderColor).
		Render(lipgloss.JoinVertical(lipgloss.Left, lowerParts...))

	lower = joinRuleRows(lower, ruleRow)

	box := lower
	if upper != "" {
		box = upper + "\n" + lower
		if ruleRow >= 0 {
			ruleRow += upperRows
		}
	}

	// Title row is at index 0, viewport at index 1.
	rightBorder := renderRightBorder(innerH, 1, m.msgsView.Height(), totalRows, scrollPct, borderColor, showScrollbar, ruleRow)
	return lipgloss.JoinHorizontal(lipgloss.Top, box, rightBorder)
}

// renderMsgsUpper renders — and memoizes — the scrollback half of the messages
// pane: the channel title plus the message viewport, framed with the left border
// only (the lower box carries the bottom border). Styling this re-measures the
// display width of every visible row via lipgloss, which a pprof of composer
// typing showed dominating CPU even though the scrollback is unchanged between
// keystrokes. The fingerprint captures every input the bytes depend on, so an
// equal key means byte-identical output and the re-style is skipped. Falls back
// to an uncached render when there's no viewCache (tests build Models without
// one); the output is identical either way.
func (m *Model) renderMsgsUpper(width, upperRows int, titleLine string, borderColor color.Color, highlighted bool, pill jumpPill) string {
	var c *scrollbackCache
	var fp string
	if m.vcache != nil {
		c = &m.vcache.msgsUpper
		// Fingerprint inputs: width + upperRows fix the box geometry; the viewport
		// width, YOffset and content version fix its rendered rows; focus +
		// highlighted fix the border colour (and guard the focus-dependent
		// selection bar baked into the content); the pill's state fixes the
		// overlay on the last row; titleLine is included verbatim (channel name,
		// presence dot, custom status). Equal key ⇒ identical bytes.
		var b strings.Builder
		b.Grow(64 + len(titleLine) + len(pill.text))
		b.WriteString(strconv.Itoa(width))
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(upperRows))
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(m.msgsView.Width()))
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(m.msgsView.YOffset()))
		b.WriteByte('|')
		b.WriteString(strconv.FormatUint(m.msgsContentVer, 10))
		b.WriteByte('|')
		if highlighted {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
		b.WriteString(strconv.Itoa(int(m.focus)))
		b.WriteByte('|')
		if pill.active {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
		if pill.hovered {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
		b.WriteString(strconv.Itoa(pill.col0))
		b.WriteByte('\x1f')
		b.WriteString(pill.text)
		b.WriteByte('\x1f')
		b.WriteString(titleLine)
		fp = b.String()
		if c.valid && c.fp == fp {
			return m.paintEffects(c.rendered, msgsBoxChrome)
		}
	}

	content := lipgloss.JoinVertical(lipgloss.Left, titleLine, overlayJumpPill(m.msgsView.View(), pill))
	s := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().UnsetBorderBottom().
		Width(width - 1).Height(upperRows).BorderForeground(borderColor).
		Render(content)
	if c != nil {
		*c = scrollbackCache{fp: fp, rendered: s, valid: true}
	}
	// Paint the text effects *after* the cache, never into it: the stored box keeps
	// its invisible sentinels, so it stays valid across animation frames and the
	// fingerprint above needs no phase. A frame is then just this recolour of the
	// visible rows, not a re-style of the box (which is the expensive part, and the
	// reason this cache exists at all).
	return m.paintEffects(s, msgsBoxChrome)
}

// msgsBoxChrome is the number of leading runes on each line of the messages box
// that belong to the frame, not the message: the left border. paintEffects copies
// them through unpainted, so a span still open across a soft-wrap can't colour
// the border column of the continuation row.
const msgsBoxChrome = 1

// renderInputBox renders the compose textarea with a top rule, sized to
// fit `width` columns. Border colour mirrors focus. Used by both the
// messages pane (thread closed) and the thread pane (thread open).
func (m *Model) renderInputBox(width int) string {
	inputBorder := dimColor
	if m.focus == focusInput {
		inputBorder = focusedColor
	}
	// Grammar/spell findings are drawn by the editor itself (pushed as inline
	// decorations in syncGrammarDecorations), so they always track the wrap and
	// scroll — no post-processing of the rendered output here.
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(inputBorder).
		Width(width).
		Render(m.input.View())

	// When someone is typing in the open channel, lay the animated dots
	// over the top rule (the separator) so the cue rides the line it
	// already draws and costs no extra height. The first rendered line is
	// the top border; replacing it keeps every other line untouched.
	if m.typingIndicatorVisible() {
		if i := strings.IndexByte(box, '\n'); i >= 0 {
			box = overlayTypingDots(m.typingIndicator.phase, width, inputBorder, m.typingLabel()) + box[i:]
		}
	}
	return box
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

	// The thread pane isn't memoized, so its effects are painted on the bare
	// viewport rows, before the box adds a border (chrome 0).
	parts := []string{titleStyle.Render(title), m.paintEffects(m.threadView.View(), 0)}
	if bar := m.renderAttachmentBar(width - 2); bar != "" {
		parts = append(parts, bar)
	}
	// Directly above the composer, so the last thing read before typing is what
	// the reply will hang under.
	if bar := m.replyBar(width - 2); bar != "" {
		parts = append(parts, bar)
	}
	ruleRow := contentRows(parts) // the input box's first line is its top rule
	parts = append(parts, m.renderInputBox(width-2))
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	// As with the messages pane, the thread frame stays bright only while the
	// thread transcript has focus; composing a reply (or touching its attachment
	// chips) dims it so the reading area blurs and the composer rule stands out.
	borderColor := dimColor
	if m.paneOwnsKeys(focusThread) {
		borderColor = focusedColor
	}

	// Right edge handled by renderRightBorder so the scrollbar can
	// overlay it (see renderMessagesPane for details).
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().
		Width(width - 1).Height(innerH).BorderForeground(borderColor)
	box := joinRuleRows(style.Render(content), ruleRow)

	rightBorder := renderRightBorder(innerH, 1, m.threadView.Height(), threadTotal, threadPct, borderColor, showScrollbar, ruleRow)
	return lipgloss.JoinHorizontal(lipgloss.Top, box, rightBorder)
}

func replyWord(n int) string {
	if n == 1 {
		return "reply"
	}
	return "replies"
}

// renderTeamTabs draws the tab strip. joins lists the columns where a pane
// border starts on the row directly below it; the strip's bottom rule grows a
// down arm at each so the tabs and the body read as one frame (see ruleLine).
func (m *Model) renderTeamTabs(joins []int) string {
	// Pre-compute distinct counts for the Feed tab badge; the memo is keyed on
	// them, so they are needed either way.
	unreadCh, mentionCh := m.feedBadgeCounts()
	if m.vcache == nil {
		return m.buildTeamTabs(joins, unreadCh, mentionCh)
	}
	c := &m.vcache.tabs
	fp := m.tabsFingerprint(joins, unreadCh, mentionCh)
	if c.valid && c.fp == fp {
		// The strip was not re-laid-out, so its click zones weren't either.
		m.vcache.tabZones = c.zones
		return c.out
	}
	out := m.buildTeamTabs(joins, unreadCh, mentionCh)
	*c = tabsCache{fp: fp, out: out, zones: m.vcache.tabZones, valid: true}
	return out
}

// tabsFingerprint hashes what the tab strip is made of. Two frames with the
// same fingerprint draw the same strip, down to the bytes.
func (m *Model) tabsFingerprint(joins []int, unreadCh, mentionCh int) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	put := func(n int) {
		binary.LittleEndian.PutUint64(buf[:], uint64(n))
		h.Write(buf[:])
	}
	put(m.width)
	put(m.teamIdx)
	put(unreadCh)
	put(mentionCh)
	hovered := -1
	if m.hover.zone == hitTab {
		hovered = m.hover.idx
	}
	put(hovered)
	for _, c := range joins {
		put(c)
	}
	maxIdx := m.maxTeamIdx()
	put(maxIdx)
	for i := 0; i <= maxIdx; i++ {
		kind, id, name := m.tabAt(i)
		put(int(kind))
		io.WriteString(h, id)
		io.WriteString(h, "\x1f")
		io.WriteString(h, name)
		io.WriteString(h, "\x1f")
	}
	return h.Sum64()
}

func (m *Model) buildTeamTabs(joins []int, unreadCh, mentionCh int) string {
	if len(m.teams) == 0 && !m.hasDMs {
		// Reserve the same vertical space so body math stays consistent.
		blank := strings.Repeat("\n", tabsHeight-1)
		return footerStyle.Render(" (no teams) ") + blank
	}
	maxIdx := m.maxTeamIdx()

	// The tab strip is always navigable (ctrl-←/→ from any focus), so the
	// active tab is always highlighted rather than tracking a focus.
	activeColor := focusedColor

	// Tabs split into a sticky synthetic prefix (DMs/Feed/Search,
	// always shown) and a scrollable team suffix. teamTabs collects the
	// latter so a window can be chosen around the active team when they
	// don't all fit; activeTeamPos is the active team's index within it.
	// Each rendered tab carries its tab-bar index so a click can map an
	// x-coordinate back to the tab (see hitTest); sticky tabs (DMs/Feed/Search)
	// and the scrollable team tabs both record it.
	type teamTab struct {
		s      string
		w      int
		idx    int
		active bool
	}
	var sticky []teamTab
	stickyW := 0
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
			case kind == tabSQL:
				style = style.Foreground(sqlTabColor)
			case kind == tabFeed && mentionCh > 0:
				style = style.Foreground(mentionTabColor).Bold(true)
			case kind == tabFeed && unreadCh > 0:
				style = style.Foreground(feedTabColor).Bold(true)
			case kind == tabFeed:
				style = style.Foreground(feedTabColor)
			default:
				style = style.Foreground(dimColor)
			}
			// Hover affordance: underline the inactive tab the pointer is over.
			if m.hover.zone == hitTab && m.hover.idx == i {
				style = style.Underline(true)
			}
		}

		rendered := style.Render(label)
		tab := teamTab{s: rendered, w: lipgloss.Width(rendered), idx: i, active: isActive}
		if kind == tabTeam {
			if isActive {
				activeTeamPos = len(teamTabs)
			}
			teamTabs = append(teamTabs, tab)
		} else {
			sticky = append(sticky, tab)
			stickyW += tab.w
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
	// Record each tab's horizontal extent as pieces are laid out left to right,
	// so a click resolves to the right tab without replaying this windowing.
	var zones []tabZone
	// The bottom rule is collected cell by cell alongside the pieces instead of
	// being taken from the rendered tab boxes: a pane border below can meet the
	// strip anywhere, including mid-tab, which a per-tab lipgloss.Border can't
	// express. ruleLine paints these cells at the end.
	var rule []ruleCell
	xpos := 0
	place := func(t teamTab) {
		pieces = append(pieces, t.s)
		zones = append(zones, tabZone{x0: xpos, x1: xpos + t.w, idx: t.idx})
		rule = appendTabRule(rule, t.w, t.active)
		xpos += t.w
	}
	for _, t := range sticky {
		place(t)
	}
	if leftClip {
		arrow := scrollArrow("‹")
		pieces = append(pieces, arrow)
		rule = append(rule, ruleCell{glyph: '─'})
		xpos += lipgloss.Width(arrow)
	}
	for i := start; i < end; i++ {
		place(teamTabs[i])
	}
	if rightClip {
		pieces = append(pieces, scrollArrow("›"))
		rule = append(rule, ruleCell{glyph: '─'})
	}
	if m.vcache != nil {
		m.vcache.tabZones = zones
	}

	row := lipgloss.JoinHorizontal(lipgloss.Top, pieces...)

	// Extend a horizontal rule across the remaining width so the tab
	// bar reads as a single header strip instead of a floating widget.
	if fill := m.width - lipgloss.Width(row); fill > 0 {
		blank := strings.Repeat(" ", fill)
		fillBlock := blank + "\n" + blank + "\n" + blank
		row = lipgloss.JoinHorizontal(lipgloss.Top, row, fillBlock)
		for i := 0; i < fill; i++ {
			rule = append(rule, ruleCell{glyph: '─'})
		}
	}

	// Cells past the terminal's edge are clipped, so the last *visible* column
	// is where the strip closes — the tabs can overrun m.width on a very narrow
	// terminal (the sticky DMs/Feed/Search tabs never scroll away).
	visible := len(rule)
	if m.width > 0 && m.width < visible {
		visible = m.width
	}
	// The leftmost column always sits on the body's left border, join or not.
	if len(rule) > 0 {
		rule[0].glyph = joinGlyph(rule[0].glyph, 0, visible)
	}
	for _, c := range joins {
		if c > 0 && c < len(rule) {
			rule[c].glyph = joinGlyph(rule[c].glyph, c, visible)
		}
	}

	lines := strings.Split(row, "\n")
	lines[len(lines)-1] = ruleLine(rule, activeColor)
	return strings.Join(lines, "\n")
}

// ruleCell is one column of the tab strip's bottom rule: the glyph to draw and
// whether it belongs to the active tab, which paints it in the accent colour.
type ruleCell struct {
	glyph  rune
	active bool
}

// appendTabRule appends the w cells one tab contributes to the bottom rule —
// the same glyphs activeTabBorder / inactiveTabBorder would have drawn. The
// active tab's bottom is open (blank between its corners) so it merges with
// the pane below.
func appendTabRule(rule []ruleCell, w int, active bool) []ruleCell {
	left, mid, right := '┴', '─', '┴'
	if active {
		left, mid, right = '┘', ' ', '└'
	}
	for i := 0; i < w; i++ {
		g := mid
		switch i {
		case 0:
			g = left
		case w - 1:
			g = right
		}
		rule = append(rule, ruleCell{glyph: g, active: active})
	}
	return rule
}

// Bottom-rule cells are reshaped as sets of arms rather than as glyphs: a pane
// border meeting the strip adds a down arm, and the strip's own ends drop the
// arm that would point off-screen — which is one rule each, instead of a
// substitution table per glyph pair.
const (
	armUp = 1 << iota
	armDown
	armLeft
	armRight
)

// joinGlyph returns glyph with a downward arm added — so the cell hands off to
// the pane border starting directly beneath it — trimmed of the arm that would
// dangle when the cell is the first or last column of a width-wide strip. Under
// the active tab's open bottom (a blank cell) the border simply continues as │.
func joinGlyph(glyph rune, col, width int) rune {
	arms := glyphArms(glyph) | armDown
	if col == 0 {
		arms &^= armLeft
	}
	if col == width-1 {
		arms &^= armRight
	}
	return armsGlyph(arms)
}

func glyphArms(glyph rune) int {
	switch glyph {
	case '─':
		return armLeft | armRight
	case '│':
		return armUp | armDown
	case '┴':
		return armUp | armLeft | armRight
	case '┬':
		return armDown | armLeft | armRight
	case '├':
		return armUp | armDown | armRight
	case '┤':
		return armUp | armDown | armLeft
	case '┼':
		return armUp | armDown | armLeft | armRight
	case '┘':
		return armUp | armLeft
	case '└':
		return armUp | armRight
	case '┐':
		return armDown | armLeft
	case '┌':
		return armDown | armRight
	}
	return 0 // blank — the active tab's open bottom
}

func armsGlyph(arms int) rune {
	switch arms {
	case armLeft | armRight, armLeft, armRight:
		return '─'
	case armUp | armDown, armUp, armDown:
		return '│'
	case armUp | armLeft | armRight:
		return '┴'
	case armDown | armLeft | armRight:
		return '┬'
	case armUp | armDown | armRight:
		return '├'
	case armUp | armDown | armLeft:
		return '┤'
	case armUp | armDown | armLeft | armRight:
		return '┼'
	case armUp | armLeft:
		return '┘'
	case armUp | armRight:
		return '└'
	case armDown | armLeft:
		return '┐'
	case armDown | armRight:
		return '┌'
	}
	return ' '
}

// ruleLine renders the collected cells, batching runs of the same colour so a
// full-width rule costs a handful of styled writes rather than one per column.
func ruleLine(rule []ruleCell, activeColor color.Color) string {
	dim := lipgloss.NewStyle().Foreground(dimColor)
	hot := lipgloss.NewStyle().Foreground(activeColor)
	var b strings.Builder
	for i := 0; i < len(rule); {
		j := i
		var seg []rune
		for ; j < len(rule) && rule[j].active == rule[i].active; j++ {
			seg = append(seg, rule[j].glyph)
		}
		st := dim
		if rule[i].active {
			st = hot
		}
		b.WriteString(st.Render(string(seg)))
		i = j
	}
	return b.String()
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
	// Hovering a link shows where it goes, taking over the status slot for as long
	// as the pointer rests on it (truncated so a long URL can't crowd out the help).
	if m.hoverLink.url != "" {
		if label, ok := m.actionHoverHint(m.hoverLink.url); ok {
			right = label // a copy chip / spoiler / image shows a friendly label, not its internal URL
		} else {
			hint := m.width / 2
			if hint < 24 {
				hint = 24
			}
			right = "↗ " + truncate(m.hoverLink.url, hint)
		}
	}
	// When the cursor rests on an underlined mistake (and nothing more urgent
	// holds the slot), surface its label + top suggestion with the alt+g cue.
	if right == "" {
		if hint := m.grammarHint(); hint != "" {
			right = hint
		}
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

	left := footerStyle.Render(prefix) + m.helpView()
	if m.help.ShowAll {
		// The expanded help lists every layer that is live right now, but a
		// narrow terminal still ellipsizes the rightmost columns — so point at
		// the sheet that never truncates.
		left += "\n" + footerStyle.Render(helpKey(m.keys.CommandPicker)+" › Keys — the full cheatsheet")
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

// helpView renders the footer's help line, memoized on the bindings it lists.
// help.View styles and measures every binding on the line, which is a real
// cost on a frame that changed nothing about the footer — and the empty feed's
// blob field renders such frames at up to 60 fps.
//
// The expanded help (`?`) is not cached: it lists every layer at once, it is
// not what an idle animation redraws, and fingerprinting it would cost more
// than rendering it.
func (m *Model) helpView() string {
	if m.vcache == nil || m.help.ShowAll {
		return m.help.View(m)
	}
	fp := helpFingerprint(m.ShortHelp())
	w := m.help.Width()
	c := &m.vcache.help
	if c.valid && c.fp == fp && c.width == w {
		return c.out
	}
	out := m.help.View(m)
	*c = helpCache{fp: fp, width: w, out: out, valid: true}
	return out
}

// helpFingerprint hashes the bindings a help line is about to show: the label
// pair it renders for each, and whether it renders it at all. Two lines with
// the same fingerprint render the same bytes.
func helpFingerprint(bindings []key.Binding) uint64 {
	h := fnv.New64a()
	var one [1]byte
	for _, b := range bindings {
		one[0] = 0
		if b.Enabled() {
			one[0] = 1
		}
		h.Write(one[:])
		hb := b.Help()
		io.WriteString(h, hb.Key)
		one[0] = 0x1f
		h.Write(one[:])
		io.WriteString(h, hb.Desc)
		h.Write(one[:])
	}
	return h.Sum64()
}
