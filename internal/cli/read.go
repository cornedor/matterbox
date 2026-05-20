package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"
)

// defaultReadLimit is how many recent posts `read` shows when --limit is
// not given.
const defaultReadLimit = 30

func newReadCmd() *cobra.Command {
	var (
		limit   int
		wait    bool
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "read <channel>",
		Short: "Print recent messages from a channel",
		Long: "Print the most recent messages from a channel, oldest first.\n\n" +
			"The channel is team/channel (e.g. eng/general) or @user for a direct\n" +
			"message. System messages (joins, leaves, …) are omitted.\n\n" +
			"With --wait, after printing history the command opens a websocket and\n" +
			"blocks until at least one new message arrives, prints it, and exits —\n" +
			"so it never returns empty-handed:\n\n" +
			"  matterbox read eng/general\n" +
			"  matterbox read @alice --limit 50\n" +
			"  matterbox read eng/general --wait --timeout 5m",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout > 0 && !wait {
				return fmt.Errorf("--timeout requires --wait")
			}
			return runRead(cmd.Context(), args[0], limit, wait, timeout, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", defaultReadLimit, "number of recent messages to show")
	cmd.Flags().BoolVarP(&wait, "wait", "w", false,
		"after printing history, block on the websocket until a new message arrives, then exit")
	cmd.Flags().DurationVar(&timeout, "timeout", 0,
		"with --wait, give up after this duration (e.g. 30s, 5m); 0 waits forever")
	return cmd
}

func runRead(ctx context.Context, spec string, limit int, wait bool, timeout time.Duration, out io.Writer) error {
	if limit <= 0 {
		limit = defaultReadLimit
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

	// In --wait mode, connect the socket BEFORE fetching history so a
	// message that lands between the fetch and the subscription isn't
	// lost — EventChannel buffers it, and the create_at cutoff below
	// dedupes anything the history fetch already returned.
	var wsc *model.WebSocketClient
	if wait {
		wsc, err = client.DialWS()
		if err != nil {
			return err
		}
		defer wsc.Close()
	}

	pl, err := client.Posts(ctx, ch.Id, limit)
	if err != nil {
		return err
	}
	posts := orderedPosts(pl)
	names, err := client.UsernamesByIDs(ctx, uniqueUserIDs(posts))
	if err != nil {
		return err
	}
	if _, err := io.WriteString(out, formatPosts(posts, names)); err != nil {
		return err
	}

	if !wait {
		return nil
	}
	// "At least one more" is relative to the newest post we just printed
	// (or now, if the channel was empty).
	since := latestCreateAt(posts)
	if since == 0 {
		since = time.Now().UnixMilli()
	}
	ev, p, err := awaitMessage(ctx, wsc, ch.Id, since, timeout)
	if err != nil {
		return err
	}
	return printLiveMessage(ctx, client, out, ev, p, "")
}

// orderedPosts flattens a PostList into chronological (oldest→newest)
// order, dropping system posts (Type != "") and any id in Order that has
// no matching entry in Posts. PostList.Order is newest-first, so we walk
// it backward.
func orderedPosts(pl *model.PostList) []*model.Post {
	if pl == nil {
		return nil
	}
	out := make([]*model.Post, 0, len(pl.Order))
	for i := len(pl.Order) - 1; i >= 0; i-- {
		p := pl.Posts[pl.Order[i]]
		if p == nil || p.Type != "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// uniqueUserIDs collects the distinct author ids across posts, preserving
// first-seen order, for a single batched username lookup.
func uniqueUserIDs(posts []*model.Post) []string {
	seen := make(map[string]bool, len(posts))
	ids := make([]string, 0, len(posts))
	for _, p := range posts {
		if p.UserId == "" || seen[p.UserId] {
			continue
		}
		seen[p.UserId] = true
		ids = append(ids, p.UserId)
	}
	return ids
}

// latestCreateAt returns the largest CreateAt across posts, or 0 if empty.
func latestCreateAt(posts []*model.Post) int64 {
	var max int64
	for _, p := range posts {
		if p.CreateAt > max {
			max = p.CreateAt
		}
	}
	return max
}

// formatPosts renders posts as "[15:04] @user  message", with multi-line
// bodies indented to line up under the message column. names maps author
// id → username; unknown authors render as @unknown.
func formatPosts(posts []*model.Post, names map[string]string) string {
	var b strings.Builder
	for _, p := range posts {
		name := names[p.UserId]
		if name == "" {
			name = "unknown"
		}
		prefix := fmt.Sprintf("[%s] @%s  ", time.UnixMilli(p.CreateAt).Format("15:04"), name)
		lines := strings.Split(p.Message, "\n")
		b.WriteString(prefix)
		b.WriteString(lines[0])
		b.WriteByte('\n')
		if len(lines) > 1 {
			indent := strings.Repeat(" ", utf8.RuneCountInString(prefix))
			for _, l := range lines[1:] {
				b.WriteString(indent)
				b.WriteString(l)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
