package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"matterbox/internal/update"
)

// installerURL is the same script the website hands out, and the same one the
// documented one-liner runs. Hardcoded rather than taken from the endpoint's
// `install` field: an endpoint that could name the script to execute would be a
// way to point this at another host entirely, and there is no reason to give it
// one.
// A var only so a test can point it at a local server; nothing reads it from
// the config or the environment.
var installerURL = "https://matterbox.work/install.sh"

const (
	// installerTimeout bounds the download. It is a few kilobytes of shell from
	// a CDN; the install itself is unbounded and runs after this, so a generous
	// deadline here costs nothing and a missing one means a hung command.
	installerTimeout = 30 * time.Second
	// installerMaxBytes is a ceiling no version of our installer comes near.
	installerMaxBytes = 1 << 20
)

func newUpgradeCmd() *cobra.Command {
	var (
		checkOnly bool
		source    bool
		prebuilt  bool
		version   string
		dir       string
	)
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Install the latest matterbox over this one",
		Long: "Replace this binary with the current release.\n\n" +
			"A release binary is downloaded, checked against the checksums published\n" +
			"with it, and moved into place — nothing that arrives over the network is\n" +
			"run before its checksum matches. Building from source instead runs the\n" +
			"installer the website hands out (https://matterbox.work/install.sh).\n\n" +
			"Which of the two happens matters, so it is worked out rather than guessed:\n" +
			"the release binaries carry inline video, so a build with that is simply\n" +
			"replaced by one. A build with the --demo soundtrack is rebuilt from source,\n" +
			"because no release has it. `matterbox --version` prints which you have.\n\n" +
			"It installs next to the binary it replaces, whatever `--dir` that took.\n\n" +
			"  matterbox upgrade\n" +
			"  matterbox upgrade --check          # say what is current, change nothing\n" +
			"  matterbox upgrade --version v1.0.0 # a specific release, including older",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if source && prebuilt {
				return fmt.Errorf("--source and --prebuilt ask for different things; pass one")
			}
			return runUpgrade(cmd.Context(), cmd.OutOrStdout(), upgradeOpts{
				checkOnly: checkOnly,
				source:    source,
				prebuilt:  prebuilt,
				version:   version,
				dir:       dir,
			})
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "report what is current and exit without installing")
	cmd.Flags().BoolVar(&source, "source", false, "build from source rather than deciding by build tags")
	cmd.Flags().BoolVar(&prebuilt, "prebuilt", false, "download the release binary rather than deciding by build tags")
	cmd.Flags().StringVar(&version, "version", "", "install this release instead of the latest (e.g. v1.0.0)")
	cmd.Flags().StringVar(&dir, "dir", "", "install here instead of alongside the current binary")
	return cmd
}

type upgradeOpts struct {
	checkOnly bool
	source    bool
	prebuilt  bool
	version   string
	dir       string
}

