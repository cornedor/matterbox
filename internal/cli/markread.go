package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"
)

func newMarkReadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mark-read <channel> [channel...]",
		Short: "Mark a channel or DM as read (clear its unread state)",
		Long: "Mark one or more channels as read on the server, clearing their unread\n" +
			"count and any mentions — the same effect opening the channel in the UI\n" +
			"has. Useful for scripting a \"catch up\", or for dismissing a noisy\n" +
			"channel without actually reading it.\n\n" +
			"Each channel is team/channel (e.g. eng/general) or @user for a direct\n" +
			"message — the same addresses read/send/unread use. Several can be given\n" +
			"at once; they're marked read left to right and the command stops at the\n" +
			"first one that fails to resolve:\n\n" +
			"  matterbox mark-read eng/general\n" +
			"  matterbox mark-read @alice\n" +
			"  matterbox mark-read eng/general eng/random @bob",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMarkRead(cmd.Context(), args)
		},
	}
	return cmd
}

func runMarkRead(ctx context.Context, specs []string) error {
	_, client, err := dial()
	if err != nil {
		return err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return err
	}
	// Confirmation goes to stderr so stdout stays clean for pipelines, matching
	// send. os.Stderr (not cmd.OutOrStderr) keeps markRead independent of cobra
	// for the unit test.
	return markRead(ctx, client, me, specs, os.Stderr)
}

// channelViewer is the slice of the client mark-read needs: resolving a spec to
// a channel, then marking it viewed (read). *mm.Client satisfies it; the test
// uses a fake so the loop can be exercised without a server.
type channelViewer interface {
	resolver
	ViewChannel(ctx context.Context, userID, channelID string) error
}

// markRead resolves each spec and marks the channel read for me, writing a
// one-line confirmation per channel. It returns at the first spec that fails to
// resolve or whose view call errors, so a typo doesn't silently skip a channel.
func markRead(ctx context.Context, v channelViewer, me *model.User, specs []string, out io.Writer) error {
	for _, spec := range specs {
		ch, err := resolveChannel(ctx, v, me, spec)
		if err != nil {
			return err
		}
		if err := v.ViewChannel(ctx, me.Id, ch.Id); err != nil {
			return fmt.Errorf("mark %s read: %w", spec, err)
		}
		fmt.Fprintf(out, "matterbox: marked %s read\n", spec)
	}
	return nil
}
