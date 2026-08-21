package ui

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/forge"
	"matterbox/internal/telemetry"
)

// Session-scoped telemetry state for the TUI.
//
// Several events describe something that *finishes* somewhere other than where
// it starts: a conversation is opened by one keystroke and painted when the
// network answers, a thread is opened before its replies exist, a search is
// issued on the UI goroutine and lands on a worker. Each of those needs a small
// amount of state carried between the two, and none of it belongs in Model
// proper.
//
// It lives behind one pointer for two reasons. Model is ~105KB and value-copied
// on every Update and View, so an inline struct here is a per-keystroke memcpy
// cost for something an opted-out session never uses; and the pointer means the
// state survives the copy, so View — which has a value receiver and cannot
// mutate the model — can still record a slow frame.
//
// The pointer is nil for an opted-out session and for every literal-built test
// Model, so every method below is nil-safe and every read goes through one.

// uiTelemetry is the carried state. Allocated by New when telemetry is on.
type uiTelemetry struct {
	// Launch readiness. app_started is held back until the facts that arrive
	// late are in (see maybeRecordLaunch); sent is what makes it once.
	sizeKnown     bool
	graphicsKnown bool
	listsKnown    bool
	launchOverdue bool
	launchSent    bool

	// The conversation being opened, if one is. Armed by enterChannel and
	// consumed by the paint that first shows it.
	open openRecord

	// The thread being opened, if one is. Consumed when its replies land.
	thread threadRecord

	// The search in flight, if one is. Consumed when its results land.
	search searchRecord
	// resultsAt is when the last result set appeared, for search_result_opened's
	// dwell — how long someone looked at the list before picking something.
	resultsAt time.Time
	// searchOpened records whether anything was opened from the current result
	// set, so switching away from an untouched one counts as abandoned.
	searchOpened bool

	// WebSocket lifecycle. connectedAt dates the current connection (for how
	// long a healthy one lasts); downAt dates the drop (for the downtime).
	wsConnectedAt time.Time
	wsDownAt      time.Time

	// Slow-frame rate limiting, and the cause hint update() leaves for View.
	lastSlowFrame time.Time
	slowFrames    int
	frameCause    string

	// Friction detection that needs a memory of the recent past: when a key
	// last did nothing (for help_after_unhandled), when the app last rewrote
	// the draft (for undo_after_edit), and the current resize burst.
	unhandledAt time.Time
	autoEditAt  time.Time
	resizeFirst time.Time
	resizeRun   int
	// pickerOpenedAt dates the picker or modal currently open, for the dwell on
	// picker_abandoned. Only one is open at a time.
	pickerOpenedAt time.Time

	// once records the report-once-per-session keys. Some signals answer their
	// question with a single observation and would otherwise flood: terminal
	// graphics ("does this work on this terminal") only needs one event per
	// outcome, and a LanguageTool server that is down would report on every
	// debounce tick. Volume for those lives in the counters instead.
	once map[string]bool
}

// newUITelemetry allocates the carried state, or returns nil when telemetry is
// off — which is what keeps an opted-out session from paying for any of it.
func newUITelemetry() *uiTelemetry {
	if !telemetry.Enabled() {
		return nil
	}
	return &uiTelemetry{}
}

// --- conversation opens ---------------------------------------------------

// openRecord is a conversation open in flight: how it was reached, what was
// already cached, and when it started. channelID guards against a second open
// landing on the first one's paint.
type openRecord struct {
	channelID string
	via       string
	kind      string
	unread    bool
	cached    int
	at        time.Time
}

// armChannelOpen records that a conversation is being opened. Called from
// enterChannel, which every open path goes through — so `via` is a required
// argument there rather than a field somebody has to remember to set, and a new
// entry point cannot be added without naming how it got there.
func (m *Model) armChannelOpen(channelID, via string) {
	if m.tel == nil || channelID == "" {
		return
	}
	m.tel.open = openRecord{
		channelID: channelID,
		via:       via,
		kind:      channelKind(m.findChannel(channelID)),
		unread:    m.unread[channelID] > 0,
		at:        time.Now(),
	}
}

// noteOpenCache records how much the local cache could supply for the open in
// flight, which is what decides between warm, partial and cold.
func (m *Model) noteOpenCache(channelID string, cached int) {
	if m.tel == nil || m.tel.open.channelID != channelID {
		return
	}
	m.tel.open.cached = cached
}

