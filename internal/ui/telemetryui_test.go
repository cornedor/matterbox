package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/replyto"
	"matterbox/internal/telemetry"
)

// The classifiers in telemetryui.go all map app state onto a closed set the
// catalogue declares. A value outside that set is dropped in production, on a
// user's machine, where nobody sees it — so every one of them is checked against
// the catalogue's own list here rather than against a hand-written expectation.

// TestChannelKindsAreCatalogued: channel_opened's channel_type is the one
// property that says what kind of conversation people live in, and an
// unrecognised Mattermost type must degrade to "unknown" rather than to nothing.
func TestChannelKindsAreCatalogued(t *testing.T) {
	cases := map[model.ChannelType]string{
		model.ChannelTypeOpen:    "public",
		model.ChannelTypePrivate: "private",
		model.ChannelTypeDirect:  "dm",
		model.ChannelTypeGroup:   "group_dm",
		model.ChannelType("Z"):   "unknown",
	}
	for typ, want := range cases {
		got := channelKind(&model.Channel{Type: typ})
		if got != want {
			t.Errorf("channelKind(%q) = %q, want %q", typ, got, want)
		}
		assertInSet(t, "channel_opened", "channel_type", got)
	}
	if got := channelKind(nil); got != "unknown" {
		t.Errorf("channelKind(nil) = %q, want unknown", got)
	}
}

// TestCacheStateDistinguishesWarmFromPartial is the property the warm-open path
// is measured by, so the three states have to mean what they say: nothing from
// the cache is cold, a full page is warm, and a short page is partial — the case
// where the transcript painted incomplete and the reconcile filled it in.
func TestCacheStateDistinguishesWarmFromPartial(t *testing.T) {
	cases := []struct {
		name   string
		cached int
		want   string
	}{
		{"nothing cached", 0, "cold"},
		{"negative is nothing", -1, "cold"},
		{"a full page", initialRenderLimit, "warm"},
		{"more than a page", initialRenderLimit + 10, "warm"},
		{"one short of a page", initialRenderLimit - 1, "partial"},
		{"a handful", 5, "partial"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cacheState(c.cached)
			if got != c.want {
				t.Errorf("cacheState(%d) = %q, want %q", c.cached, got, c.want)
			}
			assertInSet(t, "channel_opened", "cache", got)
		})
	}
}

// TestReactionSlotNamesTheQuickbarPosition: the emoji itself is never sent — a
// custom shortcode can name a person or a team — so the slot is the whole signal,
// and it has to tell "joined an existing reaction" from "took the first quickbar
// entry" from "searched past the bar entirely".
func TestReactionSlotNamesTheQuickbarPosition(t *testing.T) {
	cases := []struct {
		name          string
		searched      bool
		idx, existing int
		want          string
	}{
		{"searched", true, 0, 0, "searched"},
		{"searched past a bar", true, 7, 2, "searched"},
		{"joining an existing reaction", false, 0, 2, "recent"},
		{"first configured entry", false, 0, 0, "quickbar_1"},
		{"fifth configured entry", false, 4, 0, "quickbar_5"},
		{"past the fifth", false, 5, 0, "quickbar_other"},
		{"first configured entry, after two existing", false, 2, 2, "quickbar_1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reactionSlot(c.searched, c.idx, c.existing)
			if got != c.want {
				t.Errorf("reactionSlot(%v, %d, %d) = %q, want %q", c.searched, c.idx, c.existing, got, c.want)
			}
			assertInSet(t, "message_acted", "reaction_slot", got)
		})
	}
}

