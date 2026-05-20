package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newSendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "send <channel> [message...]",
		Short: "Post a message to a channel",
		Long: "Post a message to a channel.\n\n" +
			"The channel is team/channel (e.g. eng/general) or @user for a direct\n" +
			"message. The message is the remaining arguments joined by spaces; if no\n" +
			"message is given it is read from standard input, so this works:\n\n" +
			"  matterbox send eng/general \"deploy is done\"\n" +
			"  echo \"deploy is done\" | matterbox send eng/general\n" +
			"  matterbox send @alice \"ping\"",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg, err := messageFromArgsOrStdin(args[1:], cmd.InOrStdin())
			if err != nil {
				return err
			}
			return runSend(cmd.Context(), args[0], msg)
		},
	}
}

// messageFromArgsOrStdin joins the trailing args into the message body, or
// reads stdin when none were given (enabling `echo … | matterbox send`).
// A trailing newline from stdin is trimmed; an all-whitespace result is an
// error so we never post an empty message.
func messageFromArgsOrStdin(words []string, stdin io.Reader) (string, error) {
	msg := strings.Join(words, " ")
	if msg == "" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		msg = strings.TrimRight(string(b), "\n")
	}
	if strings.TrimSpace(msg) == "" {
		return "", fmt.Errorf("empty message")
	}
	return msg, nil
}

func runSend(ctx context.Context, spec, msg string) error {
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
	if _, err := client.Send(ctx, ch.Id, "", msg, nil); err != nil {
		return err
	}
	// Confirmation to stderr keeps stdout clean for pipelines.
	fmt.Fprintf(os.Stderr, "matterbox: sent to %s\n", spec)
	return nil
}
