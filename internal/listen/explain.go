package listen

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// Explaining a rule is the other half of making one loud: the daemon refuses to
// start on a rule that can't work, and this says why a rule that can work
// didn't. Both exist because the alternative — editing YAML, restarting the
// daemon, and asking a colleague to say the magic word again — is a terrible
// way to find out you typed the channel name wrong.
//
// Explain runs the *same* matcher the daemon runs (matchPostWhy), never a
// second implementation of it, so its answer can't drift from the behaviour it
// describes. It stops short of the actions: nothing is executed, posted, or
// written, and the rate gates are only read.

// ProbeSpec describes a trigger to evaluate the rules against. Either hand it a
// real Post (from the cache or the server) or let it build one from Message and
// the surrounding fields.
type ProbeSpec struct {
	// Kind is the event kind to simulate (Event*); empty means EventMessage.
	Kind string
	// Post, when set, is the real post to test the rules against. Its body,
	// attachments, props, and thread position are used as-is.
	Post *model.Post
	// Message is the body of the post to synthesise when Post is nil.
	Message string
	// The conversation the trigger arrived in. Channel is the display name (what
	// `channel:` globs match), ChannelType one of public/private/dm/group.
	ChannelID, Channel, ChannelType string
	// The team, by URL name (what `team:` globs match) and/or id.
	TeamID, Team string
	// Author is the post's sender, without the leading @.
	Author string
	// FromMe marks the post as the reader's own (what `from_me: true` matches).
	FromMe bool
	// Emoji and Reactor describe a reaction trigger.
	Emoji, Reactor string
	// HasFile attaches a dummy file, Thread makes the post a reply, and Bot
	// marks it as coming from an integration.
	HasFile, Thread, Bot bool
	// At is the moment the trigger fired, which a `time` condition is tested
	// against. Zero means now.
	At time.Time
}

// ValidEventKind reports whether a string names an event kind a rule can react
// to, so a caller can reject a typo instead of probing with a kind no rule will
// ever match.
func ValidEventKind(kind string) bool {
	return kind == "" || slices.Contains(validEventKinds, kind)
}

// ValidChannelType reports whether a word names a channel type the conditions
// accept (public / private / dm / group).
func ValidChannelType(word string) bool {
	_, err := channelTypeLetter(word)
	return err == nil
}

// IsReactionKind reports whether an event kind carries a reaction — which the
// emoji and reactor fields need.
func IsReactionKind(kind string) bool { return kind == EventReaction || kind == EventUnreact }

// Explanation is one rule's verdict on a probe.
type Explanation struct {
	Rule    string   // the rule's name
	Kinds   []string // the event kinds it reacts to
	Actions []string // its action types, in order
	Stop    bool     // whether it halts later rules when it fires
	// Skipped is true when the rule doesn't react to the probe's kind at all —
	// the most common reason a rule "doesn't work" once triggers exist.
	Skipped bool
	// Matched reports whether every condition held; Why names the first one that
	// didn't ("channel", "message", "state:foo", "time", …).
	Matched bool
	Why     string
	// Gate describes a rate gate that would hold the rule back even though the
	// conditions matched. Read-only: nothing is recorded.
	Gate string
}

// Warm loads the lookup tables the matcher reads — team names for `team:`
// globs, channel info for the triggers whose event omits it — without opening a
// websocket, so a one-shot caller matches the way the running daemon would.
func (e *Engine) Warm(ctx context.Context) {
	e.refreshTeams(ctx)
	e.refreshChannels(ctx)
}

// Explain evaluates every rule against the probe and reports what each would
// have done. No action runs and nothing is persisted.
func (e *Engine) Explain(spec ProbeSpec) []Explanation {
	t := e.probeTrigger(spec)
	state := e.loadState() // read every key: a probe should explain state conditions even for a ruleset that has none
	render := e.stateKeyRenderer(t)

	rules := e.ruleSet()
	out := make([]Explanation, 0, len(rules))
	for _, r := range rules {
		ex := Explanation{Rule: r.Name, Kinds: r.Kinds(), Actions: actionTypes(r.Actions), Stop: r.Stop}
		if !r.fires(t.kind) {
			ex.Skipped = true
			out = append(out, ex)
			continue
		}
		ex.Matched, ex.Why = e.matchesWhy(t.ev, t.post, r.Match, state, render)
		if ex.Matched {
			ex.Gate = e.gateStatus(r, t)
		}
		out = append(out, ex)
	}
	return out
}

