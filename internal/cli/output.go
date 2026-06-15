package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"
)

// jsonPost is the stable line shape for --json (JSON Lines) output: one object
// per message. Usernames are resolved up front — DM channels otherwise surface
// as raw userid__userid — and each line carries both the channel's address
// label (eng/general, @alice) and its id, so a consumer can feed either back
// into read/send. Field order here is the on-the-wire order.
type jsonPost struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	Channel   string `json:"channel"`
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	Message   string `json:"message"`
	CreateAt  int64  `json:"create_at"`         // unix-ms, as Mattermost stores it
	Time      string `json:"time"`              // RFC3339 (local tz), for humans / jq
	RootID    string `json:"root_id,omitempty"` // set when the post is a thread reply
}

// channelLabeler maps a channel id to its human address. labeler.header
// satisfies it directly; read passes a one-channel closure since it never
// builds a full labeler.
type channelLabeler func(channelID string) string

// toJSONPost projects a post onto the wire shape, resolving the author's
// display name exactly as the text path does (a webhook/bot override_username
// wins over the id→name map) and the channel label via lbl.
func toJSONPost(p *model.Post, lbl channelLabeler, names map[string]string) jsonPost {
	name := overrideName(p)
	if name == "" {
		name = names[p.UserId]
	}
	return jsonPost{
		ID:        p.Id,
		ChannelID: p.ChannelId,
		Channel:   lbl(p.ChannelId),
		UserID:    p.UserId,
		Username:  name,
		Message:   p.Message,
		CreateAt:  p.CreateAt,
		Time:      time.UnixMilli(p.CreateAt).Format(time.RFC3339),
		RootID:    p.RootId,
	}
}

// writeJSONPosts emits one JSON object per post, newline-delimited (JSON
// Lines). Encoder.Encode appends the trailing newline, so the stream is ready
// for `jq -c` or `while read`. HTML escaping is off so message text reads back
// verbatim (& < > stay literal).
func writeJSONPosts(out io.Writer, lbl channelLabeler, names map[string]string, posts []*model.Post) error {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	for _, p := range posts {
		if err := enc.Encode(toJSONPost(p, lbl, names)); err != nil {
			return err
		}
	}
	return nil
}

// addOutputFlags registers the --json / -o,--output toggle on cmd and returns a
// resolver that reports whether JSON Lines output was requested. --json is a
// shorthand for --output json; an --output other than text/json is an error so
// a typo (`-o yaml`) fails loudly rather than silently falling back to text.
func addOutputFlags(cmd *cobra.Command) func() (bool, error) {
	var (
		jsonFlag bool
		output   string
	)
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "shorthand for --output json: one JSON object per message (JSON Lines)")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")
	return func() (bool, error) {
		if jsonFlag {
			return true, nil
		}
		switch strings.ToLower(strings.TrimSpace(output)) {
		case "", "text":
			return false, nil
		case "json":
			return true, nil
		default:
			return false, fmt.Errorf("unknown --output %q (want text or json)", output)
		}
	}
}
