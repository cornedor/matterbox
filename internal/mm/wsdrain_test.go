package mm

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mattermost/mattermost/server/public/model"
)

// responseBufferCap mirrors the ResponseChannel buffer the driver allocates in
// makeClient. Nothing goes wrong until it fills, which is why a client that
// never drains it looks fine in every short-lived test.
const responseBufferCap = 100

// wsResponseFlood is a Mattermost-shaped WebSocket endpoint that answers with
// `responses` action replies and then pushes one `posted` event. Real servers
// interleave the two — every user_typing we send earns a reply — but stacking
// the replies first is the same situation the buffer sees, and it fails fast.
func wsResponseFlood(t *testing.T, responses int) *httptest.Server {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })

	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Soak up the auth challenge the driver sends on connect.
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		for i := range responses {
			if err := conn.WriteJSON(model.NewWebSocketResponse(model.StatusOk, int64(i+1), nil)); err != nil {
				return
			}
		}
		ev := model.NewWebSocketEvent(model.WebsocketEventPosted, "", "c1", "", nil, "")
		payload, err := ev.ToJSON()
		if err != nil {
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			return
		}
		// Hold the socket open, so a failure reads as "the event never came"
		// rather than as a disconnect.
		<-done
	}))
}

// The regression this guards: with ResponseChannel undrained, the driver's
// reader blocks on it forever once the buffer fills, and every event behind it
// — posts included — is never delivered. From the app's side messages simply
// stop arriving, with the socket still apparently up.
func TestDialWSDeliversEventsPastTheResponseBuffer(t *testing.T) {
	srv := wsResponseFlood(t, responseBufferCap*2)
	defer srv.Close()

	wsc, err := New(srv.URL, "tok").DialWS()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer wsc.Close()

	select {
	case ev, ok := <-wsc.EventChannel:
		if !ok {
			t.Fatal("event channel closed before the event arrived")
		}
		if ev.EventType() != model.WebsocketEventPosted {
			t.Errorf("got %s; want posted", ev.EventType())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no event after %d responses: the reader is wedged on ResponseChannel",
			responseBufferCap*2)
	}
}

// The drain must not outlive the socket, or every reconnect in a long session
// leaks a goroutine.
func TestDrainResponsesStopsWhenChannelCloses(t *testing.T) {
	ch := make(chan *model.WebSocketResponse, 1)
	done := make(chan struct{})
	go func() {
		drainResponses(ch)
		close(done)
	}()

	ch <- model.NewWebSocketResponse(model.StatusOk, 1, nil)
	close(ch)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain still running after the channel closed")
	}
}

// The driver closes its write channel when the socket dies and SendMessage
// sends on it unguarded, so a typing cue racing a disconnect panics on a closed
// channel — from a command goroutine, which takes the process down. Typing
// while the link drops is an ordinary thing to do.
func TestSendTypingSurvivesAClosedSocket(t *testing.T) {
	srv := wsResponseFlood(t, 0)
	defer srv.Close()

	wsc, err := New(srv.URL, "tok").DialWS()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	wsc.Close() // the disconnect the caller's captured pointer doesn't know about

	done := make(chan struct{})
	go func() {
		defer close(done)
		SendTyping(wsc, "c1", "")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SendTyping blocked on a dead socket")
	}
}

// Nothing to send on is not a crash either: the composer can outlive the socket
// entirely while the reconnect backs off.
func TestSendTypingOnNilSocket(t *testing.T) {
	SendTyping(nil, "c1", "")
}
