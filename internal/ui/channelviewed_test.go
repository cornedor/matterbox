package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// viewedEvent builds a multiple_channels_viewed WS event carrying the given
// channelID → last-viewed timestamps. Values are float64 to mirror the
// JSON-decoded shape the event actually arrives in over the wire.
func viewedEvent(times map[string]int64) *model.WebSocketEvent {
	ev := model.NewWebSocketEvent(model.WebsocketEventMultipleChannelsViewed, "", "", "me", nil, "")
	data := make(map[string]any, len(times))
	for id, t := range times {
		data[id] = float64(t)
	}
	ev.Add("channel_times", data)
	return ev
}

// A view that caught up to the channel's newest known post clears the live
// unread/mention badge and drops the feed bubble — the cross-session
// reconciliation that fixes "badge says 1, feed empty".
func TestMultipleChannelsViewedClearsCaughtUp(t *testing.T) {
	m := feedMouseModel(1)
	m.mentions["c0"] = 1
	m.findChannel("c0").LastPostAt = 100 // newest post we know about

	m.applyMultipleChannelsViewed(viewedEvent(map[string]int64{"c0": 100}))

	if _, ok := m.unread["c0"]; ok {
		t.Fatalf("expected c0 unread cleared after a caught-up view")
	}
	if _, ok := m.mentions["c0"]; ok {
		t.Fatalf("expected c0 mention cleared after a caught-up view")
	}
	if len(m.feed.entries) != 0 {
		t.Fatalf("expected the c0 feed bubble dropped, got %d entries", len(m.feed.entries))
	}
}

// A post newer than the reported view means the viewing session hadn't seen
// it: genuine unread remains, so the badge and bubble must survive.
func TestMultipleChannelsViewedKeepsNewerPost(t *testing.T) {
	m := feedMouseModel(1)
	m.findChannel("c0").LastPostAt = 500 // a post landed after the other session's view

	m.applyMultipleChannelsViewed(viewedEvent(map[string]int64{"c0": 100}))

	if m.unread["c0"] != 1 {
		t.Fatalf("expected c0 to stay unread when a newer post exists, got %d", m.unread["c0"])
	}
	if len(m.feed.entries) != 1 {
		t.Fatalf("expected the c0 feed bubble kept, got %d entries", len(m.feed.entries))
	}
}

// Our own view echoed back (the channel is already read locally) is a
// harmless no-op and must not disturb other channels' state.
func TestMultipleChannelsViewedOwnEchoNoop(t *testing.T) {
	m := feedMouseModel(2)
	delete(m.unread, "c0") // c0 already read locally
	m.findChannel("c0").LastPostAt = 100
	m.findChannel("c1").LastPostAt = 100

	m.applyMultipleChannelsViewed(viewedEvent(map[string]int64{"c0": 100}))

	if m.unread["c1"] != 1 {
		t.Fatalf("c1 must be untouched, got %d", m.unread["c1"])
	}
	if len(m.feed.entries) != 2 {
		t.Fatalf("expected both feed bubbles intact, got %d", len(m.feed.entries))
	}
}

// wsChannelTimes parses both the nested-object form (numbers decode to
// float64) and a JSON-string fallback into the same map.
func TestWSChannelTimesParsing(t *testing.T) {
	nested := model.NewWebSocketEvent(model.WebsocketEventMultipleChannelsViewed, "", "", "me", nil, "")
	nested.Add("channel_times", map[string]any{"a": float64(10), "b": float64(20)})
	got := wsChannelTimes(nested)
	if got["a"] != 10 || got["b"] != 20 {
		t.Fatalf("nested parse = %v, want a=10 b=20", got)
	}

	str := model.NewWebSocketEvent(model.WebsocketEventMultipleChannelsViewed, "", "", "me", nil, "")
	str.Add("channel_times", `{"a":10,"b":20}`)
	got = wsChannelTimes(str)
	if got["a"] != 10 || got["b"] != 20 {
		t.Fatalf("string parse = %v, want a=10 b=20", got)
	}

	none := model.NewWebSocketEvent(model.WebsocketEventMultipleChannelsViewed, "", "", "me", nil, "")
	if len(wsChannelTimes(none)) != 0 {
		t.Fatalf("missing payload should parse to empty map")
	}
}
