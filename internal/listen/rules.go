package listen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// A rule is a declarative reaction to an incoming post: when its Match passes,
// its Actions run in order. This generalises the daemon's original behaviour —
// "on a direct mention / DM, summarise and forward to Telegram" — into one of
// many possible reactions, and adds local side effects (run a command, POST a
// webhook) that a server-side Mattermost plugin can't do on a per-user basis.
//
// The notification bridge itself is no longer special-cased in handle(): it is
// just the default rule (see defaultRules), synthesised from the legacy
// notify_* options and fully replaced the moment the user writes their own
// `rules:` block. Telegram delivery is "not built in" so much as the rule you
// get when you haven't written any.

// Action type ids accepted in a rule's actions list.
const (
	ActionNotify   = "notify"    // summarise + deliver to Telegram (the legacy bridge)
	ActionExec     = "exec"      // run a local command, post piped in as JSON
	ActionWebhook  = "webhook"   // HTTP POST the post envelope as JSON
	ActionReact    = "react"     // add an emoji reaction to the post
	ActionMarkRead = "mark_read" // mark the post's channel read
	ActionLog      = "log"       // write a line to the daemon log
)

// execTimeout bounds a single exec action so a hung command can't pin a
// goroutine for the life of the daemon.
const execTimeout = 30 * time.Second

// webhookTimeout bounds a single webhook POST.
const webhookTimeout = 15 * time.Second

// RuleSpec is the on-the-wire (config) form of a rule, before compilation. The
// config package parses YAML into the mirror type and the cli layer maps it to
// these; CompileRules turns them into ready-to-run Rules, failing loud on a bad
// regexp, glob, or action so a typo is a startup error rather than a rule that
// silently never fires.
type RuleSpec struct {
	Name    string
	Stop    bool
	Match   MatchSpec
	Actions []ActionSpec
}

// MatchSpec is the config form of a rule's conditions. All set conditions are
// ANDed; an all-zero MatchSpec matches every (non-system, non-empty) post.
type MatchSpec struct {
	// Channel is a case-insensitive glob (*, ?) over the channel's display
	// name, or an exact channel id. Empty matches any channel.
	Channel string
	// Author is a username (without the leading @), matched case-insensitively
	// against the post's sender. Empty matches any author.
	Author string
	// Message is an RE2 regexp matched against the message body (add (?i) for
	// case-insensitive). Empty matches any body.
	Message string
	// Mention requires that the reader was directly named (@me) — the same test
	// the notification bridge uses for channel mentions.
	Mention bool
	// DM, when non-nil, requires the post be in a direct message (true) or not
	// (false). Nil matches either.
	DM *bool
	// HasFile requires at least one attached file.
	HasFile bool
	// IsThread, when non-nil, requires the post be a thread reply (true) or a
	// root post (false). Nil matches either.
	IsThread *bool
}

// ActionSpec is the config form of one action.
type ActionSpec struct {
	// Type is one of the Action* ids.
	Type string
	// Summarize (notify) overrides the daemon's summarize setting for this rule
	// only. Nil inherits Options.Summarize.
	Summarize *bool
	// Command (exec) is the argv to run; the post envelope is piped to its
	// stdin as JSON and the key fields are exported as MATTERBOX_* env vars.
	Command []string
	// URL (webhook) is POSTed the post envelope as a JSON body.
	URL string
	// Emoji (react) is the Mattermost emoji shortcode to add, without colons.
	Emoji string
	// Text (log) is an optional prefix for the log line.
	Text string
}

// Rule is a compiled RuleSpec.
type Rule struct {
	Name    string
	Stop    bool
	Match   Match
	Actions []Action
}

// Match is a compiled MatchSpec: the globs/regexps are pre-compiled and the
// original strings kept for an exact-id fallback.
type Match struct {
	// builtin selects the legacy isDirectMention trigger instead of the field
	// matcher; set only by defaultRules so the default notification behaviour is
	// reproduced byte-for-byte. Never set from user config.
	builtin bool

	channelRaw string
	channelRe  *regexp.Regexp
	author     string
	messageRe  *regexp.Regexp
	mention    bool
	dm         *bool
	hasFile    bool
	isThread   *bool
}

// Action is a compiled ActionSpec (currently identical, kept distinct so the
// compiled and config layers can diverge later).
type Action struct {
	Type      string
	Summarize *bool
	Command   []string
	URL       string
	Emoji     string
	Text      string
}

