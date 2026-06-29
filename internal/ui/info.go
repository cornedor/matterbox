package ui

import (
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// The channel-info side panel. Press the channel-info key (I by default) on a
// channel or DM tab to open a read-only pane describing the open channel: its
// purpose, header, member list, pinned messages, mute state and id. It mirrors
// the reference panel's layout (a right-side pane that splits the messages
// area) and shares the single right slot, so opening it closes the thread
// sidebar / reference panel and vice-versa.
//
// The pane is more than a static document: the links in the purpose/header and
// each pinned message are focusable targets that ↑/↓ cycle and ↵/o activate —
// a link opens (gated through the same scheme warning as a clicked link), a
// pinned message jumps the main pane to it (loading a window around it from the
// cache when it's outside the loaded range). Links are also clickable with the
// mouse, exactly like the messages/thread/reference panes.

// infoTargetKind distinguishes the activatable things in the panel: a link
// (open it), a pinned message (jump the main pane to it), and a member (open a
// DM with them).
type infoTargetKind int

const (
	infoTargetLink infoTargetKind = iota
	infoTargetPin
	infoTargetMember
)

// infoTarget is one focusable item in the panel. Only the field for its kind is
// set. startRow/endRow are the inclusive logical-line range it occupies in the
// rendered content (endRow == startRow for a link/member), used to scroll the
// selection into view and to resolve a mouse click.
type infoTarget struct {
	kind     infoTargetKind
	url      string // infoTargetLink: the link target
	postID   string // infoTargetPin: the pinned post id
	userID   string // infoTargetMember: the member's user id
	startRow int
	endRow   int
}

var (
	infoLabelStyle   = lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
	infoMetaKeyStyle = lipgloss.NewStyle().Foreground(dimColor)
	infoDimStyle     = lipgloss.NewStyle().Foreground(dimColor)
	infoErrStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	// infoSelLinkStyle paints the keyboard-selected link, distinct from the
	// pointer-hover highlight (mdLinkHoverStyle).
	infoSelLinkStyle = mdLinkStyle.Reverse(true)
	// infoHoverRowStyle backgrounds a member row the pointer rests on, matching
	// the link hover's background so the panel's two hover affordances read alike.
	infoHoverRowStyle = lipgloss.NewStyle().Background(lipgloss.Color("238"))
)

// openChannelInfo raises the channel-info panel for the open channel. Pressing
// the key again for the same channel closes it (a toggle). The Search / Feed /
// SQL tabs have no open channel, so it's a no-op there.
func (m Model) openChannelInfo() (tea.Model, tea.Cmd) {
	if m.onSearchTab() || m.onFeedTab() || m.onSQLTab() {
		return m, nil
	}
	c := m.findChannel(m.openChannelID)
	if c == nil {
		m.status = "no channel open"
		return m, nil
	}
	if m.infoOpen && m.infoChannelID == c.Id {
		m.closeInfo()
		return m, nil
	}
	// The info panel and the thread sidebar / reference panel share the single
	// right slot.
	if m.threadOpen {
		m.closeThread()
	}
	if m.refOpen {
		m.closeRef()
	}
	m.infoOpen = true
	m.infoChannelID = c.Id
	m.infoMembers = nil
	m.infoMembersLoaded = false
	m.infoMembersErr = nil
	m.infoPinned = nil
	m.infoPinnedLoaded = false
	m.infoPinnedErr = nil
	m.infoTargets = nil
	m.infoIdx = -1
	m.infoHoverIdx = -1
	m.infoScrollFree = false
	m.focus = focusInfo
	m.input.Blur()
	m.infoView.GotoTop()
	m.status = "channel info · ↑/↓ select · ↵ open/jump/DM · esc closes"
	m.resizeMessagesViewport()
	m.renderMessages()
	m.renderInfo()
	return m, tea.Batch(m.fetchInfoMembers(c.Id), m.fetchInfoPinned(c.Id))
}

// closeInfo tears the panel down and returns focus to the messages pane.
func (m *Model) closeInfo() {
	if !m.infoOpen {
		return
	}
	m.infoOpen = false
	m.infoChannelID = ""
	m.infoMembers = nil
	m.infoPinned = nil
	m.infoTargets = nil
	m.infoIdx = -1
	m.infoHoverIdx = -1
	m.infoScrollFree = false
	if m.focus == focusInfo {
		m.focus = focusMessages
	}
	m.resizeMessagesViewport()
	m.renderMessages()
}

// fetchInfoMembers loads the channel's member profiles for the panel. A failure
// is carried back on the message (shown in the panel) rather than the global
// status line.
func (m Model) fetchInfoMembers(channelID string) tea.Cmd {
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		us, err := client.ChannelUsers(ctx, channelID)
		return infoMembersLoadedMsg{channelID: channelID, members: us, err: err}
	}
}