// recordChannelOpened emits channel_opened for the conversation now painted,
// and disarms. Called from each path's first paint of the transcript — the warm
// ones straight away, the cold ones when the fetched page lands — so render_ms
// measures what the user waited for rather than when we set a variable.
//
// A second call for the same open does nothing, which is what lets the
// gap-fill reconcile that follows every warm open share the same hook.
func (m *Model) recordChannelOpened(channelID string) {
	if m.tel == nil || m.tel.open.channelID == "" || m.tel.open.channelID != channelID {
		return
	}
	o := m.tel.open
	m.tel.open = openRecord{}
	telemetry.ChannelOpened(telemetry.Open{
		Via:         o.via,
		ChannelType: o.kind,
		WasUnread:   o.unread,
		Cache:       cacheState(o.cached),
		RenderMs:    time.Since(o.at).Milliseconds(),
		Posts:       len(m.posts),
	})
}

// cacheState names how much of the conversation came out of the local cache,
// from the one thing known at the moment the transcript is first painted: how
// many posts the cache supplied.
//
//   - "cold": nothing. The first frame waited on the server, which is the case
//     the whole cache exists to avoid.
//   - "warm": a full page (initialRenderLimit). The pane was complete before the
//     network answered.
//   - "partial": something, but less than a page — so the transcript painted
//     short and the reconcile that follows every open filled in the rest. A
//     genuinely short conversation lands here too, since from here the two are
//     indistinguishable without a second query nobody needs.
func cacheState(cached int) string {
	switch {
	case cached <= 0:
		return "cold"
	case cached >= initialRenderLimit:
		return "warm"
	}
	return "partial"
}

// channelKind maps a channel to the catalogue's conversation types. The kind of
// conversation is safe and highly informative — it separates "people live in
// DMs" from "people live in big public channels" — while the name, id and
// members never leave the machine.
func channelKind(c *model.Channel) string {
	if c == nil {
		return "unknown"
	}
	switch c.Type {
	case model.ChannelTypeOpen:
		return "public"
	case model.ChannelTypePrivate:
		return "private"
	case model.ChannelTypeDirect:
		return "dm"
	case model.ChannelTypeGroup:
		return "group_dm"
	}
	return "unknown"
}

// --- thread opens ---------------------------------------------------------

// threadRecord is a thread pane opening, waiting on its replies.
type threadRecord struct {
	rootID string
	via    string
	at     time.Time
}

// armThreadOpen records a thread pane opening on a message.
func (m *Model) armThreadOpen(rootID, via string) {
	if m.tel == nil || rootID == "" {
		return
	}
	m.tel.thread = threadRecord{rootID: rootID, via: via, at: time.Now()}
}

// recordThreadOpened emits thread_opened once the thread's replies are in
// hand — the earliest point the reply count and the nesting depth are known,
// which is the whole reason the event waits. The nesting properties are what
// say whether the matterbox-only reply tree is worth its complexity.
func (m *Model) recordThreadOpened(rootID string, posts []*model.Post) {
	if m.tel == nil || m.tel.thread.rootID == "" || m.tel.thread.rootID != rootID {
		return
	}
	t := m.tel.thread
	m.tel.thread = threadRecord{}
	claims := nestClaims(posts)
	depth := 0
	for i := range posts {
		if d := nestDepthAt(i, posts, claims); d > depth {
			depth = d
		}
	}
	// The root is not a reply.
	replies := len(posts) - 1
	if replies < 0 {
		replies = 0
	}
	telemetry.ThreadOpened(t.via, replies, len(claims) > 0, depth)
}

// --- search ---------------------------------------------------------------

// searchRecord is a search in flight: which backend, how it was started, and
// the shape of the query. None of it is the query itself.
type searchRecord struct {
	mode     string
	scope    string
	from     string
	terms    int
	operator bool
	at       time.Time
}

// armSearch records a search being issued. `from` is how the Search tab was
// reached, which is the part that separates "nobody wants this" from "nobody
// can find this"; it is remembered on the search state rather than passed here,
// because the search itself is issued from a debounce tick that has forgotten.
func (m *Model) armSearch(mode, scope string, terms int, hadOperators bool) {
	if m.tel == nil {
		return
	}
	// A route that was never recorded reports "unknown" rather than nothing: an
	// absent property looks like a bug in the event, while "unknown" is a real
	// answer (the tab was already open, or was reached some way we don't name).
	from := m.search.from
	if from == "" {
		from = "unknown"
	}
	m.tel.search = searchRecord{
		mode:     mode,
		scope:    scope,
		from:     from,
		terms:    terms,
		operator: hadOperators,
		at:       time.Now(),
	}
	// The counted half of adoption, so the two opt-in search backends show up in
	// the snapshot alongside every other feature rather than only in their own
	// event. Plain keyword search is not a feature anyone chose.
	switch mode {
	case "ai":
		telemetry.Feature("ai_search")
	case "hybrid":
		telemetry.Feature("semantic_search")
	}
}

