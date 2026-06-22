// Package listen implements the `matterbox listen` background daemon: a single
// long-lived WebSocket connection that keeps the local message cache warm and
// fires callbacks on interesting events. The first callback is a
// summarize-on-direct-mention bridge to Telegram.
//
// It deliberately does NOT touch the TUI: it reuses the lower layers (mm, store,
// auth, config) and runs its own connection, so running the daemon and the TUI
// at the same time is safe — both write the same idempotent upserts into the
// WAL-mode store. Sharing one connection (TUI as a thin client of the daemon)
// is a future step; see the design notes in the commit history.
package listen

import (
	"context"
	"fmt"
	"log"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/aisearch"
	"matterbox/internal/chat"
	"matterbox/internal/embed"
	"matterbox/internal/mm"
	"matterbox/internal/store"
	"matterbox/internal/telegram"
)

// defaultContextPosts is how many recent channel posts are summarized for a
// channel @mention (a thread mention summarizes the whole thread instead).
const defaultContextPosts = 20

// logBodyCap bounds how much message body is echoed to the log when no Telegram
// delivery is configured, so a long paste doesn't flood journald.
const logBodyCap = 200

// pollTimeoutSec is the Telegram long-poll timeout for getUpdates.
const pollTimeoutSec = 30

// Options tunes the daemon's behaviour. Built from config in the cli layer.
type Options struct {
	// ServerURL is shown in the startup log line only.
	ServerURL string
	// NotifyOnMention enables the mention/DM → Telegram bridge. When false the
	// daemon only keeps the cache warm.
	NotifyOnMention bool
	// Summarize controls whether the notification is an LLM summary of the
	// surrounding context (true) or just the raw message text (false). Summaries
	// fall back to raw text automatically when the chat server is unreachable.
	Summarize bool
	// NotifyPrompt is the system prompt for the summary.
	NotifyPrompt string
	// TelegramChatID is the destination chat for notifications.
	TelegramChatID string
	// ContextPosts overrides defaultContextPosts when > 0.
	ContextPosts int
	// NotifySelf also notifies on the reader's own messages. Off by default
	// (you don't want pings for what you just sent); handy for testing the
	// bridge by posting in your own self-DM.
	NotifySelf bool
	// NotifyDMs controls whether direct-message channels trigger
	// notifications. When false, only channel @mentions fire. Default false.
	NotifyDMs bool
	// NotifyDelaySeconds is how long to wait before delivering a notification.
	// After the delay the daemon fetches the channel-member record from the
	// Mattermost server and skips the notification if LastViewedAt ≥ the
	// post's timestamp — i.e. any client (TUI, mobile, web) marked it read.
	// 0 delivers immediately with no read-check. Default 60.
	NotifyDelaySeconds int
	// RespectMutes skips notifications for channels muted in Mattermost.
	RespectMutes bool
	// QuietHours is the raw "HH:MM-HH:MM" suppression window (local); empty =
	// always on. Parsed once in New.
	QuietHours string
	// TwoWay enables the inbound Telegram channel (replies, buttons, commands).
	// Requires TelegramChatID, the only sender the bot obeys.
	TwoWay bool

	// Rules are the compiled per-post rules (from the `rules:` config block).
	// When empty the daemon synthesises a default rule from the Notify*
	// options that reproduces the legacy mention/DM → Telegram bridge, so an
	// existing config behaves exactly as before. See rules.go.
	Rules []Rule

	// /ask agentic search. AskEndpoint+AskModel come from the summary chat
	// server (set regardless of Summarize); empty disables /ask. AskPrompt frames
	// the agent, AskMaxSteps bounds its tool-call rounds, AskTimeout bounds the
	// whole run. EmbedClient (+EmbedModel/EmbedDim) powers semantic/hybrid modes;
	// nil makes them fall back to keyword.
	AskEndpoint string
	AskAPIKey   string
	AskModel    string
	AskPrompt   string
	AskMaxSteps int
	AskTimeout  time.Duration
	EmbedClient *embed.Client
	EmbedModel  string
	EmbedDim    int
}