// CompileRules validates and compiles user rule specs. It returns an error on
// the first bad regexp, glob, or action so the daemon refuses to start rather
// than running with a rule that can never match (or never fire).
func CompileRules(specs []RuleSpec) ([]Rule, error) {
	rules := make([]Rule, 0, len(specs))
	for i, s := range specs {
		name := s.Name
		if name == "" {
			name = fmt.Sprintf("rule %d", i+1)
		}
		m, err := compileMatch(s.Match)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", name, err)
		}
		if len(s.Actions) == 0 {
			return nil, fmt.Errorf("rule %q: no actions", name)
		}
		actions := make([]Action, 0, len(s.Actions))
		for _, a := range s.Actions {
			ca, err := compileAction(a)
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", name, err)
			}
			actions = append(actions, ca)
		}
		rules = append(rules, Rule{Name: name, Stop: s.Stop, Match: m, Actions: actions})
	}
	return rules, nil
}

func compileMatch(s MatchSpec) (Match, error) {
	m := Match{
		channelRaw: s.Channel,
		author:     s.Author,
		mention:    s.Mention,
		dm:         s.DM,
		hasFile:    s.HasFile,
		isThread:   s.IsThread,
	}
	if s.Channel != "" {
		re, err := globToRegexp(s.Channel)
		if err != nil {
			return Match{}, fmt.Errorf("bad channel glob %q: %w", s.Channel, err)
		}
		m.channelRe = re
	}
	if s.Message != "" {
		re, err := regexp.Compile(s.Message)
		if err != nil {
			return Match{}, fmt.Errorf("bad message regexp %q: %w", s.Message, err)
		}
		m.messageRe = re
	}
	return m, nil
}

func compileAction(a ActionSpec) (Action, error) {
	switch a.Type {
	case ActionNotify, ActionMarkRead, ActionLog:
		// no required fields
	case ActionExec:
		if len(a.Command) == 0 {
			return Action{}, fmt.Errorf("exec action needs a command")
		}
	case ActionWebhook:
		if strings.TrimSpace(a.URL) == "" {
			return Action{}, fmt.Errorf("webhook action needs a url")
		}
	case ActionReact:
		if strings.TrimSpace(a.Emoji) == "" {
			return Action{}, fmt.Errorf("react action needs an emoji")
		}
	case "":
		return Action{}, fmt.Errorf("action has no type")
	default:
		return Action{}, fmt.Errorf("unknown action type %q (want one of: %s, %s, %s, %s, %s, %s)",
			a.Type, ActionNotify, ActionExec, ActionWebhook, ActionReact, ActionMarkRead, ActionLog)
	}
	return Action{
		Type:      a.Type,
		Summarize: a.Summarize,
		Command:   a.Command,
		URL:       a.URL,
		Emoji:     strings.Trim(a.Emoji, ": "),
		Text:      a.Text,
	}, nil
}

// globToRegexp turns a shell-ish glob (* and ?) into an anchored,
// case-insensitive regexp. Every other character is matched literally.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// defaultRules reproduces the daemon's original behaviour as a single rule when
// the user has configured none: on the legacy direct-mention/DM trigger, run
// the notify action. Returns nil (no rules) when notifications are off, leaving
// the daemon a pure cache-warmer — exactly as before.
func defaultRules(opts Options) []Rule {
	if !opts.NotifyOnMention {
		return nil
	}
	return []Rule{{
		Name:    "notify-mentions-and-dms",
		Match:   Match{builtin: true},
		Actions: []Action{{Type: ActionNotify}},
	}}
}

// applyRules evaluates the engine's rules against an ingested post, running the
// actions of every matching rule in order and stopping at the first matched
// rule that sets Stop. System messages, deletions, and empty bodies never
// match anything (mirroring the old isDirectMention guards), so a join notice
// or a tombstone can't trigger an exec rule.
func (e *Engine) applyRules(ctx context.Context, ev *model.WebSocketEvent, p *model.Post) {
	if p == nil || p.DeleteAt != 0 || p.IsSystemMessage() || strings.TrimSpace(p.Message) == "" {
		return
	}
	for _, r := range e.rules {
		if !e.matches(ev, p, r.Match) {
			continue
		}
		e.runActions(ctx, ev, p, r.Actions)
		if r.Stop {
			return
		}
	}
}

// matches reports whether a post satisfies a rule's conditions. The builtin
// match defers to isDirectMention so the default rule behaves identically to
// the pre-rules daemon; user rules use the field matcher.
func (e *Engine) matches(ev *model.WebSocketEvent, p *model.Post, m Match) bool {
	meID, meName := "", ""
	if e.me != nil {
		meID, meName = e.me.Id, e.me.Username
	}
	if m.builtin {
		return isDirectMention(ev, p, meID, meName, e.opts.NotifySelf)
	}
	return matchPost(ev, p, m, meID, meName)
}

