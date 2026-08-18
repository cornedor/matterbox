package listen

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// The event kinds a rule can react to, listed in its `on:` field. A rule that
// names none reacts to EventMessage — the only trigger the engine had before
// this existed — so every rule written against the old daemon keeps its
// meaning.
//
// Everything downstream of a trigger (the field matcher, the templates, the
// actions) reads a post and a `posted`-shaped WebSocket event. The events
// Mattermost sends for the other kinds are shaped differently and carry less:
// a reaction event has no post at all, only ids. Rather than teach every
// matcher about each event shape, the engine normalises at the edge — it looks
// up what the event omits and synthesises the posted-shaped event the rest of
// the pipeline expects (see reactionTrigger / enrich).
const (
	EventMessage  = "message"          // a new post arrived
	EventEdit     = "edit"             // an existing post was edited
	EventDelete   = "delete"           // a post was deleted
	EventReaction = "reaction"         // someone added an emoji reaction to a post
	EventUnreact  = "reaction_removed" // someone removed one
	EventSchedule = "schedule"         // the rule's own timer came due
)

// validEventKinds lists every accepted `on:` value for the unknown-kind error.
var validEventKinds = []string{
	EventMessage, EventEdit, EventDelete, EventReaction, EventUnreact, EventSchedule,
}

// postKinds are the kinds whose trigger is a post the reader can act on — the
// ones where a react / mark_read / thread-send action has something to aim at.
// A schedule rule has no post, so those actions are rejected at compile time
// rather than failing silently at 09:00.
func kindHasPost(kind string) bool { return kind != EventSchedule }

// trigger is one thing that set the rules in motion: a post arriving, a post
// being edited or deleted, a reaction landing on a post, or a rule's schedule
// coming due. ev is always a posted-shaped event (synthesised where the real
// one isn't), and post is always non-nil — a bare stub carrying the firing time
// for a schedule trigger — so the matcher and the actions need no special cases.
type trigger struct {
	kind string
	ev   *model.WebSocketEvent
	post *model.Post
	// caps holds the submatches of the rule's `message` regexp, filled per
	// matching rule (see captures) and exposed to templates as {{ .match.1 }}.
	// Nil for a rule without a message condition.
	caps map[string]string
	// rule names the rule whose timer fired, for a schedule trigger.
	rule string
}

// msgTrigger wraps a live post event, the original (and still most common)
// trigger.
func msgTrigger(ev *model.WebSocketEvent, p *model.Post) trigger {
	return trigger{kind: EventMessage, ev: ev, post: p}
}

// withCaps returns the trigger with regexp captures attached, so the actions of
// the rule that matched can interpolate them without the other rules' captures
// leaking in.
func (t trigger) withCaps(caps map[string]string) trigger {
	t.caps = caps
	return t
}

// eligible reports whether a trigger is worth evaluating at all. System
// messages never match anything, mirroring the guard the daemon has always
// applied. The body/tombstone tests differ per kind: a delete trigger is *about*
// a post with DeleteAt set, and a reaction can land on a post with no text at
// all (an image), so only the message-shaped kinds require a non-empty body.
func (t trigger) eligible() bool {
	p := t.post
	if p == nil || p.IsSystemMessage() {
		return false
	}
	switch t.kind {
	case EventDelete, EventSchedule:
		return true
	case EventReaction, EventUnreact:
		return p.DeleteAt == 0
	default:
		// "Empty" means nothing to match on, which is not the same as an empty
		// body: an integration's alert routinely has no body at all and carries
		// everything in an attachment.
		return p.DeleteAt == 0 && strings.TrimSpace(matchText(p)) != ""
	}
}

// triggerAtKey carries the moment a trigger fired through the synthesised
// event, so a `time` condition on a reaction rule tests when the reaction
// happened rather than when the (possibly ancient) post it landed on was
// written. Absent on a live posted event, where the post's own CreateAt is the
// same instant.
const triggerAtKey = "trigger_at"

// triggerAt is the instant a trigger fired: the synthesised stamp if present,
// else the post's creation time, else now (a hand-built event in a test).
func triggerAt(ev *model.WebSocketEvent, p *model.Post) time.Time {
	if s := eventStr(ev, triggerAtKey); s != "" {
		if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.UnixMilli(ms)
		}
	}
	if p != nil && p.CreateAt > 0 {
		return time.UnixMilli(p.CreateAt)
	}
	return templateClock()
}

// emojiKey / reactorKey carry a reaction trigger's specifics through the
// synthesised event, where the emoji/reactor conditions and the envelope read
// them back.
const (
	emojiKey   = "emoji_name"
	reactorKey = "reactor"
)

