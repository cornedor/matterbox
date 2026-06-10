package ui

import (
	"context"
	"fmt"
	"os"
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
	"matterbox/internal/embed"
	"matterbox/internal/mm"
	"matterbox/internal/opener"
	"matterbox/internal/semindex"
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

	// openChannelID is the channel whose messages m.posts holds — i.e.
	// what the messages pane is actually showing. It is distinct from the
	// sidebar cursor (m.channelIdx), which navigation moves without
	// opening. Sending, the pane title, optimistic append and live updates
	// all key off this, not the cursor, so moving the selection while a
	// conversation is open never retargets the open panel.
	openChannelID string

	// markReadDelay is how long the open channel must stay open before it's
	// marked read (server + badges). Snapshotted from config at New(); 0 means
	// mark read immediately on open (the original behaviour).
	markReadDelay time.Duration
	// viewGen is bumped on every channel open. A scheduled mark-read tick
	// captures the generation it was queued under and only fires if it still
	// matches — so switching (or refocusing) before the dwell elapses drops the
	// stale tick, and reopening the same channel starts a fresh full dwell.
	viewGen int
	// viewSettled is true once the open channel's dwell has elapsed and it has
	// been marked read. While false, live posts arriving in the channel do not
	// mark it read early — the pending dwell tick covers them.
	viewSettled bool

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

	// anchorMsgSelBottom is the mirror of anchorMsgSelTop: the next
	// renderMessages call pins the selected post to the *bottom* of the
	// viewport. Used by the paths that page in / append newer posts and by
	// End/G, where the viewport offset would otherwise be stale after a
	// trimPostWindowHead drop shifted the content up. Cleared on each render.
	anchorMsgSelBottom bool

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

	// embedder is the background semantic-index loop: it embeds not-yet-
	// embedded cached messages (and new arrivals) into post_vectors while the
	// app runs. Optional — disabled when there's no store/embeddings config or
	// auto_index is off — and self-healing when the embeddings server is down.
	embedder embedderState

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

	// lastChannelByTeam maps a persistent team bucket (real team or the
	// synthetic DMs bucket) to the last channel opened there, so switching
	// to a team reopens that channel instead of the first one. Persisted to
	// channel_stats.json alongside lastActive; synthetic tabs are excluded.
	lastChannelByTeam map[string]string

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
	emoji   emojiState

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

	// Semantic-search indexing. embedClient talks to the embeddings server;
	// embedModel/embedDim identify the model (and Matryoshka truncation) the
	// vectors are written under; embedBatch is the per-request post count. All
	// snapshotted from config at New() time. The embedder loop (embedindex.go)
	// builds a semindex.Indexer from these per batch; step-4 search reuses
	// embedClient to embed the query.
	embedClient *embed.Client
	embedModel  string
	embedDim    int
	embedBatch  int

	// Reaction-picker modal. While reactionPickerPostID is non-empty the
	// modal owns every keystroke (digit selects + fires, ↑/↓+↵ navigate,
	// esc cancels). reactionPickerIdx is the cursor position within
	// reactionEmojis.
	reactionPickerPostID string
	reactionPickerIdx    int

	// Open-target picker modal. When a post has more than one openable
	// target (attachments + links), `o` raises this list instead of
	// guessing. While openPickerItems is non-empty the modal owns every
	// keystroke (digit opens + fires, ↑/↓+↵ navigate, esc cancels);
	// openPickerIdx is the cursor within openPickerItems.
	openPickerItems []openable
	openPickerIdx   int

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

	stats, la, lastByTeam := loadChannelStats()

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
	var embedClient *embed.Client
	var embedModel string
	var embedDim int
	embedAuto := false
	markReadDelay := defaultMarkReadDelay
	navModifier := "ctrl"
	if cfg != nil {
		switch {
		case cfg.Keybindings.NavModifier != "":
			navModifier = cfg.Keybindings.NavModifier
		case cfg.Keybindings.CtrlArrowNav != nil && !*cfg.Keybindings.CtrlArrowNav:
			// Honour a pre-NavModifier config that hasn't been migrated yet.
			navModifier = "none"
		}
		reactions = append([]string(nil), cfg.Reactions...)
		teamOrder = append([]string(nil), cfg.TeamOrder...)
		summaryEndpoint = cfg.Summary.Endpoint
		summaryAPIKey = cfg.Summary.APIKey
		summaryModel = cfg.Summary.Model
		summaryPrompt = cfg.Summary.Prompt
		aiSearchPrompt = cfg.AISearch.Prompt
		aiSearchMaxSteps = cfg.AISearch.MaxSteps
		aiSearchTimeout = time.Duration(cfg.AISearch.TimeoutMinutes) * time.Minute
		if ec := cfg.Embeddings; ec.Endpoint != "" && ec.Model != "" {
			embedClient = embed.New(ec.Endpoint, ec.APIKey, ec.Model, ec.Dim)
			embedModel = ec.Model
			embedDim = ec.Dim
			embedAuto = ec.AutoIndex == nil || *ec.AutoIndex
		}
		if cfg.MarkReadDelaySeconds != nil {
			markReadDelay = time.Duration(*cfg.MarkReadDelaySeconds) * time.Second
		}
	}
	// Unless the sidebar nav uses the ctrl modifier itself, ctrl+←/→ never
	// reach the global dispatch, so let them word-jump in the composer (the
	// textarea otherwise only binds alt+←/→ for that — ctrl+arrows would do
	// nothing). Keep the alt+ defaults too.
	if prefix, _ := navMod(navModifier); prefix != "ctrl+" {
		ta.KeyMap.WordBackward = key.NewBinding(
			key.WithKeys("alt+left", "alt+b", "ctrl+left"),
			key.WithHelp("ctrl+←", "word backward"),
		)
		ta.KeyMap.WordForward = key.NewBinding(
			key.WithKeys("alt+right", "alt+f", "ctrl+right"),
			key.WithHelp("ctrl+→", "word forward"),
		)
	}
	// The background indexer runs only when there's a store to write to, a
	// client to call, and the user hasn't disabled it. A down server is fine —
	// the loop just backs off (see embedindex.go).
	embedderEnabled := st != nil && embedClient != nil && embedAuto

	return Model{
		client:              client,
		ctx:                 context.Background(),
		channels:            map[string][]*model.Channel{},
		userNames:           map[string]string{},
		focus:               focusMessages,
		msgsView:            msgsView,
		threadView:          threadView,
		historyView:         historyView,
		filter:              ti,
		switcher:            sw,
		openStats:           stats,
		store:               st,
		lastActiveTeamID:    la.teamID(),
		lastActiveChannelID: la.channelID(),
		lastChannelByTeam:   lastByTeam,
		input:               ta,
		unread:              map[string]int{},
		mentions:            map[string]int{},
		uploadCancel:        map[string]context.CancelFunc{},
		loading:             true,
		status:              "loading…",
		keys:                newKeyMap(navModifier),
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
		embedClient:         embedClient,
		embedModel:          embedModel,
		embedDim:            embedDim,
		embedBatch:          semindex.DefaultBatch,
		embedder:            embedderState{enabled: embedderEnabled},
		markReadDelay:       markReadDelay,
	}
}