// fetchInfoPinned loads the channel's pinned posts (newest first) for the
// panel, resolving any sender names the UI can't already name.
func (m Model) fetchInfoPinned(channelID string) tea.Cmd {
	// Snapshot the name cache on the UI goroutine; the closure below runs on a
	// Bubble Tea worker and must not read the live m.userNames while Update
	// writes it (issue #2).
	client, ctx, known := m.client, m.ctx, snapshotNames(m.userNames)
	return func() tea.Msg {
		pl, err := client.PinnedPosts(ctx, channelID)
		if err != nil {
			return infoPinnedLoadedMsg{channelID: channelID, err: err}
		}
		posts := orderedPinned(pl)
		need := map[string]struct{}{}
		for _, p := range posts {
			if _, have := known[p.UserId]; !have {
				need[p.UserId] = struct{}{}
			}
		}
		users := map[string]string{}
		if len(need) > 0 {
			ids := make([]string, 0, len(need))
			for id := range need {
				ids = append(ids, id)
			}
			if us, err := client.UsersByIDs(ctx, ids); err == nil {
				for _, u := range us {
					users[u.Id] = u.Username
				}
			}
		}
		return infoPinnedLoadedMsg{channelID: channelID, posts: posts, users: users}
	}
}

// orderedPinned flattens a pinned-posts PostList to a slice ordered newest
// first, so the most recently pinned message reads at the top of the section.
func orderedPinned(pl *model.PostList) []*model.Post {
	if pl == nil {
		return nil
	}
	posts := make([]*model.Post, 0, len(pl.Posts))
	for _, p := range pl.Posts {
		if p != nil {
			posts = append(posts, p)
		}
	}
	sort.SliceStable(posts, func(i, j int) bool { return posts[i].CreateAt > posts[j].CreateAt })
	return posts
}

