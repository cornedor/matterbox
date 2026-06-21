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
	"strconv"
	"strings"
	"text/template"
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
	ActionNotify    = "notify"     // summarise + deliver to Telegram (the legacy bridge)
	ActionExec      = "exec"       // run a local command, post piped in as JSON
	ActionWebhook   = "webhook"    // HTTP POST the post envelope as JSON
	ActionReact     = "react"      // add an emoji reaction to the post
	ActionMarkRead  = "mark_read"  // mark the post's channel read
	ActionLog       = "log"        // write a line to the daemon log
	ActionStateSet  = "state_set"  // write a value into the persistent rule ledger
	ActionStateIncr = "state_incr" // add to an integer value in the ledger
	ActionStateDel  = "state_del"  // remove a key from the ledger
)

// validActionTypes lists every accepted action type for the unknown-type error.
var validActionTypes = []string{
	ActionNotify, ActionExec, ActionWebhook, ActionReact, ActionMarkRead,
	ActionLog, ActionStateSet, ActionStateIncr, ActionStateDel,
}

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
	// Channels is a list of case-insensitive globs (*, ?) over the channel's
	// display name, or exact channel ids; a post matches the condition when it
	// matches ANY entry (OR). Empty matches any channel.
	Channels []string
	// Authors is a list of usernames (without the leading @), matched
	// case-insensitively against the post's sender; a post matches when the
	// sender equals ANY entry (OR). Empty matches any author.
	Authors []string
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
	// Not, when set, inverts a nested match: the rule matches only when the post
	// does NOT satisfy this sub-match. Lets a rule say "everything in #ops
	// except from the bots" in one place. Recursive.
	Not *MatchSpec
	// Frequency, when set, gates the rule on a rolling-window threshold on top
	// of the field conditions: even once the fields match, the actions run only
	// on the post that brings the count of recent matches (within the window) up
	// to Count. Lets a rule fire on a burst — "3 sev-1s in 10 minutes" — instead
	// of every matching message. The window is in-memory and live-only (see
	// frequency).
	Frequency *FrequencySpec
	// State conditions match against the persistent ledger (the state_* actions):
	// every condition must hold (AND). Lets a rule react to accumulated context —
	// "this author's failure_count is at least 3" — that no single message
	// carries. Within one post the ledger is re-read after a rule mutates it, so
	// a rule can match on a counter an earlier rule just incremented.
	State []StateCondSpec
}

// FrequencySpec is the config form of a rule's rolling-window gate.
type FrequencySpec struct {
	// Count is how many matches within the window must accumulate before the
	// rule fires. 1 (or unset) is effectively no gate.
	Count int
	// Within is the window length as a Go duration string ("10m", "1h30m").
	Within string
	// By groups the counting: "author" counts per sender, "channel" per channel,
	// and "global" (the default, also "" ) counts every match together.
	By string
}

// StateCondSpec is one condition on a ledger key. Key is required; every set
// operator is ANDed (e.g. gte:1 + lt:10 is a range). Comparisons treat the
// stored value as text (Eq/Ne) or, for the inequalities, as a number — a
// non-numeric value never satisfies a numeric comparison.
type StateCondSpec struct {
	Key    string   // the ledger key to read
	Exists *bool    // require the key be present (true) or absent (false)
	Eq     *string  // value equals this exactly (string compare)
	Ne     *string  // value differs from this
	Gt     *float64 // value (as a number) >  this
	Gte    *float64 // value (as a number) >= this
	Lt     *float64 // value (as a number) <  this
	Lte    *float64 // value (as a number) <= this
}

