package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// DirEnv is the environment variable that relocates the whole matterbox
// directory. It names that directory verbatim — nothing is appended.
const DirEnv = "MATTERBOX_CONFIG_DIR"

// Dir returns the directory holding everything matterbox keeps on disk:
// config.yaml, the saved token, the message cache, the stats files and the
// TUI's control socket.
//
// DirEnv wins when set. It is the one platform-independent knob for pointing
// matterbox somewhere else: os.UserConfigDir reads a different variable per
// platform (XDG_CONFIG_HOME on Linux, HOME on macOS), so tests that want an
// isolated directory would otherwise have to know which OS they run on. It
// also doubles as a way to run a second profile against another server.
func Dir() (string, error) {
	if d := os.Getenv(DirEnv); d != "" {
		return d, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("locate config dir: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "matterbox"), nil
}

// File returns the path of name inside Dir. name may be a nested path
// ("emoji/aliases.json"); nothing is created.
func File(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}