// renderInfo rebuilds the panel viewport from the open channel's metadata,
// recording the focusable targets (purpose/header links + pinned messages) and
// keeping the selected one in view.
func (m *Model) renderInfo() {
	if !m.infoOpen {
		return
	}
	// New content generation: invalidates the info scroll-geometry cache.
	m.infoContentVer++
	width := m.infoView.Width()
	c := m.findChannel(m.infoChannelID)
	if c == nil {
		m.infoView.SetContent(infoDimStyle.Render("channel not found"))
		m.infoTargets = nil
		return
	}

	self := ""
	if m.me != nil {
		self = m.me.Username
	}
	var lines []string
	var targets []infoTarget

	section := func(label string) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, infoLabelStyle.Render(label))
	}
	// addMarkdown appends already-styled markdown lines, registering each link
	// it carries (OSC 8 open marker) as a focusable target at its line.
	addMarkdown := func(text string) {
		for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			for _, url := range osc8OpensInLine(l) {
				targets = append(targets, infoTarget{kind: infoTargetLink, url: url, startRow: len(lines), endRow: len(lines)})
			}
			lines = append(lines, l)
		}
	}

	if strings.TrimSpace(c.Purpose) != "" {
		section("Purpose")
		addMarkdown(expandTables(renderMarkdown(c.Purpose, m.emojiImg, nil, self), width))
	}
	if strings.TrimSpace(c.Header) != "" {
		section("Header")
		addMarkdown(expandTables(renderMarkdown(c.Header, m.emojiImg, nil, self), width))
	}

	// Members — each is a focusable target that opens a DM with that person.
	section(infoCountLabel("Members", len(m.infoMembers), m.infoMembersLoaded))
	switch {
	case m.infoMembersErr != nil:
		lines = append(lines, "  "+infoErrStyle.Render(m.infoMembersErr.Error()))
	case !m.infoMembersLoaded:
		lines = append(lines, "  "+infoDimStyle.Render("loading…"))
	case len(m.infoMembers) == 0:
		lines = append(lines, "  "+infoDimStyle.Render("none"))
	default:
		for _, mem := range m.sortedMembers() {
			start := len(lines)
			lines = append(lines, "  "+mem.label)
			targets = append(targets, infoTarget{kind: infoTargetMember, userID: mem.id, startRow: start, endRow: start})
		}
	}

	// Pinned messages.
	section(infoCountLabel("Pinned", len(m.infoPinned), m.infoPinnedLoaded))
	switch {
	case m.infoPinnedErr != nil:
		lines = append(lines, "  "+infoErrStyle.Render(m.infoPinnedErr.Error()))
	case !m.infoPinnedLoaded:
		lines = append(lines, "  "+infoDimStyle.Render("loading…"))
	case len(m.infoPinned) == 0:
		lines = append(lines, "  "+infoDimStyle.Render("none"))
	default:
		for _, p := range m.infoPinned {
			start := len(lines)
			name := m.postAuthorName(p)
			ts := formatPostTime(p.CreateAt)
			lines = append(lines, "  "+userStyle.Render(name)+"  "+timeStyle.Render(ts))
			if body := m.markdownBodyRaw(p); strings.TrimSpace(body) != "" {
				lines = append(lines, strings.Split(strings.TrimRight(body, "\n"), "\n")...)
			}
			targets = append(targets, infoTarget{kind: infoTargetPin, postID: p.Id, startRow: start, endRow: len(lines) - 1})
		}
	}

	// Channel facts.
	section("Channel")
	muted := "no"
	if m.channelMuted(c.Id) {
		muted = "yes"
	}
	lines = append(lines, infoMetaLine("Type", channelTypeLabel(c)))
	lines = append(lines, infoMetaLine("Muted", muted))
	lines = append(lines, infoMetaLine("ID", c.Id))

	m.clampInfoIdx(len(targets))

	selIdx := -1
	if m.focus == focusInfo && m.infoIdx >= 0 && m.infoIdx < len(targets) {
		selIdx = m.infoIdx
	}
	// Hover highlight on the member row under the pointer, unless it's the row
	// already carrying the selection bar.
	if m.infoHoverIdx >= 0 && m.infoHoverIdx < len(targets) && m.infoHoverIdx != selIdx {
		if t := targets[m.infoHoverIdx]; t.kind == infoTargetMember {
			for r := t.startRow; r <= t.endRow && r < len(lines); r++ {
				lines[r] = infoHoverRowStyle.Render(lines[r])
			}
		}
	}

	// Selection decoration + scroll-into-view geometry.
	selVisStart, selVisRows := -1, 0
	if selIdx >= 0 {
		t := targets[selIdx]
		selVisStart = visualOffsetOf(lines, t.startRow, width)
		if t.kind == infoTargetPin || t.kind == infoTargetMember {
			bar := selectedBarStyle.Render("▎")
			for r := t.startRow; r <= t.endRow && r < len(lines); r++ {
				if strings.HasPrefix(lines[r], "  ") {
					lines[r] = bar + " " + lines[r][2:]
				} else {
					lines[r] = bar + " " + lines[r]
				}
			}
			selVisRows = visualOffsetOf(lines, t.endRow+1, width) - selVisStart
		} else {
			selVisRows = visualHeight(lines[t.startRow], width)
		}
	}

	content := strings.Join(lines, "\n")
	if m.focus == focusInfo && m.infoIdx >= 0 && m.infoIdx < len(targets) && targets[m.infoIdx].kind == infoTargetLink {
		content = highlightLink(content, targets[m.infoIdx].url, infoSelLinkStyle)
	}
	content = m.infoHover(content)
	m.infoTargets = targets
	m.infoView.SetContent(content)

	if h := m.infoView.Height(); h > 0 && selVisStart >= 0 {
		visEnd := selVisStart + selVisRows
		off := m.infoView.YOffset()
		switch {
		case m.infoScrollFree:
			off = m.infoFreeOffset
		case selVisStart < off:
			off = selVisStart
		case visEnd > off+h:
			off = visEnd - h
		}
		if total := visualOffsetOf(lines, len(lines), width); off > total-h {
			off = total - h
		}
		if off < 0 {
			off = 0
		}
		m.infoView.SetYOffset(off)
	}
}

// infoMember is one sorted member row: the user id (to open a DM) and the
// rendered "@name" label.
type infoMember struct {
	id    string
	label string
}

