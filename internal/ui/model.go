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

	"matterbox/internal/config"
	"matterbox/internal/mm"
	"matterbox/internal/store"
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
	focusSearch
	focusFeed
)

const numFocus = 8

// dmTeamID is a synthetic team identifier used to bucket DMs / group-DMs,
// which carry an empty Channel.TeamId on the server.
const dmTeamID = "__dm__"

// unreadTeamID is a synthetic team identifier for the virtual "Unread"
// tab. No channels are stored under this key; visibleChannels computes
// the list on demand from m.unread / m.mentions across every bucket.
const unreadTeamID = "__unread__"

// searchTeamID is a synthetic team identifier for the virtual "Search"
// tab. Its body is the live-search UI (input + result bubbles) rather
// than the channel list.
const searchTeamID = "__search__"

// feedTeamID is a synthetic team identifier for the virtual "Feed" tab.
// Its body is the combined unread feed (one bubble per unread channel)
// rather than the channel list. Like Unread, no channels are stored
// under this key — the feed is computed on demand from m.unread /
// m.mentions across every bucket.
const feedTeamID = "__feed__"

type tabKind int

const (
	tabTeam tabKind = iota
	tabDM
	tabUnread
	tabSearch
	tabFeed
)

type Model struct {
	client *mm.Client
	ctx    context.Context

	me          *model.User
	teams       []*model.Team
	teamsLoaded bool
	// teamOrder is the user's preferred left-to-right team-tab ordering,
	// by URL name, loaded from config.yaml and re-saved after each in-app
	// reorder. applyTeamOrder uses it to sort m.teams; teams not listed
	// fall to the end alphabetically.
	teamOrder      []string
	hasDMs         bool
	channels       map[string][]*model.Channel // teamID → channels (DMs under dmTeamID)
	channelsLoaded bool
	members        model.ChannelMembersWithTeamData
	membersLoaded  bool
	userNames      map[string]string // userID → username

	teamIdx    int
	channelIdx int
	chanOff    int

	posts   []*model.Post
	postIdx int // index of the selected post in m.posts

	// anchorMsgSelTop is a one-shot flag: when true, the next
	// renderMessages call positions the selected post at the *top* of
	// the viewport instead of the usual "ensure visible" behaviour
	// (which would otherwise drop it at the bottom). Used after
	// prepending older cached posts so the new selection appears
	// where the previous top of content used to be, keeping the view
	// stable from the user's perspective. Cleared on each render.
	anchorMsgSelTop bool

	// channel filter
	filter      textinput.Model
	filterMode  bool
	filterValue string // committed/live filter applied to channel list

	// leaderPending is true between pressing the "," leader key and the
	// next keystroke, which handleLeaderKey interprets as a pane/tab jump.
	// Only set/cleared inside handleKey's navigation region, so it never
	// outlives a single keypress.
	leaderPending bool

	// global channel switcher (ctrl+k). When switcherMode is true, the
	// switcher owns every keystroke and an overlay popup is rendered in
	// place of the main body. switcherCmdPending is non-nil while the
	// switcher is in "> command" arg-prompt mode, waiting for the user
	// to enter the captive argument for a previously-selected command.
	switcher           textinput.Model
	switcherMode       bool
	switcherIdx        int
	switcherCmdPending *switcherCommand

	// indexer is the background channel-backfill state machine. Only one
	// run is active at a time; subsequent triggers surface a hint.
	indexer indexerState

	// typing drives the "Typing animation" > command: a fake key-by-key
	// reveal of a message implemented as a stream of EditPost calls.
	typing typingAnim

	// ball drives the "Bouncing ball" > command: a ball ricocheting
	// inside a code-block box, animated by editing one post per frame.
	ball ballAnim

	// Persisted per-channel usage counters (loaded from
	// ~/.config/matterbox/channel_stats.json). Used as a sort signal in
	// the switcher so frequently-opened channels float to the top.
	openStats map[string]channelStat

	// store is the local SQLite database that caches messages and powers
	// future local search. Best-effort: if Open fails, it stays nil and
	// the app falls back to the original fresh-fetch behaviour. Every
	// access in this package is guarded by `if m.store != nil`.
	store *store.Store

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
	// lastInputHeight is the input height the messages pane was last
	// reflowed for. syncInputHeight compares against it to reflow only
	// when the textarea's DynamicHeight actually changed.
	lastInputHeight int

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

	// editingPostID is non-empty while the user is editing an existing
	// post: the textarea is preloaded with that post's message and Send
	// patches it on the server instead of creating a new post. Cleared
	// after a successful patch, on esc, or when the post is deleted out
	// from under the editor. The post may live in either m.posts or
	// m.threadPosts; both panes are searched on send.
	editingPostID string

	// Delete-confirmation modal. While deleteConfirmPostID is non-empty
	// the modal owns every keystroke (y/enter confirms, n/esc cancels)
	// and renders over the body via lipgloss.Place. The summary text is
	// cached so we don't have to walk m.posts again on every render.
	deleteConfirmPostID string
	deleteConfirmText   string

	// Edit-history popup. historyMode owns every keystroke and renders
	// the popup as an overlay. historyPost is the current version (kept
	// to label the final entry); historyRevisions are the prior
	// versions, oldest first.
	historyMode      bool
	historyPost      *model.Post
	historyRevisions []*model.Post
	historyView      viewport.Model

	// Search tab state: live FTS5 search over the persisted message
	// corpus. Activated by F (all channels) or / (scoped to the current
	// channel), or by selecting the synthetic "Search" tab. See
	// internal/ui/search.go for the implementation.
	search searchState

	// Feed tab state: the combined unread feed (one bubble per unread
	// channel, across every team + DMs). Activated by U or by selecting
	// the synthetic "Feed" tab. See internal/ui/feed.go.
	feed feedState

	// pendingJumpPostID, when non-empty, is the post id renderMessages
	// should center on after the next postsLoadedMsg lands — used by the
	// search-result → channel jump flow.
	pendingJumpPostID string

	// pollDialog owns the modal-input flow used to fill in matterpoll's
	// "Add Option" interactive dialog (and any other plugin-emitted
	// dialog). The modal is opened by an `open_dialog` WS event that
	// arrives after the user fires the corresponding action button.
	pollDialog pollDialogState

	// reactionEmojis is the picker list configured at startup from
	// ~/.config/matterbox/config.yaml. Snapshot at New() time — the file
	// isn't watched, so editing it requires a restart.
	reactionEmojis []string

	// Summary command (ctrl+k → "> Summarize"). The endpoint/model/prompt
	// are snapshotted from config.yaml at New() time; summary holds the live
	// modal state (duration picker, in-flight request, result popup).
	summaryEndpoint string
	summaryAPIKey   string
	summaryModel    string
	summaryPrompt   string
	summary         summaryState

	// Agentic search on the Search tab (a query ending in "?"). Reuses the
	// summary endpoint+model; aiSearchPrompt frames the agent, aiSearchMaxSteps
	// bounds its tool-call rounds, and aiSearchTimeout bounds the whole run.
	// aiSearch holds the live run state (trace, in-flight request, final answer).
	aiSearchPrompt   string
	aiSearchMaxSteps int
	aiSearchTimeout  time.Duration
	aiSearch         aiSearchState

	// Reaction-picker modal. While reactionPickerPostID is non-empty the
	// modal owns every keystroke (digit selects + fires, ↑/↓+↵ navigate,
	// esc cancels). reactionPickerIdx is the cursor position within
	// reactionEmojis.
	reactionPickerPostID string
	reactionPickerIdx    int

	keys keyMap
	help help.Model

	// postLineCache memoizes renderPostLines / renderThreadPostLines
	// output keyed by post id, with a fingerprint over the inputs (see
	// postcache.go). Bounded at postLineCacheCap; cleared on width
	// change. Polls are intentionally not cached — their render depends
	// on the current selection.
	postLineCache map[string]postLineCacheEntry
}

