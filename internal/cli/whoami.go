package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Print the authenticated user's id, username, and email",
		Long: "Print the user the saved token authenticates as: username, user id, and\n" +
			"email, one per line. Useful in scripts that need your own user id (e.g.\n" +
			"to recognise your messages) without hand-rolling a /api/v4/users/me call.\n\n" +
			"  matterbox whoami\n" +
			"  matterbox whoami | awk '$1==\"id\"{print $2}'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWhoami(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

func runWhoami(ctx context.Context, out io.Writer) error {
	_, client, err := dial()
	if err != nil {
		return err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return err
	}
	// Aligned single-token values so a field is `awk '{print $2}'`-able.
	fmt.Fprintf(out, "username  %s\n", me.Username)
	fmt.Fprintf(out, "id        %s\n", me.Id)
	fmt.Fprintf(out, "email     %s\n", me.Email)
	return nil
}
