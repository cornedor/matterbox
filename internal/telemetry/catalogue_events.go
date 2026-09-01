package telemetry

// This file is the list of everything matterbox can report. It is long on
// purpose: the alternative to a long explicit list is a short permissive one,
// and a permissive telemetry layer is how message text ends up in an analytics
// product by accident.
//
// Adding an event means adding it here first. Capture will not send an event
// this file doesn't declare, `docs/telemetry.md` is generated from it, and
// TestCatalogueCoversCallSites fails if a call site emits something undeclared.

// ---------------------------------------------------------------------------
// Shared value sets
// ---------------------------------------------------------------------------

// Contexts are the layers of handleKey's precedence ladder — the modals, modes
// and panes that can own a keystroke — named exactly as the keyContexts table
// in internal/ui/contexts.go names them.
//
// Borrowing that table's own labels rather than maintaining a parallel list is
// the point: keyContexts is the app's tested model of which bindings are live
// where, so telemetry built on it describes the routing the app actually
// implements instead of a second, drifting description of it.
// TestContextNamesAreCatalogued fails if a layer is added there and not here.
//
// This is the dimension that turns a count into a question. The same key
// pressed in focus:messages and in modal:reaction-picker are two different
// events about two different problems, and an action that only ever fires from
// global:reading is telling us something about where people live in the UI.
var Contexts = []string{
	"modal:key-debug", "modal:game", "modal:delete-confirm",
	"modal:reaction-picker", "modal:jira-picker", "modal:jira-points",
	"modal:jira-comment", "modal:confirm", "modal:open-picker",
	"modal:code-picker", "modal:poll-dialog", "modal:channel-form",
	"modal:saved-posts", "modal:template-picker", "modal:kaomoji-picker",
	"modal:history", "modal:summary", "modal:keys-sheet",
	"modal:text-popup", "modal:image-preview", "modal:stl-view",
	"modal:switcher",
	"global:switcher-chord", "global:command-picker", "global:team-jump",
	"global:nav", "mode:filter", "focus:input",
	"focus:search", "focus:sql", "global:reading",
	"focus:messages", "focus:thread", "focus:ref",
	"focus:info-media", "focus:info", "focus:sqlresults",
	"focus:attachments", "focus:teams", "focus:feed",
	// Not keyContexts rows. The setup wizard is a separate bubbletea program with
	// its own key handling, and "unknown" covers a keypress arriving before any
	// layer is live.
	"welcome", "unknown",
}

// ChannelTypes are Mattermost's four channel kinds, mapped to readable names.
// The *type* of conversation is safe and highly informative — it separates
// "people live in DMs" from "people live in big public channels", which
// changes what the sidebar and the feed should optimise for. The channel's
// name, id and members are never sent.
var ChannelTypes = []string{"public", "private", "dm", "group_dm", "unknown"}

// OpenVias are the routes to opening a conversation. This is the single most
// useful UX dimension in the app: it says whether people navigate by sidebar,
// by fuzzy switcher, by keyboard jump, or from outside the app entirely — and
// a route nobody takes is a feature to fix or remove.
var OpenVias = []string{
	"sidebar_key", "sidebar_mouse", "switcher", "filter", "nav_key",
	"team_jump", "dm_jump", "feed", "search_hit", "permalink", "cli",
	"notification", "unread_jump", "palette", "restore", "unknown",
}

// Outcomes are the coarse result of an operation that can fail. Deliberately
// few: the point is to spot "this feature errors for a third of the people who
// try it", not to build an error taxonomy.
var Outcomes = []string{"ok", "empty", "cancelled", "denied", "timeout", "unavailable", "error"}

// ErrorClasses group failures by what a user would have to do about them,
// which is the only grouping that helps decide what to fix.
var ErrorClasses = []string{
	"network", "auth", "permission", "not_found", "rate_limited",
	"server", "config", "disk", "parse", "unsupported", "internal", "unknown",
}

// SetupSteps are the setup wizard's screens, in order. Shared by setup_step and
// setup_finished so the funnel's stages and its endpoint can't drift apart.
var SetupSteps = []string{"server", "login", "advanced", "telemetry"}

// ImageProtocols are the terminal graphics protocols matterbox can use. Which
// one is available decides whether image previews, emoji images and video are
// usable at all, so it is the first thing to check when a media feature looks
// unused: unused because unwanted, or unused because unsupported?
var ImageProtocols = []string{"kitty", "sixel", "iterm", "none"}

// ---------------------------------------------------------------------------
// Whitelists for the counter maps in usage_snapshot
// ---------------------------------------------------------------------------

// ActionIDs is every rebindable action in the keymap — the ids from the
// actionDefs registry in internal/ui/keys.go. The snapshot reports how many
// times each one fired, which answers the two questions a keymap this large
// raises: which bindings are dead weight, and which are so heavily used that
// they deserve a better key.
//
// TestActionIDsMatchKeymap in internal/ui keeps this in step with the
// registry, so a new action cannot be added without appearing here (and
// therefore in the published docs).
var ActionIDs = []string{
	"apply_open", "attachment_remove", "bottom", "cancel_edit", "channel_info",
	"channel_next", "channel_prev", "clear_filter", "clear_input",
	"close_thread", "collapse_message", "command_picker", "compose",
	"confirm_no", "confirm_yes", "copy_code_block", "copy_markdown",
	"copy_selection", "cut_selection", "delete_post", "down", "download_attachment", "edit_history", "edit_post",
	"feed_mark_all_read", "feed_reply", "feed_toggle_muted", "filter",
	"focus_next", "focus_prev", "goto_dm", "goto_feed", "goto_parent",
	"goto_team", "help", "input_down", "input_up", "jira_assignee",
	"jira_comment", "jira_points", "jira_priority", "jira_reply", "jira_status",
	"leave_input", "left", "load_team", "mark_read", "move_team_left",
	"move_team_right", "newline", "next_match", "open_attachment",
	"open_channel", "open_reference", "open_thread", "page_down", "page_up",
	"paste", "preview_image", "prev_match", "prev_own_message", "quit", "react",
	"redo", "ref_approve", "ref_jobs", "ref_merge", "refresh",
	"reply_in_thread", "right", "search_all", "search_here",
	"select_down", "select_left", "select_right", "select_up", "send",
	"sheet_remove", "switcher", "team_next", "team_prev", "top", "undo", "up",
}

