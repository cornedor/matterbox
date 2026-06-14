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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/chat"
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

// defaultReaction is the emoji the 👍 quick-action button adds.
const defaultReaction = "+1"

// maxReplyTargets caps the in-memory notification→thread map (so a long-running
// daemon doesn't grow unbounded); oldest entries are evicted first. Replies to
// notifications older than this since a restart fall back to a "no context" note.
const maxReplyTargets = 1000

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
	// RespectMutes skips notifications for channels muted in Mattermost.
	RespectMutes bool
	// QuietHours is the raw "HH:MM-HH:MM" suppression window (local); empty =
	// always on. Parsed once in New.
	QuietHours string
	// TwoWay enables the inbound Telegram channel (replies, buttons, commands).
	// Requires TelegramChatID, the only sender the bot obeys.
	TwoWay bool
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

	wg sync.WaitGroup // tracks in-flight notify + inbound goroutines for shutdown

	// quiet-hours window (minutes-of-day), parsed from opts.QuietHours in New.
	quietStart, quietEnd int
	quietOn              bool

	mutedMu sync.RWMutex
	muted   map[string]bool // channels muted in Mattermost (RespectMutes)

	// replies maps a sent notification's Telegram message id to the Mattermost
	// thread a free-text reply should post into. Bounded by maxReplyTargets.
	repliesMu    sync.Mutex
	replies      map[int]replyTarget
	repliesOrder []int
}

// replyTarget is where a Telegram reply to a notification gets posted.
type replyTarget struct {
	channelID string
	rootID    string
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
		muted:   map[string]bool{},
		replies: map[int]replyTarget{},
	}
	if start, end, ok := parseQuietHours(opts.QuietHours); ok {
		e.quietStart, e.quietEnd, e.quietOn = start, end, true
	}
	return e
}

// Run connects, consumes events, and reconnects with exponential backoff until
// ctx is cancelled. It returns ctx.Err() once all in-flight notifications have
// drained, so a caller wiring it to SIGINT/SIGTERM gets a clean shutdown.
func (e *Engine) Run(ctx context.Context) error {
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
// cancelled.
func (e *Engine) consume(ctx context.Context, wsc *model.WebSocketClient) {
	for {
		select {
		case <-ctx.Done():
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
		if e.opts.NotifyOnMention && e.me != nil && isDirectMention(ev, p, e.me.Id, e.me.Username, e.opts.NotifySelf) {
			switch {
			case e.opts.RespectMutes && e.isMuted(p.ChannelId):
				e.log.Printf("mention in muted channel %s — skipped", p.ChannelId)
			case e.inQuietHoursNow():
				e.log.Printf("mention during quiet hours — skipped (cached; use /unread)")
			default:
				e.wg.Add(1)
				go e.notify(ctx, ev, p)
			}
		}
	case model.WebsocketEventPostEdited:
		if p := postFromEvent(ev); p != nil {
			e.ingest(p)
		}
	case model.WebsocketEventPostDeleted:
		if p := postFromEvent(ev); p != nil {
			if err := e.store.Delete(p.Id); err != nil {
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
// its own goroutine; respects ctx (cancelled on shutdown).
func (e *Engine) notify(ctx context.Context, ev *model.WebSocketEvent, p *model.Post) {
	defer e.wg.Done()

	senderFallback := ""
	if names, err := e.client.UsernamesByIDs(ctx, []string{p.UserId}); err == nil {
		if n := names[p.UserId]; n != "" {
			senderFallback = "@" + n
		}
	}
	label := channelLabel(ev, senderFallback)

	body := strings.TrimSpace(p.Message)
	if e.chat != nil && e.opts.Summarize {
		if s, err := e.summarize(ctx, ev, p, label); err != nil {
			e.log.Printf("summarize failed for %s, sending raw text: %v", label, err)
		} else if s != "" {
			body = s
		}
	}

	if e.tg == nil {
		e.log.Printf("mention: %s — %s", label, truncateForLog(body))
		return
	}

	// With two-way enabled, attach quick-action buttons and remember where a
	// free-text reply to this notification should land.
	var keyboard [][]telegram.Button
	if e.inboundEnabled() {
		keyboard = [][]telegram.Button{{
			{Text: "👍", Data: "k:" + p.Id},
			{Text: "✓ Read", Data: "r:" + p.ChannelId},
		}}
		body += "\n\n↩ reply to respond"
	}
	msgID, err := e.tg.Send(ctx, e.opts.TelegramChatID, "🔔 "+label+"\n"+body, keyboard)
	if err != nil {
		e.log.Printf("telegram delivery failed for %s: %v", label, err)
		return
	}
	if e.inboundEnabled() {
		root := p.RootId
		if root == "" {
			root = p.Id
		}
		e.rememberReply(msgID, replyTarget{channelID: p.ChannelId, rootID: root})
	}
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

// rememberReply records (bounded) where a reply to Telegram message msgID posts.
func (e *Engine) rememberReply(msgID int, t replyTarget) {
	if msgID == 0 {
		return
	}
	e.repliesMu.Lock()
	defer e.repliesMu.Unlock()
	if _, exists := e.replies[msgID]; !exists {
		e.repliesOrder = append(e.repliesOrder, msgID)
		for len(e.repliesOrder) > maxReplyTargets {
			delete(e.replies, e.repliesOrder[0])
			e.repliesOrder = e.repliesOrder[1:]
		}
	}
	e.replies[msgID] = t
}

func (e *Engine) lookupReply(msgID int) (replyTarget, bool) {
	e.repliesMu.Lock()
	defer e.repliesMu.Unlock()
	t, ok := e.replies[msgID]
	return t, ok
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
	case u.Message != nil && strings.TrimSpace(u.Message.Text) != "":
		text := strings.TrimSpace(u.Message.Text)
		switch {
		case strings.HasPrefix(text, "/"):
			e.handleCommand(ctx, u.Message)
		case u.Message.ReplyToMessage != nil:
			e.handleReply(ctx, u.Message)
		default:
			e.sendTG(ctx, "Reply to a notification to post back, or try /help.")
		}
	}
}

// handleCallback runs a tapped quick-action button (👍 react / ✓ mark read).
func (e *Engine) handleCallback(ctx context.Context, cb *telegram.CallbackQuery) {
	action, arg := decodeCallback(cb.Data)
	note := "done"
	switch action {
	case "k": // react 👍
		if err := e.client.AddReaction(ctx, e.me.Id, arg, defaultReaction); err != nil {
			e.log.Printf("react failed: %v", err)
			note = "react failed"
		} else {
			note = "👍 reacted"
		}
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

// handleReply posts a free-text Telegram reply back into the Mattermost thread
// the replied-to notification came from.
func (e *Engine) handleReply(ctx context.Context, msg *telegram.Message) {
	target, ok := e.lookupReply(msg.ReplyToMessage.MessageID)
	if !ok {
		e.sendTG(ctx, "I no longer have context for that message (the daemon may have restarted) — reply to a newer notification.")
		return
	}
	if _, err := e.client.Send(ctx, target.channelID, target.rootID, msg.Text, nil); err != nil {
		e.log.Printf("post reply failed: %v", err)
		e.sendTG(ctx, "Failed to post: "+err.Error())
		return
	}
	e.sendTG(ctx, "↩ posted")
	e.log.Printf("posted reply to channel %s", target.channelID)
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