func New(client *mm.Client, cfg *config.Config) Model {
	ti := textinput.New()
	ti.Prompt = "f "
	ti.Placeholder = "filter…"
	ti.CharLimit = 64
	ti.SetWidth(channelsWidth - 4)

	sw := textinput.New()
	sw.Prompt = "> "
	sw.Placeholder = "switch to channel or > for commands…"
	sw.CharLimit = 64
	sw.SetWidth(switcherWidth - 6)

	ta := textarea.New()
	ta.Placeholder = "message…"
	ta.CharLimit = 4000
	ta.ShowLineNumbers = false
	// v2's built-in DynamicHeight grows the textarea between MinHeight
	// and MaxHeight rows as content is added/removed (counting wrapped
	// visual rows), so the textarea owns its own height — syncInputHeight
	// only reflows the messages pane when that height changes.
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

	var st *store.Store
	if p, err := store.DefaultPath(); err == nil {
		// Best-effort: silently degrade if the DB can't be opened. The
		// app stays functional without the cache.
		var opts []store.Option
		if cfg != nil && cfg.Search.RecencyHalfLifeDays > 0 {
			opts = append(opts, store.WithRecencyHalfLife(
				time.Duration(cfg.Search.RecencyHalfLifeDays*float64(24*time.Hour))))
		}
		st, _ = store.Open(p, opts...)
	}

	msgsView := viewport.New()
	msgsView.SoftWrap = true
	threadView := viewport.New()
	threadView.SoftWrap = true
	historyView := viewport.New()
	historyView.SoftWrap = true

	var reactions []string
	var teamOrder []string
	var summaryEndpoint, summaryAPIKey, summaryModel, summaryPrompt string
	var aiSearchPrompt string
	var aiSearchMaxSteps int
	var aiSearchTimeout time.Duration
	if cfg != nil {
		reactions = append([]string(nil), cfg.Reactions...)
		teamOrder = append([]string(nil), cfg.TeamOrder...)
		summaryEndpoint = cfg.Summary.Endpoint
		summaryAPIKey = cfg.Summary.APIKey
		summaryModel = cfg.Summary.Model
		summaryPrompt = cfg.Summary.Prompt
		aiSearchPrompt = cfg.AISearch.Prompt
		aiSearchMaxSteps = cfg.AISearch.MaxSteps
		aiSearchTimeout = time.Duration(cfg.AISearch.TimeoutMinutes) * time.Minute
	}

	return Model{
		client:              client,
		ctx:                 context.Background(),
		channels:            map[string][]*model.Channel{},
		userNames:           map[string]string{},
		focus:               focusChannels,
		msgsView:            msgsView,
		threadView:          threadView,
		historyView:         historyView,
		filter:              ti,
		switcher:            sw,
		openStats:           stats,
		store:               st,
		lastActiveTeamID:    la.teamID(),
		lastActiveChannelID: la.channelID(),
		input:               ta,
		unread:              map[string]int{},
		mentions:            map[string]int{},
		uploadCancel:        map[string]context.CancelFunc{},
		loading:             true,
		status:              "loading…",
		keys:                newKeyMap(),
		help:                h,
		search:              newSearchState(st != nil),
		feed:                newFeedState(),
		reactionEmojis:      reactions,
		teamOrder:           teamOrder,
		summaryEndpoint:     summaryEndpoint,
		summaryAPIKey:       summaryAPIKey,
		summaryModel:        summaryModel,
		summaryPrompt:       summaryPrompt,
		summary:             newSummaryState(),
		aiSearchPrompt:      aiSearchPrompt,
		aiSearchMaxSteps:    aiSearchMaxSteps,
		aiSearchTimeout:     aiSearchTimeout,
		aiSearch:            newAISearchState(),
	}
}

