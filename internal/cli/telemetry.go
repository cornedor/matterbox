package cli

import (
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"matterbox/internal/config"
	"matterbox/internal/telemetry"
)

// Telemetry for the non-interactive surface.
//
// There are twenty-odd subcommands and no idea which of them anyone runs.
// Several exist only to be called from scripts and notification handlers —
// `matterbox react`, `matterbox mark-read`, `matterbox reply` are invoked by the
// desktop-notification helper, where nobody would ever notice a regression, let
// alone report one. A verb could have been broken for months. So this is the
// cheapest high-value event in the catalogue: which verb, did it work, how long
// did it take, and was a person or a script running it.
//
// It costs a one-shot command two things, both deliberately bounded: a config
// stat-and-read after the work is done, and a flush capped at 700ms
// (flushBudgetCLI). A scripted send must not get slower because of telemetry
// nobody asked for, which is why the CLI mode gets its own budget and no
// snapshot goroutine.

// reportCommand records the outcome of a subcommand. Called from Execute once
// the command has finished, so nothing here is on the critical path of the work
// itself.
//
// Telemetry is started here rather than in each subcommand because consent
// lives in the config and most subcommands have no reason to load it before
// doing their job. Started after the fact, used once, closed.
func reportCommand(cmd *cobra.Command, started time.Time, runErr error) {
	if cmd == nil {
		return
	}
	name := cmd.Name()
	if !telemetry.KnownCLICommand(name) {
		// A verb the catalogue doesn't know: TestCLICommandsMatchRegistry should
		// have caught it, so rather than sending something that would be dropped
		// anyway, say nothing.
		return
	}

	if !startCLITelemetry() {
		return
	}
	defer telemetry.Close()

	outcome, class := classify(runErr)
	telemetry.CLICommand(name, outcome, time.Since(started), isTTY(), class)
	// A cancelled command classifies as no failure at all, and an empty class
	// would be dropped by the catalogue anyway.
	if runErr != nil && class != "" {
		// The same failure as a reliability signal, and — for the classes that
		// mean our code rather than the network — as an error-tracking issue.
		// A subcommand is the surface where this matters most: several are only
		// ever run by scripts, so a verb that has been broken for months has
		// nobody to notice it.
		telemetry.OperationFailed(telemetry.Failure{
			Where:       "cli.other",
			Class:       class,
			Retried:     false,
			UserVisible: true,
			Err:         runErr,
		})
	}
}

// reportPanic reports a panic escaping a subcommand, then flushes, because the
// process is about to die and a queued event dies with it. Called from the
// deferred handler in Execute while the panic is still unwinding, so the frames
// are the ones that led to it.
//
// The verb is unknown here — ExecuteC never returned, so there is nothing to
// name it with — which is exactly what the `cli.other` label is for.
func reportPanic(v any) {
	if !startCLITelemetry() {
		return
	}
	telemetry.ReportPanic("cli.other", v)
	telemetry.Close()
}

// startCLITelemetry brings telemetry up for a one-shot subcommand, and reports
// whether it is actually running. Consent is checked before the config is even
// read for it, and the build stamp is only read when there is something to
// attach it to — `matterbox decode` should not pay for a report nobody asked
// for.
func startCLITelemetry() bool {
	cfg := loadConfigIfPresent()
	if cfg == nil || !cfg.TelemetryEnabled() {
		return false
	}
	// ReleasePending rather than StartMode: a subcommand's own events (a failed
	// login, a search it ran) happened before this point and were held in
	// memory, since there was no client to take them. Opening one replays them
	// — and without consent it discards them instead. See telemetry/pending.go.
	telemetry.ReleasePending(cfg, telemetry.ModeCLI)
	if !telemetry.Enabled() {
		return false
	}
	stamp := readBuildStamp()
	telemetry.SetBuild(versionName(stamp), stamp.tags)
	return true
}

// loadConfigIfPresent reads the config only when it already exists. config.Load
// writes a default file when there isn't one, and a telemetry check is no reason
// to create a config for someone running `matterbox decode` in a container —
// nor to change what a subcommand leaves behind on disk.
func loadConfigIfPresent() *config.Config {
	path, err := config.Path()
	if err != nil {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	return cfg
}

// isTTY reports whether stdout is a terminal, which is how a person running a
// command by hand is told apart from a script or a notification handler running
// it. Worth knowing: the two want different things from the same verb, and a
// verb only ever called by scripts should not be optimised for readability.
func isTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// classify maps an error onto the catalogue's outcome and error class. The
// work lives in the telemetry package because the TUI needs the same mapping
// for the failures it reports, and two copies of a table like this drift.
func classify(err error) (outcome, class string) { return telemetry.Classify(err) }