// wantsKind reports whether any loaded rule reacts to this event kind. The
// engine consults it before doing any work for an event — a reaction event
// costs a post lookup and a channel resolve, which a config with no reaction
// rule should never pay.
func (e *Engine) wantsKind(kind string) bool {
	for _, r := range e.ruleSet() {
		if r.fires(kind) {
			return true
		}
	}
	return false
}

// chanInfo is what a rule needs to know about a channel it can't read off the
// event: the display name (`channel` globs), the type (`dm` / `channel_type`)
// and the team (`team` globs, permalinks).
type chanInfo struct {
	displayName string
	typ         model.ChannelType
	teamID      string
}

// channelInfo resolves a channel id, filling the cache from the reader's full
// channel list on a miss. One list call covers every channel the reader is in,
// so a burst of reaction events costs at most one round-trip; the refill is
// throttled so an id we genuinely can't see (an archived channel, a channel we
// left) can't turn every event into an API call.
func (e *Engine) channelInfo(ctx context.Context, id string) (chanInfo, bool) {
	if id == "" {
		return chanInfo{}, false
	}
	e.chanMu.Lock()
	info, ok := e.chanCache[id]
	stale := e.clock().Sub(e.chanAt) >= chanCacheTTL
	e.chanMu.Unlock()
	if ok || !stale {
		return info, ok
	}
	e.refreshChannels(ctx)
	e.chanMu.Lock()
	defer e.chanMu.Unlock()
	info, ok = e.chanCache[id]
	return info, ok
}

// chanCacheTTL bounds how often a cache miss may trigger a full channel list
// refill.
const chanCacheTTL = time.Minute

// refreshChannels reloads the channel cache from the server. A failure leaves
// the previous cache in place (and still stamps the attempt, so a server that
// is down doesn't get hammered once per event).
func (e *Engine) refreshChannels(ctx context.Context) {
	if e.me == nil {
		return
	}
	e.chanMu.Lock()
	e.chanAt = e.clock()
	e.chanMu.Unlock()

	chans, err := e.client.AllChannels(ctx, e.me.Id)
	if err != nil {
		e.log.Printf("rule trigger: load channels: %v", err)
		return
	}
	m := make(map[string]chanInfo, len(chans))
	for _, ch := range chans {
		if ch != nil {
			m[ch.Id] = chanInfo{displayName: ch.DisplayName, typ: ch.Type, teamID: ch.TeamId}
		}
	}
	e.chanMu.Lock()
	e.chanCache = m
	e.chanMu.Unlock()
}

// username resolves a user id to a username, caching the answer. Usernames
// rarely change and a wrong one only ever affects an author/reactor condition,
// so the cache never expires.
func (e *Engine) username(ctx context.Context, userID string) string {
	if userID == "" {
		return ""
	}
	e.userMu.Lock()
	name, ok := e.userCache[userID]
	e.userMu.Unlock()
	if ok {
		return name
	}
	names, err := e.client.UsernamesByIDs(ctx, []string{userID})
	if err != nil {
		e.log.Printf("rule trigger: resolve user %s: %v", userID, err)
		return ""
	}
	name = names[userID]
	e.userMu.Lock()
	if e.userCache == nil {
		e.userCache = map[string]string{}
	}
	e.userCache[userID] = name
	e.userMu.Unlock()
	return name
}

// enrich fills the data fields a `posted` event carries but an edit or delete
// event doesn't — channel name/type, team, sender — so the same matcher runs
// against all of them. It mutates the event in place, which is safe because it
// happens on the ingest goroutine before any action sees it. Fields the event
// already has are left alone.
func (e *Engine) enrich(ctx context.Context, ev *model.WebSocketEvent, p *model.Post) {
	if ev == nil || p == nil {
		return
	}
	if eventStr(ev, "channel_display_name") == "" || eventStr(ev, "channel_type") == "" {
		if info, ok := e.channelInfo(ctx, p.ChannelId); ok {
			ev.Add("channel_display_name", info.displayName)
			ev.Add("channel_type", string(info.typ))
			if info.teamID != "" {
				ev.Add("team_id", info.teamID)
			}
		}
	}
	if eventStr(ev, "sender_name") == "" {
		if name := e.username(ctx, p.UserId); name != "" {
			ev.Add("sender_name", "@"+name)
		}
	}
	if eventStr(ev, triggerAtKey) == "" {
		ev.Add(triggerAtKey, strconv.FormatInt(e.clock().UnixMilli(), 10))
	}
}

// reactionFromEvent decodes the reaction embedded in a reaction_added /
// reaction_removed event (Mattermost JSON-encodes it into data["reaction"]).
func reactionFromEvent(ev *model.WebSocketEvent) *model.Reaction {
	if ev == nil {
		return nil
	}
	raw, ok := ev.GetData()["reaction"].(string)
	if !ok || raw == "" {
		return nil
	}
	var r model.Reaction
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil
	}
	return &r
}

