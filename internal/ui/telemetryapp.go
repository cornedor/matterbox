package ui

import (
	"time"

	"matterbox/internal/config"
	"matterbox/internal/telemetry"
)

// Launch telemetry: one app_started event per session, describing the machine
// and the configuration rather than the person.
//
// It is assembled in two halves. New() has the config — Model deliberately
// doesn't retain it, unpacking what it needs into fields instead — so the
// feature and keybinding facts are gathered there and held in launchEnv. The
// terminal size only arrives with the first WindowSizeMsg, and it is the single
// most useful environment fact a TUI has (it decides whether the three-pane
// layout fits at all), so that is where the event is finally sent.
//
// The first size message is also the earliest moment guaranteed to happen.
// Waiting for the channel list would mean a launch that never reaches the
// server goes uncounted, which would quietly bias every rate computed against
// it — exactly the launches most worth knowing about.
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
		// ImageProtocol is deliberately left unset: terminal graphics support is
		// only known once the startup probe has been answered, up to three
		// seconds later (see emojiProbeCmd). media_rendered carries the protocol
		// at the point it actually matters.
		FeaturesOn:   enabledFeatures(cfg),
		MouseEnabled: mouseEnabled,
		NavModifier:  navModifierFromConfig(cfg),
		Overridden:   overriddenActions(cfg),
		// Not loaded yet at first layout, and a zero would read as "this user
		// has no teams" rather than "we hadn't asked yet".
		Teams:    -1,
		Channels: -1,
	}
}

// recordLaunch emits app_started, once, from the first WindowSizeMsg — the
// earliest point the terminal size is known. Consuming launchEnv is what makes
// it once: every later resize finds it nil.
func (m *Model) recordLaunch() {
	if m.launchEnv == nil {
		return
	}
	env := *m.launchEnv
	m.launchEnv = nil

	env.Cols = m.width
	env.Rows = m.height
	env.StartupMillis = time.Since(processStart).Milliseconds()
	// Anything already on screen came out of the local cache rather than the
	// network — the warm-open path this measures.
	env.CacheWarm = len(m.posts) > 0
	telemetry.AppStarted(env)
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
