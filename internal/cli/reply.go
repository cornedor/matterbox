package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"

	"matterbox/internal/replyto"
)

func newReplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reply <message-id> [message...]",
		Short: "Reply in a message's thread",
		Long: "Post a reply in the thread of an existing message.\n\n" +
			"The message id is a post id — the `id` field of `read --json` output, or\n" +
			"the $MATTERBOX_POST_ID a `matterbox listen` exec rule exports. The reply is\n" +
			"rooted at that message's thread: if the message starts a thread the reply\n" +
			"goes under it, and if the message is already a reply the new one joins the\n" +
			"same thread (Mattermost threads are one level deep). The channel is taken\n" +
			"from the message, so you don't name it.\n\n" +
			"When the message is itself a reply, the new one records that it answers that\n" +
			"specific message — invisibly, so other clients see the ordinary flat reply\n" +
			"they always saw, while matterbox draws it nested underneath.\n\n" +
			"The message body is the remaining arguments joined by spaces; with none it\n" +
			"is read from standard input, so both forms work:\n\n" +
			"  matterbox reply 7f3k… \"on it, thanks\"\n" +
			"  echo \"on it\" | matterbox reply 7f3k…",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg, err := messageFromArgsOrStdin(args[1:], cmd.InOrStdin(), false)
			if err != nil {
				return err
			}
			return runReply(cmd.Context(), args[0], msg)
		},
	}
	return cmd
}

func runReply(ctx context.Context, postID, msg string) error {
	_, client, err := dial()
	if err != nil {
		return err
	}
	// Confirmation goes to stderr so stdout stays clean for pipelines, matching
	// send. os.Stderr (not cmd.OutOrStderr) keeps reply independent of cobra for
	// the unit test.
	return reply(ctx, client, postID, msg, os.Stderr)
}

// threadReplier is the slice of the client reply needs: looking up the target
// post (to learn its channel and thread root) and posting the reply into that
// thread. *mm.Client satisfies it; the test uses a fake so the threading logic
// can be exercised without a server.
type threadReplier interface {
	Thread(ctx context.Context, postID string) (*model.PostList, error)
	Send(ctx context.Context, channelID, rootID, message string, fileIDs []string) (*model.Post, error)
}

// reply posts msg into the thread of postID. It fetches the post to learn its
// channel and thread root, then sends with that root so the reply threads under
// the original message (or joins its existing thread). A post that already has a
// RootId reuses it — Mattermost's own threads don't nest — so replying to a
// reply lands in the same thread rather than starting a new one off a reply.
//
// Replying to a reply does, however, record *which* reply it answers, as
// invisible bytes on the body (see internal/replyto). Nothing changes for other
// clients — the post is the same flat reply it always was — but matterbox draws
// it nested under the message named here, which is the message the caller
// actually pointed at. That matters most for the inline reply on a desktop
// notification, where the id in hand is the specific message being answered.
func reply(ctx context.Context, r threadReplier, postID, msg string, out io.Writer) error {
	postID = strings.TrimSpace(postID)
	if postID == "" {
		return fmt.Errorf("empty message id")
	}
	pl, err := r.Thread(ctx, postID)
	if err != nil {
		return fmt.Errorf("look up message %s: %w", postID, err)
	}
	var p *model.Post
	if pl != nil {
		p = pl.Posts[postID]
	}
	if p == nil {
		return fmt.Errorf("no message %q", postID)
	}
	root := p.RootId
	if root == "" {
		root = p.Id
	}
	if p.RootId != "" {
		// The target is a reply, not a thread root, so "which message" is a
		// question worth answering; against a root, RootId already says it.
		msg = replyto.Attach(msg, p.Id)
	}
	if _, err := r.Send(ctx, p.ChannelId, root, msg, nil); err != nil {
		return fmt.Errorf("reply in thread %s: %w", root, err)
	}
	fmt.Fprintf(out, "matterbox: replied in thread %s\n", root)
	return nil
}