// ActionSpec is the config form of one action.
type ActionSpec struct {
	// Type is one of the Action* ids.
	Type string
	// Summarize (notify) overrides the daemon's summarize setting for this rule
	// only. Nil inherits Options.Summarize.
	Summarize *bool
	// Urgent (notify) delivers even during quiet hours and for muted channels —
	// the do-not-disturb bypass for an on-call keyword. Self/DM gating still
	// applies. Default false.
	Urgent bool
	// ChatID (notify) overrides the destination Telegram chat for this rule.
	// Empty uses telegram.chat_id. A rule sending to a non-default chat does not
	// get the two-way reply buttons (those only work for the configured chat).
	ChatID string
	// Command (exec) is the argv to run; the post envelope is piped to its
	// stdin as JSON and the key fields are exported as MATTERBOX_* env vars.
	Command []string
	// URL (webhook) is POSTed the post envelope as a JSON body.
	URL string
	// Headers (webhook) are extra HTTP headers sent with the POST; values are
	// expanded with the daemon's environment ($TOKEN / ${TOKEN}) so secrets stay
	// out of the config file. Use e.g. Authorization for authenticated endpoints.
	Headers map[string]string
	// Emoji (react) is the Mattermost emoji shortcode to add, without colons.
	Emoji string
	// Text (log) is an optional prefix for the log line.
	Text string
	// Key (state_set/state_incr/state_del) is the ledger key to act on. It is a
	// Go text/template expanded against the post (e.g. "failures:{{ .author }}"
	// for a per-author counter), so one rule can address many keys.
	Key string
	// Value (state_set) is the value to store, also a text/template over the
	// post and the current state (e.g. "{{ .create_at }}", "{{ .state.count }}").
	Value string
	// By (state_incr) is the amount to add; nil means 1. Negative decrements.
	By *int
}

// Rule is a compiled RuleSpec.
type Rule struct {
	Name    string
	Stop    bool
	Match   Match
	Actions []Action
	// mutatesState is true when any action writes the ledger, so applyRules knows
	// to re-read the snapshot after this rule for the benefit of later `state`
	// conditions. Precomputed so the hot path doesn't rescan the actions.
	mutatesState bool
}

// Match is a compiled MatchSpec: the globs/regexps are pre-compiled and the
// original strings kept for an exact-id fallback.
type Match struct {
	// builtin selects the legacy isDirectMention trigger instead of the field
	// matcher; set only by defaultRules so the default notification behaviour is
	// reproduced byte-for-byte. Never set from user config.
	builtin bool

	channelsRaw []string
	channelRes  []*regexp.Regexp
	authors     []string
	messageRe   *regexp.Regexp
	mention     bool
	dm          *bool
	hasFile     bool
	isThread    *bool
	not         *Match
	// freq is the compiled rolling-window gate, applied by the engine after the
	// field conditions pass (matchPost never reads it — it is stateful and must
	// not run during catch-up replay or pure match tests).
	freq *frequency
	// state holds the compiled ledger conditions; matchPost evaluates them against
	// the snapshot it is handed (so it stays pure and testable).
	state []stateCond
}

// frequency is a compiled FrequencySpec: a rolling-window threshold. The engine
// keeps a per-(rule, group) window of recent match times in memory; a rule
// fires on the match that fills the window to count, then the window resets so
// it re-arms only after another full burst.
type frequency struct {
	count  int
	within time.Duration
	by     string // "author" | "channel" | "global"
}

// stateCond is a compiled StateCondSpec — one condition on a ledger key. eval
// reports whether the snapshot satisfies it.
type stateCond struct {
	key              string
	exists           *bool
	eq, ne           *string
	gt, gte, lt, lte *float64
}

// eval reports whether the ledger snapshot satisfies this condition. An absent
// key has no value, so any value comparison (eq/ne/numeric) against it fails;
// use exists:false to match absence. A non-numeric stored value never satisfies
// a numeric comparison.
func (c stateCond) eval(state map[string]string) bool {
	val, present := state[c.key]
	if c.exists != nil && *c.exists != present {
		return false
	}
	if c.eq != nil && val != *c.eq {
		return false
	}
	if c.ne != nil && val == *c.ne {
		return false
	}
	if c.gt != nil || c.gte != nil || c.lt != nil || c.lte != nil {
		n, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			return false
		}
		if c.gt != nil && !(n > *c.gt) {
			return false
		}
		if c.gte != nil && !(n >= *c.gte) {
			return false
		}
		if c.lt != nil && !(n < *c.lt) {
			return false
		}
		if c.lte != nil && !(n <= *c.lte) {
			return false
		}
	}
	return true
}

