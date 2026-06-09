package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"

	"matterbox/internal/store"
)

func newDigestCmd() *cobra.Command {
	var (
		since string
		until string
		limit int
	)
	cmd := &cobra.Command{
		Use:     "digest",
		Aliases: []string{"activity"},
		Short:   "List your own messages across all channels for a time range",
		Long: "List the messages you posted across every channel in a time window —\n" +
			"\"what did I work on\" — grouped by channel, most-recently-active first.\n\n" +
			"Messages come from the local cache (the same store the TUI and search use),\n" +
			"so this is a single fast scan rather than one history fetch per channel; it\n" +
			"only sees what matterbox has cached. --since / --until accept now, today,\n" +
			"yesterday, 7d, 2h, or 2006-01-02 (--until is exclusive). --since defaults to\n" +
			"the start of today.\n\n" +
			"  matterbox digest\n" +
			"  matterbox digest --since yesterday\n" +
			"  matterbox digest --since 7d\n" +
			"  matterbox digest --since 2026-06-01 --until 2026-06-08",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			now := time.Now()
			sinceMs, untilMs, err := parseSinceUntil(since, until, now, time.Local)
			if err != nil {
				return err
			}
			if sinceMs == 0 { // no --since → default to the start of today
				sinceMs = startOfDay(now, time.Local).UnixMilli()
			}
			return runDigest(cmd.Context(), sinceMs, untilMs, limit, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "start of the range (default: start of today)")
	cmd.Flags().StringVar(&until, "until", "", "end of the range, exclusive (default: now)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "cap the number of messages shown, keeping the most recent (0 = all)")
	return cmd
}

func runDigest(ctx context.Context, sinceMs, untilMs int64, limit int, out io.Writer) error {
	_, client, err := dial()
	if err != nil {
		return err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return err
	}

	p, err := store.DefaultPath()
	if err != nil {
		return err
	}
	st, err := store.Open(p)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	posts, err := st.AuthoredBetween(me.Id, sinceMs, untilMs, limit)
	if err != nil {
		return err
	}
	if len(posts) == 0 {
		fmt.Fprintln(os.Stderr, "matterbox: nothing from you in that range")
		return nil
	}

	// Channel/team metadata only labels the groups; degrade to raw ids/slugs
	// rather than failing a digest whose content is already in hand.
	channels, _ := client.AllChannels(ctx, me.Id)
	chByID := make(map[string]*model.Channel, len(channels))
	for _, c := range channels {
		chByID[c.Id] = c
	}
	teamSlug := map[string]string{}
	if teams, terr := client.Teams(ctx, me.Id); terr == nil {
		for _, t := range teams {
			teamSlug[t.Id] = t.Name
		}
	}
	names, _ := client.UsernamesByIDs(ctx, digestPartnerIDs(posts, chByID, me.Id))
	if names == nil {
		names = map[string]string{}
	}
	names[me.Id] = me.Username // render our own posts by name, not @unknown
	lbl := labeler{meID: me.Id, teamSlug: teamSlug, channels: chByID, names: names}

	printDigest(out, lbl, names, posts)
	return nil
}

// digestPartnerIDs collects the DM partners of every channel the digest
// touches, so their usernames can be resolved for the @partner headers.
func digestPartnerIDs(posts []*model.Post, chByID map[string]*model.Channel, meID string) []string {
	seen := map[string]bool{}
	var ids []string
	for _, p := range posts {
		ch := chByID[p.ChannelId]
		if ch == nil || ch.Type != model.ChannelTypeDirect {
			continue
		}
		if id := dmPartnerID(ch, meID); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// printDigest groups posts (already oldest→newest) by channel and prints each
// group under a "header · N messages" line, channels ordered most-recently
// active first. Timestamps carry the date since a digest can span days.
func printDigest(out io.Writer, lbl labeler, names map[string]string, posts []*model.Post) {
	order := make([]string, 0)
	byChan := map[string][]*model.Post{}
	for _, p := range posts {
		if _, ok := byChan[p.ChannelId]; !ok {
			order = append(order, p.ChannelId)
		}
		byChan[p.ChannelId] = append(byChan[p.ChannelId], p)
	}
	sort.SliceStable(order, func(i, j int) bool {
		gi, gj := byChan[order[i]], byChan[order[j]]
		return gi[len(gi)-1].CreateAt > gj[len(gj)-1].CreateAt
	})
	for i, chID := range order {
		grp := byChan[chID]
		fmt.Fprintf(out, "%s · %s\n", lbl.header(chID), plural(len(grp), "message", "messages"))
		io.WriteString(out, formatPostsLayout(grp, names, "Jan 2 15:04"))
		if i < len(order)-1 {
			io.WriteString(out, "\n")
		}
	}
}
