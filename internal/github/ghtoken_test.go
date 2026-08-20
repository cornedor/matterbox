package github

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenFromGhHostLevel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", dir)
	path := filepath.Join(dir, "hosts.yml")
	if err := os.WriteFile(path, []byte("github.com:\n  oauth_token: gho_host\n  user: alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := TokenFromGh("github.com"); got != "gho_host" {
		t.Fatalf("TokenFromGh = %q, want gho_host", got)
	}
	if got := TokenFromGh("GitHub.COM"); got != "gho_host" {
		t.Fatalf("case-insensitive host: got %q", got)
	}
	if got := TokenFromGh("ghe.example.com"); got != "" {
		t.Fatalf("missing host = %q, want empty", got)
	}
}

func TestTokenFromGhActiveUser(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", dir)
	content := `github.com:
  user: bob
  users:
    alice:
      oauth_token: gho_alice
    bob:
      oauth_token: gho_bob
`
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := TokenFromGh("github.com"); got != "gho_bob" {
		t.Fatalf("TokenFromGh = %q, want gho_bob", got)
	}
}

func TestTokenFromGhMissingFile(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	if got := TokenFromGh("github.com"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