// matchPost evaluates the field conditions of a (non-builtin) match. Pure for
// testability: every condition it reads comes from the event/post, plus the
// reader's id and username for the Mention check.
func matchPost(ev *model.WebSocketEvent, p *model.Post, m Match, meID, meName string) bool {
	isDM := eventStr(ev, "channel_type") == string(model.ChannelTypeDirect)
	if m.dm != nil && *m.dm != isDM {
		return false
	}
	if m.mention && !(wsMentions(ev)[meID] && mentionsName(p.Message, meName)) {
		return false
	}
	if m.author != "" {
		sender := strings.TrimPrefix(eventStr(ev, "sender_name"), "@")
		if !strings.EqualFold(sender, m.author) {
			return false
		}
	}
	if m.channelRe != nil {
		name := eventStr(ev, "channel_display_name")
		if !m.channelRe.MatchString(name) && m.channelRaw != p.ChannelId {
			return false
		}
	}
	if m.messageRe != nil && !m.messageRe.MatchString(p.Message) {
		return false
	}
	if m.hasFile && !postHasFile(p) {
		return false
	}
	if m.isThread != nil {
		isReply := p.RootId != "" && p.RootId != p.Id
		if *m.isThread != isReply {
			return false
		}
	}
	return true
}

// postHasFile reports whether the post carries an attachment, from either the
// file id list or the embedded metadata (no network call on the ingest path).
func postHasFile(p *model.Post) bool {
	if len(p.FileIds) > 0 {
		return true
	}
	return p.Metadata != nil && len(p.Metadata.Files) > 0
}

// runActions dispatches a matched rule's actions. Side effects that touch the
// network or run a command are spun off (tracked by e.wg, cancelled on
// shutdown) so they never block the single ingest goroutine; log is cheap and
// runs inline.
func (e *Engine) runActions(ctx context.Context, ev *model.WebSocketEvent, p *model.Post, actions []Action) {
	for _, a := range actions {
		switch a.Type {
		case ActionNotify:
			e.notifyGate(ctx, ev, p, e.summarizeFor(a))
		case ActionExec:
			a := a
			e.wg.Add(1)
			go e.runExec(ctx, ev, p, a)
		case ActionWebhook:
			a := a
			e.wg.Add(1)
			go e.runWebhook(ctx, ev, p, a)
		case ActionReact:
			a := a
			e.wg.Add(1)
			go e.runReact(ctx, p, a)
		case ActionMarkRead:
			e.wg.Add(1)
			go e.runMarkRead(ctx, p)
		case ActionLog:
			e.runLog(ev, p, a)
		}
	}
}

// summarizeFor resolves a notify action's effective summarize setting: the
// per-rule override if present, else the daemon default.
func (e *Engine) summarizeFor(a Action) bool {
	if a.Summarize != nil {
		return *a.Summarize
	}
	return e.opts.Summarize
}

// notifyGate applies the notification policy (skip own messages, muted
// channels, and quiet hours, and honour notify_dms) before spinning off the
// delivery. This is the same gate the pre-rules daemon applied inline in
// handle(); centralising it here means every notify action — default or
// user-defined — respects the same do-not-disturb settings.
func (e *Engine) notifyGate(ctx context.Context, ev *model.WebSocketEvent, p *model.Post, summarize bool) {
	if e.me != nil && p.UserId == e.me.Id && !e.opts.NotifySelf {
		return
	}
	if eventStr(ev, "channel_type") == string(model.ChannelTypeDirect) && !e.opts.NotifyDMs {
		return
	}
	if e.opts.RespectMutes && e.isMuted(p.ChannelId) {
		e.log.Printf("mention in muted channel %s — skipped", p.ChannelId)
		return
	}
	if e.inQuietHoursNow() {
		e.log.Printf("mention during quiet hours — skipped (cached; use /unread)")
		return
	}
	e.wg.Add(1)
	go e.notify(ctx, ev, p, summarize)
}

// envelope is the JSON view of a post passed to exec/webhook actions. It is
// intentionally flat and stable so scripts can depend on it.
type envelope struct {
	PostID    string `json:"post_id"`
	ChannelID string `json:"channel_id"`
	Channel   string `json:"channel"`
	Author    string `json:"author"`
	Message   string `json:"message"`
	IsDM      bool   `json:"is_dm"`
	CreateAt  int64  `json:"create_at"`
	Permalink string `json:"permalink,omitempty"`
}

