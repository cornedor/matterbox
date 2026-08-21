package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/config"
	"matterbox/internal/telemetry"
)

// Launch telemetry: one app_started event per session, describing the machine
// and the configuration rather than the person.
//
// It is assembled in stages, because the facts arrive at different times and the
// most valuable ones arrive last. New() has the config — Model deliberately
// doesn't retain it, unpacking what it needs into fields instead — so the feature
// and keybinding facts are gathered there and held in launchEnv. The terminal
// size arrives with the first WindowSizeMsg. The graphics protocol is only known
// once the terminal has answered the startup probe, up to three seconds later.
// The team and channel counts only once the sidebar has loaded.
//
// So the event waits for all four, with a deadline: whichever comes first. That
// deadline is the point. Waiting indefinitely for the channel list would mean a
// launch that never reaches the server goes uncounted, which would quietly bias
// every rate computed against it — exactly the launches most worth knowing
// about. Waiting only for the size (which is what this did originally) meant
// image_protocol and the counts were always absent, so "is this terminal capable" and
// "how big is this sidebar" — the two questions most environment findings turn
// on — could not be asked at all.
//
// A fact still missing when the deadline passes is left out rather than sent as
// a wrong default: an absent property reads as "not known at launch", while a
// zero would read as "this user has no channels".
//
// launchEnv is a pointer so an opted-out session carries eight nil bytes rather
// than the struct, and so consuming it doubles as the "already sent" flag.

// buildInfo is what only the command layer knows: the version this binary was
// stamped with and the tags it was compiled with. Set once by cli before the
// program runs, so the ui package doesn't have to reach into the build stamp.
var buildInfo struct {
	version   string
	buildTags string
}

// SetBuildInfo records the build's identity for the launch event. Called from
// internal/cli, which owns the version stamp and the build stamp. Safe to skip:
// the properties are simply left out of the event.
func SetBuildInfo(version, buildTags string) {
	buildInfo.version = version
	buildInfo.buildTags = buildTags
}

// processStart is when this process began, for the startup-time measurement.
// Package init is close enough — everything before it is the Go runtime coming
// up, which is not something matterbox can change.
var processStart = time.Now()

// newLaunchEnv assembles everything about this launch that is known before the
// terminal has reported its size. Returns nil when telemetry is off, which is
// what keeps an opted-out session from building it at all.
func newLaunchEnv(cfg *config.Config, mouseEnabled bool) *telemetry.Env {
	if !telemetry.Enabled() {
		return nil
	}
	return &telemetry.Env{
		Version:   buildInfo.version,
		BuildTags: buildInfo.buildTags,
		GoVersion: telemetry.GoVersion(),
		OS:        telemetry.OSName(),
		Arch:      telemetry.ArchName(),
		Terminal:  telemetry.DetectTerminal(),
		// Filled in later, when they are known: the graphics protocol once the
		// probe has answered, the counts once the sidebar has loaded. See the
		// package note above.
		FeaturesOn:   enabledFeatures(cfg),
		MouseEnabled: mouseEnabled,
		NavModifier:  navModifierFromConfig(cfg),
		Overridden:   overriddenActions(cfg),
		// Sentinel for "we hadn't asked yet"; a zero would read as "this user
		// has no teams".
		Teams:    -1,
		Channels: -1,
	}
}

// launchGrace is how long the launch event waits for the facts that arrive late
// — the graphics probe (bounded at emojiProbeTimeout, 3s) and the channel list
// (bounded by nothing at all). Past it the event goes out with whatever is
// known, so a launch that never reaches the server is still counted.
const launchGrace = 5 * time.Second

// launchDeadlineMsg fires when launchGrace is up.
type launchDeadlineMsg struct{}

// launchDeadlineCmd arms the deadline. Returns nil when telemetry is off, so an
// opted-out session gets no extra timer.
func (m *Model) launchDeadlineCmd() tea.Cmd {
	if m.launchEnv == nil {
		return nil
	}
	return tea.Tick(launchGrace, func(time.Time) tea.Msg { return launchDeadlineMsg{} })
}

// noteLaunchSize records the first WindowSizeMsg. The terminal size is the single
// most useful environment fact a TUI has — it decides whether the three-pane
// layout fits at all — and it is also the only one the event will not go out
// without, since a size of zero would describe no terminal anyone is using.
func (m *Model) noteLaunchSize() { m.markLaunch(func(t *uiTelemetry) { t.sizeKnown = true }) }

// noteLaunchGraphics records that the startup graphics probe has resolved, one
// way or the other. Called from both the reply and the timeout, because both
// settle the question.
func (m *Model) noteLaunchGraphics() { m.markLaunch(func(t *uiTelemetry) { t.graphicsKnown = true }) }