// Action is a compiled ActionSpec. The state actions carry their key/value as
// pre-parsed templates so a syntax error in a template is caught at startup,
// like a bad regexp or glob.
type Action struct {
	Type      string
	Summarize *bool
	Urgent    bool
	ChatID    string
	Command   []string
	URL       string
	Headers   map[string]string
	Emoji     string
	Text      string

	// State action fields. keyTmpl/valueTmpl are the compiled forms of the
	// Key/Value strings; by is the resolved state_incr amount (default 1).
	keyTmpl   *template.Template
	valueTmpl *template.Template
	by        int64
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
		rules = append(rules, Rule{
			Name: name, Stop: s.Stop, Match: m, Actions: actions,
			mutatesState: actionsMutateState(actions),
		})
	}
	return rules, nil
}

// actionsMutateState reports whether any action writes the ledger.
func actionsMutateState(actions []Action) bool {
	for _, a := range actions {
		switch a.Type {
		case ActionStateSet, ActionStateIncr, ActionStateDel:
			return true
		}
	}
	return false
}

func compileMatch(s MatchSpec) (Match, error) {
	m := Match{
		channelsRaw: s.Channels,
		authors:     s.Authors,
		mention:     s.Mention,
		dm:          s.DM,
		hasFile:     s.HasFile,
		isThread:    s.IsThread,
	}
	for _, ch := range s.Channels {
		if ch == "" {
			continue
		}
		re, err := globToRegexp(ch)
		if err != nil {
			return Match{}, fmt.Errorf("bad channel glob %q: %w", ch, err)
		}
		m.channelRes = append(m.channelRes, re)
	}
	if s.Message != "" {
		re, err := regexp.Compile(s.Message)
		if err != nil {
			return Match{}, fmt.Errorf("bad message regexp %q: %w", s.Message, err)
		}
		m.messageRe = re
	}
	if s.Not != nil {
		nm, err := compileMatch(*s.Not)
		if err != nil {
			return Match{}, fmt.Errorf("not: %w", err)
		}
		m.not = &nm
	}
	if s.Frequency != nil {
		f, err := compileFrequency(*s.Frequency)
		if err != nil {
			return Match{}, fmt.Errorf("frequency: %w", err)
		}
		m.freq = f
	}
	for _, sc := range s.State {
		c, err := compileStateCond(sc)
		if err != nil {
			return Match{}, fmt.Errorf("state: %w", err)
		}
		m.state = append(m.state, c)
	}
	return m, nil
}

// compileFrequency validates and normalises a FrequencySpec. A bad duration,
// non-positive count, or unknown grouping is a startup error so the gate can't
// silently misbehave.
func compileFrequency(s FrequencySpec) (*frequency, error) {
	if s.Count < 1 {
		return nil, fmt.Errorf("count must be >= 1, got %d", s.Count)
	}
	within, err := time.ParseDuration(s.Within)
	if err != nil {
		return nil, fmt.Errorf("bad within %q: %w", s.Within, err)
	}
	if within <= 0 {
		return nil, fmt.Errorf("within must be positive, got %s", within)
	}
	by := strings.ToLower(strings.TrimSpace(s.By))
	switch by {
	case "", "global":
		by = "global"
	case "author", "channel":
		// ok
	default:
		return nil, fmt.Errorf("unknown by %q (want author, channel, or global)", s.By)
	}
	return &frequency{count: s.Count, within: within, by: by}, nil
}

// compileStateCond validates a StateCondSpec: a key is required, and at least
// one operator must be set (a bare key would match nothing meaningful). A
// gt/gte and lt/lte pair forms a range.
func compileStateCond(s StateCondSpec) (stateCond, error) {
	if strings.TrimSpace(s.Key) == "" {
		return stateCond{}, fmt.Errorf("condition needs a key")
	}
	if s.Exists == nil && s.Eq == nil && s.Ne == nil &&
		s.Gt == nil && s.Gte == nil && s.Lt == nil && s.Lte == nil {
		return stateCond{}, fmt.Errorf("condition on %q needs an operator (exists, eq, ne, gt, gte, lt, lte)", s.Key)
	}
	return stateCond{
		key:    s.Key,
		exists: s.Exists,
		eq:     s.Eq,
		ne:     s.Ne,
		gt:     s.Gt,
		gte:    s.Gte,
		lt:     s.Lt,
		lte:    s.Lte,
	}, nil
}

