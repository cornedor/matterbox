package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// postedEvent builds a "posted" websocket event for channelID carrying
// the given post, mirroring what the server broadcasts.
func postedEvent(t *testing.T, channelID string, p *model.Post) *model.WebSocketEvent {
	t.Helper()
	ev := model.NewWebSocketEvent(model.WebsocketEventPosted, "", channelID, "", nil, "")
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal post: %v", err)
	}
	ev.Add("post", string(b))
	return ev
}

func TestNewMessageFromEvent(t *testing.T) {
	const ch = "chan1"
	const since = int64(1000)

	t.Run("new message in channel is returned", func(t *testing.T) {
		ev := postedEvent(t, ch, &model.Post{Id: "p1", Message: "hi", CreateAt: since + 1})
		if got := newMessageFromEvent(ev, ch, since); got == nil || got.Id != "p1" {
			t.Fatalf("got %+v, want post p1", got)
		}
	})

	t.Run("post at or before cutoff is skipped (dedupe gap)", func(t *testing.T) {
		ev := postedEvent(t, ch, &model.Post{Id: "old", CreateAt: since})
		if got := newMessageFromEvent(ev, ch, since); got != nil {
			t.Errorf("got %+v, want nil for create_at == since", got)
		}
	})

	t.Run("other channel is skipped when scoped", func(t *testing.T) {
		ev := postedEvent(t, "other", &model.Post{Id: "p2", CreateAt: since + 1})
		if got := newMessageFromEvent(ev, ch, since); got != nil {
			t.Errorf("got %+v, want nil for a different channel", got)
		}
	})

	t.Run("any channel matches when channelID empty", func(t *testing.T) {
		ev := postedEvent(t, "other", &model.Post{Id: "p3", CreateAt: since + 1})
		if got := newMessageFromEvent(ev, "", since); got == nil || got.Id != "p3" {
			t.Fatalf("got %+v, want post p3 for empty channelID", got)
		}
	})

	t.Run("non-posted event is skipped", func(t *testing.T) {
		ev := model.NewWebSocketEvent(model.WebsocketEventTyping, "", ch, "", nil, "")
		if got := newMessageFromEvent(ev, ch, since); got != nil {
			t.Errorf("got %+v, want nil for a typing event", got)
		}
	})

	t.Run("nil event is skipped", func(t *testing.T) {
		if got := newMessageFromEvent(nil, ch, since); got != nil {
			t.Errorf("got %+v, want nil for nil event", got)
		}
	})
}

func TestPostFromEvent(t *testing.T) {
	ev := postedEvent(t, "c", &model.Post{Id: "p9", Message: "body"})
	p := postFromEvent(ev)
	if p == nil || p.Id != "p9" || p.Message != "body" {
		t.Fatalf("postFromEvent = %+v, want id p9 / body", p)
	}

	empty := model.NewWebSocketEvent(model.WebsocketEventPosted, "", "c", "", nil, "")
	if got := postFromEvent(empty); got != nil {
		t.Errorf("postFromEvent(no post data) = %+v, want nil", got)
	}
}

func TestAwaitMessage(t *testing.T) {
	const ch = "chan1"
	const since = int64(1000)

	t.Run("returns the first matching message, skipping noise", func(t *testing.T) {
		wsc := &model.WebSocketClient{EventChannel: make(chan *model.WebSocketEvent, 4)}
		wsc.EventChannel <- model.NewWebSocketEvent(model.WebsocketEventTyping, "", ch, "", nil, "")
		wsc.EventChannel <- postedEvent(t, "other", &model.Post{Id: "skip", CreateAt: since + 1})
		wsc.EventChannel <- postedEvent(t, ch, &model.Post{Id: "want", CreateAt: since + 1})

		_, p, err := awaitMessage(context.Background(), wsc, ch, since, time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Id != "want" {
			t.Errorf("got post %q, want want", p.Id)
		}
	})

	t.Run("times out", func(t *testing.T) {
		wsc := &model.WebSocketClient{EventChannel: make(chan *model.WebSocketEvent)}
		_, _, err := awaitMessage(context.Background(), wsc, ch, since, 20*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("err = %v, want a timeout error", err)
		}
	})

	t.Run("errors when the socket closes", func(t *testing.T) {
		wsc := &model.WebSocketClient{EventChannel: make(chan *model.WebSocketEvent)}
		close(wsc.EventChannel)
		_, _, err := awaitMessage(context.Background(), wsc, ch, since, 0)
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("err = %v, want a closed-socket error", err)
		}
	})

	t.Run("honours context cancellation", func(t *testing.T) {
		wsc := &model.WebSocketClient{EventChannel: make(chan *model.WebSocketEvent)}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := awaitMessage(ctx, wsc, ch, since, 0); err == nil {
			t.Fatal("expected context cancellation error, got nil")
		}
	})
}