// leaderHints are the synthetic bindings shown in the footer while the
// "," leader chord is pending, so the user sees the available jump
// targets before pressing the second key. The keys are display-only
// (they're never matched against — handleLeaderKey owns the resolution).
func leaderHints() []key.Binding {
	nb := func(k, h string) key.Binding {
		return key.NewBinding(key.WithKeys(k), key.WithHelp(k, h))
	}
	return []key.Binding{
		nb("t", "team bar"),
		nb("c", "channels"),
		nb("m", "messages"),
		nb("i", "compose"),
		nb("d", "DMs"),
		nb("1-9", "team N"),
	}
}

// ShortHelp returns the bindings shown on the footer's single-line help.
// The selection depends on which pane has focus so the prompt always
// matches the keys that will work right now.
func (m Model) ShortHelp() []key.Binding {
	k := m.keys
	if m.leaderPending {
		return leaderHints()
	}
	switch {
	case m.switcherMode:
		return []key.Binding{k.ApplyOpen, k.Up, k.Down, k.CancelEdit}
	case m.filterMode:
		return []key.Binding{k.ApplyOpen, k.CancelEdit}
	case m.focus == focusInput:
		return []key.Binding{k.Send, k.NewLine, k.Paste, k.LeaveInput, k.Tab}
	case m.focus == focusChannels:
		return []key.Binding{k.Tab, k.Up, k.Down, k.OpenChannel, k.Compose, k.SearchHere, k.Filter, k.ClearFilter, k.Leader, k.Switcher, k.Unread, k.Feed, k.Help, k.Quit}
	case m.focus == focusMessages:
		return []key.Binding{k.Tab, k.Up, k.Down, k.Compose, k.OpenThread, k.ReplyInThread, k.SearchHere, k.NextHit, k.PrevHit, k.OpenAttach, k.CopyMD, k.EditPost, k.DeletePost, k.React, k.ShowHistory, k.Leader, k.Switcher, k.Unread, k.Feed, k.Help, k.Quit}
	case m.focus == focusThread:
		return []key.Binding{k.Tab, k.Up, k.Down, k.Compose, k.SearchHere, k.OpenAttach, k.CopyMD, k.EditPost, k.DeletePost, k.React, k.ShowHistory, k.CloseThread, k.Leader, k.Switcher, k.Unread, k.Help, k.Quit}
	case m.focus == focusAttachments:
		return []key.Binding{k.Left, k.Right, k.OpenAttach, k.AttachRemove, k.Tab, k.Leader, k.Help, k.Quit}
	case m.focus == focusTeams:
		return []key.Binding{k.Tab, k.SwitchTeam, k.LoadTeam, k.MoveTeamLeft, k.MoveTeamRight, k.SearchHere, k.Leader, k.Switcher, k.Search, k.Unread, k.Feed, k.Help, k.Quit}
	case m.focus == focusSearch:
		return []key.Binding{k.Up, k.Down, k.ApplyOpen, k.CancelEdit, k.Tab, k.Help, k.Quit}
	case m.focus == focusFeed:
		return []key.Binding{k.Up, k.Down, k.OpenChannel, k.MarkRead, k.Refresh, k.Tab, k.Leader, k.Unread, k.Help, k.Quit}
	}
	return []key.Binding{k.Tab, k.Leader, k.Switcher, k.Search, k.SearchHere, k.Unread, k.Feed, k.Help, k.Quit}
}