// buildEnvelope assembles the exec/webhook payload from the event + post,
// using only data already in hand (no extra API calls on the ingest path).
func (e *Engine) buildEnvelope(ev *model.WebSocketEvent, p *model.Post) envelope {
	return envelope{
		PostID:    p.Id,
		ChannelID: p.ChannelId,
		Channel:   eventStr(ev, "channel_display_name"),
		Author:    strings.TrimPrefix(eventStr(ev, "sender_name"), "@"),
		Message:   p.Message,
		IsDM:      eventStr(ev, "channel_type") == string(model.ChannelTypeDirect),
		CreateAt:  p.CreateAt,
		Permalink: e.permalink(ev, p.Id),
	}
}

// runExec runs a rule's command with the post envelope on stdin (as JSON) and
// the key fields exported as MATTERBOX_* environment variables, bounded by
// execTimeout. Output is logged (truncated); a failure is logged, not fatal.
func (e *Engine) runExec(ctx context.Context, ev *model.WebSocketEvent, p *model.Post, a Action) {
	defer e.wg.Done()
	env := e.buildEnvelope(ev, p)
	payload, err := json.Marshal(env)
	if err != nil {
		e.log.Printf("rule exec: marshal envelope: %v", err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, a.Command[0], a.Command[1:]...)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = execEnv(env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.log.Printf("rule exec %v failed: %v (%s)", a.Command, err, truncateForLog(string(out)))
		return
	}
	e.log.Printf("rule exec %v ok%s", a.Command, logSuffix(string(out)))
}

// execEnv builds the child environment: the parent's plus the MATTERBOX_* post
// fields, so a script can read either stdin JSON or individual variables.
func execEnv(env envelope) []string {
	return append(os.Environ(),
		"MATTERBOX_POST_ID="+env.PostID,
		"MATTERBOX_CHANNEL_ID="+env.ChannelID,
		"MATTERBOX_CHANNEL="+env.Channel,
		"MATTERBOX_AUTHOR="+env.Author,
		"MATTERBOX_MESSAGE="+env.Message,
		"MATTERBOX_IS_DM="+boolStr(env.IsDM),
		"MATTERBOX_PERMALINK="+env.Permalink,
	)
}

// runWebhook POSTs the post envelope as JSON to the configured URL, bounded by
// webhookTimeout. Non-2xx and transport errors are logged, not fatal.
func (e *Engine) runWebhook(ctx context.Context, ev *model.WebSocketEvent, p *model.Post, a Action) {
	defer e.wg.Done()
	payload, err := json.Marshal(e.buildEnvelope(ev, p))
	if err != nil {
		e.log.Printf("rule webhook: marshal envelope: %v", err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, a.URL, bytes.NewReader(payload))
	if err != nil {
		e.log.Printf("rule webhook: build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.log.Printf("rule webhook %s failed: %v", a.URL, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e.log.Printf("rule webhook %s: status %d", a.URL, resp.StatusCode)
		return
	}
	e.log.Printf("rule webhook %s ok (%d)", a.URL, resp.StatusCode)
}

// runReact adds an emoji reaction to the triggering post.
func (e *Engine) runReact(ctx context.Context, p *model.Post, a Action) {
	defer e.wg.Done()
	if e.me == nil {
		return
	}
	if err := e.client.AddReaction(ctx, e.me.Id, p.Id, a.Emoji); err != nil {
		e.log.Printf("rule react %q on %s: %v", a.Emoji, p.Id, err)
	}
}

// runMarkRead marks the triggering post's channel read.
func (e *Engine) runMarkRead(ctx context.Context, p *model.Post) {
	defer e.wg.Done()
	if e.me == nil {
		return
	}
	if err := e.client.ViewChannel(ctx, e.me.Id, p.ChannelId); err != nil {
		e.log.Printf("rule mark_read %s: %v", p.ChannelId, err)
	}
}

// runLog writes a single line about the matched post to the daemon log.
func (e *Engine) runLog(ev *model.WebSocketEvent, p *model.Post, a Action) {
	prefix := strings.TrimSpace(a.Text)
	if prefix == "" {
		prefix = "rule matched"
	}
	label := channelLabel(ev, "")
	e.log.Printf("%s: %s — %s", prefix, label, truncateForLog(p.Message))
}

// logSuffix renders trailing command output for a one-line log entry, or "" if
// the command was quiet.
func logSuffix(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	return ": " + truncateForLog(out)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
