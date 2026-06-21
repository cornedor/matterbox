// Package config loads the matterbox user-facing configuration from
// ~/.config/matterbox/config.yaml. A missing or partial file is fine:
// defaults are filled in so first-run users don't need to write anything
// to get a working client.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the deserialised on-disk form. New fields should default to
// something sensible in fillDefaults so an empty/older config still
// produces a working app.
type Config struct {
	ServerURL string   `yaml:"server_url"`
	Reactions []string `yaml:"reactions"`
	// TeamOrder is the user's preferred left-to-right ordering of the team
	// tabs, by team URL name (case-insensitive; the team's display name is
	// also accepted). Teams not listed here are appended alphabetically.
	// Maintained in-app by reordering the team bar (< / >); see
	// internal/ui. Empty means "all alphabetical".
	TeamOrder []string `yaml:"team_order"`
	// Summary configures the "> Summarize" command (ctrl+k) which sends a
	// transcript to an OpenAI-compatible chat-completions endpoint and shows
	// the model's summary.
	Summary SummaryConfig `yaml:"summary"`
	// AISearch configures agentic search on the Search tab: a query ending
	// in "?" is handed to the model, which calls tools to find the relevant
	// messages. It reuses the Summary endpoint + model (same local server).
	AISearch AISearchConfig `yaml:"ai_search"`
	// Embeddings configures semantic search: a separate OpenAI-compatible
	// /v1/embeddings server (an embedding model, not the chat model) whose
	// vectors are stored alongside the FTS5 index and fused with keyword
	// ranking. Distinct from Summary because embeddings need their own model
	// loaded with --embeddings; see scripts/llama-embeddings.sh.
	Embeddings EmbeddingsConfig `yaml:"embeddings"`
	// Search tunes ranking for both the Search tab and AI search.
	Search SearchConfig `yaml:"search"`
	// MarkReadDelaySeconds is how long a channel must stay open before it is
	// marked read (on the server and in the sidebar/feed badges). A quick peek
	// shorter than this leaves the channel unread. Pointer so an absent key
	// defaults to defaultMarkReadDelaySeconds while an explicit 0 means "mark
	// read immediately" (the original behaviour). See internal/ui.
	MarkReadDelaySeconds *int `yaml:"mark_read_delay_seconds"`
	// GroupMessageSeconds collapses the author/timestamp header on a run of
	// consecutive messages from one person: a message sent within this many
	// seconds of the previous one (with nobody else posting in between) renders
	// as a bare continuation line under the first, instead of repeating the
	// name and time. Pointer so an absent key defaults to
	// defaultGroupMessageSeconds (120) while an explicit 0 disables grouping —
	// every message keeps its own header. See internal/ui.
	GroupMessageSeconds *int `yaml:"group_message_seconds"`
	// CustomStatus toggles showing DM partners' custom statuses (the emoji +
	// text a user sets, e.g. "🌴 On vacation"): the full text in the messages
	// header and a small hint glyph in the sidebar. Pointer so an absent key
	// defaults to true; an explicit false hides the custom-status glyph and
	// text everywhere, leaving only the presence dots. See internal/ui.
	CustomStatus *bool `yaml:"custom_status"`
	// Mouse enables mouse support: the wheel scrolls the message feed (and
	// inside a message taller than the pane), the open thread, and the Search /
	// Feed result lists; clicking a team tab or channel switches to it; clicking
	// a message selects it; clicking a Search hit / Feed bubble selects it and a
	// second click opens it; dragging within a message selects text that's
	// copied on release; and hovering a tab or channel highlights it. Pointer so
	// an absent key defaults to true; set `mouse: false` to keep the terminal's
	// native click-drag text selection / copy, which capturing the mouse
	// otherwise disables (most terminals fall back to shift-drag). See internal/ui.
	Mouse *bool `yaml:"mouse"`
	// DownloadDir is where the "download attachment" key (s on a message)
	// saves files. A leading "~" is expanded to the home directory and the
	// directory is created on first download. Empty defaults to ~/Downloads.
	// See internal/ui.
	DownloadDir string `yaml:"download_dir"`
	// SQLTab adds a synthetic "SQL" tab: a read-only SQL editor over the local
	// message cache that renders each result row as a chat message. Pointer so
	// an absent key defaults to false — the tab is hidden — while an explicit
	// `sql_tab: true` shows it. See internal/ui/sqltab.go.
	SQLTab *bool `yaml:"sql_tab"`
	// Keybindings holds optional keymap tweaks. See internal/ui.
	Keybindings KeybindingsConfig `yaml:"keybindings"`
	// EmojiImages controls whether custom (server) emoji render as real
	// inline images via the Kitty graphics protocol instead of literal
	// `:name:` text. "auto" (default) probes the terminal at startup and
	// enables images only on a Kitty/Ghostty-class terminal with a truecolor
	// profile (and not inside tmux); "off" keeps the literal-text behaviour
	// everywhere. Unicode emoji are unaffected — they always render as font
	// glyphs. See internal/ui.
	EmojiImages string `yaml:"emoji_images"`
	// Animations groups the optional motion effects so a user who finds
	// movement distracting can switch them off individually. An object rather
	// than a flat flag because more toggles are planned (e.g. animating GIFs in
	// the space-to-preview modal). See internal/ui.
	Animations AnimationsConfig `yaml:"animations"`
	// Giphy turns a pasted Giphy link into an inline ![alt](url) image in the
	// composer. The link is expanded instantly from its id (offline); when
	// api_key is set the line is then upgraded in place with the GIF's real
	// title and the chosen rendition. See internal/ui.
	Giphy GiphyConfig `yaml:"giphy"`
	// Telegram configures the outbound Telegram bridge used by the
	// `matterbox listen` daemon: a bot token (from @BotFather) and the chat id
	// that receives notifications. An empty bot_token disables delivery (the
	// daemon still keeps the local cache warm). See internal/listen.
	Telegram TelegramConfig `yaml:"telegram"`
	// Listen configures the `matterbox listen` background daemon: whether to
	// notify on direct mentions / DMs, whether to summarize the surrounding
	// context with the chat model, and the summary prompt. See internal/listen.
	Listen ListenConfig `yaml:"listen"`
	// Rules are per-message reactions evaluated by the `matterbox listen`
	// daemon: when a rule's conditions match an incoming post, its actions run
	// (notify, run a command, POST a webhook, react, mark read, log). When
	// empty the daemon falls back to the built-in notification behaviour
	// described by the listen.* options. See internal/listen/rules.go and
	// docs/rules.md.
	Rules []RuleConfig `yaml:"rules,omitempty"`
	// Jira configures the issue side panel: press the open-reference key (v) on
	// a message naming a Jira issue to fetch it from Jira Cloud and show it
	// inline. An empty base_url/email/api_token disables the panel. See
	// internal/jira and internal/ui.
	Jira JiraConfig `yaml:"jira"`
	// GitLab configures the merge-request side panel: press the open-reference
	// key (v) on a message linking a merge request to fetch it from GitLab and
	// show it inline. An empty base_url (with no usable token) disables the
	// panel. See internal/gitlab and internal/ui.
	GitLab GitLabConfig `yaml:"gitlab"`
	// LanguageTool configures the optional grammar/spell checker for the
	// composer: while you type, the draft is checked against a LanguageTool
	// server and mistakes are underlined in place (alt+g surfaces the
	// suggestions). Off unless enabled is true. See internal/languagetool and
	// internal/ui/grammar.go.
	LanguageTool LanguageToolConfig `yaml:"language_tool"`
}

