package listen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// postFromEvent decodes the post embedded in a posted/edited websocket event
// (Mattermost JSON-encodes it into data["post"]). Returns nil if absent or
// unparseable. Mirrors the TUI's parsePost and the CLI's postFromEvent.
func postFromEvent(ev *model.WebSocketEvent) *model.Post {
	if ev == nil {
		return nil
	}
	raw, ok := ev.GetData()["post"].(string)
	if !ok || raw == "" {
		return nil
	}
	var p model.Post
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil
	}
	return &p
}

// wsMentions returns the set of user ids the server resolved as mentioned by
// the post (Mattermost JSON-encodes the list into data["mentions"]). For a
// broadcast like @channel/@here the server expands it to the affected members,
// so membership here alone does not mean the reader was named — see
// isDirectMention.
func wsMentions(ev *model.WebSocketEvent) map[string]bool {
	out := map[string]bool{}
	if ev == nil {
		return out
	}
	raw, ok := ev.GetData()["mentions"].(string)
	if !ok || raw == "" {
		return out
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// eventStr reads a string field from the event's data map ("" if absent).
func eventStr(ev *model.WebSocketEvent, key string) string {
	if ev == nil {
		return ""
	}
	s, _ := ev.GetData()[key].(string)
	return s
}

// isDirectMention reports whether the post should trigger a notification for
// the reader (meID / meUsername). True when:
//   - it's a direct message (channel_type "D") from someone else, or
//   - the reader was server-resolved as mentioned AND named by @username in the
//     text — which excludes broad @channel/@here/@all mentions that merely
//     expand to include the reader.
//
// The reader's own posts, system messages, deleted posts, and empty bodies
// never trigger.
func isDirectMention(ev *model.WebSocketEvent, p *model.Post, meID, meUsername string) bool {
	if ev == nil || p == nil || meID == "" {
		return false
	}
	if p.UserId == meID || p.DeleteAt != 0 || p.IsSystemMessage() {
		return false
	}
	if strings.TrimSpace(p.Message) == "" {
		return false
	}
	if eventStr(ev, "channel_type") == string(model.ChannelTypeDirect) {
		return true
	}
	return wsMentions(ev)[meID] && mentionsName(p.Message, meUsername)
}

// mentionsName reports whether msg names @username as a whole token (case-
// insensitive). Used to distinguish an explicit personal mention from a broad
// @channel mention that merely resolved to include the reader.
func mentionsName(msg, username string) bool {
	if username == "" {
		return false
	}
	re, err := regexp.Compile(`(?i)@` + regexp.QuoteMeta(username) + `\b`)
	if err != nil {
		return false
	}
	return re.MatchString(msg)
}

// channelLabel renders a short, human source line for the notification header,
// e.g. "DM from @alice" or "@alice in Engineering". senderFallback (a resolved
// "@name") is used when the event omits sender_name.
func channelLabel(ev *model.WebSocketEvent, senderFallback string) string {
	sender := eventStr(ev, "sender_name")
	if sender == "" {
		sender = senderFallback
	}
	if sender == "" {
		sender = "someone"
	}
	if eventStr(ev, "channel_type") == string(model.ChannelTypeDirect) {
		return "DM from " + sender
	}
	ch := eventStr(ev, "channel_display_name")
	if ch == "" {
		ch = "a channel"
	}
	return sender + " in " + ch
}

// postsByCreateAt collects the posts from a PostList sorted oldest-first,
// regardless of the list's own Order (which differs between the channel and
// thread endpoints). Nil/empty lists yield nil.
func postsByCreateAt(pl *model.PostList) []*model.Post {
	if pl == nil || len(pl.Posts) == 0 {
		return nil
	}
	out := make([]*model.Post, 0, len(pl.Posts))
	for _, p := range pl.Posts {
		if p != nil {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreateAt < out[j].CreateAt })
	return out
}

// transcript renders posts (oldest-first) as "[15:04] @name: body" lines,
// skipping system/deleted/empty posts. names maps user id → username.
func transcript(posts []*model.Post, names map[string]string) string {
	var b strings.Builder
	for _, p := range posts {
		if line := postLine(p, names); line != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// postLine formats one post for a transcript, or "" for posts that shouldn't
// appear (system / deleted / empty body).
func postLine(p *model.Post, names map[string]string) string {
	if p == nil || p.DeleteAt != 0 || p.IsSystemMessage() {
		return ""
	}
	body := strings.TrimSpace(p.Message)
	if body == "" {
		return ""
	}
	name := names[p.UserId]
	if name == "" {
		name = p.UserId
	}
	ts := time.UnixMilli(p.CreateAt).Local().Format("15:04")
	return fmt.Sprintf("[%s] @%s: %s", ts, name, body)
}

// uniqueUserIDs returns the distinct author ids across posts, for a single
// bulk username lookup.
func uniqueUserIDs(posts []*model.Post) []string {
	seen := map[string]bool{}
	var ids []string
	for _, p := range posts {
		if p != nil && p.UserId != "" && !seen[p.UserId] {
			seen[p.UserId] = true
			ids = append(ids, p.UserId)
		}
	}
	return ids
}
