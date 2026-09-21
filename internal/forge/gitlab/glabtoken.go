package gitlab

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"gopkg.in/yaml.v3"
)

// tokenLookupTimeout bounds the `glab auth status` fallback. It runs at most
// once, during startup, and only when no token is configured — but a hung
// keyring prompt must not hold the TUI hostage.
const tokenLookupTimeout = 2 * time.Second

// TokenFromGlab returns the token the glab CLI holds for host, or "" if there
// is none. It lets the panel reuse an existing `glab auth login` when
// gitlab.token isn't set in matterbox config — the counterpart of github's
// TokenFromGH, and it has the same two cases.
//
// glab stores the token either in its config (~/.config/glab-cli/config.yml, or
// $GLAB_CONFIG_DIR/config.yml) or — since it made keyring storage the default —
// in the OS keyring, where only glab itself can read it, hence the subprocess.
// The config is read first either way, because it also answers the cheaper
// question of whether this host is logged in at all: with no entry there is
// nothing to ask glab about, so the common "no glab login" case costs one failed
// stat rather than a fork on every startup.
func TokenFromGlab(host string) string {
	if host == "" {
		return ""
	}
	tok, loggedIn := tokenFromGlabConfig(host)
	switch {
	case tok != "":
		return tok
	case loggedIn:
		return tokenFromGlabCLI(host)
	}
	return ""
}

// tokenFromGlabConfig reads glab's config file, honoring $GLAB_CONFIG_DIR. It
// returns the host's token when the file carries one, and whether the host has
// an entry at all — a keyring login leaves an entry with no token (and
// use_keyring set).
func tokenFromGlabConfig(host string) (token string, loggedIn bool) {
	path := glabConfigPath()
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var cfg struct {
		Hosts map[string]struct {
			Token string `yaml:"token"`
		} `yaml:"hosts"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", false
	}
	entry, ok := cfg.Hosts[host]
	if !ok {
		return "", false
	}
	return strings.TrimSpace(entry.Token), true
}

// glabConfigPath resolves glab's config file location, honoring
// $GLAB_CONFIG_DIR and falling back to ~/.config/glab-cli.
func glabConfigPath() string {
	if dir := os.Getenv("GLAB_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "config.yml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "glab-cli", "config.yml")
}

// tokenFromGlabCLI asks the glab binary for the token it holds for host — the
// only way to reach a keyring-stored login. glab has no `auth token` command
// like gh's, so this reads the one line of `auth status --show-token` that
// carries it ("✓ Token found in operating system keyring: <token>"), which glab
// writes to stderr. Silent on every failure (no glab on PATH, not logged in, no
// keyring access, a phrasing we don't recognise): the caller just goes without a
// token, which is what it did before this fallback existed.
func tokenFromGlabCLI(host string) string {
	bin, err := exec.LookPath("glab")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), tokenLookupTimeout)
	defer cancel()
	// CombinedOutput, not Output: the status report goes to stderr.
	out, err := exec.CommandContext(ctx, bin, "auth", "status", "--hostname", host, "--show-token").CombinedOutput()
	if err != nil {
		return ""
	}
	return parseGlabStatusToken(string(out))
}

// parseGlabStatusToken pulls the token out of `glab auth status --show-token`.
// The line is "<glyph> Token found in <where>: <token>"; anything that is not a
// single bare word after the colon (the masked "****" of a run without
// --show-token, a sentence, an empty tail) is not a token and is refused.
func parseGlabStatusToken(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = ansi.Strip(line)
		if !strings.Contains(line, "Token") {
			continue
		}
		i := strings.LastIndex(line, ": ")
		if i < 0 {
			continue
		}
		tok := strings.TrimSpace(line[i+2:])
		if tok == "" || strings.ContainsAny(tok, " \t*") {
			continue
		}
		return tok
	}
	return ""
}
