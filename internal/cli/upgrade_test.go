package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matterbox/internal/update"
)

// TestInstallerArgs pins the decision that actually matters here: a build with
// optional features compiled in must be rebuilt from source, because the
// release binaries are pure Go and would silently take inline video and the
// --demo soundtrack away.
func TestInstallerArgs(t *testing.T) {
	cases := []struct {
		name string
		tags string
		opts upgradeOpts
		want []string
	}{
		{
			name: "a build with optional features rebuilds from source",
			tags: "demoaudio,video",
			opts: upgradeOpts{dir: "/opt/bin"},
			want: []string{"--source", "--dir", "/opt/bin"},
		},
		{
			name: "a plain build takes the release binary",
			tags: "",
			opts: upgradeOpts{dir: "/opt/bin"},
			want: []string{"--prebuilt", "--dir", "/opt/bin"},
		},
		{
			name: "--source overrides the tags",
			tags: "",
			opts: upgradeOpts{source: true, dir: "/opt/bin"},
			want: []string{"--source", "--dir", "/opt/bin"},
		},
		{
			name: "--prebuilt overrides the tags",
			tags: "video",
			opts: upgradeOpts{prebuilt: true, dir: "/opt/bin"},
			want: []string{"--prebuilt", "--dir", "/opt/bin"},
		},
		{
			name: "a pinned version is passed through",
			tags: "",
			opts: upgradeOpts{version: "v1.0.0", dir: "/opt/bin"},
			want: []string{"--prebuilt", "--version", "v1.0.0", "--dir", "/opt/bin"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := installerArgs(buildStamp{tags: c.tags}, c.opts)
			if err != nil {
				t.Fatalf("installerArgs: %v", err)
			}
			if strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Errorf("installerArgs = %v, want %v", got, c.want)
			}
		})
	}
}

// With no --dir the upgrade must land where the binary it replaces already is,
// which is what keeps it on the PATH.
func TestInstallerArgsDefaultsToTheRunningBinarysDirectory(t *testing.T) {
	got, err := installerArgs(buildStamp{}, upgradeOpts{})
	if err != nil {
		t.Fatalf("installerArgs: %v", err)
	}
	if len(got) < 2 || got[len(got)-2] != "--dir" {
		t.Fatalf("installerArgs = %v, want it to end in --dir <path>", got)
	}
	dir := got[len(got)-1]
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path on this platform")
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if dir != filepath.Dir(exe) {
		t.Errorf("--dir %q, want %q", dir, filepath.Dir(exe))
	}
}

func TestPrintUpdateNoticeSaysNothingWithoutOne(t *testing.T) {
	t.Cleanup(func() { update.SetPending(nil) })
	update.SetPending(nil)

	var b strings.Builder
	printUpdateNotice(&b, "v1.0.0")
	if b.String() != "" {
		t.Errorf("printUpdateNotice wrote %q with no release pending, want nothing", b.String())
	}
}

// The notice is for a person at a prompt. `matterbox > log` is a script, and a
// script has no use for it — so the TTY check is the whole test here, and it is
// why this one only asserts the not-a-terminal case: `go test` never has one.
func TestPrintUpdateNoticeStaysOutOfPipes(t *testing.T) {
	t.Cleanup(func() { update.SetPending(nil) })
	update.SetPending(&update.Release{Version: "v9.9.9"})

	var b strings.Builder
	printUpdateNotice(&b, "v1.0.0")
	if isTTY() {
		t.Skip("stdout is a terminal, so there is nothing to assert")
	}
	if b.String() != "" {
		t.Errorf("printUpdateNotice wrote %q to a pipe, want nothing", b.String())
	}
}