// sortedMembers returns the channel's members as "@name" rows, marking the
// logged-in user, with self first and the rest alphabetical.
func (m *Model) sortedMembers() []infoMember {
	selfID := ""
	if m.me != nil {
		selfID = m.me.Id
	}
	out := make([]infoMember, 0, len(m.infoMembers))
	for _, u := range m.infoMembers {
		if u == nil {
			continue
		}
		label := "@" + u.Username
		if u.Id == selfID {
			label += " (you)"
		}
		out = append(out, infoMember{id: u.Id, label: label})
	}
	sort.SliceStable(out, func(i, j int) bool {
		yi := out[i].id == selfID
		yj := out[j].id == selfID
		if yi != yj {
			return yi
		}
		return strings.ToLower(out[i].label) < strings.ToLower(out[j].label)
	})
	return out
}

// openDMWithMember closes the panel and opens (creating if needed) the DM with
// the given user, reusing the group-DM resolve path so a freshly-created DM is
// inserted into the sidebar before the jump. Selecting yourself is a no-op.
func (m Model) openDMWithMember(userID string) (tea.Model, tea.Cmd) {
	if m.me == nil {
		m.status = "user not loaded yet"
		return m, nil
	}
	if userID == m.me.Id {
		m.status = "that's you"
		return m, nil
	}
	m.closeInfo()
	m.status = "opening DM…"
	client, ctx, meID := m.client, m.ctx, m.me.Id
	return m, func() tea.Msg {
		ch, err := client.DirectChannel(ctx, meID, userID)
		return groupDMResolvedMsg{ch: ch, err: err}
	}
}

// infoCountLabel builds a section heading with a count once the data is loaded
// ("Pinned (3)"), or a bare heading while it's still loading.
func infoCountLabel(label string, n int, loaded bool) string {
	if !loaded {
		return label
	}
	return label + " (" + strconv.Itoa(n) + ")"
}

// infoMetaLine renders one aligned "key: value" fact row with a dim key.
func infoMetaLine(key, value string) string {
	return "  " + infoMetaKeyStyle.Render(key+":") + " " + value
}

// channelTypeLabel names a channel's kind for the info panel.
func channelTypeLabel(c *model.Channel) string {
	switch c.Type {
	case model.ChannelTypeDirect:
		return "direct message"
	case model.ChannelTypeGroup:
		return "group message"
	case model.ChannelTypePrivate:
		return "private channel"
	default:
		return "public channel"
	}
}

// infoHover paints the hovered link's background when the pointer rests on a
// link in this panel, mirroring the reference pane's hover highlight.
func (m *Model) infoHover(content string) string {
	if m.hoverLink.pane == focusInfo && m.hoverLink.url != "" {
		return highlightLink(content, m.hoverLink.url, mdLinkHoverStyle)
	}
	return content
}

// infoCurrentTarget returns the selected focusable item, or nil.
func (m *Model) infoCurrentTarget() *infoTarget {
	if m.infoIdx < 0 || m.infoIdx >= len(m.infoTargets) {
		return nil
	}
	return &m.infoTargets[m.infoIdx]
}

// clampInfoIdx keeps the selection index valid for a target list of length n,
// dropping to -1 (no selection) when there are no targets.
func (m *Model) clampInfoIdx(n int) {
	if n == 0 {
		m.infoIdx = -1
		return
	}
	if m.infoIdx >= n {
		m.infoIdx = n - 1
	}
	if m.infoIdx < 0 {
		m.infoIdx = -1
	}
}

// moveInfoTarget advances the selection by delta among the focusable targets,
// re-anchoring the view on it. From no selection it lands on the first/last.
func (m *Model) moveInfoTarget(delta int) {
	n := len(m.infoTargets)
	if n == 0 {
		return
	}
	switch {
	case m.infoIdx < 0 && delta > 0:
		m.infoIdx = 0
	case m.infoIdx < 0:
		m.infoIdx = n - 1
	default:
		m.infoIdx += delta
		if m.infoIdx < 0 {
			m.infoIdx = 0
		}
		if m.infoIdx >= n {
			m.infoIdx = n - 1
		}
	}
	m.infoScrollFree = false
	m.renderInfo()
}

