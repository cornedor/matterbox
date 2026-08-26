package telemetry

import "time"

// Typed emitters, one per catalogued event. Call sites use these rather than
// Capture so a property name is a struct field the compiler checks, not a map
// key a typo can silently drop — the catalogue would reject the misspelling,
// but at runtime, on a user's machine, where nobody would ever see it.
//
// Every emitter is a no-op when telemetry is off, so call sites need no guard.
// The only thing worth guarding is *computing* an argument that costs something
// (walking a post list to count mentions), and Enabled() exists for that.

// Env describes the machine and build, for app_started. Assembled once at
// startup by the caller, which is the only place that knows all of it.
type Env struct {
	Version       string
	BuildTags     string
	GoVersion     string
	OS            string
	Arch          string
	Terminal      string
	ImageProtocol string
	Cols          int
	Rows          int
	FeaturesOn    []string
	MouseEnabled  bool
	NavModifier   string
	Overridden    []string
	Teams         int
	Channels      int
	FirstRun      bool
	StartupMillis int64
	CacheWarm     bool
}

// AppStarted reports a TUI launch. The environment properties are mirrored
// onto the PostHog person as well as the event, so "what do our users run"
// is a person-level question — otherwise a single user restarting matterbox
// forty times a day would look like forty kitty users.
//
// Fields the caller could not determine are left out rather than sent as a
// wrong default: the graphics protocol is only known once the terminal has
// answered the startup probe, and the team and channel counts only once the
// sidebar has loaded. An absent property reads as "not known at launch", while
// a zero would read as "this user has no channels".
func AppStarted(e Env) {
	props := map[string]any{
		"cols":               Cols(e.Cols),
		"rows":               Rows(e.Rows),
		"features_on":        e.FeaturesOn,
		"mouse_enabled":      e.MouseEnabled,
		"overridden_actions": e.Overridden,
		"first_run":          e.FirstRun,
		"startup_ms":         Millis(e.StartupMillis),
		"cache_warm":         e.CacheWarm,
	}
	optional := map[string]string{
		"version":        e.Version,
		"build_tags":     e.BuildTags,
		"go_version":     e.GoVersion,
		"os":             e.OS,
		"arch":           e.Arch,
		"terminal":       e.Terminal,
		"image_protocol": e.ImageProtocol,
		"nav_modifier":   e.NavModifier,
	}
	for name, v := range optional {
		if v != "" {
			props[name] = v
		}
	}
	if e.Teams >= 0 {
		props["teams"] = Count(e.Teams)
	}
	if e.Channels >= 0 {
		props["channels"] = Count(e.Channels)
	}
	capture("app_started", props, []string{
		"version", "build_tags", "go_version", "os", "arch", "terminal",
		"image_protocol", "cols", "rows", "features_on", "mouse_enabled",
		"nav_modifier", "teams", "channels",
	})
}

// AppStopped reports the end of a session, with the totals the counters have
// been keeping. Close flushes the final snapshot, so call this before it.
func AppStopped(reason string) {
	actions, messages, channels, since := tally.sessionTotals()
	Capture("app_stopped", map[string]any{
		"session":         SinceSeconds(since),
		"reason":          reason,
		"actions":         Count(actions),
		"messages_sent":   Count(messages),
		"channels_opened": Count(channels),
	})
}

// VersionUpgraded reports the first launch of a new build.
func VersionUpgraded(from, to string) {
	Capture("version_upgraded", map[string]any{"from": from, "to": to})
}

// SetupStep reports the wizard showing a step. attempt counts how many times
// this run has shown it — a step shown repeatedly is a step being fought with.
func SetupStep(step string, attempt int) {
	Capture("setup_step", map[string]any{"step": step, "attempt": attempt})
}

// SetupFinished closes the activation funnel. optIn is only ever true: a
// declined telemetry question sends nothing at all, so the property exists to
// make the opt-in rate legible rather than to record a "no".
func SetupFinished(outcome, lastStep, authMethod string, took time.Duration, optIn bool) {
	Capture("setup_finished", map[string]any{
		"outcome":          outcome,
		"last_step":        lastStep,
		"duration":         Seconds(int64(took.Seconds())),
		"auth_method":      authMethod,
		"telemetry_opt_in": optIn,
	})
}

