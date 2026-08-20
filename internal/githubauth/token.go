// Package githubauth stores and loads OAuth access tokens for GitHub
// integrations in matterbox, separate from Mattermost auth.
package githubauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"matterbox/internal/config"
)

type hostToken struct {
	Token   string    `json:"token"`
	User    string    `json:"user,omitempty"`
	SavedAt time.Time `json:"saved_at,omitempty"`
	// Future fields can hold auth metadata (e.g. scopes) without breaking
	// backward compatibility.
}

type tokenFile struct {
	Hosts map[string]hostToken `json:"hosts"`
}

func tokenPath() (string, error) {
	return config.File("gh_token.json")
}

// TokenPath returns the path of the saved GitHub token file.
func TokenPath() (string, error) {
	return tokenPath()
}

// ReadTokenForHost loads the stored OAuth token for host.
// host is the GitHub instance hostname (e.g. github.com).
func ReadTokenForHost(host string) (token string, user string, err error) {
	if host == "" {
		return "", "", errors.New("githubauth: empty host")
	}
	p, err := tokenPath()
	if err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("no token at %s", p)
		}
		return "", "", fmt.Errorf("read token file: %w", err)
	}

	var tf tokenFile
	if err := json.Unmarshal(b, &tf); err != nil {
		return "", "", fmt.Errorf("parse token file: %w", err)
	}
	if tf.Hosts == nil {
		return "", "", fmt.Errorf("token file %s has no hosts", p)
	}
	ht, ok := tf.Hosts[host]
	if !ok || ht.Token == "" {
		return "", "", fmt.Errorf("no token for host %s", host)
	}
	return ht.Token, ht.User, nil
}

// HasTokenForHost reports whether there's a saved (non-empty) token for host.
func HasTokenForHost(host string) bool {
	_, _, err := ReadTokenForHost(host)
	return err == nil
}

// SaveTokenForHost writes token for host, storing host tokens in a single
// JSON file with 0600 permissions.
func SaveTokenForHost(host, token, user string) error {
	if host == "" {
		return errors.New("githubauth: empty host")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("githubauth: empty token")
	}

	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	var tf tokenFile
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &tf) // best-effort: overwrite on any parse errors
	}
	if tf.Hosts == nil {
		tf.Hosts = map[string]hostToken{}
	}
	tf.Hosts[host] = hostToken{
		Token:   token,
		User:    user,
		SavedAt: time.Now().UTC(),
	}

	b, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	return nil
}

// ClearTokenForHost removes the saved token for host.
// If the file becomes empty afterwards, it is left as-is (simpler than
// rewriting/removing) since it still holds only a per-user credential.
func ClearTokenForHost(host string) error {
	if host == "" {
		return errors.New("githubauth: empty host")
	}
	p, err := tokenPath()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read token file: %w", err)
	}
	var tf tokenFile
	if err := json.Unmarshal(b, &tf); err != nil {
		return fmt.Errorf("parse token file: %w", err)
	}
	if tf.Hosts != nil {
		delete(tf.Hosts, host)
	}
	nb, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(nb, '\n'), 0o600)
}
