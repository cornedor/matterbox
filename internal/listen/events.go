package listen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	emoji "github.com/kyokomi/emoji/v2"
	"github.com/mattermost/mattermost/server/public/model"
)

// emojiNames maps a unicode emoji (as Telegram sends it, with or without the
// U+FE0F variation selector) to a Mattermost reaction shortcode. Built once
// from the shared gemoji data, with stable overrides for the common reactions
// whose canonical alias would otherwise be a less-recognizable synonym.
var emojiNames = buildEmojiNames()

func buildEmojiNames() map[string]string {
	m := make(map[string]string)
	put := func(ch, name string) {
		m[ch] = name
		if stripped := strings.ReplaceAll(ch, "\uFE0F", ""); stripped != ch {
			m[stripped] = name
		}
	}
	for ch, aliases := range emoji.RevCodeMap() {
		if len(aliases) > 0 {
			put(ch, strings.Trim(aliases[0], ":"))
		}
	}
	// Mattermost-preferred names (RevCodeMap may pick a synonym, and its alias
	// order isn't guaranteed); pin the common reactions.
	for ch, name := range map[string]string{
		"👍": "+1", "👎": "-1", "❤\uFE0F": "heart", "🎉": "tada", "😂": "joy", "🔥": "fire",
	} {
		put(ch, name)
	}
	return m
}

// mattermostEmojiName converts a Telegram reaction emoji to a Mattermost
// shortcode (no colons), tolerating the optional variation selector.
func mattermostEmojiName(tgEmoji string) (string, bool) {
	if n, ok := emojiNames[tgEmoji]; ok {
		return n, true
	}
	n, ok := emojiNames[strings.ReplaceAll(tgEmoji, "\uFE0F", "")]
	return n, ok
}

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
// System messages, deleted posts, and empty bodies never trigger. The reader's
// own posts are skipped too, unless notifySelf is set — a testing affordance
// (post in your self-DM to exercise the whole pipeline) that some also use as a
// note-to-self relay.
func isDirectMention(ev *model.WebSocketEvent, p *model.Post, meID, meUsername string, notifySelf bool) bool {
	if ev == nil || p == nil || meID == "" {
		return false
	}
	if p.UserId == meID && !notifySelf {
		return false
	}
	if p.DeleteAt != 0 || p.IsSystemMessage() {
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

// parseQuietHours parses a "HH:MM-HH:MM" window into start/end minutes-of-day.
// ok is false for an empty or malformed string (treated as "no quiet hours").
func parseQuietHours(s string) (start, end int, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	a, b, found := strings.Cut(s, "-")
	if !found {
		return 0, 0, false
	}
	start, ok1 := parseHHMM(a)
	end, ok2 := parseHHMM(b)
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	return start, end, true
}

func parseHHMM(s string) (int, bool) {
	h, m, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found {
		return 0, false
	}
	hh, err1 := strconv.Atoi(strings.TrimSpace(h))
	mm, err2 := strconv.Atoi(strings.TrimSpace(m))
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// inQuietHours reports whether minute-of-day m lies in [start, end), handling a
// window that wraps past midnight (start > end). An empty window is never quiet.
func inQuietHours(m, start, end int) bool {
	if start == end {
		return false
	}
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// decodeCallback splits inline-button callback_data of the form "action:arg".
func decodeCallback(data string) (action, arg string) {
	action, arg, _ = strings.Cut(data, ":")
	return action, arg
}

// parseCommand splits a "/cmd args" message into the lowercased command (no
// leading slash, no "@botname" suffix) and the trimmed argument string. Returns
// empty cmd when text isn't a command.
func parseCommand(text string) (cmd, args string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	cmd, args, _ = strings.Cut(text[1:], " ")
	if at := strings.IndexByte(cmd, '@'); at >= 0 {
		cmd = cmd[:at] // Telegram appends @botname in groups
	}
	return strings.ToLower(cmd), strings.TrimSpace(args)
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
