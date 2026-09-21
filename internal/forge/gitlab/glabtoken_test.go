package gitlab

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeGlabConfig drops a glab config.yml into a temp config dir and points
// $GLAB_CONFIG_DIR at it.
func writeGlabConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GLAB_CONFIG_DIR", dir)
}

func TestTokenFromGlabConfigFile(t *testing.T) {
	writeGlabConfig(t, "hosts:\n    git.example.com:\n        token: glpat-secret\n        user: ada\n")
	if got := TokenFromGlab("git.example.com"); got != "glpat-secret" {
		t.Errorf("TokenFromGlab = %q, want the token from config.yml", got)
	}
}

func TestTokenFromGlabUnknownHostAsksNothing(t *testing.T) {
	writeGlabConfig(t, "hosts:\n    git.example.com:\n        token: glpat-secret\n")
	if got := TokenFromGlab("git.other.com"); got != "" {
		t.Errorf("TokenFromGlab for an unconfigured host = %q, want empty", got)
	}
	if got := TokenFromGlab(""); got != "" {
		t.Errorf(`TokenFromGlab("") = %q, want empty`, got)
	}
}

func TestTokenFromGlabMissingConfig(t *testing.T) {
	t.Setenv("GLAB_CONFIG_DIR", filepath.Join(t.TempDir(), "absent"))
	if got := TokenFromGlab("git.example.com"); got != "" {
		t.Errorf("TokenFromGlab with no glab config = %q, want empty", got)
	}
}

// A keyring login leaves an entry with no token, which is what sends us to the
// CLI. Verified end to end with a fake glab on PATH, since that subprocess is
// the whole point of the fallback.
func TestTokenFromGlabKeyringLoginAsksTheCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub")
	}
	writeGlabConfig(t, "hosts:\n    git.example.com:\n        user: ada\n        use_keyring: \"true\"\n")

	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"# Mirrors glab: the status report goes to stderr.\n" +
		"echo 'git.example.com' >&2\n" +
		"echo '  ✓ Logged in to git.example.com as ada (keyring)' >&2\n" +
		"echo '  ✓ Token found in operating system keyring: glpat-from-keyring' >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got := TokenFromGlab("git.example.com"); got != "glpat-from-keyring" {
		t.Errorf("TokenFromGlab = %q, want the keyring token glab reported", got)
	}
}

func TestParseGlabStatusToken(t *testing.T) {
	cases := []struct {
		name, out, want string
	}{
		{"keyring", "  ✓ Token found in operating system keyring: glpat-abc\n", "glpat-abc"},
		{"config file", "  ✓ Token found in configuration file: glpat-def\n", "glpat-def"},
		{"styled", "  \x1b[32m✓\x1b[0m Token found in keyring: \x1b[1mglpat-ghi\x1b[0m\n", "glpat-ghi"},
		// Without --show-token glab masks it; a mask is not a token.
		{"masked", "  ✓ Token found in operating system keyring: **************\n", ""},
		{"no token line", "  ✓ Logged in to git.example.com as ada\n", ""},
		{"nothing after the colon", "  ✓ Token found in keyring: \n", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseGlabStatusToken(c.out); got != c.want {
				t.Errorf("parseGlabStatusToken = %q, want %q", got, c.want)
			}
		})
	}
}