// Engine owns the daemon's connection lifecycle and event handling. Construct
// with New and drive with Run.
type Engine struct {
	client *mm.Client
	store  *store.Store
	chat   *chat.Client     // nil → never summarize (send raw text)
	tg     *telegram.Client // nil → log only, no delivery
	me     *model.User
	opts   Options
	log    *log.Logger

	// rules drives the reaction to each incoming post. Either the user's
	// configured rules (opts.Rules) or, when they configured none, the
	// synthesised default that reproduces the legacy mention/DM → Telegram
	// bridge (see defaultRules).
	rules []Rule

	// freqWindows backs the rules' frequency gate: a sliding window of recent
	// match times per (rule, group) key (see frequencyAllows). In-memory and
	// live-only — cleared on restart, never touched by catch-up.
	freqMu      sync.Mutex
	freqWindows map[string][]time.Time

	// usesState caches whether any rule matches on the ledger, so a config that
	// never does pays no per-message state read (see matchState).
	usesState bool

	// now is the engine's clock, overridable in tests so the frequency window
	// can be driven deterministically. nil means time.Now (see clock).
	now func() time.Time

	wg sync.WaitGroup // tracks in-flight notify + inbound goroutines for shutdown

	// quiet-hours window (minutes-of-day), parsed from opts.QuietHours in New.
	quietStart, quietEnd int
	quietOn              bool

	mutedMu sync.RWMutex
	muted   map[string]bool // channels muted in Mattermost (RespectMutes)

	// lastSeenMs is the catch-up cursor: mentions at or before it have already
	// been notified. Persisted in the store (key cursorKey) so a restart neither
	// loses nor replays missed mentions.
	seenMu     sync.Mutex
	lastSeenMs int64

	// teams maps team id → URL name for building post permalinks; defaultTeam is
	// the fallback when a post carries no team (DMs). Loaded once at startup.
	teamsMu     sync.RWMutex
	teams       map[string]string
	defaultTeam string

	// sendChan caches resolved channel ids for `send` actions that target a
	// configured channel (team/channel or @user), so such a rule resolves the
	// spec once rather than per matching post. Channel ids survive renames.
	sendChanMu sync.Mutex
	sendChan   map[string]string

	// askCatalog caches the channel/team/user snapshot for /ask, rebuilt after
	// askCatalogTTL so newly-joined channels eventually appear.
	askMu        sync.Mutex
	askCatalog   aisearch.Catalog
	askCatalogAt time.Time
	askReady     bool

	// convos remembers recent /ask transcripts keyed by the answer's Telegram
	// message id, so replying to an answer continues that conversation. In-memory
	// and capped (convoIDs is the insertion order for eviction); a restart drops
	// them, after which a reply falls through to the thread-reply path.
	convoMu  sync.Mutex
	convos   map[int][]aisearch.Message
	convoIDs []int
}

// askCatalogTTL bounds how long the /ask channel/team/user snapshot is reused
// before a rebuild picks up new channels.
const askCatalogTTL = 10 * time.Minute

// askConvoCap is how many recent /ask conversations are remembered for
// follow-ups; older ones are evicted.
const askConvoCap = 50

// askAuthorCap bounds how many distinct message authors are resolved to
// usernames for the /ask catalog (best-effort citation quality without an
// org-wide user fetch).
const askAuthorCap = 4000

// askProgressInterval throttles how often the live "searching…" placeholder is
// edited with the current step, to stay well under Telegram's edit rate limit.
const askProgressInterval = 2 * time.Second

// cursorKey is the meta-table key for the catch-up cursor.
const cursorKey = "listen.last_seen_ms"

// catchupMaxAge bounds how far back catch-up looks, so a daemon that was off for
// weeks doesn't surface a flood of stale mentions on its first reconnect.
const catchupMaxAge = 7 * 24 * time.Hour

// maxPhotoBytes is the largest image forwarded via Telegram sendPhoto.
const maxPhotoBytes = 10 << 20

// maxInboundBytes caps a file pulled from a Telegram reply and re-uploaded to
// Mattermost. Telegram's Bot API won't serve downloads over 20 MB anyway.
const maxInboundBytes = 20 << 20

// captionCap is Telegram's photo-caption length limit.
const captionCap = 1024

// notifTarget links a sent notification to the Mattermost message it's about,
// so a Telegram reply posts into the right thread and a Telegram reaction lands
// on the right post.
type notifTarget struct {
	channelID string
	rootID    string
	postID    string
}

// New builds an Engine. chat and tg may be nil (summarization / delivery are
// then skipped). me must be non-nil for mention detection to run.
func New(client *mm.Client, st *store.Store, ch *chat.Client, tg *telegram.Client, me *model.User, opts Options, logger *log.Logger) *Engine {
	if opts.ContextPosts <= 0 {
		opts.ContextPosts = defaultContextPosts
	}
	if logger == nil {
		logger = log.Default()
	}
	e := &Engine{
		client: client, store: st, chat: ch, tg: tg, me: me, opts: opts, log: logger,
		muted:       map[string]bool{},
		teams:       map[string]string{},
		freqWindows: map[string][]time.Time{},
	}
	if start, end, ok := parseQuietHours(opts.QuietHours); ok {
		e.quietStart, e.quietEnd, e.quietOn = start, end, true
	}
	if len(opts.Rules) > 0 {
		e.rules = opts.Rules
	} else {
		e.rules = defaultRules(opts)
	}
	e.usesState = rulesUseState(e.rules)
	return e
}

