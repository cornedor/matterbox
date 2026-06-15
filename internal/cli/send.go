package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// maxAttachments mirrors the web client's per-post file cap.
const maxAttachments = 5

func newSendCmd() *cobra.Command {
	var files []string
	cmd := &cobra.Command{
		Use:   "send <channel> [message...]",
		Short: "Post a message to a channel",
		Long: "Post a message to a channel.\n\n" +
			"The channel is team/channel (e.g. eng/general) or @user for a direct\n" +
			"message. A comma-separated list of users (@a,@b[,@c…]) addresses the\n" +
			"group DM you share with all of them, creating it if it doesn't exist\n" +
			"yet — a group DM holds you plus 2–7 others. The message is the\n" +
			"remaining arguments joined by spaces; if no message is given it is\n" +
			"read from standard input, so this works:\n\n" +
			"  matterbox send eng/general \"deploy is done\"\n" +
			"  echo \"deploy is done\" | matterbox send eng/general\n" +
			"  matterbox send @alice \"ping\"\n" +
			"  matterbox send @alice,@bob \"standup in 5\"\n\n" +
			"Attach files with --file (repeatable, max 5). With an attachment the\n" +
			"message is optional, so a caption-less upload works:\n\n" +
			"  matterbox send @alice --file diagram.png \"see attached\"\n" +
			"  matterbox send eng/general --file a.gif",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg, err := messageFromArgsOrStdin(args[1:], cmd.InOrStdin(), len(files) > 0)
			if err != nil {
				return err
			}
			return runSend(cmd.Context(), args[0], msg, files)
		},
	}
	cmd.Flags().StringArrayVar(&files, "file", nil, "attach a file (repeatable, max 5)")
	return cmd
}

// messageFromArgsOrStdin joins the trailing args into the message body, or
// reads stdin when none were given (enabling `echo … | matterbox send`).
// A trailing newline from stdin is trimmed; an all-whitespace result is an
// error so we never post an empty message — unless allowEmpty is set, which
// the caller does when files are attached (a caption is then optional and we
// don't block reading stdin for one).
func messageFromArgsOrStdin(words []string, stdin io.Reader, allowEmpty bool) (string, error) {
	msg := strings.Join(words, " ")
	if msg == "" && !allowEmpty {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		msg = strings.TrimRight(string(b), "\n")
	}
	if strings.TrimSpace(msg) == "" && !allowEmpty {
		return "", fmt.Errorf("empty message")
	}
	return msg, nil
}

func runSend(ctx context.Context, spec, msg string, files []string) error {
	if len(files) > maxAttachments {
		return fmt.Errorf("too many attachments: %d (max %d)", len(files), maxAttachments)
	}
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
	// Upload attachments to the target channel first; the resulting file
	// ids are attached to the post via Send.
	var fileIDs []string
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read attachment %q: %w", p, err)
		}
		info, err := client.UploadFile(ctx, ch.Id, filepath.Base(p), data)
		if err != nil {
			return fmt.Errorf("upload %q: %w", p, err)
		}
		fileIDs = append(fileIDs, info.Id)
	}
	if _, err := client.Send(ctx, ch.Id, "", msg, fileIDs); err != nil {
		return err
	}
	// Confirmation to stderr keeps stdout clean for pipelines.
	if len(fileIDs) > 0 {
		fmt.Fprintf(os.Stderr, "matterbox: sent to %s (%d attachment(s))\n", spec, len(fileIDs))
	} else {
		fmt.Fprintf(os.Stderr, "matterbox: sent to %s\n", spec)
	}
	return nil
}
