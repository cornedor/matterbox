package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type tokenFile struct {
	Token string `json:"token"`
}

func tokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("locate config dir: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "matterbox", "mm_token.json"), nil
}

// ReadToken loads the saved Mattermost session token written by mm_login.py.
func ReadToken() (string, error) {
	p, err := tokenPath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no token at %s — run `python mm_login.py` first", p)
		}
		return "", fmt.Errorf("read token file: %w", err)
	}
	var tf tokenFile
	if err := json.Unmarshal(b, &tf); err != nil {
		return "", fmt.Errorf("parse token file: %w", err)
	}
	if tf.Token == "" {
		return "", fmt.Errorf("token file %s has empty token — re-run `python mm_login.py`", p)
	}
	return tf.Token, nil
}