// recordSearchRun emits search_run for the search whose results just landed.
// There are four backends and no evidence about which earns its keep; the
// result count and the latency per mode are what answer that.
func (m *Model) recordSearchRun(results int, failed bool) {
	if m.tel == nil || m.tel.search.mode == "" {
		return
	}
	s := m.tel.search
	m.tel.search = searchRecord{}
	m.tel.resultsAt = time.Now()
	m.tel.searchOpened = false
	outcome := "ok"
	switch {
	case failed:
		outcome = "error"
	case results == 0:
		outcome = "empty"
	}
	telemetry.SearchRun(telemetry.Search{
		Mode:         s.mode,
		Scope:        s.scope,
		From:         s.from,
		Terms:        s.terms,
		HadOperators: s.operator,
		Results:      results,
		LatencyMs:    time.Since(s.at).Milliseconds(),
		Outcome:      outcome,
	})
}

// recordSearchHitOpened emits search_result_opened for a hit being opened, at
// its 1-based rank. This is the half of search quality a result count can't
// show: fifty hits nobody opens is a failed search, and the rank says whether
// the ranking is any good.
func (m *Model) recordSearchHitOpened(idx int) {
	if m.tel == nil {
		return
	}
	m.tel.searchOpened = true
	dwell := time.Duration(0)
	if !m.tel.resultsAt.IsZero() {
		dwell = time.Since(m.tel.resultsAt)
	}
	telemetry.SearchResultOpened(m.searchMode(), idx+1, dwell)
}

// noteSearchAbandoned counts a result set nobody opened anything from. Called
// when a new query replaces the old one's results, which is the moment the
// previous answer is known to have been useless.
func (m *Model) noteSearchAbandoned() {
	if m.tel == nil || m.tel.searchOpened || m.tel.resultsAt.IsZero() {
		return
	}
	if len(m.search.hits) == 0 {
		return // an empty result set is search_empty, already counted
	}
	m.tel.resultsAt = time.Time{}
	telemetry.Friction("search_abandoned")
}

// searchMode names the backend behind the current result set, for the hit
// event. Derived from the state rather than remembered, since the hits outlive
// the search that produced them.
func (m *Model) searchMode() string {
	switch {
	case m.aiSearch.active():
		return "ai"
	case m.embedClient != nil && m.search.semantic:
		return "hybrid"
	}
	return "local_fts"
}

// --- websocket ------------------------------------------------------------

// noteWSConnected records the moment a connection came up, and reports
// ws_reconnected when it follows a drop. Silent disconnects are this client's
// worst failure — the UI looks fine and messages stop arriving — so how long
// people spend disconnected, and whether the catch-up recovered anything, is
// the reliability picture.
func (m *Model) noteWSConnected(attempts int, resync string) {
	if m.tel == nil {
		return
	}
	now := time.Now()
	down := m.tel.wsDownAt
	m.tel.wsConnectedAt = now
	m.tel.wsDownAt = time.Time{}
	if attempts == 0 {
		return // the clean first connect; there is nothing to have recovered
	}
	downtime := time.Duration(0)
	if !down.IsZero() {
		downtime = now.Sub(down)
	}
	telemetry.WSReconnected(attempts, downtime, resync)
}

// noteWSDropped reports ws_disconnected and starts the downtime clock.
func (m *Model) noteWSDropped(err error) {
	if m.tel == nil {
		return
	}
	now := time.Now()
	connected := time.Duration(0)
	if !m.tel.wsConnectedAt.IsZero() {
		connected = now.Sub(m.tel.wsConnectedAt)
	}
	// Only the first drop of a run starts the clock: the retry chain
	// re-enters this on every failed attempt, and the downtime people care
	// about is since the connection was last working.
	if m.tel.wsDownAt.IsZero() {
		m.tel.wsDownAt = now
	}
	m.tel.wsConnectedAt = time.Time{}
	class := "unknown"
	if err != nil {
		class = telemetry.ClassifyError(err)
	}
	telemetry.WSDisconnected(class, connected, err == nil)
}

// --- media ----------------------------------------------------------------

// onceBudget bounds the report-once set, so a key built from more values than
// expected can't turn "once each" into an unbounded stream.
const onceBudget = 32

// firstTime reports whether this is the first time this session has asked about
// key, and remembers that it has. False once the budget is spent, which fails
// closed: past it, nothing new is reported.
func (m *Model) firstTime(key string) bool {
	if m.tel == nil || m.tel.once[key] || len(m.tel.once) >= onceBudget {
		return false
	}
	if m.tel.once == nil {
		m.tel.once = make(map[string]bool, 8)
	}
	m.tel.once[key] = true
	return true
}

