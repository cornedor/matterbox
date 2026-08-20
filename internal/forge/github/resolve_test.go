package github

import (
	"os"
	"path/filepath"
	"testing"

	"matterbox/internal/config"
	"matterbox/internal/githubauth"
)

func TestResolveTokenPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", dir)
	t.Setenv(config.DirEnv, dir)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")

	host := "github.com"
	if err := githubauth.SaveTokenForHost(host, "gho_oauth", "alice"); err != nil {
		t.Fatal(err)
	}

	tok, src := ResolveToken("", host)
	if tok != "gho_oauth" || src != "matterbox OAuth (matterbox github login)" {
		t.Fatalf("want oauth fallback, got %q (%s)", tok, src)
	}

	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("github.com:\n  oauth_token: gho_cli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, src = ResolveToken("", host)
	if tok != "gho_cli" || src != "gh CLI (~/.config/gh/hosts.yml)" {
		t.Fatalf("want gh CLI over oauth, got %q (%s)", tok, src)
	}

	tok, src = ResolveToken("ghp_cfg", host)
	if tok != "ghp_cfg" || src != "config github.token" {
		t.Fatalf("want config, got %q (%s)", tok, src)
	}

	t.Setenv("GH_TOKEN", "gho_env")
	tok, src = ResolveToken("ghp_cfg", host)
	if tok != "gho_env" || src != "GH_TOKEN" {
		t.Fatalf("want GH_TOKEN to override config, got %q (%s)", tok, src)
	}

	t.Setenv("GITHUB_TOKEN", "gho_primary")
	tok, src = ResolveToken("ghp_cfg", host)
	if tok != "gho_primary" || src != "GITHUB_TOKEN" {
		t.Fatalf("want GITHUB_TOKEN first, got %q (%s)", tok, src)
	}
}