func runUpgrade(ctx context.Context, out io.Writer, o upgradeOpts) error {
	if runtime.GOOS == "windows" {
		// No windows release is built (see the matrix in release.yml), and the
		// source path is a shell script, so there is nothing here to offer.
		return fmt.Errorf("upgrade has no path on this platform.\n" +
			"    The releases are at https://github.com/cornedor/matterbox/releases")
	}
	stamp := readBuildStamp()
	current := versionName(stamp)

	// Asked for now, by a person, about this machine — so the daily interval and
	// the config's off switch are both beside the point. See update.Force.
	rel, err := update.Force(ctx)
	if err != nil && o.version == "" {
		// A pinned version does not need the endpoint to have answered. Every
		// other path does: there is nothing to install without it.
		return fmt.Errorf("could not work out the latest release: %w", err)
	}

	// Where this build stands. Nothing here claims an install — that is said
	// after the --check gate below, so a check can never announce something it
	// is not going to do.
	switch {
	case rel == nil:
		fmt.Fprintf(out, "could not reach %s; you have %s\n", update.Endpoint, current)
	case !update.Comparable(current):
		// A build that names no release — `go build` from a working tree, an
		// install from a branch — cannot be compared against anything. The
		// automatic check stays quiet about those; this is not the automatic
		// check, and it can still replace one.
		fmt.Fprintf(out, "%s is the latest release; this build is %s and names none\n", rel.Version, current)
	case update.Newer(current, rel.Version):
		fmt.Fprintf(out, "%s is out — you have %s\n", rel.Version, current)
	case o.version == "":
		// Current, and nothing else was asked for: the one case with no work in
		// it at all.
		fmt.Fprintf(out, "%s is the latest release; you have %s\n", rel.Version, current)
		if o.checkOnly {
			fmt.Fprintln(out, rel.URL)
		} else {
			fmt.Fprintln(out, "nothing to do")
		}
		return nil
	default:
		fmt.Fprintf(out, "%s is the latest release; you have %s\n", rel.Version, current)
	}

	target := o.version
	if target == "" {
		target = rel.Version
	}
	if o.checkOnly {
		if o.version != "" {
			fmt.Fprintf(out, "--version %s would be installed\n", o.version)
		} else if rel != nil {
			fmt.Fprintln(out, rel.URL)
		}
		return nil
	}

	fmt.Fprintf(out, "installing %s\n", target)

	// A release binary is fetched and verified here rather than by a script:
	// what comes down the wire is a tarball checked against the checksums
	// published with it, and nothing downloaded is executed before that check
	// passes. The script is still the right tool for the source path, which is
	// a compile — it works out what this machine can build and says which
	// package would fix what it cannot.
	if !fromSource(stamp, o) {
		dst, err := installTarget(o.dir)
		if err != nil {
			return err
		}
		return installPrebuilt(ctx, out, target, dst)
	}

	args, err := installerArgs(o)
	if err != nil {
		return err
	}
	script, cleanup, err := fetchInstaller(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	return runInstaller(ctx, script, args)
}

// releaseTags is what the prebuilt release binaries are compiled with — keep it
// in step with the build step in .github/workflows/release.yml. netgo and
// osusergo are in there because the Linux releases are linked static; they are
// not features anyone could notice losing, but they are tags, and a build that
// carries them must not be sent down the source path for it.
var releaseTags = map[string]bool{
	"video":    true,
	"netgo":    true,
	"osusergo": true,
}

// keptByRelease reports whether a release binary would still have everything
// this build was compiled with.
func keptByRelease(tags string) bool {
	for _, t := range strings.Split(tags, ",") {
		if t = strings.TrimSpace(t); t != "" && !releaseTags[t] {
			return false
		}
	}
	return true
}

// fromSource is the decision worth getting right: a build carrying an optional
// feature the release binaries don't have must be rebuilt from source, or the
// upgrade would silently take that feature away. Today that means the --demo
// soundtrack — the releases carry inline video, so a video build can simply
// take one. The build itself is the only thing that knows what it has, and it
// recorded it.
func fromSource(stamp buildStamp, o upgradeOpts) bool {
	switch {
	case o.source:
		return true
	case o.prebuilt:
		return false
	default:
		return !keptByRelease(stamp.tags)
	}
}

// installerArgs is what the installer script is told on the source path. Only
// the source path: taking a release binary no longer goes through a script at
// all (see installPrebuilt), so there is no --prebuilt to pass any more.
func installerArgs(o upgradeOpts) ([]string, error) {
	args := []string{"--source"}
	if o.version != "" {
		args = append(args, "--version", o.version)
	}
	dir := o.dir
	if dir == "" {
		var err error
		if dir, err = installDir(); err != nil {
			return nil, err
		}
	}
	return append(args, "--dir", dir), nil
}

// installDir is the directory holding the binary being replaced, so an upgrade
// lands where the thing it replaces already is — which is on the PATH, whatever
// --dir the original install used. Symlinks are resolved: a matterbox reached
// through one should be replaced where it actually lives, not where the link
// sits.
func installDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// fetchInstaller downloads the script to a file rather than piping it into a
// shell. Same script and same trust — HTTPS to our own domain — but a partial
// download becomes a shell syntax error on a file nobody ran, instead of half
// an install.
//
// That trust is the whole of it, so the transport is held to it: the request is
// bounded in time and size, and a redirect off the origin installerURL names —
// another host, or plain http — is refused rather than followed. Without that,
// the set of parties who can hand this machine a shell script is everyone who
// can answer for any host we could be pointed at, which is a good deal larger
// than the one we meant to trust.
func fetchInstaller(ctx context.Context) (path string, cleanup func(), err error) {
	origin, err := url.Parse(installerURL)
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, installerURL, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", "matterbox")
	res, err := installerClient(origin).Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("could not download the installer: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("could not download the installer: %s: %s", installerURL, res.Status)
	}

	f, err := os.CreateTemp("", "matterbox-install-*.sh")
	if err != nil {
		return "", nil, err
	}
	remove := func() { os.Remove(f.Name()) }
	// One byte past the cap, so a body that overruns it is told apart from one
	// that ends exactly on it. A truncated script is a script that would run
	// half an install.
	n, err := io.Copy(f, io.LimitReader(res.Body, installerMaxBytes+1))
	if err == nil && n > installerMaxBytes {
		err = fmt.Errorf("%s is larger than %d bytes; refusing to run it", installerURL, installerMaxBytes)
	}
	if err != nil {
		f.Close()
		remove()
		return "", nil, fmt.Errorf("could not download the installer: %w", err)
	}
	if err := f.Close(); err != nil {
		remove()
		return "", nil, err
	}
	return f.Name(), remove, nil
}

// installerClient bounds the download and pins it to the origin installerURL
// names. The redirect rule is the part that matters: matterbox.work is a
// hostname we can be sure of, and following a redirect away from it would hand
// that decision to whoever answered.
func installerClient(origin *url.URL) *http.Client {
	return &http.Client{
		Timeout: installerTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != origin.Scheme || req.URL.Host != origin.Host {
				return fmt.Errorf("refusing a redirect from %s to %s://%s",
					installerURL, req.URL.Scheme, req.URL.Host)
			}
			if len(via) >= 5 {
				return fmt.Errorf("%s redirected too many times", installerURL)
			}
			return nil
		},
	}
}

// runInstaller hands the terminal to the script. Its output is the point — it
// says which optional features this machine can compile and which package would
// fix the ones it cannot — so it is inherited rather than captured.
func runInstaller(ctx context.Context, script string, args []string) error {
	cmd := exec.CommandContext(ctx, "sh", append([]string{script}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the installer did not finish: %w", err)
	}
	return nil
}

// printUpdateNotice is the second half of the update notice: the toast in the
// TUI is gone in twenty seconds and cannot be acted on without quitting first,
// so the same fact is said once more here — after the TUI has released the
// terminal, where the command is one paste away.
//
// Silent when stdout is not a terminal: `matterbox > log` is somebody's script,
// and a script has no use for this.
func printUpdateNotice(w io.Writer, current string) {
	rel := update.Pending()
	if rel == nil || !isTTY() {
		return
	}
	fmt.Fprintf(w, "\n  matterbox %s is out — you have %s\n", rel.Version, current)
	fmt.Fprintf(w, "  matterbox upgrade\n\n")
}