// activateInfoTarget acts on the selected item: open a link, or jump the main
// pane to a pinned message.
func (m Model) activateInfoTarget() (tea.Model, tea.Cmd) {
	t := m.infoCurrentTarget()
	if t == nil {
		m.status = "nothing selected — ↑/↓ to pick a link or pinned message"
		return m, nil
	}
	switch t.kind {
	case infoTargetLink:
		return m, m.openTarget(openable{name: t.url, url: t.url})
	case infoTargetPin:
		return m.jumpToInfoPin(t.postID)
	case infoTargetMember:
		return m.openDMWithMember(t.userID)
	}
	return m, nil
}

// jumpToInfoPin closes the panel and jumps the main pane to the pinned post.
func (m Model) jumpToInfoPin(postID string) (tea.Model, tea.Cmd) {
	channelID := m.infoChannelID
	m.closeInfo()
	return m.jumpToChannelPost(channelID, postID)
}

// jumpToChannelPost positions the messages pane on postID within channelID
// (the open channel). If the post is already in the loaded window it's just
// selected; otherwise a context window is pulled from the cache (PostsAround),
// falling back to a channel reload with a pending jump when the cache can't
// satisfy it.
func (m Model) jumpToChannelPost(channelID, postID string) (tea.Model, tea.Cmd) {
	if channelID == m.openChannelID {
		for i, p := range m.posts {
			if p.Id == postID {
				m.selectPostAt(i)
				m.renderMessages()
				return m, nil
			}
		}
	}
	if m.store != nil {
		if around, err := m.store.PostsAround(channelID, postID, 30, 30); err == nil && len(around) > 0 {
			// This cache branch swaps the visible posts for channelID, which may
			// differ from the currently-open channel. When it does, route the
			// switch through enterChannel so openChannelID (and therefore replies)
			// follows the posts instead of staying pinned to the previous channel.
			// A same-channel jump needs none of that bookkeeping.
			var draftCmd tea.Cmd
			if channelID != m.openChannelID {
				draftCmd = m.enterChannel(channelID)
			}
			m.posts = around
			m.postIdx = len(around) - 1
			for i, p := range around {
				if p.Id == postID {
					m.postIdx = i
					break
				}
			}
			m.focus = focusMessages
			m.input.Blur()
			m.msgScrollFree = false
			m.pendingJumpPostID = ""
			m.loading = false
			m.renderMessages()
			var gapCmd tea.Cmd
			if gapID, _ := m.store.LatestPostID(channelID); gapID != "" {
				gapCmd = m.fetchPostsAfter(channelID, gapID)
			}
			return m, tea.Batch(draftCmd, gapCmd)
		}
	}
	// Fallback: reload the channel and let jumpToPendingPost position it if the
	// loaded page happens to include the post.
	m.pendingJumpPostID = postID
	return m, m.openChannelLoadCmd(channelID)
}

// hitInfoContent maps a cell inside the channel-info panel's viewport to the
// content coordinate under it, so a click / hover can resolve the link or
// pinned message there. Mirrors hitRefContent. Returns hitNone over the empty
// rows below the content or before the panel has geometry.
func (m *Model) hitInfoContent(x, y int) hit {
	x0, top, width, height, yoff := m.infoGeom()
	if width <= 0 || height <= 0 {
		return hit{zone: hitNone}
	}
	row := y - top
	if row < 0 || row >= height {
		return hit{zone: hitNone}
	}
	vrow := yoff + row
	_, starts := m.ensureWrapIndex(focusInfo, width)
	total := 0
	if len(starts) > 0 {
		total = starts[len(starts)-1]
	}
	if vrow < 0 || vrow >= total {
		return hit{zone: hitNone}
	}
	line, col := m.contentCoord(focusInfo, x, x0, width, vrow)
	return hit{zone: hitInfo, line: line, col: col}
}

// infoHoverAt resolves the pointer to the member row under it in the panel, or
// -1 over anything else (links carry their own OSC 8 hover via hoverLinkAt).
func (m *Model) infoHoverAt(x, y int) int {
	if !m.infoOpen {
		return -1
	}
	h := m.hitInfoContent(x, y)
	if h.zone != hitInfo {
		return -1
	}
	for i, t := range m.infoTargets {
		if t.kind == infoTargetMember && h.line >= t.startRow && h.line <= t.endRow {
			return i
		}
	}
	return -1
}