// TestAttachmentKindNeverEchoesTheFilename: the filename routinely carries a
// customer, a project or a person's name, so only the coarse kind may be sent —
// and every branch has to land inside the catalogue's set.
func TestAttachmentKindNeverEchoesTheFilename(t *testing.T) {
	cases := []struct{ filename, mime, want string }{
		{"", "image/png", "image"},
		{"", "video/mp4", "video"},
		{"", "audio/ogg", "audio"},
		{"", "application/pdf", "pdf"},
		{"", "text/plain", "text"},
		{"acme-q3-forecast.png", "", "image"},
		{"standup.MP4", "", "video"},
		{"call.opus", "", "audio"},
		{"contract.pdf", "", "pdf"},
		{"schema.sql", "", "text"},
		{"logs.tar.gz", "", "archive"},
		{"weird.qqq", "", "other"},
		{"noextension", "", "other"},
	}
	for _, c := range cases {
		got := attachmentKind(c.filename, c.mime)
		if got != c.want {
			t.Errorf("attachmentKind(%q, %q) = %q, want %q", c.filename, c.mime, got, c.want)
		}
		assertInSet(t, "attachment_added", "kind", got)
	}
}

// TestForgeActionIDsAreCatalogued: the Jira writes are named by the field they
// set, which is config vocabulary; anything the catalogue doesn't know must fall
// back to a declared value rather than being dropped, since the round trip
// happened either way.
func TestForgeActionIDsAreCatalogued(t *testing.T) {
	for _, action := range []string{
		"open", "status", "priority", "points", "assignee", "comment",
		"reply", "approve", "merge", "jobs", "refresh", "something_new", "",
	} {
		assertInSet(t, "forge_action", "action", forgeActionID(action))
	}
}

// TestFrameCauseIsCatalogued walks the messages that actually drive renders and
// checks each maps to a declared cause. A slow frame with an invalid cause loses
// the one property that says which fix it needs.
func TestFrameCauseIsCatalogued(t *testing.T) {
	for _, c := range []struct {
		msg  tea.Msg
		want string
	}{
		{tea.WindowSizeMsg{}, "resize"},
		{resizeSettleMsg{}, "resize"},
		{inlineImagesFetchedMsg{}, "image"},
		{emojiImagesFetchedMsg{}, "image"},
		{previewImageLoadedMsg{}, "image"},
		{streamOpenedMsg{}, "image"},
		{imgAnimTickMsg{}, "animation"},
		{effectsAnimTickMsg{}, "animation"},
		{previewTickMsg{}, "animation"},
		{tea.KeyPressMsg{}, "render"},
	} {
		got := frameCause(c.msg)
		if got != c.want {
			t.Errorf("frameCause(%T) = %q, want %q", c.msg, got, c.want)
		}
		assertInSet(t, "slow_frame", "cause", got)
	}
}

// TestResyncReachIsCatalogued: "failed" is the case worth having — a socket that
// comes back while the account is unknown cannot rebuild the unread badges, so
// the reconnect recovered the connection and not the state.
func TestResyncReachIsCatalogued(t *testing.T) {
	cases := []struct {
		name string
		me   *model.User
		open string
		want string
	}{
		{"members and a conversation", &model.User{Id: "u"}, "c1", "full"},
		{"members only", &model.User{Id: "u"}, "", "partial"},
		{"a conversation but no account", nil, "c1", "failed"},
		{"nothing established yet", nil, "", "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &Model{me: c.me, openChannelID: c.open}
			got := m.resyncReach()
			if got != c.want {
				t.Errorf("resyncReach() = %q, want %q", got, c.want)
			}
			assertInSet(t, "ws_reconnected", "resync", got)
		})
	}
}

// TestActedFeatureMapsToCataloguedFeatures: the counter map is a whitelist, so a
// feature id it doesn't know is silently dropped — which reads as "nobody uses
// this feature", the exact conclusion the counter exists to support.
func TestActedFeatureMapsToCataloguedFeatures(t *testing.T) {
	for _, action := range []string{
		"react", "unreact", "copy_code", "collapse", "expand", "save", "unsave",
	} {
		id := actedFeature(action)
		if id == "" {
			t.Errorf("actedFeature(%q) mapped to nothing", action)
			continue
		}
		if !inStrings(telemetry.FeatureIDs, id) {
			t.Errorf("actedFeature(%q) = %q, which telemetry.FeatureIDs doesn't declare", action, id)
		}
	}
	// The actions that are not a named feature report nothing, which Feature
	// treats as a no-op.
	for _, action := range []string{"edit", "delete", "history", "pin"} {
		if id := actedFeature(action); id != "" {
			t.Errorf("actedFeature(%q) = %q, want no feature", action, id)
		}
	}
}

