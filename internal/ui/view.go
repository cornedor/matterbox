package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattermost/mattermost/server/public/model"
)

const channelsWidth = 26

var (
	border       = lipgloss.NormalBorder()
	focusedColor = lipgloss.Color("12")  // bright blue
	dimColor     = lipgloss.Color("241") // grey

	titleStyle    = lipgloss.NewStyle().Bold(true)
	userStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	timeStyle     = lipgloss.NewStyle().Foreground(dimColor)
	selectedRow   = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("8"))
	unselectedRow = lipgloss.NewStyle()
	tabActive     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12")).Padding(0, 1)
	tabInactive   = lipgloss.NewStyle().Foreground(dimColor).Padding(0, 1)
	dmTabStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Padding(0, 1)
	unreadTab     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11")).Padding(0, 1) // yellow when there are unreads
	mentionTab    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")).Padding(0, 1)  // red when there are mentions
	footerStyle   = lipgloss.NewStyle().Foreground(dimColor)
	filterStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	unreadStyle  = lipgloss.NewStyle().Bold(true)
	mentionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true) // red
	attachmentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))         // cyan
	selectedBarStyle = lipgloss.NewStyle().Foreground(focusedColor)               // selected-post left bar
	replyHintStyle   = lipgloss.NewStyle().Foreground(dimColor)                   // ↳ reply, ↪ N replies
)

func (m *Model) resizeMessagesViewport() {
	bodyH := m.height - 4
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
	m.msgsView.Width = msgsW - 2
	m.msgsView.Height = bodyH - 3 - 2
	if m.msgsView.Height < 1 {
		m.msgsView.Height = 1
	}
	m.threadView.Width = threadW - 4
	if m.threadView.Width < 1 {
		m.threadView.Width = 1
	}
	m.threadView.Height = bodyH - 3 - 2
	if m.threadView.Height < 1 {
		m.threadView.Height = 1
	}
	m.renderMessages()
	m.renderThread()
}

const threadPaneMinWidth = 24

func (m *Model) resizeInput() {
	if m.threadOpen {
		// Input lives under the messages pane; size to that pane.
		w := m.msgsView.Width
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

	if h := m.msgsView.Height; h > 0 && selStart >= 0 {
		off := m.msgsView.YOffset
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

	if h := m.threadView.Height; h > 0 && selStart >= 0 {
		off := m.threadView.YOffset
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
	if att := renderAttachments(p, m.threadView.Width); att != "" {
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
	if att := renderAttachments(p, m.msgsView.Width); att != "" {
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

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "starting…"
	}

	bodyH := m.height - 2
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

	tabs := m.renderTeamTabs()
	footer := m.renderFooter()

	return lipgloss.JoinVertical(lipgloss.Left, body, tabs, footer)
}

func (m Model) renderChannelsPane(height int) string {
	innerH := height - 2 // borders
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

	style := lipgloss.NewStyle().Border(border).Width(channelsWidth).Height(innerH)
	if m.focus == focusChannels {
		style = style.BorderForeground(focusedColor)
	} else {
		style = style.BorderForeground(dimColor)
	}
	return style.Render(strings.Join(rows, "\n"))
}

func (m Model) renderMessagesPane(height, width int) string {
	innerH := height - 2
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
		h := m.msgsView.Height - popupH
		if h < 1 {
			h = 1
		}
		m.msgsView.Height = h
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
	parts = append(parts, inputBox)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	style := lipgloss.NewStyle().Border(border).Width(width - 2).Height(innerH)
	if m.focus == focusMessages || m.focus == focusInput {
		style = style.BorderForeground(focusedColor)
	} else {
		style = style.BorderForeground(dimColor)
	}
	return style.Render(content)
}

func (m Model) renderThreadPane(height, width int) string {
	innerH := height - 2
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

	style := lipgloss.NewStyle().Border(border).Width(width - 2).Height(innerH)
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
		return footerStyle.Render(" (no teams) ")
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
	var parts []string
	maxIdx := m.maxTeamIdx()
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
		selected := i == m.teamIdx
		switch {
		case selected && m.focus == focusTeams:
			parts = append(parts, tabActive.Render("[ "+label+" ]"))
		case selected:
			parts = append(parts, tabActive.Render(label))
		case kind == tabDM:
			parts = append(parts, dmTabStyle.Render(label))
		case kind == tabUnread && mentionCh > 0:
			parts = append(parts, mentionTab.Render(label))
		case kind == tabUnread && unreadCh > 0:
			parts = append(parts, unreadTab.Render(label))
		default:
			parts = append(parts, tabInactive.Render(label))
		}
	}
	row := strings.Join(parts, " ")
	if m.focus == focusTeams {
		row += footerStyle.Render("  ←/→ switch, enter to load")
	}
	return row
}

func (m Model) renderFooter() string {
	var left string
	switch {
	case m.filterMode:
		left = "type to filter  enter: apply+open  esc: cancel"
	case m.focus == focusInput:
		if m.threadOpen {
			left = "↳ replying in thread  enter: send  alt+enter: newline  esc: leave  tab: next"
		} else {
			left = "type to send  enter: send  alt+enter: newline  esc: leave  tab: next"
		}
	case m.focus == focusChannels:
		left = "tab: focus  ↑↓ nav  enter: open  /: filter  esc: clear filter  q: quit"
	case m.focus == focusMessages:
		left = "tab: focus  ↑↓/jk select  enter: open thread  o: open attachment  q: quit"
	case m.focus == focusThread:
		left = "tab: focus  ↑↓/jk select  o: open attachment  esc: close thread  q: quit"
	default:
		left = "tab: focus  ↑↓/←→ nav  enter: open  q: quit"
	}
	right := m.status
	if right == "" && m.me != nil {
		right = m.me.Username
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return footerStyle.Render(left + strings.Repeat(" ", gap) + right)
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
