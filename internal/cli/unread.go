package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"

	"matterbox/internal/mm"
)

// unreadFetchPage is how many recent posts we pull for a channel whose
// read boundary is unknown (LastViewedAt == 0), so we don't walk its whole
// history just to find what's new.
const unreadFetchPage = 50

func newUnreadCmd() *cobra.Command {
	var (
		perChannel int
		wait       bool
		timeout    time.Duration
		asJSONFn   func() (bool, error)
	)
	cmd := &cobra.Command{
		Use:   "unread",
		Short: "Print unread messages across all channels",
		Long: "Print every unread message, grouped by channel, oldest first within\n" +
			"each group. Channels with mentions sort first, then by most recent\n" +
			"activity. Each group header is the channel's address (eng/general or\n" +
			"@user), so you can feed it straight back to read/send.\n\n" +
			"With --wait, after printing any unread the command opens a websocket\n" +
			"and blocks until the next new message in any channel arrives, prints it,\n" +
			"and exits — so an existing backlog doesn't make it return instantly:\n\n" +
			"  matterbox unread\n" +
			"  matterbox unread --limit 10\n" +
			"  matterbox unread --wait --timeout 15m",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout > 0 && !wait {
				return fmt.Errorf("--timeout requires --wait")
			}
			asJSON, err := asJSONFn()
			if err != nil {
				return err
			}
			return runUnread(cmd.Context(), perChannel, wait, timeout, asJSON, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVarP(&perChannel, "limit", "n", 0,
		"max unread messages to show per channel (0 = all); older ones collapse to a count")
	cmd.Flags().BoolVarP(&wait, "wait", "w", false,
		"after printing, block on the websocket until a new message arrives, then exit")
	cmd.Flags().DurationVar(&timeout, "timeout", 0,
		"with --wait, give up after this duration (e.g. 30s, 15m); 0 waits forever")
	asJSONFn = addOutputFlags(cmd)
	return cmd
}

// unreadGroup is one channel's worth of unread messages for output.
type unreadGroup struct {
	channelID string
	posts     []*model.Post // oldest→newest, capped to perChannel
	total     int           // count-based "N new" (server's authoritative total)
	mention   int
	truncated int // unread messages dropped by the per-channel cap
}

// lastActivity is the create time of the newest unread post, for sorting.
func (g unreadGroup) lastActivity() int64 {
	if n := len(g.posts); n > 0 {
		return g.posts[n-1].CreateAt
	}
	return 0
}

func runUnread(ctx context.Context, perChannel int, wait bool, timeout time.Duration, asJSON bool, out io.Writer) error {
	_, client, err := dial()
	if err != nil {
		return err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return err
	}

	// In --wait mode, connect the socket first (and pin the "new" cutoff to
	// now) so a message arriving while we gather state isn't lost.
	var (
		wsc   *model.WebSocketClient
		since int64
	)
	if wait {
		if wsc, err = client.DialWS(); err != nil {
			return err
		}
		defer wsc.Close()
		since = time.Now().UnixMilli()
	}

	channels, err := client.AllChannels(ctx, me.Id)
	if err != nil {
		return err
	}
	members, err := client.ChannelMembers(ctx, me.Id)
	if err != nil {
		return err
	}
	chByID := make(map[string]*model.Channel, len(channels))
	for _, c := range channels {
		chByID[c.Id] = c
	}
	// Teams are only needed to render team/channel headers; degrade to the
	// channel slug alone if the lookup fails.
	teamSlug := map[string]string{}
	if teams, terr := client.Teams(ctx, me.Id); terr == nil {
		for _, t := range teams {
			teamSlug[t.Id] = t.Name
		}
	}

	groups, err := gatherUnread(ctx, client, chByID, members, perChannel)
	if err != nil {
		return err
	}

	names, err := client.UsernamesByIDs(ctx, unreadUserIDs(groups, chByID, me.Id))
	if err != nil {
		return err
	}
	lbl := labeler{meID: me.Id, teamSlug: teamSlug, channels: chByID, names: names}

	switch {
	case len(groups) > 0 && asJSON:
		// JSON Lines drops the per-channel header/count rows; each line already
		// carries its channel, and the grouping is just create-order within a
		// channel anyway.
		for _, g := range groups {
			if err := writeJSONPosts(out, lbl.header, names, g.posts); err != nil {
				return err
			}
		}
	case len(groups) > 0:
		printUnread(out, lbl, groups, names)
	case !wait:
		fmt.Fprintln(os.Stderr, "matterbox: nothing unread")
	}

	if !wait {
		return nil
	}

	// --wait always blocks for a genuinely new message, then exits — matching
	// `read --wait`. Any existing unread above is context, not the thing we
	// were waiting for, so a backlog no longer makes --wait return instantly.
	// The cutoff is the later of "when we started" and the newest message we
	// just printed, so we never re-surface something already shown.
	for _, g := range groups {
		if a := g.lastActivity(); a > since {
			since = a
		}
	}
	if len(groups) > 0 {
		fmt.Fprintln(os.Stderr, "matterbox: waiting for a new message…")
	} else {
		fmt.Fprintln(os.Stderr, "matterbox: caught up — waiting for a new message…")
	}
	ev, p, err := awaitMessage(ctx, wsc, "", since, timeout, "")
	if err != nil {
		return err
	}
	// A caught-up wait won't have the DM partner resolved, so label the live
	// message from the event (sender_name) for both the text header and the
	// JSON channel field.
	liveLbl := channelLabeler(func(cid string) string { return lbl.headerForEvent(cid, ev, p) })
	return printLiveMessage(ctx, client, out, ev, p, lbl.headerForEvent(p.ChannelId, ev, p), asJSON, liveLbl)
}

// gatherUnread turns the member records into per-channel unread groups: for
// each channel with unread (or a mention), it fetches the posts past the
// read boundary, filters them, and caps to perChannel. Channels that turn
// out to have nothing genuinely unread are dropped. Groups come back
// sorted mentions-first, then most-recent activity.
func gatherUnread(ctx context.Context, client *mm.Client, chByID map[string]*model.Channel, members model.ChannelMembersWithTeamData, perChannel int) ([]unreadGroup, error) {
	var groups []unreadGroup
	for _, mb := range members {
		ch := chByID[mb.ChannelId]
		if ch == nil {
			continue
		}
		unread := int(ch.TotalMsgCount - mb.MsgCount)
		mention := int(mb.MentionCount)
		if unread <= 0 && mention <= 0 {
			continue
		}

		var (
			pl  *model.PostList
			err error
		)
		if mb.LastViewedAt > 0 {
			pl, err = client.PostsSince(ctx, mb.ChannelId, mb.LastViewedAt)
		} else {
			pl, err = client.Posts(ctx, mb.ChannelId, unreadFetchPage)
		}
		if err != nil {
			return nil, err
		}
		posts := unreadFromList(pl, mb.LastViewedAt)
		if len(posts) == 0 {
			continue
		}
		truncated := 0
		if perChannel > 0 && len(posts) > perChannel {
			truncated = len(posts) - perChannel
			posts = posts[len(posts)-perChannel:]
		}
		groups = append(groups, unreadGroup{
			channelID: mb.ChannelId,
			posts:     posts,
			total:     unread,
			mention:   mention,
			truncated: truncated,
		})
	}

	sort.SliceStable(groups, func(i, j int) bool {
		mi, mj := groups[i].mention > 0, groups[j].mention > 0
		if mi != mj {
			return mi
		}
		return groups[i].lastActivity() > groups[j].lastActivity()
	})
	return groups, nil
}

// unreadFromList flips a PostList into oldest→newest order, keeping only
// genuine unread messages: non-deleted, non-system posts created after the
// read boundary. A non-positive boundary keeps every returned post.
func unreadFromList(pl *model.PostList, lastViewedAt int64) []*model.Post {
	if pl == nil {
		return nil
	}
	out := make([]*model.Post, 0, len(pl.Order))
	for i := len(pl.Order) - 1; i >= 0; i-- {
		p := pl.Posts[pl.Order[i]]
		if p == nil || p.DeleteAt != 0 || p.Type != "" {
			continue
		}
		if lastViewedAt > 0 && p.CreateAt <= lastViewedAt {
			continue
		}
		out = append(out, p)
	}
	return out
}

// unreadUserIDs collects every id we need a username for: post authors plus
// the partner in each direct-message group.
func unreadUserIDs(groups []unreadGroup, chByID map[string]*model.Channel, meID string) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, g := range groups {
		for _, p := range g.posts {
			add(p.UserId)
		}
		if ch := chByID[g.channelID]; ch != nil && ch.Type == model.ChannelTypeDirect {
			add(dmPartnerID(ch, meID))
		}
	}
	return ids
}

// printUnread writes the grouped unread feed: a header line per channel
// ("eng/general · 3 new · 1 mention"), an optional truncation note, then
// the messages, with a blank line between groups.
func printUnread(out io.Writer, lbl labeler, groups []unreadGroup, names map[string]string) {
	for i, g := range groups {
		n := g.total
		if n <= 0 {
			n = len(g.posts)
		}
		header := fmt.Sprintf("%s · %d new", lbl.header(g.channelID), n)
		if g.mention > 0 {
			header += fmt.Sprintf(" · %s", plural(g.mention, "mention", "mentions"))
		}
		io.WriteString(out, header+"\n")
		if g.truncated > 0 {
			fmt.Fprintf(out, "  … +%d earlier unread\n", g.truncated)
		}
		io.WriteString(out, formatPosts(g.posts, names))
		if i < len(groups)-1 {
			io.WriteString(out, "\n")
		}
	}
}

// labeler turns a channel id into a human header. For normal channels that
// header is the team/channel address; for DMs it's @partner.
type labeler struct {
	meID     string
	teamSlug map[string]string         // teamID → URL slug
	channels map[string]*model.Channel // channelID → channel
	names    map[string]string         // userID → username
}

func (l labeler) header(channelID string) string {
	ch := l.channels[channelID]
	if ch == nil {
		return channelID
	}
	switch ch.Type {
	case model.ChannelTypeDirect:
		if n := l.names[dmPartnerID(ch, l.meID)]; n != "" {
			return "@" + n
		}
		return "@?"
	case model.ChannelTypeGroup:
		if ch.DisplayName != "" {
			return ch.DisplayName
		}
		return "(group)"
	default:
		if slug := l.teamSlug[ch.TeamId]; slug != "" {
			return slug + "/" + ch.Name
		}
		return ch.Name
	}
}

// headerForEvent labels a live-waited message. For DMs (or a channel we
// don't have locally) the event's sender_name is the most reliable label,
// since a caught-up wait won't have resolved DM partners.
func (l labeler) headerForEvent(channelID string, ev *model.WebSocketEvent, p *model.Post) string {
	if ov := overrideName(p); ov != "" {
		return "@" + ov
	}
	ch := l.channels[channelID]
	if ch == nil || ch.Type == model.ChannelTypeDirect {
		if sn, _ := ev.GetData()["sender_name"].(string); sn != "" {
			return "@" + strings.TrimPrefix(sn, "@")
		}
	}
	return l.header(channelID)
}

// dmPartnerID returns the other user in a direct-message channel (whose
// Name is "userA__userB"), or "" for a DM with yourself.
func dmPartnerID(ch *model.Channel, meID string) string {
	for _, id := range strings.Split(ch.Name, "__") {
		if id != "" && id != meID {
			return id
		}
	}
	return ""
}

// plural renders "1 mention" / "2 mentions".
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