// LanguageToolConfig holds the grammar/spell-checker settings. The feature is
// opt-in (enabled defaults false); server_url and language fall back to sensible
// defaults in fillDefaults so enabling it needs only `enabled: true`.
type LanguageToolConfig struct {
	// Enabled turns the composer grammar/spell check on. Pointer so an absent
	// key defaults to false (feature hidden) while `enabled: true` opts in.
	Enabled *bool `yaml:"enabled"`
	// ServerURL is the LanguageTool API base (the /v2 root), e.g.
	// http://localhost:8010/v2. The check endpoint is this + /check. Defaults to
	// http://localhost:8010/v2.
	ServerURL string `yaml:"server_url"`
	// Language is the checking language code, e.g. en-US, en-GB, nl, or "auto"
	// to let the server detect it per message. Defaults to "auto".
	Language string `yaml:"language"`
	// Picky enables LanguageTool's "picky" level — stricter style, typography
	// and grammar rules on top of the defaults. Pointer so an absent key
	// defaults to false (the default level). Set `picky: true` for strict mode.
	Picky *bool `yaml:"picky"`
}

// LanguageToolEnabled reports whether the composer grammar checker is on.
func (c Config) LanguageToolEnabled() bool {
	return c.LanguageTool.Enabled != nil && *c.LanguageTool.Enabled
}

// JiraConfig holds the Jira Cloud connection used by the issue side panel. All
// fields empty by default (the panel is opt-in). base_url + email + api_token
// must all be set for the panel to fetch; projects gates bare-ID detection.
type JiraConfig struct {
	// BaseURL is the instance root, e.g. https://your-instance.atlassian.net.
	// Also used to recognise /browse/KEY links that point at this instance.
	BaseURL string `yaml:"base_url"`
	// Email is the Atlassian account email — the username half of the Cloud
	// Basic-auth pair.
	Email string `yaml:"email"`
	// APIToken is an Atlassian API token (id.atlassian.com → Security → API
	// tokens). The JIRA_API_TOKEN environment variable overrides this, handy for
	// keeping the secret out of the YAML file.
	APIToken string `yaml:"api_token"`
	// Projects is the allowlist of project keys (e.g. ["ABC", "PROJ"]) for which
	// a BARE id like ABC-123 in message text opens the panel. Empty (the
	// default) means only full atlassian.net/browse/KEY links are detected — no
	// bare-id guessing, so look-alikes like "UTF-8" never trigger.
	Projects []string `yaml:"projects"`
	// StoryPointsField pins the custom-field id that holds story points (e.g.
	// "customfield_10016"). Empty (the default) auto-detects it from the
	// instance's field metadata — set this only if auto-detection picks the
	// wrong field.
	StoryPointsField string `yaml:"story_points_field"`
}

// GitLabConfig holds the GitLab connection used by the merge-request side
// panel. base_url is required; token may be left empty to fall back to an
// existing glab CLI login (or the GITLAB_TOKEN env var).
type GitLabConfig struct {
	// BaseURL is the instance root, e.g. https://git.example.com. Also used to
	// recognise /-/merge_requests/N links that point at this instance.
	BaseURL string `yaml:"base_url"`
	// Token is a personal or project access token (read_api is enough to view;
	// api is needed for the approve/merge actions). Empty falls back to the
	// GITLAB_TOKEN env var, then to the token glab stored for this host in
	// ~/.config/glab-cli/config.yml — so an existing `glab auth login` just
	// works without copying the secret into this file.
	Token string `yaml:"token"`
}

// TelegramConfig holds the credentials for the Telegram bridge. Both fields are
// empty by default (the bridge is opt-in); set them to forward mentions.
type TelegramConfig struct {
	// BotToken is the bot token from @BotFather. Empty disables delivery.
	BotToken string `yaml:"bot_token"`
	// ChatID is the destination: a numeric chat id (message the bot, then read
	// it from https://api.telegram.org/bot<token>/getUpdates) or an
	// @channelusername.
	ChatID string `yaml:"chat_id"`
}

