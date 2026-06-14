package listen

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/telegram"
)

// helpText is sent for /help, /start, and unknown commands.
const helpText = "matterbox bot:\n" +
	"• reply to a notification → posts back into that thread\n" +
	"• 👍 / ✓ buttons → react / mark the channel read\n\n" +
	"/unread — channels with unread messages and mentions\n" +
	"/search <words> — keyword search across your cached messages\n" +
	"/digest — a short summary of everything unread\n" +
	"/help — this message"

// digestPostCap bounds how many transcript lines /digest sends to the model.
const digestPostCap = 300

// handleCommand dispatches a slash command from the authorized chat.
func (e *Engine) handleCommand(ctx context.Context, msg *telegram.Message) {
	cmd, args := parseCommand(msg.Text)
	switch cmd {
	case "help", "start":
		e.sendTG(ctx, helpText)
	case "search", "s":
		e.cmdSearch(ctx, args)
	case "unread", "u":
		e.cmdUnread(ctx)
	case "digest", "d":
		e.cmdDigest(ctx)
	default:
		e.sendTG(ctx, "Unknown command.\n\n"+helpText)
	}
}

// cmdSearch runs a keyword search over the local cache and returns the top hits.
func (e *Engine) cmdSearch(ctx context.Context, query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		e.sendTG(ctx, "Usage: /search <words>")
		return
	}
	hits, err := e.store.Search(query, nil, 8, 0)
	if err != nil {
		e.sendTG(ctx, "Search failed: "+err.Error())
		return
	}
	if len(hits) == 0 {
		e.sendTG(ctx, "No matches for: "+query)
		return
	}
	var chIDs, authorIDs []string
	for _, h := range hits {
		if h.Match != nil {
			chIDs = append(chIDs, h.Match.ChannelId)
			authorIDs = append(authorIDs, h.Match.UserId)
		}
	}
	lbl := e.buildLabeler(ctx, chIDs, authorIDs)

	var b strings.Builder
	fmt.Fprintf(&b, "🔎 %s for %q:", plural(len(hits), "result", "results"), query)
	for _, h := range hits {
		p := h.Match
		if p == nil {
			continue
		}
		name := lbl.names[p.UserId]
		if name == "" {
			name = p.UserId
		}
		ts := time.UnixMilli(p.CreateAt).Local().Format("Jan 2 15:04")
		fmt.Fprintf(&b, "\n\n• %s · %s · @%s\n%s", lbl.label(p.ChannelId), ts, name, snippet(p.Message, 200))
	}
	e.sendTG(ctx, b.String())
}

// cmdUnread lists channels with unread messages / mentions, mentions first.
func (e *Engine) cmdUnread(ctx context.Context) {
	chByID, members, err := e.channelsAndMembers(ctx)
	if err != nil {
		e.sendTG(ctx, "Unread failed: "+err.Error())
		return
	}
	type row struct {
		channelID       string
		unread, mention int
	}
	var rows []row
	for _, mb := range members {
		ch := chByID[mb.ChannelId]
		if ch == nil {
			continue
		}
		unread := int(ch.TotalMsgCountRoot - mb.MsgCountRoot)
		mention := int(mb.MentionCountRoot)
		if unread <= 0 && mention <= 0 {
			continue
		}
		rows = append(rows, row{mb.ChannelId, unread, mention})
	}
	if len(rows) == 0 {
		e.sendTG(ctx, "✅ all caught up")
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		mi, mj := rows[i].mention > 0, rows[j].mention > 0
		if mi != mj {
			return mi
		}
		return rows[i].unread > rows[j].unread
	})

	chIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		chIDs = append(chIDs, r.channelID)
	}
	lbl := e.buildLabeler(ctx, chIDs, nil)

	var b strings.Builder
	b.WriteString("📬 Unread:")
	for _, r := range rows {
		line := "\n• " + lbl.label(r.channelID) + " — " + plural(r.unread, "new", "new")
		if r.mention > 0 {
			line += ", " + plural(r.mention, "mention", "mentions")
		}
		b.WriteString(line)
	}
	e.sendTG(ctx, b.String())
}

