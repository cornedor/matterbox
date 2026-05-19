package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

// inputPromptFunc returns a PromptFunc for the input textarea that only
// renders the given prompt string on the first visual line and pads
// continuation lines with two spaces, keeping multi-line content
// visually aligned. promptWidth (passed to SetPromptFunc) must equal
// the rune width of the prompt — currently always "> " or "↳ ", both 2.
func inputPromptFunc(prompt string) func(textarea.PromptInfo) string {
	return func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return prompt
		}
		return "  "
	}
}

type focus int

const (
	focusChannels focus = iota
	focusMessages
	focusThread
	focusInput
	focusAttachments
	focusTeams
)

const numFocus = 6

// dmTeamID is a synthetic team identifier used to bucket DMs / group-DMs,
// which carry an empty Channel.TeamId on the server.
const dmTeamID = "__dm__"

// unreadTeamID is a synthetic team identifier for the virtual "Unread"
// tab. No channels are stored under this key; visibleChannels computes
// the list on demand from m.unread / m.mentions across every bucket.
const unreadTeamID = "__unread__"

type tabKind int

const (
	tabTeam tabKind = iota
	tabDM
	tabUnread
)

type Model struct {
	client *mm.Client
	ctx    context.Context

	me              *model.User
	teams           []*model.Team
	teamsLoaded     bool
	hasDMs          bool
	channels        map[string][]*model.Channel // teamID → channels (DMs under dmTeamID)
	channelsLoaded  bool
	members         model.ChannelMembersWithTeamData
	membersLoaded   bool
	userNames       map[string]string // userID → username

	teamIdx    int
	channelIdx int
	chanOff    int

	posts   []*model.Post
	postIdx int // index of the selected post in m.posts

	// channel filter
	filter      textinput.Model
	filterMode  bool
	filterValue string // committed/live filter applied to channel list

	// global channel switcher (ctrl+k). When switcherMode is true, the
	// switcher owns every keystroke and an overlay popup is rendered in
	// place of the main body.
	switcher     textinput.Model
	switcherMode bool
	switcherIdx  int

	// Persisted per-channel usage counters (loaded from
	// ~/.config/matterbox/channel_stats.json). Used as a sort signal in
	// the switcher so frequently-opened channels float to the top.
	openStats map[string]channelStat

	// lastActiveTeamID / lastActiveChannelID are the last team and
	// channel recorded when the user explicitly opened a channel.
	// Persisted to channel_stats.json and restored on startup.
	lastActiveTeamID    string
	lastActiveChannelID string

	focus    focus
	width    int
	height   int
	msgsView viewport.Model

	input textarea.Model

	loading bool
	status  string

	ws      *model.WebSocketClient
	wsRetry int

	unread   map[string]int
	mentions map[string]int

	mention mentionState

	// Pending file attachments composed for the next outgoing post. Each
	// chip carries its own spinner and upload context so uploads run
	// concurrently and can be cancelled individually (e.g. when removed
	// mid-upload). attachmentIdx is the cursor when focus == focusAttachments.
	attachments   []pendingAttachment
	attachmentIdx int
	uploadCancel  map[string]context.CancelFunc

	// Thread sidebar state. threadOpen toggles the panel; the rest
	// describes which thread is being shown and the loaded posts.
	threadOpen      bool
	threadRootID    string
	threadChannelID string
	threadPosts     []*model.Post
	threadIdx       int
	threadLoading   bool
	threadView      viewport.Model

	keys keyMap
	help help.Model
}