// MouseTargets are the clickable regions of the UI (the hitZone enum in
// internal/ui/mouse.go). Counted next to the keyboard actions because the
// comparison is the interesting part: a pane people only ever reach by
// clicking has a discoverability problem in its keybinding, and one nobody
// clicks may not need to be clickable.
var MouseTargets = []string{
	"tab", "channel", "message", "thread", "feed", "search", "reference",
	"info", "sql", "composer", "jump_bottom", "feed_mark_all", "feed_blobs", "toast",
	"nothing",
}

// PaletteIDs are the ">" command-palette entries, by stable id rather than by
// display name — several of those names interpolate a channel name ("Mute
// #incidents"), which must never be sent.
var PaletteIDs = []string{
	"summarize", "create_channel", "join_channel", "start_group_dm", "keys",
	"saved_messages", "message_stats", "status_online", "status_away",
	"status_dnd", "status_offline", "status_custom_set", "status_custom_clear",
	"image_click", "index_channel", "typing_animation", "gorillas", "gorillas_hotseat",
	"kurve", "kurve_hotseat", "rejoin_game", "debug_key_inspector",
	"copy_message_link", "copy_channel_link",
	"debug_copy_message_id", "debug_copy_channel_id", "channel_mute",
	"channel_unmute", "feed_hide_muted", "feed_show_muted", "feed_mark_all_read",
	"sidebar_all_channels", "sidebar_unread_channels",
	"mark_unread_post",
}

// SlashIDs are the built-in "/" commands, ours and therefore safe to name.
// Server-side and plugin commands are reported as "server" without their trigger
// word: those names come from the Mattermost instance and can name an
// organisation's internal tooling, which makes them identifying in a way our own
// command names are not.
var SlashIDs = []string{
	"me", "shrug", "kaomoji", "tmpl", "dm", "search", "help", "copy",
	// The text-effect commands, which the registry generates from the effects
	// table. Counted individually because that is the only way to learn which
	// of the effects were worth building — they are matterbox-only, so nobody
	// else's usage data can answer it.
	"shimmer", "rainbow", "scroll", "pulse", "glow", "warn", "ok", "bad",
	"whisper", "underline", "spoiler",
	// Anything the Mattermost instance or a plugin provides.
	"server", "unknown",
}

// FeatureIDs names the features whose use is worth counting on its own —
// the ones that cost real effort to build and whose adoption nobody currently
// knows. This is the list that answers "what did we build that nobody uses".
var FeatureIDs = []string{
	"ai_search", "semantic_search", "summary", "embed_index", "sql_tab",
	"grammar_check", "text_effects", "emoji_images", "image_preview",
	"video_preview", "attachments_paste", "attachments_drop", "download",
	"drafts", "templates", "saved_messages", "kaomoji", "polls", "reactions",
	"nested_reply", "collapse", "code_copy", "markdown_table", "permalink",
	"jira", "gitlab", "github", "games", "custom_status", "channel_create",
	"channel_edit", "channel_join", "group_dm", "message_stats", "cheatsheet",
	"key_inspector", "mouse_selection", "feed", "digest", "rules",
}

// FrictionIDs names the counted friction signals — the moments that suggest
// the UI was not understood rather than that a feature was used. Together they
// are the "what is used incorrectly" half of the brief, and each one is a
// specific, checkable hypothesis rather than a vague dissatisfaction metric.
var FrictionIDs = []string{
	// A key was pressed that no binding in this surface answers to. The
	// strongest single signal there is: it is someone's mental model of the
	// keymap disagreeing with the keymap.
	"unhandled_key",
	// The same unhandled key, but one that *is* bound in a different pane —
	// so the person knows the key, they just expected it to work here too.
	"unhandled_key_bound_elsewhere",
	// Help or the cheatsheet opened within a few seconds of an unhandled key:
	// someone who got stuck and went looking.
	"help_after_unhandled",
	// Three or more escapes in a row: trying to get out of something.
	"esc_cascade",
	// The same action fired many times in a very short window — mashing, which
	// usually means the app gave no feedback that the first press worked.
	"action_repeated",
	// A picker or modal opened and closed without committing to anything.
	"picker_abandoned",
	// Text was typed into the composer and then discarded without sending.
	"composer_discarded",
	// A delete confirmation was answered "no" — the destructive key is too
	// easy to hit, or its target was not obvious.
	"delete_cancelled",
	// Undo pressed straight after an edit landed.
	"undo_after_edit",
	// A send failed.
	"send_failed",
	// Scrolled to the top of loaded history and had to wait for a fetch.
	"scroll_wall",
	// A frame took long enough to be visible as lag.
	"slow_frame",
	// A resize storm (a tiling window manager or a drag) — the debounce
	// working or not working in the field.
	"resize_storm",
	// A search returned nothing.
	"search_empty",
	// A search's results were never opened.
	"search_abandoned",
}

// ---------------------------------------------------------------------------
// The events
// ---------------------------------------------------------------------------

