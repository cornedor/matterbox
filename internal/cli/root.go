// Package cli is the command-line entry point for matterbox. It owns the
// cobra command tree: the root command launches the interactive TUI (the
// default action, preserving the original `matterbox` behaviour), and the
// `send` / `read` subcommands provide a scriptable, non-interactive mode.
//
// All commands share dial(), which loads config + token and builds the
// Mattermost client. Keeping the command surface in one package means a
// future migration (more verbs, shell completion) touches only here, not
// main.go.
package cli

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"time"

	tea "charm.land/bubbletea/v2"
	emoji "github.com/kyokomi/emoji/v2"
	"github.com/spf13/cobra"

	"matterbox/internal/auth"
	"matterbox/internal/config"
	"matterbox/internal/mm"
	"matterbox/internal/telemetry"
	"matterbox/internal/ui"
)

// pprofAddr backs the root command's --pprof flag. It is only meaningful
// for the TUI launch (the root command's own action), so it stays a local
// flag rather than a persistent one inherited by the subcommands.
var pprofAddr string

// newRootCmd builds the command tree. Errors and usage dumps are silenced
// so Execute can print a single clean "matterbox: <err>" line instead of
// cobra's default two-line "Error:\n<usage>" output.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "matterbox",
		Short: "A terminal client for Mattermost",
		Long: "matterbox is a TUI Mattermost client.\n\n" +
			"Run with no arguments to open the interactive UI — on a first run (no saved\n" +
			"login) it opens the `welcome` setup wizard first, so a fresh install is usable\n" +
			"out of the box. Or use a subcommand (login, send, reply, react, read, unread,\n" +
			"mark-read, open, search, channels, digest, whoami, embed, listen, rules,\n" +
			"keys, decode) to work with messages non-interactively for scripting or to run\n" +
			"the background sync/notification daemon (listen).",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       versionBlock(),
		// No subcommand → open the interactive UI. On a first run (no saved
		// token) run the setup wizard first, then continue into the TUI with
		// the login it just created, so new users never hit a "run welcome"
		// error out of the box.
		RunE: func(cmd *cobra.Command, args []string) error {
			if !auth.HasToken() {
				if err := runWelcome(false); err != nil {
					return err
				}
				// The user can quit the wizard before signing in (no token
				// saved). Don't fall through into the TUI's "no token" error —
				// exit cleanly; they can re-run when ready.
				if !auth.HasToken() {
					return nil
				}
				// The launch about to happen is the first one, which is worth
				// telling apart from every later launch: it is the only sample
				// of what a fresh install's defaults look like before anyone
				// has changed anything.
				ui.SetFirstRun()
			}
			return runTUI()
		},
	}
	// --version is the only way to ask (there is no `version` subcommand): it
	// prints the whole build/tag/platform block a bug report wants, which
	// Version already holds, so the template is a bare substitution.
	root.SetVersionTemplate("{{.Version}}")
	// Registered by hand only for the help text: cobra would otherwise add
	// this flag itself, described as the unhelpful "version for matterbox".
	root.Flags().BoolP("version", "v", false,
		"print the build, its optional features, and the platform")
	root.Flags().StringVar(&pprofAddr, "pprof", "",
		"if set (e.g. localhost:6060), serve net/http/pprof on this address")
	root.AddCommand(newWelcomeCmd(), newLoginCmd(), newURLHandlerCmd(), newRegisterHandlerCmd(), newGithubCmd(), newSendCmd(), newReplyCmd(), newReactCmd(), newReadCmd(), newUnreadCmd(), newMarkReadCmd(), newOpenCmd(), newSearchCmd(), newChannelsCmd(), newDigestCmd(), newWhoamiCmd(), newEmbedCmd(), newListenCmd(), newRulesCmd(), newKeysCmd(), newDecodeCmd())
	return root
}

