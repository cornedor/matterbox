package listen

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
	"matterbox/internal/telegram"
)

// TestInboundHandlerTimesOut: a hung Mattermost call must not wedge the
// Telegram poll loop, which runs handlers one at a time.
func TestInboundHandlerTimesOut(t *testing.T) {
	release := make(chan struct{})
	mmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // hang until the client gives up (or the test ends)
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer mmSrv.Close()
	defer close(release)
	tgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer tgSrv.Close()

	defer func(d time.Duration) { inboundTimeout = d }(inboundTimeout)
	inboundTimeout = 100 * time.Millisecond

	e := &Engine{
		client: mm.New(mmSrv.URL, "tok"),
		tg:     telegram.NewWithBase("tok", tgSrv.URL),
		me:     &model.User{Id: "u-me"},
		log:    log.New(io.Discard, "", 0),
	}
	done := make(chan struct{})
	go func() {
		e.handleCallback(t.Context(), &telegram.CallbackQuery{ID: "cb", Data: "r:c1"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleCallback still blocked on a hung Mattermost call")
	}
}
