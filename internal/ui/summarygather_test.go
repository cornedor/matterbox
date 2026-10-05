package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

// TestSummaryGatherPagesWholeWindow: the summary covers every post created in
// the window, past one server page and without the update_at-keyed (and
// 1000-row capped) posts?since endpoint.
func TestSummaryGatherPagesWholeWindow(t *testing.T) {
	const n, since = 450, 100 // posts p0…p449 at create_at 0…449; window is 100+
	order := make([]string, n)
	for i := range order {
		order[i] = fmt.Sprintf("p%d", n-1-i) // newest first
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("since") != "" {
			t.Errorf("summary used posts?since")
		}
		perPage, _ := strconv.Atoi(q.Get("per_page"))
		start := 0
		for i, id := range order {
			if id == q.Get("before") {
				start = i + 1
			}
		}
		pl := model.NewPostList()
		for _, id := range order[start:min(start+perPage, n)] {
			ts, _ := strconv.Atoi(id[1:])
			pl.AddPost(&model.Post{Id: id, ChannelId: "c1", UserId: "u1", Message: "msg " + id, CreateAt: int64(ts)})
			pl.AddOrder(id)
		}
		_ = json.NewEncoder(w).Encode(pl)
	}))
	defer srv.Close()

	m := Model{client: mm.New(srv.URL, "token"), ctx: context.Background()}
	msg := m.summaryGatherChannelCmd(1, "c1", since, map[string]string{"u1": "alice"})()
	got, ok := msg.(summaryGatheredMsg)
	if !ok || got.err != nil {
		t.Fatalf("gather: %#v", msg)
	}
	if got.count != n-since {
		t.Errorf("summarized %d posts, want %d (the whole window)", got.count, n-since)
	}
}
