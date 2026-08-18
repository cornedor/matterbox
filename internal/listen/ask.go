package listen

import (
	"context"
	"strings"
	"time"

	"matterbox/internal/aisearch"
	"matterbox/internal/telegram"
)

// defaultAskTimeout bounds a whole /ask run (all tool rounds) when no timeout is
// configured. Generous because each round waits on a local model.
const defaultAskTimeout = 4 * time.Minute

// askEnabled reports whether /ask can run: it needs Telegram delivery plus a
// chat endpoint + model (the summary server).
func (e *Engine) askEnabled() bool {
	return e.tg != nil &&
		strings.TrimSpace(e.opts.AskEndpoint) != "" &&
		strings.TrimSpace(e.opts.AskModel) != ""
}

// askConfig builds the aisearch.Config for one run from the daemon's options.
func (e *Engine) askConfig() aisearch.Config {
	return aisearch.Config{
		Store:       e.store,
		Endpoint:    e.opts.AskEndpoint,
		APIKey:      e.opts.AskAPIKey,
		Model:       e.opts.AskModel,
		MaxSteps:    e.opts.AskMaxSteps,
		EmbedClient: e.opts.EmbedClient,
		EmbedModel:  e.opts.EmbedModel,
		EmbedDim:    e.opts.EmbedDim,
	}
}

// cmdAsk runs the agentic search for a /ask question and delivers the answer to
// Telegram. The search itself runs on a goroutine (it can take a minute against
// a local model) so the inbound poll loop keeps serving other updates.
func (e *Engine) cmdAsk(ctx context.Context, args string) {
	q := strings.TrimSpace(args)
	if q == "" {
		e.sendTG(ctx, "Usage: /ask <question> — I'll search your cached messages and answer. Reply to my answer to ask a follow-up.")
		return
	}
	if !e.askEnabled() {
		e.sendTG(ctx, "/ask needs the chat model configured (the summary endpoint + model in config.yaml).")
		return
	}
	placeholder, err := e.tg.Send(ctx, e.opts.TelegramChatID, "🔎 searching…", nil)
	if err != nil {
		e.log.Printf("ask: send placeholder: %v", err)
		return
	}
	e.wg.Add(1)
	go e.runAsk(ctx, placeholder, q, nil)
}

// maybeAskFollowup continues a prior /ask conversation when msg is a reply to an
// answer the bot delivered. It returns true when it handled the reply (so the
// caller skips the thread-reply path), false when the replied-to message isn't a
// known /ask answer.
func (e *Engine) maybeAskFollowup(ctx context.Context, msg *telegram.Message) bool {
	if msg.ReplyToMessage == nil {
		return false
	}
	history, ok := e.lookupConvo(msg.ReplyToMessage.MessageID)
	if !ok {
		return false
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}
	if text == "" {
		e.sendTG(ctx, "Reply with a follow-up question.")
		return true
	}
	if !e.askEnabled() {
		e.sendTG(ctx, "/ask isn't configured.")
		return true
	}
	placeholder, err := e.tg.Send(ctx, e.opts.TelegramChatID, "🔎 thinking…", nil)
	if err != nil {
		e.log.Printf("ask follow-up: send placeholder: %v", err)
		return true
	}
	e.wg.Add(1)
	go e.runAsk(ctx, placeholder, text, history)
	return true
}