// noteLaunchLists records that the sidebar has loaded, once both halves are in.
func (m *Model) noteLaunchLists() {
	if !m.teamsLoaded || !m.channelsLoaded {
		return
	}
	m.markLaunch(func(t *uiTelemetry) { t.listsKnown = true })
}

// noteLaunchDeadline gives up waiting and sends with what is known.
func (m *Model) noteLaunchDeadline() { m.markLaunch(func(t *uiTelemetry) { t.launchOverdue = true }) }

// markLaunch applies one readiness fact and sends the event if that was the
// last one outstanding.
//
// The fact itself lands behind m.tel, so it sticks even when the caller holds a
// Model copy (Init does). The send can't misfire from such a copy: it requires
// sizeKnown, which is only ever set from the WindowSizeMsg handler on the live
// model, and launchSent — also behind the pointer — makes it once regardless.
func (m *Model) markLaunch(set func(*uiTelemetry)) {
	if m.tel == nil || m.launchEnv == nil {
		return
	}
	set(m.tel)
	m.maybeRecordLaunch()
}

// maybeRecordLaunch emits app_started once the late-arriving facts are in, or
// once the deadline has passed. Consuming launchEnv is what makes it once.
func (m *Model) maybeRecordLaunch() {
	t := m.tel
	if m.launchEnv == nil || t == nil || t.launchSent || !t.sizeKnown {
		return
	}
	if !t.launchOverdue && !(t.graphicsKnown && t.listsKnown) {
		return
	}
	env := *m.launchEnv
	m.launchEnv = nil
	t.launchSent = true

	env.Cols = m.width
	env.Rows = m.height
	env.StartupMillis = time.Since(processStart).Milliseconds()
	// Anything already on screen came out of the local cache rather than the
	// network — the warm-open path this measures.
	env.CacheWarm = len(m.posts) > 0
	if t.graphicsKnown {
		env.ImageProtocol = m.imageProtocol()
	}
	if t.listsKnown {
		env.Teams = len(m.teams)
		env.Channels = m.sidebarChannelCount()
	}
	telemetry.AppStarted(env)
	// The version this build is, against the last one this machine saw. Done
	// here rather than at startup because it wants the message cache, which is
	// where the last-seen version is remembered — and because it reads best
	// immediately after app_started.
	telemetry.CheckVersion(m.store, buildInfo.version)
}

// sidebarChannelCount is how many conversations the sidebar can show, across
// every team and the DM buckets. Sidebar and switcher design depends on this
// figure and we are currently guessing at it.
func (m *Model) sidebarChannelCount() int {
	n := 0
	for _, list := range m.channels {
		n += len(list)
	}
	return n
}

// enabledFeatures lists the optional features this config has switched on, as
// catalogue feature ids. This is the denominator for every adoption question:
// "the SQL tab is unused" means nothing without knowing how many people have it
// enabled, and most of these are off by default.
//
// Presence of an endpoint or a token is the test for the integrations, because
// that is exactly what makes them work — there is no separate enable flag.
func enabledFeatures(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	var on []string
	add := func(id string, yes bool) {
		if yes {
			on = append(on, id)
		}
	}
	// Local-LLM features share one endpoint; both are unusable without it.
	add("summary", cfg.Summary.Endpoint != "")
	add("ai_search", cfg.Summary.Endpoint != "")
	add("semantic_search", cfg.Embeddings.Endpoint != "")
	add("sql_tab", boolAt(cfg.SQLTab))
	add("grammar_check", cfg.LanguageToolEnabled())
	add("jira", cfg.Jira.BaseURL != "")
	add("gitlab", cfg.GitLab.BaseURL != "")
	add("github", cfg.GitHub.BaseURL != "")
	add("emoji_images", cfg.EmojiImages != "" && cfg.EmojiImages != "off")
	add("image_preview", cfg.ImageThumbnails != "" && cfg.ImageThumbnails != "off")
	add("custom_status", boolAt(cfg.CustomStatus))
	add("rules", len(cfg.Rules) > 0)
	add("templates", true) // always available; the counter says whether it's used
	add("feed", true)
	return on
}

// overriddenActions lists the actions whose keys the user has rebound. A default
// that people keep replacing is a default that is wrong, and this is the only
// way to find out which ones those are.
func overriddenActions(cfg *config.Config) []string {
	if cfg == nil || len(cfg.Keybindings.Bindings) == 0 {
		return nil
	}
	out := make([]string, 0, len(cfg.Keybindings.Bindings))
	for id := range cfg.Keybindings.Bindings {
		// Only ids the registry knows: an unknown one fails startup validation
		// anyway, and the catalogue would drop it.
		if _, ok := actionByID[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// boolAt dereferences one of config's tri-state bools, treating absent as
// false. Every one of them is filled in by fillDefaults, so this only guards
// the hand-built configs in tests.
func boolAt(p *bool) bool { return p != nil && *p }