// clock returns the current time through the engine's overridable now hook,
// defaulting to time.Now. Tests set e.now to drive the frequency window.
func (e *Engine) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// Run connects, consumes events, and reconnects with exponential backoff until
// ctx is cancelled. It returns ctx.Err() once all in-flight notifications have
// drained, so a caller wiring it to SIGINT/SIGTERM gets a clean shutdown.
func (e *Engine) Run(ctx context.Context) error {
	e.loadCursor()      // catch-up watermark (set to now on first ever run)
	e.refreshTeams(ctx) // team names for permalinks
	e.refreshMuted(ctx) // best-effort initial mute set, before events flow
	if e.inboundEnabled() {
		e.log.Printf("two-way enabled: replies + commands accepted from chat %s", e.opts.TelegramChatID)
		e.wg.Add(1)
		go e.pollUpdates(ctx)
	}
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			e.wg.Wait()
			return err
		}
		wsc, err := e.client.DialWS()
		if err != nil {
			// An expired/revoked token never recovers by retrying — alert and
			// exit cleanly so the supervisor doesn't restart-loop forever.
			if IsUnauthorized(err) {
				e.alertTokenExpired(ctx)
				e.wg.Wait()
				return nil
			}
			attempt++
			d := backoff(attempt)
			e.log.Printf("websocket dial failed (retry in %s): %v", d, err)
			if !sleepCtx(ctx, d) {
				e.wg.Wait()
				return ctx.Err()
			}
			continue
		}
		attempt = 0
		e.log.Printf("connected (%s)", e.opts.ServerURL)
		go e.refreshMuted(ctx) // pick up mute changes made while we were away
		e.catchUp(ctx)         // notify mentions that arrived while disconnected
		e.consume(ctx, wsc)
		wsc.Close()
		if err := ctx.Err(); err != nil {
			e.wg.Wait()
			return err
		}
		attempt++
		d := backoff(attempt)
		e.log.Printf("websocket disconnected, reconnecting in %s", d)
		if !sleepCtx(ctx, d) {
			e.wg.Wait()
			return ctx.Err()
		}
	}
}

// consume drains the event channel until it closes (disconnect) or ctx is
// cancelled. It also watches PingTimeoutChannel: a silent half-open drop (no
// FIN/RST reaches us) never errors the reader's deadline-less NextReader, so
// EventChannel would never close and we'd sit on a dead socket forever. The
// client's ping watchdog signals such a death (~65s with no server ping);
// returning hands control back to Run, which Closes the dead socket (breaking
// the stuck reader) and reconnects.
func (e *Engine) consume(ctx context.Context, wsc *model.WebSocketClient) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-wsc.PingTimeoutChannel:
			e.log.Printf("websocket ping timeout (no server ping ~65s); treating as disconnect")
			return
		case ev, ok := <-wsc.EventChannel:
			if !ok {
				return
			}
			e.handle(ctx, ev)
		}
	}
}

// handle ingests posts into the store and fires the mention callback. It runs
// on the single consume goroutine, so persistence stays ordered; the (possibly
// slow) summarize+deliver work is spun off so it never blocks ingestion.
func (e *Engine) handle(ctx context.Context, ev *model.WebSocketEvent) {
	switch ev.EventType() {
	case model.WebsocketEventPosted:
		p := postFromEvent(ev)
		if p == nil {
			return
		}
		e.ingest(p)
		// Everything that reacts to a new post — the Telegram notification
		// included — flows through the rules engine. The notification is just
		// the default rule (notifyGate applies the do-not-disturb policy that
		// used to live inline here); user-configured rules replace it.
		e.applyRules(ctx, ev, p)
	case model.WebsocketEventPostEdited:
		if p := postFromEvent(ev); p != nil {
			e.ingest(p)
		}
	case model.WebsocketEventPostDeleted:
		if p := postFromEvent(ev); p != nil {
			// Soft-delete so the shared cache treats deletions uniformly with
			// the TUI (set delete_at, keep the row); read paths filter it out.
			if err := e.store.MarkDeleted(p.Id, p.DeleteAt); err != nil {
				e.log.Printf("delete post %s: %v", p.Id, err)
			}
		}
	}
}

// ingest upserts one post into the cache. Errors are logged, not fatal: a single
// bad write shouldn't take the daemon down.
func (e *Engine) ingest(p *model.Post) {
	if err := e.store.UpsertMany([]*model.Post{p}); err != nil {
		e.log.Printf("persist post %s: %v", p.Id, err)
	}
}

