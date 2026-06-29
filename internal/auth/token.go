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

// TokenPath returns the path of the saved token file. Exposed so the
// `login` command can show the user where the token lives.
func TokenPath() (string, error) {
	return tokenPath()
}

// HasToken reports whether a usable (non-empty) session token is saved,
// without ReadToken's descriptive "run login first" error. The root command
// uses it to detect a first run and drop the user into the setup wizard.
func HasToken() bool {
	tok, err := ReadToken()
	return err == nil && tok != ""
}

// ReadToken loads the saved Mattermost session token written by `matterbox login`.
func ReadToken() (string, error) {
	p, err := tokenPath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no token at %s — run `matterbox login` first", p)
		}
		return "", fmt.Errorf("read token file: %w", err)
	}
	var tf tokenFile
	if err := json.Unmarshal(b, &tf); err != nil {
		return "", fmt.Errorf("parse token file: %w", err)
	}
	if tf.Token == "" {
		return "", fmt.Errorf("token file %s has empty token — run `matterbox login`", p)
	}
	return tf.Token, nil
}

// SaveToken writes the Mattermost session token to the token file,
// creating the matterbox config dir if needed. Any existing token is
// overwritten. The file is written 0600 since it holds a credential.
func SaveToken(token string) error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	b, err := json.MarshalIndent(tokenFile{Token: token}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	return nil
}

// ClearToken removes the saved token file. A missing file is not an error.
func ClearToken() error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove token file: %w", err)
	}
	return nil
}