// Events is the catalogue. Order is presentational — it groups the events the
// way the generated documentation reads best (lifecycle, then the core loop,
// then features, then friction, then reliability).
var Events = []EventSpec{
	// -- lifecycle ---------------------------------------------------------
	{
		Name:    "app_started",
		Emitter: "AppStarted",
		Desc:    "The TUI launched and finished its first layout.",
		Why: "Establishes the denominator for every other number, and describes the " +
			"environment matterbox actually runs in: terminal, graphics support, window " +
			"size, and which optional features are configured on. Most \"this feature is " +
			"unused\" findings turn out to be \"this feature is unavailable\", and this is " +
			"the event that tells the two apart.",
		Props: []PropSpec{
			{Name: "version", Kind: KindVersion, Desc: "matterbox version / git describe output of the build."},
			{Name: "build_tags", Kind: KindVersion, Desc: "Optional build tags compiled in (e.g. `video`), comma separated."},
			{Name: "go_version", Kind: KindVersion, Desc: "Go toolchain the binary was built with."},
			{Name: "os", Kind: KindEnum, Values: []string{"linux", "darwin", "windows", "freebsd", "openbsd", "netbsd", "other"}, Desc: "Operating system."},
			{Name: "arch", Kind: KindEnum, Values: []string{"amd64", "arm64", "arm", "386", "other"}, Desc: "CPU architecture."},
			{Name: "terminal", Kind: KindEnum, Values: []string{
				"kitty", "ghostty", "wezterm", "alacritty", "foot", "iterm2", "apple_terminal",
				"vscode", "windows_terminal", "konsole", "gnome_terminal", "xterm", "rxvt",
				"tmux", "screen", "zellij", "other", "unknown",
			}, Desc: "Terminal emulator, recognised from a fixed list of $TERM_PROGRAM / $TERM values. An unrecognised terminal reports `other`, never the raw variable."},
			{Name: "image_protocol", Kind: KindEnum, Values: ImageProtocols, Desc: "Terminal graphics protocol available for image, emoji and video rendering."},
			{Name: "cols", Kind: KindEnum, Values: ColsBuckets, Desc: "Terminal width, bucketed. Decides whether the three-pane layout fits."},
			{Name: "rows", Kind: KindEnum, Values: RowsBuckets, Desc: "Terminal height, bucketed."},
			{Name: "features_on", Kind: KindEnumSet, Values: FeatureIDs, Desc: "Which optional features are enabled in config — adoption of a feature can only be read against how many people have it switched on."},
			{Name: "mouse_enabled", Kind: KindBool, Desc: "Whether mouse support is on."},
			{Name: "nav_modifier", Kind: KindEnum, Values: []string{"ctrl", "alt", "shift", "super", "meta", "hyper", "none"}, Desc: "Configured modifier for arrow-key sidebar navigation."},
			{Name: "overridden_actions", Kind: KindEnumSet, Values: ActionIDs, Desc: "Which actions have a custom keybinding. A default people keep rebinding is a default that is wrong."},
			{Name: "teams", Kind: KindEnum, Values: CountBuckets, Desc: "Number of teams the account is in, bucketed."},
			{Name: "channels", Kind: KindEnum, Values: CountBuckets, Desc: "Number of channels in the sidebar, bucketed. Sidebar and switcher design depends on this and we are guessing at it today."},
			{Name: "first_run", Kind: KindBool, Desc: "Whether this launch immediately followed the setup wizard."},
			{Name: "startup_ms", Kind: KindEnum, Values: MillisBuckets, Desc: "Time from process start to the first rendered frame, bucketed."},
			{Name: "cache_warm", Kind: KindBool, Desc: "Whether the local message cache had content to render before the network answered."},
		},
	},
	{
		Name:    "app_stopped",
		Emitter: "AppStopped",
		Desc:    "The TUI exited. Sent with a final usage_snapshot on the way out.",
		Why: "Session length and what a session contained is the retention picture: " +
			"whether people keep matterbox open all day (in which case background " +
			"behaviour and notifications matter most) or dip in and out (in which case " +
			"startup time and the unread feed matter most).",
		Props: []PropSpec{
			{Name: "session", Kind: KindEnum, Values: SecondsBuckets, Desc: "How long the session lasted, bucketed."},
			{Name: "reason", Kind: KindEnum, Values: []string{"quit", "signal", "error", "unknown"}, Desc: "How the session ended."},
			{Name: "actions", Kind: KindEnum, Values: CountBuckets, Desc: "Total keyboard actions in the session, bucketed."},
			{Name: "messages_sent", Kind: KindEnum, Values: CountBuckets, Desc: "Messages sent in the session, bucketed."},
			{Name: "channels_opened", Kind: KindEnum, Values: CountBuckets, Desc: "Distinct conversations opened, bucketed."},
		},
	},
	{
		Name:    "usage_snapshot",
		Emitter: "Flush",
		Desc: "A periodic tally of everything counted rather than reported individually: " +
			"keyboard actions, mouse targets, palette and slash commands, feature use, and " +
			"friction signals. Flushed every few minutes and once more at exit.",
		Why: "Answers \"what is used and what is dead\" across the entire surface — all " +
			"79 keybindings, every clickable region, every command — without sending an " +
			"event per keystroke. A key nobody presses shows up as a permanent absence " +
			"from these maps, which is exactly the finding that lets a keymap this large " +
			"be pruned. The trade is deliberate: counts are cheap and complete, but they " +
			"carry no ordering, so anything where sequence matters is a discrete event " +
			"above instead.",
		Props: []PropSpec{
			{Name: "window", Kind: KindEnum, Values: SecondsBuckets, Desc: "How long this tally covers, bucketed."},
			{Name: "final", Kind: KindBool, Desc: "Whether this is the last snapshot of a session (sent alongside app_stopped)."},
			{Name: "actions", Kind: KindCounterMap, Values: ActionIDs, Desc: "Keyboard action id → how many times it fired."},
			{Name: "actions_used", Kind: KindEnumSet, Values: ActionIDs, Desc: "The same action ids as a flat list, so PostHog can break down \"was this ever used\" without a HogQL query over the map."},
			{Name: "mouse", Kind: KindCounterMap, Values: MouseTargets, Desc: "Click target → how many times it was clicked."},
			{Name: "palette", Kind: KindCounterMap, Values: PaletteIDs, Desc: "Command-palette entry id → how many times it ran."},
			{Name: "slash", Kind: KindCounterMap, Values: SlashIDs, Desc: "Built-in slash command → how many times it ran. Server and plugin commands count as `server`."},
			{Name: "features", Kind: KindCounterMap, Values: FeatureIDs, Desc: "Feature id → how many times it was used."},
			{Name: "friction", Kind: KindCounterMap, Values: FrictionIDs, Desc: "Friction signal → how many times it occurred. See the friction events below for what each one means."},
			{Name: "surfaces", Kind: KindCounterMap, Values: Contexts, Desc: "Pane → how many actions happened while it was focused, i.e. where the time goes."},
		},
	},
	{
		Name:    "version_upgraded",
		Emitter: "VersionUpgraded",
		// Call sites use CheckVersion, which owns the comparison against the
		// remembered version and the write-back; see EventSpec.Trigger.
		Trigger: "CheckVersion",
		Desc:    "The first launch of a build whose version differs from the last one seen.",
		Why: "Tells us whether people update at all, and how long a release takes to " +
			"reach them — without which \"this bug is fixed\" and \"nobody is running the " +
			"fix\" look identical in the data.",
		Props: []PropSpec{
			{Name: "from", Kind: KindVersion, Desc: "Previously recorded version."},
			{Name: "to", Kind: KindVersion, Desc: "Version now running."},
		},
	},

	// -- setup -------------------------------------------------------------
	{
		Name:    "setup_step",
		Emitter: "SetupStep",
		Desc:    "The setup wizard displayed a step.",
		Why: "The wizard is the whole first impression, and a fresh install that fails " +
			"here never becomes a user. Step-by-step events make it a funnel, so the step " +
			"people abandon is visible instead of inferred.",
		Props: []PropSpec{
			{Name: "step", Kind: KindEnum, Values: SetupSteps, Desc: "Which wizard step."},
			{Name: "attempt", Kind: KindCount, Desc: "How many times this step has been shown this run — a step shown three times is a step being fought with."},
		},
	},
	{
		Name:    "setup_finished",
		Emitter: "SetupFinished",
		Desc:    "The setup wizard completed or was abandoned.",
		Why: "Closes the activation funnel: what fraction of fresh installs reach a " +
			"working login, how long it takes, and where the rest stop.",
		Props: []PropSpec{
			{Name: "outcome", Kind: KindEnum, Values: []string{"completed", "abandoned"}, Desc: "Whether setup reached a working login."},
			{Name: "last_step", Kind: KindEnum, Values: SetupSteps, Desc: "The step it ended on. Always the telemetry question for a wizard that was answered — which is the only kind that can report anything at all — so this is here for a future step order rather than for today's."},
			{Name: "duration", Kind: KindEnum, Values: SecondsBuckets, Desc: "How long setup took, bucketed."},
			{Name: "auth_method", Kind: KindEnum, Values: []string{"password", "oauth", "token", "none"}, Desc: "How the login was obtained."},
			{Name: "telemetry_opt_in", Kind: KindBool, Desc: "The answer to the telemetry question. Recorded only when the answer was yes — a `no` sends nothing at all, so this property is always true and exists to make the opt-in rate legible next to install counts."},
		},
	},
	{
		Name:    "login_failed",
		Emitter: "LoginFailed",
		Desc:    "A login attempt was rejected.",
		Why: "Login failures are invisible to us today and are the most likely reason a " +
			"new install is abandoned. The class of failure separates \"our OAuth flow is " +
			"broken\" from \"they typed the wrong password\".",
		Props: []PropSpec{
			{Name: "method", Kind: KindEnum, Values: []string{"password", "oauth", "token"}, Desc: "Which login route."},
			{Name: "class", Kind: KindEnum, Values: ErrorClasses, Desc: "Failure class."},
			{Name: "mfa", Kind: KindBool, Desc: "Whether the server asked for MFA."},
		},
	},

	// -- the core loop -----------------------------------------------------
	{
		Name:    "channel_opened",
		Emitter: "ChannelOpened",
		Desc:    "A conversation was opened and rendered.",
		Why: "The central navigation question: how people get to a conversation, and " +
			"whether it appears fast. `via` is the payoff — it ranks the sidebar against " +
			"the switcher against the keyboard jumps against outside entry points, and a " +
			"route nobody uses is a feature to fix or delete. The timing properties are " +
			"the only field measurement of the warm-cache render path.",
		Props: []PropSpec{
			{Name: "via", Kind: KindEnum, Values: OpenVias, Desc: "How the conversation was reached."},
			{Name: "channel_type", Kind: KindEnum, Values: ChannelTypes, Desc: "Kind of conversation. Never its name or id."},
			{Name: "was_unread", Kind: KindBool, Desc: "Whether it had unread messages."},
			{Name: "cache", Kind: KindEnum, Values: []string{"warm", "cold", "partial"}, Desc: "Whether the local cache could render it before the network answered."},
			{Name: "render_ms", Kind: KindEnum, Values: MillisBuckets, Desc: "Time to the first rendered frame of the conversation, bucketed."},
			{Name: "posts", Kind: KindEnum, Values: CountBuckets, Desc: "Posts rendered, bucketed."},
		},
	},
	{
		Name:    "message_sent",
		Emitter: "MessageSent",
		Desc:    "A message was sent successfully.",
		Why: "The core action of the product, and the properties describe *how* people " +
			"compose rather than what they write: whether replies are threaded, whether " +
			"attachments and code blocks are common, how long messages are. That decides " +
			"where composer effort belongs. No part of the text is sent — only its shape.",
		Props: []PropSpec{
			{Name: "surface", Kind: KindEnum, Values: []string{"composer", "thread", "feed_reply", "cli", "control_socket"}, Desc: "Where it was composed."},
			{Name: "is_reply", Kind: KindBool, Desc: "Whether it is a thread reply."},
			{Name: "is_nested_reply", Kind: KindBool, Desc: "Whether it carries a matterbox nested-reply parent."},
			{Name: "length", Kind: KindEnum, Values: LengthBuckets, Desc: "Message length in characters, bucketed. The text itself is never sent."},
			{Name: "lines", Kind: KindEnum, Values: CountBuckets, Desc: "Number of lines, bucketed."},
			{Name: "attachments", Kind: KindEnum, Values: CountBuckets, Desc: "Attachment count, bucketed."},
			{Name: "mentions", Kind: KindEnum, Values: CountBuckets, Desc: "Count of @mentions, bucketed. The names are not sent."},
			{Name: "has_code_block", Kind: KindBool, Desc: "Whether it contains a fenced code block."},
			{Name: "has_link", Kind: KindBool, Desc: "Whether it contains a URL. The URL is not sent."},
			{Name: "has_emoji", Kind: KindBool, Desc: "Whether it contains an emoji shortcode."},
			{Name: "has_effect", Kind: KindBool, Desc: "Whether it carries a matterbox text effect."},
			{Name: "send_ms", Kind: KindEnum, Values: MillisBuckets, Desc: "Round trip to the server, bucketed."},
		},
	},
	{
		Name:    "message_acted",
		Emitter: "MessageActed",
		Desc:    "A message was edited, deleted, reacted to, copied, collapsed or saved.",
		Why: "These are the actions the transcript exists to support, and several of them " +
			"(edit history, collapse, code copy) were expensive to build with no evidence " +
			"anyone uses them. `age` matters for edit and delete: editing something from " +
			"three days ago is a different feature from fixing a typo ten seconds later.",
		Props: []PropSpec{
			{Name: "action", Kind: KindEnum, Values: []string{
				"edit", "delete", "react", "unreact", "copy_markdown", "copy_code",
				"collapse", "expand", "save", "unsave", "history", "permalink", "pin",
			}, Desc: "What was done."},
			{Name: "own", Kind: KindBool, Desc: "Whether it was the user's own message."},
			{Name: "age", Kind: KindEnum, Values: SecondsBuckets, Desc: "How old the message was, bucketed."},
			{Name: "outcome", Kind: KindEnum, Values: Outcomes, Desc: "Result."},
			{Name: "reaction_slot", Kind: KindEnum, Values: []string{"quickbar_1", "quickbar_2", "quickbar_3", "quickbar_4", "quickbar_5", "quickbar_other", "searched", "recent", "custom"}, Desc: "For a reaction: which slot of the configured quick-reaction bar it came from, or that it was searched for. The emoji itself is not sent — the slot is what tells us whether the default bar holds the right five."},
			{Name: "via", Kind: KindEnum, Values: []string{"key", "mouse", "palette", "picker", "cli"}, Desc: "How the action was invoked."},
		},
	},
	{
		Name:    "thread_opened",
		Emitter: "ThreadOpened",
		Desc:    "A thread pane was opened on a message.",
		Why: "Threading is the feature most likely to be either central or ignored, and " +
			"we do not know which. Reply depth and the nested-reply flag also say whether " +
			"the matterbox-only nested reply tree is worth its complexity.",
		Props: []PropSpec{
			{Name: "via", Kind: KindEnum, Values: []string{"key", "mouse", "feed", "permalink", "reply", "unknown"}, Desc: "How the thread was opened."},
			{Name: "replies", Kind: KindEnum, Values: CountBuckets, Desc: "Replies in the thread, bucketed."},
			{Name: "nested", Kind: KindBool, Desc: "Whether the thread contains matterbox nested replies."},
			{Name: "depth", Kind: KindEnum, Values: CountBuckets, Desc: "Deepest nesting level, bucketed."},
		},
	},
	{
		Name:    "search_run",
		Emitter: "SearchRun",
		Desc:    "A search was executed.",
		Why: "There are four search backends (server FTS, local FTS, semantic, and the " +
			"agentic AI search) and no evidence about which earns its keep. Result count " +
			"and latency per mode say which one finds things and which one is slow; the " +
			"query itself is never sent, only how many words it had.",
		Props: []PropSpec{
			{Name: "mode", Kind: KindEnum, Values: []string{"server", "local_fts", "semantic", "hybrid", "ai"}, Desc: "Which backend ran."},
			{Name: "scope", Kind: KindEnum, Values: []string{"channel", "all", "feed"}, Desc: "Search scope."},
			{Name: "from", Kind: KindEnum, Values: []string{"key", "palette", "slash", "cli", "unknown"}, Desc: "How it was started."},
			{Name: "terms", Kind: KindEnum, Values: CountBuckets, Desc: "Number of words in the query, bucketed. The query text is never sent."},
			{Name: "had_operators", Kind: KindBool, Desc: "Whether the query used search operators (from:, in:, quotes)."},
			{Name: "results", Kind: KindEnum, Values: CountBuckets, Desc: "Hits returned, bucketed."},
			{Name: "latency_ms", Kind: KindEnum, Values: MillisBuckets, Desc: "Time to results, bucketed."},
			{Name: "outcome", Kind: KindEnum, Values: Outcomes, Desc: "Result."},
		},
	},
	{
		Name:    "search_result_opened",
		Emitter: "SearchResultOpened",
		Desc:    "A search hit was opened.",
		Why: "The other half of search quality: a search that returns fifty results " +
			"nobody opens has failed, and rank says whether the ranking is any good. " +
			"Together with search_run this is a funnel — searched, opened, or gave up.",
		Props: []PropSpec{
			{Name: "mode", Kind: KindEnum, Values: []string{"server", "local_fts", "semantic", "hybrid", "ai"}, Desc: "Which backend produced the hit."},
			{Name: "rank", Kind: KindEnum, Values: RankBuckets, Desc: "1-based position of the opened hit, bucketed. Says whether the top result is the right one."},
			{Name: "dwell", Kind: KindEnum, Values: SecondsBuckets, Desc: "Time between results appearing and the hit being opened, bucketed."},
		},
	},
	{
		Name:    "feed_used",
		Emitter: "FeedUsed",
		Desc:    "An action was taken in the unread feed.",
		Why: "The feed is matterbox's own idea rather than a Mattermost concept, so " +
			"whether it is the main way people triage — or an unused tab — is worth " +
			"knowing before more is built on it.",
		Props: []PropSpec{
			{Name: "action", Kind: KindEnum, Values: []string{"opened", "mark_read", "mark_all_read", "reply", "open_channel", "toggle_muted", "refresh"}, Desc: "What was done."},
			{Name: "items", Kind: KindEnum, Values: CountBuckets, Desc: "Unread items in the feed at the time, bucketed."},
			{Name: "via", Kind: KindEnum, Values: []string{"key", "mouse", "palette", "unknown"}, Desc: "How it was invoked."},
		},
	},

	// -- features ----------------------------------------------------------
	{
		Name:    "feature_used",
		Emitter: "FeatureUsed",
		Desc: "A named feature was used, with the outcome and how long it took where that " +
			"applies. The counted equivalent lives in usage_snapshot's `features` map; this " +
			"event exists for the features whose *outcome* matters, not just their count.",
		Why: "Direct answer to \"what did we build that nobody uses, and what fails for " +
			"the people who do try it\". Adoption alone can be read from the snapshot; a " +
			"feature that is used but errors half the time needs the outcome dimension to " +
			"be visible at all.",
		Props: []PropSpec{
			{Name: "feature", Kind: KindEnum, Values: FeatureIDs, Desc: "Which feature."},
			{Name: "outcome", Kind: KindEnum, Values: Outcomes, Desc: "Result."},
			{Name: "latency_ms", Kind: KindEnum, Values: MillisBuckets, Desc: "How long it took, bucketed, where the feature is slow enough to matter."},
			{Name: "size", Kind: KindEnum, Values: CountBuckets, Desc: "A feature-specific magnitude, bucketed: posts summarised, rows returned, suggestions offered, messages indexed."},
			{Name: "via", Kind: KindEnum, Values: []string{"key", "mouse", "palette", "slash", "cli", "auto", "unknown"}, Desc: "How it was invoked. Separates \"nobody wants this\" from \"nobody can find this\"."},
			{Name: "error_class", Kind: KindEnum, Values: ErrorClasses, Desc: "Failure class when the outcome is an error."},
		},
	},
	{
		Name:    "forge_action",
		Emitter: "ForgeAction",
		Desc:    "A Jira, GitLab or GitHub action was performed from the reference panel.",
		Why: "The forge integrations are the largest optional subsystem in the app. " +
			"Whether anyone changes a Jira status or approves a merge request from " +
			"matterbox — rather than just reading the panel — decides whether the write " +
			"paths were worth building and which ones to extend.",
		Props: []PropSpec{
			{Name: "provider", Kind: KindEnum, Values: []string{"jira", "gitlab", "github"}, Desc: "Which provider. Never the instance URL, project or issue key."},
			{Name: "action", Kind: KindEnum, Values: []string{
				"open", "status", "priority", "points", "assignee", "comment",
				"reply", "approve", "merge", "jobs", "refresh",
			}, Desc: "What was done."},
			{Name: "outcome", Kind: KindEnum, Values: Outcomes, Desc: "Result."},
			{Name: "latency_ms", Kind: KindEnum, Values: MillisBuckets, Desc: "API round trip, bucketed."},
			{Name: "error_class", Kind: KindEnum, Values: ErrorClasses, Desc: "Failure class when it failed."},
		},
	},
	{
		Name:    "attachment_added",
		Emitter: "AttachmentAdded",
		Desc:    "A file was attached to a message.",
		Why: "Three separate paths exist (paste, drag-and-drop, picker) and we do not " +
			"know whether any of them is discoverable. Size and kind say whether the " +
			"upload path needs progress feedback.",
		Props: []PropSpec{
			{Name: "via", Kind: KindEnum, Values: []string{"paste", "drop", "picker", "cli"}, Desc: "How the file was attached."},
			{Name: "kind", Kind: KindEnum, Values: []string{"image", "video", "audio", "pdf", "text", "archive", "other"}, Desc: "Coarse file kind, from the extension. The filename is never sent."},
			{Name: "size", Kind: KindEnum, Values: BytesBuckets, Desc: "File size, bucketed."},
			{Name: "count", Kind: KindEnum, Values: CountBuckets, Desc: "Attachments now pending, bucketed."},
			{Name: "outcome", Kind: KindEnum, Values: Outcomes, Desc: "Result."},
		},
	},
	{
		Name:    "media_rendered",
		Emitter: "MediaRendered",
		Desc:    "An image, animated emoji or video frame was drawn in the terminal.",
		Why: "Terminal graphics are the most fragile thing matterbox does and the most " +
			"likely to be silently broken on a given terminal. Pairing the protocol with " +
			"the outcome shows which terminals media actually works on rather than which " +
			"ones we hope it works on.",
		Props: []PropSpec{
			{Name: "kind", Kind: KindEnum, Values: []string{"image_preview", "inline_image", "emoji_image", "video", "thumbnail"}, Desc: "What was rendered."},
			{Name: "protocol", Kind: KindEnum, Values: ImageProtocols, Desc: "Graphics protocol used."},
			{Name: "outcome", Kind: KindEnum, Values: Outcomes, Desc: "Result."},
			{Name: "decode_ms", Kind: KindEnum, Values: MillisBuckets, Desc: "Decode plus transmit time, bucketed."},
			{Name: "error_class", Kind: KindEnum, Values: ErrorClasses, Desc: "Failure class when it failed."},
		},
	},

	// -- friction ----------------------------------------------------------
	{
		Name:    "unhandled_key",
		Emitter: "UnhandledKey",
		Desc:    "A keypress in a reading pane matched no binding and was not text input.",
		Why: "The best available proxy for a broken mental model: the person expected " +
			"that key to do something here and it did nothing. Aggregated by key and " +
			"surface it names the exact bindings people expect but do not have — which is " +
			"a far better source of keymap changes than our own intuition. `bound_elsewhere` " +
			"separates \"invented a key\" from \"knows the key, wrong pane\", and the latter " +
			"is usually a bug in where we scoped the binding.",
		Props: []PropSpec{
			{Name: "key", Kind: KindEnum, Values: ReportableKeys, Desc: "The keystroke, from a fixed list of non-text keys (modified keys, function keys, navigation). Plain typed characters are never reported, so nothing here can spell out content."},
			{Name: "surface", Kind: KindEnum, Values: Contexts, Desc: "Which pane had focus."},
			{Name: "bound_elsewhere", Kind: KindBool, Desc: "Whether that key is bound to something in a different pane."},
		},
	},
	{
		Name:    "friction",
		Emitter: "FrictionEvent",
		Desc: "One of the counted friction signals crossed the threshold that makes it " +
			"worth reporting on its own: a help lookup after a dead key, an escape " +
			"cascade, a mashed action, an abandoned picker, a discarded draft.",
		Why: "Turns the friction counters into something with context. The counter says " +
			"how often people get stuck; this says where, and in what, so the fix has an " +
			"address. This is the \"what needs better UX\" half of the brief.",
		Props: []PropSpec{
			{Name: "signal", Kind: KindEnum, Values: FrictionIDs, Desc: "Which signal."},
			{Name: "surface", Kind: KindEnum, Values: Contexts, Desc: "Where it happened."},
			{Name: "action", Kind: KindEnum, Values: ActionIDs, Desc: "Which action was involved, for the action-specific signals."},
			{Name: "count", Kind: KindCount, Desc: "How many repetitions triggered it (escapes in the cascade, presses in the mash)."},
			{Name: "dwell", Kind: KindEnum, Values: SecondsBuckets, Desc: "How long the person spent before giving up, bucketed."},
			{Name: "size", Kind: KindEnum, Values: LengthBuckets, Desc: "For a discarded draft, how much was typed before it was thrown away. The text is never sent."},
		},
	},
	{
		Name:    "slow_frame",
		Emitter: "SlowFrame",
		Desc:    "A render took long enough to be perceptible.",
		Why: "Render cost is the recurring performance problem in this codebase and it " +
			"has only ever been measured locally, on one machine, against one cache. This " +
			"is the field version: which pane, at which terminal size, with how much " +
			"history loaded. It is rate-limited so a slow session cannot flood.",
		Props: []PropSpec{
			{Name: "surface", Kind: KindEnum, Values: Contexts, Desc: "Focused pane at the time."},
			{Name: "ms", Kind: KindEnum, Values: MillisBuckets, Desc: "Frame time, bucketed."},
			{Name: "posts", Kind: KindEnum, Values: CountBuckets, Desc: "Posts loaded in the transcript, bucketed."},
			{Name: "cols", Kind: KindEnum, Values: ColsBuckets, Desc: "Terminal width, bucketed."},
			{Name: "cause", Kind: KindEnum, Values: []string{"render", "resize", "image", "animation", "unknown"}, Desc: "What the frame was doing."},
		},
	},

	// -- reliability -------------------------------------------------------
	{
		Name:    "ws_disconnected",
		Emitter: "WSDisconnected",
		Desc:    "The Mattermost websocket dropped.",
		Why: "Silent disconnects are the worst failure this client has: the UI looks " +
			"fine and messages stop arriving. Knowing how often it happens in the field, " +
			"and after how long a healthy connection, is the only way to tell a flaky " +
			"network from a bug in our reconnect logic. `cause` is what draws that line: " +
			"a ping timeout means the socket stopped producing without erroring, which is " +
			"a half-open link or a reader of ours that stalled — the latter is ours to fix, " +
			"and classifies identically to a plain network drop without this.",
		Props: []PropSpec{
			{Name: "class", Kind: KindEnum, Values: ErrorClasses, Desc: "Why it dropped."},
			{Name: "connected", Kind: KindEnum, Values: SecondsBuckets, Desc: "How long it had been connected, bucketed."},
			{Name: "clean", Kind: KindBool, Desc: "Whether it was a clean close."},
			{Name: "cause", Kind: KindEnum, Values: []string{"ping_timeout", "read_error", "closed"}, Desc: "How the socket ended, which the class can't tell apart."},
		},
	},
	{
		Name:    "ws_reconnected",
		Emitter: "WSReconnected",
		Desc:    "The websocket came back.",
		Why: "Completes the disconnect picture: how long people spend disconnected, how " +
			"many attempts it takes, and whether the catch-up resync works — a reconnect " +
			"that recovers the socket but not the missed messages is still a failure.",
		Props: []PropSpec{
			{Name: "attempts", Kind: KindEnum, Values: CountBuckets, Desc: "Reconnect attempts needed, bucketed."},
			{Name: "downtime", Kind: KindEnum, Values: SecondsBuckets, Desc: "Time disconnected, bucketed."},
			{Name: "resync", Kind: KindEnum, Values: []string{"none", "partial", "full", "failed"}, Desc: "Whether missed messages were recovered."},
		},
	},
	{
		Name:    "operation_failed",
		Emitter: "OperationFailed",
		Desc:    "An operation that the user was waiting on failed.",
		Why: "The general reliability signal, grouped by which subsystem broke rather " +
			"than by error string. `where` is a short label written by hand at the call " +
			"site; `detail` is scrubbed error text, kept because the shape of a failure is " +
			"often the only clue and dropped to a placeholder when nothing in it was safe.",
		Props: []PropSpec{
			{Name: "where", Kind: KindEnum, Values: FailureSites, Desc: "Which operation, from a fixed list of hand-written labels."},
			{Name: "class", Kind: KindEnum, Values: ErrorClasses, Desc: "Failure class."},
			{Name: "status", Kind: KindCount, Desc: "HTTP status code, for API failures. 0 when there wasn't one."},
			{Name: "retried", Kind: KindBool, Desc: "Whether it was retried."},
			{Name: "user_visible", Kind: KindBool, Desc: "Whether the user saw an error, as opposed to it being handled silently."},
			{Name: "detail", Kind: KindErrorText, Desc: "Scrubbed error text: paths, URLs, ids, quoted strings, mentions and tokens are replaced with placeholders before it leaves the machine. See the privacy section."},
		},
	},
	{
		Name:    "panic_recovered",
		Emitter: "PanicRecovered",
		// Call sites use Crash, which takes the value from recover() and the
		// stack while it is still standing; see EventSpec.Trigger.
		Trigger: "Crash",
		Desc:    "A panic was caught rather than taking the process down.",
		Why: "A crash a user works around is a crash we never hear about. The matterbox " +
			"stack frames are enough to find the bug, and they are code from a public " +
			"repository rather than anything about the person running it.",
		Props: []PropSpec{
			{Name: "where", Kind: KindEnum, Values: FailureSites, Desc: "Which subsystem recovered it."},
			{Name: "frames", Kind: KindFrames, Desc: "matterbox stack frames, innermost first. Only functions in the matterbox module are kept — no arguments, no file paths, no dependency or standard-library frames."},
			{Name: "detail", Kind: KindErrorText, Desc: "Scrubbed panic value."},
		},
	},

	// -- non-interactive surfaces -----------------------------------------
	{
		Name:    "cli_command",
		Emitter: "CLICommand",
		Desc:    "A `matterbox <verb>` subcommand ran to completion.",
		Why: "There are twenty-odd subcommands and no idea which are used. Some exist " +
			"only to be called from scripts and notification handlers, where nobody would " +
			"ever report a problem — so a verb that always fails could have been broken " +
			"for months. This is the cheapest high-value event in the catalogue.",
		Props: []PropSpec{
			{Name: "command", Kind: KindEnum, Values: CLICommands, Desc: "Which subcommand."},
			{Name: "outcome", Kind: KindEnum, Values: Outcomes, Desc: "Result."},
			{Name: "duration_ms", Kind: KindEnum, Values: MillisBuckets, Desc: "Wall-clock duration, bucketed."},
			{Name: "tty", Kind: KindBool, Desc: "Whether stdout was a terminal — i.e. a person ran it, rather than a script."},
			{Name: "error_class", Kind: KindEnum, Values: ErrorClasses, Desc: "Failure class when it failed."},
		},
	},
	{
		Name:    "daemon_started",
		Emitter: "DaemonStarted",
		Desc:    "The `matterbox listen` daemon started.",
		Why: "The daemon runs unattended for weeks; its configuration is invisible to us " +
			"and its rules engine is the most complex config surface in the product. How " +
			"many rules people write, and which delivery channels they wire up, decides " +
			"where that subsystem goes next.",
		Daemon: true,
		Props: []PropSpec{
			{Name: "version", Kind: KindVersion, Desc: "Build running."},
			{Name: "rules", Kind: KindCount, Desc: "Number of configured rules."},
			{Name: "channels_on", Kind: KindEnumSet, Values: []string{"desktop", "telegram", "two_way", "digest", "exec", "summarize"}, Desc: "Which delivery and rule capabilities are configured."},
		},
	},
	{
		Name:    "rule_fired",
		Emitter: "RuleFired",
		Desc:    "A listen rule matched and its actions ran.",
		Why: "Says which rule *kinds* are worth their complexity — and, through the " +
			"outcome, whether the exec and notify actions people rely on are actually " +
			"succeeding on an unattended machine. Rule names are user-written and are " +
			"never sent; only the action types are.",
		Daemon: true,
		Props: []PropSpec{
			{Name: "action", Kind: KindEnum, Values: []string{"notify", "telegram", "exec", "reply", "react", "mark_read", "digest", "summarize", "other"}, Desc: "Action type that ran. Never the rule's name or its command line."},
			{Name: "outcome", Kind: KindEnum, Values: Outcomes, Desc: "Result."},
			{Name: "trigger", Kind: KindEnum, Values: []string{"message", "mention", "dm", "reaction", "schedule", "other"}, Desc: "What triggered the rule."},
		},
	},
	{
		Name:    "notification_actioned",
		Emitter: "NotificationActioned",
		Desc:    "A delivered notification was acted on.",
		Why: "The desktop notification buttons and inline reply took real work and sit " +
			"outside the app where nothing can be observed. This says whether anyone " +
			"presses them, and whether the action then succeeds.",
		Daemon: true,
		Props: []PropSpec{
			{Name: "channel", Kind: KindEnum, Values: []string{"desktop", "telegram"}, Desc: "Where the notification was delivered."},
			{Name: "action", Kind: KindEnum, Values: []string{"read", "react", "reply", "open", "dismissed", "expired"}, Desc: "What the user did with it."},
			{Name: "outcome", Kind: KindEnum, Values: Outcomes, Desc: "Result of carrying it out."},
		},
	},
}