// FullHelp returns the bindings grouped into columns for the expanded
// help view (toggled with `?`). Columns mirror the panes of the UI.
func (m Model) FullHelp() [][]key.Binding {
	k := m.keys
	if m.leaderPending {
		return [][]key.Binding{leaderHints()}
	}
	return [][]key.Binding{
		{k.Tab, k.ShiftTab, k.Leader, k.Switcher, k.Search, k.SearchHere, k.Unread, k.Feed, k.Help, k.Quit},
		{k.Up, k.Down, k.Home, k.End, k.Left, k.Right, k.PageDown, k.PageUp, k.NextHit, k.PrevHit},
		{k.Filter, k.ClearFilter, k.OpenChannel, k.OpenThread, k.ReplyInThread, k.CloseThread},
		{k.OpenAttach, k.CopyMD, k.EditPost, k.DeletePost, k.React, k.ShowHistory, k.Compose, k.Send, k.NewLine, k.LeaveInput},
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

// resolveUnknownSenders scans everything currently on screen — the feed,
// the open thread, search hits, and DM partners — for user ids we can't
// name yet and, if any, returns a Cmd that fetches their usernames in
// the background. This is what self-heals a "broken username" (a post
// rendered as a truncated raw id): posts painted from the warm cache, or
// arriving over WebSocket without a sender_name, reach the screen before
// their author is resolved. Run from Update after each event; it returns
// nil (cheaply) once everything visible is named. Deduplication is the
// client's job — UsernamesByIDs coalesces repeated lookups for the same
// set via singleflight, so firing this on every event is fine.
func (m Model) resolveUnknownSenders() tea.Cmd {
	var ids []string
	seen := map[string]struct{}{}
	want := func(uid string) {
		if uid == "" {
			return
		}
		// Membership (not emptiness) is the test: a "" entry is a negative
		// cache for a user the server didn't return, so we don't keep
		// asking. Real names overwrite it if another path learns one.
		if _, known := m.userNames[uid]; known {
			return
		}
		if _, dup := seen[uid]; dup {
			return
		}
		seen[uid] = struct{}{}
		ids = append(ids, uid)
	}
	for _, p := range m.posts {
		if p != nil {
			want(p.UserId)
		}
	}
	for _, p := range m.threadPosts {
		if p != nil {
			want(p.UserId)
		}
	}
	for _, h := range m.search.hits {
		if h.Match != nil {
			want(h.Match.UserId)
		}
		for _, p := range h.Before {
			want(p.UserId)
		}
		for _, p := range h.After {
			want(p.UserId)
		}
	}
	// DM/group-DM partners drive the sidebar labels (@name); a missing one
	// shows as @<truncated-id> until resolved.
	for _, c := range m.channels[dmTeamID] {
		if c.Type != model.ChannelTypeDirect {
			continue
		}
		for _, id := range strings.Split(c.Name, "__") {
			if m.me == nil || id != m.me.Id {
				want(id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return func() tea.Msg {
		names, err := m.client.UsernamesByIDs(m.ctx, ids)
		if err != nil {
			return usersResolvedMsg{ids: ids, err: err}
		}
		return usersResolvedMsg{ids: ids, users: names}
	}
}

// initialRenderLimit is how many cached posts we paint when reopening a
// channel. Matches fetchPosts' page size so warm and cold paths produce
// visually identical opening views.
const initialRenderLimit = 60

// loadFromStore returns the most recent cached posts for the channel
// (oldest→newest), or nil if the store is unavailable, the read failed,
// or no posts are cached.
func (m Model) loadFromStore(channelID string) []*model.Post {
	if m.store == nil {
		return nil
	}
	posts, err := m.store.RecentForChannel(channelID, initialRenderLimit)
	if err != nil {
		return nil
	}
	return posts
}

// olderPageSize is how many older posts we pull from the cache each time
// the user scrolls past the top of what's currently rendered.
const olderPageSize = 60

// loadOlderFromStore returns up to olderPageSize cached posts in
// `channelID` that are strictly older than beforeCreateAt, oldest→newest.
// Returns nil when the store is unavailable, the read fails, or no
// older cached posts exist.
func (m Model) loadOlderFromStore(channelID string, beforeCreateAt int64) []*model.Post {
	if m.store == nil || channelID == "" || beforeCreateAt <= 0 {
		return nil
	}
	posts, err := m.store.BeforeInChannel(channelID, beforeCreateAt, olderPageSize)
	if err != nil {
		return nil
	}
	return posts
}

// loadNewerFromStore mirrors loadOlderFromStore but pages forward in
// time. Used to extend the rendered slice when the user scrolls past
// the last post — most useful after opening a search hit which centred
// the view on an older post.
func (m Model) loadNewerFromStore(channelID string, afterCreateAt int64) []*model.Post {
	if m.store == nil || channelID == "" || afterCreateAt <= 0 {
		return nil
	}
	posts, err := m.store.AfterInChannel(channelID, afterCreateAt, olderPageSize)
	if err != nil {
		return nil
	}
	return posts
}

// fetchPostsAfter pulls every post created after afterPostID in the
// channel. Use this when reopening a channel whose history is already
// in the cache: paint cached → call this → append the gap.
func (m Model) fetchPostsAfter(channelID, afterPostID string) tea.Cmd {
	return func() tea.Msg {
		pl, err := m.client.PostsAfter(m.ctx, channelID, afterPostID, 200)
		if err != nil {
			return errMsg{err}
		}
		// pl.Order is newest-first; flip to oldest-first.
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
		return postsGapFilledMsg{channelID: channelID, posts: ordered, users: users}
	}
}

// persistPosts writes the given posts to the store on a worker
// goroutine so a slow disk can't stall the UI. No-op when the store is
// unavailable or the slice is empty.
func (m Model) persistPosts(posts ...*model.Post) tea.Cmd {
	if m.store == nil || len(posts) == 0 {
		return nil
	}
	st := m.store
	// Copy so the caller can safely mutate its slice after returning.
	cp := append([]*model.Post(nil), posts...)
	return func() tea.Msg {
		_ = st.UpsertMany(cp)
		return nil
	}
}

// persistDelete removes a post by Id from the store. No-op when the
// store is unavailable.
func (m Model) persistDelete(id string) tea.Cmd {
	if m.store == nil || id == "" {
		return nil
	}
	st := m.store
	return func() tea.Msg {
		_ = st.Delete(id)
		return nil
	}
}

// openChannelLoadCmd handles the load side of opening a channel. If the
// store has cached posts it paints them immediately (sets m.posts /
// m.postIdx / renders), clears the status line, and returns a Cmd that
// gap-fills any messages created after the newest cached post. Otherwise
// it clears m.posts, sets the loading status, and returns the standard
// fetchPosts Cmd. Callers tea.Batch the returned Cmd with any unrelated
// work (focus changes, stat bumps, etc.).
func (m *Model) openChannelLoadCmd(channelID string) tea.Cmd {
	if cached := m.loadFromStore(channelID); len(cached) > 0 {
		m.posts = cached
		m.postIdx = len(m.posts) - 1
		m.status = ""
		m.loading = false
		// Mirror postsLoadedMsg's badge-clearing so the sidebar doesn't
		// keep showing unread/mention counts on the channel the user
		// just opened while the gap-fill is in flight.
		delete(m.unread, channelID)
		delete(m.mentions, channelID)
		// If a search hit queued a jump to a specific post and it's in the
		// cached page, position the cursor on it before painting.
		m.jumpToPendingPost()
		m.renderMessages()
		return m.fetchPostsAfter(channelID, cached[len(cached)-1].Id)
	}
	m.posts = nil
	m.status = "loading messages…"
	m.renderMessages()
	return m.fetchPosts(channelID)
}

// tabAt resolves a 0-based tab index into its kind and (for teams) the
// team's ID + display name. Tab order is: DMs (if present), Unread,
// Feed, Search, teams in their loaded order.
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
	if i == 0 {
		return tabFeed, feedTeamID, "Feed"
	}
	i--
	if i == 0 {
		return tabSearch, searchTeamID, "Search"
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
	if m.lastActiveTeamID == unreadTeamID || m.lastActiveTeamID == searchTeamID || m.lastActiveTeamID == feedTeamID {
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

// teamName resolves a team id to its display name. DMs / group-DMs carry
// an empty TeamId; those (and ids we don't know) return "". Used by
// channelBreadcrumb to prefix a channel with its team.
func (m Model) teamName(teamID string) string {
	if teamID == "" {
		return ""
	}
	for _, t := range m.teams {
		if t.Id == teamID {
			return displayTeam(t)
		}
	}
	return ""
}

// channelBreadcrumb renders a channel with its team prefix, e.g.
// "Engineering › #general". Direct and group messages have no team, so
// they're prefixed with "DMs" ("DMs › @alice"). Falls back to the bare
// channel label when the team can't be resolved (e.g. a channel from a
// team the user has since left but whose posts are still cached).
func (m Model) channelBreadcrumb(c *model.Channel) string {
	label := m.channelLabel(c)
	switch c.Type {
	case model.ChannelTypeDirect, model.ChannelTypeGroup:
		return "DMs › " + label
	}
	if name := m.teamName(c.TeamId); name != "" {
		return name + " › " + label
	}
	return label
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

// applyTeamOrder sorts m.teams to match the user's saved team_order:
// listed teams first in their saved order, every other team appended
// alphabetically. A config entry matches a team by display name or by
// its URL name, case-insensitively, so a hand-edited config stays
// forgiving. The team tabs and the ",1".."9" / [N] shortcuts both follow
// m.teams order, so this is the single place that defines it.
func (m *Model) applyTeamOrder() {
	if len(m.teams) < 2 {
		return
	}
	rank := make(map[string]int, len(m.teamOrder))
	for i, name := range m.teamOrder {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, dup := rank[key]; !dup {
			rank[key] = i
		}
	}
	teamRank := func(t *model.Team) (int, bool) {
		if r, ok := rank[strings.ToLower(displayTeam(t))]; ok {
			return r, true
		}
		if r, ok := rank[strings.ToLower(t.Name)]; ok {
			return r, true
		}
		return 0, false
	}
	sort.SliceStable(m.teams, func(i, j int) bool {
		ri, oki := teamRank(m.teams[i])
		rj, okj := teamRank(m.teams[j])
		if oki != okj {
			return oki // listed teams sort before unlisted ones
		}
		if oki && ri != rj {
			return ri < rj
		}
		return strings.ToLower(displayTeam(m.teams[i])) < strings.ToLower(displayTeam(m.teams[j]))
	})
}

// firstTeamTabIdx returns the tab index of the leftmost real team, or -1
// if the user belongs to no teams. Real teams are the trailing tabs (after
// the synthetic DMs/Unread/Feed/Search tabs), so this is where the [N] /
// ",N" numbering begins.
func (m *Model) firstTeamTabIdx() int {
	for i := 0; i <= m.maxTeamIdx(); i++ {
		if kind, _, _ := m.tabAt(i); kind == tabTeam {
			return i
		}
	}
	return -1
}

// moveTeam swaps the team under the active tab with its neighbour `dir`
// steps away (dir is -1 for left, +1 for right) within m.teams, keeping
// that team selected. It returns true when a swap happened — i.e. the
// active tab is a team and there is a neighbouring team to swap with (it
// never moves a team into the synthetic-tab region). Callers persist the
// new order on a true result.
func (m *Model) moveTeam(dir int) bool {
	base := m.firstTeamTabIdx()
	if base < 0 {
		return false
	}
	pos := m.teamIdx - base
	if pos < 0 || pos >= len(m.teams) {
		return false // active tab is a synthetic tab, not a team
	}
	np := pos + dir
	if np < 0 || np >= len(m.teams) {
		return false
	}
	m.teams[pos], m.teams[np] = m.teams[np], m.teams[pos]
	m.teamIdx = base + np
	m.teamOrder = m.currentTeamOrder()
	return true
}

// currentTeamOrder snapshots m.teams' URL names in their current
// left-to-right order, the form persisted to config.yaml. The URL name is
// unique and stable, unlike the display name (which can collide or
// change); applyTeamOrder still accepts either when reading back.
func (m *Model) currentTeamOrder() []string {
	out := make([]string, 0, len(m.teams))
	for _, t := range m.teams {
		out = append(out, teamKey(t))
	}
	return out
}

// teamKey is the stable identifier persisted in team_order: the team's
// URL name, falling back to the display name if (unexpectedly) unset.
func teamKey(t *model.Team) string {
	if t.Name != "" {
		return t.Name
	}
	return displayTeam(t)
}

// persistTeamOrder writes the current team_order to config.yaml on a
// worker goroutine. Best-effort: a failed write only loses the reorder,
// so the error is swallowed (mirrors persistPosts).
func (m Model) persistTeamOrder() tea.Cmd {
	order := append([]string(nil), m.teamOrder...)
	return func() tea.Msg {
		_ = config.SaveTeamOrder(order)
		return nil
	}
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
