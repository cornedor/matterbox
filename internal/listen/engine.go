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

	wg sync.WaitGroup // tracks in-flight notify goroutines for clean shutdown
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
	return &Engine{client: client, store: st, chat: ch, tg: tg, me: me, opts: opts, log: logger}
}

// Run connects, consumes events, and reconnects with exponential backoff until
// ctx is cancelled. It returns ctx.Err() once all in-flight notifications have
// drained, so a caller wiring it to SIGINT/SIGTERM gets a clean shutdown.
func (e *Engine) Run(ctx context.Context) error {
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
		if e.opts.NotifyOnMention && e.me != nil && isDirectMention(ev, p, e.me.Id, e.me.Username) {
			e.wg.Add(1)
			go e.notify(ctx, ev, p)
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

	msg := "🔔 " + label + "\n" + body
	if e.tg == nil {
		e.log.Printf("mention: %s — %s", label, truncateForLog(body))
		return
	}
	if err := e.tg.SendMessage(ctx, e.opts.TelegramChatID, msg); err != nil {
		e.log.Printf("telegram delivery failed for %s: %v", label, err)
		return
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