// reactionTrigger builds the trigger for a reaction event: it looks up the post
// the reaction landed on (cache first, server second) and synthesises the
// posted-shaped event the matcher expects.
//
// The conditions keep their post-centric meaning — `author` and `from_me` are
// about whoever wrote the post, not whoever reacted — because the useful rule
// is "someone reacted to *my* message". Who reacted is a separate condition
// (`reactor`), and the emoji is `emoji`.
func (e *Engine) reactionTrigger(ctx context.Context, ev *model.WebSocketEvent, kind string) (trigger, bool) {
	r := reactionFromEvent(ev)
	if r == nil || r.PostId == "" {
		return trigger{}, false
	}
	if e.isSelfReactionEcho(r) {
		return trigger{}, false
	}
	p, err := e.store.Post(r.PostId)
	if err != nil {
		e.log.Printf("rule trigger: cache lookup %s: %v", r.PostId, err)
	}
	if p == nil {
		if p, err = e.client.Post(ctx, r.PostId); err != nil {
			e.log.Printf("rule trigger: fetch post %s: %v", r.PostId, err)
			return trigger{}, false
		}
	}
	synth := model.NewWebSocketEvent(model.WebsocketEventPosted, "", p.ChannelId, "", nil, "")
	if info, ok := e.channelInfo(ctx, p.ChannelId); ok {
		synth.Add("channel_display_name", info.displayName)
		synth.Add("channel_type", string(info.typ))
		if info.teamID != "" {
			synth.Add("team_id", info.teamID)
		}
	}
	if name := e.username(ctx, p.UserId); name != "" {
		synth.Add("sender_name", "@"+name)
	}
	if e.me != nil && mentionsName(p.Message, e.me.Username) {
		synth.Add("mentions", `["`+e.me.Id+`"]`)
	}
	synth.Add(emojiKey, strings.Trim(r.EmojiName, ": "))
	synth.Add(reactorKey, e.username(ctx, r.UserId))
	at := r.CreateAt
	if at == 0 {
		at = e.clock().UnixMilli()
	}
	synth.Add(triggerAtKey, strconv.FormatInt(at, 10))
	return trigger{kind: kind, ev: synth, post: p}, true
}

// selfReactTTL bounds how long a reaction this daemon added is remembered as
// its own, waiting for the websocket to echo it back.
const selfReactTTL = 30 * time.Second

// noteSelfReaction remembers a reaction the react action just added. The server
// broadcasts it back like any other, and without this a rule of the shape
// "on: reaction … actions: react" would chase its own tail forever. Only
// reactions *this daemon* made are remembered, so one you add from your phone
// still triggers rules — which is the whole point of a reaction trigger.
func (e *Engine) noteSelfReaction(postID, emoji string) {
	if postID == "" || emoji == "" {
		return
	}
	now := e.clock()
	e.selfReactMu.Lock()
	defer e.selfReactMu.Unlock()
	if e.selfReact == nil {
		e.selfReact = map[string]time.Time{}
	}
	for k, at := range e.selfReact {
		if now.Sub(at) > selfReactTTL {
			delete(e.selfReact, k)
		}
	}
	e.selfReact[selfReactKey(postID, emoji)] = now
}

// isSelfReactionEcho reports whether this event is the echo of a reaction the
// daemon just added, consuming the note so a later, genuine reaction with the
// same emoji still fires.
func (e *Engine) isSelfReactionEcho(r *model.Reaction) bool {
	if e.me == nil || r.UserId != e.me.Id {
		return false
	}
	key := selfReactKey(r.PostId, strings.Trim(r.EmojiName, ": "))
	e.selfReactMu.Lock()
	defer e.selfReactMu.Unlock()
	at, ok := e.selfReact[key]
	if !ok || e.clock().Sub(at) > selfReactTTL {
		return false
	}
	delete(e.selfReact, key)
	return true
}

func selfReactKey(postID, emoji string) string { return postID + "\x00" + emoji }

// scheduleTrigger builds the trigger for a rule whose timer came due. There is
// no post and no channel, so the stub post carries only the firing time — which
// is what a `time` condition and the {{ now }} template read.
func (e *Engine) scheduleTrigger(r Rule, at time.Time) trigger {
	ev := model.NewWebSocketEvent(model.WebsocketEventPosted, "", "", "", nil, "")
	ev.Add(triggerAtKey, strconv.FormatInt(at.UnixMilli(), 10))
	return trigger{
		kind: EventSchedule,
		ev:   ev,
		post: &model.Post{CreateAt: at.UnixMilli()},
		rule: r.Name,
	}
}