// LoginFailed reports a rejected login attempt.
func LoginFailed(method, class string, mfa bool) {
	Capture("login_failed", map[string]any{"method": method, "class": class, "mfa": mfa})
}

// Open describes a conversation being opened, for channel_opened.
type Open struct {
	Via         string
	ChannelType string
	WasUnread   bool
	Cache       string
	RenderMs    int64
	Posts       int
}

// ChannelOpened reports a conversation opening, and bumps the session's
// distinct-conversation total.
func ChannelOpened(o Open) {
	Capture("channel_opened", map[string]any{
		"via":          o.Via,
		"channel_type": o.ChannelType,
		"was_unread":   o.WasUnread,
		"cache":        o.Cache,
		"render_ms":    Millis(o.RenderMs),
		"posts":        Count(o.Posts),
	})
	countChannelOpened()
}

// Sent describes a message that went out, for message_sent. Every field is a
// shape of the message; none of them is its text.
type Sent struct {
	Surface       string
	IsReply       bool
	IsNestedReply bool
	Length        int
	Lines         int
	Attachments   int
	Mentions      int
	HasCodeBlock  bool
	HasLink       bool
	HasEmoji      bool
	HasEffect     bool
	SendMs        int64
}

// MessageSent reports a successful send and bumps the session total.
func MessageSent(s Sent) {
	Capture("message_sent", map[string]any{
		"surface":         s.Surface,
		"is_reply":        s.IsReply,
		"is_nested_reply": s.IsNestedReply,
		"length":          Length(s.Length),
		"lines":           Count(s.Lines),
		"attachments":     Count(s.Attachments),
		"mentions":        Count(s.Mentions),
		"has_code_block":  s.HasCodeBlock,
		"has_link":        s.HasLink,
		"has_emoji":       s.HasEmoji,
		"has_effect":      s.HasEffect,
		"send_ms":         Millis(s.SendMs),
	})
	countMessageSent()
}

// Acted describes an action on an existing message, for message_acted.
type Acted struct {
	Action       string
	Own          bool
	Age          time.Time
	Outcome      string
	ReactionSlot string
	Via          string
}

// MessageActed reports editing, deleting, reacting to or copying a message.
func MessageActed(a Acted) {
	props := map[string]any{
		"action":  a.Action,
		"own":     a.Own,
		"age":     SinceSeconds(a.Age),
		"outcome": a.Outcome,
		"via":     a.Via,
	}
	if a.ReactionSlot != "" {
		props["reaction_slot"] = a.ReactionSlot
	}
	Capture("message_acted", props)
}

// ThreadOpened reports a thread pane opening.
func ThreadOpened(via string, replies int, nested bool, depth int) {
	Capture("thread_opened", map[string]any{
		"via":     via,
		"replies": Count(replies),
		"nested":  nested,
		"depth":   Count(depth),
	})
}

// Search describes a search that ran, for search_run.
type Search struct {
	Mode         string
	Scope        string
	From         string
	Terms        int
	HadOperators bool
	Results      int
	LatencyMs    int64
	Outcome      string
}

// SearchRun reports a search. An empty result set is also counted as friction:
// a search that finds nothing is the most common way search disappoints.
func SearchRun(s Search) {
	Capture("search_run", map[string]any{
		"mode":          s.Mode,
		"scope":         s.Scope,
		"from":          s.From,
		"terms":         Count(s.Terms),
		"had_operators": s.HadOperators,
		"results":       Count(s.Results),
		"latency_ms":    Millis(s.LatencyMs),
		"outcome":       s.Outcome,
	})
	if s.Results == 0 && s.Outcome == "ok" {
		Friction("search_empty")
	}
}

// SearchResultOpened reports a hit being opened, which is the half of search
// quality that result counts can't show.
func SearchResultOpened(mode string, rank int, dwell time.Duration) {
	Capture("search_result_opened", map[string]any{
		"mode":  mode,
		"rank":  Rank(rank),
		"dwell": Seconds(int64(dwell.Seconds())),
	})
}