// ListenConfig holds the behaviour of the `matterbox listen` daemon. Defaults
// in fillDefaults.
type ListenConfig struct {
	// NotifyOnMention forwards a notification when you are directly @mentioned
	// or sent a DM. Pointer so an absent key defaults to true while an explicit
	// false runs the daemon as a cache-warmer only.
	NotifyOnMention *bool `yaml:"notify_on_mention"`
	// Summarize controls whether the notification is an LLM summary of the
	// surrounding conversation (true) — using the `summary` endpoint+model — or
	// just the raw message text (false). A summary automatically falls back to
	// raw text when the chat server is down. Pointer so an absent key defaults
	// to true.
	Summarize *bool `yaml:"summarize"`
	// NotifyPrompt is the system prompt for the notification summary. The
	// reader's @username and the message source are appended at request time.
	NotifyPrompt string `yaml:"notify_prompt"`
	// RespectMutes skips notifications for channels you've muted in Mattermost.
	// Pointer so an absent key defaults to true.
	RespectMutes *bool `yaml:"respect_mutes"`
	// QuietHours suppresses notifications during a daily window, "HH:MM-HH:MM"
	// in local time (e.g. "22:00-08:00"; may wrap past midnight). Empty = always
	// on. Messages are still cached — catch up with the bot's /unread command.
	QuietHours string `yaml:"quiet_hours"`
	// TwoWay enables the inbound Telegram channel: reply to a notification to
	// post back, tap the 👍 / ✓ buttons, and run /search /unread /digest. Needs
	// telegram.chat_id (the only sender the bot obeys). Pointer so an absent key
	// defaults to true; set false for notify-only.
	TwoWay *bool `yaml:"two_way"`
	// NotifyDMs controls whether direct-message channels trigger notifications.
	// When false (the default), only explicit channel @mentions are forwarded —
	// a DM conversation you are actively reading no longer pings you on Telegram.
	// Set true to restore the original behaviour. Pointer so an absent key
	// defaults to false.
	NotifyDMs *bool `yaml:"notify_dms"`
	// NotifyDelaySeconds is how long (in seconds) the daemon waits after seeing
	// a mention before sending the Telegram notification. During that window it
	// checks the Mattermost server's LastViewedAt for the channel: if any client
	// (TUI, mobile, web — on any machine) has since marked it read, the
	// notification is suppressed. 0 = deliver immediately. Pointer so an absent
	// key defaults to 60.
	NotifyDelaySeconds *int `yaml:"notify_delay_seconds"`
}

// RuleConfig is one rule in the `rules:` list for the `matterbox listen`
// daemon. It is the YAML form; internal/listen compiles it (validating globs,
// regexps, and action types at startup). See docs/rules.md.
type RuleConfig struct {
	// Name labels the rule in the daemon log (optional).
	Name string `yaml:"name,omitempty"`
	// Stop halts evaluation of later rules once this one matches.
	Stop bool `yaml:"stop,omitempty"`
	// Match holds the conditions; all set conditions must hold (AND). An empty
	// match matches every non-system, non-empty post.
	Match RuleMatchConfig `yaml:"match"`
	// Actions run in order when Match passes.
	Actions []RuleActionConfig `yaml:"actions"`
}

// RuleMatchConfig holds a rule's conditions. See internal/listen.MatchSpec.
type RuleMatchConfig struct {
	// Channel is a case-insensitive glob (*, ?) over the channel's display name,
	// or an exact channel id. Accepts a single value or a list (match any).
	Channel StringList `yaml:"channel,omitempty"`
	// Author is a username (no leading @), matched case-insensitively. Accepts a
	// single value or a list (match any).
	Author StringList `yaml:"author,omitempty"`
	// Message is an RE2 regexp over the body (prefix (?i) for case-insensitive).
	Message string `yaml:"message,omitempty"`
	// Mention requires that you were directly named (@you).
	Mention bool `yaml:"mention,omitempty"`
	// DM, when set, requires a direct message (true) or a channel (false).
	DM *bool `yaml:"dm,omitempty"`
	// HasFile requires at least one attachment.
	HasFile bool `yaml:"has_file,omitempty"`
	// IsThread, when set, requires a thread reply (true) or a root post (false).
	IsThread *bool `yaml:"is_thread,omitempty"`
	// Not inverts a nested match: the rule fires only when the post does NOT
	// satisfy it (e.g. everything in a channel except posts from a bot).
	Not *RuleMatchConfig `yaml:"not,omitempty"`
}

// RuleActionConfig is one action. type is required; the remaining fields are
// read per type. See internal/listen.ActionSpec.
type RuleActionConfig struct {
	// Type is one of: notify, exec, webhook, react, mark_read, log.
	Type string `yaml:"type"`
	// Summarize (notify) overrides listen.summarize for this rule only.
	Summarize *bool `yaml:"summarize,omitempty"`
	// Urgent (notify) delivers even during quiet hours / for muted channels.
	Urgent bool `yaml:"urgent,omitempty"`
	// ChatID (notify) overrides the destination Telegram chat for this rule.
	ChatID string `yaml:"chat_id,omitempty"`
	// Command (exec) is the argv; the post is piped to stdin as JSON and its
	// fields exported as MATTERBOX_* env vars.
	Command []string `yaml:"command,omitempty"`
	// URL (webhook) receives the post envelope as a JSON POST body.
	URL string `yaml:"url,omitempty"`
	// Headers (webhook) are extra request headers; values are expanded from the
	// daemon's environment ($TOKEN) so secrets stay out of the config.
	Headers map[string]string `yaml:"headers,omitempty"`
	// Emoji (react) is the Mattermost emoji shortcode to add (no colons).
	Emoji string `yaml:"emoji,omitempty"`
	// Text (log) is an optional prefix for the log line.
	Text string `yaml:"text,omitempty"`
}

// StringList is a YAML field that accepts either a single scalar ("ops") or a
// list (["ops", "alerts"]), so a condition can match one value or several
// without a breaking schema change. It always unmarshals to a slice.
type StringList []string

// UnmarshalYAML accepts a scalar or a sequence of scalars.
func (s *StringList) UnmarshalYAML(unmarshal func(any) error) error {
	var one string
	if err := unmarshal(&one); err == nil {
		if one == "" {
			*s = nil
		} else {
			*s = StringList{one}
		}
		return nil
	}
	var many []string
	if err := unmarshal(&many); err != nil {
		return err
	}
	*s = StringList(many)
	return nil
}

// GiphyConfig configures pasted-Giphy-link expansion. Defaults in fillDefaults.
type GiphyConfig struct {
	// APIKey is a Giphy API key (https://developers.giphy.com). Optional: with
	// no key a pasted link still expands offline to a working image, but the alt
	// text comes from the URL slug ("gif" for a bare media link) rather than the
	// GIF's real title. The GIPHY_API_KEY environment variable overrides this.
	APIKey string `yaml:"api_key"`
	// Rendition is which Giphy size to post: "fixed_height" (default, 200px tall
	// — the picker's look), "fixed_height_small" (100px), "fixed_width" (200px
	// wide), "downsized" / "downsized_medium" (full dimensions, size-capped;
	// needs api_key), or "original" (full quality, can be several MB).
	Rendition string `yaml:"rendition"`
}

