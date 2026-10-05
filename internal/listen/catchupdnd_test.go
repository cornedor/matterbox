package listen

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
	"matterbox/internal/telegram"
)

// TestCatchUpHonoursMutes: the reconnect digest goes through the same
// do-not-disturb policy as a live notification, so a missed mention in a
// muted channel is not pushed.
func TestCatchUpHonoursMutes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		muted     bool
		wantSends int32
	}{
		{"unmuted channel is pushed", false, 1},
		{"muted channel is held", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := &model.Channel{Id: "c1", Type: model.ChannelTypeOpen, DisplayName: "Eng", TeamId: "t1"}
			mb := model.ChannelMemberWithTeamData{ChannelMember: model.ChannelMember{
				ChannelId: "c1", UserId: "u-me", MentionCount: 1, MentionCountRoot: 1, LastViewedAt: 1000,
			}}
			post := &model.Post{Id: "p1", ChannelId: "c1", UserId: "u-bob", Message: "@corne can you look", CreateAt: time.Now().UnixMilli()}
			mmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch p := r.URL.Path; {
				case strings.Contains(p, "/channel_members"):
					_ = json.NewEncoder(w).Encode([]model.ChannelMemberWithTeamData{mb})
				case strings.HasSuffix(p, "/channels"):
					_ = json.NewEncoder(w).Encode([]*model.Channel{ch})
				case strings.Contains(p, "/posts"):
					pl := model.NewPostList()
					pl.AddPost(post)
					pl.AddOrder(post.Id)
					_ = json.NewEncoder(w).Encode(pl)
				default:
					io.WriteString(w, `[]`)
				}
			}))
			t.Cleanup(mmSrv.Close)
			var sends int32
			tgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/sendMessage") {
					atomic.AddInt32(&sends, 1)
				}
				io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
			}))
			t.Cleanup(tgSrv.Close)

			e := newStoreEngine(t)
			e.client = mm.New(mmSrv.URL, "tok")
			e.tg = telegram.NewWithBase("tok", tgSrv.URL)
			e.me = &model.User{Id: "u-me", Username: "corne"}
			e.opts = Options{NotifyOnMention: true, RespectMutes: true, TelegramChatID: "42"}
			e.rules = defaultRules(e.opts)
			if tc.muted {
				e.muted = map[string]bool{"c1": true}
			}

			e.catchUp(t.Context())

			if got := atomic.LoadInt32(&sends); got != tc.wantSends {
				t.Errorf("digest sends = %d, want %d", got, tc.wantSends)
			}
			if e.cursor() == 0 {
				t.Error("cursor not advanced; a held mention would be re-scanned forever")
			}
		})
	}
}