// TestSearchHelpersDescribeTheQueryNotItsText: search_run carries the shape of a
// query and nothing else, so the term count and the operator flag are the whole
// contract — and the mode/scope have to be catalogue values.
func TestSearchHelpersDescribeTheQueryNotItsText(t *testing.T) {
	if got := searchTerms("deploy failed on staging"); got != 4 {
		t.Errorf("searchTerms = %d, want 4", got)
	}
	if got := searchTerms("   "); got != 0 {
		t.Errorf("searchTerms of blank = %d, want 0", got)
	}
	for _, raw := range []string{`in:platform auth`, `team:"Core Team" bug`, `"exact phrase"`, `~semantic question`} {
		if !searchHasOperators(raw) {
			t.Errorf("searchHasOperators(%q) = false, want true", raw)
		}
	}
	if searchHasOperators("just some words") {
		t.Error("plain prose was reported as using search operators")
	}
	assertInSet(t, "search_run", "mode", searchBackend(true))
	assertInSet(t, "search_run", "mode", searchBackend(false))
	assertInSet(t, "search_run", "scope", searchScope(parsedQuery{in: "platform"}))
	assertInSet(t, "search_run", "scope", searchScope(parsedQuery{}))
}

// TestMediaReportsEachOutcomeOnce: media_rendered answers "does this work on
// this terminal", which one event per outcome answers; a per-image stream would
// answer nothing extra at a hundred times the volume.
func TestMediaReportsEachOutcomeOnce(t *testing.T) {
	m := &Model{tel: &uiTelemetry{}}
	if !m.firstTime("media/inline_image/ok/") {
		t.Fatal("the first observation was not reported")
	}
	if m.firstTime("media/inline_image/ok/") {
		t.Error("the same observation was reported twice")
	}
	if !m.firstTime("media/inline_image/error/parse") {
		t.Error("a different outcome was suppressed by the first one")
	}
	// Past the budget it fails closed rather than becoming a stream.
	for i := 0; i < onceBudget*2; i++ {
		m.firstTime("filler-" + time.Duration(i).String())
	}
	if m.firstTime("media/video/ok/") {
		t.Error("the once-set grew past its budget")
	}
	// Nil-safe: an opted-out session has no carried state at all.
	var off Model
	if off.firstTime("anything") {
		t.Error("an opted-out session reported a media observation")
	}
}

// TestErrSiteSplitsAuthFromTheRest fixes the one distinction the shared failure
// path can honestly draw: a rejected session needs `matterbox login`, anything
// else needs the network or the server back.
func TestErrSiteSplitsAuthFromTheRest(t *testing.T) {
	if got := errSite(errors.New("Invalid or expired session, please login again")); got != "auth.token" {
		t.Errorf("errSite(session) = %q, want auth.token", got)
	}
	if got := errSite(errors.New("dial tcp: connection refused")); got != "api.other" {
		t.Errorf("errSite(network) = %q, want api.other", got)
	}
	for _, err := range []error{errors.New("unauthorized"), errors.New("boom")} {
		if !inStrings(telemetry.FailureSites, errSite(err)) {
			t.Errorf("errSite returned %q, which telemetry.FailureSites doesn't declare", errSite(err))
		}
	}
}