func New(client *mm.Client) Model {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filter…"
	ti.CharLimit = 64
	ti.SetWidth(channelsWidth - 4)

	sw := textinput.New()
	sw.Prompt = "> "
	sw.Placeholder = "switch to channel…"
	sw.CharLimit = 64
	sw.SetWidth(switcherWidth - 6)

	ta := textarea.New()
	ta.Placeholder = "message…"
	ta.CharLimit = 4000
	ta.ShowLineNumbers = false
	// v2's built-in DynamicHeight grows the textarea between MinHeight
	// and MaxHeight rows as content is added/removed, so we no longer
	// need a hand-rolled syncInputHeight.
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = maxInputHeight
	ta.SetHeight(1)
	// PromptFunc only renders "> " on the first wrapped row of the input
	// and pads continuation lines with two spaces so multi-line content
	// reads cleanly. (The plain .Prompt field would prefix every visual
	// row, which looks like multiple separate prompts when the textarea
	// grows.) The default prompt is updated when a thread is opened
	// (`m.input.SetPromptFunc(...)` in openThreadForPost).
	ta.SetPromptFunc(2, inputPromptFunc("> "))
	// Drop the default cursor-line highlight — it underlines/inverts the
	// whole row the cursor is on, which renders as a stray horizontal
	// bar above the typed content inside our bordered input box.
	taStyles := ta.Styles()
	taStyles.Focused.CursorLine = lipgloss.NewStyle()
	taStyles.Blurred.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(taStyles)
	// Enter sends; alt+enter / ctrl+j / shift+enter all insert a newline.
	// v2's default kitty "disambiguate" flag makes shift+enter a distinct
	// keystroke on kitty-protocol-capable terminals.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("alt+enter", "ctrl+j", "shift+enter"),
		key.WithHelp("shift+↵", "newline"),
	)

	h := help.New()

	stats, la := loadChannelStats()

	msgsView := viewport.New()
	msgsView.SoftWrap = true
	threadView := viewport.New()
	threadView.SoftWrap = true

	return Model{
		client:             client,
		ctx:                context.Background(),
		channels:           map[string][]*model.Channel{},
		userNames:          map[string]string{},
		focus:              focusChannels,
		msgsView:           msgsView,
		threadView:         threadView,
		filter:             ti,
		switcher:           sw,
		openStats:          stats,
		lastActiveTeamID:   la.teamID(),
		lastActiveChannelID: la.channelID(),
		input:      ta,
		unread:       map[string]int{},
		mentions:     map[string]int{},
		uploadCancel: map[string]context.CancelFunc{},
		loading:      true,
		status:       "loading…",
		keys:         newKeyMap(),
		help:         h,
	}
}

// ShortHelp returns the bindings shown on the footer's single-line help.
// The selection depends on which pane has focus so the prompt always
// matches the keys that will work right now.
func (m Model) ShortHelp() []key.Binding {
	k := m.keys
	switch {
	case m.switcherMode:
		return []key.Binding{k.ApplyOpen, k.Up, k.Down, k.CancelEdit}
	case m.filterMode:
		return []key.Binding{k.ApplyOpen, k.CancelEdit}
	case m.focus == focusInput:
		return []key.Binding{k.Send, k.NewLine, k.Paste, k.LeaveInput, k.Tab}
	case m.focus == focusChannels:
		return []key.Binding{k.Tab, k.Up, k.Down, k.OpenChannel, k.Filter, k.ClearFilter, k.Switcher, k.Unread, k.Help, k.Quit}
	case m.focus == focusMessages:
		return []key.Binding{k.Tab, k.Up, k.Down, k.OpenThread, k.OpenAttach, k.CopyMD, k.Switcher, k.Unread, k.Help, k.Quit}
	case m.focus == focusThread:
		return []key.Binding{k.Tab, k.Up, k.Down, k.OpenAttach, k.CopyMD, k.CloseThread, k.Switcher, k.Unread, k.Help, k.Quit}
	case m.focus == focusAttachments:
		return []key.Binding{k.Left, k.Right, k.OpenAttach, k.AttachRemove, k.Tab, k.Help, k.Quit}
	case m.focus == focusTeams:
		return []key.Binding{k.Tab, k.SwitchTeam, k.LoadTeam, k.Switcher, k.Unread, k.Help, k.Quit}
	}
	return []key.Binding{k.Tab, k.Switcher, k.Unread, k.Help, k.Quit}
}

// FullHelp returns the bindings grouped into columns for the expanded
// help view (toggled with `?`). Columns mirror the panes of the UI.
func (m Model) FullHelp() [][]key.Binding {
	k := m.keys
	return [][]key.Binding{
		{k.Tab, k.ShiftTab, k.Switcher, k.Unread, k.Help, k.Quit},
		{k.Up, k.Down, k.Home, k.End, k.Left, k.Right},
		{k.Filter, k.ClearFilter, k.OpenChannel, k.OpenThread, k.CloseThread},
		{k.OpenAttach, k.CopyMD, k.Send, k.NewLine, k.LeaveInput},
		{k.Paste, k.AttachRemove},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchMe(), m.connectWS())
}

func (m Model) connectWS() tea.Cmd {
	return func() tea.Msg {
		ws, err := m.client.DialWS()
		if err != nil {
			return wsClosedMsg{err: err}
		}
		return wsConnectedMsg{ws: ws}
	}
}

