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
	// Search tunes ranking for both the Search tab and AI search.
	Search SearchConfig `yaml:"search"`
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

// defaultServerURL is the URL used when config.yaml is missing or has
// no server_url. It preserves the original hard-coded behaviour from
// before this file existed.
const defaultServerURL = "https://mattermost.example.com"

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

// defaultSearchRecencyHalfLifeDays mirrors store.defaultRecencyHalfLife (90
// days): old enough to keep strong older matches reachable, recent-biased
// enough that stale chat sinks. Kept here so the config is self-documenting.
const defaultSearchRecencyHalfLifeDays = 90

// AI-search defaults. The agent runs against the same local server as the
// summarizer (Summary.Endpoint / Summary.Model).
const (
	defaultAISearchMaxSteps       = 32
	defaultAISearchTimeoutMinutes = 4
	defaultAISearchPrompt         = "You are a search agent embedded in a Mattermost chat client. The user describes what they're looking for; you find the actual messages by calling the provided tools, then call finish.\n\n" +
		"Rules:\n" +
		"- Always use the tools to find real messages. Never answer from your own knowledge or invent content.\n" +
		"- search_messages is KEYWORD search, not semantic — use concrete words you'd expect in the messages, not a rephrased question.\n" +
		"- Search by TOPIC, not by subject name. A project, team, or channel name (e.g. \"Acme\") usually appears only in the channel title, NOT inside the messages — so never put it in your search terms. Use it to find the channel instead: call list_channels, or pass it as the 'channel'/'team' argument.\n" +
		"- Start broad, then react to the match count the tool reports: put a few topic words and synonyms in 'any_of'. If there are too many matches, NARROW by adding an 'all_of' term, a 'phrase', or a 'none_of' term. If there are zero, don't pile on more words — LOOSEN: drop an all_of/phrase term, or call list_channels to find where the topic lives and search there.\n" +
		"- If the shown matches look unrelated but the count says there are more, page deeper with 'offset' (10, 20, …) before changing the query.\n" +
		"- Use read_around on a promising hit (by its mN ref) to confirm context before answering.\n" +
		"- Keep going until you have solid evidence, but use only a handful of tool calls.\n" +
		"- When you can answer, call finish with a one- or two-sentence answer that names the channel(s) where the information was found. If you found nothing relevant, say so plainly."

	// legacyAISearchPromptV1 is the first shipped default. It instructed the
	// model to dump many keywords into a single OR'd 'queries' array — a
	// parameter that no longer exists. fillDefaults upgrades any config still
	// carrying it verbatim to defaultAISearchPrompt (see Load for the rewrite).
	legacyAISearchPromptV1 = "You are a search agent embedded in a Mattermost chat client. The user describes what they're looking for; you find the actual messages by calling the provided tools, then call finish.\n\n" +
		"Rules:\n" +
		"- Always use the tools to find real messages. Never answer from your own knowledge or invent content.\n" +
		"- Search by TOPIC, not by subject name. A project, team, or channel name (e.g. \"Acme\") usually appears only in the channel title, NOT inside the messages — so never put it in your search terms. Use it to find the channel instead: call list_channels, or pass it as the 'channel'/'team' argument.\n" +
		"- In search_messages, list MANY keywords at once in 'queries': the topic words from the request, likely product/tool names, and synonyms/rephrasings. Terms are matched with OR and ranked by relevance, so breadth is free — more terms means better recall, and the strongest matches still come first.\n" +
		"- If results are weak, call list_channels to discover where the topic lives (it matches channel names and purposes), then search again scoped to a likely channel.\n" +
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
	addDefaults := cfg.Summary == (SummaryConfig{}) || cfg.AISearch == (AISearchConfig{}) || cfg.Search == (SearchConfig{})
	// An existing config may still carry the obsolete v1 search prompt; rewrite
	// the file so the upgraded prompt is persisted, not just patched in memory.
	migrated := cfg.AISearch.Prompt == legacyAISearchPromptV1
	cfg.fillDefaults()
	if addDefaults || migrated {
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
	// Empty (new config) or carrying the obsolete v1 prompt (which references
	// the removed 'queries' param) → install the current default.
	if c.AISearch.Prompt == "" || c.AISearch.Prompt == legacyAISearchPromptV1 {
		c.AISearch.Prompt = defaultAISearchPrompt
	}
	if c.AISearch.MaxSteps <= 0 {
		c.AISearch.MaxSteps = defaultAISearchMaxSteps
	}
	if c.AISearch.TimeoutMinutes <= 0 {
		c.AISearch.TimeoutMinutes = defaultAISearchTimeoutMinutes
	}
	if c.Search.RecencyHalfLifeDays <= 0 {
		c.Search.RecencyHalfLifeDays = defaultSearchRecencyHalfLifeDays
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
		"# search:     ranking for the Search tab and AI search. recency_half_life_days\n" +
		"#             halves a match's relevance weight per that many days of age\n" +
		"#             (lower = stronger recency bias; default 90).\n"
	return os.WriteFile(p, append([]byte(header), body...), 0o644)
}
