package github

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// tokenLookupTimeout bounds the `gh auth token` fallback. It runs at most once,
// during startup, and only when no token is configured — but a hung keyring
// prompt must not hold the TUI hostage.
const tokenLookupTimeout = 2 * time.Second

// TokenFromGH returns the token the gh CLI holds for host, or "" if there is
// none. It lets the panel reuse an existing `gh auth login` when github.token
// isn't set in matterbox config — the counterpart of gitlab's TokenFromGlab.
//
// gh stores the token one of two ways: in ~/.config/gh/hosts.yml when the system
// has no keyring (or the user asked for insecure storage), otherwise in the OS
// keyring, where only gh itself can read it — hence the subprocess. hosts.yml is
// read first either way, because it also answers the cheaper question of whether
// this host is logged in at all: with no entry there is nothing to ask gh about,
// so the common "no gh login" case costs one failed stat rather than a fork on
// every startup.
func TokenFromGH(host string) string {
	if host == "" {
		return ""
	}
	tok, loggedIn := tokenFromHostsFile(host)
	switch {
	case tok != "":
		return tok
	case loggedIn:
		return tokenFromGHCLI(host)
	}
	return ""
}

// tokenFromHostsFile reads gh's hosts.yml, honoring $GH_CONFIG_DIR. It returns
// the host's oauth_token when the file carries one, and whether the host has an
// entry at all — a keyring login leaves an entry with no token.
func tokenFromHostsFile(host string) (token string, loggedIn bool) {
	path := ghHostsPath()
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var cfg map[string]struct {
		OAuthToken string `yaml:"oauth_token"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", false
	}
	entry, ok := cfg[host]
	if !ok {
		return "", false
	}
	return strings.TrimSpace(entry.OAuthToken), true
}

// ghHostsPath resolves gh's hosts file location, honoring $GH_CONFIG_DIR and
// falling back to ~/.config/gh.
func ghHostsPath() string {
	if dir := os.Getenv("GH_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "hosts.yml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "gh", "hosts.yml")
}

// tokenFromGHCLI asks the gh binary for the token it holds for host — the only
// way to reach a keyring-stored login. Silent on every failure (no gh on PATH,
// not logged in, no keyring access): the caller just goes without a token.
func tokenFromGHCLI(host string) string {
	bin, err := exec.LookPath("gh")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), tokenLookupTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "auth", "token", "--hostname", host).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