// assertInSet fails when value isn't one the catalogue allows for that event's
// property. This is the check that matters: an out-of-set value is dropped
// silently in production, so a classifier that drifts from the catalogue loses
// the property rather than failing loudly.
func assertInSet(t *testing.T, event, prop, value string) {
	t.Helper()
	spec, ok := telemetry.Spec(event)
	if !ok {
		t.Fatalf("event %q is not catalogued", event)
	}
	for _, p := range spec.Props {
		if p.Name != prop {
			continue
		}
		if !inStrings(p.Values, value) {
			t.Errorf("%s.%s = %q, which the catalogue doesn't allow (%v)", event, prop, value, p.Values)
		}
		return
	}
	t.Fatalf("event %q has no property %q", event, prop)
}

func inStrings(set []string, s string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// TestNewEventsPassTheCatalogueStrictly is the end-to-end guard on everything
// wired above. Strict mode turns a dropped property into a panic, so driving the
// real paths with it on fails loudly here for exactly the mistake that would
// fail silently on a user's machine: an enum value the catalogue doesn't allow,
// or a property name that doesn't exist.
//
// It is deliberately a walk rather than a unit test per event — the value of the
// check is that it covers the actual call sites, including the ones that build
// their properties several layers away from the emitter.
func TestNewEventsPassTheCatalogueStrictly(t *testing.T) {
	in := startTelemetry(t)
	prev := telemetry.SetStrict(true)
	t.Cleanup(func() { telemetry.SetStrict(prev) })

	m := newTestModel()
	if m.tel == nil {
		t.Fatal("an opted-in session built no telemetry state")
	}
	m.width, m.height = 120, 40

	// The launch event, with every late-arriving fact in — the case the staged
	// assembly exists for, and the one that carries image_protocol and the
	// sidebar counts.
	m.teams = []*model.Team{{Id: "t1"}, {Id: "t2"}}
	m.channels = map[string][]*model.Channel{"t1": {{Id: "c1"}, {Id: "c2"}}, "t2": {{Id: "c3"}}}
	m.teamsLoaded, m.channelsLoaded = true, true
	m.noteLaunchSize()
	m.noteLaunchGraphics()
	m.noteLaunchLists()
	if m.launchEnv != nil {
		t.Error("app_started was not sent once every fact was in")
	}

	// A conversation opened by every route the app has, painted cold.
	for _, via := range []string{
		"sidebar_key", "sidebar_mouse", "switcher", "filter", "team_jump",
		"dm_jump", "feed", "search_hit", "permalink", "cli", "restore", "palette",
	} {
		m.armChannelOpen("c1", via)
		m.recordChannelOpened("c1")
	}

	// A thread, with and without nesting.
	m.armThreadOpen("root", "key")
	m.recordThreadOpened("root", []*model.Post{{Id: "root"}, {Id: "r1", RootId: "root"}})

	// Each search backend, and a hit opened from it.
	for _, mode := range []string{"local_fts", "hybrid", "ai"} {
		m.armSearch(mode, "all", 3, true)
		m.recordSearchRun(4, false)
		m.recordSearchHitOpened(0)
	}
	m.armSearch("local_fts", "channel", 1, false)
	m.recordSearchRun(0, true)

	// The feed, every action.
	for _, action := range []string{
		"opened", "mark_read", "mark_all_read", "reply", "open_channel",
		"toggle_muted", "refresh",
	} {
		m.recordFeedAction(action, "key")
	}

	// Actions on a message, local and server-answered.
	p := &model.Post{Id: "p1", UserId: "u-me", CreateAt: time.Now().Add(-time.Hour).UnixMilli()}
	for _, action := range []string{
		"edit", "delete", "react", "unreact", "copy_markdown", "copy_code",
		"collapse", "expand", "save", "unsave", "history", "pin",
	} {
		m.recordActed(m.actedRecord(action, p, "key"))
	}
	acted := m.actedRecord("react", p, "picker")
	acted.ReactionSlot = reactionSlot(false, 0, 0)
	if cmd := m.reportActed(func() tea.Msg { return errMsg{errors.New("403 forbidden")} }, acted); cmd != nil {
		cmd()
	}

	// Named features, with and without an error.
	m.recordFeature("summary", "palette", time.Now().Add(-time.Second), 42, nil)
	m.recordFeature("sql_tab", "key", time.Now(), 0, errors.New("no such table"))
	m.recordFeatureOutcome("summary", "palette", "empty", noLatency, 0)

	// Forge round trips, read and write.
	m.recordForge("jira", "status", time.Now(), nil)
	m.recordForge("gitlab", "merge", time.Now(), errors.New("409 conflict"))
	m.recordForge("github", "open", noLatency, nil)

	// Attachments, terminal graphics, a slow frame, the websocket.
	m.recordAttachment("paste", pendingAttachment{filename: "a.png", mime: "image/png", size: 1 << 18}, "ok")
	m.recordAttachment("drop", pendingAttachment{}, "denied")
	m.recordMedia("inline_image", "ok", "", 12)
	m.recordMedia("video", "error", "unsupported", 900)
	m.tel.frameCause = "resize"
	m.tel.lastSlowFrame = time.Time{}
	m.recordFrame(slowFrameFloor + time.Millisecond)
	m.noteWSDropped(errors.New("dial tcp: connection refused"), false)
	m.noteWSDropped(errors.New("ping timeout"), true)
	m.noteWSDropped(nil, false)
	m.noteWSConnected(3, m.resyncReach())

	// The friction signals with an address.
	m.noteUnhandledAt()
	m.noteHelpOpened()
	m.noteComposerDiscarded("a draft nobody sent")
	m.noteScrollWall()
	m.notePickerAbandoned("modal:reaction-picker", time.Now().Add(-2*time.Second))
	m.noteAutoComposerEdit()
	m.noteUndo()
	for range resizeStormBurst {
		m.noteResize()
	}

	telemetry.Close()

	// A spot check that the walk actually produced events rather than being
	// silently inert — strict mode only catches malformed ones.
	body := in.all()
	for _, want := range []string{
		"app_started", "channel_opened", "thread_opened", "search_run", "search_result_opened",
		"feed_used", "message_acted", "feature_used", "forge_action",
		"attachment_added", "media_rendered", "slow_frame", "ws_disconnected",
		"ws_reconnected", "friction",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the walk produced no %s event", want)
		}
	}
}

