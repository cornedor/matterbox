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
}

// SummaryConfig holds the settings for the message-summary command. All
// fields default in fillDefaults so an empty/missing section still works
// against a local llama.cpp / Ollama style server.
type SummaryConfig struct {
	// Endpoint is the base URL of the OpenAI-compatible server. The client
	// appends "/v1/chat/completions" (a trailing "/v1" is accepted too).
	Endpoint string `yaml:"endpoint"`
	// Model is the model id sent in the request. Defaults to whatever was
	// available locally when this build shipped; run `curl <endpoint>/v1/models`
	// to see what your server actually has loaded.
	Model string `yaml:"model"`
	// Prompt is the system prompt prepended to the transcript. The current
	// user's @username is appended at request time so the model can flag
	// where the reader is mentioned.
	Prompt string `yaml:"prompt"`
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
	addSummary := cfg.Summary == (SummaryConfig{})
	cfg.fillDefaults()
	if addSummary {
		if werr := writeConfig(p, cfg); werr != nil {
			fmt.Fprintf(os.Stderr, "matterbox: could not add summary defaults to %s: %v\n", p, werr)
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
		"#             OpenAI-compatible server; model is sent verbatim; prompt is\n" +
		"#             the system prompt prepended to the chat transcript.\n"
	return os.WriteFile(p, append([]byte(header), body...), 0o644)
}