func compileAction(a ActionSpec) (Action, error) {
	ca := Action{
		Type:      a.Type,
		Summarize: a.Summarize,
		Urgent:    a.Urgent,
		ChatID:    a.ChatID,
		Command:   a.Command,
		URL:       a.URL,
		Headers:   a.Headers,
		Emoji:     strings.Trim(a.Emoji, ": "),
		Text:      a.Text,
	}
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
	case ActionStateSet:
		if strings.TrimSpace(a.Key) == "" {
			return Action{}, fmt.Errorf("state_set action needs a key")
		}
		kt, err := compileTemplate(a.Key)
		if err != nil {
			return Action{}, fmt.Errorf("state_set: bad key template: %w", err)
		}
		vt, err := compileTemplate(a.Value)
		if err != nil {
			return Action{}, fmt.Errorf("state_set: bad value template: %w", err)
		}
		ca.keyTmpl, ca.valueTmpl = kt, vt
	case ActionStateIncr:
		if strings.TrimSpace(a.Key) == "" {
			return Action{}, fmt.Errorf("state_incr action needs a key")
		}
		kt, err := compileTemplate(a.Key)
		if err != nil {
			return Action{}, fmt.Errorf("state_incr: bad key template: %w", err)
		}
		ca.keyTmpl = kt
		ca.by = 1
		if a.By != nil {
			ca.by = int64(*a.By)
		}
	case ActionStateDel:
		if strings.TrimSpace(a.Key) == "" {
			return Action{}, fmt.Errorf("state_del action needs a key")
		}
		kt, err := compileTemplate(a.Key)
		if err != nil {
			return Action{}, fmt.Errorf("state_del: bad key template: %w", err)
		}
		ca.keyTmpl = kt
	case "":
		return Action{}, fmt.Errorf("action has no type")
	default:
		return Action{}, fmt.Errorf("unknown action type %q (want one of: %s)",
			a.Type, strings.Join(validActionTypes, ", "))
	}
	return ca, nil
}