// setInfoHover installs the hovered member row, re-rendering the panel only
// when it changes so a move within one row stays a cache-hit re-render.
func (m *Model) setInfoHover(idx int) {
	if m.infoHoverIdx == idx {
		return
	}
	m.infoHoverIdx = idx
	m.renderInfo()
}

// clickInfoTarget resolves a click that didn't land on a link to the focusable
// target whose line range covers it: a pinned message jumps the main pane to it
// (and selects it); a plain link line just selects the link.
func (m Model) clickInfoTarget(line int) (tea.Model, tea.Cmd) {
	for i, t := range m.infoTargets {
		if line < t.startRow || line > t.endRow {
			continue
		}
		m.infoIdx = i
		switch t.kind {
		case infoTargetPin:
			return m.jumpToInfoPin(t.postID)
		case infoTargetMember:
			return m.openDMWithMember(t.userID)
		}
		// A link line clicked off the link text: just focus it.
		m.infoScrollFree = false
		m.renderInfo()
		return m, nil
	}
	return m, nil
}

// handleInfoKey owns every keystroke while the panel has focus: esc / the
// channel-info key close it, ↑/↓ move between focusable targets, ↵/o activate
// the selected one, and anything else scrolls the viewport.
func (m Model) handleInfoKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closeInfo()
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.ChannelInfo): // same key that opened it closes it
		m.closeInfo()
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if len(m.infoTargets) > 0 {
			m.moveInfoTarget(1)
			return m, nil
		}
	case key.Matches(msg, m.keys.Up):
		if len(m.infoTargets) > 0 {
			m.moveInfoTarget(-1)
			return m, nil
		}
	case key.Matches(msg, m.keys.OpenAttach), key.Matches(msg, m.keys.OpenChannel):
		return m.activateInfoTarget()
	}
	var cmd tea.Cmd
	m.infoView, cmd = m.infoView.Update(msg)
	m.infoScrollFree = true
	m.infoFreeOffset = m.infoView.YOffset()
	return m, cmd
}

// renderInfoPane draws the bordered side pane: a title row + the scrollable
// info viewport, with the shared right-border/scrollbar treatment. Mirrors
// renderRefPane.
func (m *Model) renderInfoPane(height, width int) string {
	innerH := height
	if innerH < 1 {
		innerH = 1
	}
	if width < threadPaneMinWidth {
		width = threadPaneMinWidth
	}

	total, pct := m.infoScrollGeom()
	showScrollbar := total > m.infoView.Height() && pct < 1.0

	content := lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render(m.infoPaneTitle(width)), m.infoView.View())

	borderColor := dimColor
	if m.focus == focusInfo {
		borderColor = focusedColor
	}
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().
		Width(width - 1).Height(innerH).BorderForeground(borderColor)
	box := style.Render(content)

	rightBorder := renderRightBorder(innerH, 1, m.infoView.Height(), total, pct, borderColor, showScrollbar)
	return lipgloss.JoinHorizontal(lipgloss.Top, box, rightBorder)
}

// infoPaneTitle is the pane heading: the channel's label, truncated to width.
func (m *Model) infoPaneTitle(width int) string {
	title := "Info"
	if c := m.findChannel(m.infoChannelID); c != nil {
		title = "Info · " + m.channelLabel(c)
	}
	if width > 2 {
		title = truncate(title, width-2)
	}
	return title
}

// osc8OpensInLine returns the URLs of the OSC 8 hyperlink open markers in one
// rendered line, in order. The close marker (empty URL) is skipped. A link that
// soft-wraps carries its open marker only on its first row, so scanning per
// logical line yields each link once at its starting line.
func osc8OpensInLine(line string) []string {
	const open = "\x1b]8;;"
	var urls []string
	rest := line
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return urls
		}
		rest = rest[i+len(open):]
		end, adv := osc8URLEnd(rest)
		if url := rest[:end]; url != "" {
			urls = append(urls, url)
		}
		rest = rest[adv:]
	}
}

// visualOffsetOf returns the cumulative soft-wrapped row count of lines[:upto]
// at the given width — the visual row the upto'th logical line begins on.
func visualOffsetOf(lines []string, upto, width int) int {
	if upto > len(lines) {
		upto = len(lines)
	}
	acc := 0
	for i := 0; i < upto; i++ {
		acc += visualHeight(lines[i], width)
	}
	return acc
}
