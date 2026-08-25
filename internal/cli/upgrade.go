package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"matterbox/internal/update"
)

// installerURL is the same script the website hands out, and the same one the
// documented one-liner runs. Hardcoded rather than taken from the endpoint's
// `install` field: an endpoint that could name the script to execute would be a
// way to point this at another host entirely, and there is no reason to give it
// one.
const installerURL = "https://matterbox.work/install.sh"

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
		Long: "Replace this binary with the current release, by running the same installer\n" +
			"the website hands out (https://matterbox.work/install.sh).\n\n" +
			"How it rebuilds matters, so it is worked out rather than guessed: a binary\n" +
			"compiled with optional features — inline video, the --demo soundtrack — is\n" +
			"rebuilt from source so it keeps them, and one without is replaced by the\n" +
			"release binary. `matterbox --version` prints which of the two you have.\n\n" +
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
		return fmt.Errorf("upgrade runs the shell installer, which this platform has no equivalent of.\n" +
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

	args, err := installerArgs(stamp, o)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "installing %s\n", target)
	script, cleanup, err := fetchInstaller(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	return runInstaller(ctx, script, args)
}

// installerArgs works out what to tell the installer. The mode is the decision
// worth getting right: the release binaries are pure Go and carry neither
// optional feature, so replacing a build that has them with one that does not
// would silently take away inline video and the --demo soundtrack. The build
// itself is the only thing that knows, and it recorded it.
func installerArgs(stamp buildStamp, o upgradeOpts) ([]string, error) {
	args := []string{}
	switch {
	case o.source:
		args = append(args, "--source")
	case o.prebuilt:
		args = append(args, "--prebuilt")
	case stamp.tags != "":
		args = append(args, "--source")
	default:
		args = append(args, "--prebuilt")
	}
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
func fetchInstaller(ctx context.Context) (path string, cleanup func(), err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, installerURL, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", "matterbox")
	res, err := http.DefaultClient.Do(req)
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
	if _, err := io.Copy(f, io.LimitReader(res.Body, 1<<20)); err != nil {
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