// recordMedia reports a terminal-graphics draw the first time this session sees
// this combination of what was drawn and how it went. Everything after that is
// carried by the feature counters, which is where volume belongs.
//
// Terminal graphics are the most fragile thing matterbox does and the most
// likely to be silently broken on a given terminal, so pairing the protocol
// with the outcome is the whole point: it shows which terminals media actually
// works on rather than which ones we hope it works on.
func (m *Model) recordMedia(kind, outcome, errClass string, decodeMs int64) {
	if m.tel == nil {
		return
	}
	if kind == "emoji_image" && outcome == "ok" {
		telemetry.Feature("emoji_images")
	}
	if !m.firstTime("media/" + kind + "/" + outcome + "/" + errClass) {
		return
	}
	telemetry.MediaRendered(kind, m.imageProtocol(), outcome, decodeMs, errClass)
}

// imageProtocol names the terminal graphics protocol in use. matterbox
// implements exactly one — the Kitty protocol, with unicode placeholders — so
// this is "kitty" when the startup probe said yes and the profile is truecolor,
// and "none" otherwise. It is the first thing to check when a media feature
// looks unused: unused because unwanted, or unused because unsupported?
func (m *Model) imageProtocol() string {
	if m.emojiImg.graphicsReady() {
		return "kitty"
	}
	return "none"
}

// --- slow frames ----------------------------------------------------------

// slowFrameFloor is the frame time worth reporting. 50ms is where a render
// stops feeling instant and 200ms is where it reads as a hang; the floor sits
// between them so an ordinary heavy frame doesn't crowd out the ones that
// actually hurt.
const slowFrameFloor = 120 * time.Millisecond

// slowFrameGap and slowFrameBudget bound the reporting. A genuinely slow
// session would otherwise send an event per frame — thousands of them, all
// saying the same thing.
const (
	slowFrameGap    = 30 * time.Second
	slowFrameBudget = 12
)

// noteFrameCause records what this event is about to make the renderer do, so a
// slow frame can say whether it was a resize, an image transmit, an animation
// tick or ordinary work. Set from update() where the message type is known;
// View has no idea why it is running.
func (m *Model) noteFrameCause(cause string) {
	if m.tel == nil {
		return
	}
	m.tel.frameCause = cause
}

// recordFrame reports a render slow enough to be seen. Render cost is the
// recurring performance problem in this codebase and it has only ever been
// measured on one machine against one cache; this is the field version — which
// pane, at which terminal size, with how much history loaded.
//
// Called from View, which has a value receiver: the state it rate-limits
// against lives behind m.tel, so the mutation survives.
func (m *Model) recordFrame(took time.Duration) {
	if m.tel == nil || took < slowFrameFloor {
		return
	}
	now := time.Now()
	if m.tel.slowFrames >= slowFrameBudget || now.Sub(m.tel.lastSlowFrame) < slowFrameGap {
		// Still counted, so the rate is visible in the snapshot even when the
		// individual frames aren't reported.
		telemetry.Friction("slow_frame")
		return
	}
	m.tel.lastSlowFrame = now
	m.tel.slowFrames++
	cause := m.tel.frameCause
	if cause == "" {
		cause = "render"
	}
	telemetry.SlowFrame(m.currentContext(), took.Milliseconds(), len(m.posts), m.width, cause)
}

// searchBackend names which of the four search implementations ran, for
// search_run. The keyword path reports "local_fts" rather than "server": every
// search in the TUI goes through the local FTS index, and calling it something
// else would make the four-backend comparison the event exists for meaningless.
func searchBackend(semantic bool) string {
	if semantic {
		return "hybrid"
	}
	return "local_fts"
}

// searchScope names how wide the search was. A team:/in: modifier narrows it to
// specific conversations; without one it runs across the whole cache.
func searchScope(p parsedQuery) string {
	if p.team != "" || p.in != "" {
		return "channel"
	}
	return "all"
}

// searchTerms counts the words in a query. The count is the only thing about
// the query that is sent — it says whether people search with one word or a
// sentence, which is what decides whether the ranking should favour phrases.
func searchTerms(text string) int { return len(strings.Fields(text)) }

// searchHasOperators reports whether the query used any of the search syntax:
// the team:/in: modifiers, the "~" semantic prefix, or a quoted phrase. Whether
// the syntax is used at all decides whether it was worth building and whether it
// needs to be more discoverable. The "?" AI trigger is not counted here — the
// mode already says the agent ran, and an AI question is prose.
func searchHasOperators(raw string) bool {
	return modifierRe.MatchString(raw) || strings.Contains(raw, `"`) ||
		strings.HasPrefix(strings.TrimSpace(raw), "~")
}

// --- the unread feed -------------------------------------------------------

// recordFeedAction emits feed_used. The feed is matterbox's own idea rather
// than a Mattermost concept, so whether it is the main way people triage — or a
// tab nobody opens — is worth knowing before more is built on it. `items` is how
// many bubbles were waiting, which is what says whether triage means clearing
// three things or forty.
func (m *Model) recordFeedAction(action, via string) {
	if m.tel == nil {
		return
	}
	telemetry.FeedUsed(action, len(m.feed.entries), via)
	telemetry.Feature("feed")
}