// TestLaunchWaitsForTheLateFactsButNotForever is the deadline that makes
// app_started's staging safe. The graphics probe and the channel list are the
// two most useful environment facts and both arrive late, but a launch that
// never reaches the server must still be counted — otherwise every rate computed
// against it is biased by exactly the launches most worth knowing about.
func TestLaunchWaitsForTheLateFactsButNotForever(t *testing.T) {
	in := startTelemetry(t)

	m := newTestModel()
	m.width, m.height = 100, 30

	// A size alone is not enough: the late facts are still outstanding.
	m.noteLaunchSize()
	if m.launchEnv == nil {
		t.Fatal("app_started went out before the late facts or the deadline")
	}
	// Half the sidebar is not the sidebar.
	m.teamsLoaded = true
	m.noteLaunchLists()
	if m.launchEnv == nil {
		t.Fatal("app_started went out with only half the channel list loaded")
	}
	// The deadline gives up waiting and sends what is known.
	m.noteLaunchDeadline()
	if m.launchEnv != nil {
		t.Fatal("the deadline did not release app_started")
	}
	// And it is once: a fact arriving afterwards must not send a second one.
	m.channelsLoaded = true
	m.noteLaunchLists()
	m.noteLaunchGraphics()

	telemetry.Close()
	body := in.all()
	if n := strings.Count(body, `"app_started"`); n != 1 {
		t.Errorf("app_started sent %d times, want exactly one:\n%s", n, body)
	}
	// The counts were never known, so they must be absent rather than zero — a
	// zero would read as "this user has no channels".
	if strings.Contains(body, `"channels":"0"`) {
		t.Errorf("an unknown channel count was sent as zero:\n%s", body)
	}
}

