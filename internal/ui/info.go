package ui

import (
	"fmt"
	"path/filepath"
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

// infoMode selects the panel's content. The media view is a drill-down inside
// the same viewport, not a second pane: it reuses the whole target machinery
// below, so selection, scrolling, hover and click resolution work unchanged.
type infoMode int

const (
	infoModeMain infoMode = iota
	infoModeMedia
)

// infoTargetKind distinguishes the activatable things in the panel: a link
// (open it), a pinned message (jump the main pane to it), a member (open a DM
// with them), the row that adds members to the channel, the row that opens the
// media listing, and one attachment within that listing.
type infoTargetKind int

const (
	infoTargetLink infoTargetKind = iota
	infoTargetPin
	infoTargetMember
	infoTargetAddMember
	infoTargetMedia
	infoTargetMediaItem
)

// infoTarget is one focusable item in the panel. Only the field for its kind is
// set. startRow/endRow are the inclusive logical-line range it occupies in the
// rendered content (endRow == startRow for everything but a pinned message and
// a media item), used to scroll the selection into view and to resolve a mouse
// click.
type infoTarget struct {
	kind     infoTargetKind
	url      string // infoTargetLink: the link target
	postID   string // infoTargetPin: the pinned post id
	userID   string // infoTargetMember: the member's user id
	mediaIdx int    // infoTargetMediaItem: index into m.infoMedia
	startRow int
	endRow   int
}

// isRow reports whether the target occupies whole indented lines — everything
// but a link, which is a run of characters inside a line. Rows carry the
// selection bar and the pointer-hover background; links get their own
// link-scoped highlight instead.
func (t infoTarget) isRow() bool { return t.kind != infoTargetLink }

var (
	infoLabelStyle   = lipgloss.NewStyle().Foreground(focusedColor).Bold(true)
	infoMetaKeyStyle = lipgloss.NewStyle().Foreground(dimColor)
	infoDimStyle     = lipgloss.NewStyle().Foreground(dimColor)
	infoErrStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	// infoActionStyle paints an actionable row (the "+ Add members…" affordance)
	// in the accent colour so it reads as a button, not another member.
	infoActionStyle = lipgloss.NewStyle().Foreground(focusedColor)
	// infoSelLinkStyle paints the keyboard-selected link, distinct from the
	// pointer-hover highlight (mdLinkHoverStyle).
	infoSelLinkStyle = mdLinkStyle.Reverse(true)
	// infoHoverRowStyle backgrounds a member row the pointer rests on, matching
	// the link hover's background so the panel's two hover affordances read alike.
	infoHoverRowStyle = lipgloss.NewStyle().Background(panelHoverBg)
)

// openChannelInfo raises the channel-info panel for the open channel. Pressing
// the key again for the same channel closes it (a toggle). The Search / Feed /
// SQL tabs have no open channel, so it's a no-op there.
func (m Model) openChannelInfo() (tea.Model, tea.Cmd) {
	return m, m.raiseChannelInfo()
}

// raiseChannelInfo is the pointer-receiver half of openChannelInfo, so
// command runners can open the panel without copying the ~133KB Model.
func (m *Model) raiseChannelInfo() tea.Cmd {
	if m.onSearchTab() || m.onFeedTab() || m.onSQLTab() {
		return nil
	}
	c := m.findChannel(m.openChannelID)
	if c == nil {
		m.status = "no channel open"
		return nil
	}
	if m.infoOpen && m.infoChannelID == c.Id {
		m.closeInfo()
		return nil
	}
	// The info panel and the thread sidebar / reference panel share the single
	// right slot.
	var threadCmd tea.Cmd
	if m.threadOpen {
		threadCmd = m.closeThread()
	}
	if m.refOpen {
		m.closeRef()
	}
	m.infoOpen = true
	m.infoChannelID = c.Id
	m.infoMode = infoModeMain
	m.infoMainIdx = -1
	m.infoMembers = nil
	m.infoMembersLoaded = false
	m.infoMembersErr = nil
	m.infoPinned = nil
	m.infoPinnedLoaded = false
	m.infoPinnedErr = nil
	m.infoMedia = nil
	m.infoMediaLoaded = false
	m.infoMediaTruncated = false
	m.infoMediaErr = nil
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
	return tea.Batch(threadCmd, m.fetchInfoMembers(c.Id), m.fetchInfoPinned(c.Id), m.fetchInfoMedia(c.Id))
}

// closeInfo tears the panel down and returns focus to the messages pane.
func (m *Model) closeInfo() {
	if !m.infoOpen {
		return
	}
	m.infoOpen = false
	m.infoChannelID = ""
	m.infoMode = infoModeMain
	m.infoMembers = nil
	m.infoPinned = nil
	m.infoMedia = nil
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

// openInfoMedia drills the panel into the media listing, parking the main
// view's selection so esc can restore it.
func (m *Model) openInfoMedia() {
	m.infoMainIdx = m.infoIdx
	m.infoMode = infoModeMedia
	m.infoIdx = -1
	m.infoHoverIdx = -1
	m.infoScrollFree = false
	m.infoView.GotoTop()
	if m.infoMediaErr != nil {
		m.status = "media: " + m.infoMediaErr.Error()
	} else {
		m.status = "media · ↑/↓ select · space preview · o open · s save · esc back"
	}
	m.renderInfo()
}

// closeInfoMedia returns from the media listing to the channel facts, landing
// the cursor back on the row the drill-down was entered from.
func (m *Model) closeInfoMedia() {
	m.infoMode = infoModeMain
	m.infoIdx = m.infoMainIdx
	m.infoHoverIdx = -1
	m.infoScrollFree = false
	m.status = "channel info · ↑/↓ select · ↵ open/jump/DM · esc closes"
	m.renderInfo()
}

// infoMediaLimit caps how many of a channel's posts ChannelFiles scans. A post
// can carry several attachments, so the listing can be longer than this; what
// it bounds is the newest-first walk, keeping the query in the low milliseconds
// even on a channel with tens of thousands of posts.
const infoMediaLimit = 500

// fetchInfoMedia loads the channel's cached attachments for the panel. The
// local store is the only source — Mattermost exposes no channel-files endpoint
// — so this is a disk read, not a request, and works offline.
func (m Model) fetchInfoMedia(channelID string) tea.Cmd {
	// Snapshot the name cache on the UI goroutine; the closure runs on a Bubble
	// Tea worker and must not read m.userNames while Update writes it. Same
	// reasoning as fetchInfoPinned.
	st, client, ctx, known := m.store, m.client, m.ctx, snapshotNames(m.userNames)
	return func() tea.Msg {
		if st == nil {
			return infoMediaLoadedMsg{channelID: channelID}
		}
		files, err := st.ChannelFiles(channelID, infoMediaLimit)
		if err != nil {
			return infoMediaLoadedMsg{channelID: channelID, err: err}
		}
		// Resolve uploaders the name cache can't already name. Distinct posts
		// dedupe to a handful of ids, so this is one request at most.
		posts := map[string]struct{}{}
		need := map[string]struct{}{}
		for _, f := range files {
			posts[f.PostId] = struct{}{}
			if _, have := known[f.CreatorId]; !have && f.CreatorId != "" {
				need[f.CreatorId] = struct{}{}
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
		return infoMediaLoadedMsg{
			channelID: channelID,
			files:     files,
			users:     users,
			truncated: len(posts) >= infoMediaLimit,
		}
	}
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

// renderInfo rebuilds the panel viewport for the current mode, then applies the
// decoration both modes share: the selection bar, the pointer hover, and the
// scroll-into-view geometry. Those work off the target list alone, so a new
// content mode only has to produce (lines, targets).
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

	var lines []string
	var targets []infoTarget
	if m.infoMode == infoModeMedia {
		lines, targets = m.infoMediaContent()
	} else {
		lines, targets = m.infoMainContent(c, width)
	}

	m.clampInfoIdx(len(targets))

	m.decorateInfo(lines, targets, width)
}

// infoMainContent builds the panel's default view: purpose, header, members,
// pinned messages, the media drill-down row, and the channel facts.
func (m *Model) infoMainContent(c *model.Channel, width int) ([]string, []infoTarget) {
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
		// Through the effects-aware renderer: a header can carry \rainbow{…}
		// spans, which renderInfoPane's paint pass then colours (see
		// renderMarkdownEffects — a header without effects takes the plain path).
		addMarkdown(expandTables(renderMarkdownEffects(c.Header, m.emojiImg, nil, self), width))
	}

	// Members — each is a focusable target that opens a DM with that person,
	// closed by an "add members" row on channels that accept new ones.
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
	if canAddMembers(c) {
		start := len(lines)
		lines = append(lines, "  "+infoActionStyle.Render("+ Add members…"))
		targets = append(targets, infoTarget{kind: infoTargetAddMember, startRow: start, endRow: start})
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

	// The media drill-down. Its own section heading would cost two lines to say
	// what the row already says, so it stands alone.
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	mediaStart := len(lines)
	lines = append(lines, "  "+infoActionStyle.Render("📎 "+infoCountLabel("All media", len(m.infoMedia), m.infoMediaLoaded)+" ›"))
	targets = append(targets, infoTarget{kind: infoTargetMedia, startRow: mediaStart, endRow: mediaStart})

	// Channel facts.
	section("Channel")
	muted := "no"
	if m.channelMuted(c.Id) {
		muted = "yes"
	}
	lines = append(lines, infoMetaLine("Type", channelTypeLabel(c)))
	lines = append(lines, infoMetaLine("Muted", muted))
	lines = append(lines, infoMetaLine("ID", c.Id))

	return lines, targets
}

// infoMediaContent builds the media drill-down: every attachment cached for the
// channel, newest first, as a two-line row (name, then uploader · time · size).
// Width isn't needed — rows are plain text the viewport wraps.
func (m *Model) infoMediaContent() ([]string, []infoTarget) {
	var lines []string
	var targets []infoTarget

	lines = append(lines, infoLabelStyle.Render(infoCountLabel("Media", len(m.infoMedia), m.infoMediaLoaded)))
	switch {
	case m.infoMediaErr != nil:
		lines = append(lines, "  "+infoErrStyle.Render(m.infoMediaErr.Error()))
	case !m.infoMediaLoaded:
		lines = append(lines, "  "+infoDimStyle.Render("loading…"))
	case len(m.infoMedia) == 0:
		lines = append(lines, "  "+infoDimStyle.Render("none"))
	default:
		for i, f := range m.infoMedia {
			start := len(lines)
			lines = append(lines, "  "+mediaIcon(f)+" "+normalizeFilename(f.Name))
			meta := m.fileAuthorName(f) + " · " + formatPostTime(f.CreateAt) + " · " + humanSize(f.Size)
			lines = append(lines, "     "+infoDimStyle.Render(meta))
			targets = append(targets, infoTarget{kind: infoTargetMediaItem, mediaIdx: i, postID: f.PostId, startRow: start, endRow: len(lines) - 1})
		}
		if m.infoMediaTruncated {
			lines = append(lines, "")
			lines = append(lines, "  "+infoDimStyle.Render(fmt.Sprintf("newest %d messages only", infoMediaLimit)))
		}
	}
	lines = append(lines, "")
	lines = append(lines, "  "+infoDimStyle.Render("esc back"))
	return lines, targets
}

// decorateInfo applies what both content modes share: the pointer-hover
// background, the selection bar, the selected-link highlight, and the
// scroll-into-view geometry. lines is mutated in place.
func (m *Model) decorateInfo(lines []string, targets []infoTarget, width int) {
	selIdx := -1
	if m.focus == focusInfo && m.infoIdx >= 0 && m.infoIdx < len(targets) {
		selIdx = m.infoIdx
	}
	// Hover highlight on the actionable row under the pointer, unless it's the
	// row already carrying the selection bar. A pinned message is excluded: it
	// spans a rendered markdown body that already carries its own styling.
	if m.infoHoverIdx >= 0 && m.infoHoverIdx < len(targets) && m.infoHoverIdx != selIdx {
		switch t := targets[m.infoHoverIdx]; t.kind {
		case infoTargetMember, infoTargetAddMember, infoTargetMedia, infoTargetMediaItem:
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
		if t.isRow() {
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

// mediaIcon picks a glyph for an attachment, preferring its MIME type and
// falling back to the file extension. The fallback isn't belt-and-braces: the
// server leaves mime_type empty for a fair slice of uploads (videos and
// archives especially), which would otherwise all read as a plain paperclip.
func mediaIcon(f *model.FileInfo) string {
	mime, _, _ := strings.Cut(f.MimeType, ";")
	kind, sub, _ := strings.Cut(strings.TrimSpace(mime), "/")
	switch kind {
	case "image":
		return "🖼"
	case "video":
		return "🎬"
	case "audio":
		return "🎵"
	case "text":
		return "📄"
	}
	switch sub {
	case "pdf":
		return "📄"
	case "zip", "gzip", "x-tar", "x-7z-compressed", "vnd.rar":
		return "📦"
	}
	return iconForExt(f)
}

// iconForExt is mediaIcon's fallback: the extension the server recorded, or the
// one on the filename when even that is missing.
func iconForExt(f *model.FileInfo) string {
	ext := strings.ToLower(strings.TrimPrefix(f.Extension, "."))
	if ext == "" {
		_, ext, _ = strings.Cut(strings.ToLower(filepath.Ext(f.Name)), ".")
	}
	switch ext {
	case "png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "tiff", "heic", "heif", "avif":
		return "🖼"
	case "mp4", "mov", "mkv", "webm", "avi", "m4v", "wmv", "flv", "mpg", "mpeg":
		return "🎬"
	case "mp3", "wav", "ogg", "oga", "m4a", "flac", "aac", "opus":
		return "🎵"
	case "pdf", "txt", "md", "csv", "tsv", "log", "json", "yaml", "yml", "xml", "html",
		"patch", "diff", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods":
		return "📄"
	case "zip", "tar", "gz", "tgz", "bz2", "7z", "rar", "xz", "zst":
		return "📦"
	}
	return "📎"
}

// fileAuthorName names an attachment's uploader, mirroring postAuthorName's
// fallback to a truncated id when the name cache can't resolve them.
func (m *Model) fileAuthorName(f *model.FileInfo) string {
	name := m.userNames[f.CreatorId]
	if name == "" {
		name = f.CreatorId
		if len(name) > 8 {
			name = name[:8]
		}
	}
	return name
}

// infoSelectedFile returns the attachment the media view's cursor is on, or nil
// when the selection isn't a media row.
func (m *Model) infoSelectedFile() *model.FileInfo {
	t := m.infoCurrentTarget()
	if t == nil || t.kind != infoTargetMediaItem || t.mediaIdx >= len(m.infoMedia) {
		return nil
	}
	return m.infoMedia[t.mediaIdx]
}

// activateInfoTarget acts on the selected item: open a link or an attachment,
// jump the main pane to a pinned message, DM a member, or drill into the media
// listing.
func (m Model) activateInfoTarget() (tea.Model, tea.Cmd) {
	t := m.infoCurrentTarget()
	if t == nil {
		if m.infoMode == infoModeMedia {
			m.status = "nothing selected — ↑/↓ to pick an attachment"
		} else {
			m.status = "nothing selected — ↑/↓ to pick a link or pinned message"
		}
		return m, nil
	}
	switch t.kind {
	case infoTargetLink:
		return m, m.openTarget(openable{name: t.url, url: t.url})
	case infoTargetPin:
		return m.jumpToInfoPin(t.postID)
	case infoTargetMember:
		return m.openDMWithMember(t.userID)
	case infoTargetAddMember:
		return m.openAddMembersPrompt()
	case infoTargetMedia:
		m.openInfoMedia()
		return m, nil
	case infoTargetMediaItem:
		f := m.infoSelectedFile()
		if f == nil {
			return m, nil
		}
		m.status = "opening " + normalizeFilename(f.Name) + "…"
		return m, m.openTarget(openable{name: f.Name, file: f})
	}
	return m, nil
}

// downloadInfoMedia saves just the selected attachment, unlike `s` on a message
// (downloadFromPost), which saves every file that message carries.
func (m Model) downloadInfoMedia() (tea.Model, tea.Cmd) {
	f := m.infoSelectedFile()
	if f == nil {
		m.status = "no attachment selected"
		return m, nil
	}
	m.status = "downloading " + normalizeFilename(f.Name) + "…"
	return m, m.downloadFiles([]*model.FileInfo{f})
}

// previewInfoMedia opens the image preview on the selected attachment, with the
// whole listing as the gallery — so ←/→ walk every previewable file in the
// channel, not just the ones sharing a message.
func (m Model) previewInfoMedia() (tea.Model, tea.Cmd) {
	f := m.infoSelectedFile()
	if f == nil {
		m.status = "no attachment selected"
		return m, nil
	}
	if !m.filePreviewable(f) {
		m.status = "no preview for " + normalizeFilename(f.Name) + " — press o to open"
		return m, nil
	}
	var items []previewItem
	start := 0
	for _, cand := range m.infoMedia {
		if !m.filePreviewable(cand) {
			continue
		}
		if cand.Id == f.Id {
			start = len(items)
		}
		items = append(items, previewItem{file: cand, name: cand.Name})
	}
	return m.openPreviewItems(items, start)
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

// infoHoverAt resolves the pointer to the actionable row under it in the panel,
// or -1 over anything else (links carry their own OSC 8 hover via hoverLinkAt).
// The set of hoverable kinds matches decorateInfo's.
func (m *Model) infoHoverAt(x, y int) int {
	if !m.infoOpen {
		return -1
	}
	h := m.hitInfoContent(x, y)
	if h.zone != hitInfo {
		return -1
	}
	for i, t := range m.infoTargets {
		switch t.kind {
		case infoTargetMember, infoTargetAddMember, infoTargetMedia, infoTargetMediaItem:
		default:
			continue
		}
		if h.line >= t.startRow && h.line <= t.endRow {
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
		case infoTargetAddMember:
			return m.openAddMembersPrompt()
		}
		// A link line clicked off the link text: just focus it.
		m.infoScrollFree = false
		m.renderInfo()
		return m, nil
	}
	return m, nil
}

// handleInfoKey owns every keystroke while the panel has focus: esc backs out
// of the media drill-down (or closes the panel), the channel-info key closes it
// outright, ↑/↓ move between focusable targets, ↵/o activate the selected one,
// and — on a media row — space previews and s saves it. Anything else scrolls
// the viewport, which is why the media keys only fire on a media row: space
// still has to page the panel in the main view.
func (m Model) handleInfoKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.infoMode == infoModeMedia {
			m.closeInfoMedia()
			return m, nil
		}
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
	case key.Matches(msg, m.keys.Preview):
		if m.infoSelectedFile() != nil {
			return m.previewInfoMedia()
		}
	case key.Matches(msg, m.keys.Download):
		if m.infoSelectedFile() != nil {
			return m.downloadInfoMedia()
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

	// Effects in the header section are painted on the bare viewport rows,
	// before the box adds its border (chrome 0) — mirroring the thread pane.
	content := lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render(m.infoPaneTitle(width)), m.paintEffects(m.infoView.View(), 0))

	borderColor := dimColor
	if m.focus == focusInfo {
		borderColor = focusedColor
	}
	style := lipgloss.NewStyle().Border(border).UnsetBorderTop().UnsetBorderRight().
		Width(width - 1).Height(innerH).BorderForeground(borderColor)
	box := style.Render(content)

	rightBorder := renderRightBorder(innerH, 1, m.infoView.Height(), total, pct, borderColor, showScrollbar, -1)
	return lipgloss.JoinHorizontal(lipgloss.Top, box, rightBorder)
}

// infoPaneTitle is the pane heading: the channel's label, truncated to width.
func (m *Model) infoPaneTitle(width int) string {
	head := "Info"
	if m.infoMode == infoModeMedia {
		head = "Media"
	}
	title := head
	if c := m.findChannel(m.infoChannelID); c != nil {
		title = head + " · " + m.channelLabel(c)
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