// --- actions on a message --------------------------------------------------

// actedRecord builds a message_acted record for an action on a post. Several of
// these features (edit history, collapse, code copy) were expensive to build
// with no evidence anyone uses them, and `age` is what separates fixing a typo
// ten seconds later from editing something from three days ago — a different
// feature wearing the same key.
func (m *Model) actedRecord(action string, p *model.Post, via string) telemetry.Acted {
	a := telemetry.Acted{Action: action, Via: via, Outcome: "ok"}
	if p == nil {
		return a
	}
	a.Own = m.me != nil && p.UserId == m.me.Id
	if p.CreateAt > 0 {
		a.Age = time.UnixMilli(p.CreateAt)
	}
	return a
}

// recordActed emits message_acted for an action whose outcome is already
// settled — the local ones: a copy, a collapse, opening the edit history.
func (m *Model) recordActed(a telemetry.Acted) {
	if m.tel == nil {
		return
	}
	telemetry.MessageActed(a)
	telemetry.Feature(actedFeature(a.Action))
}

// reportActed wraps a mutation Cmd so message_acted carries the server's answer
// instead of the optimistic local one. The wrapped Cmd runs untouched and its
// message is passed straight through; this only reads it to decide the outcome.
//
// Wrapping rather than threading the record through addReactionCmd, deletePost
// and friends keeps the mutation helpers exactly as they were — they are shared
// by several call sites and by the games, none of which should grow a telemetry
// parameter to satisfy one of them.
func (m *Model) reportActed(cmd tea.Cmd, a telemetry.Acted) tea.Cmd {
	if m.tel == nil || cmd == nil {
		return cmd
	}
	return func() tea.Msg {
		msg := cmd()
		a.Outcome, _ = msgResult(msg)
		telemetry.MessageActed(a)
		telemetry.Feature(actedFeature(a.Action))
		return msg
	}
}

// msgResult reads a mutation's result message for a failure and classifies it.
// The messages the mutation Cmds can return are few and all carry their error in
// the same shape; anything else means the call came back clean.
func msgResult(msg tea.Msg) (outcome, class string) {
	var err error
	switch v := msg.(type) {
	case errMsg:
		err = v.err
	case reactionErrMsg:
		err = v.err
	case postEditedMsg:
		err = v.err
	case savedChangedMsg:
		err = v.err
	case pinnedChangedMsg:
		err = v.err
	}
	return telemetry.Classify(err)
}

// reactionSlot names where a picked emoji came from, for message_acted. The
// emoji itself is never sent — a custom shortcode can name a person or a team.
// The slot is what answers the actual question: does the configured
// quick-reaction bar hold the right five, or does everyone search past it?
func reactionSlot(searched bool, idx, existing int) string {
	if searched {
		return "searched"
	}
	// The picker lists the reactions already on the post first, then the
	// configured bar — so an index inside the first group is someone joining an
	// existing reaction rather than choosing one.
	if idx < existing {
		return "recent"
	}
	switch idx - existing {
	case 0:
		return "quickbar_1"
	case 1:
		return "quickbar_2"
	case 2:
		return "quickbar_3"
	case 3:
		return "quickbar_4"
	case 4:
		return "quickbar_5"
	}
	return "quickbar_other"
}

// savedAction names which half of the save toggle ran. Both are catalogued
// separately because they answer different questions: saving is adoption,
// unsaving is whether the list is ever revisited.
func savedAction(saved bool) string {
	if saved {
		return "save"
	}
	return "unsave"
}

// collapseAction names which way the fold went. expanded is the post's state
// *after* the toggle.
func collapseAction(expanded bool) string {
	if expanded {
		return "expand"
	}
	return "collapse"
}

// reactAction names which half of the reaction toggle ran. Removing a reaction
// is not an "I like this emoji" signal, and the catalogue keeps the two apart
// for the same reason the popularity counter does.
func reactAction(had bool) string {
	if had {
		return "unreact"
	}
	return "react"
}

// --- named features --------------------------------------------------------

// featureStart stamps the clock for a feature whose latency matters, returning
// the moment to hand back to recordFeature. A plain time.Time rather than more
// carried state: these all run inside one message cycle or carry the value on
// their own state.
func featureStart() time.Time { return time.Now() }

// noLatency is the started value for a feature whose duration says nothing — a
// local toggle, a form submission whose round trip the user was not waiting on.
// Named so a call site reads as a decision rather than a placeholder; the
// property is simply left off the event.
var noLatency time.Time