func (m Model) fetchChannelMembers(userID string) tea.Cmd {
	return func() tea.Msg {
		ms, err := m.client.ChannelMembers(m.ctx, userID)
		if err != nil {
			return errMsg{err}
		}
		return membersLoadedMsg{members: ms}
	}
}

// markChannelViewed marks the given channel as read on the server. The
// response is ignored — local counters are reset by the caller via
// applyUnread / postsLoadedMsg.
func (m Model) markChannelViewed(channelID string) tea.Cmd {
	if m.me == nil {
		return nil
	}
	userID := m.me.Id
	return func() tea.Msg {
		_ = m.client.ViewChannel(m.ctx, userID, channelID)
		return nil
	}
}

// applyUnreadFromMembers seeds m.unread / m.mentions from the
// server-side channel-member state. Only runs once both channels and
// members have arrived; the currently-focused channel is skipped so an
// in-progress fetchPosts that beat us here keeps its zero badge.
func (m *Model) applyUnreadFromMembers() {
	if !m.channelsLoaded || !m.membersLoaded {
		return
	}
	chByID := map[string]*model.Channel{}
	for _, list := range m.channels {
		for _, c := range list {
			chByID[c.Id] = c
		}
	}
	var current string
	if vis := m.visibleChannels(); m.channelIdx < len(vis) {
		current = vis[m.channelIdx].Id
	}
	for _, mb := range m.members {
		if mb.ChannelId == current {
			delete(m.unread, mb.ChannelId)
			delete(m.mentions, mb.ChannelId)
			continue
		}
		ch, ok := chByID[mb.ChannelId]
		if !ok {
			continue
		}
		unread := ch.TotalMsgCount - mb.MsgCount
		if unread > 0 {
			m.unread[mb.ChannelId] = int(unread)
		} else {
			delete(m.unread, mb.ChannelId)
		}
		if mb.MentionCount > 0 {
			m.mentions[mb.ChannelId] = int(mb.MentionCount)
		} else {
			delete(m.mentions, mb.ChannelId)
		}
	}
}

// fetchFileInfos resolves attachments for a post that arrived without
// populated metadata (typically via WebSocket).
func (m Model) fetchFileInfos(postID string) tea.Cmd {
	return func() tea.Msg {
		infos, err := m.client.FileInfosForPost(m.ctx, postID)
		if err != nil {
			return errMsg{err}
		}
		return fileInfosLoadedMsg{postID: postID, infos: infos}
	}
}

// openable represents anything `o` can open from the selected post:
// either an uploaded file (download to cache, then xdg-open the local
// path) or a direct URL (xdg-open it as-is, letting the desktop
// dispatch to the browser).
type openable struct {
	name string
	file *model.FileInfo // download via API and open locally
	url  string          // hand directly to xdg-open
}

// collectOpenables enumerates everything in a post that `o` can act on:
// uploaded attachments first (in metadata order), then inline
// ![alt](url) image links (in message order).
func collectOpenables(p *model.Post) []openable {
	var out []openable
	if p.Metadata != nil {
		for _, f := range p.Metadata.Files {
			out = append(out, openable{name: f.Name, file: f})
		}
	}
	for _, sub := range mdImageRe.FindAllStringSubmatch(p.Message, -1) {
		alt, url := sub[1], sub[2]
		if url == "" {
			continue
		}
		name := alt
		if name == "" {
			name = url
		}
		out = append(out, openable{name: name, url: url})
	}
	return out
}

// openOpenable runs xdg-open on either a downloaded file or a URL.
func (m Model) openOpenable(o openable) tea.Cmd {
	return func() tea.Msg {
		target := o.url
		if o.file != nil {
			path, err := m.cachedFilePath(o.file)
			if err != nil {
				return attachmentOpenedMsg{name: o.name, err: err}
			}
			if _, statErr := os.Stat(path); statErr != nil {
				data, err := m.client.DownloadFile(m.ctx, o.file.Id)
				if err != nil {
					return attachmentOpenedMsg{name: o.name, err: err}
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					return attachmentOpenedMsg{name: o.name, err: err}
				}
			}
			target = path
		}
		// xdg-open forks and returns immediately; we don't wait for the
		// viewer process to exit.
		if err := exec.Command("xdg-open", target).Start(); err != nil {
			return attachmentOpenedMsg{name: o.name, err: err}
		}
		return attachmentOpenedMsg{name: o.name}
	}
}

// cachedFilePath returns the on-disk location for a downloaded
// attachment, creating the cache directory if needed. Files are keyed
// by ID so the same upload doesn't get re-downloaded.
func (m Model) cachedFilePath(f *model.FileInfo) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "matterbox", "files")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := f.Name
	if name == "" {
		name = "file"
	}
	return filepath.Join(dir, fmt.Sprintf("%s_%s", f.Id, name)), nil
}

