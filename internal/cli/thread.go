package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"
)

func newThreadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thread",
		Short: "Follow, unfollow, or check a thread",
		Long: "Read or change whether you follow a thread — the membership Mattermost's\n" +
			"collapsed reply threads notify on. You follow a thread you started, replied\n" +
			"in, or were mentioned in; unfollowing is how you mute one.\n\n" +
			"Each verb takes the id (or permalink) of any message in the thread:\n\n" +
			"  matterbox thread status 7f3k…\n" +
			"  matterbox thread unfollow https://chat.example.com/team/pl/7f3k…",
	}
	verb := func(use, short string, run func(ctx context.Context, f threadFollower, rootID, teamID string) error) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <message-id>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				_, client, err := dial()
				if err != nil {
					return err
				}
				ctx := cmd.Context()
				rootID, teamID, err := threadOf(ctx, client, postID(args[0]))
				if err != nil {
					return err
				}
				return run(ctx, client, rootID, teamID)
			},
		}
	}
	cmd.AddCommand(
		verb("status", "Print whether you follow the thread", func(ctx context.Context, f threadFollower, rootID, teamID string) error {
			return threadStatus(ctx, f, rootID, teamID, os.Stdout)
		}),
		verb("follow", "Follow the thread", func(ctx context.Context, f threadFollower, rootID, teamID string) error {
			return setThreadFollow(ctx, f, rootID, teamID, true, os.Stderr)
		}),
		verb("unfollow", "Unfollow the thread", func(ctx context.Context, f threadFollower, rootID, teamID string) error {
			return setThreadFollow(ctx, f, rootID, teamID, false, os.Stderr)
		}),
	)
	return cmd
}

// threadFollower is the slice of the client the verbs need. *mm.Client
// satisfies it; the test uses a fake.
type threadFollower interface {
	ThreadFollowing(ctx context.Context, teamID, rootID string) (bool, error)
	SetThreadFollowing(ctx context.Context, teamID, rootID string, follow bool) error
}

// threadLocator resolves a message to its thread and team.
type threadLocator interface {
	Post(ctx context.Context, postID string) (*model.Post, error)
	Channel(ctx context.Context, channelID string) (*model.Channel, error)
}

// threadOf returns the root of the thread postID belongs to and the team its
// channel is in ("" for a DM).
func threadOf(ctx context.Context, l threadLocator, postID string) (rootID, teamID string, err error) {
	p, err := l.Post(ctx, postID)
	if err != nil {
		return "", "", fmt.Errorf("message %s: %w", postID, err)
	}
	rootID = p.RootId
	if rootID == "" {
		rootID = p.Id
	}
	ch, err := l.Channel(ctx, p.ChannelId)
	if err != nil {
		return "", "", err
	}
	return rootID, ch.TeamId, nil
}

func threadStatus(ctx context.Context, f threadFollower, rootID, teamID string, out io.Writer) error {
	ok, err := f.ThreadFollowing(ctx, teamID, rootID)
	if err != nil {
		return err
	}
	if ok {
		fmt.Fprintln(out, "following")
	} else {
		fmt.Fprintln(out, "not following")
	}
	return nil
}

func setThreadFollow(ctx context.Context, f threadFollower, rootID, teamID string, follow bool, out io.Writer) error {
	if err := f.SetThreadFollowing(ctx, teamID, rootID, follow); err != nil {
		return err
	}
	if follow {
		fmt.Fprintf(out, "matterbox: following thread %s\n", rootID)
	} else {
		fmt.Fprintf(out, "matterbox: unfollowed thread %s\n", rootID)
	}
	return nil
}
