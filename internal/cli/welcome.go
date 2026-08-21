package cli

import (
	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"matterbox/internal/config"
	"matterbox/internal/welcome"
)

// newWelcomeCmd builds the `welcome` subcommand: a first-run setup wizard that
// plays the vaporwave intro animation and then collects the server URL,
// authentication, and a screen of preferences on top of the running scene,
// writing config.yaml and the saved token. Unlike the other verbs it does not
// dial() — it runs before there is a token, since setting one up is the point.
func newWelcomeCmd() *cobra.Command {
	var demo bool
	cmd := &cobra.Command{
		Use:   "welcome",
		Short: "Run the first-run setup wizard (animated)",
		Long: "Run the interactive setup wizard: a vaporwave intro, then a short form to\n" +
			"set your Mattermost server URL, sign in, and pick a few preferences\n" +
			"(mark-read delay, SQL tab, mouse support, animations, ctrl+arrow nav).\n" +
			"It also asks whether anonymous telemetry may be collected — off unless you\n" +
			"say yes, and asked only here, never while you're using the client.\n" +
			"It writes ~/.config/matterbox/config.yaml and the saved login token.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWelcome(demo)
		},
	}
	cmd.Flags().BoolVar(&demo, "demo", false, "demoscene mode: bob the title on a sine wave and play a chiptune soundtrack")
	return cmd
}

func runWelcome(demo bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// In demo mode, play the embedded tracker soundtrack behind the intro. The
	// lifecycle matches the program's: started here, stopped when Run() returns.
	if demo {
		defer welcome.StartDemoMusic()()
	}
	if _, err := tea.NewProgram(welcome.New(cfg, demo)).Run(); err != nil {
		return err
	}
	return nil
}
