package semindex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/embed"
	"matterbox/internal/store"
)

// fakeEmbedServer returns one unit vector per input and records the inputs it
// was sent (so a test can assert the document prefix was applied).
func fakeEmbedServer(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		*seen = append(*seen, req.Input...)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var resp struct {
			Data []item `json:"data"`
		}
		for i := range req.Input {
			v := make([]float32, 8)
			v[i%8] = 1
			resp.Data = append(resp.Data, item{Index: i, Embedding: v})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkPost(s *store.Store, t *testing.T, id, msg string) {
	t.Helper()
	if err := s.Upsert(&model.Post{Id: id, ChannelId: "c1", UserId: "u1", Message: msg, CreateAt: 1, UpdateAt: 1}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

func TestModelTag(t *testing.T) {
	if got := ModelTag("m", 256); got != "m@256" {
		t.Errorf("ModelTag(m,256) = %q", got)
	}
	if got := ModelTag("m", 0); got != "m" {
		t.Errorf("ModelTag(m,0) = %q", got)
	}
}

func TestBackfillEmbedsAllAndIsResumable(t *testing.T) {
	st := tempStore(t)
	var seen []string
	srv := fakeEmbedServer(t, &seen)
	defer srv.Close()

	for _, id := range []string{"p1", "p2", "p3", "p4", "p5"} {
		mkPost(st, t, id, "message "+id)
	}
	mkPost(st, t, "empty", "") // no text → never embedded

	client := embed.New(srv.URL, "", "m", 8)
	ix := New(st, client, "m", 8, 2) // batch of 2 → multiple rounds

	total, err := ix.Backfill(context.Background(), nil)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if total != 5 {
		t.Fatalf("embedded %d, want 5", total)
	}
	if n, _ := st.VectorCount(ix.Tag()); n != 5 {
		t.Errorf("VectorCount = %d, want 5", n)
	}

	// Document prefix must be applied to every input.
	for _, in := range seen {
		if !strings.HasPrefix(in, "title: none | text: ") {
			t.Errorf("input missing document prefix: %q", in)
		}
	}

	// Re-running embeds nothing (idempotent / resumable).
	again, err := ix.Backfill(context.Background(), nil)
	if err != nil {
		t.Fatalf("re-backfill: %v", err)
	}
	if again != 0 {
		t.Errorf("re-run embedded %d, want 0", again)
	}
}

func TestRunOnceMoreFlag(t *testing.T) {
	st := tempStore(t)
	var seen []string
	srv := fakeEmbedServer(t, &seen)
	defer srv.Close()

	mkPost(st, t, "p1", "a")
	mkPost(st, t, "p2", "b")
	mkPost(st, t, "p3", "c")

	ix := New(st, embed.New(srv.URL, "", "m", 8), "m", 8, 2)

	n, more, err := ix.RunOnce(context.Background())
	if err != nil || n != 2 || !more {
		t.Fatalf("round 1: n=%d more=%v err=%v, want 2,true,nil", n, more, err)
	}
	n, more, err = ix.RunOnce(context.Background())
	if err != nil || n != 1 || more {
		t.Fatalf("round 2: n=%d more=%v err=%v, want 1,false,nil", n, more, err)
	}
}

func TestLongMessageIsChunkedIntoOneVector(t *testing.T) {
	st := tempStore(t)
	var seen []string
	srv := fakeEmbedServer(t, &seen)
	defer srv.Close()

	// A message far longer than maxChunkRunes → multiple chunks, one post.
	long := strings.Repeat("word ", maxChunkRunes) // ~5*1500 runes
	mkPost(st, t, "big", long)

	ix := New(st, embed.New(srv.URL, "", "m", 8), "m", 8, 8)
	total, err := ix.Backfill(context.Background(), nil)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if total != 1 {
		t.Fatalf("post count embedded = %d, want 1", total)
	}
	if n, _ := st.VectorCount(ix.Tag()); n != 1 {
		t.Errorf("VectorCount = %d, want 1 (chunks pool to one vector)", n)
	}
	// The post must have produced more than one chunk input...
	if len(seen) < 2 {
		t.Errorf("expected multiple chunk inputs, got %d", len(seen))
	}
	// ...and every chunk must be within the rune budget (plus the prefix).
	for _, in := range seen {
		body := strings.TrimPrefix(in, "title: none | text: ")
		if len([]rune(body)) > maxChunkRunes {
			t.Errorf("chunk exceeds maxChunkRunes: %d runes", len([]rune(body)))
		}
	}
	// It must not be re-queued (it got a vector).
	if again, _ := ix.Backfill(context.Background(), nil); again != 0 {
		t.Errorf("re-run embedded %d, want 0 (no poison-pill retry)", again)
	}
}

func TestChunks(t *testing.T) {
	if got := chunks("  hi  "); len(got) != 1 || got[0] != "hi" {
		t.Errorf("short message: %q", got)
	}
	if got := chunks("   "); got != nil {
		t.Errorf("whitespace should yield no chunks: %q", got)
	}
	long := strings.Repeat("a", maxChunkRunes*2+10)
	got := chunks(long)
	if len(got) != 3 {
		t.Fatalf("want 3 chunks for 2x+10, got %d", len(got))
	}
	for _, c := range got {
		if len([]rune(c)) > maxChunkRunes {
			t.Errorf("chunk too long: %d", len([]rune(c)))
		}
	}
}

func TestRunOnceServerDownErrors(t *testing.T) {
	st := tempStore(t)
	mkPost(st, t, "p1", "a")
	// Point at a closed server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	ix := New(st, embed.New(srv.URL, "", "m", 8), "m", 8, 8)
	if _, _, err := ix.RunOnce(context.Background()); err == nil {
		t.Error("expected error when embeddings server is unreachable")
	}
}

func TestNilIndexerNoop(t *testing.T) {
	var ix *Indexer
	if n, more, err := ix.RunOnce(context.Background()); n != 0 || more || err != nil {
		t.Errorf("nil indexer RunOnce = %d,%v,%v", n, more, err)
	}
	if ix.Tag() != "" {
		t.Errorf("nil indexer Tag = %q", ix.Tag())
	}
}
