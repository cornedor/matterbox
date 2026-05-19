package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

const channelsWidth = 26

// maxInputHeight bounds how many rows the input textarea is allowed to
// expand to as content grows. The textarea scrolls internally once the
// content exceeds this — the messages pane keeps the remaining space.
const maxInputHeight = 6

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
	unreadTabColor    = lipgloss.Color("11") // yellow when there are unreads
	mentionTabColor   = lipgloss.Color("9")  // red when there are mentions
	footerStyle       = lipgloss.NewStyle().Foreground(dimColor)
	filterStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	unreadStyle  = lipgloss.NewStyle().Bold(true)
	mentionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true) // red
	attachmentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))         // cyan
	selectedBarStyle = lipgloss.NewStyle().Foreground(focusedColor)               // selected-post left bar
	replyHintStyle   = lipgloss.NewStyle().Foreground(dimColor)                   // ↳ reply, ↪ N replies
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

func (m *Model) resizeMessagesViewport() {
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
	if m.threadOpen {
		threadW = rightW / 2
		if threadW < threadPaneMinWidth {
			threadW = threadPaneMinWidth
		}
		if threadW > rightW-threadPaneMinWidth {
			threadW = rightW - threadPaneMinWidth
		}
		msgsW = rightW - threadW
	}
	m.msgsView.SetWidth(msgsW - 2)
	attBarH := m.attachmentBarHeight(msgsW - 2)
	// bodyH already excludes the tab strip + footer. The messages pane
	// also has to make room for its bottom border (1), title row (1), the
	// top-border row above the input (1), the input itself (variable),
	// and any attachment chip strip. Everything left is the viewport.
	mh := bodyH - 2 - 1 - 1 - m.input.Height() - attBarH
	if mh < 1 {
		mh = 1
	}
	m.msgsView.SetHeight(mh)
	tw := threadW - 4
	if tw < 1 {
		tw = 1
	}
	m.threadView.SetWidth(tw)
	th := bodyH - 3 - 2
	if th < 1 {
		th = 1
	}
	m.threadView.SetHeight(th)
	m.renderMessages()
	m.renderThread()
}

const threadPaneMinWidth = 24

