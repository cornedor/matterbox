package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

// awaitMessage blocks on the websocket until a posted event with
// create_at > since arrives, and returns the event plus the decoded post.
// channelID scopes the wait to one channel; an empty channelID matches a
// new message in any channel. A non-zero timeout caps the wait; 0 waits
// forever. The websocket closing before a message arrives is an error.
//
// It only selects events off the channel — the connection itself is owned
// by the caller — so it's exercised in tests with a hand-fed EventChannel.
func awaitMessage(ctx context.Context, wsc *model.WebSocketClient, channelID string, since int64, timeout time.Duration) (*model.WebSocketEvent, *model.Post, error) {
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timeoutCh = t.C
	}
	for {
		select {
		case ev, ok := <-wsc.EventChannel:
			if !ok {
				return nil, nil, fmt.Errorf("websocket closed before a new message arrived")
			}
			if p := newMessageFromEvent(ev, channelID, since); p != nil {
				return ev, p, nil
			}
		case <-timeoutCh:
			return nil, nil, fmt.Errorf("timed out after %s waiting for a new message", timeout)
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// newMessageFromEvent returns the post to print if ev is a brand-new
// message created after since, or nil to skip the event. channelID scopes
// to one channel; "" matches any. Pure, so the gap/dedupe logic is
// unit-testable.
func newMessageFromEvent(ev *model.WebSocketEvent, channelID string, since int64) *model.Post {
	if ev == nil || ev.EventType() != model.WebsocketEventPosted {
		return nil
	}
	if channelID != "" {
		if b := ev.GetBroadcast(); b == nil || b.ChannelId != channelID {
			return nil
		}
	}
	p := postFromEvent(ev)
	if p == nil || p.CreateAt <= since {
		return nil
	}
	return p
}

// postFromEvent decodes the post embedded in a posted/edited websocket
// event (Mattermost JSON-encodes it into data["post"]).
func postFromEvent(ev *model.WebSocketEvent) *model.Post {
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

// eventNames builds the id→username map for rendering a single live post.
// Posted events usually carry the sender's name in data["sender_name"]
// ("@alice"), saving a lookup; otherwise we resolve it via the API.
func eventNames(ctx context.Context, client *mm.Client, ev *model.WebSocketEvent, p *model.Post) map[string]string {
	if sn, ok := ev.GetData()["sender_name"].(string); ok && sn != "" {
		return map[string]string{p.UserId: strings.TrimPrefix(sn, "@")}
	}
	if names, err := client.UsernamesByIDs(ctx, []string{p.UserId}); err == nil {
		return names
	}
	return nil
}

// printLiveMessage renders one freshly-arrived post to out, optionally
// prefixed with a channel header line (used by `unread --wait`, which can
// surface a message from any channel; `read --wait` passes an empty
// header since the channel is already known).
func printLiveMessage(ctx context.Context, client *mm.Client, out io.Writer, ev *model.WebSocketEvent, p *model.Post, header string) error {
	body := formatPosts([]*model.Post{p}, eventNames(ctx, client, ev, p))
	if header != "" {
		body = header + "\n" + body
	}
	_, err := io.WriteString(out, body)
	return err
}