// ReportableKeys is the closed set of keystrokes unhandled_key may report. It
// exists to make a text leak impossible by construction: every entry is a key
// that cannot be part of typed prose — modified keys, function keys, navigation
// and editing keys. A bare letter, digit or punctuation mark is not in the list
// and so is never sent, which means a stream of unhandled_key events can never
// reconstruct anything the user wrote.
//
// Bare letters are the tempting case and are deliberately excluded, shifted
// ones included: `z` doing nothing in the feed is a real finding, but reporting
// single characters would mean reporting the keystrokes of someone typing into
// a pane they mistook for the composer, and enough of those in a row is the
// text itself. So the invariant is that no single-character keystroke is ever
// reported (TestReportableKeysAreNotText), and an unhandled bare letter arrives
// as "other" — which still records that a key did nothing, and where, just not
// which one. Modified keys carry no such risk: nobody types prose in ctrl+.
var ReportableKeys = func() []string {
	keys := []string{
		"up", "down", "left", "right", "home", "end", "pgup", "pgdown",
		"tab", "shift+tab", "enter", "shift+enter", "alt+enter", "esc",
		"space", "backspace", "delete", "insert",
		"shift+up", "shift+down", "shift+left", "shift+right",
		"ctrl+up", "ctrl+down", "ctrl+left", "ctrl+right",
		"alt+up", "alt+down", "alt+left", "alt+right",
		"ctrl+home", "ctrl+end", "alt+backspace", "ctrl+backspace",
		"other",
	}
	// Function keys.
	for _, f := range []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12"} {
		keys = append(keys, f)
	}
	// Modified letters and digits only: ctrl+a…ctrl+z, alt+a…alt+z, alt+0…alt+9.
	for c := byte('a'); c <= 'z'; c++ {
		keys = append(keys, "ctrl+"+string(c), "alt+"+string(c), "ctrl+alt+"+string(c))
	}
	for c := byte('0'); c <= '9'; c++ {
		keys = append(keys, "alt+"+string(c), "ctrl+"+string(c))
	}
	return keys
}()