// defaultMarkReadDelay mirrors config.defaultMarkReadDelaySeconds and is the
// fallback dwell used when no config is supplied (e.g. in tests).
const defaultMarkReadDelay = 5 * time.Second

// leaderHints are the synthetic bindings shown in the footer while the
// "," leader chord is pending, so the user sees the available jump
// targets before pressing the second key. The keys are display-only
// (they're never matched against — handleLeaderKey owns the resolution).
func leaderHints() []key.Binding {
	nb := func(k, h string) key.Binding {
		return key.NewBinding(key.WithKeys(k), key.WithHelp(k, h))
	}
	return []key.Binding{
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
	case m.focus == focusMessages:
		return []key.Binding{k.Tab, k.Up, k.Down, k.NavChanNext, k.NavTeamNext, k.Compose, k.OpenThread, k.ReplyInThread, k.SearchHere, k.Filter, k.NextHit, k.PrevHit, k.OpenAttach, k.CopyMD, k.EditPost, k.DeletePost, k.React, k.ShowHistory, k.Leader, k.Switcher, k.Unread, k.Feed, k.Help, k.Quit}
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
		{k.NavChanPrev, k.NavChanNext, k.NavTeamPrev, k.NavTeamNext},
		{k.Filter, k.ClearFilter, k.OpenChannel, k.OpenThread, k.ReplyInThread, k.CloseThread},
		{k.OpenAttach, k.CopyMD, k.EditPost, k.DeletePost, k.React, k.ShowHistory, k.Compose, k.Send, k.NewLine, k.LeaveInput},
		{k.Paste, k.AttachRemove},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchMe(), m.connectWS(), m.startEmbedder())
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

// scheduleMarkViewed defers marking channelID read until it has been open
// for m.markReadDelay, so a quick peek doesn't clear unread. It returns a
// tick carrying the current viewGen; the markViewedMsg handler only acts if
// that generation (and the open channel) still match when it fires. A
// non-positive delay means "immediately" — clear the badges now and mark
// read without a tick (the original behaviour, opt-in via config 0).
func (m *Model) scheduleMarkViewed(channelID string) tea.Cmd {
	if m.markReadDelay <= 0 {
		delete(m.unread, channelID)
		delete(m.mentions, channelID)
		m.viewSettled = true
		return m.markChannelViewed(channelID)
	}
	gen := m.viewGen
	return tea.Tick(m.markReadDelay, func(time.Time) tea.Msg {
		return markViewedMsg{channelID: channelID, gen: gen}
	})
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
		// Use the *root* counters. This server (like modern Mattermost
		// defaults) runs collapsed reply threads, which freezes the legacy
		// non-root TotalMsgCount/MsgCount — they track each other and so
		// TotalMsgCount-MsgCount stays ~0 even with genuine unread, hiding
		// every channel. The root counters are the live ones the Mattermost
		// sidebar itself uses, so they match what the user sees there.
		unread := ch.TotalMsgCountRoot - mb.MsgCountRoot
		if unread > 0 {
			m.unread[mb.ChannelId] = int(unread)
		} else {
			delete(m.unread, mb.ChannelId)
		}
		if mb.MentionCountRoot > 0 {
			m.mentions[mb.ChannelId] = int(mb.MentionCountRoot)
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
// either an uploaded file (download to cache, then open the local path
// via the OS handler) or a direct URL (open it as-is, letting the
// desktop dispatch to the browser).
type openable struct {
	name string
	file *model.FileInfo // download via API and open locally
	url  string          // hand directly to the OS default handler
}

// collectOpenables enumerates everything in a post that `o` can act on:
// uploaded attachments first (in metadata order), then inline
// ![alt](url) images, [text](url) links, and bare URLs (in message
// order). URLs are deduped by target so an image and the link it
// desugars to — or a bare URL repeated as a markdown link — only show
// up once.
func collectOpenables(p *model.Post) []openable {
	var out []openable
	seen := map[string]bool{}
	add := func(name, url string) {
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		if name == "" {
			name = url
		}
		out = append(out, openable{name: name, url: url})
	}
	if p.Metadata != nil {
		for _, f := range p.Metadata.Files {
			out = append(out, openable{name: f.Name, file: f})
		}
	}
	// Inline images: ![alt](url).
	for _, sub := range mdImageRe.FindAllStringSubmatch(p.Message, -1) {
		add(sub[1], sub[2])
	}
	// Markdown links: [text](url). The image form ![alt](url) also matches
	// here (the leading "!" isn't part of the pattern), but its target is
	// already in `seen`, so the duplicate is dropped.
	for _, sub := range mdLinkRe.FindAllStringSubmatch(p.Message, -1) {
		add(sub[1], sub[2])
	}
	// Bare URLs not already captured as an image/link target. Trailing
	// sentence punctuation (and markdown's closing paren) is trimmed so we
	// open the URL, not the URL plus a stray ")".
	for _, raw := range mdURLRe.FindAllString(p.Message, -1) {
		clean, _ := trimTrailingURLPunct(raw)
		add("", clean)
	}
	return out
}

// openOpenable opens either a downloaded file or a URL via the OS handler.
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
		// The launcher forks and returns immediately; we don't wait for the
		// viewer process to exit.
		if err := opener.Open(target); err != nil {
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

// orderOldestFirst flips a PostList's newest-first Order into an
// oldest-first slice of posts, skipping any id missing from pl.Posts.
func orderOldestFirst(pl *model.PostList) []*model.Post {
	ordered := make([]*model.Post, 0, len(pl.Order))
	for i := len(pl.Order) - 1; i >= 0; i-- {
		if p, ok := pl.Posts[pl.Order[i]]; ok {
			ordered = append(ordered, p)
		}
	}
	return ordered
}

// resolveSenderNames fetches usernames for any post authors in `posts` not
// already known to m.userNames, returning them keyed userID → username.
// Shared by the post-fetch commands so each doesn't re-implement the same
// "collect unknown ids → look them up" dance. Returns an empty (non-nil)
// map when every author is already cached.
func (m Model) resolveSenderNames(posts []*model.Post) (map[string]string, error) {
	need := map[string]struct{}{}
	for _, p := range posts {
		if _, have := m.userNames[p.UserId]; !have {
			need[p.UserId] = struct{}{}
		}
	}
	users := map[string]string{}
	if len(need) == 0 {
		return users, nil
	}
	ids := make([]string, 0, len(need))
	for id := range need {
		ids = append(ids, id)
	}
	us, err := m.client.UsersByIDs(m.ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, u := range us {
		users[u.Id] = u.Username
	}
	return users, nil
}

func (m Model) fetchPosts(channelID string) tea.Cmd {
	return func() tea.Msg {
		pl, err := m.client.Posts(m.ctx, channelID, 60)
		if err != nil {
			return errMsg{err}
		}
		ordered := orderOldestFirst(pl)
		users, err := m.resolveSenderNames(ordered)
		if err != nil {
			return errMsg{err}
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

// maxLoadedPosts bounds how many posts renderMessages keeps live at once.
// Paging past either end of the rendered window (the Up-at-top /
// Down-at-bottom handlers) loads olderPageSize more from the cache and
// trims the same count off the far end, so the window slides instead of
// growing for the whole session. renderMessages is O(loaded posts), so
// capping the slice keeps per-keystroke cost flat no matter how far back
// the user reads; trimmed posts stay in the SQLite store and page back in
// when the user reverses direction.
const maxLoadedPosts = 400

// trimPostWindowTail drops posts off the newest end of m.posts once the
// loaded window exceeds maxLoadedPosts. Called after prepending older
// posts (scrolling up), where the selection sits near the top so the
// trimmed tail is safely off-screen. Never drops the selected post or an
// unpersisted optimistic stub (empty Id) — those can't be recovered from
// the store.
func (m *Model) trimPostWindowTail() {
	for len(m.posts) > maxLoadedPosts {
		last := len(m.posts) - 1
		if last <= m.postIdx || m.posts[last].Id == "" {
			return
		}
		m.posts = m.posts[:last]
	}
}

// trimPostWindowHead drops posts off the oldest end of m.posts once the
// loaded window exceeds maxLoadedPosts, shifting postIdx to keep the
// selection pinned to the same post. Called after appending newer posts
// (scrolling down), where the selection sits near the bottom so the
// trimmed head is safely off-screen.
func (m *Model) trimPostWindowHead() {
	drop := len(m.posts) - maxLoadedPosts
	if drop > m.postIdx {
		drop = m.postIdx // never drop the selected post or past it
	}
	if drop <= 0 {
		return
	}
	// Copy into a fresh slice so the trimmed prefix's backing storage is
	// released rather than retained behind the re-slice.
	m.posts = append([]*model.Post(nil), m.posts[drop:]...)
	m.postIdx -= drop
}

// mergePostsByTime unions two create_at-ascending post slices into one
// ascending slice, deduping by Id. On an Id collision the post from
// `incoming` wins — it's the fresher server copy, carrying any edits or
// reaction/metadata updates. Optimistic stubs (empty Id, an own-send not
// yet confirmed) in `existing` can't be matched by Id, so they're kept at
// the end after every real post, preserving their original order. Unlike a
// plain append this can insert `incoming` posts *between* existing ones,
// which is what heals an interior cache gap when the recent window is
// reconciled with the server on channel open.
func mergePostsByTime(existing, incoming []*model.Post) []*model.Post {
	byID := make(map[string]*model.Post, len(existing)+len(incoming))
	var stubs []*model.Post
	for _, p := range existing {
		if p == nil {
			continue
		}
		if p.Id == "" {
			stubs = append(stubs, p)
			continue
		}
		byID[p.Id] = p
	}
	for _, p := range incoming {
		if p == nil || p.Id == "" {
			continue
		}
		byID[p.Id] = p
	}
	merged := make([]*model.Post, 0, len(byID)+len(stubs))
	for _, p := range byID {
		merged = append(merged, p)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].CreateAt != merged[j].CreateAt {
			return merged[i].CreateAt < merged[j].CreateAt
		}
		return merged[i].Id < merged[j].Id
	})
	return append(merged, stubs...)
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
		ordered := orderOldestFirst(pl)
		users, err := m.resolveSenderNames(ordered)
		if err != nil {
			return errMsg{err}
		}
		return postsGapFilledMsg{channelID: channelID, posts: ordered, users: users}
	}
}

// reconcilePageSize is how many of the channel's most recent posts the
// warm-open path pulls from the server to reconcile against the cache.
// Anchored at "now" and walking backward, this page is authoritative for
// the recent window regardless of cache state — the property a forward
// PostsAfter(newestCached) lacks once the cache has an interior gap. 200
// matches the old gap-fill's page size, so the per-open fetch ceiling is
// unchanged; it covers a multi-day absence in a typical channel, and any
// deeper gap surfaces when the user scrolls back.
const reconcilePageSize = 200

// fetchRecent pulls the channel's most recent page from the server and
// returns it as a postsGapFilledMsg so the open channel's recent window is
// reconciled (not just appended to) — see the postsGapFilledMsg handler's
// merge. Used on warm open in place of fetchPostsAfter: because it's
// anchored at the newest server post rather than the newest *cached* one,
// it surfaces posts hidden beneath a stale cache high-water mark.
func (m Model) fetchRecent(channelID string) tea.Cmd {
	return func() tea.Msg {
		pl, err := m.client.Posts(m.ctx, channelID, reconcilePageSize)
		if err != nil {
			return errMsg{err}
		}
		ordered := orderOldestFirst(pl)
		users, err := m.resolveSenderNames(ordered)
		if err != nil {
			return errMsg{err}
		}
		return postsGapFilledMsg{channelID: channelID, posts: ordered, users: users}
	}
}

// historyPageSize is how many posts the on-demand history pagers
// (fetchOlder / fetchNewer) pull per server round-trip when the user
// scrolls past the loaded window. Matches olderPageSize so a server page
// and a cache page advance the view by the same amount.
const historyPageSize = olderPageSize

// fetchOlder pulls the page of posts immediately older than beforePostID
// straight from the server and returns it as an olderPostsMsg. Used when
// the user scrolls past the top of the loaded window: unlike the cache
// pager (BeforeInChannel) it can cross an interior hole the cache would
// silently skip, and it keeps working past the oldest cached post into
// history the cache never held. atChannelStart (PrevPostId == "") reports
// the genuine start of the channel, as opposed to merely the cache floor.
func (m Model) fetchOlder(channelID, beforePostID string) tea.Cmd {
	return func() tea.Msg {
		pl, err := m.client.PostsBefore(m.ctx, channelID, beforePostID, historyPageSize)
		if err != nil {
			return errMsg{err}
		}
		ordered := orderOldestFirst(pl)
		users, err := m.resolveSenderNames(ordered)
		if err != nil {
			return errMsg{err}
		}
		return olderPostsMsg{
			channelID:      channelID,
			posts:          ordered,
			users:          users,
			atChannelStart: pl.PrevPostId == "",
		}
	}
}

// fetchNewer is the forward mirror of fetchOlder: it pulls the page of
// posts immediately newer than afterPostID from the server. Used when the
// user scrolls past the bottom of the loaded window (e.g. reading forward
// from a search hit centred on an old post) so a hole between the loaded
// tail and the live tail gets crossed rather than jumped. atChannelEnd
// (NextPostId == "") reports that the page reaches the channel's newest
// post.
func (m Model) fetchNewer(channelID, afterPostID string) tea.Cmd {
	return func() tea.Msg {
		pl, err := m.client.PostsAfter(m.ctx, channelID, afterPostID, historyPageSize)
		if err != nil {
			return errMsg{err}
		}
		ordered := orderOldestFirst(pl)
		users, err := m.resolveSenderNames(ordered)
		if err != nil {
			return errMsg{err}
		}
		return newerPostsMsg{
			channelID:    channelID,
			posts:        ordered,
			users:        users,
			atChannelEnd: pl.NextPostId == "",
		}
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
	m.openChannelID = channelID
	// New focus session: start a fresh dwell. The badges are intentionally
	// left intact until the dwell elapses (see scheduleMarkViewed), so a
	// quick peek doesn't clear unread.
	m.viewGen++
	m.viewSettled = false
	if cached := m.loadFromStore(channelID); len(cached) > 0 {
		m.posts = cached
		m.postIdx = len(m.posts) - 1
		m.status = ""
		m.loading = false
		// If a search hit queued a jump to a specific post and it's in the
		// cached page, position the cursor on it before painting.
		m.jumpToPendingPost()
		m.renderMessages()
		// Reconcile the recent window against the server rather than only
		// fetching posts *after* the newest cached one. The cache is not
		// guaranteed contiguous: a message posted while matterbox was
		// offline, followed by a newer message caught live over WebSocket,
		// leaves an interior gap *below* the newest cached post — which a
		// forward-only PostsAfter(newestCached) can never see. fetchRecent
		// pulls the channel's latest page (anchored at "now") so the merge
		// fills any such hole. See fetchRecent / the postsGapFilledMsg merge.
		return m.fetchRecent(channelID)
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

// isRestorableTeamID reports whether a team ID names a persistent bucket
// whose last-open channel is worth remembering and restoring. The synthetic
// Unread/Search/Feed tabs are computed on the fly, so they're excluded; real
// teams and the DMs bucket qualify.
func isRestorableTeamID(id string) bool {
	switch id {
	case "", unreadTeamID, searchTeamID, feedTeamID:
		return false
	}
	return true
}

// preferredChannelIdx returns the index in vis of the channel last opened in
// the focused team (from lastChannelByTeam), so switching to a team reopens
// where the user left off. Falls back to 0 when there's no remembered
// channel or it's no longer in the visible list.
func (m Model) preferredChannelIdx(vis []*model.Channel) int {
	id := m.lastChannelByTeam[m.currentTeamID()]
	if id == "" {
		return 0
	}
	for i, c := range vis {
		if c.Id == id {
			return i
		}
	}
	return 0
}

// restoreLastActive attempts to set teamIdx and channelIdx from a
// previous session's persisted state. If the saved team or channel no
// longer exists, the indices are left unchanged and the caller's existing
// clamping logic kicks in. Does nothing for the synthetic Unread tab.
func (m *Model) restoreLastActive() {
	if m.lastActiveChannelID == "" || !isRestorableTeamID(m.lastActiveTeamID) {
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
		if k == dmTeamID {
			// DMs sort by most recently chatted in, newest first;
			// fall back to label for channels with no posts yet.
			sort.SliceStable(list, func(i, j int) bool {
				ai, aj := list[i].LastPostAt, list[j].LastPostAt
				if ai != aj {
					return ai > aj
				}
				return strings.ToLower(m.channelLabel(list[i])) < strings.ToLower(m.channelLabel(list[j]))
			})
		} else {
			sort.SliceStable(list, func(i, j int) bool {
				return strings.ToLower(m.channelLabel(list[i])) < strings.ToLower(m.channelLabel(list[j]))
			})
		}
		m.channels[k] = list
	}
	_, m.hasDMs = m.channels[dmTeamID]
}

// sortDMBucket re-orders the DM bucket by most recent activity (newest
// first), matching bucketChannels. Called when a live post bumps a DM's
// LastPostAt so the sidebar stays "recently chatted in" within a session.
func (m *Model) sortDMBucket() {
	list := m.channels[dmTeamID]
	if len(list) < 2 {
		return
	}
	sort.SliceStable(list, func(i, j int) bool {
		ai, aj := list[i].LastPostAt, list[j].LastPostAt
		if ai != aj {
			return ai > aj
		}
		return strings.ToLower(m.channelLabel(list[i])) < strings.ToLower(m.channelLabel(list[j]))
	})
}

// touchChannelActivity bumps a channel's LastPostAt to at least ts and, if
// it's a DM/group, re-sorts the DM bucket so the sidebar reflects the most
// recent conversation. No-op if the channel isn't known locally.
func (m *Model) touchChannelActivity(channelID string, ts int64) {
	c := m.findChannel(channelID)
	if c == nil || ts <= c.LastPostAt {
		return
	}
	c.LastPostAt = ts
	if c.Type != model.ChannelTypeDirect && c.Type != model.ChannelTypeGroup && c.TeamId != "" {
		return
	}
	// Re-sorting shifts what sits under the positional cursor, so pin the
	// cursor to the channel it currently points at across the sort.
	var cursorID string
	if vis := m.visibleChannels(); m.channelIdx >= 0 && m.channelIdx < len(vis) {
		cursorID = vis[m.channelIdx].Id
	}
	m.sortDMBucket()
	if cursorID != "" {
		for i, ch := range m.visibleChannels() {
			if ch.Id == cursorID {
				m.channelIdx = i
				break
			}
		}
	}
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