func (m Model) copyPostMarkdown(p *model.Post) tea.Cmd {
	return func() tea.Msg {
		if err := clipboard.WriteAll(p.Message); err != nil {
			return errMsg{err}
		}
		return copyClipboardMsg{}
	}
}

type copyClipboardMsg struct{}

func (m Model) sendMessage(channelID, rootID, text string, fileIDs []string) tea.Cmd {
	return func() tea.Msg {
		p, err := m.client.Send(m.ctx, channelID, rootID, text, fileIDs)
		if err != nil {
			return errMsg{err}
		}
		return postSentMsg{channelID: channelID, post: p}
	}
}

// fetchThread loads every post in the thread rooted at rootID, in
// chronological order, and resolves any unseen sender usernames.
func (m Model) fetchThread(rootID string) tea.Cmd {
	return func() tea.Msg {
		pl, err := m.client.Thread(m.ctx, rootID)
		if err != nil {
			return errMsg{err}
		}
		ordered := orderedThread(pl, rootID)

		need := map[string]struct{}{}
		for _, p := range ordered {
			if _, have := m.userNames[p.UserId]; !have {
				need[p.UserId] = struct{}{}
			}
		}
		users := map[string]string{}
		if len(need) > 0 {
			ids := make([]string, 0, len(need))
			for id := range need {
				ids = append(ids, id)
			}
			us, err := m.client.UsersByIDs(m.ctx, ids)
			if err != nil {
				return errMsg{err}
			}
			for _, u := range us {
				users[u.Id] = u.Username
			}
		}
		return threadLoadedMsg{rootID: rootID, posts: ordered, users: users}
	}
}

// orderedThread linearises the thread PostList into [root, …replies]
// sorted by CreateAt. The Mattermost API returns the posts as a map +
// Order slice, but Order is newest-first; we want oldest-first with the
// root pinned to the top.
func orderedThread(pl *model.PostList, rootID string) []*model.Post {
	if pl == nil {
		return nil
	}
	var root *model.Post
	replies := make([]*model.Post, 0, len(pl.Posts))
	for _, p := range pl.Posts {
		if p == nil {
			continue
		}
		if p.Id == rootID {
			root = p
			continue
		}
		replies = append(replies, p)
	}
	sort.SliceStable(replies, func(i, j int) bool {
		return replies[i].CreateAt < replies[j].CreateAt
	})
	out := make([]*model.Post, 0, len(replies)+1)
	if root != nil {
		out = append(out, root)
	}
	out = append(out, replies...)
	return out
}

// appendOptimistic adds a placeholder post for the user's own outgoing
// message so it shows immediately. The next refetch (post-send response
// or WS-driven) will replace it with the canonical server version.
// When rootID is non-empty, the stub is also appended to threadPosts so
// the thread sidebar updates instantly.
func (m *Model) appendOptimistic(channelID, rootID, text string, fileIDs []string) {
	if m.me == nil {
		return
	}
	m.userNames[m.me.Id] = m.me.Username
	stub := &model.Post{
		UserId:    m.me.Id,
		ChannelId: channelID,
		RootId:    rootID,
		Message:   text,
		FileIds:   fileIDs,
		CreateAt:  time.Now().UnixMilli(),
	}
	// Show in the main feed only when the target channel is what the
	// user is currently looking at — replying to a thread from another
	// channel shouldn't pollute the open channel's transcript.
	if m.isCurrentChannel(channelID) {
		m.posts = append(m.posts, stub)
	}
	if rootID != "" && m.threadOpen && m.threadRootID == rootID {
		m.threadPosts = append(m.threadPosts, stub)
		m.threadIdx = len(m.threadPosts) - 1
	}
}

// waitWSEvent yields a tea.Cmd that blocks until the next WebSocket
// event arrives, then returns it as a wsEventMsg. The caller is
// responsible for re-scheduling this cmd after handling the event.
func waitWSEvent(ws *model.WebSocketClient) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ws.EventChannel
		if !ok {
			return wsClosedMsg{}
		}
		return wsEventMsg{ev: ev}
	}
}

func (m Model) fetchMe() tea.Cmd {
	return func() tea.Msg {
		u, err := m.client.Me(m.ctx)
		if err != nil {
			return errMsg{err}
		}
		return meLoadedMsg{u}
	}
}