// AnimationsConfig groups the optional motion toggles. Each field is a pointer
// so an absent key takes the (on) default while an explicit false disables just
// that animation. Defaults in fillDefaults.
type AnimationsConfig struct {
	// CustomEmoji animates GIF custom (server) emoji: every appearance of an
	// animated-GIF emoji cycles through its frames in place. No effect unless
	// emoji_images renders custom emoji as images in the first place. Pointer
	// so an absent key defaults to true; an explicit false freezes them on the
	// first frame (the pre-animation behaviour).
	CustomEmoji *bool `yaml:"custom_emoji"`
	// ImagePreview animates GIFs in the image-preview modal (space on a message
	// with an image attachment). Same Kitty-only path as still previews; an
	// explicit false shows the first frame only. Pointer so an absent key
	// defaults to true.
	ImagePreview *bool `yaml:"image_preview"`
}

// KeybindingsConfig holds optional keymap tweaks. Defaults in fillDefaults.
type KeybindingsConfig struct {
	// NavModifier sets the modifier for the arrow-key sidebar navigation
	// (switch team with ←/→ and channel with ↑/↓ from any focus). One of
	// "ctrl" (default), "alt", "shift", "super" (the macOS ⌘ / Windows key;
	// also accepted as "cmd"), "meta", "hyper", or "none" to turn arrow-nav
	// off — which frees ctrl+←/→ for the composer's word-jump. The ctrl+h/j/k/l
	// vim aliases stay bound regardless. On macOS ctrl+arrows collide with
	// Mission Control: "shift" is the most broadly compatible alternative, and
	// "super" (⌘) works on terminals that speak the Kitty keyboard protocol
	// (Ghostty, kitty, WezTerm) but not the default Terminal.app / iTerm2.
	NavModifier string `yaml:"nav_modifier"`

	// VimNav controls when the ctrl+h/j/k/l vim keys switch team / channel:
	// "global" (default) navigates from any focus, including while typing;
	// "reading" navigates only outside text inputs, so ctrl+h / ctrl+k stay
	// available as the composer's emacs editing keys; "off" never navigates
	// with the vim keys. The modifier-arrow aliases (see NavModifier) keep
	// navigating in every mode. See internal/ui.
	VimNav string `yaml:"vim_nav"`

	// CtrlArrowNav is the superseded boolean toggle. Kept only so a config
	// written before NavModifier existed keeps working: when NavModifier is
	// unset, an explicit false migrates to "none". fillDefaults clears it once
	// migrated, so a rewritten config carries only nav_modifier.
	CtrlArrowNav *bool `yaml:"ctrl_arrow_nav,omitempty"`

	// Bindings maps an action id (e.g. "channel_next", "delete_post") to the
	// key or keys that trigger it, replacing that action's defaults. A value
	// may be a single string ("shift+d") or a list (["i", "a"]); an empty
	// list or "none" unbinds the action. Unknown action names and unparseable
	// chords are reported as startup errors (see internal/ui). Absent by
	// default — the action ids are documented in the config header.
	Bindings map[string]StringOrList `yaml:"bindings,omitempty"`
}

// StringOrList is a yaml field that accepts either a single scalar string or
// a list of strings, so `compose: i` and `compose: [i, a]` both parse. A null
// or empty value yields an empty slice (used to unbind an action).
type StringOrList []string

// UnmarshalYAML accepts a scalar (→ one element), a sequence (→ the list), or
// null/empty (→ empty slice).
func (s *StringOrList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag == "!!null" || value.Value == "" {
			*s = []string{}
			return nil
		}
		*s = []string{value.Value}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*s = list
		return nil
	default:
		return fmt.Errorf("keybinding value must be a string or a list, got yaml kind %d", value.Kind)
	}
}

// SearchConfig tunes local message search ranking. Defaults in fillDefaults.
type SearchConfig struct {
	// RecencyHalfLifeDays controls how strongly older messages are
	// down-weighted in ranked results: a match's relevance weight halves for
	// every this-many days of age, so recent discussion outranks stale chat
	// unless an older message is much more relevant. Lower = stronger recency
	// bias; raise it to make age matter less. Defaults to 90.
	RecencyHalfLifeDays float64 `yaml:"recency_half_life_days"`
}

// SummaryConfig holds the settings for the message-summary command. All
// fields default in fillDefaults so an empty/missing section still works
// against a local llama.cpp / Ollama style server.
type SummaryConfig struct {
	// Endpoint is the base URL of the OpenAI-compatible server. The client
	// appends "/v1/chat/completions" (a trailing "/v1" is accepted too).
	Endpoint string `yaml:"endpoint"`
	// APIKey is the optional Bearer token sent in each request's Authorization
	// header. Not needed for a local server; required by hosted API services.
	APIKey string `yaml:"api_key"`
	// Model is the model id sent in the request. Defaults to whatever was
	// available locally when this build shipped; run `curl <endpoint>/v1/models`
	// to see what your server actually has loaded.
	Model string `yaml:"model"`
	// Prompt is the system prompt prepended to the transcript. The current
	// user's @username is appended at request time so the model can flag
	// where the reader is mentioned.
	Prompt string `yaml:"prompt"`
}

// AISearchConfig holds the settings for agentic search on the Search tab.
// The endpoint + model come from SummaryConfig (one local server); these
// fields tune the agent itself. Both default in fillDefaults.
type AISearchConfig struct {
	// Prompt is the system prompt that frames the search agent: how to use
	// the tools, when to stop, and to never answer from its own knowledge.
	// The available team names and the current scope are appended at request
	// time so the model knows where it can look.
	Prompt string `yaml:"prompt"`
	// MaxSteps caps how many tool-call rounds the agent may take before it is
	// forced to answer with whatever it has. Keeps a small model from looping
	// and bounds the per-search token + latency cost.
	MaxSteps int `yaml:"max_steps"`
	// TimeoutMinutes bounds the whole agentic run (all tool rounds together),
	// not a single request. Generous because each round waits on a local model;
	// raise it for a slow server or a high MaxSteps. Defaults in fillDefaults.
	TimeoutMinutes int `yaml:"timeout_minutes"`
}