// compileTemplate parses a state action's key/value template. missingkey=zero
// renders an absent field (or absent state key) as the zero value — an empty
// string — instead of "<no value>", so a template referencing a not-yet-set
// counter degrades quietly.
func compileTemplate(text string) (*template.Template, error) {
	return template.New("state").Option("missingkey=zero").Parse(text)
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

// ruleHasNotify reports whether a rule can deliver a Telegram notification.
func ruleHasNotify(r Rule) bool {
	for _, a := range r.Actions {
		if a.Type == ActionNotify {
			return true
		}
	}
	return false
}

// hasNotifyRule reports whether any compiled rule can notify. It gates the
// reconnect catch-up: a daemon with no notify rule (cache-warmer only) skips it.
func (e *Engine) hasNotifyRule() bool {
	for _, r := range e.rules {
		if ruleHasNotify(r) {
			return true
		}
	}
	return false
}

// notifyMatches reports whether some rule with a notify action matches the
// post — i.e. whether a live post would have produced a notification. The
// catch-up path uses it to replay missed mentions/DMs through the user's actual
// rules instead of the old hardcoded mention/DM test, so a config that, say,
// only notifies for one channel no longer gets a catch-up digest for the rest.
func (e *Engine) notifyMatches(ev *model.WebSocketEvent, p *model.Post) bool {
	state := e.matchState()
	for _, r := range e.rules {
		if ruleHasNotify(r) && e.matches(ev, p, r.Match, state) {
			return true
		}
	}
	return false
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
	// The ledger snapshot is read once up front (nil when no rule uses `state`),
	// then refreshed after any rule whose actions wrote state, so a later rule's
	// `state` condition observes what an earlier rule's state_incr just stored.
	state := e.matchState()
	for i := range e.rules {
		r := e.rules[i]
		if !e.matches(ev, p, r.Match, state) {
			continue
		}
		// A frequency gate is applied only once the fields match: it records the
		// hit and reports whether the window has now reached the threshold. When
		// it hasn't, the rule neither runs its actions nor honours Stop — it is as
		// if this post didn't match it, so later rules still get their turn.
		if r.Match.freq != nil && !e.frequencyAllows(i, r.Match.freq, ev, p) {
			continue
		}
		e.runActions(ctx, ev, p, r.Actions)
		if e.usesState && r.mutatesState {
			state = e.matchState()
		}
		if r.Stop {
			return
		}
	}
}

// frequencyAllows records this post as a hit for the rule's window and reports
// whether the rule should fire now. It keeps a sliding window of recent hit
// times per (rule, group); a hit that fills the window to count fires the rule
// and clears the window, so the rule re-arms only after another full burst
// rather than firing on every subsequent message. The window lives only in
// memory and is live-only: it is empty after a restart and is never touched by
// the reconnect catch-up (which replays history and would otherwise corrupt it).
func (e *Engine) frequencyAllows(ruleIdx int, f *frequency, ev *model.WebSocketEvent, p *model.Post) bool {
	key := freqBucketKey(ruleIdx, f, ev, p)
	now := e.clock()
	cutoff := now.Add(-f.within)

	e.freqMu.Lock()
	defer e.freqMu.Unlock()
	if e.freqWindows == nil {
		e.freqWindows = make(map[string][]time.Time)
	}
	// Drop expired hits in place, then record this one.
	win := e.freqWindows[key]
	kept := win[:0]
	for _, t := range win {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	if len(kept) >= f.count {
		delete(e.freqWindows, key) // fired: reset so it re-arms on a fresh burst
		return true
	}
	e.freqWindows[key] = kept
	return false
}

// freqBucketKey is the window key for a rule's frequency gate: the rule index
// (so two rules never share a window) joined with the group value selected by
// `by`. The NUL separator can't appear in a username or channel id, so distinct
// (rule, group) pairs never collide.
func freqBucketKey(ruleIdx int, f *frequency, ev *model.WebSocketEvent, p *model.Post) string {
	var group string
	switch f.by {
	case "author":
		group = strings.TrimPrefix(eventStr(ev, "sender_name"), "@")
	case "channel":
		group = p.ChannelId
	default: // global
		group = ""
	}
	return strconv.Itoa(ruleIdx) + "\x00" + group
}

// matches reports whether a post satisfies a rule's conditions. The builtin
// match defers to isDirectMention so the default rule behaves identically to
// the pre-rules daemon; user rules use the field matcher. state is the ledger
// snapshot the field matcher tests `state` conditions against (nil when no rule
// uses state — see matchState).
func (e *Engine) matches(ev *model.WebSocketEvent, p *model.Post, m Match, state map[string]string) bool {
	meID, meName := "", ""
	if e.me != nil {
		meID, meName = e.me.Id, e.me.Username
	}
	if m.builtin {
		return isDirectMention(ev, p, meID, meName, e.opts.NotifySelf)
	}
	return matchPost(ev, p, m, meID, meName, state)
}

// matchPost evaluates the field conditions of a (non-builtin) match. Pure for
// testability: every condition it reads comes from the event/post, the reader's
// id and username for the Mention check, plus the ledger snapshot for `state`
// conditions.
func matchPost(ev *model.WebSocketEvent, p *model.Post, m Match, meID, meName string, state map[string]string) bool {
	isDM := eventStr(ev, "channel_type") == string(model.ChannelTypeDirect)
	if m.dm != nil && *m.dm != isDM {
		return false
	}
	if m.mention && !(wsMentions(ev)[meID] && mentionsName(p.Message, meName)) {
		return false
	}
	if len(m.authors) > 0 {
		sender := strings.TrimPrefix(eventStr(ev, "sender_name"), "@")
		if !matchesAny(sender, m.authors) {
			return false
		}
	}
	if len(m.channelRes) > 0 {
		name := eventStr(ev, "channel_display_name")
		if !matchesChannel(name, p.ChannelId, m) {
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
	for _, c := range m.state {
		if !c.eval(state) {
			return false
		}
	}
	// A nested not: matches the whole post only when the sub-match does not.
	if m.not != nil && matchPost(ev, p, *m.not, meID, meName, state) {
		return false
	}
	return true
}

// matchesAny reports whether sender equals any of authors (case-insensitive).
func matchesAny(sender string, authors []string) bool {
	for _, a := range authors {
		if a != "" && strings.EqualFold(sender, a) {
			return true
		}
	}
	return false
}

// matchesChannel reports whether the channel display name matches any of the
// compiled globs, or its id equals any raw entry (the exact-id fallback).
func matchesChannel(name, channelID string, m Match) bool {
	for _, re := range m.channelRes {
		if re.MatchString(name) {
			return true
		}
	}
	for _, raw := range m.channelsRaw {
		if raw == channelID {
			return true
		}
	}
	return false
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
			e.notifyGate(ctx, ev, p, e.notifyOptsFor(a))
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
		case ActionStateSet, ActionStateIncr, ActionStateDel:
			// State writes run inline, in order: they are quick local SQLite
			// writes, and running them synchronously means a later exec/webhook in
			// the same rule observes the values this rule just wrote.
			e.runState(ev, p, a)
		}
	}
}

// notifyOpts carries a notify action's resolved delivery settings through the
// gate into the delivery goroutine.
type notifyOpts struct {
	summarize bool   // LLM summary (true) vs raw message text (false)
	urgent    bool   // bypass quiet hours + muted-channel suppression
	chatID    string // destination chat; "" → the configured telegram.chat_id
}

// notifyOptsFor resolves a notify action's effective delivery settings: the
// per-rule overrides if present, else the daemon defaults.
func (e *Engine) notifyOptsFor(a Action) notifyOpts {
	summarize := e.opts.Summarize
	if a.Summarize != nil {
		summarize = *a.Summarize
	}
	chatID := a.ChatID
	if chatID == "" {
		chatID = e.opts.TelegramChatID
	}
	return notifyOpts{summarize: summarize, urgent: a.Urgent, chatID: chatID}
}

// notifyGate applies the notification policy (skip own messages, muted
// channels, and quiet hours, and honour notify_dms) before spinning off the
// delivery. This is the same gate the pre-rules daemon applied inline in
// handle(); centralising it here means every notify action — default or
// user-defined — respects the same do-not-disturb settings. An urgent action
// bypasses the quiet-hours and muted-channel suppression (but not the self / DM
// gates), so an on-call keyword still pages while you're heads-down.
func (e *Engine) notifyGate(ctx context.Context, ev *model.WebSocketEvent, p *model.Post, opts notifyOpts) {
	if e.me != nil && p.UserId == e.me.Id && !e.opts.NotifySelf {
		return
	}
	if eventStr(ev, "channel_type") == string(model.ChannelTypeDirect) && !e.opts.NotifyDMs {
		return
	}
	if !opts.urgent {
		if e.opts.RespectMutes && e.isMuted(p.ChannelId) {
			e.log.Printf("mention in muted channel %s — skipped", p.ChannelId)
			return
		}
		if e.inQuietHoursNow() {
			e.log.Printf("mention during quiet hours — skipped (cached; use /unread)")
			return
		}
	}
	e.wg.Add(1)
	go e.notify(ctx, ev, p, opts)
}

// envelope is the JSON view of a post passed to exec/webhook actions. It is
// intentionally flat and stable so scripts can depend on it: fields are only
// ever added, never renamed or removed.
type envelope struct {
	PostID    string   `json:"post_id"`
	ChannelID string   `json:"channel_id"`
	Channel   string   `json:"channel"`
	TeamID    string   `json:"team_id,omitempty"`
	Team      string   `json:"team,omitempty"`
	Author    string   `json:"author"`
	Message   string   `json:"message"`
	IsDM      bool     `json:"is_dm"`
	IsThread  bool     `json:"is_thread"`
	RootID    string   `json:"root_id,omitempty"`
	Mentioned bool     `json:"mentioned"`
	Files     []string `json:"files,omitempty"`
	CreateAt  int64    `json:"create_at"`
	Permalink string   `json:"permalink,omitempty"`
	// State is a snapshot of the persistent rule ledger (the state_* actions),
	// so a script or webhook receives every stored value alongside the post.
	State map[string]string `json:"state,omitempty"`
}

// buildEnvelope assembles the exec/webhook payload from the event + post,
// using only data already in hand (no extra API calls on the ingest path).
func (e *Engine) buildEnvelope(ev *model.WebSocketEvent, p *model.Post) envelope {
	meID, meName := "", ""
	if e.me != nil {
		meID, meName = e.me.Id, e.me.Username
	}
	teamID := eventStr(ev, "team_id")
	e.teamsMu.RLock()
	team := e.teams[teamID]
	e.teamsMu.RUnlock()
	return envelope{
		PostID:    p.Id,
		ChannelID: p.ChannelId,
		Channel:   eventStr(ev, "channel_display_name"),
		TeamID:    teamID,
		Team:      team,
		Author:    strings.TrimPrefix(eventStr(ev, "sender_name"), "@"),
		Message:   p.Message,
		IsDM:      eventStr(ev, "channel_type") == string(model.ChannelTypeDirect),
		IsThread:  p.RootId != "" && p.RootId != p.Id,
		RootID:    p.RootId,
		Mentioned: wsMentions(ev)[meID] && mentionsName(p.Message, meName),
		Files:     postFileNames(p),
		CreateAt:  p.CreateAt,
		Permalink: e.permalink(ev, p.Id),
		State:     e.loadState(),
	}
}

// loadState reads the whole rule ledger for inclusion in an envelope. A read
// error (or a daemon with no cache) degrades to no state rather than dropping
// the post entirely.
func (e *Engine) loadState() map[string]string {
	st, err := e.store.AllState()
	if err != nil {
		e.log.Printf("rule state: load: %v", err)
		return nil
	}
	return st
}

// matchState returns the ledger snapshot used to evaluate `state` conditions,
// or nil when no rule has any — so a config that never matches on state pays no
// per-message read.
func (e *Engine) matchState() map[string]string {
	if !e.usesState {
		return nil
	}
	return e.loadState()
}

// rulesUseState reports whether any rule (including nested not: blocks) carries
// a state condition; the engine caches it as usesState.
func rulesUseState(rules []Rule) bool {
	for _, r := range rules {
		if matchUsesState(r.Match) {
			return true
		}
	}
	return false
}

func matchUsesState(m Match) bool {
	if len(m.state) > 0 {
		return true
	}
	return m.not != nil && matchUsesState(*m.not)
}

// postFileNames returns the attachment filenames carried in the post metadata,
// or nil. Uses only embedded metadata (no fetch); a post that has FileIds but
// no metadata reports no names but still trips has_file / the file count.
func postFileNames(p *model.Post) []string {
	if p.Metadata == nil || len(p.Metadata.Files) == 0 {
		return nil
	}
	names := make([]string, 0, len(p.Metadata.Files))
	for _, f := range p.Metadata.Files {
		names = append(names, f.Name)
	}
	return names
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
// fields, so a script can read either stdin JSON or individual variables. The
// rule ledger is exposed both as MATTERBOX_STATE (the whole map as JSON) and as
// one MATTERBOX_STATE_<KEY> per entry for a quick `$MATTERBOX_STATE_FOO` read.
func execEnv(env envelope) []string {
	out := append(os.Environ(),
		"MATTERBOX_POST_ID="+env.PostID,
		"MATTERBOX_CHANNEL_ID="+env.ChannelID,
		"MATTERBOX_CHANNEL="+env.Channel,
		"MATTERBOX_TEAM_ID="+env.TeamID,
		"MATTERBOX_TEAM="+env.Team,
		"MATTERBOX_AUTHOR="+env.Author,
		"MATTERBOX_MESSAGE="+env.Message,
		"MATTERBOX_IS_DM="+boolStr(env.IsDM),
		"MATTERBOX_IS_THREAD="+boolStr(env.IsThread),
		"MATTERBOX_ROOT_ID="+env.RootID,
		"MATTERBOX_MENTIONED="+boolStr(env.Mentioned),
		"MATTERBOX_FILES="+strings.Join(env.Files, ","),
		"MATTERBOX_PERMALINK="+env.Permalink,
	)
	if len(env.State) > 0 {
		if b, err := json.Marshal(env.State); err == nil {
			out = append(out, "MATTERBOX_STATE="+string(b))
		}
		for k, v := range env.State {
			if name := envKeySanitize(k); name != "" {
				out = append(out, "MATTERBOX_STATE_"+name+"="+v)
			}
		}
	}
	return out
}

// envKeySanitize maps a ledger key to a usable environment-variable suffix:
// upper-cased, with every non-alphanumeric run collapsed to a single
// underscore. A key that sanitizes to empty (or starts with a digit) is exposed
// only via MATTERBOX_STATE, not its own variable.
func envKeySanitize(key string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(key) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return ""
	}
	return s
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
	// Custom headers (e.g. Authorization) override the default; values are
	// expanded from the daemon's environment so a token can live in $TOKEN
	// rather than the config file.
	for k, v := range a.Headers {
		req.Header.Set(k, os.ExpandEnv(v))
	}
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

// runState applies a state_set / state_incr / state_del action against the
// persistent ledger. The key (and, for state_set, the value) are templates
// expanded against the post and current state, so one rule can address per-author
// or per-channel keys. Failures are logged, never fatal.
func (e *Engine) runState(ev *model.WebSocketEvent, p *model.Post, a Action) {
	key, err := e.renderTemplate(a.keyTmpl, ev, p)
	if err != nil {
		e.log.Printf("rule %s: render key: %v", a.Type, err)
		return
	}
	if strings.TrimSpace(key) == "" {
		e.log.Printf("rule %s: key rendered empty — skipped", a.Type)
		return
	}
	switch a.Type {
	case ActionStateSet:
		val, err := e.renderTemplate(a.valueTmpl, ev, p)
		if err != nil {
			e.log.Printf("rule state_set %q: render value: %v", key, err)
			return
		}
		if err := e.store.SetState(key, val); err != nil {
			e.log.Printf("rule state_set %q: %v", key, err)
		}
	case ActionStateIncr:
		n, err := e.store.IncrState(key, a.by)
		if err != nil {
			e.log.Printf("rule state_incr %q: %v", key, err)
			return
		}
		e.log.Printf("rule state_incr %q = %d", key, n)
	case ActionStateDel:
		if err := e.store.DeleteState(key); err != nil {
			e.log.Printf("rule state_del %q: %v", key, err)
		}
	}
}

// renderTemplate expands a state action's compiled template against the post.
// The template data is the exec/webhook envelope (so `{{ .author }}`,
// `{{ .create_at }}`, … match the documented field names) plus the current
// ledger under `.state` (so `{{ .state.failure_count }}` reads a counter).
func (e *Engine) renderTemplate(t *template.Template, ev *model.WebSocketEvent, p *model.Post) (string, error) {
	if t == nil {
		return "", nil
	}
	env := e.buildEnvelope(ev, p)
	data, err := envelopeMap(env)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// envelopeMap turns the envelope into the generic map a text/template walks,
// keyed by the same snake_case names the JSON payload uses. Round-tripping
// through JSON keeps the template field names identical to the documented exec/
// webhook contract — one source of truth for both.
func envelopeMap(env envelope) (map[string]any, error) {
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	// UseNumber keeps create_at and friends as their exact integer text — a plain
	// decode would widen them to float64 and render "{{ .create_at }}" in
	// scientific notation (1.7e+12 instead of 1700000000000).
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if _, ok := m["state"]; !ok {
		m["state"] = map[string]string{} // always present so {{ .state.x }} is safe
	}
	return m, nil
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
