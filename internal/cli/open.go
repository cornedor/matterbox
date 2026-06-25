package cli

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"

	"matterbox/internal/ui"
)

// newOpenCmd builds `matterbox open`, which tells an already-running TUI on
// this machine to switch to a channel. It writes to the TUI's local control
// socket (see ui.ServeControlSocket); with no TUI running it errors.
func newOpenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "open <channel>",
		Short: "Open a channel or DM in the running matterbox TUI",
		Long: "Tell an already-running matterbox TUI to switch to a channel.\n\n" +
			"The channel is team/channel (e.g. eng/general), @user for a direct\n" +
			"message, or a raw channel id — the same addresses read/send/mark-read\n" +
			"take. A raw id (what `matterbox listen` notification buttons pass via\n" +
			"$MATTERBOX_CHANNEL_ID) is sent as-is; anything else is resolved first.\n\n" +
			"This drives the TUI over a local control socket, so it only works on\n" +
			"the same machine as a running matterbox, and is a no-op (error) if none\n" +
			"is running. It's how a desktop-notification click can jump straight to\n" +
			"the conversation:\n\n" +
			"  matterbox open eng/general\n" +
			"  matterbox open @alice\n" +
			"  matterbox open \"$MATTERBOX_CHANNEL_ID\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOpen(cmd.Context(), args[0])
		},
	}
	return cmd
}

// runOpen resolves the spec to a channel id (a raw 26-char id passes straight
// through, like mark-read) and hands it to the running TUI's control socket.
func runOpen(ctx context.Context, spec string) error {
	channelID := strings.TrimSpace(spec)
	if !model.IsValidId(channelID) {
		_, client, err := dial()
		if err != nil {
			return err
		}
		me, err := client.Me(ctx)
		if err != nil {
			return err
		}
		ch, err := resolveChannel(ctx, client, me, spec)
		if err != nil {
			return err
		}
		channelID = ch.Id
	}
	return sendControl("open " + channelID)
}

// sendControl writes one newline-terminated command to the running TUI's
// control socket. A failed dial means no TUI is listening here.
func sendControl(command string) error {
	path, err := ui.ControlSocketPath()
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return fmt.Errorf("no running matterbox TUI to drive (control socket %s)", path)
	}
	defer conn.Close()
	_, err = fmt.Fprintln(conn, command)
	return err
}