func (m Model) fetchTeams(userID string) tea.Cmd {
	return func() tea.Msg {
		t, err := m.client.Teams(m.ctx, userID)
		if err != nil {
			return errMsg{err}
		}
		sort.SliceStable(t, func(i, j int) bool {
			return displayTeam(t[i]) < displayTeam(t[j])
		})
		return teamsLoadedMsg{t}
	}
}

// fetchAllChannels gets every channel for the user (across teams + DMs) and
// pre-resolves the usernames needed for DM display names.
func (m Model) fetchAllChannels(userID string) tea.Cmd {
	return func() tea.Msg {
		chs, err := m.client.AllChannels(m.ctx, userID)
		if err != nil {
			return errMsg{err}
		}

		// Collect user IDs we need to resolve to render DMs.
		need := map[string]struct{}{}
		for _, c := range chs {
			if c.Type != model.ChannelTypeDirect {
				continue
			}
			for _, id := range strings.Split(c.Name, "__") {
				if id != "" && id != userID {
					need[id] = struct{}{}
				}
			}
		}
		names := map[string]string{}
		if len(need) > 0 {
			ids := make([]string, 0, len(need))
			for id := range need {
				ids = append(ids, id)
			}
			us, err := m.client.UsersByIDs(m.ctx, ids)
			if err != nil {
				return errMsg{err}
			}
			for _, u := range us {
				names[u.Id] = u.Username
			}
		}
		return channelsLoadedMsg{channels: chs, userNames: names}
	}
}

func (m Model) fetchPosts(channelID string) tea.Cmd {
	return func() tea.Msg {
		pl, err := m.client.Posts(m.ctx, channelID, 60)
		if err != nil {
			return errMsg{err}
		}
		ordered := make([]*model.Post, 0, len(pl.Order))
		for i := len(pl.Order) - 1; i >= 0; i-- {
			if p, ok := pl.Posts[pl.Order[i]]; ok {
				ordered = append(ordered, p)
			}
		}

		need := map[string]struct{}{}
		for _, p := range ordered {
			if _, have := m.userNames[p.UserId]; !have {
				need[p.UserId] = struct{}{}
			}
		}
		users := map[string]string{}
		if len(need) > 0 {
			ids := make([]string, 0, len(need))
			for id := range need {
				ids = append(ids, id)
			}
			us, err := m.client.UsersByIDs(m.ctx, ids)
			if err != nil {
				return errMsg{err}
			}
			for _, u := range us {
				users[u.Id] = u.Username
			}
		}
		return postsLoadedMsg{channelID: channelID, posts: ordered, users: users}
	}
}

// tabAt resolves a 0-based tab index into its kind and (for teams) the
// team's ID + display name. Tab order is: DMs (if present), Unread,
// teams in their loaded order.
func (m Model) tabAt(i int) (kind tabKind, id, name string) {
	if m.hasDMs {
		if i == 0 {
			return tabDM, dmTeamID, "DMs"
		}
		i--
	}
	if i == 0 {
		return tabUnread, unreadTeamID, "Unread"
	}
	i--
	if i >= 0 && i < len(m.teams) {
		return tabTeam, m.teams[i].Id, displayTeam(m.teams[i])
	}
	return tabTeam, "", ""
}

// currentTeamID returns the team ID corresponding to the focused tab, or
// the synthetic dmTeamID / unreadTeamID for the virtual tabs.
func (m Model) currentTeamID() string {
	_, id, _ := m.tabAt(m.teamIdx)
	return id
}

// visibleChannels returns the channels in the current team, filtered.
// For the Unread tab the list is computed on demand from m.unread /
// m.mentions across every bucket.
func (m Model) visibleChannels() []*model.Channel {
	var all []*model.Channel
	if m.currentTeamID() == unreadTeamID {
		all = m.unreadChannels()
	} else {
		all = m.channels[m.currentTeamID()]
	}
	if m.filterValue == "" {
		return all
	}
	needle := strings.ToLower(m.filterValue)
	out := make([]*model.Channel, 0, len(all))
	for _, c := range all {
		if strings.Contains(strings.ToLower(m.channelLabel(c)), needle) {
			out = append(out, c)
		}
	}
	return out
}

