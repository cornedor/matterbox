package githubauth

import (
	"os"
	"testing"

	"matterbox/internal/config"
)

func TestSaveReadClearTokenForHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)

	host := "github.com"
	if err := SaveTokenForHost(host, "gho_secret_token", "octocat"); err != nil {
		t.Fatal(err)
	}
	if !HasTokenForHost(host) {
		t.Fatal("expected token to exist after save")
	}

	tok, user, err := ReadTokenForHost(host)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "gho_secret_token" || user != "octocat" {
		t.Errorf("got token=%q user=%q", tok, user)
	}

	p, err := TokenPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("token file missing: %v", err)
	}

	if err := ClearTokenForHost(host); err != nil {
		t.Fatal(err)
	}
	if HasTokenForHost(host) {
		t.Error("token should be cleared")
	}
}

func TestReadTokenForHostMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.DirEnv, dir)

	_, _, err := ReadTokenForHost("github.com")
	if err == nil {
		t.Error("expected error when no token saved")
	}
}

func TestHostFromURL(t *testing.T) {
	if got := HostFromURL("https://github.com"); got != "github.com" {
		t.Errorf("HostFromURL = %q", got)
	}
	if got := HostFromURL("https://ghe.example.com"); got != "ghe.example.com" {
		t.Errorf("HostFromURL = %q", got)
	}
}

func TestAPIBaseFromWebBase(t *testing.T) {
	base, err := APIBaseFromWebBase("https://github.com")
	if err != nil || base != "https://api.github.com" {
		t.Errorf("github.com api base = %q, err = %v", base, err)
	}
	base, err = APIBaseFromWebBase("github.com")
	if err != nil || base != "https://api.github.com" {
		t.Errorf("scheme-less api base = %q, err = %v", base, err)
	}
	base, err = APIBaseFromWebBase("https://ghe.example.com")
	if err != nil || base != "https://ghe.example.com/api/v3" {
		t.Errorf("GHE api base = %q, err = %v", base, err)
	}
	base, err = APIBaseFromWebBase("https://ghe.example.com/github")
	if err != nil || base != "https://ghe.example.com/api/v3" {
		t.Errorf("GHE with path api base = %q, err = %v", base, err)
	}
}