// notify builds and delivers a notification for a direct mention / DM. Runs in
// its own goroutine; respects ctx (cancelled on shutdown). summarize selects
// whether the body is an LLM summary of the surrounding context or the raw
// message text — resolved per notify action by the caller (notifyGate).
func (e *Engine) notify(ctx context.Context, ev *model.WebSocketEvent, p *model.Post, opts notifyOpts) {
	defer e.wg.Done()
	summarize := opts.summarize
	chatID := opts.chatID
	// Two-way reply buttons only work for the configured chat (the only sender
	// the bot obeys), so a rule routing to a different chat gets a plain message.
	twoWay := e.inboundEnabled() && chatID == e.opts.TelegramChatID

	// Delay-then-read-check: wait the configured window, then query the
	// Mattermost server for the channel's LastViewedAt. Any client — TUI,
	// mobile, web — that viewed the channel during the window updates
	// LastViewedAt on the server, so this works even when the daemon runs on
	// a different machine than the client.
	if e.opts.NotifyDelaySeconds > 0 {
		if !sleepCtx(ctx, time.Duration(e.opts.NotifyDelaySeconds)*time.Second) {
			return // shutdown
		}
		if e.wasRead(ctx, p) {
			e.log.Printf("mention in channel %s read within %ds — skipped", p.ChannelId, e.opts.NotifyDelaySeconds)
			e.advanceCursor(p.CreateAt)
			return
		}
	}

	senderFallback := ""
	if names, err := e.client.UsernamesByIDs(ctx, []string{p.UserId}); err == nil {
		if n := names[p.UserId]; n != "" {
			senderFallback = "@" + n
		}
	}
	label := channelLabel(ev, senderFallback)

	body := strings.TrimSpace(p.Message)
	if e.chat != nil && summarize {
		if s, err := e.summarize(ctx, ev, p, label); err != nil {
			e.log.Printf("summarize failed for %s, sending raw text: %v", label, err)
		} else if s != "" {
			body = s
		}
	}

	if e.tg == nil {
		e.log.Printf("mention: %s — %s", label, truncateForLog(body))
		e.advanceCursor(p.CreateAt)
		return
	}

	// With two-way enabled, offer an explicit mark-read button and remember the
	// Mattermost message so a Telegram reply or emoji reaction can act on it.
	var keyboard [][]telegram.Button
	if twoWay {
		keyboard = [][]telegram.Button{{
			{Text: "✓ Read", Data: "r:" + p.ChannelId},
		}}
		body += "\n\n↩ reply to respond · react to forward the emoji + mark read"
	}
	text := "🔔 " + label + "\n" + body
	if link := e.permalink(ev, p.Id); link != "" {
		text += "\n" + link
	}

	// Forward an image attachment as a photo (caption-capped); fall back to a
	// text notification when there's no image or the upload fails.
	var (
		msgID int
		err   error
	)
	if name, data, ok := e.imageAttachment(ctx, p); ok {
		caption := text
		if len(caption) > captionCap {
			caption = caption[:captionCap-1] + "…"
		}
		if msgID, err = e.tg.SendPhoto(ctx, chatID, caption, name, data, keyboard); err != nil {
			e.log.Printf("send photo failed for %s, falling back to text: %v", label, err)
			msgID, err = e.tg.Send(ctx, chatID, text, keyboard)
		}
	} else {
		msgID, err = e.tg.Send(ctx, chatID, text, keyboard)
	}
	if err != nil {
		e.log.Printf("telegram delivery failed for %s: %v", label, err)
		return
	}
	if twoWay {
		root := p.RootId
		if root == "" {
			root = p.Id
		}
		e.rememberNotif(msgID, notifTarget{channelID: p.ChannelId, rootID: root, postID: p.Id})
	}
	e.advanceCursor(p.CreateAt)
	e.log.Printf("notified telegram: %s", label)
}

// summarize gathers the conversation around the triggering post (the whole
// thread for a reply, otherwise the recent channel tail) and asks the chat model
// for a short notification summary.
func (e *Engine) summarize(ctx context.Context, ev *model.WebSocketEvent, p *model.Post, label string) (string, error) {
	var (
		pl  *model.PostList
		err error
	)
	if p.RootId != "" {
		pl, err = e.client.Thread(ctx, p.RootId)
	} else {
		pl, err = e.client.Posts(ctx, p.ChannelId, e.opts.ContextPosts)
	}
	if err != nil {
		return "", err
	}
	posts := ensureContains(postsByCreateAt(pl), p)
	names, _ := e.client.UsernamesByIDs(ctx, uniqueUserIDs(posts))
	if names == nil {
		names = map[string]string{}
	}
	text := transcript(posts, names)
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	return e.chat.Complete(ctx, e.systemPrompt(label), text)
}

// systemPrompt appends the reader + source framing to the configured prompt so
// the model knows whose perspective to summarize from.
func (e *Engine) systemPrompt(label string) string {
	me := ""
	if e.me != nil {
		me = e.me.Username
	}
	return e.opts.NotifyPrompt + fmt.Sprintf(
		"\n\nThe reader is @%s and was just mentioned (%s). In one or two short "+
			"sentences, say what is going on and whether they need to respond. "+
			"Keep @usernames intact.", me, label)
}

// ensureContains appends p to posts (re-sorting by time) when the live event is
// newer than the fetched page, so the triggering message is always summarized.
func ensureContains(posts []*model.Post, p *model.Post) []*model.Post {
	if p == nil {
		return posts
	}
	for _, q := range posts {
		if q != nil && q.Id == p.Id {
			return posts
		}
	}
	posts = append(posts, p)
	// Cheap: the slice is small (one page / one thread). Keep oldest-first.
	for i := len(posts) - 1; i > 0 && posts[i-1].CreateAt > posts[i].CreateAt; i-- {
		posts[i-1], posts[i] = posts[i], posts[i-1]
	}
	return posts
}

// truncateForLog clips a body to logBodyCap runes for a single-line log entry.
func truncateForLog(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > logBodyCap {
		return s[:logBodyCap] + "…"
	}
	return s
}

// inboundEnabled reports whether the Telegram receive loop (replies, buttons,
// commands) should run: delivery configured, two-way on, and a chat_id to lock
// to (the only sender obeyed).
func (e *Engine) inboundEnabled() bool {
	return e.tg != nil && e.opts.TwoWay && e.opts.TelegramChatID != ""
}

// isMuted reports whether the channel is muted in Mattermost.
func (e *Engine) isMuted(channelID string) bool {
	e.mutedMu.RLock()
	defer e.mutedMu.RUnlock()
	return e.muted[channelID]
}