// Execute runs the command tree and returns a process exit code: 0 on
// success, 1 on any error. main() is a one-liner around this.
func Execute() int {
	started := time.Now()
	// Hold any event a subcommand emits until reportCommand can decide whether
	// there is anyone to send it to. Telemetry is started after the work is
	// done — so `matterbox decode` never reads a config it has no use for —
	// which means an event emitted during the work would otherwise find no
	// client and be dropped. One atomic store; the buffer is bounded and
	// discarded when consent is absent.
	telemetry.BeginPending()
	root := newRootCmd()
	// A panic escaping a subcommand would otherwise be a crash nobody hears
	// about: several verbs are only ever run by scripts and notification
	// handlers. Report it while the stack is still standing, flush, and let the
	// panic carry on so the crash and its trace are exactly what they were.
	// The TUI's own panics are caught closer in, inside Update and View.
	defer func() {
		if v := recover(); v != nil {
			reportPanic(v)
			panic(v)
		}
	}()
	// ExecuteC, not Execute: it returns the command that actually ran, which is
	// what the telemetry needs to name the verb. Behaviour is otherwise
	// identical.
	cmd, err := root.ExecuteC()
	// Anonymous telemetry, if the user opted in. Only for a subcommand — the
	// root command is the TUI, which reports its own app_started / app_stopped
	// with far more to say than an exit code. A no-op without consent.
	if cmd != nil && cmd != root {
		reportCommand(cmd, started, err)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "matterbox:", err)
		return 1
	}
	return 0
}

// dial loads config + token and returns a ready Mattermost client. Shared
// by every command, including the TUI launch.
func dial() (*config.Config, *mm.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	token, err := auth.ReadToken()
	if err != nil {
		return nil, nil, err
	}
	return cfg, mm.New(cfg.ServerURL, token), nil
}

// runTUI reproduces the original main(): optional pprof server, emoji
// padding tweak, then the bubbletea program over the shared client.
func runTUI() error {
	emoji.ReplacePadding = ""

	if pprofAddr != "" {
		runtime.SetBlockProfileRate(1)
		runtime.SetMutexProfileFraction(1)
		go func() {
			if err := http.ListenAndServe(pprofAddr, nil); err != nil {
				fmt.Fprintln(os.Stderr, "matterbox: pprof server:", err)
			}
		}()
	}

	cfg, client, err := dial()
	if err != nil {
		return err
	}
	// Fail loud on a bad keybinding override (unknown action, bad chord, or a
	// conflict) before launching, rather than silently dropping the binding.
	if err := ui.CheckKeybindings(cfg); err != nil {
		return fmt.Errorf("keybindings: %w", err)
	}
	// Anonymous telemetry, if the user opted in: a no-op otherwise, so there is
	// nothing to branch on here. Close flushes on the way out — after the
	// program has released the terminal, so a slow flush can't stall the exit
	// mid-teardown.
	telemetry.Start(cfg)
	defer telemetry.Close()
	// ui builds the launch event but has no way to know what this binary is
	// called or what it was compiled with.
	stamp := readBuildStamp()
	ui.SetBuildInfo(versionName(stamp), stamp.tags)
	// The same stamp on error reports, so a crash says which build produced it
	// — the first question asked of any of them, and unrecoverable afterwards.
	telemetry.SetBuild(versionName(stamp), stamp.tags)
	// v2 drops tea.WithAltScreen(); each tea.View opts in via
	// v.AltScreen = true (set in Model.View). v2 always requests the
	// kitty "disambiguate escape codes" flag, which makes shift+enter
	// arrive as a distinct keypress on capable terminals.
	prog := tea.NewProgram(ui.New(client, cfg))
	// Listen on the control socket so `matterbox open <channel>` (e.g. from a
	// desktop-notification click) can jump this running TUI to a conversation.
	stop := ui.ServeControlSocket(prog)
	defer stop()
	if _, err := prog.Run(); err != nil {
		// The session ended badly. Report it before the deferred Close flushes,
		// so the reason survives.
		telemetry.AppStopped("error")
		return err
	}
	telemetry.AppStopped("quit")
	return nil
}
