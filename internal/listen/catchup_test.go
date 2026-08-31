package listen

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
	"matterbox/internal/telegram"
)

// catchupServer stands in for Mattermost with exactly what catchUp reads: one
// public channel the reader has an unread mention in, and a post fetch whose
// outcome the test controls. postsFail makes that fetch a 500 — the case that
// used to be indistinguishable from "nothing was missed".
func catchupServer(t *testing.T, postsFail bool) *mm.Client {
	t.Helper()
	ch := &model.Channel{Id: "c1", Type: model.ChannelTypeOpen, DisplayName: "Eng", TeamId: "t1"}
	mb := model.ChannelMemberWithTeamData{
		ChannelMember: model.ChannelMember{
			ChannelId: "c1", UserId: "u-me",
			MentionCount: 1, MentionCountRoot: 1,
			MsgCount: 0, LastViewedAt: 1000,
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case strings.Contains(p, "/channel_members"):
			_ = json.NewEncoder(w).Encode([]model.ChannelMemberWithTeamData{mb})
		case strings.HasSuffix(p, "/channels"):
			_ = json.NewEncoder(w).Encode([]*model.Channel{ch})
		case strings.Contains(p, "/posts"):
			if postsFail {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"message":"boom"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(&model.PostList{Order: []string{}, Posts: map[string]*model.Post{}})
		default:
			io.WriteString(w, `[]`)
		}
	}))
	t.Cleanup(srv.Close)
	return mm.New(srv.URL, "tok")
}

func catchupEngine(t *testing.T, postsFail bool) *Engine {
	t.Helper()
	e := newStoreEngine(t)
	e.client = catchupServer(t, postsFail)
	e.me = &model.User{Id: "u-me", Username: "corne"}
	e.rules = defaultRules(Options{NotifyOnMention: true})
	// Non-nil only to get past catchUp's guard; the paths under test return
	// before any delivery, so nothing reaches the network.
	e.tg = telegram.New("tok")
	return e
}

// TestCatchUpHoldsCursorOnFetchFailure pins the data-loss bug: a channel whose
// posts couldn't be fetched yields no items, and advancing the cursor on that
// silence buries those mentions permanently — the daemon would never look at
// that window again.
func TestCatchUpHoldsCursorOnFetchFailure(t *testing.T) {
	e := catchupEngine(t, true)
	e.catchUp(t.Context())
	if got := e.cursor(); got != 0 {
		t.Errorf("cursor advanced to %d after a failed fetch; missed mentions are now unreachable", got)
	}
}

// TestCatchUpAdvancesWhenClean is the counterpart: with every channel readable
// and nothing missed, the cursor must move, or every reconnect re-scans the
// same window forever.
func TestCatchUpAdvancesWhenClean(t *testing.T) {
	e := catchupEngine(t, false)
	e.catchUp(t.Context())
	if e.cursor() == 0 {
		t.Error("cursor stayed at 0 after a clean sweep with nothing to report")
	}
}

// TestSendTGReportsFailure guards the signal catchUp now gates the cursor on:
// a delivery that failed must not read as success.
func TestSendTGReportsFailure(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	e := &Engine{
		log:  log.New(io.Discard, "", 0),
		tg:   telegram.NewWithBase("tok", srv.URL),
		opts: Options{TelegramChatID: "42"},
	}
	if err := e.sendTG(t.Context(), "hi"); err == nil {
		t.Error("sendTG returned nil for a 502; catchUp would advance the cursor over an undelivered digest")
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Error("sendTG never reached the server")
	}
}