// gateStatus describes a rate gate that would hold a matching rule back, or ""
// when it would fire now. The cooldown stamp is read, never written; the
// frequency window lives only in the running daemon's memory, so it is
// described rather than consulted.
func (e *Engine) gateStatus(r Rule, t trigger) string {
	if c := r.Match.cool; c != nil {
		if v, ok, err := e.store.GetMeta(cooldownMetaKey(r.Name, c, t.ev, t.post)); err == nil && ok {
			if ms, perr := strconv.ParseInt(v, 10, 64); perr == nil {
				if left := c.every - e.clock().Sub(time.UnixMilli(ms)); left > 0 {
					return fmt.Sprintf("cooldown: %s left of %s", left.Round(time.Second), c.every)
				}
			}
		}
	}
	if f := r.Match.freq; f != nil {
		return fmt.Sprintf("frequency: fires on the %s match within %s (per %s) — the window lives in the running daemon", ordinal(f.count), f.within, f.by)
	}
	return ""
}

// ordinal renders 1, 2, 3 as 1st, 2nd, 3rd for the frequency description.
func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

// ActionTypes lists a rule's action types in order, for a listing.
func (r Rule) ActionTypes() []string { return actionTypes(r.Actions) }

// actionTypes lists a rule's action types in order, for the listing.
func actionTypes(actions []Action) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Type)
	}
	return out
}

// probeTrigger builds the (event, post) pair a ProbeSpec describes, filling the
// same data fields a live event carries so the matcher needs no special case
// for a dry run.
func (e *Engine) probeTrigger(spec ProbeSpec) trigger {
	kind := spec.Kind
	if kind == "" {
		kind = EventMessage
	}
	at := spec.At
	if at.IsZero() {
		at = e.clock()
	}

	p := spec.Post
	if p == nil {
		p = &model.Post{Message: spec.Message, CreateAt: at.UnixMilli()}
		if spec.HasFile {
			p.FileIds = []string{"probe-file"}
		}
		if spec.Thread {
			p.RootId = "probe-root"
		}
		if spec.Bot {
			p.AddProp(model.PostPropsFromWebhook, "true")
		}
	}
	if p.ChannelId == "" {
		p.ChannelId = spec.ChannelID
	}
	if p.Id == "" {
		p.Id = "probe-post"
	}
	if spec.FromMe && e.me != nil {
		p.UserId = e.me.Id
	}

	ev := model.NewWebSocketEvent(model.WebsocketEventPosted, "", p.ChannelId, "", nil, "")
	if kind == EventEdit || kind == EventDelete {
		// The real edit/delete events aren't `posted` events, and the mention
		// test treats them differently (see mentioned) — so the probe mustn't
		// claim they are.
		ev.SetEvent(model.WebsocketEventPostEdited)
	}
	ev.Add("channel_display_name", spec.Channel)
	ev.Add("channel_type", probeChannelType(spec))
	if spec.Team != "" || spec.TeamID != "" {
		id := spec.TeamID
		if id == "" {
			id = "probe-team"
		}
		ev.Add("team_id", id)
		if spec.Team != "" {
			e.teamsMu.Lock()
			if e.teams == nil {
				e.teams = map[string]string{}
			}
			e.teams[id] = spec.Team
			e.teamsMu.Unlock()
		}
	}
	author := strings.TrimPrefix(spec.Author, "@")
	if author == "" && spec.FromMe && e.me != nil {
		author = e.me.Username
	}
	if author != "" {
		ev.Add("sender_name", "@"+author)
	}
	if e.me != nil && mentionsName(p.Message, e.me.Username) {
		ev.Add("mentions", `["`+e.me.Id+`"]`)
	}
	if spec.Emoji != "" {
		ev.Add(emojiKey, strings.Trim(spec.Emoji, ": "))
	}
	if spec.Reactor != "" {
		ev.Add(reactorKey, strings.TrimPrefix(spec.Reactor, "@"))
	}
	ev.Add(triggerAtKey, strconv.FormatInt(at.UnixMilli(), 10))
	return trigger{kind: kind, ev: ev, post: p}
}

// probeChannelType resolves the channel-type letter for a probe: the explicit
// type if given, else public — with dm inferred from an empty display name only
// when the caller asked for it.
func probeChannelType(spec ProbeSpec) string {
	if spec.ChannelType != "" {
		if letter, err := channelTypeLetter(spec.ChannelType); err == nil {
			return letter
		}
	}
	return string(model.ChannelTypeOpen)
}