// refreshMuted reloads the set of channels muted in Mattermost (notify prop
// mark_unread = mention). Best-effort: a failure leaves the previous set.
func (e *Engine) refreshMuted(ctx context.Context) {
	if !e.opts.RespectMutes || e.me == nil {
		return
	}
	members, err := e.client.ChannelMembers(ctx, e.me.Id)
	if err != nil {
		e.log.Printf("refresh muted channels: %v", err)
		return
	}
	set := make(map[string]bool)
	for _, m := range members {
		if m.NotifyProps[model.MarkUnreadNotifyProp] == model.ChannelMarkUnreadMention {
			set[m.ChannelId] = true
		}
	}
	e.mutedMu.Lock()
	e.muted = set
	e.mutedMu.Unlock()
}

// inQuietHoursNow reports whether the current local time is in the quiet window.
func (e *Engine) inQuietHoursNow() bool {
	if !e.quietOn {
		return false
	}
	now := time.Now()
	return inQuietHours(now.Hour()*60+now.Minute(), e.quietStart, e.quietEnd)
}

// wasRead reports whether the channel was viewed (by any client) after the
// post arrived, by querying LastViewedAt from the Mattermost server. On error
// it returns false so the notification fires rather than being silently lost.
func (e *Engine) wasRead(ctx context.Context, p *model.Post) bool {
	if e.me == nil {
		return false
	}
	m, err := e.client.ChannelMember(ctx, p.ChannelId, e.me.Id)
	if err != nil {
		e.log.Printf("check read state for channel %s: %v — notifying anyway", p.ChannelId, err)
		return false
	}
	return m.LastViewedAt >= p.CreateAt
}

// rememberNotif persists which Mattermost message a sent notification (msgID)
// referenced, so a later reply or reaction works across daemon restarts.
func (e *Engine) rememberNotif(msgID int, t notifTarget) {
	if msgID == 0 {
		return
	}
	if err := e.store.PutNotifTarget(msgID, t.channelID, t.rootID, t.postID); err != nil {
		e.log.Printf("persist notif target: %v", err)
	}
}

func (e *Engine) lookupNotif(msgID int) (notifTarget, bool) {
	ch, root, post, ok, err := e.store.GetNotifTarget(msgID)
	if err != nil {
		e.log.Printf("lookup notif target: %v", err)
		return notifTarget{}, false
	}
	return notifTarget{channelID: ch, rootID: root, postID: post}, ok
}

// sendTG sends a plain text message to the configured chat, logging on error.
func (e *Engine) sendTG(ctx context.Context, text string) {
	if _, err := e.tg.Send(ctx, e.opts.TelegramChatID, text, nil); err != nil {
		e.log.Printf("telegram send: %v", err)
	}
}

// pollUpdates long-polls Telegram for inbound messages/buttons and dispatches
// them until ctx is cancelled.
func (e *Engine) pollUpdates(ctx context.Context) {
	defer e.wg.Done()
	offset := 0
	for {
		if ctx.Err() != nil {
			return
		}
		ups, err := e.tg.GetUpdates(ctx, offset, pollTimeoutSec)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.log.Printf("telegram getUpdates: %v", err)
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			e.handleUpdate(ctx, u)
		}
	}
}

// authorized reports whether an update came from the configured chat_id — the
// only sender the bot acts on. Everyone else is ignored silently.
func (e *Engine) authorized(u telegram.Update) bool {
	var from *telegram.User
	switch {
	case u.CallbackQuery != nil:
		from = u.CallbackQuery.From
	case u.MessageReaction != nil:
		from = u.MessageReaction.User
	case u.Message != nil:
		from = u.Message.From
	}
	return from != nil && strconv.FormatInt(from.ID, 10) == e.opts.TelegramChatID
}

// handleUpdate routes one inbound update: button tap, command, or reply.
func (e *Engine) handleUpdate(ctx context.Context, u telegram.Update) {
	if !e.authorized(u) {
		e.log.Printf("ignoring telegram update from an unauthorized sender")
		return
	}
	switch {
	case u.CallbackQuery != nil:
		e.handleCallback(ctx, u.CallbackQuery)
	case u.MessageReaction != nil:
		e.handleReaction(ctx, u.MessageReaction)
	case u.Message != nil:
		e.handleMessage(ctx, u.Message)
	}
}

// handleMessage routes one inbound chat message: a command, a reply (text and/or
// an image/file attachment), or — for anything that isn't a reply — a usage
// hint. A photo/document message carries an empty Text (words ride in Caption),
// so gating on Text alone would silently drop image replies; gate on a usable
// payload instead.
func (e *Engine) handleMessage(ctx context.Context, msg *telegram.Message) {
	text := strings.TrimSpace(msg.Text)
	_, _, hasFile := inboundFile(msg)
	switch {
	case strings.HasPrefix(text, "/"):
		e.handleCommand(ctx, msg)
	case msg.ReplyToMessage != nil:
		// A reply to an /ask answer continues that conversation; anything else
		// is a reply to a notification, posted back into its thread.
		if e.maybeAskFollowup(ctx, msg) {
			return
		}
		e.handleReply(ctx, msg)
	case text != "" || hasFile:
		e.sendTG(ctx, "Reply to a notification to post back, or try /help.")
	}
}