// EmbeddingsConfig holds the settings for semantic search. Defaults in
// fillDefaults target a second local llama.cpp instance (separate port from the
// chat server) launched with --embeddings.
type EmbeddingsConfig struct {
	// Endpoint is the base URL of the OpenAI-compatible embeddings server. The
	// client appends "/v1/embeddings" (a trailing "/v1" is accepted too).
	// Defaults to a different port than Summary.Endpoint because an embedding
	// model and a chat model are separate llama.cpp instances.
	Endpoint string `yaml:"endpoint"`
	// APIKey is the optional Bearer token (not needed for a local server).
	APIKey string `yaml:"api_key"`
	// Model is the embedding model id sent in each request. EmbeddingGemma is
	// the default: it pairs with the Gemma chat model and supports Matryoshka
	// truncation (see Dim).
	Model string `yaml:"model"`
	// Dim truncates each embedding to its first Dim components (then
	// renormalizes) before storing — a Matryoshka model stays meaningful at a
	// smaller size, cutting the on-disk vector to Dim bytes. 0 keeps the model's
	// native dimensionality.
	Dim int `yaml:"dim"`
	// AutoIndex enables the TUI's background indexer, which embeds not-yet-
	// embedded messages while you use the app (newest first, plus new messages
	// as they arrive). Pointer so an absent key defaults to true while an
	// explicit `false` keeps it off — e.g. to reserve the GPU for the chat model
	// and only embed via the `matterbox embed` command. Either way matterbox
	// runs fine when the embeddings server is down (semantic search just
	// degrades to keyword).
	AutoIndex *bool `yaml:"auto_index"`
}

// PlaceholderServerURL is the stand-in server_url written into a fresh
// config when none is set. Commands that need a real server (e.g. `login`)
// treat a server_url still equal to this as "not configured yet".
const PlaceholderServerURL = "https://mattermost.example.com"

// defaultServerURL is a placeholder used when config.yaml is missing or
// has no server_url set. Point it at your own Mattermost instance by
// setting server_url in config.yaml.
const defaultServerURL = PlaceholderServerURL

// Summary defaults. The model is the one that was available on the local
// server (http://127.0.0.1:8321) at build time; override it in config.yaml
// once your server loads a different model.
const (
	defaultSummaryEndpoint = "http://127.0.0.1:8321"
	defaultSummaryModel    = "gemma-4-E4B-it-UD-Q4_K_XL.gguf"
	defaultSummaryPrompt   = "You are a concise assistant that summarizes Mattermost chat conversations for a busy reader.\n\n" +
		"Given the transcript below, produce a short summary that captures:\n" +
		"- the main topics discussed, grouped by theme;\n" +
		"- any decisions made and open questions still unanswered;\n" +
		"- concrete action items and who owns them.\n\n" +
		"Use short markdown bullet points. Refer to people by their @username exactly as written in the transcript. " +
		"Skip greetings and small talk. If little of substance was discussed, say so in one line."
)

// Embeddings defaults. The endpoint is a SEPARATE port from the summary/chat
// server (127.0.0.1:8321) because semantic search needs an embedding model
// loaded with --embeddings — its own llama.cpp instance. EmbeddingGemma is a
// Matryoshka model, so the default Dim truncates 768→256 to quarter the on-disk
// vector size at little quality cost.
const (
	defaultEmbeddingsEndpoint = "http://127.0.0.1:8322"
	defaultEmbeddingsModel    = "embeddinggemma-300m-qat-Q8_0.gguf"
	defaultEmbeddingsDim      = 256
)

// defaultSearchRecencyHalfLifeDays mirrors store.defaultRecencyHalfLife (90
// days): old enough to keep strong older matches reachable, recent-biased
// enough that stale chat sinks. Kept here so the config is self-documenting.
const defaultSearchRecencyHalfLifeDays = 90

// defaultGiphyRendition is the Giphy size posted when none is configured:
// fixed_height (200px tall) matches how the Mattermost GIF picker normally
// posts (a /200.gif), and is light enough that everyone in the channel loads
// it quickly.
const defaultGiphyRendition = "fixed_height"

// LanguageTool defaults: a locally-running server and per-message language
// auto-detection, so enabling the feature needs only `enabled: true`.
const (
	defaultLanguageToolURL  = "http://localhost:8010/v2"
	defaultLanguageToolLang = "auto"
)

// defaultMarkReadDelaySeconds is the dwell a channel must stay open before
// it's marked read. Long enough that an accidental peek doesn't clear
// unread, short enough not to feel laggy when you actually read it.
const defaultMarkReadDelaySeconds = 5

// defaultDownloadDir is where attachments are saved when no download_dir is
// configured. A leading "~" is expanded to the user's home directory in
// internal/ui when the directory is resolved.
const defaultDownloadDir = "~/Downloads"

// defaultGroupMessageSeconds is the window within which consecutive
// same-author messages collapse under a single header. Two minutes mirrors
// the grouping window the Mattermost web client and similar chat clients use.
const defaultGroupMessageSeconds = 120

// AI-search defaults. The agent runs against the same local server as the
// summarizer (Summary.Endpoint / Summary.Model).
const (
	defaultAISearchMaxSteps       = 32
	defaultAISearchTimeoutMinutes = 4
	defaultAISearchPrompt         = "You are a search agent embedded in a Mattermost chat client. The user describes what they're looking for; you find the actual messages by calling the provided tools, then call finish.\n\n" +
		"Rules:\n" +
		"- Always use the tools to find real messages. Never answer from your own knowledge or invent content.\n" +
		"- search_messages has three modes. mode:\"keyword\" (default) matches exact words — best for specific terms, names, error codes, or IDs. mode:\"semantic\" matches by MEANING from a natural-language 'query' — it finds paraphrases, synonyms, and other languages (this chat mixes Dutch and English), so reach for it when you know the topic but not the exact words people used. mode:\"hybrid\" fuses both. If a keyword search returns 0 or only weak hits, retry the same idea with mode:\"semantic\".\n" +
		"- Search by TOPIC, not by subject name. A project, team, or channel name (e.g. \"Acme\") usually appears only in the channel title, NOT inside the messages — so never put it in your search terms or 'query'. Use it to find the channel instead: call list_channels, or pass it as the 'channel'/'team' argument.\n" +
		"- In keyword mode, start broad: put a few topic words and synonyms in 'any_of'. If there are too many matches, NARROW with an 'all_of' term, a 'phrase', or a 'none_of' term. If there are zero, LOOSEN — drop an all_of/phrase term, add synonyms, or switch to mode:\"semantic\". In semantic/hybrid mode, put a short natural-language description in 'query' and rephrase it if the hits are off.\n" +
		"- If the shown matches look unrelated but the count says there are more, page deeper with 'offset' (10, 20, …) before changing the query.\n" +
		"- Use read_around on a promising hit (by its mN ref) to confirm context before answering.\n" +
		"- Keep going until you have solid evidence, but use only a handful of tool calls.\n" +
		"- When you can answer, call finish with a one- or two-sentence answer that names the channel(s) where the information was found. If you found nothing relevant, say so plainly."
)

