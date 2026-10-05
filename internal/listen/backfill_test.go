package listen

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/mm"
)

// backfillServer has two channels: c1 got traffic during the gap (a new post
// and a deletion), c2 has been quiet since before it. postsFail makes c1's
// fetch a 500. It records which channels' posts were requested.
func backfillServer(t *testing.T, gapStart int64, postsFail bool) (*mm.Client, func() []string) {
	t.Helper()
	var (
		mu        sync.Mutex
		requested []string
	)
	channels := []*model.Channel{
		{Id: "c1", Type: model.ChannelTypeOpen, LastPostAt: gapStart + 100},
		{Id: "c2", Type: model.ChannelTypeOpen, LastPostAt: gapStart - 100},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/channels"):
			_ = json.NewEncoder(w).Encode(channels)
		case strings.HasSuffix(p, "/posts"):
			ch := strings.Split(p, "/")[4] // /api/v4/channels/<id>/posts
			mu.Lock()
			requested = append(requested, ch)
			mu.Unlock()
			if postsFail {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"message":"boom"}`)
				return
			}
			if since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64); since == 0 {
				t.Errorf("backfill fetched %s without since", ch)
			}
			pl := model.NewPostList()
			pl.AddPost(&model.Post{Id: "new", ChannelId: ch, UserId: "u2", Message: "while you were away", CreateAt: gapStart + 100, UpdateAt: gapStart + 100})
			pl.AddOrder("new")
			pl.AddPost(&model.Post{Id: "gone", ChannelId: ch, UserId: "u2", CreateAt: gapStart - 5000, UpdateAt: gapStart + 50, DeleteAt: gapStart + 50})
			_ = json.NewEncoder(w).Encode(pl)
		default:
			io.WriteString(w, `[]`)
		}
	}))
	t.Cleanup(srv.Close)
	return mm.New(srv.URL, "tok"), func() []string { mu.Lock(); defer mu.Unlock(); return requested }
}

func backfillEngine(t *testing.T, postsFail bool) (*Engine, int64, func() []string) {
	t.Helper()
	e := newStoreEngine(t)
	e.me = &model.User{Id: "u-me", Username: "corne"}
	gapStart := time.Now().Add(-time.Hour).UnixMilli()
	if err := e.store.SetMeta(syncedKey, strconv.FormatInt(gapStart, 10)); err != nil {
		t.Fatal(err)
	}
	// A post cached before the gap, deleted during it.
	if err := e.store.Upsert(&model.Post{Id: "gone", ChannelId: "c1", UserId: "u2", Message: "secret", CreateAt: gapStart - 5000}); err != nil {
		t.Fatal(err)
	}
	client, requested := backfillServer(t, gapStart, postsFail)
	e.client = client
	return e, gapStart, requested
}

func watermark(t *testing.T, e *Engine) int64 {
	t.Helper()
	v, _, err := e.store.GetMeta(syncedKey)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := strconv.ParseInt(v, 10, 64)
	return ms
}

// TestBackfillCacheFillsGap: posts that arrived while disconnected land in the
// cache, deletions become tombstones, quiet channels aren't fetched, and the
// watermark moves up — with notifications off, which used to skip it entirely.
func TestBackfillCacheFillsGap(t *testing.T) {
	e, gapStart, requested := backfillEngine(t, false)
	e.backfillCache(t.Context())

	if got, _ := e.store.Post("new"); got == nil || got.Message != "while you were away" {
		t.Errorf("post from the gap not cached: %+v", got)
	}
	if got, _ := e.store.Post("gone"); got == nil || got.DeleteAt == 0 || got.Message != "" {
		t.Errorf("post deleted during the gap not tombstoned: %+v", got)
	}
	if r := requested(); len(r) != 1 || r[0] != "c1" {
		t.Errorf("fetched %v, want only c1 (c2 had no post since the watermark)", r)
	}
	if w := watermark(t, e); w <= gapStart {
		t.Errorf("watermark %d didn't advance past %d", w, gapStart)
	}
	if !e.cacheSynced {
		t.Error("cacheSynced not set after a complete backfill")
	}
}

// TestBackfillCacheHoldsWatermarkOnFailure: a channel that couldn't be read
// must not be buried under a newer watermark.
func TestBackfillCacheHoldsWatermarkOnFailure(t *testing.T) {
	e, gapStart, _ := backfillEngine(t, true)
	e.backfillCache(t.Context())
	if w := watermark(t, e); w != gapStart {
		t.Errorf("watermark moved to %d after a failed fetch, want %d", w, gapStart)
	}
	if e.cacheSynced {
		t.Error("cacheSynced set although a channel failed")
	}
	// keepSynced (the probe tick) retries rather than advancing.
	e.keepSynced(t.Context())
	if w := watermark(t, e); w != gapStart {
		t.Errorf("keepSynced advanced the watermark over an unfilled hole: %d", w)
	}
}

// TestBackfillCacheFirstRun: with no watermark there's no known gap; start
// tracking without fetching history.
func TestBackfillCacheFirstRun(t *testing.T) {
	e := newStoreEngine(t)
	e.me = &model.User{Id: "u-me"}
	client, requested := backfillServer(t, 0, false)
	e.client = client
	e.backfillCache(t.Context())
	if r := requested(); len(r) != 0 {
		t.Errorf("first run fetched %v", r)
	}
	if watermark(t, e) == 0 || !e.cacheSynced {
		t.Error("first run didn't start tracking")
	}
}
