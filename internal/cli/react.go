package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"
)

func newReactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "react <message-id> <emoji> [emoji...]",
		Short: "Add an emoji reaction to a message",
		Long: "Add one or more emoji reactions to a message, the same as clicking the\n" +
			"reaction button in the UI.\n\n" +
			"The message id is a post id — the `id` field of `read --json` output, or\n" +
			"the root id `read --thread` takes. Each emoji is a shortcode; surrounding\n" +
			"colons are optional, so `tada` and `:tada:` are the same. Several emoji can\n" +
			"be given at once; they're added left to right and the command stops at the\n" +
			"first one the server rejects (e.g. an unknown emoji):\n\n" +
			"  matterbox react 7f3k… tada\n" +
			"  matterbox react 7f3k… :+1:\n" +
			"  matterbox react 7f3k… eyes rocket\n\n" +
			"A post id is easy to grab from read:\n\n" +
			"  matterbox read eng/general --json | jq -r 'select(.username==\"alice\").id'",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReact(cmd.Context(), args[0], args[1:])
		},
	}
	return cmd
}

func runReact(ctx context.Context, postID string, emojis []string) error {
	_, client, err := dial()
	if err != nil {
		return err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return err
	}
	// Confirmation goes to stderr so stdout stays clean for pipelines, matching
	// send/mark-read. os.Stderr (not cmd.OutOrStderr) keeps react independent of
	// cobra for the unit test.
	return react(ctx, client, me, postID, emojis, os.Stderr)
}

// reactor is the slice of the client react needs: adding an emoji reaction on
// behalf of a user. *mm.Client satisfies it; the test uses a fake so the loop
// can be exercised without a server.
type reactor interface {
	AddReaction(ctx context.Context, userID, postID, emojiName string) error
}

// react adds each emoji as a reaction to postID on my behalf, writing a
// one-line confirmation per emoji. It returns at the first emoji that fails
// (an unknown shortcode, say), so a typo doesn't silently skip the rest.
func react(ctx context.Context, r reactor, me *model.User, postID string, emojis []string, out io.Writer) error {
	postID = strings.TrimSpace(postID)
	if postID == "" {
		return fmt.Errorf("empty message id")
	}
	for _, e := range emojis {
		name := normalizeEmojiName(e)
		if name == "" {
			return fmt.Errorf("empty emoji name")
		}
		if err := r.AddReaction(ctx, me.Id, postID, name); err != nil {
			return fmt.Errorf("react :%s: to %s: %w", name, postID, err)
		}
		fmt.Fprintf(out, "matterbox: reacted :%s: to %s\n", name, postID)
	}
	return nil
}

// normalizeEmojiName strips surrounding colons and whitespace so both `tada`
// and `:tada:` are accepted; Mattermost stores reactions by bare shortcode.
func normalizeEmojiName(s string) string {
	return strings.Trim(strings.TrimSpace(s), ":")
}