// defaultListenNotifyPrompt frames the notification summary for the
// `matterbox listen` Telegram bridge. Kept short: the output is a push
// notification, not a report.
const defaultListenNotifyPrompt = "You are a notification assistant for a Mattermost user. " +
	"Summarize the conversation below in one or two short sentences suitable for a phone push " +
	"notification: who said what, and what (if anything) the reader needs to do. Be concise and " +
	"concrete, refer to people by their @username, and never invent details that aren't in the text."

// defaultReactions is the picker list used when the user hasn't
// configured their own. Names match Mattermost's emoji shortcodes.
var defaultReactions = []string{
	"+1",
	"-1",
	"heart",
	"tada",
	"eyes",
	"rocket",
	"laughing",
	"thinking_face",
}

// Path returns the canonical config file location.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("locate config dir: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "matterbox", "config.yaml"), nil
}

// Load reads config.yaml, applies defaults, and writes the file back to
// disk if it didn't already exist so the user can discover what's
// configurable just by opening it. Parse errors are surfaced verbatim
// (the user wrote something we couldn't make sense of — silently
// falling back to defaults would hide the typo).
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	b, err := os.ReadFile(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		cfg.fillDefaults()
		if werr := writeConfig(p, cfg); werr != nil {
			// Non-fatal: the app can run without persisting the file.
			fmt.Fprintf(os.Stderr, "matterbox: could not write default config to %s: %v\n", p, werr)
		}
		return cfg, nil
	case err != nil:
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", p, err)
	}
	// An existing config from before the summary feature won't have the
	// section at all. Detect that (whole section empty), fill the defaults,
	// and rewrite the file once so the discovered model + prompt show up as
	// editable defaults. Best-effort: a failed rewrite only means the file
	// keeps working off in-memory defaults.
	addDefaults := cfg.Summary == (SummaryConfig{}) || cfg.AISearch == (AISearchConfig{}) || cfg.Embeddings == (EmbeddingsConfig{}) || cfg.Embeddings.AutoIndex == nil || cfg.Search == (SearchConfig{}) || cfg.MarkReadDelaySeconds == nil || cfg.GroupMessageSeconds == nil || cfg.DownloadDir == "" || cfg.SQLTab == nil || cfg.Keybindings.NavModifier == "" || cfg.Keybindings.VimNav == "" || cfg.EmojiImages == "" || cfg.Animations.CustomEmoji == nil || cfg.Animations.ImagePreview == nil || cfg.Giphy.Rendition == "" || cfg.Listen.NotifyOnMention == nil || cfg.Listen.Summarize == nil || cfg.Listen.NotifyPrompt == "" || cfg.Listen.RespectMutes == nil || cfg.Listen.TwoWay == nil || cfg.Listen.NotifyDMs == nil || cfg.Listen.NotifyDelaySeconds == nil
	cfg.fillDefaults()
	if addDefaults {
		if werr := writeConfig(p, cfg); werr != nil {
			fmt.Fprintf(os.Stderr, "matterbox: could not add LLM defaults to %s: %v\n", p, werr)
		}
	}
	return cfg, nil
}

func (c *Config) fillDefaults() {
	if c.ServerURL == "" {
		c.ServerURL = defaultServerURL
	}
	if len(c.Reactions) == 0 {
		c.Reactions = append([]string(nil), defaultReactions...)
	}
	if c.Summary.Endpoint == "" {
		c.Summary.Endpoint = defaultSummaryEndpoint
	}
	if c.Summary.Model == "" {
		c.Summary.Model = defaultSummaryModel
	}
	if c.Summary.Prompt == "" {
		c.Summary.Prompt = defaultSummaryPrompt
	}
	if c.AISearch.Prompt == "" {
		c.AISearch.Prompt = defaultAISearchPrompt
	}
	if c.AISearch.MaxSteps <= 0 {
		c.AISearch.MaxSteps = defaultAISearchMaxSteps
	}
	if c.AISearch.TimeoutMinutes <= 0 {
		c.AISearch.TimeoutMinutes = defaultAISearchTimeoutMinutes
	}
	if c.Embeddings.Endpoint == "" {
		c.Embeddings.Endpoint = defaultEmbeddingsEndpoint
	}
	if c.Embeddings.Model == "" {
		c.Embeddings.Model = defaultEmbeddingsModel
	}
	if c.Embeddings.Dim <= 0 {
		c.Embeddings.Dim = defaultEmbeddingsDim
	}
	if c.Embeddings.AutoIndex == nil {
		t := true
		c.Embeddings.AutoIndex = &t
	}
	if c.Search.RecencyHalfLifeDays <= 0 {
		c.Search.RecencyHalfLifeDays = defaultSearchRecencyHalfLifeDays
	}
	if c.MarkReadDelaySeconds == nil {
		d := defaultMarkReadDelaySeconds
		c.MarkReadDelaySeconds = &d
	}
	if c.GroupMessageSeconds == nil {
		d := defaultGroupMessageSeconds
		c.GroupMessageSeconds = &d
	}
	if c.CustomStatus == nil {
		t := true
		c.CustomStatus = &t
	}
	if c.DownloadDir == "" {
		c.DownloadDir = defaultDownloadDir
	}
	if c.SQLTab == nil {
		f := false
		c.SQLTab = &f
	}
	if c.LanguageTool.Enabled == nil {
		f := false
		c.LanguageTool.Enabled = &f
	}
	if c.LanguageTool.ServerURL == "" {
		c.LanguageTool.ServerURL = defaultLanguageToolURL
	}
	if c.LanguageTool.Language == "" {
		c.LanguageTool.Language = defaultLanguageToolLang
	}
	if c.LanguageTool.Picky == nil {
		f := false
		c.LanguageTool.Picky = &f
	}
	if c.Keybindings.NavModifier == "" {
		// Default to the ctrl modifier, but honour a pre-NavModifier config's
		// ctrl_arrow_nav: false by migrating it to "none".
		c.Keybindings.NavModifier = "ctrl"
		if c.Keybindings.CtrlArrowNav != nil && !*c.Keybindings.CtrlArrowNav {
			c.Keybindings.NavModifier = "none"
		}
	}
	if c.Keybindings.VimNav == "" {
		c.Keybindings.VimNav = "global"
	}
	// The legacy toggle has been folded into NavModifier; drop it so a
	// rewritten config carries only the new key.
	c.Keybindings.CtrlArrowNav = nil
	if c.EmojiImages == "" {
		c.EmojiImages = "auto"
	}
	if c.Animations.CustomEmoji == nil {
		t := true
		c.Animations.CustomEmoji = &t
	}
	if c.Animations.ImagePreview == nil {
		t := true
		c.Animations.ImagePreview = &t
	}
	if c.Giphy.Rendition == "" {
		c.Giphy.Rendition = defaultGiphyRendition
	}
	if c.Listen.NotifyOnMention == nil {
		t := true
		c.Listen.NotifyOnMention = &t
	}
	if c.Listen.Summarize == nil {
		t := true
		c.Listen.Summarize = &t
	}
	if c.Listen.NotifyPrompt == "" {
		c.Listen.NotifyPrompt = defaultListenNotifyPrompt
	}
	if c.Listen.RespectMutes == nil {
		t := true
		c.Listen.RespectMutes = &t
	}
	if c.Listen.TwoWay == nil {
		t := true
		c.Listen.TwoWay = &t
	}
	if c.Listen.NotifyDMs == nil {
		f := false
		c.Listen.NotifyDMs = &f
	}
	if c.Listen.NotifyDelaySeconds == nil {
		d := 60
		c.Listen.NotifyDelaySeconds = &d
	}
}