// cmdDigest summarizes everything unread into a short briefing.
func (e *Engine) cmdDigest(ctx context.Context) {
	if e.chat == nil {
		e.sendTG(ctx, "Digest needs the chat model configured (the summary endpoint).")
		return
	}
	chByID, members, err := e.channelsAndMembers(ctx)
	if err != nil {
		e.sendTG(ctx, "Digest failed: "+err.Error())
		return
	}
	type group struct {
		channelID string
		mention   int
		posts     []*model.Post
	}
	var groups []group
	var chIDs, authorIDs []string
	for _, mb := range members {
		ch := chByID[mb.ChannelId]
		if ch == nil {
			continue
		}
		unread := int(ch.TotalMsgCountRoot - mb.MsgCountRoot)
		mention := int(mb.MentionCountRoot)
		if unread <= 0 && mention <= 0 {
			continue
		}
		var pl *model.PostList
		if mb.LastViewedAt > 0 {
			pl, _ = e.client.PostsSince(ctx, mb.ChannelId, mb.LastViewedAt)
		} else {
			pl, _ = e.client.Posts(ctx, mb.ChannelId, 30)
		}
		posts := unreadPosts(pl, mb.LastViewedAt)
		if len(posts) == 0 {
			continue
		}
		groups = append(groups, group{mb.ChannelId, mention, posts})
		chIDs = append(chIDs, mb.ChannelId)
		for _, p := range posts {
			authorIDs = append(authorIDs, p.UserId)
		}
	}
	if len(groups) == 0 {
		e.sendTG(ctx, "✅ nothing unread to summarize")
		return
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].mention > groups[j].mention })
	lbl := e.buildLabeler(ctx, chIDs, authorIDs)

	var b strings.Builder
	lines := 0
	for _, g := range groups {
		if lines >= digestPostCap {
			break
		}
		fmt.Fprintf(&b, "\n## %s\n", lbl.label(g.channelID))
		for _, p := range g.posts {
			if line := postLine(p, lbl.names); line != "" {
				b.WriteString(line + "\n")
				lines++
			}
			if lines >= digestPostCap {
				break
			}
		}
	}
	system := e.opts.NotifyPrompt + fmt.Sprintf(
		"\n\nSummarize the unread Mattermost messages below for @%s as a short briefing "+
			"grouped by channel: the gist of each and any action items for them. Be concise.", e.me.Username)
	out, err := e.chat.Complete(ctx, system, b.String())
	if err != nil {
		e.sendTG(ctx, "Digest summary failed: "+err.Error())
		return
	}
	if strings.TrimSpace(out) == "" {
		out = "(the model returned an empty summary)"
	}
	e.sendTG(ctx, "📰 Digest:\n"+out)
}

// channelsAndMembers fetches the user's channels (indexed by id) and member
// records in one place for the unread/digest commands.
func (e *Engine) channelsAndMembers(ctx context.Context) (map[string]*model.Channel, model.ChannelMembersWithTeamData, error) {
	channels, err := e.client.AllChannels(ctx, e.me.Id)
	if err != nil {
		return nil, nil, err
	}
	members, err := e.client.ChannelMembers(ctx, e.me.Id)
	if err != nil {
		return nil, nil, err
	}
	chByID := make(map[string]*model.Channel, len(channels))
	for _, c := range channels {
		chByID[c.Id] = c
	}
	return chByID, members, nil
}

// chanLabeler turns a channel id into a human label (team/channel or @partner).
type chanLabeler struct {
	meID     string
	teamSlug map[string]string
	channels map[string]*model.Channel
	names    map[string]string
}

func (l chanLabeler) label(channelID string) string {
	ch := l.channels[channelID]
	if ch == nil {
		return channelID
	}
	switch ch.Type {
	case model.ChannelTypeDirect:
		if n := l.names[dmPartner(ch, l.meID)]; n != "" {
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

// buildLabeler loads channel + team metadata and resolves the usernames needed
// to label the given channels (DM partners) and post authors.
func (e *Engine) buildLabeler(ctx context.Context, channelIDs, authorIDs []string) chanLabeler {
	l := chanLabeler{
		meID:     e.me.Id,
		teamSlug: map[string]string{},
		channels: map[string]*model.Channel{},
		names:    map[string]string{},
	}
	channels, _ := e.client.AllChannels(ctx, e.me.Id)
	for _, c := range channels {
		l.channels[c.Id] = c
	}
	if teams, err := e.client.Teams(ctx, e.me.Id); err == nil {
		for _, t := range teams {
			l.teamSlug[t.Id] = t.Name
		}
	}
	need := map[string]bool{}
	var ids []string
	add := func(id string) {
		if id != "" && !need[id] {
			need[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range authorIDs {
		add(id)
	}
	for _, cid := range channelIDs {
		if c := l.channels[cid]; c != nil && c.Type == model.ChannelTypeDirect {
			add(dmPartner(c, e.me.Id))
		}
	}
	if names, err := e.client.UsernamesByIDs(ctx, ids); err == nil {
		for k, v := range names {
			l.names[k] = v
		}
	}
	return l
}

// dmPartner returns the other user id in a direct-message channel ("a__b"), or
// "" for a self-DM.
func dmPartner(ch *model.Channel, meID string) string {
	for _, id := range strings.Split(ch.Name, "__") {
		if id != "" && id != meID {
			return id
		}
	}
	return ""
}

// unreadPosts filters a PostList to genuine unread messages (after the read
// boundary; non-system, non-deleted, non-empty), oldest-first.
func unreadPosts(pl *model.PostList, lastViewedAt int64) []*model.Post {
	posts := postsByCreateAt(pl)
	out := posts[:0]
	for _, p := range posts {
		if p.DeleteAt != 0 || p.IsSystemMessage() || strings.TrimSpace(p.Message) == "" {
			continue
		}
		if lastViewedAt > 0 && p.CreateAt <= lastViewedAt {
			continue
		}
		out = append(out, p)
	}
	return out
}

// snippet collapses a message to a single trimmed line capped at max runes.
func snippet(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// plural renders "1 new" / "2 new".
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