// FailureSites is the closed set of hand-written labels naming where something
// failed. A label is a constant in the source, never derived from data, which
// is what keeps operation_failed from becoming a channel for arbitrary strings.
var FailureSites = []string{
	"api.posts", "api.channels", "api.users", "api.teams", "api.files",
	"api.prefs", "api.search", "api.reactions", "api.status", "api.other",
	"ws.connect", "ws.auth", "ws.read", "ws.resync",
	"store.open", "store.migrate", "store.write", "store.query", "store.fts",
	"store.vector", "store.purge",
	"auth.login", "auth.token", "auth.oauth", "auth.refresh",
	"config.load", "config.save", "config.schema",
	"embed.server", "embed.index", "llm.request", "llm.tools",
	"jira.api", "gitlab.api", "github.api",
	"media.decode", "media.transmit", "media.download", "media.upload",
	"clipboard", "opener", "control_socket", "notify", "telegram",
	"render", "ui.other", "cli.other", "listen.rule", "listen.other",
}

// CLICommands is the subcommand list, matching the verbs registered in
// internal/cli. TestCLICommandsMatchRegistry keeps it in step.
//
// The root command — the TUI — is deliberately absent: it reports app_started
// and app_stopped, which say far more than an exit code, so Execute skips the
// cli_command event for it. A value here that nothing ever sends would be this
// page claiming a verb is tracked when it isn't.
var CLICommands = []string{
	"welcome", "login", "url-handler", "register-handler", "github", "send",
	"reply", "react", "read", "unread", "mark-read", "open", "search",
	"channels", "digest", "whoami", "embed", "listen", "rules", "keys",
	"decode", "upgrade",
}

// reportableKeySet indexes ReportableKeys for the lookup below.
var reportableKeySet = func() map[string]bool {
	m := make(map[string]bool, len(ReportableKeys))
	for _, k := range ReportableKeys {
		m[k] = true
	}
	return m
}()

// ReportableKey maps a keystroke to what unhandled_key may say about it: the
// keystroke itself when it is one of the non-text keys on the list, and "other"
// for everything else — which is every single-character key, and so every key
// that could be part of something typed.
//
// Callers pass the canonical bubbletea keystroke ("alt+up", "ctrl+w"), and get
// back a value that is always valid for the event's key property. Funnelling it
// through here rather than checking at the call site is what makes the "no
// single-character keystroke is ever reported" invariant hold everywhere at
// once.
func ReportableKey(keystroke string) string {
	if reportableKeySet[keystroke] {
		return keystroke
	}
	return "other"
}

// KnownCLICommand reports whether name is a catalogued subcommand. Call sites
// use it to stay silent about a verb the catalogue doesn't know, rather than
// sending an event whose command property would be dropped — which would look
// like a command with no name at all.
func KnownCLICommand(name string) bool { return inSet(CLICommands, name) }