// FeedUsed reports an action in the unread feed.
func FeedUsed(action string, items int, via string) {
	Capture("feed_used", map[string]any{
		"action": action,
		"items":  Count(items),
		"via":    via,
	})
}

// Use describes a feature invocation, for feature_used.
type Use struct {
	Feature    string
	Outcome    string
	LatencyMs  int64
	Size       int
	Via        string
	ErrorClass string
}

// FeatureUsed reports a feature being used, with its outcome, and bumps the
// feature counter so the snapshot's adoption picture stays complete.
func FeatureUsed(u Use) {
	props := map[string]any{
		"feature": u.Feature,
		"outcome": u.Outcome,
		"via":     u.Via,
	}
	if u.LatencyMs > 0 {
		props["latency_ms"] = Millis(u.LatencyMs)
	}
	if u.Size > 0 {
		props["size"] = Count(u.Size)
	}
	if u.ErrorClass != "" {
		props["error_class"] = u.ErrorClass
	}
	Capture("feature_used", props)
	Feature(u.Feature)
}

// ForgeAction reports a Jira / GitLab / GitHub action from the reference panel.
func ForgeAction(provider, action, outcome string, latencyMs int64, errClass string) {
	props := map[string]any{
		"provider":   provider,
		"action":     action,
		"outcome":    outcome,
		"latency_ms": Millis(latencyMs),
	}
	if errClass != "" {
		props["error_class"] = errClass
	}
	Capture("forge_action", props)
	Feature(provider)
}

// AttachmentAdded reports a file being attached.
func AttachmentAdded(via, kind string, size int64, count int, outcome string) {
	Capture("attachment_added", map[string]any{
		"via":     via,
		"kind":    kind,
		"size":    Bytes(size),
		"count":   Count(count),
		"outcome": outcome,
	})
}

// MediaRendered reports a terminal-graphics draw and its outcome — the only
// way to learn which terminals media actually works on.
func MediaRendered(kind, protocol, outcome string, decodeMs int64, errClass string) {
	props := map[string]any{
		"kind":      kind,
		"protocol":  protocol,
		"outcome":   outcome,
		"decode_ms": Millis(decodeMs),
	}
	if errClass != "" {
		props["error_class"] = errClass
	}
	Capture("media_rendered", props)
}

// UnhandledKey reports a keypress that no active layer claimed. The single
// most useful friction signal: it names the bindings people expect and do not
// have. boundElsewhere distinguishes an invented key from a known key pressed
// in the wrong pane, and the latter usually means the binding is scoped wrong.
func UnhandledKey(key, context string, boundElsewhere bool) {
	Capture("unhandled_key", map[string]any{
		"key":             key,
		"surface":         context,
		"bound_elsewhere": boundElsewhere,
	})
	Friction("unhandled_key")
	if boundElsewhere {
		Friction("unhandled_key_bound_elsewhere")
	}
}

// Stuck describes a friction signal with enough context to act on, for the
// friction event.
type Stuck struct {
	Signal  string
	Context string
	Action  string
	Count   int
	Dwell   time.Duration
	Size    int
}

// FrictionEvent reports a friction signal that crossed the threshold worth an
// event of its own. The counter is bumped by the caller or by Action/Escape,
// which detect the run; this adds the address.
func FrictionEvent(s Stuck) {
	props := map[string]any{
		"signal":  s.Signal,
		"surface": s.Context,
	}
	if s.Action != "" {
		props["action"] = s.Action
	}
	if s.Count > 0 {
		props["count"] = s.Count
	}
	if s.Dwell > 0 {
		props["dwell"] = Seconds(int64(s.Dwell.Seconds()))
	}
	if s.Size > 0 {
		props["size"] = Length(s.Size)
	}
	Capture("friction", props)
}