// recordFeature emits feature_used. The counted half of adoption already lives
// in usage_snapshot's `features` map; this event is for the features whose
// *outcome* matters as well as their frequency — a feature that is used but
// errors half the time needs the outcome dimension to be visible at all.
//
// size is the feature's own magnitude: posts summarised, rows returned,
// suggestions offered, files downloaded.
func (m *Model) recordFeature(id, via string, started time.Time, size int, err error) {
	if m.tel == nil {
		return
	}
	outcome, class := telemetry.Classify(err)
	u := telemetry.Use{Feature: id, Outcome: outcome, Size: size, Via: via, ErrorClass: class}
	if !started.IsZero() {
		u.LatencyMs = time.Since(started).Milliseconds()
	}
	telemetry.FeatureUsed(u)
}

// recordFeatureOutcome is recordFeature for a result that isn't an error value —
// an empty answer, a cancelled run.
func (m *Model) recordFeatureOutcome(id, via, outcome string, started time.Time, size int) {
	if m.tel == nil {
		return
	}
	u := telemetry.Use{Feature: id, Outcome: outcome, Size: size, Via: via}
	if !started.IsZero() {
		u.LatencyMs = time.Since(started).Milliseconds()
	}
	telemetry.FeatureUsed(u)
}

// errFromString revives an error from a message that carries its failure as a
// string (the SQL and search results cross the bubbletea boundary that way, to
// stay cheaply copyable). Only the classification is wanted, and Classify reads
// the text, so nothing is lost.
func errFromString(s string) error {
	if s == "" {
		return nil
	}
	return errors.New(s)
}

// reportFeature wraps a feature's own Cmd so feature_used carries the server's
// answer, the same trick reportActed uses for message mutations: the wrapped Cmd
// runs untouched and its message is only read to decide the outcome.
func (m *Model) reportFeature(id, via string, cmd tea.Cmd) tea.Cmd {
	if m.tel == nil || cmd == nil {
		return cmd
	}
	started := featureStart()
	return func() tea.Msg {
		msg := cmd()
		outcome, class := msgResult(msg)
		telemetry.FeatureUsed(telemetry.Use{
			Feature:    id,
			Outcome:    outcome,
			Via:        via,
			LatencyMs:  time.Since(started).Milliseconds(),
			ErrorClass: class,
		})
		return msg
	}
}

// --- Jira / GitLab / GitHub ------------------------------------------------

// recordForge emits forge_action. The forge integrations are the largest
// optional subsystem in the app, and the provider is the only thing about them
// that is sent — never the instance URL, the project, the repository or the
// issue key, any of which would name the organisation the user works for.
func (m *Model) recordForge(provider, action string, started time.Time, err error) {
	if m.tel == nil || provider == "" {
		return
	}
	outcome, class := telemetry.Classify(err)
	latency := int64(0)
	if !started.IsZero() {
		latency = time.Since(started).Milliseconds()
	}
	telemetry.ForgeAction(provider, forgeActionID(action), outcome, latency, class)
}

// forgeProviderID maps a provider to the catalogue's label. Derived from
// Provider.Name(), which is a fixed string in our own source rather than
// anything configured, and dropped to "" (no event) for an index that no longer
// resolves.
func forgeProviderID(p forge.Provider) string {
	if p == nil {
		return ""
	}
	switch strings.ToLower(p.Name()) {
	case "gitlab":
		return "gitlab"
	case "github":
		return "github"
	}
	return ""
}

// forgeActionID maps an internal action or Jira field name to the catalogue's
// label. The Jira writes are named by the field they set, which is already the
// vocabulary the event uses; anything unrecognised reports as a refresh rather
// than being dropped, since it is still a round trip to the provider.
func forgeActionID(action string) string {
	switch action {
	case "open", "status", "priority", "points", "assignee", "comment",
		"reply", "approve", "merge", "jobs", "refresh":
		return action
	}
	return "refresh"
}

// refProviderID names the provider behind a reference, whichever kind it is.
func refProviderID(m Model, r *reference) string {
	if r == nil {
		return ""
	}
	if r.kind == refJira {
		return "jira"
	}
	return forgeProviderID(m.forgeAt(r.forge))
}

// --- attachments -----------------------------------------------------------

// recordAttachment emits attachment_added for one pending upload. count is the
// number now waiting to go out, which says whether multi-file posts are a real
// case or a hypothetical one.
func (m *Model) recordAttachment(via string, att pendingAttachment, outcome string) {
	if m.tel == nil {
		return
	}
	telemetry.AttachmentAdded(via, attachmentKind(att.filename, att.mime), att.size, len(m.attachments), outcome)
	telemetry.Feature("attachments_" + via)
}

