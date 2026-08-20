package github

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// TokenFromGh returns the token the GitHub CLI (gh) stored for host in its
// config (~/.config/gh/hosts.yml, or $GH_CONFIG_DIR/hosts.yml), or "" if
// there's no config, no entry for host, or it can't be read. It lets the panel
// reuse an existing `gh auth login` when github.token isn't set — the same
// pattern GitLab uses with glab.
func TokenFromGh(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	path := ghHostsPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// hosts.yml is a map of hostname → host entry. Fields vary by gh version:
	// older/single-account puts oauth_token on the host; multi-account keeps
	// tokens under users.<login> and points user at the active login.
	var cfg map[string]struct {
		OAuthToken string `yaml:"oauth_token"`
		User       string `yaml:"user"`
		Users      map[string]struct {
			OAuthToken string `yaml:"oauth_token"`
		} `yaml:"users"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	entry, ok := cfg[host]
	if !ok {
		return ""
	}
	if tok := strings.TrimSpace(entry.OAuthToken); tok != "" {
		return tok
	}
	if entry.User != "" {
		if u, ok := entry.Users[entry.User]; ok {
			return strings.TrimSpace(u.OAuthToken)
		}
	}
	for _, u := range entry.Users {
		if tok := strings.TrimSpace(u.OAuthToken); tok != "" {
			return tok
		}
	}
	return ""
}

// ghHostsPath resolves gh's hosts.yml location, honoring $GH_CONFIG_DIR and
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
