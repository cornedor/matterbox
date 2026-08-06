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

	"matterbox/internal/control"
	"matterbox/internal/mm"
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
	ActionSend      = "send"       // post a message (to the trigger's channel or a configured one)
	ActionLog       = "log"        // write a line to the daemon log
	ActionStateSet  = "state_set"  // write a value into the persistent rule ledger
	ActionStateIncr = "state_incr" // add to an integer value in the ledger
	ActionStateDel  = "state_del"  // remove a key from the ledger
)

// validActionTypes lists every accepted action type for the unknown-type error.
var validActionTypes = []string{
	ActionNotify, ActionExec, ActionWebhook, ActionReact, ActionMarkRead,
	ActionSend, ActionLog, ActionStateSet, ActionStateIncr, ActionStateDel,
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
	// Teams is a list of case-insensitive globs (*, ?) over the team's URL name
	// (the slug in the channel URL, e.g. "core"), or exact team ids; a post
	// matches when its team matches ANY entry (OR). Empty matches any team. A
	// direct message carries no team, so a Teams condition never matches a DM.
	Teams []string
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
	// FromMe, when non-nil, requires the post be the reader's own (true) or
	// someone else's (false). Nil matches either. It is the self-test the notify
	// and send actions apply internally, surfaced as a match condition so the
	// other actions (exec/webhook/react) can gate on it too — `from_me: false`
	// keeps a DM rule from firing on your own outgoing messages.
	FromMe *bool
	// HasFile requires at least one attached file.
	HasFile bool
	// IsThread, when non-nil, requires the post be a thread reply (true) or a
	// root post (false). Nil matches either.
	IsThread *bool
	// Viewing, when non-nil, requires that you are (true) or are not (false)
	// looking at the post's channel right now: a matterbox TUI on this machine
	// has it open and its terminal has focus. `viewing: false` is what keeps a
	// desktop-notification rule quiet about the conversation you're already
	// reading. The daemon asks the TUI over its control socket; no TUI (or a
	// daemon on another machine) means you aren't viewing anything, so a
	// `viewing: false` rule keeps firing exactly as it did before.
	Viewing *bool
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
	// Cooldown, when set, gates the rule to fire at most once per interval: even
	// once the fields match, the actions run only if the rule hasn't fired within
	// the last Every (optionally per author/channel/team). The last-fire time is
	// persisted, so the interval survives a restart. Use it for "do this every N
	// days/weeks" — the general form of a once-per-day greeting.
	Cooldown *CooldownSpec
	// State conditions match against the persistent ledger (the state_* actions):
	// every condition must hold (AND). Lets a rule react to accumulated context —
	// "this author's failure_count is at least 3" — that no single message
	// carries. Within one post the ledger is re-read after a rule mutates it, so
	// a rule can match on a counter an earlier rule just incremented.
	State []StateCondSpec
}