// inboundFile returns the file_id and a suggested filename for a photo or
// document on msg, and ok=false when msg carries no forwardable file. A document
// (image sent "as a file", PDF, …) brings its own name; a photo doesn't, so the
// name is derived later from the download path.
func inboundFile(msg *telegram.Message) (fileID, filename string, ok bool) {
	if d := msg.Document; d != nil && d.FileID != "" {
		return d.FileID, d.FileName, true
	}
	if ph := msg.LargestPhoto(); ph != nil && ph.FileID != "" {
		return ph.FileID, "", true
	}
	return "", "", false
}

// handleCallback runs a tapped quick-action button (👍 react / ✓ mark read).
func (e *Engine) handleCallback(ctx context.Context, cb *telegram.CallbackQuery) {
	action, arg := decodeCallback(cb.Data)
	note := "done"
	switch action {
	case "r": // mark channel read
		if err := e.client.ViewChannel(ctx, e.me.Id, arg); err != nil {
			e.log.Printf("mark-read failed: %v", err)
			note = "mark-read failed"
		} else {
			note = "✓ marked read"
		}
	default:
		note = "unknown action"
	}
	if err := e.tg.AnswerCallback(ctx, cb.ID, note); err != nil {
		e.log.Printf("answer callback: %v", err)
	}
}

// handleReply posts a Telegram reply back into the Mattermost thread the
// replied-to notification came from. The reply may be text, an image/file, or
// both (a photo/document with a caption); any attachment is pulled from Telegram
// and re-uploaded to Mattermost, since the Telegram file id means nothing there.
func (e *Engine) handleReply(ctx context.Context, msg *telegram.Message) {
	target, ok := e.lookupNotif(msg.ReplyToMessage.MessageID)
	if !ok {
		e.sendTG(ctx, "I no longer have context for that message (the daemon may have restarted) — reply to a newer notification.")
		return
	}
	fileIDs, err := e.uploadInboundFile(ctx, target.channelID, msg)
	if err != nil {
		e.log.Printf("forward attachment failed: %v", err)
		e.sendTG(ctx, "Couldn't forward the attachment: "+err.Error())
		return
	}
	body := msg.Text
	if body == "" {
		body = msg.Caption // photo/document captions ride here, not in Text
	}
	if _, err := e.client.Send(ctx, target.channelID, target.rootID, body, fileIDs); err != nil {
		e.log.Printf("post reply failed: %v", err)
		e.sendTG(ctx, "Failed to post: "+err.Error())
		return
	}
	// Replying means you've dealt with it — mark the channel read too.
	if err := e.client.ViewChannel(ctx, e.me.Id, target.channelID); err != nil {
		e.log.Printf("mark read after reply: %v", err)
	}
	e.sendTG(ctx, "↩ posted")
	e.log.Printf("posted reply to channel %s", target.channelID)
}

// handleReaction mirrors a Telegram emoji reaction on a notification onto the
// Mattermost post (add/remove to match), and marks the channel read — reacting
// is the closest signal the Bot API gives that you saw the message.
func (e *Engine) handleReaction(ctx context.Context, mr *telegram.MessageReactionUpdated) {
	target, ok := e.lookupNotif(mr.MessageID)
	if !ok {
		return // reaction on an unknown/old notification — nothing to map it to
	}
	added, removed := reactionEmojiDiff(mr.OldReaction, mr.NewReaction)
	for _, em := range added {
		name, ok := mattermostEmojiName(em)
		if !ok {
			e.log.Printf("no Mattermost emoji for %q — skipped", em)
			e.sendTG(ctx, "Couldn't map "+em+" to a Mattermost reaction.")
			continue
		}
		if err := e.client.AddReaction(ctx, e.me.Id, target.postID, name); err != nil {
			e.log.Printf("add reaction %s: %v", name, err)
		}
	}
	for _, em := range removed {
		if name, ok := mattermostEmojiName(em); ok {
			if err := e.client.RemoveReaction(ctx, e.me.Id, target.postID, name); err != nil {
				e.log.Printf("remove reaction %s: %v", name, err)
			}
		}
	}
	if len(added) > 0 {
		if err := e.client.ViewChannel(ctx, e.me.Id, target.channelID); err != nil {
			e.log.Printf("mark read after reaction: %v", err)
		}
		e.log.Printf("forwarded reaction(s) %v + marked read (%s)", added, target.channelID)
	}
}