// TestLaunchNeedsATerminalSize: the size is the one fact the event will not go
// out without, because a size of zero describes no terminal anyone is using.
func TestLaunchNeedsATerminalSize(t *testing.T) {
	in := startTelemetry(t)

	m := newTestModel()
	m.noteLaunchGraphics()
	m.noteLaunchLists()
	m.noteLaunchDeadline()
	if m.launchEnv == nil {
		t.Error("app_started went out with no terminal size at all")
	}
	telemetry.Close()
	if body := in.all(); strings.Contains(body, "app_started") {
		t.Errorf("app_started described a terminal that never reported a size:\n%s", body)
	}
}

// TestChannelOpenedReportsTheRouteAndTheCache drives the real open path so the
// event is checked where it is actually produced: enterChannel takes the route
// as an argument precisely so a new entry point can't be added without naming
// one, and this is what proves the argument reaches the event.
func TestChannelOpenedReportsTheRouteAndTheCache(t *testing.T) {
	in := startTelemetry(t)

	m := newTestModel()
	m.width, m.height = 120, 40
	m.channels = map[string][]*model.Channel{"t1": {{Id: "c1", Type: model.ChannelTypeDirect}}}
	m.unread = map[string]int{"c1": 3}

	// No store, so nothing is cached: this is the cold path, which reports when
	// the fetched page lands rather than at the keystroke.
	m.openChannelLoadCmd("c1", "switcher")
	if m.tel.open.channelID != "c1" {
		t.Fatal("the open was not armed")
	}
	m.update(postsLoadedMsg{channelID: "c1", posts: []*model.Post{{Id: "p1"}, {Id: "p2"}}})
	if m.tel.open.channelID != "" {
		t.Error("the open stayed armed after the transcript was painted")
	}
	// A second paint of the same channel must not report again.
	m.update(postsLoadedMsg{channelID: "c1", posts: []*model.Post{{Id: "p1"}}})

	telemetry.Close()
	body := in.all()
	if n := strings.Count(body, "channel_opened"); n != 1 {
		t.Errorf("channel_opened sent %d times, want 1:\n%s", n, body)
	}
	for _, want := range []string{`"via":"switcher"`, `"channel_type":"dm"`, `"cache":"cold"`, `"was_unread":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("channel_opened missing %s:\n%s", want, body)
		}
	}
}

// TestMessageShapeReadsThePayloadChannel: is_nested_reply and has_effect live in
// the invisible payload appended to the body, and the visible length must be
// measured on the text alone — an invisible payload inflating `length` would
// make every effect-carrying message look like an essay.
func TestMessageShapeReadsThePayloadChannel(t *testing.T) {
	plain := messageShape("hello there", "composer", "", 0, 0)
	if plain.IsNestedReply || plain.HasEffect {
		t.Errorf("a plain message claimed a payload: %+v", plain)
	}
	if plain.Length != len("hello there") {
		t.Errorf("length = %d, want %d", plain.Length, len("hello there"))
	}

	nested := messageShape(replyto.Attach("answering that", "abcdefghijklmnopqrstuvwxyz"), "thread", "root1", 0, 0)
	if !nested.IsNestedReply {
		t.Error("a nested reply's parent payload was not detected")
	}
	if nested.Length != len("answering that") {
		t.Errorf("the payload inflated the length: %d, want %d", nested.Length, len("answering that"))
	}

	withEffect := compileEffects(`\shimmer{today}`)
	shaped := messageShape(withEffect, "composer", "", 0, 0)
	if !shaped.HasEffect {
		t.Errorf("an effect payload was not detected in %q", withEffect)
	}
	if shaped.Length != len("today") {
		t.Errorf("the effect payload inflated the length: %d, want %d", shaped.Length, len("today"))
	}
	if shaped.HasLink || shaped.HasCodeBlock {
		t.Errorf("an effect message claimed a link or a code block: %+v", shaped)
	}
}
