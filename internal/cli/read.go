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
		limit    int
		since    string
		until    string
		wait     bool
		timeout  time.Duration
		asJSONFn func() (bool, error)
	)
	cmd := &cobra.Command{
		Use:   "read <channel>",
		Short: "Print recent messages from a channel",
		Long: "Print the most recent messages from a channel, oldest first.\n\n" +
			"The channel is team/channel (e.g. eng/general) or @user for a direct\n" +
			"message. System messages (joins, leaves, …) are omitted.\n\n" +
			"--since / --until bound the messages by time (now, today, yesterday,\n" +
			"7d, 2h, or 2006-01-02). --until is exclusive, so a single day is\n" +
			"--since 2026-06-08 --until 2026-06-09. With --since the whole window is\n" +
			"shown by default (no silent --limit truncation across day boundaries);\n" +
			"pass --limit to cap it. Without --since, --until just filters the most\n" +
			"recent --limit messages.\n\n" +
			"With --wait, after printing history the command opens a websocket and\n" +
			"blocks until at least one new message arrives, prints it, and exits —\n" +
			"so it never returns empty-handed:\n\n" +
			"  matterbox read eng/general\n" +
			"  matterbox read @alice --limit 50\n" +
			"  matterbox read eng/general --since yesterday\n" +
			"  matterbox read eng/general --since 2026-06-08 --until 2026-06-09\n" +
			"  matterbox read eng/general --wait --timeout 5m",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout > 0 && !wait {
				return fmt.Errorf("--timeout requires --wait")
			}
			asJSON, err := asJSONFn()
			if err != nil {
				return err
			}
			sinceMs, untilMs, err := parseSinceUntil(since, until, time.Now(), time.Local)
			if err != nil {
				return err
			}
			// A bounded read shows the whole window by default; only re-cap it
			// when the user explicitly asked for a count. (0 = no cap below.)
			if sinceMs > 0 && !cmd.Flags().Changed("limit") {
				limit = 0
			}
			return runRead(cmd.Context(), args[0], limit, sinceMs, untilMs, wait, timeout, asJSON, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", defaultReadLimit, "number of recent messages to show (0 = no cap)")
	cmd.Flags().StringVar(&since, "since", "", "only messages at or after this time (e.g. yesterday, 7d, 2026-06-08)")
	cmd.Flags().StringVar(&until, "until", "", "only messages before this time (exclusive)")
	cmd.Flags().BoolVarP(&wait, "wait", "w", false,
		"after printing history, block on the websocket until a new message arrives, then exit")
	cmd.Flags().DurationVar(&timeout, "timeout", 0,
		"with --wait, give up after this duration (e.g. 30s, 5m); 0 waits forever")
	asJSONFn = addOutputFlags(cmd)
	return cmd
}

func runRead(ctx context.Context, spec string, limit int, sinceMs, untilMs int64, wait bool, timeout time.Duration, asJSON bool, out io.Writer) error {
	// limit <= 0 means "no cap"; only the unbounded recent read needs a
	// default fetch size to stand in for it.
	dateMode := sinceMs > 0 || untilMs > 0
	if limit <= 0 && !dateMode {
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

	// --since pulls everything past the boundary in one shot; otherwise fall
	// back to the recent page (and let --until filter it client-side).
	var pl *model.PostList
	if sinceMs > 0 {
		pl, err = client.PostsSince(ctx, ch.Id, sinceMs)
	} else {
		fetchN := limit
		if fetchN <= 0 {
			fetchN = defaultReadLimit
		}
		pl, err = client.Posts(ctx, ch.Id, fetchN)
	}
	if err != nil {
		return err
	}
	posts := filterByCreateRange(orderedPosts(pl), sinceMs, untilMs)
	posts = tailN(posts, limit)
	names, err := client.UsernamesByIDs(ctx, uniqueUserIDs(posts))
	if err != nil {
		return err
	}
	// read is single-channel, so every line's channel is the address the user
	// gave — no need to fetch teams just to label it.
	lbl := channelLabeler(func(string) string { return spec })
	if asJSON {
		if err := writeJSONPosts(out, lbl, names, posts); err != nil {
			return err
		}
	} else if _, err := io.WriteString(out, formatPosts(posts, names)); err != nil {
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
	return printLiveMessage(ctx, client, out, ev, p, "", asJSON, lbl)
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

// filterByCreateRange keeps posts whose create_at is in [sinceMs, untilMs):
// at or after sinceMs and strictly before untilMs. A non-positive bound is
// ignored, so the zero window returns posts unchanged. Matches the store's
// [after, before) convention so the CLI and the FTS filters agree.
func filterByCreateRange(posts []*model.Post, sinceMs, untilMs int64) []*model.Post {
	if sinceMs <= 0 && untilMs <= 0 {
		return posts
	}
	out := make([]*model.Post, 0, len(posts))
	for _, p := range posts {
		if sinceMs > 0 && p.CreateAt < sinceMs {
			continue
		}
		if untilMs > 0 && p.CreateAt >= untilMs {
			continue
		}
		out = append(out, p)
	}
	return out
}

// tailN keeps the most recent n posts (the slice tail, since posts are
// oldest→newest). A non-positive n means "no cap" and returns posts as-is.
func tailN(posts []*model.Post, n int) []*model.Post {
	if n <= 0 || len(posts) <= n {
		return posts
	}
	return posts[len(posts)-n:]
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
// overrideName returns a post's webhook/bot display name from
// Props["override_username"], or "" when it isn't an override post. The same
// UserId can post under multiple identities (a human and a bot on their
// account), so this per-post name must win over the id→name map.
func overrideName(p *model.Post) string {
	if ov, ok := p.GetProp("override_username").(string); ok && ov != "" {
		return ov
	}
	return ""
}

func formatPosts(posts []*model.Post, names map[string]string) string {
	return formatPostsLayout(posts, names, "15:04")
}

// formatPostsLayout is formatPosts with a caller-chosen timestamp layout, so a
// digest spanning several days can stamp dates ("Jan 2 15:04") while the
// single-channel read/unread/wait views stay time-only ("15:04"). The
// continuation-line indent follows the rendered prefix width, so it adapts to
// the layout automatically.
func formatPostsLayout(posts []*model.Post, names map[string]string, layout string) string {
	var b strings.Builder
	for _, p := range posts {
		name := overrideName(p)
		if name == "" {
			name = names[p.UserId]
		}
		if name == "" {
			name = "unknown"
		}
		prefix := fmt.Sprintf("[%s] @%s  ", time.UnixMilli(p.CreateAt).Format(layout), name)
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