// SaveTeamOrder persists the given left-to-right team-tab ordering to
// config.yaml, leaving every other setting as it is on disk. Best-effort
// from the caller's perspective: the UI fires this after each reorder and
// ignores the error, since a failed write only loses the new ordering.
func SaveTeamOrder(order []string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	cfg.TeamOrder = order
	p, err := Path()
	if err != nil {
		return err
	}
	return writeConfig(p, cfg)
}

// writeConfig serialises a config to disk with a small header so the file
// reads as documentation as well as data.
func writeConfig(p string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	const header = "# matterbox configuration\n" +
		"# server_url: Mattermost base URL (https://...).\n" +
		"# reactions:  emoji shortcodes shown in the reaction picker (R on a message).\n" +
		"# team_order: left-to-right team tabs (team names); reorder in-app with < / >.\n" +
		"# summary:    the '> Summarize' command (ctrl+k). endpoint is an\n" +
		"#             OpenAI-compatible server; api_key is an optional Bearer\n" +
		"#             token for hosted APIs; model is sent verbatim; prompt is\n" +
		"#             the system prompt prepended to the chat transcript.\n" +
		"# ai_search:  agentic search on the Search tab (a query ending in '?').\n" +
		"#             Reuses the summary endpoint+model; prompt frames the agent,\n" +
		"#             max_steps caps the tool-call rounds, timeout_minutes bounds\n" +
		"#             the whole run.\n" +
		"# embeddings: semantic search. A SEPARATE OpenAI-compatible server from\n" +
		"#             summary (its own port + an embedding model loaded with\n" +
		"#             --embeddings; see scripts/llama-embeddings.sh). model is\n" +
		"#             sent verbatim; dim truncates each vector (Matryoshka) to\n" +
		"#             that many dimensions before storing; auto_index toggles the\n" +
		"#             background indexer (false = embed only via `matterbox embed`).\n" +
		"# search:     ranking for the Search tab and AI search. recency_half_life_days\n" +
		"#             halves a match's relevance weight per that many days of age\n" +
		"#             (lower = stronger recency bias; default 90).\n" +
		"# mark_read_delay_seconds: how long a channel must stay open before it's\n" +
		"#             marked read (default 5). 0 marks read immediately on open.\n" +
		"# group_message_seconds: collapse the name+time header on consecutive\n" +
		"#             messages from the same person sent within this many seconds\n" +
		"#             of each other (default 120). 0 keeps a header on every\n" +
		"#             message.\n" +
		"# custom_status: show DM partners' custom statuses (default true); false\n" +
		"#             shows presence dots only.\n" +
		"# download_dir: where the download-attachment key (s on a message) saves\n" +
		"#             files (default ~/Downloads). A leading ~ is expanded and the\n" +
		"#             directory is created on first download.\n" +
		"# sql_tab:    show the read-only SQL tab — a query editor over your local\n" +
		"#             message cache whose rows render as chat messages (default\n" +
		"#             false, hidden). Set true to add it to the tab strip.\n" +
		"# keybindings: nav_modifier sets the modifier for arrow-key team/channel\n" +
		"#             navigation: ctrl (default), alt, shift, super (the ⌘/Windows\n" +
		"#             key; also \"cmd\"), meta, hyper, or none. On macOS ctrl+arrows\n" +
		"#             clash with Mission Control — try shift, or super on a\n" +
		"#             Kitty-protocol terminal (Ghostty/kitty/WezTerm).\n" +
		"#             vim_nav controls the ctrl+h/j/k/l keys: global (default,\n" +
		"#             navigate from anywhere incl. while typing), reading (navigate\n" +
		"#             only outside text inputs, freeing ctrl+h/ctrl+k for the\n" +
		"#             composer's emacs editing), or off. Arrow nav stays on in all\n" +
		"#             modes.\n" +
		"#             bindings rebinds individual actions by id: a single key or a\n" +
		"#             list, e.g.  bindings: {compose: [i, a], delete_post: shift+d}.\n" +
		"#             An empty list or \"none\" unbinds. Modifiers: ctrl/alt/shift/\n" +
		"#             super/meta/hyper. Unknown action ids and bad chords are\n" +
		"#             reported at startup with the full list of valid actions.\n" +
		"#             Jump-to actions: goto_team (alt+1…9), goto_dm (alt+d),\n" +
		"#             goto_feed (alt+u); search_here / search_all answer to\n" +
		"#             ctrl+f / ctrl+shift+f. On macOS, alt+ reaches the app only\n" +
		"#             with macos-option-as-alt = true in Ghostty — otherwise\n" +
		"#             rebind these here (e.g. goto_team: [super+1, …]).\n" +
		"# emoji_images: render custom (server) emoji as inline images via the\n" +
		"#             Kitty graphics protocol. auto (default) enables them on a\n" +
		"#             Kitty/Ghostty truecolor terminal outside tmux; off keeps\n" +
		"#             literal :name: text. Unicode emoji are unaffected.\n" +
		"# animations: optional motion effects, off-able if distracting.\n" +
		"#             custom_emoji (default true) animates GIF custom emoji in\n" +
		"#             place; image_preview (default true) animates GIFs in the\n" +
		"#             space-to-preview modal; false freezes either on frame one.\n" +
		"# giphy:      expand a pasted Giphy link into an inline image. The link is\n" +
		"#             turned into ![alt](url) instantly (offline, from its id);\n" +
		"#             with api_key set (https://developers.giphy.com, or the\n" +
		"#             GIPHY_API_KEY env var) the line is then upgraded with the\n" +
		"#             GIF's real title. rendition picks the size: fixed_height\n" +
		"#             (default, 200px), fixed_height_small (100px), fixed_width,\n" +
		"#             downsized / downsized_medium (need api_key), or original.\n" +
		"# telegram:   outbound bridge for `matterbox listen`. bot_token is a\n" +
		"#             @BotFather token; chat_id is the destination (numeric id, or\n" +
		"#             @channelusername). Empty bot_token disables delivery.\n" +
		"# listen:     the `matterbox listen` daemon. notify_on_mention (default\n" +
		"#             true) forwards direct @mentions and DMs to Telegram;\n" +
		"#             summarize (default true) sends an LLM summary of the\n" +
		"#             surrounding context (via the summary endpoint+model, falling\n" +
		"#             back to raw text when it's down) instead of the bare message;\n" +
		"#             notify_prompt is that summary's system prompt;\n" +
		"#             respect_mutes (default true) skips channels you muted in\n" +
		"#             Mattermost; quiet_hours (e.g. \"22:00-08:00\", local, may wrap\n" +
		"#             midnight; empty = always on) suppresses pushes in that window\n" +
		"#             (messages are still cached — use the bot's /unread);\n" +
		"#             two_way (default true) enables replying from Telegram and the\n" +
		"#             /search /unread /digest commands (needs telegram.chat_id);\n" +
		"#             notify_dms (default false) also forwards DM messages — off by\n" +
		"#             default so chatting in a DM you're actively reading stays quiet;\n" +
		"#             notify_delay_seconds (default 60) waits this long before sending\n" +
		"#             the notification, then checks the server's read state — if any\n" +
		"#             client marked the channel read during the window the notification\n" +
		"#             is suppressed (0 = deliver immediately, no read-check).\n" +
		"# rules:      per-message automation for `matterbox listen`. Each rule has a\n" +
		"#             match (conditions, ANDed) and actions (run in order). Match on\n" +
		"#             channel (display-name glob or id), author, message (RE2 regexp),\n" +
		"#             mention (you were @named), dm, has_file, is_thread; channel and\n" +
		"#             author take a single value or a list (match any), and a nested\n" +
		"#             not: inverts a sub-match. Actions: notify (Telegram; urgent\n" +
		"#             bypasses quiet_hours/mutes, chat_id routes elsewhere), exec (run\n" +
		"#             a command; the post is piped in as JSON + MATTERBOX_* env vars),\n" +
		"#             webhook (POST the post as JSON; headers add request headers,\n" +
		"#             values expanded from $ENV), react (emoji), mark_read, log.\n" +
		"#             stop: true ends evaluation. With no rules the daemon uses a\n" +
		"#             built-in notify rule from the listen options above. Full\n" +
		"#             reference + examples in docs/rules.md.\n" +
		"# jira:       the issue side panel. Press v on a message naming a Jira\n" +
		"#             issue to fetch it from Jira Cloud and view it inline.\n" +
		"#             base_url is the instance root (https://you.atlassian.net);\n" +
		"#             email + api_token are the Cloud Basic-auth pair (an API\n" +
		"#             token from id.atlassian.com, or the JIRA_API_TOKEN env var).\n" +
		"#             projects allowlists project keys (e.g. [ABC, PROJ]) whose\n" +
		"#             bare ids (ABC-123) open the panel; empty means only full\n" +
		"#             atlassian.net/browse/KEY links are detected.\n" +
		"#             story_points_field pins the story-points custom field id\n" +
		"#             (e.g. customfield_10016); empty auto-detects it.\n" +
		"# gitlab:     the merge-request side panel. Press v on a message linking a\n" +
		"#             merge request to fetch it from GitLab and view it inline\n" +
		"#             (title, pipeline status, merge readiness, approvals).\n" +
		"#             base_url is the instance root (https://git.example.com);\n" +
		"#             token is a personal/project access token (read_api to view,\n" +
		"#             api to approve/merge). Empty token falls back to GITLAB_TOKEN\n" +
		"#             or an existing glab CLI login for the same host.\n" +
		"# language_tool: composer grammar/spell check (off by default). enabled\n" +
		"#             true turns it on; while you type the draft is checked against\n" +
		"#             a LanguageTool server and mistakes are underlined in place —\n" +
		"#             alt+g opens the suggestions for the mistake at the cursor.\n" +
		"#             server_url is the API /v2 root (default\n" +
		"#             http://localhost:8010/v2); language is the code to check\n" +
		"#             against — en-US, en-GB, nl, … or auto (default) to detect\n" +
		"#             it per message; picky true enables strict mode (extra\n" +
		"#             style/typography/grammar rules; default false).\n"
	return os.WriteFile(p, append([]byte(header), body...), 0o644)
}
