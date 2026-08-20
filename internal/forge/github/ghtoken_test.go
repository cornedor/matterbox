package github

import (
	"os"
	"path/filepath"
	"testing"
)

// writeHosts drops a gh hosts.yml into a temp config dir and points
// $GH_CONFIG_DIR at it.
func writeHosts(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_CONFIG_DIR", dir)
}

func TestTokenFromHostsFile(t *testing.T) {
	writeHosts(t, "github.com:\n    oauth_token: gho_secret\n    user: ada\n")
	if got := TokenFromGH("github.com"); got != "gho_secret" {
		t.Errorf("TokenFromGH = %q, want the oauth_token from hosts.yml", got)
	}
}

func TestTokenFromGHUnknownHostAsksNothing(t *testing.T) {
	// A host with no entry is not logged in, so there is nothing to read and no
	// reason to shell out to gh.
	writeHosts(t, "github.com:\n    oauth_token: gho_secret\n")
	if got := TokenFromGH("github.example.com"); got != "" {
		t.Errorf("TokenFromGH for an unconfigured host = %q, want empty", got)
	}
	if got := TokenFromGH(""); got != "" {
		t.Errorf("TokenFromGH(\"\") = %q, want empty", got)
	}
}

func TestTokenFromGHMissingConfig(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", filepath.Join(t.TempDir(), "absent"))
	if got := TokenFromGH("github.com"); got != "" {
		t.Errorf("TokenFromGH with no gh config = %q, want empty", got)
	}
}