func (m *Model) resizeInput() {
	if m.threadOpen {
		// Input lives under the messages pane; size to that pane.
		w := m.msgsView.Width()
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

// syncInputHeight grows / shrinks the input textarea to fit its current
// content (1..maxInputHeight rows) and reflows the messages viewport
// when the height changes. Safe to call after every keystroke; it
// short-circuits when the height is already correct so renderMessages
// doesn't churn for every character.
func (m *Model) syncInputHeight() {
	want := m.input.LineCount()
	if want < 1 {
		want = 1
	}
	if want > maxInputHeight {
		want = maxInputHeight
	}
	if want == m.input.Height() {
		return
	}
	m.input.SetHeight(want)
	m.resizeMessagesViewport()
}

func (m *Model) renderMessages() {
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
	var allLines []string
	selStart, selEnd := -1, -1
	for i, p := range m.posts {
		chunk := m.renderPostLines(p)
		if i == m.postIdx {
			selStart = len(allLines)
			if decorate {
				for j, l := range chunk {
					// Replace the two-space gutter with the bar so the
					// selected post's lines stay at the same x-position.
					if strings.HasPrefix(l, "  ") {
						chunk[j] = bar + " " + l[2:]
					} else {
						chunk[j] = bar + " " + l
					}
				}
			}
			selEnd = selStart + len(chunk)
		}
		allLines = append(allLines, chunk...)
	}
	m.msgsView.SetContent(strings.Join(allLines, "\n"))

	if h := m.msgsView.Height(); h > 0 && selStart >= 0 {
		off := m.msgsView.YOffset()
		switch {
		case selStart < off:
			off = selStart
		case selEnd > off+h:
			off = selEnd - h
		}
		if off < 0 {
			off = 0
		}
		m.msgsView.SetYOffset(off)
	}
}

// renderThread populates the thread viewport with the loaded thread
// posts, mirroring renderMessages' selection-bar behaviour for the
// focused row.
func (m *Model) renderThread() {
	if !m.threadOpen {
		return
	}
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
	var allLines []string
	selStart, selEnd := -1, -1
	for i, p := range m.threadPosts {
		chunk := m.renderThreadPostLines(p, i == 0)
		if i == m.threadIdx {
			selStart = len(allLines)
			if decorate {
				for j, l := range chunk {
					if strings.HasPrefix(l, "  ") {
						chunk[j] = bar + " " + l[2:]
					} else {
						chunk[j] = bar + " " + l
					}
				}
			}
			selEnd = selStart + len(chunk)
		}
		allLines = append(allLines, chunk...)
	}
	m.threadView.SetContent(strings.Join(allLines, "\n"))

	if h := m.threadView.Height(); h > 0 && selStart >= 0 {
		off := m.threadView.YOffset()
		switch {
		case selStart < off:
			off = selStart
		case selEnd > off+h:
			off = selEnd - h
		}
		if off < 0 {
			off = 0
		}
		m.threadView.SetYOffset(off)
	}
}

// renderThreadPostLines is the thread-sidebar variant of
// renderPostLines: the root post gets no ↳/↪ hint (it IS the root),
// replies omit the reply hint since context makes it obvious.
func (m *Model) renderThreadPostLines(p *model.Post, isRoot bool) []string {
	name := m.userNames[p.UserId]
	if name == "" {
		name = p.UserId
		if len(name) > 8 {
			name = name[:8]
		}
	}
	ts := time.UnixMilli(p.CreateAt).Local().Format("15:04")
	header := userStyle.Render(name) + "  " + timeStyle.Render(ts)
	if isRoot {
		header += "  " + replyHintStyle.Render("· root")
	}
	lines := []string{header}
	if body := renderMarkdown(p.Message); body != "" {
		lines = append(lines, strings.Split(body, "\n")...)
	}
	if att := renderAttachments(p, m.threadView.Width()); att != "" {
		lines = append(lines, strings.Split(att, "\n")...)
	}
	return lines
}

// renderPostLines returns one rendered line per visual row of a post:
// the header line, the (possibly multi-line) body, and one line per
// attachment. Existing styles already include a two-space left gutter
// on body and attachment lines.
func (m *Model) renderPostLines(p *model.Post) []string {
	name := m.userNames[p.UserId]
	if name == "" {
		name = p.UserId
		if len(name) > 8 {
			name = name[:8]
		}
	}
	ts := time.UnixMilli(p.CreateAt).Local().Format("15:04")
	header := userStyle.Render(name) + "  " + timeStyle.Render(ts)
	if p.RootId != "" {
		header = replyHintStyle.Render("↳ ") + header
	} else if p.ReplyCount > 0 {
		header += "  " + replyHintStyle.Render(fmt.Sprintf("↪ %d", p.ReplyCount))
	}
	lines := []string{header}
	if body := renderMarkdown(p.Message); body != "" {
		lines = append(lines, strings.Split(body, "\n")...)
	}
	if att := renderAttachments(p, m.msgsView.Width()); att != "" {
		lines = append(lines, strings.Split(att, "\n")...)
	}
	return lines
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

func (m Model) viewContent() string {
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

	channelsPane := m.renderChannelsPane(bodyH)
	rightW := m.width - channelsWidth
	if rightW < 10 {
		rightW = 10
	}
	msgsW := rightW
	threadW := 0
	if m.threadOpen {
		threadW = rightW / 2
		if threadW < threadPaneMinWidth {
			threadW = threadPaneMinWidth
		}
		if threadW > rightW-threadPaneMinWidth {
			threadW = rightW - threadPaneMinWidth
		}
		msgsW = rightW - threadW
	}
	messagesPane := m.renderMessagesPane(bodyH, msgsW)
	panes := []string{channelsPane, messagesPane}
	if m.threadOpen {
		panes = append(panes, m.renderThreadPane(bodyH, threadW))
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, panes...)
	if m.switcherMode {
		body = lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, m.renderSwitcher())
	}

	tabs := m.renderTeamTabs()

	return lipgloss.JoinVertical(lipgloss.Left, tabs, body, footer)
}

func (m Model) renderChannelsPane(height int) string {
	innerH := height - 1 // border (bottom only; top connects to tab strip)
	if innerH < 1 {
		innerH = 1
	}

	vis := m.visibleChannels()

	// Header line: title or filter input.
	var header string
	if m.filterMode {
		header = filterStyle.Render(m.filter.View())
	} else if m.filterValue != "" {
		header = filterStyle.Render("/ " + m.filterValue)
	} else {
		title := "Channels"
		switch m.currentTeamID() {
		case dmTeamID:
			title = "DMs"
		case unreadTeamID:
			title = "Unread"
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
		labelText := truncate(m.channelLabel(ch), channelsWidth-4-lipgloss.Width(badgeText))
		switch {
		case mentionN > 0:
			suffix = mentionStyle.Render(labelText) + badgeStyle.Render(badgeText)
		case unreadN > 0:
			suffix = unreadStyle.Render(labelText) + badgeStyle.Render(badgeText)
		default:
			suffix = labelText
		}
		row := "  " + suffix
		if i == m.channelIdx {
			row = "> " + suffix
			if m.focus == focusChannels {
				row = selectedRow.Width(channelsWidth - 2).Render(row)
			}
		}
		rows = append(rows, row)
	}
	if len(vis) == 0 {
		empty := "  (none)"
		if m.currentTeamID() == unreadTeamID {
			empty = "  all caught up"
		}
		rows = append(rows, footerStyle.Render(empty))
	}
	for len(rows) < innerH {
		rows = append(rows, "")
	}

	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().Width(channelsWidth).Height(innerH)
	if m.focus == focusChannels {
		style = style.BorderForeground(focusedColor)
	} else {
		style = style.BorderForeground(dimColor)
	}
	return style.Render(strings.Join(rows, "\n"))
}

func (m Model) renderMessagesPane(height, width int) string {
	innerH := height - 1 // border (bottom only; top connects to tab strip)
	if innerH < 1 {
		innerH = 1
	}
	if width < 10 {
		width = 10
	}

	vis := m.visibleChannels()
	title := "Messages"
	if m.channelIdx < len(vis) {
		title = m.channelLabel(vis[m.channelIdx])
	}

	// Shrink the messages viewport (on this local copy of m) to make
	// room for the @-mention popup when it's open. The mutation is
	// scoped to this render call — no side effect on the real model.
	popup := m.renderMentionPopup()
	if popup != "" {
		popupH := lipgloss.Height(popup)
		h := m.msgsView.Height() - popupH
		if h < 1 {
			h = 1
		}
		m.msgsView.SetHeight(h)
	}
	msgsPart := m.msgsView.View()

	inputBorder := dimColor
	if m.focus == focusInput {
		inputBorder = focusedColor
	}
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(inputBorder).
		Width(width - 2).
		Render(m.input.View())

	parts := []string{titleStyle.Render(title), msgsPart}
	if popup != "" {
		parts = append(parts, popup)
	}
	if bar := m.renderAttachmentBar(width - 2); bar != "" {
		parts = append(parts, bar)
	}
	parts = append(parts, inputBox)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	// lipgloss v2 changed Width() semantics: it now sets the OUTER box
	// (border included) instead of the content area. The `width` we got
	// is the intended outer width, so pass it directly.
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().Width(width).Height(innerH)
	if m.focus == focusMessages || m.focus == focusInput || m.focus == focusAttachments {
		style = style.BorderForeground(focusedColor)
	} else {
		style = style.BorderForeground(dimColor)
	}
	return style.Render(content)
}

// attachmentBarHeight returns the rendered height of the chip strip
// (0 when empty). Used by resizeMessagesViewport so the messages
// viewport shrinks to make room for chips.
func (m Model) attachmentBarHeight(width int) int {
	bar := m.renderAttachmentBar(width)
	if bar == "" {
		return 0
	}
	return lipgloss.Height(bar)
}

func (m Model) renderThreadPane(height, width int) string {
	innerH := height - 1 // border (bottom only; top connects to tab strip)
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

	body := m.threadView.View()

	content := lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render(title), body)

	// v2 Width = outer width (see renderMessagesPane).
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().Width(width).Height(innerH)
	if m.focus == focusThread {
		style = style.BorderForeground(focusedColor)
	} else {
		style = style.BorderForeground(dimColor)
	}
	return style.Render(content)
}

func replyWord(n int) string {
	if n == 1 {
		return "reply"
	}
	return "replies"
}

func (m Model) renderTeamTabs() string {
	if len(m.teams) == 0 && !m.hasDMs {
		// Reserve the same vertical space so body math stays consistent.
		blank := strings.Repeat("\n", tabsHeight-1)
		return footerStyle.Render(" (no teams) ") + blank
	}
	// Pre-compute distinct counts for the Unread tab badge.
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
	rendered := make([]string, 0, maxIdx+1)

	activeColor := dimColor
	if m.focus == focusTeams {
		activeColor = focusedColor
	}

	for i := 0; i <= maxIdx; i++ {
		kind, _, name := m.tabAt(i)
		label := name
		if kind == tabUnread {
			switch {
			case mentionCh > 0:
				label = "Unread " + strconv.Itoa(mentionCh) + "!"
			case unreadCh > 0:
				label = "Unread " + strconv.Itoa(unreadCh)
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
			case kind == tabUnread && mentionCh > 0:
				style = style.Foreground(mentionTabColor).Bold(true)
			case kind == tabUnread && unreadCh > 0:
				style = style.Foreground(unreadTabColor).Bold(true)
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

		rendered = append(rendered, style.Render(label))
	}

	row := lipgloss.JoinHorizontal(lipgloss.Top, rendered...)

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

func (m Model) renderFooter() string {
	right := m.status
	if right == "" && m.me != nil {
		right = m.me.Username
	}

	// Leave room for the right-hand status and a one-cell gutter so the
	// help bubble can ellipsize cleanly if the bindings don't all fit.
	avail := m.width - lipgloss.Width(right) - 1
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
	case m.focus == focusInput && m.threadOpen:
		prefix = "↳ replying in thread  "
	case m.focus == focusInput:
		prefix = "type to send  "
	}

	left := footerStyle.Render(prefix) + m.help.View(m)
	if m.help.ShowAll {
		// Full help is multi-line; right-align the status on the last row.
		gap := m.width - lipgloss.Width(lastLine(left)) - lipgloss.Width(right)
		if gap < 1 {
			gap = 1
		}
		return left + strings.Repeat(" ", gap) + footerStyle.Render(right)
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + footerStyle.Render(right)
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
	r := []rune(s)
	if len(r) <= n-1 {
		return s
	}
	return string(r[:n-1]) + "…"
}
