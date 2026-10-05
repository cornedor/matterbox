package mm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestPostsCreatedSincePages: GetPostsSince is capped server-side at an
// arbitrary 1000 rows, so a busy window must be paged with the before-cursor
// until it reaches the boundary.
func TestPostsCreatedSincePages(t *testing.T) {
	// 450 posts, newest first: p449 (create_at 449) … p0 (create_at 0).
	const n = 450
	order := make([]string, n)
	for i := range order {
		order[i] = fmt.Sprintf("p%d", n-1-i)
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		start := 0
		if before := r.URL.Query().Get("before"); before != "" {
			for i, id := range order {
				if id == before {
					start = i + 1
				}
			}
		}
		end := min(start+perPage, n)
		pl := model.NewPostList()
		for _, id := range order[start:end] {
			ts, _ := strconv.Atoi(id[1:])
			pl.AddPost(&model.Post{Id: id, ChannelId: "c1", CreateAt: int64(ts)})
			pl.AddOrder(id)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pl)
	}))
	defer srv.Close()

	pl, err := New(srv.URL, "tok").PostsCreatedSince(context.Background(), "c1", 30)
	if err != nil {
		t.Fatalf("PostsCreatedSince: %v", err)
	}
	if len(pl.Order) != n-30 {
		t.Fatalf("got %d posts, want %d (everything created at or after 30)", len(pl.Order), n-30)
	}
	if pl.Order[0] != "p449" || pl.Order[len(pl.Order)-1] != "p30" {
		t.Errorf("order = %s … %s, want p449 … p30 (newest first)", pl.Order[0], pl.Order[len(pl.Order)-1])
	}
	if requests != 3 {
		t.Errorf("made %d requests, want 3 (stop at the page that crosses the boundary)", requests)
	}
}
