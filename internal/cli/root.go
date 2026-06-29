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

	tea "charm.land/bubbletea/v2"
	emoji "github.com/kyokomi/emoji/v2"
	"github.com/spf13/cobra"

	"matterbox/internal/auth"
	"matterbox/internal/config"
	"matterbox/internal/mm"
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
			"Run with no arguments to open the interactive UI, run `welcome` for the\n" +
			"first-run setup wizard, or use a subcommand (login, send, reply, react,\n" +
			"read, unread, mark-read, open, search, channels, digest, whoami, embed,\n" +
			"listen, keys) to work with messages non-interactively for scripting or to\n" +
			"run the background sync/notification daemon (listen).",
		SilenceUsage:  true,
		SilenceErrors: true,
		// No subcommand → open the interactive UI, exactly as before.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI()
		},
	}
	root.Flags().StringVar(&pprofAddr, "pprof", "",
		"if set (e.g. localhost:6060), serve net/http/pprof on this address")
	root.AddCommand(newWelcomeCmd(), newLoginCmd(), newURLHandlerCmd(), newRegisterHandlerCmd(), newSendCmd(), newReplyCmd(), newReactCmd(), newReadCmd(), newUnreadCmd(), newMarkReadCmd(), newOpenCmd(), newSearchCmd(), newChannelsCmd(), newDigestCmd(), newWhoamiCmd(), newEmbedCmd(), newListenCmd(), newKeysCmd())
	return root
}

// Execute runs the command tree and returns a process exit code: 0 on
// success, 1 on any error. main() is a one-liner around this.
func Execute() int {
	if err := newRootCmd().Execute(); err != nil {
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
		return err
	}
	return nil
}