// unreadChannels gathers every channel (across all buckets, including
// DMs) with a non-zero unread or mention count, sorted with mentions
// first then alphabetically.
func (m Model) unreadChannels() []*model.Channel {
	var out []*model.Channel
	for _, list := range m.channels {
		for _, c := range list {
			if m.unread[c.Id] > 0 || m.mentions[c.Id] > 0 {
				out = append(out, c)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		mi, mj := m.mentions[out[i].Id] > 0, m.mentions[out[j].Id] > 0
		if mi != mj {
			return mi
		}
		return strings.ToLower(m.channelLabel(out[i])) < strings.ToLower(m.channelLabel(out[j]))
	})
	return out
}

// switchToChannelHomeTeam navigates the team tabs and channel selection
// to put `ch` in its native bucket (its team, or DMs for direct/group
// channels). Used when the user opens a channel from the virtual
// Unread tab so the messages pane stays in sync with isCurrentChannel.
func (m *Model) switchToChannelHomeTeam(ch *model.Channel) {
	targetTeamID := ch.TeamId
	if ch.Type == model.ChannelTypeDirect || ch.Type == model.ChannelTypeGroup || targetTeamID == "" {
		targetTeamID = dmTeamID
	}
	for i := 0; i <= m.maxTeamIdx(); i++ {
		_, id, _ := m.tabAt(i)
		if id == targetTeamID {
			m.teamIdx = i
			break
		}
	}
	for i, c := range m.channels[targetTeamID] {
		if c.Id == ch.Id {
			m.channelIdx = i
			m.chanOff = 0
			return
		}
	}
}

// restoreLastActive attempts to set teamIdx and channelIdx from a
// previous session's persisted state. If the saved team or channel no
// longer exists, the indices are left unchanged and the caller's existing
// clamping logic kicks in. Does nothing for the synthetic Unread tab.
func (m *Model) restoreLastActive() {
	if m.lastActiveTeamID == "" || m.lastActiveChannelID == "" {
		return
	}
	if m.lastActiveTeamID == unreadTeamID {
		return
	}
	for i := 0; i <= m.maxTeamIdx(); i++ {
		_, id, _ := m.tabAt(i)
		if id == m.lastActiveTeamID {
			m.teamIdx = i
			break
		}
	}
	chList, ok := m.channels[m.lastActiveTeamID]
	if !ok {
		return
	}
	for i, c := range chList {
		if c.Id == m.lastActiveChannelID {
			m.channelIdx = i
			m.chanOff = 0
			return
		}
	}
}

// threadTeamID returns the TeamId of the thread's channel (empty for DMs),
// looking it up across every bucket. Returns "" if not found.
func (m Model) threadTeamID() string {
	if m.threadChannelID == "" {
		return ""
	}
	for _, list := range m.channels {
		for _, c := range list {
			if c.Id == m.threadChannelID {
				return c.TeamId
			}
		}
	}
	return ""
}

// channelLabel renders the per-row label, resolving DM partner usernames.
func (m Model) channelLabel(c *model.Channel) string {
	switch c.Type {
	case model.ChannelTypeDirect:
		other := ""
		for _, id := range strings.Split(c.Name, "__") {
			if id != "" && (m.me == nil || id != m.me.Id) {
				other = id
				break
			}
		}
		if other == "" && m.me != nil {
			return "@" + m.me.Username + " (you)"
		}
		if n, ok := m.userNames[other]; ok && n != "" {
			return "@" + n
		}
		if len(other) > 8 {
			return "@" + other[:8]
		}
		return "@?"
	case model.ChannelTypeGroup:
		if c.DisplayName != "" {
			return "·" + c.DisplayName
		}
		return "·group"
	case model.ChannelTypePrivate:
		return "🔒" + displayChannel(c)
	default:
		return "#" + displayChannel(c)
	}
}

// bucketChannels splits the channels slice into per-team buckets, sorts
// each bucket, and updates m.hasDMs.
func (m *Model) bucketChannels(chs []*model.Channel) {
	m.channels = map[string][]*model.Channel{}
	for _, c := range chs {
		key := c.TeamId
		if c.Type == model.ChannelTypeDirect || c.Type == model.ChannelTypeGroup || key == "" {
			key = dmTeamID
		}
		m.channels[key] = append(m.channels[key], c)
	}
	for k, list := range m.channels {
		list := list
		sort.SliceStable(list, func(i, j int) bool {
			return strings.ToLower(m.channelLabel(list[i])) < strings.ToLower(m.channelLabel(list[j]))
		})
		m.channels[k] = list
	}
	_, m.hasDMs = m.channels[dmTeamID]
}

func displayTeam(t *model.Team) string {
	if t.DisplayName != "" {
		return t.DisplayName
	}
	return t.Name
}

func displayChannel(c *model.Channel) string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	return c.Name
}