// attachmentKind maps a file to a coarse kind, from its MIME type where the
// clipboard supplied one and its extension otherwise. The filename itself is
// never sent — it routinely carries a project, a customer or a person's name.
func attachmentKind(filename, mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	case mime == "application/pdf":
		return "pdf"
	case strings.HasPrefix(mime, "text/"):
		return "text"
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg", ".avif":
		return "image"
	case ".mp4", ".mkv", ".webm", ".mov", ".avi":
		return "video"
	case ".mp3", ".ogg", ".wav", ".flac", ".m4a", ".opus":
		return "audio"
	case ".pdf":
		return "pdf"
	case ".txt", ".md", ".log", ".csv", ".json", ".yaml", ".yml", ".go", ".sql":
		return "text"
	case ".zip", ".tar", ".gz", ".bz2", ".xz", ".7z", ".rar":
		return "archive"
	}
	return "other"
}

// decodeMillis is the elapsed time since started, in milliseconds, or 0 when
// the caller had nothing to measure — which the catalogue's bucket reads as
// "under a millisecond", the only honest reading of an absent measurement here.
func decodeMillis(started time.Time) int64 {
	if started.IsZero() {
		return 0
	}
	return time.Since(started).Milliseconds()
}

// frameCause classifies the message that is about to drive a render, for
// slow_frame's cause. Deliberately coarse: the four cases are the ones with
// different fixes — a resize storm is the debounce, an image is the encode, an
// animation tick is the repaint budget, and everything else is the renderer.
func frameCause(msg tea.Msg) string {
	switch msg.(type) {
	case tea.WindowSizeMsg, resizeSettleMsg:
		return "resize"
	case inlineImagesFetchedMsg, inlineThumbFramesMsg, emojiImagesFetchedMsg,
		previewImageLoadedMsg, previewReencodedMsg, streamOpenedMsg, streamChunkMsg:
		return "image"
	case imgAnimTickMsg, effectsAnimTickMsg, previewTickMsg, previewStreamTickMsg:
		return "animation"
	}
	return "render"
}

// resyncReach describes how far the post-reconnect catch-up can reach, for
// ws_reconnected. It is what resyncAfterReconnect will actually attempt, which
// is the honest answer available at reconnect time:
//
//   - "full": the read state is refetched from server truth *and* the open
//     conversation is reconciled, so both the badges and the transcript recover.
//   - "partial": one of the two — usually no conversation open to reconcile.
//   - "failed": a conversation is open but the account isn't loaded, so the
//     unread state cannot be rebuilt at all and the badges stay wrong.
//   - "none": nothing to catch up on; no session state established yet.
func (m *Model) resyncReach() string {
	members := m.me != nil              // fetchChannelMembers will run
	transcript := m.openChannelID != "" // fetchRecent will run
	switch {
	case members && transcript:
		return "full"
	case members:
		return "partial"
	case transcript:
		return "failed"
	}
	return "none"
}

// --- failures --------------------------------------------------------------

// errSite labels a failure arriving on the shared errMsg path. Everything on
// that path is a Mattermost API call, so the only distinction worth drawing is
// the one that changes what the user has to do: a rejected session needs
// `matterbox login`, anything else needs the network or the server to come back.
//
// The split is taken from the error class rather than from isUnauthorized (which
// only knows 401 and the word itself, because that is all the status line needs),
// so an expired session recognised by any of Classify's shapes lands here too.
func errSite(err error) string {
	if telemetry.ClassifyError(err) == "auth" {
		return "auth.token"
	}
	return "api.other"
}

// actedFeature maps a message action to the feature counter it belongs to, or ""
// for the ones that are not a named feature (an edit, a delete). Returning ""
// is a no-op in Feature, so there is nothing to guard at the call site.
func actedFeature(action string) string {
	switch action {
	case "react", "unreact":
		return "reactions"
	case "copy_code":
		return "code_copy"
	case "collapse", "expand":
		return "collapse"
	case "save", "unsave":
		return "saved_messages"
	}
	return ""
}

// --- friction --------------------------------------------------------------

// helpAfterUnhandledWindow is how soon after a dead key opening help counts as
// "got stuck and went looking". Long enough to cover reading the screen and
// deciding, short enough that an unrelated help lookup minutes later doesn't
// get blamed on it.
const helpAfterUnhandledWindow = 8 * time.Second

// noteUnhandledAt records when a key did nothing, so a help lookup shortly
// afterwards can be attributed to it.
func (m *Model) noteUnhandledAt() {
	if m.tel == nil {
		return
	}
	m.tel.unhandledAt = time.Now()
}

// noteHelpOpened reports help_after_unhandled when help or the cheatsheet is
// opened in the wake of a dead key. On its own, "someone opened help" says
// nothing; following a keypress that did nothing, it is the clearest evidence
// the keymap disagreed with someone's expectation of it — and it names the
// surface, so the fix has an address.
func (m *Model) noteHelpOpened() {
	if m.tel == nil || m.tel.unhandledAt.IsZero() {
		return
	}
	dwell := time.Since(m.tel.unhandledAt)
	m.tel.unhandledAt = time.Time{}
	if dwell > helpAfterUnhandledWindow {
		return
	}
	telemetry.Friction("help_after_unhandled")
	telemetry.FrictionEvent(telemetry.Stuck{
		Signal:  "help_after_unhandled",
		Context: m.currentContext(),
		Dwell:   dwell,
	})
}

