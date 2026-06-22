package listen

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
	"matterbox/internal/store"
)

func postListJSON(t *testing.T, next string, posts ...*model.Post) []byte {
	t.Helper()
	pl := &model.PostList{
		Order:      make([]string, 0, len(posts)),
		Posts:      make(map[string]*model.Post, len(posts)),
		NextPostId: next,
	}
	for _, p := range posts {
		pl.Order = append(pl.Order, p.Id)
		pl.Posts[p.Id] = p
	}
	b, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal post list: %v", err)
	}
	return b
}

// TestBackfillDrainsAndSeeds is the regression for the reconnect cache gap: on
// (re)connect the daemon must ingest the posts each channel gained while it was
// disconnected, independent of notification settings. A channel with cached
// history is drained forward from its newest cached post (PostsAfter); a channel
// with no baseline but recent activity is seeded (PostsSince); a channel whose
// newest post we already hold is skipped without a fetch.
func TestBackfillDrainsAndSeeds(t *testing.T) {
	now := time.Now().UnixMilli()
	hour := int64(60 * 60 * 1000)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// c1 already has a cached post; the gap is everything after it.
	cached := &model.Post{Id: "p1aaaaaaaaaaaaaaaaaaaaaaaa", ChannelId: "c1", UserId: "u1", Message: "cached", CreateAt: now - hour, UpdateAt: now - hour}
	if err := st.UpsertMany([]*model.Post{cached}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	gap2 := &model.Post{Id: "p2aaaaaaaaaaaaaaaaaaaaaaaa", ChannelId: "c1", UserId: "u1", Message: "gap-a", CreateAt: now - 30*60*1000, UpdateAt: now - 30*60*1000}
	gap3 := &model.Post{Id: "p3aaaaaaaaaaaaaaaaaaaaaaaa", ChannelId: "c1", UserId: "u1", Message: "gap-b", CreateAt: now - 10*60*1000, UpdateAt: now - 10*60*1000}
	// c3 has no cached history but recent activity — must be seeded.
	seed := &model.Post{Id: "p9aaaaaaaaaaaaaaaaaaaaaaaa", ChannelId: "c3", UserId: "u1", Message: "seeded", CreateAt: now - 5*60*1000, UpdateAt: now - 5*60*1000}

	channelsJSON, err := json.Marshal([]*model.Channel{
		{Id: "c1", LastPostAt: now},             // has a gap past the cached post
		{Id: "c2", LastPostAt: now - 8*24*hour}, // older than the floor → skip
		{Id: "c3", LastPostAt: now},             // never cached, recent → seed
	})
	if err != nil {
		t.Fatalf("marshal channels: %v", err)
	}

	var (
		mu       sync.Mutex
		reqPaths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		reqPaths = append(reqPaths, r.URL.Path+"?"+q.Encode())
		mu.Unlock()
		switch {
		case r.URL.Path == "/api/v4/users/me/channels":
			_, _ = w.Write(channelsJSON)
		case r.URL.Path == "/api/v4/channels/c1/posts" && q.Get("after") == cached.Id:
			_, _ = w.Write(postListJSON(t, "", gap2, gap3))
		case r.URL.Path == "/api/v4/channels/c3/posts" && q.Get("since") != "":
			_, _ = w.Write(postListJSON(t, "", seed))
		default:
			http.Error(w, "unexpected: "+r.URL.Path+"?"+q.Encode(), http.StatusNotFound)
		}
	}))
	defer srv.Close()

	e := &Engine{
		client: mm.New(srv.URL, "token"),
		store:  st,
		me:     &model.User{Id: "me", Username: "me"},
		log:    log.New(io.Discard, "", 0),
	}
	e.backfill(context.Background())

	// c1's gap posts are now cached.
	got, err := st.RecentForChannel("c1", 10)
	if err != nil {
		t.Fatalf("recent c1: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("c1 want 3 posts after backfill, got %d", len(got))
	}
	if got[2].Id != gap3.Id || got[2].Message != "gap-b" {
		t.Errorf("c1 newest = %q/%q, want gap-b", got[2].Id, got[2].Message)
	}

	// c3 was seeded from scratch.
	got3, err := st.RecentForChannel("c3", 10)
	if err != nil {
		t.Fatalf("recent c3: %v", err)
	}
	if len(got3) != 1 || got3[0].Id != seed.Id {
		t.Fatalf("c3 want seeded post, got %d posts", len(got3))
	}

	// c2 must not have been fetched (no activity past the floor).
	mu.Lock()
	defer mu.Unlock()
	for _, p := range reqPaths {
		if containsChannel(p, "c2") {
			t.Errorf("c2 should have been skipped, but was fetched: %s", p)
		}
	}

	// Watermark advanced so a later reconnect won't rescan back to this point.
	v, ok, err := st.GetMeta(ingestCursorKey)
	if err != nil || !ok {
		t.Fatalf("ingest cursor not persisted: ok=%v err=%v", ok, err)
	}
	if ms, _ := strconv.ParseInt(v, 10, 64); ms < now {
		t.Errorf("watermark = %s, want >= %d", v, now)
	}
}

func containsChannel(path, ch string) bool {
	return len(path) >= len("/api/v4/channels/"+ch+"/") &&
		path[:len("/api/v4/channels/"+ch+"/")] == "/api/v4/channels/"+ch+"/"
}