// runAsk drives one agentic search to completion and edits the placeholder into
// the answer, then remembers the transcript under the answer's message id so a
// reply can continue it. prior is nil for a fresh /ask, or the transcript of the
// conversation being followed up.
func (e *Engine) runAsk(parent context.Context, placeholderID int, question string, prior []aisearch.Message) {
	defer e.wg.Done()

	timeout := e.opts.AskTimeout
	if timeout <= 0 {
		timeout = defaultAskTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cat := e.catalog(ctx)

	var messages []aisearch.Message
	if len(prior) > 0 {
		// Copy so the append can't clobber the slice still held in the convo map.
		messages = append(prior[:len(prior):len(prior)], aisearch.Message{Role: "user", Content: question})
	} else {
		messages = []aisearch.Message{
			{Role: "system", Content: e.buildAskSystem(cat)},
			{Role: "user", Content: question},
		}
	}

	// Throttled live progress: edit the placeholder with the current step.
	var lastEdit time.Time
	onStep := func(s aisearch.TraceStep) {
		now := time.Now()
		if now.Sub(lastEdit) < askProgressInterval {
			return
		}
		lastEdit = now
		label := s.Label()
		if r := s.Result(); r != "" {
			label += " → " + r
		}
		_ = e.tg.EditMessageText(ctx, e.opts.TelegramChatID, placeholderID, "🔎 "+label)
	}

	res, err := aisearch.Ask(ctx, e.askConfig(), cat, messages, onStep)
	if err != nil {
		e.log.Printf("ask failed: %v", err)
		// Use the parent ctx for the error edit: the run ctx may be the thing
		// that just expired.
		_ = e.tg.EditMessageText(parent, e.opts.TelegramChatID, placeholderID, "⚠️ search failed: "+err.Error())
		return
	}

	answer := strings.TrimSpace(res.Answer)
	if answer == "" {
		answer = "(no answer)"
	}
	if res.Tentative {
		answer = "🤔 best guess (unconfirmed):\n" + answer
	}
	answer += "\n\n↩ reply to ask a follow-up"
	if err := e.tg.EditMessageText(parent, e.opts.TelegramChatID, placeholderID, answer); err != nil {
		e.log.Printf("ask: deliver answer: %v", err)
		return
	}
	e.rememberConvo(placeholderID, res.History)
	e.log.Printf("ask: answered (%d hits, %d turns)", len(res.Hits), len(res.History))
}

// buildAskSystem appends a small orientation (team names, today's date) to the
// configured agent prompt — mirroring the TUI's buildAISearchSystem. The model
// discovers channels via the list_channels tool, so the full catalog is not
// dumped here (keeping the prompt small for the local model).
func (e *Engine) buildAskSystem(cat aisearch.Catalog) string {
	system := e.opts.AskPrompt
	if names := cat.TeamNames(); len(names) > 0 {
		system += "\n\nTeams you can search: " + strings.Join(names, ", ") + "."
	}
	system += "\nToday is " + time.Now().Local().Format("Monday, January 2, 2006") + "."
	return system
}

// catalog returns the /ask channel/team/user snapshot, rebuilding it on first
// use and after askCatalogTTL so newly-joined channels appear.
func (e *Engine) catalog(ctx context.Context) aisearch.Catalog {
	e.askMu.Lock()
	defer e.askMu.Unlock()
	if e.askReady && time.Since(e.askCatalogAt) < askCatalogTTL {
		return e.askCatalog
	}
	cat := e.buildCatalog(ctx)
	e.askCatalog = cat
	e.askCatalogAt = time.Now()
	e.askReady = true
	return cat
}

// buildCatalog fetches the teams + channels the user can see and resolves the
// usernames needed to label authors and DM partners, then snapshots them into
// an aisearch.Catalog. Errors degrade gracefully (a missing piece just yields a
// thinner catalog) rather than failing the search.
func (e *Engine) buildCatalog(ctx context.Context) aisearch.Catalog {
	teams, err := e.client.Teams(ctx, e.me.Id)
	if err != nil {
		e.log.Printf("ask: load teams: %v", err)
	}
	channels, err := e.client.AllChannels(ctx, e.me.Id)
	if err != nil {
		e.log.Printf("ask: load channels: %v", err)
	}
	people := aisearch.ResolvePeople(ctx, e.client, e.me.Id, channels, e.store)
	cat := aisearch.BuildCatalog(e.me.Id, teams, channels, people)
	// Cached volume per channel ranks the channel/people listings, so the agent
	// sees the conversations that actually hold something first.
	if counts, err := e.store.ChannelPostCounts(); err == nil {
		cat = cat.WithVolumes(counts)
	} else {
		e.log.Printf("ask: channel volumes: %v", err)
	}
	return cat
}

// rememberConvo records a finished /ask transcript under the answer's Telegram
// message id, evicting the oldest once the cap is reached.
func (e *Engine) rememberConvo(msgID int, history []aisearch.Message) {
	if msgID == 0 || len(history) == 0 {
		return
	}
	e.convoMu.Lock()
	defer e.convoMu.Unlock()
	if e.convos == nil {
		e.convos = map[int][]aisearch.Message{}
	}
	if _, exists := e.convos[msgID]; !exists {
		e.convoIDs = append(e.convoIDs, msgID)
	}
	e.convos[msgID] = history
	for len(e.convoIDs) > askConvoCap {
		oldest := e.convoIDs[0]
		e.convoIDs = e.convoIDs[1:]
		delete(e.convos, oldest)
	}
}

// lookupConvo returns the remembered transcript for an /ask answer's message id.
func (e *Engine) lookupConvo(msgID int) ([]aisearch.Message, bool) {
	e.convoMu.Lock()
	defer e.convoMu.Unlock()
	h, ok := e.convos[msgID]
	return h, ok
}
