package gitlab

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// TokenFromGlab returns the token the glab CLI stored for host in its config
// (~/.config/glab-cli/config.yml, or $GLAB_CONFIG_DIR/config.yml), or "" if
// there's no config, no entry for host, or it can't be read. It lets the panel
// reuse an existing glab login when gitlab.token isn't set in matterbox config.
func TokenFromGlab(host string) string {
	if host == "" {
		return ""
	}
	path := glabConfigPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		Hosts map[string]struct {
			Token string `yaml:"token"`
		} `yaml:"hosts"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.Hosts[host].Token
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
