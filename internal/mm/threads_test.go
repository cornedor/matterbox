package mm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// threadServer answers the thread-membership routes the way Mattermost does:
// 200 for a followed thread, 404 app.user.get_thread_membership_for_user.not_found
// otherwise. follows is mutated by the PUT/DELETE …/following routes.
func threadServer(t *testing.T, follows map[string]bool, teamCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/users/me/teams", func(w http.ResponseWriter, _ *http.Request) {
		teamCalls.Add(1)
		json.NewEncoder(w).Encode([]*model.Team{{Id: "team1"}})
	})
	mux.HandleFunc("GET /api/v4/users/me/teams/{team}/threads/{root}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("team") == "" {
			t.Errorf("thread lookup without a team")
		}
		if !follows[r.PathValue("root")] {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(model.NewAppError("GetThreadMembershipForUser", "app.user.get_thread_membership_for_user.not_found", nil, "", http.StatusNotFound))
			return
		}
		json.NewEncoder(w).Encode(model.ThreadResponse{PostId: r.PathValue("root")})
	})
	mux.HandleFunc("PUT /api/v4/users/me/teams/{team}/threads/{root}/following", func(w http.ResponseWriter, r *http.Request) {
		follows[r.PathValue("root")] = true
		w.Write([]byte(`{"status":"OK"}`))
	})
	mux.HandleFunc("DELETE /api/v4/users/me/teams/{team}/threads/{root}/following", func(w http.ResponseWriter, r *http.Request) {
		follows[r.PathValue("root")] = false
		w.Write([]byte(`{"status":"OK"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestThreadFollowing: a 404 is "not following", not an error; follow and
// unfollow round-trip; a DM thread ("" team) borrows one of the reader's teams,
// looked up once.
func TestThreadFollowing(t *testing.T) {
	var teamCalls atomic.Int32
	follows := map[string]bool{"r1": true}
	c := New(threadServer(t, follows, &teamCalls).URL, "tok")
	ctx := context.Background()

	if ok, err := c.ThreadFollowing(ctx, "teamX", "r1"); err != nil || !ok {
		t.Fatalf("followed thread: got %v, %v; want true, nil", ok, err)
	}
	if ok, err := c.ThreadFollowing(ctx, "", "r2"); err != nil || ok {
		t.Fatalf("unfollowed thread: got %v, %v; want false, nil", ok, err)
	}
	if err := c.SetThreadFollowing(ctx, "", "r2", true); err != nil {
		t.Fatalf("follow: %v", err)
	}
	if ok, _ := c.ThreadFollowing(ctx, "", "r2"); !ok {
		t.Error("after follow: following = false")
	}
	if err := c.SetThreadFollowing(ctx, "teamX", "r1", false); err != nil {
		t.Fatalf("unfollow: %v", err)
	}
	if ok, _ := c.ThreadFollowing(ctx, "teamX", "r1"); ok {
		t.Error("after unfollow: following = true")
	}
	if n := teamCalls.Load(); n != 1 {
		t.Errorf("team lookups = %d, want 1 (cached)", n)
	}
}
