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
	return &cobra.Command{
		Use:   "welcome",
		Short: "Run the first-run setup wizard (animated)",
		Long: "Run the interactive setup wizard: a vaporwave intro, then a short form to\n" +
			"set your Mattermost server URL, sign in, and pick a few preferences\n" +
			"(mark-read delay, SQL tab, mouse support, animations, ctrl+arrow nav).\n" +
			"It writes ~/.config/matterbox/config.yaml and the saved login token.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWelcome()
		},
	}
}

func runWelcome() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := tea.NewProgram(welcome.New(cfg)).Run(); err != nil {
		return err
	}
	return nil
}
