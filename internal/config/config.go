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
	// Keybindings holds optional keymap tweaks. See internal/ui.
	Keybindings KeybindingsConfig `yaml:"keybindings"`
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

	// CtrlArrowNav is the superseded boolean toggle. Kept only so a config
	// written before NavModifier existed keeps working: when NavModifier is
	// unset, an explicit false migrates to "none". fillDefaults clears it once
	// migrated, so a rewritten config carries only nav_modifier.
	CtrlArrowNav *bool `yaml:"ctrl_arrow_nav,omitempty"`
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

// defaultMarkReadDelaySeconds is the dwell a channel must stay open before
// it's marked read. Long enough that an accidental peek doesn't clear
// unread, short enough not to feel laggy when you actually read it.
const defaultMarkReadDelaySeconds = 5

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
	addDefaults := cfg.Summary == (SummaryConfig{}) || cfg.AISearch == (AISearchConfig{}) || cfg.Embeddings == (EmbeddingsConfig{}) || cfg.Embeddings.AutoIndex == nil || cfg.Search == (SearchConfig{}) || cfg.MarkReadDelaySeconds == nil || cfg.Keybindings.NavModifier == ""
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
	if c.Keybindings.NavModifier == "" {
		// Default to the ctrl modifier, but honour a pre-NavModifier config's
		// ctrl_arrow_nav: false by migrating it to "none".
		c.Keybindings.NavModifier = "ctrl"
		if c.Keybindings.CtrlArrowNav != nil && !*c.Keybindings.CtrlArrowNav {
			c.Keybindings.NavModifier = "none"
		}
	}
	// The legacy toggle has been folded into NavModifier; drop it so a
	// rewritten config carries only the new key.
	c.Keybindings.CtrlArrowNav = nil
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
		"# keybindings: nav_modifier sets the modifier for arrow-key team/channel\n" +
		"#             navigation: ctrl (default), alt, shift, super (the ⌘/Windows\n" +
		"#             key; also \"cmd\"), meta, hyper, or none. ctrl+h/j/k/l always\n" +
		"#             navigate too. On macOS ctrl+arrows clash with Mission\n" +
		"#             Control — try shift, or super on a Kitty-protocol terminal\n" +
		"#             (Ghostty/kitty/WezTerm).\n"
	return os.WriteFile(p, append([]byte(header), body...), 0o644)
}