// reactionEmojiDiff returns the emoji added and removed between two reaction
// sets (only "emoji"-type reactions), each sorted for deterministic handling.
func reactionEmojiDiff(oldR, newR []telegram.ReactionType) (added, removed []string) {
	oldSet, newSet := emojiSet(oldR), emojiSet(newR)
	for em := range newSet {
		if !oldSet[em] {
			added = append(added, em)
		}
	}
	for em := range oldSet {
		if !newSet[em] {
			removed = append(removed, em)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func emojiSet(rs []telegram.ReactionType) map[string]bool {
	m := make(map[string]bool, len(rs))
	for _, r := range rs {
		if r.Type == "emoji" && r.Emoji != "" {
			m[r.Emoji] = true
		}
	}
	return m
}

// loadCursor reads the catch-up watermark from the store. On the first ever run
// (no stored value) it is set to now, so existing unread isn't replayed as
// "missed".
func (e *Engine) loadCursor() {
	if v, ok, err := e.store.GetMeta(cursorKey); err != nil {
		e.log.Printf("load cursor: %v", err)
	} else if ok {
		if ms, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			e.lastSeenMs = ms
			return
		}
	}
	e.lastSeenMs = time.Now().UnixMilli()
	if err := e.store.SetMeta(cursorKey, strconv.FormatInt(e.lastSeenMs, 10)); err != nil {
		e.log.Printf("init cursor: %v", err)
	}
}

func (e *Engine) cursor() int64 {
	e.seenMu.Lock()
	defer e.seenMu.Unlock()
	return e.lastSeenMs
}

// advanceCursor moves the catch-up watermark forward (never back) and persists.
func (e *Engine) advanceCursor(ms int64) {
	e.seenMu.Lock()
	if ms <= e.lastSeenMs {
		e.seenMu.Unlock()
		return
	}
	e.lastSeenMs = ms
	e.seenMu.Unlock()
	if err := e.store.SetMeta(cursorKey, strconv.FormatInt(ms, 10)); err != nil {
		e.log.Printf("persist cursor: %v", err)
	}
}

// catchUp delivers (as one consolidated message) the direct mentions / DMs that
// arrived while the daemon was disconnected — unread, newer than the cursor, and
// within catchupMaxAge — then advances the cursor. Runs after every connect.
//
// The candidate set (unread mentions + DMs) is replayed through the rules so the
// digest honours the user's notify rules; it is deliberately scoped to that
// bounded set (the only thing the server lets us query cheaply on reconnect) and
// to notify only — exec/webhook/react are live-only and never re-fire for
// historical posts. See docs/rules.md.
func (e *Engine) catchUp(ctx context.Context) {
	if !e.hasNotifyRule() || e.me == nil || e.tg == nil {
		return
	}
	cursor := e.cursor()
	if floor := time.Now().Add(-catchupMaxAge).UnixMilli(); cursor < floor {
		cursor = floor
	}
	chByID, members, err := e.channelsAndMembers(ctx)
	if err != nil {
		e.log.Printf("catch-up: %v", err)
		return
	}
	type item struct {
		channelID string
		post      *model.Post
	}
	var (
		items            []item
		chIDs, authorIDs []string
	)
	for _, mb := range members {
		ch := chByID[mb.ChannelId]
		if ch == nil {
			continue
		}
		isDM := ch.Type == model.ChannelTypeDirect
		if isDM && !e.opts.NotifyDMs {
			continue
		}
		if isDM {
			if int(ch.TotalMsgCountRoot-mb.MsgCountRoot) <= 0 {
				continue
			}
		} else if mb.MentionCountRoot == 0 {
			continue
		}
		var pl *model.PostList
		if mb.LastViewedAt > 0 {
			pl, _ = e.client.PostsSince(ctx, mb.ChannelId, mb.LastViewedAt)
		} else {
			pl, _ = e.client.Posts(ctx, mb.ChannelId, 50)
		}
		for _, p := range unreadPosts(pl, mb.LastViewedAt) {
			if p.UserId == e.me.Id || p.CreateAt <= cursor {
				continue
			}
			if !isDM && !mentionsName(p.Message, e.me.Username) {
				continue
			}
			items = append(items, item{mb.ChannelId, p})
			chIDs = append(chIDs, mb.ChannelId)
			authorIDs = append(authorIDs, p.UserId)
		}
	}
	now := time.Now().UnixMilli()
	if len(items) == 0 {
		e.advanceCursor(now)
		return
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].post.CreateAt < items[j].post.CreateAt })
	lbl := e.buildLabeler(ctx, chIDs, authorIDs)

	// Keep only the missed posts a notify rule would actually fire on, so a
	// config that narrows or replaces the default notification (e.g. notify for
	// one channel only) gets a catch-up digest that matches its live behaviour.
	kept := items[:0]
	for _, it := range items {
		ev := e.catchupEvent(chByID[it.channelID], it.post, lbl.names[it.post.UserId])
		if e.notifyMatches(ev, it.post) {
			kept = append(kept, it)
		}
	}
	items = kept
	if len(items) == 0 {
		e.advanceCursor(now)
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📥 While you were away — %s:", plural(len(items), "mention", "mentions"))
	const show = 20
	for i, it := range items {
		if i >= show {
			fmt.Fprintf(&b, "\n…and %d more — /unread for the rest", len(items)-show)
			break
		}
		name := lbl.names[it.post.UserId]
		if name == "" {
			name = it.post.UserId
		}
		fmt.Fprintf(&b, "\n• %s · @%s · %s", lbl.label(it.channelID), name, snippet(it.post.Message, 140))
	}
	e.sendTG(ctx, b.String())
	e.advanceCursor(now)
	e.log.Printf("catch-up: %d missed mention(s)", len(items))
}

// catchupEvent synthesises the "posted" event a missed post would have carried,
// so the catch-up path can match it against the same rules the live path uses.
// It fills the fields the matcher reads (channel type/name/team, sender, and —
// when the reader is named in the text — the mentions set).
func (e *Engine) catchupEvent(ch *model.Channel, p *model.Post, authorName string) *model.WebSocketEvent {
	ev := model.NewWebSocketEvent(model.WebsocketEventPosted, "", p.ChannelId, "", nil, "")
	if ch != nil {
		ev.Add("channel_type", string(ch.Type))
		ev.Add("channel_display_name", ch.DisplayName)
		ev.Add("team_id", ch.TeamId)
	}
	if authorName != "" {
		ev.Add("sender_name", "@"+authorName)
	}
	if e.me != nil && mentionsName(p.Message, e.me.Username) {
		ev.Add("mentions", `["`+e.me.Id+`"]`)
	}
	return ev
}

// refreshTeams loads team id → URL name (for permalinks) once at startup.
func (e *Engine) refreshTeams(ctx context.Context) {
	if e.me == nil {
		return
	}
	teams, err := e.client.Teams(ctx, e.me.Id)
	if err != nil {
		e.log.Printf("load teams: %v", err)
		return
	}
	m := make(map[string]string, len(teams))
	for _, t := range teams {
		m[t.Id] = t.Name
	}
	e.teamsMu.Lock()
	e.teams = m
	if e.defaultTeam == "" && len(teams) > 0 {
		e.defaultTeam = teams[0].Name
	}
	e.teamsMu.Unlock()
}

// permalink builds a Mattermost permalink (server/<team>/pl/<id>) to the post,
// using the post's team and falling back to any team for DMs. "" if no team.
func (e *Engine) permalink(ev *model.WebSocketEvent, postID string) string {
	if e.opts.ServerURL == "" || postID == "" {
		return ""
	}
	e.teamsMu.RLock()
	team := e.teams[eventStr(ev, "team_id")]
	if team == "" {
		team = e.defaultTeam
	}
	e.teamsMu.RUnlock()
	if team == "" {
		return ""
	}
	return strings.TrimRight(e.opts.ServerURL, "/") + "/" + team + "/pl/" + postID
}

// imageAttachment returns the first image attachment on the post (filename +
// bytes) if present and within maxPhotoBytes, for forwarding to Telegram.
func (e *Engine) imageAttachment(ctx context.Context, p *model.Post) (string, []byte, bool) {
	var files []*model.FileInfo
	if p.Metadata != nil {
		files = p.Metadata.Files
	}
	if len(files) == 0 && len(p.FileIds) > 0 {
		if fi, err := e.client.FileInfosForPost(ctx, p.Id); err == nil {
			files = fi
		}
	}
	for _, f := range files {
		if f == nil || !strings.HasPrefix(f.MimeType, "image/") || f.Size <= 0 || f.Size > maxPhotoBytes {
			continue
		}
		data, err := e.client.DownloadFile(ctx, f.Id)
		if err != nil {
			e.log.Printf("download attachment %s: %v", f.Id, err)
			return "", nil, false
		}
		name := f.Name
		if name == "" {
			name = "image"
		}
		return name, data, true
	}
	return "", nil, false
}

// uploadInboundFile downloads any photo/document on a Telegram reply and
// re-uploads it to the Mattermost channel, returning the resulting file ids to
// attach to the post (nil when the reply carries no attachment). The bytes flow
// through us because a Telegram file id is meaningless to Mattermost.
func (e *Engine) uploadInboundFile(ctx context.Context, channelID string, msg *telegram.Message) ([]string, error) {
	fileID, name, ok := inboundFile(msg)
	if !ok {
		return nil, nil
	}
	data, fpath, err := e.tg.FetchFile(ctx, fileID, maxInboundBytes)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = path.Base(fpath) // photos carry no name; the path ends in e.g. file_42.jpg
	}
	if name == "" || name == "." || name == "/" {
		name = "image"
	}
	fi, err := e.client.UploadFile(ctx, channelID, name, data)
	if err != nil {
		return nil, err
	}
	return []string{fi.Id}, nil
}

// IsUnauthorized reports whether err looks like a 401 from Mattermost (an
// expired or revoked session token). Mirrors the TUI's check.
func IsUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "401") || strings.Contains(s, "unauthorized")
}

// alertTokenExpired warns (log + Telegram) that the session token is dead and
// re-login is needed.
func (e *Engine) alertTokenExpired(ctx context.Context) {
	e.log.Printf("Mattermost session expired (401) — run `matterbox login` on the host and restart")
	if e.tg != nil {
		e.sendTG(ctx, "⚠️ matterbox: your Mattermost session expired. Run `matterbox login` on the host and restart the daemon.")
	}
}

// backoff returns the reconnect delay for the n-th consecutive failure (1 → 1s,
// capped at 32s). Mirrors the TUI's wsBackoff.
func backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	shift := n - 1
	if shift > 5 {
		shift = 5
	}
	return time.Second << shift
}

// sleepCtx waits for d or ctx cancellation; returns false if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