// CooldownSpec is the config form of a rule's minimum-interval gate: the
// inverse of FrequencySpec. Where frequency fires on a burst, cooldown fires at
// most once per Every, then stays quiet — "greet every 2 days", "digest weekly".
// Unlike the in-memory frequency window, the last-fire time is persisted, so the
// interval is honoured across a daemon restart.
type CooldownSpec struct {
	// Every is the minimum time between firings as a Go duration string ("48h",
	// "168h", "30m"). Required.
	Every string
	// By groups the cooldown: "author" keeps a separate interval per sender,
	// "channel" per channel, "team" per team, and "global" (the default, also "")
	// is one interval for the whole rule.
	By string
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
	// Command (exec) is the argv to run; each element is a text/template over the
	// post (e.g. "{{ .author }} sent a message"), so an argument can carry post
	// fields. The post envelope is also piped to its stdin as JSON and the key
	// fields are exported as MATTERBOX_* env vars.
	Command []string
	// URL (webhook) is POSTed the post envelope as a JSON body.
	URL string
	// Headers (webhook) are extra HTTP headers sent with the POST; values are
	// expanded with the daemon's environment ($TOKEN / ${TOKEN}) so secrets stay
	// out of the config file. Use e.g. Authorization for authenticated endpoints.
	Headers map[string]string
	// Emoji (react) is the Mattermost emoji shortcode to add, without colons.
	Emoji string
	// Text is the optional prefix for the log line (log), or the message body —
	// a text/template over the post — for the send action.
	Text string
	// Channel (send) is the target channel: "team/channel" or "@user". Empty
	// posts into the same channel the triggering message arrived in.
	Channel string
	// Thread (send) posts the message as a reply in the triggering post's thread
	// instead of a new top-level post. Ignored when Channel is set (a configured
	// target channel is not the trigger's thread).
	Thread bool
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
	teamsRaw    []string
	teamRes     []*regexp.Regexp
	authors     []string
	messageRe   *regexp.Regexp
	mention     bool
	dm          *bool
	fromMe      *bool
	hasFile     bool
	isThread    *bool
	viewing     *bool
	not         *Match
	// freq is the compiled rolling-window gate, applied by the engine after the
	// field conditions pass (matchPost never reads it — it is stateful and must
	// not run during catch-up replay or pure match tests).
	freq *frequency
	// cool is the compiled minimum-interval gate, likewise applied by the engine
	// (not matchPost) after the fields pass and persisted across restarts.
	cool *cooldown
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

// cooldown is a compiled CooldownSpec: a minimum interval between firings. The
// engine reads the last-fire time from the persistent meta store, fires when it
// is at least `every` old (or unset), and records the new time — so the gate
// holds across restarts. Keyed per (rule, group) like frequency.
type cooldown struct {
	every time.Duration
	by    string // "author" | "channel" | "team" | "global"
}

// stateCond is a compiled StateCondSpec — one condition on a ledger key. The
// key is a template (like the state action keys), so a condition can address a
// per-channel or per-author key — `hot:{{ .channel_id }}`. eval reports whether
// the snapshot satisfies it.
type stateCond struct {
	key              string             // raw key text (used when no renderer is supplied, e.g. in tests)
	keyTmpl          *template.Template // compiled key template, rendered against the post at match time
	exists           *bool
	eq, ne           *string
	gt, gte, lt, lte *float64
}

// eval reports whether the ledger snapshot satisfies this condition. render
// expands the key template against the post (nil falls back to the raw key, for
// pure tests). An absent key has no value, so any value comparison (eq/ne/
// numeric) against it fails; use exists:false to match absence. A non-numeric
// stored value never satisfies a numeric comparison.
func (c stateCond) eval(state map[string]string, render func(*template.Template) string) bool {
	key := c.key
	if render != nil && c.keyTmpl != nil {
		key = render(c.keyTmpl)
	}
	val, present := state[key]
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
	Channel   string // send: target ("team/channel" or "@user"); "" = trigger's channel
	Thread    bool   // send: reply in the trigger's thread

	// State action fields. keyTmpl/valueTmpl are the compiled forms of the
	// Key/Value strings; by is the resolved state_incr amount (default 1).
	keyTmpl   *template.Template
	valueTmpl *template.Template
	// textTmpl is the compiled send message body (the Text field).
	textTmpl *template.Template
	// cmdTmpls are the compiled forms of the exec Command argv: each element is a
	// template over the post envelope, so a command can carry {{ .author }} etc.
	cmdTmpls []*template.Template
	by       int64
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
		teamsRaw:    s.Teams,
		authors:     s.Authors,
		mention:     s.Mention,
		dm:          s.DM,
		fromMe:      s.FromMe,
		hasFile:     s.HasFile,
		isThread:    s.IsThread,
		viewing:     s.Viewing,
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
	for _, tm := range s.Teams {
		if tm == "" {
			continue
		}
		re, err := globToRegexp(tm)
		if err != nil {
			return Match{}, fmt.Errorf("bad team glob %q: %w", tm, err)
		}
		m.teamRes = append(m.teamRes, re)
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
	if s.Cooldown != nil {
		c, err := compileCooldown(*s.Cooldown)
		if err != nil {
			return Match{}, fmt.Errorf("cooldown: %w", err)
		}
		m.cool = c
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

// compileCooldown validates and normalises a CooldownSpec. A missing or
// non-positive interval, or an unknown grouping, is a startup error so the gate
// can't silently misbehave.
func compileCooldown(s CooldownSpec) (*cooldown, error) {
	every, err := time.ParseDuration(s.Every)
	if err != nil {
		return nil, fmt.Errorf("bad every %q: %w", s.Every, err)
	}
	if every <= 0 {
		return nil, fmt.Errorf("every must be positive, got %s", every)
	}
	by := strings.ToLower(strings.TrimSpace(s.By))
	switch by {
	case "", "global":
		by = "global"
	case "author", "channel", "team":
		// ok
	default:
		return nil, fmt.Errorf("unknown by %q (want author, channel, team, or global)", s.By)
	}
	return &cooldown{every: every, by: by}, nil
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
	kt, err := compileTemplate(s.Key)
	if err != nil {
		return stateCond{}, fmt.Errorf("bad key template %q: %w", s.Key, err)
	}
	return stateCond{
		key:     s.Key,
		keyTmpl: kt,
		exists:  s.Exists,
		eq:      s.Eq,
		ne:      s.Ne,
		gt:      s.Gt,
		gte:     s.Gte,
		lt:      s.Lt,
		lte:     s.Lte,
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
		Channel:   strings.TrimSpace(a.Channel),
		Thread:    a.Thread,
	}
	switch a.Type {
	case ActionNotify, ActionMarkRead, ActionLog:
		// no required fields
	case ActionSend:
		if strings.TrimSpace(a.Text) == "" {
			return Action{}, fmt.Errorf("send action needs text")
		}
		tt, err := compileTemplate(a.Text)
		if err != nil {
			return Action{}, fmt.Errorf("send: bad text template: %w", err)
		}
		ca.textTmpl = tt
	case ActionExec:
		if len(a.Command) == 0 {
			return Action{}, fmt.Errorf("exec action needs a command")
		}
		ca.cmdTmpls = make([]*template.Template, len(a.Command))
		for i, arg := range a.Command {
			t, err := compileTemplate(arg)
			if err != nil {
				return Action{}, fmt.Errorf("exec: bad command template %q: %w", arg, err)
			}
			ca.cmdTmpls[i] = t
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

// templateClock is the time source for the now/today template functions. It is
// a package var rather than time.Now directly so a test can drive a date-
// dependent rule (e.g. a once-per-day key built from {{ today }})
// deterministically.
var templateClock = time.Now

// templateFuncs are the helpers available in rule templates — state keys/values
// and the send body — on top of text/template's built-ins. now/today expose the
// daemon's clock so a rule can build a per-day ledger key ("greeted:{{ today }}")
// or stamp a message with the time, which the field set alone can't express.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// now is the current local time; format it with Go's reference layout,
		// e.g. {{ now.Format "15:04" }}.
		"now": func() time.Time { return templateClock() },
		// today is the current local date as "2006-01-02" — a ready-made per-day
		// key, e.g. key: "greeted:{{ today }}".
		"today": func() string { return templateClock().Format("2006-01-02") },
	}
}

// compileTemplate parses a rule template (a state action's key/value, or the
// send body). missingkey=zero renders an absent field (or absent state key) as
// the zero value — an empty string — instead of "<no value>", so a template
// referencing a not-yet-set counter degrades quietly. templateFuncs adds the
// now/today date helpers.
func compileTemplate(text string) (*template.Template, error) {
	return template.New("state").Option("missingkey=zero").Funcs(templateFuncs()).Parse(text)
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
	render := e.stateKeyRenderer(ev, p)
	for _, r := range e.rules {
		if ruleHasNotify(r) && e.matches(ev, p, r.Match, state, render) {
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
	// render expands templated `state.key`s (e.g. hot:{{ .channel_id }}) against
	// this post; it is built once and lazily fills its envelope on first use.
	state := e.matchState()
	render := e.stateKeyRenderer(ev, p)
	for i := range e.rules {
		r := e.rules[i]
		if !e.matches(ev, p, r.Match, state, render) {
			continue
		}
		// A cooldown gate suppresses the rule until its interval has elapsed since
		// the last firing. Checked read-only first, so a post that is still on
		// cooldown skips the rule without recording a frequency hit; the new fire
		// time is written only once the rule actually runs (below).
		if r.Match.cool != nil && !e.cooldownReady(r, ev, p) {
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
		if r.Match.cool != nil {
			e.recordCooldown(r, ev, p)
		}
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

// cooldownReady reports whether the rule may fire now — i.e. it has never fired
// for this (rule, group), or its interval has elapsed since it last did. It only
// reads the persisted last-fire time; recordCooldown commits the new one once
// the rule actually runs. A read error fails open (ready) and is logged, so a
// transient store hiccup delays nothing.
func (e *Engine) cooldownReady(r Rule, ev *model.WebSocketEvent, p *model.Post) bool {
	key := cooldownMetaKey(r.Name, r.Match.cool, ev, p)
	v, ok, err := e.store.GetMeta(key)
	if err != nil {
		e.log.Printf("rule %s: read cooldown: %v", r.Name, err)
		return true
	}
	if !ok {
		return true // never fired
	}
	last, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return true // unreadable timestamp — treat as ready
	}
	return e.clock().Sub(time.UnixMilli(last)) >= r.Match.cool.every
}

// recordCooldown stamps the rule's last-fire time for this (rule, group) in the
// persistent meta store, re-arming the interval. Called only after the rule's
// actions are dispatched, so a gate that blocked the firing never advances it.
func (e *Engine) recordCooldown(r Rule, ev *model.WebSocketEvent, p *model.Post) {
	key := cooldownMetaKey(r.Name, r.Match.cool, ev, p)
	if err := e.store.SetMeta(key, strconv.FormatInt(e.clock().UnixMilli(), 10)); err != nil {
		e.log.Printf("rule %s: record cooldown: %v", r.Name, err)
	}
}

// cooldownMetaKey is the meta-store key for a rule's cooldown: a reserved
// "cooldown:" namespace (kept out of the user-facing rule_state ledger), the
// rule name, and the group selected by `by`. The NUL separator can't appear in a
// name, channel id, or username, so distinct (rule, group) pairs never collide.
func cooldownMetaKey(ruleName string, c *cooldown, ev *model.WebSocketEvent, p *model.Post) string {
	var group string
	switch c.by {
	case "author":
		group = strings.TrimPrefix(eventStr(ev, "sender_name"), "@")
	case "channel":
		group = p.ChannelId
	case "team":
		group = eventStr(ev, "team_id")
	default: // global
		group = ""
	}
	return "cooldown:" + ruleName + "\x00" + group
}

// matches reports whether a post satisfies a rule's conditions. The builtin
// match defers to isDirectMention so the default rule behaves identically to
// the pre-rules daemon; user rules use the field matcher. state is the ledger
// snapshot the field matcher tests `state` conditions against (nil when no rule
// uses state — see matchState).
func (e *Engine) matches(ev *model.WebSocketEvent, p *model.Post, m Match, state map[string]string, render func(*template.Template) string) bool {
	meID, meName := "", ""
	if e.me != nil {
		meID, meName = e.me.Id, e.me.Username
	}
	if m.builtin {
		return isDirectMention(ev, p, meID, meName, e.opts.NotifySelf)
	}
	return matchPost(ev, p, m, meID, meName, e.teamName(ev), state, render, e.tuiStatus())
}

// teamName resolves an event's team id to its URL name (slug) for the team
// matcher, via the same id→name map buildEnvelope uses for permalinks. A post
// with no team (a DM) resolves to "", which matchesTeam treats as no match.
func (e *Engine) teamName(ev *model.WebSocketEvent) string {
	id := eventStr(ev, "team_id")
	if id == "" {
		return ""
	}
	e.teamsMu.RLock()
	defer e.teamsMu.RUnlock()
	return e.teams[id]
}

// matchPost evaluates the field conditions of a (non-builtin) match. Pure for
// testability: every condition it reads comes from the event/post, the reader's
// id and username for the Mention check, the ledger snapshot for `state`
// conditions, plus tui — the TUI's answer to "are you looking at this?",
// resolved once per post by the caller (see Engine.tuiStatus) and passed down
// so a nested not: evaluates `viewing` against the same reading.
func matchPost(ev *model.WebSocketEvent, p *model.Post, m Match, meID, meName, teamName string, state map[string]string, render func(*template.Template) string, tui control.Status) bool {
	isDM := eventStr(ev, "channel_type") == string(model.ChannelTypeDirect)
	if m.dm != nil && *m.dm != isDM {
		return false
	}
	// The same p.UserId == me test the notify/send actions apply internally, so an
	// exec/webhook/react rule can exclude (from_me: false) or target (true) the
	// reader's own posts. With no known reader (meID == "") nothing is "from me".
	if m.fromMe != nil {
		isMe := meID != "" && p.UserId == meID
		if *m.fromMe != isMe {
			return false
		}
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
	if len(m.teamRes) > 0 {
		if !matchesTeam(teamName, eventStr(ev, "team_id"), m) {
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
	if m.viewing != nil && *m.viewing != tui.Viewing(p.ChannelId) {
		return false
	}
	for _, c := range m.state {
		if !c.eval(state, render) {
			return false
		}
	}
	// A nested not: matches the whole post only when the sub-match does not.
	if m.not != nil && matchPost(ev, p, *m.not, meID, meName, teamName, state, render, tui) {
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

// matchesTeam reports whether the team URL name matches any of the compiled
// globs, or its id equals any raw entry (the exact-id fallback) — mirroring
// matchesChannel. An empty name (a DM, which has no team) never matches: the
// globs are skipped and the raw entries are non-empty, so a direct message is
// excluded from any team condition.
func matchesTeam(name, teamID string, m Match) bool {
	for _, re := range m.teamRes {
		if name != "" && re.MatchString(name) {
			return true
		}
	}
	for _, raw := range m.teamsRaw {
		if raw != "" && raw == teamID {
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
		case ActionSend:
			a := a
			e.wg.Add(1)
			go e.runSend(ctx, ev, p, a)
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
// channels, quiet hours, the conversation you're currently reading, and honour
// notify_dms) before spinning off the delivery. This is the same gate the
// pre-rules daemon applied inline in handle(); centralising it here means every
// notify action — default or user-defined — respects the same do-not-disturb
// settings. An urgent action bypasses the quiet-hours and muted-channel
// suppression (but not the self / DM gates), so an on-call keyword still pages
// while you're heads-down.
func (e *Engine) notifyGate(ctx context.Context, ev *model.WebSocketEvent, p *model.Post, opts notifyOpts) {
	if e.me != nil && p.UserId == e.me.Id && !e.opts.NotifySelf {
		return
	}
	if eventStr(ev, "channel_type") == string(model.ChannelTypeDirect) && !e.opts.NotifyDMs {
		return
	}
	// You are looking at this conversation right now, in a TUI on this machine:
	// the message is already on your screen. Urgent doesn't bypass this — like
	// the post-delay read-check it isn't a preference about being disturbed but
	// a fact about having seen it.
	if e.tuiStatus().Viewing(p.ChannelId) {
		e.log.Printf("channel %s is open and focused in the TUI — notification skipped", p.ChannelId)
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

// stateKeyRenderer returns a function that expands a state condition's key
// template against this post. The post's template data (the same envelope map
// the action templates use) is built lazily on first use and cached, so a post
// that never evaluates a templated key pays nothing, and one that evaluates
// several builds the envelope once.
func (e *Engine) stateKeyRenderer(ev *model.WebSocketEvent, p *model.Post) func(*template.Template) string {
	var data map[string]any
	built := false
	return func(t *template.Template) string {
		if t == nil {
			return ""
		}
		if !built {
			if d, err := envelopeMap(e.buildEnvelope(ev, p)); err == nil {
				data = d
			}
			built = true
		}
		var b strings.Builder
		if err := t.Execute(&b, data); err != nil {
			return ""
		}
		return b.String()
	}
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
// execTimeout. The argv is rendered per post (each element is a template over
// the envelope). Output is logged (truncated); a failure is logged, not fatal.
func (e *Engine) runExec(ctx context.Context, ev *model.WebSocketEvent, p *model.Post, a Action) {
	defer e.wg.Done()
	env := e.buildEnvelope(ev, p)
	payload, err := json.Marshal(env)
	if err != nil {
		e.log.Printf("rule exec: marshal envelope: %v", err)
		return
	}
	argv, err := renderCommand(a, env)
	if err != nil {
		e.log.Printf("rule exec: render command: %v", err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = execEnv(env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.log.Printf("rule exec %v failed: %v (%s)", argv, err, truncateForLog(string(out)))
		return
	}
	e.log.Printf("rule exec %v ok%s", argv, logSuffix(string(out)))
}

// renderCommand expands an exec action's templated argv against the post
// envelope, so a command like ["notify-send", "{{ .author }} sent a message"]
// is filled in per post. When the templates aren't compiled (an Action built
// directly rather than through compileAction, e.g. in a test) it falls back to
// the raw Command. It errors if any argument fails to render or the executable
// (argv[0]) renders empty — exec'ing "" would be a confusing failure.
func renderCommand(a Action, env envelope) ([]string, error) {
	if len(a.cmdTmpls) == 0 {
		return a.Command, nil
	}
	data, err := envelopeMap(env)
	if err != nil {
		return nil, err
	}
	argv := make([]string, len(a.cmdTmpls))
	for i, t := range a.cmdTmpls {
		var b strings.Builder
		if err := t.Execute(&b, data); err != nil {
			return nil, err
		}
		argv[i] = b.String()
	}
	if strings.TrimSpace(argv[0]) == "" {
		return nil, fmt.Errorf("command rendered an empty executable")
	}
	return argv, nil
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

// runSend posts a message in reaction to the triggering post. The body is a
// template (so it can carry {{ .author }}, {{ today }}, …). With no Channel it
// posts into the channel the trigger arrived in; a Channel ("team/channel" or
// "@user") routes it elsewhere. Own posts are skipped (unless notify_self) so an
// ungated send rule can't loop on the very message it just posted.
func (e *Engine) runSend(ctx context.Context, ev *model.WebSocketEvent, p *model.Post, a Action) {
	defer e.wg.Done()
	if e.me == nil {
		return
	}
	if p.UserId == e.me.Id && !e.opts.NotifySelf {
		return
	}
	body, err := e.renderTemplate(a.textTmpl, ev, p)
	if err != nil {
		e.log.Printf("rule send: render text: %v", err)
		return
	}
	if strings.TrimSpace(body) == "" {
		e.log.Printf("rule send: text rendered empty — skipped")
		return
	}
	channelID, rootID := p.ChannelId, ""
	if a.Thread {
		if rootID = p.RootId; rootID == "" {
			rootID = p.Id
		}
	}
	if a.Channel != "" {
		id, err := e.resolveSendTarget(ctx, a.Channel)
		if err != nil {
			e.log.Printf("rule send: resolve %q: %v", a.Channel, err)
			return
		}
		channelID, rootID = id, "" // a configured channel is not the trigger's thread
	}
	if _, err := e.client.Send(ctx, channelID, rootID, body, nil); err != nil {
		e.log.Printf("rule send to %s: %v", channelID, err)
	}
}

// resolveSendTarget turns a send action's channel spec ("team/channel" or
// "@user"/"@a,@b") into a channel id, caching the result: channel ids are
// stable across renames, so a send rule that targets a fixed channel resolves it
// once rather than on every matching post.
func (e *Engine) resolveSendTarget(ctx context.Context, spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	e.sendChanMu.Lock()
	id, ok := e.sendChan[spec]
	e.sendChanMu.Unlock()
	if ok {
		return id, nil
	}
	var ch *model.Channel
	var err error
	if strings.HasPrefix(spec, "@") {
		ch, err = mm.ResolveRecipients(ctx, e.client, e.me.Id, spec)
	} else {
		team, channel, found := strings.Cut(spec, "/")
		if !found || team == "" || channel == "" {
			return "", fmt.Errorf("channel %q must be team/channel (e.g. eng/general) or @user", spec)
		}
		ch, err = e.client.ChannelByName(ctx, team, channel)
	}
	if err != nil {
		return "", err
	}
	e.sendChanMu.Lock()
	if e.sendChan == nil {
		e.sendChan = map[string]string{}
	}
	e.sendChan[spec] = ch.Id
	e.sendChanMu.Unlock()
	return ch.Id, nil
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