// SlowFrame reports a render slow enough to be seen. Rate-limited by the
// caller — a genuinely slow session would otherwise report every frame.
func SlowFrame(context string, ms int64, posts, cols int, cause string) {
	Capture("slow_frame", map[string]any{
		"surface": context,
		"ms":      Millis(ms),
		"posts":   Count(posts),
		"cols":    Cols(cols),
		"cause":   cause,
	})
	Friction("slow_frame")
}

// WSDisconnected reports the websocket dropping.
func WSDisconnected(class string, connected time.Duration, clean bool, cause string) {
	Capture("ws_disconnected", map[string]any{
		"class":     class,
		"connected": Seconds(int64(connected.Seconds())),
		"clean":     clean,
		"cause":     cause,
	})
}

// WSReconnected reports it coming back, and whether the catch-up worked — a
// socket that reconnects without recovering missed messages is still broken.
func WSReconnected(attempts int, downtime time.Duration, resync string) {
	Capture("ws_reconnected", map[string]any{
		"attempts": Count(attempts),
		"downtime": Seconds(int64(downtime.Seconds())),
		"resync":   resync,
	})
}

// Failure describes a failed operation, for operation_failed.
type Failure struct {
	Where       string
	Class       string
	Status      int
	Retried     bool
	UserVisible bool
	Err         error
}

// OperationFailed reports a failure the user was waiting on. The error text is
// scrubbed by the catalogue's KindErrorText validation, so a call site does not
// have to remember to wrap it.
//
// It is also the funnel into error tracking, so a call site gets both halves
// from one call: the event always fires, and a PostHog exception is raised
// alongside it when the class says the failure is ours rather than the world's
// (see worthAnIssue). That split is what keeps the issue list about bugs — a
// dropped connection is a number, not something to fix.
func OperationFailed(f Failure) {
	props := map[string]any{
		"where":        f.Where,
		"class":        f.Class,
		"status":       f.Status,
		"retried":      f.Retried,
		"user_visible": f.UserVisible,
	}
	if f.Err != nil {
		props["detail"] = f.Err.Error()
	}
	Capture("operation_failed", props)

	if f.Err != nil && worthAnIssue(f.Class) {
		report(f.Where, f.Class, Scrub(f.Err.Error()), stackFrames(1), true)
	}
}

// PanicRecovered reports a caught panic with matterbox's own stack frames.
// stack is a raw runtime stack trace; ScrubStack reduces it to our frames.
//
// Call sites reach this through telemetry.Crash / telemetry.ReportPanic, which
// take the panic value straight from recover() and capture the stack while it
// is still standing. Calling it by hand afterwards would report the handler's
// frames instead of the ones that broke.
func PanicRecovered(where string, value string, stack string) {
	Capture("panic_recovered", map[string]any{
		"where":  where,
		"frames": ScrubStack(stack),
		"detail": value,
	})
}

// CLICommand reports a subcommand run. The cheapest high-value event there is:
// several verbs exist only to be called from scripts, where a breakage would
// never be reported by anyone.
func CLICommand(command, outcome string, took time.Duration, tty bool, errClass string) {
	props := map[string]any{
		"command":     command,
		"outcome":     outcome,
		"duration_ms": Millis(took.Milliseconds()),
		"tty":         tty,
	}
	if errClass != "" {
		props["error_class"] = errClass
	}
	Capture("cli_command", props)
}

// DaemonStarted reports the listen daemon coming up.
func DaemonStarted(version string, rules int, channelsOn []string) {
	Capture("daemon_started", map[string]any{
		"version":     version,
		"rules":       rules,
		"channels_on": channelsOn,
	})
}

// RuleFired reports a listen rule's action running. Rule names are written by
// the user and are never sent — only the action type.
func RuleFired(action, outcome, trigger string) {
	Capture("rule_fired", map[string]any{
		"action":  action,
		"outcome": outcome,
		"trigger": trigger,
	})
}

// NotificationActioned reports a delivered notification being acted on —
// whether anyone presses the desktop buttons, and whether it then works.
func NotificationActioned(channel, action, outcome string) {
	Capture("notification_actioned", map[string]any{
		"channel": channel,
		"action":  action,
		"outcome": outcome,
	})
}