// noteComposerDiscarded reports a draft thrown away without being sent. The
// size is how much was typed, bucketed; the text itself never leaves the
// machine. A composer people keep clearing is one that is hard to correct in
// place.
func (m *Model) noteComposerDiscarded(text string) {
	if m.tel == nil || strings.TrimSpace(text) == "" {
		return
	}
	telemetry.Friction("composer_discarded")
	telemetry.FrictionEvent(telemetry.Stuck{
		Signal:  "composer_discarded",
		Context: m.currentContext(),
		Size:    len([]rune(text)),
	})
}

// noteScrollWall reports hitting the top of loaded history and having to wait
// for a fetch. It is the one place in the app where reading stops dead, and how
// often people reach it decides whether the render window (400 posts) and the
// cache page size are set anywhere near right.
func (m *Model) noteScrollWall() {
	if m.tel == nil {
		return
	}
	telemetry.Friction("scroll_wall")
	telemetry.FrictionEvent(telemetry.Stuck{
		Signal:  "scroll_wall",
		Context: m.currentContext(),
		Count:   len(m.posts),
	})
}

// resizeStormBurst and resizeStormWindow define the storm: this many size
// messages inside this window is a tiling window manager or a drag, which is
// the case the render debounce exists for. Reported once per burst.
const (
	resizeStormBurst  = 12
	resizeStormWindow = 2 * time.Second
)

// noteResize counts a size message and reports resize_storm when a burst
// crosses the threshold — the debounce working, or not working, in the field.
func (m *Model) noteResize() {
	if m.tel == nil {
		return
	}
	now := time.Now()
	if now.Sub(m.tel.resizeFirst) > resizeStormWindow {
		m.tel.resizeFirst = now
		m.tel.resizeRun = 0
	}
	m.tel.resizeRun++
	if m.tel.resizeRun != resizeStormBurst {
		return
	}
	telemetry.Friction("resize_storm")
	telemetry.FrictionEvent(telemetry.Stuck{
		Signal:  "resize_storm",
		Context: m.currentContext(),
		Count:   m.tel.resizeRun,
	})
}

// notePickerAbandoned reports a picker or modal closed without committing to
// anything. Several of these were expensive to build, and one that is opened
// and dismissed far more often than it is used is either offering the wrong
// things or is being opened by accident.
func (m *Model) notePickerAbandoned(surface string, opened time.Time) {
	if m.tel == nil {
		return
	}
	telemetry.Friction("picker_abandoned")
	s := telemetry.Stuck{Signal: "picker_abandoned", Context: surface}
	if !opened.IsZero() {
		s.Dwell = time.Since(opened)
	}
	telemetry.FrictionEvent(s)
}

// autoEditWindow is how soon after the app rewrote the draft an undo counts as
// rejecting that rewrite.
const autoEditWindow = 5 * time.Second

// noteAutoComposerEdit records that something other than typing changed the
// draft — a grammar suggestion accepted, a template inserted. The composer's
// own undo landing shortly afterwards is then a verdict on it.
func (m *Model) noteAutoComposerEdit() {
	if m.tel == nil {
		return
	}
	m.tel.autoEditAt = time.Now()
}

// noteUndo reports undo_after_edit when the composer's undo follows an edit the
// app made rather than one the user typed: the suggestion, template or
// expansion was wrong, which is a far more actionable finding than the raw undo
// count the action counters already carry.
func (m *Model) noteUndo() {
	if m.tel == nil || m.tel.autoEditAt.IsZero() {
		return
	}
	dwell := time.Since(m.tel.autoEditAt)
	m.tel.autoEditAt = time.Time{}
	if dwell > autoEditWindow {
		return
	}
	telemetry.Friction("undo_after_edit")
	telemetry.FrictionEvent(telemetry.Stuck{
		Signal:  "undo_after_edit",
		Context: m.currentContext(),
		Dwell:   dwell,
	})
}

// notePickerOpened stamps the clock for the picker or modal now opening, so an
// abandonment can report how long the person spent in it before giving up. Only
// one is ever open at a time, so one field is enough.
func (m *Model) notePickerOpened() {
	if m.tel == nil {
		return
	}
	m.tel.pickerOpenedAt = time.Now()
}

// pickerAt returns when the open picker was raised, or the zero time when
// nothing recorded it. Nil-safe so a call site can read it inline.
func (t *uiTelemetry) pickerAt() time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.pickerOpenedAt
}
