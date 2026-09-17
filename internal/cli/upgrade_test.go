package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matterbox/internal/update"
)

// TestFromSource pins the decision that actually matters here: a build carrying
// an optional feature the release binaries don't have must be rebuilt from
// source, or the upgrade would silently take that feature away. The releases
// carry video, so only demoaudio forces the source path now — get that
// backwards in either direction and someone loses their soundtrack or spends
// ten minutes compiling for nothing.
func TestFromSource(t *testing.T) {
	cases := []struct {
		name string
		tags string
		opts upgradeOpts
		want bool
	}{
		{"a build with the soundtrack rebuilds from source", "demoaudio,video", upgradeOpts{}, true},
		{"demoaudio alone is enough to force source", "demoaudio", upgradeOpts{}, true},
		{"a video build takes the release binary, which has video too", "video", upgradeOpts{}, false},
		{"the static release tags are not features to preserve", "video,netgo,osusergo", upgradeOpts{}, false},
		{"a plain build takes the release binary", "", upgradeOpts{}, false},
		{"--source overrides the tags", "", upgradeOpts{source: true}, true},
		{"--prebuilt overrides the tags", "demoaudio", upgradeOpts{prebuilt: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fromSource(buildStamp{tags: c.tags}, c.opts); got != c.want {
				t.Errorf("fromSource = %v, want %v", got, c.want)
			}
		})
	}
}

// The source path is the only one that still runs the script, so that is all it
// ever asks for — plus the pinned version, which is the user's own input.
func TestInstallerArgsAsksForASourceBuild(t *testing.T) {
	got, err := installerArgs(upgradeOpts{version: "v1.0.0", dir: "/opt/bin"})
	if err != nil {
		t.Fatalf("installerArgs: %v", err)
	}
	want := []string{"--source", "--version", "v1.0.0", "--dir", "/opt/bin"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("installerArgs = %v, want %v", got, want)
	}
}

// With no --dir the upgrade must land where the binary it replaces already is,
// which is what keeps it on the PATH.
func TestInstallerArgsDefaultsToTheRunningBinarysDirectory(t *testing.T) {
	got, err := installerArgs(upgradeOpts{})
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

// TestFetchInstallerRefusesARedirectOffTheOrigin is the one that matters: the
// only thing standing between this command and running someone else's shell
// script is that the script comes from matterbox.work over HTTPS. Follow a
// redirect elsewhere and that guarantee is gone.
func TestFetchInstallerRefusesARedirectOffTheOrigin(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "#!/bin/sh\nrm -rf /")
	}))
	defer elsewhere.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/install.sh", http.StatusFound)
	}))
	defer origin.Close()

	withInstallerURL(t, origin.URL+"/install.sh")
	path, cleanup, err := fetchInstaller(context.Background())
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatalf("followed a redirect to another host and wrote %s", path)
	}
	if !strings.Contains(err.Error(), "refusing a redirect") {
		t.Fatalf("error does not say why: %v", err)
	}
}

// TestFetchInstallerFollowsARedirectOnTheOrigin — the rule is about leaving the
// host, not about redirects. A CDN moving /install.sh must still work.
func TestFetchInstallerFollowsARedirectOnTheOrigin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/real.sh" {
			http.Redirect(w, r, "/real.sh", http.StatusFound)
			return
		}
		fmt.Fprint(w, "#!/bin/sh\nexit 0\n")
	}))
	defer srv.Close()

	withInstallerURL(t, srv.URL+"/install.sh")
	path, cleanup, err := fetchInstaller(context.Background())
	if err != nil {
		t.Fatalf("fetchInstaller: %v", err)
	}
	defer cleanup()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "#!/bin/sh") {
		t.Fatalf("downloaded %q", got)
	}
}

// TestFetchInstallerRefusesAnOversizedBody: silently truncating at the cap
// leaves a script that stops mid-command, which is a half install.
func TestFetchInstallerRefusesAnOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, installerMaxBytes+64))
	}))
	defer srv.Close()

	withInstallerURL(t, srv.URL+"/install.sh")
	_, cleanup, err := fetchInstaller(context.Background())
	if cleanup != nil {
		cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("want a size refusal, got %v", err)
	}
}

func withInstallerURL(t *testing.T, u string) {
	t.Helper()
	prev := installerURL
	installerURL = u
	t.Cleanup(func() { installerURL = prev })
}
