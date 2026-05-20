package store

import (
	"path/filepath"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkPost(id, channelID, msg string, createAt int64) *model.Post {
	return &model.Post{
		Id:        id,
		ChannelId: channelID,
		UserId:    "u1",
		Message:   msg,
		CreateAt:  createAt,
		UpdateAt:  createAt,
	}
}

func TestUpsertAndRecent(t *testing.T) {
	s := tempStore(t)
	p1 := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "hello world", 100)
	p2 := mkPost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "second message", 200)
	if err := s.UpsertMany([]*model.Post{p2, p1}); err != nil { // out-of-order on purpose
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.RecentForChannel("c1", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 posts, got %d", len(got))
	}
	// Oldest → newest.
	if got[0].Id != p1.Id || got[1].Id != p2.Id {
		t.Errorf("wrong order: %s, %s", got[0].Id, got[1].Id)
	}
	if got[0].Message != "hello world" {
		t.Errorf("message lost: %q", got[0].Message)
	}
}

func TestUpsertEdit(t *testing.T) {
	s := tempStore(t)
	p := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "original text", 100)
	if err := s.Upsert(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Same Id, new message, EditAt set.
	edited := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "edited text", 100)
	edited.EditAt = 150
	if err := s.Upsert(edited); err != nil {
		t.Fatalf("upsert edit: %v", err)
	}
	got, _ := s.RecentForChannel("c1", 10)
	if len(got) != 1 {
		t.Fatalf("want 1 post after edit, got %d", len(got))
	}
	if got[0].Message != "edited text" {
		t.Errorf("edit lost: %q", got[0].Message)
	}

	// FTS: old text must no longer match, new text must.
	if n := ftsCount(t, s, "original"); n != 0 {
		t.Errorf("old text still in fts: %d", n)
	}
	if n := ftsCount(t, s, "edited"); n != 1 {
		t.Errorf("new text not in fts: %d", n)
	}
}

func TestDelete(t *testing.T) {
	s := tempStore(t)
	p := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "going away", 100)
	if err := s.Upsert(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Delete(p.Id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := s.RecentForChannel("c1", 10)
	if len(got) != 0 {
		t.Errorf("post not deleted: %d remain", len(got))
	}
	if n := ftsCount(t, s, "going"); n != 0 {
		t.Errorf("fts not cleaned: %d", n)
	}
}

func TestLatestPostID(t *testing.T) {
	s := tempStore(t)
	id, err := s.LatestPostID("c1")
	if err != nil {
		t.Fatalf("latest empty: %v", err)
	}
	if id != "" {
		t.Errorf("want empty for unknown channel, got %q", id)
	}
	p1 := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "first", 100)
	p2 := mkPost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "second", 200)
	if err := s.UpsertMany([]*model.Post{p1, p2}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	id, err = s.LatestPostID("c1")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if id != p2.Id {
		t.Errorf("want %s, got %s", p2.Id, id)
	}
}

func TestRecentLimit(t *testing.T) {
	s := tempStore(t)
	for i := 0; i < 5; i++ {
		p := mkPost(
			"id"+string(rune('a'+i))+"aaaaaaaaaaaaaaaaaaaaaaa",
			"c1",
			"msg",
			int64(100+i),
		)
		if err := s.Upsert(p); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	got, _ := s.RecentForChannel("c1", 3)
	if len(got) != 3 {
		t.Fatalf("limit not respected: got %d", len(got))
	}
	// Should be the three newest (CreateAt 102, 103, 104), oldest-first.
	if got[0].CreateAt != 102 || got[2].CreateAt != 104 {
		t.Errorf("wrong window: %d..%d", got[0].CreateAt, got[2].CreateAt)
	}
}

func TestRevisionsCapture(t *testing.T) {
	s := tempStore(t)
	p := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "v1", 100)
	if err := s.Upsert(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// First edit.
	e1 := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "v2", 100)
	e1.EditAt = 200
	if err := s.Upsert(e1); err != nil {
		t.Fatalf("upsert e1: %v", err)
	}
	// Second edit.
	e2 := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "v3", 100)
	e2.EditAt = 300
	if err := s.Upsert(e2); err != nil {
		t.Fatalf("upsert e2: %v", err)
	}
	revs, err := s.Revisions(p.Id)
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("want 2 revisions, got %d", len(revs))
	}
	// Archived versions are the prior content; "v3" is the current
	// (still in posts), "v2" and "v1" should be in revisions
	// oldest-first.
	if revs[0].Message != "v1" || revs[1].Message != "v2" {
		t.Errorf("wrong revisions: %q, %q", revs[0].Message, revs[1].Message)
	}
}

func TestRevisionsSkipUnchangedUpsert(t *testing.T) {
	s := tempStore(t)
	p := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "stable", 100)
	if err := s.Upsert(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Re-upsert with identical message + edit_at (simulates a refetch
	// where nothing actually changed, or a metadata-only update from
	// fileInfosLoadedMsg).
	if err := s.Upsert(p); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	revs, err := s.Revisions(p.Id)
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	if len(revs) != 0 {
		t.Errorf("unchanged upsert created %d phantom revision(s)", len(revs))
	}
}

func TestRevisionsEmptyForUnknownPost(t *testing.T) {
	s := tempStore(t)
	revs, err := s.Revisions("doesnotexist")
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	if revs != nil {
		t.Errorf("want nil revisions for unknown post, got %v", revs)
	}
}

func TestSearchPrefixAndContext(t *testing.T) {
	s := tempStore(t)
	posts := []*model.Post{
		mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "good morning", 100),
		mkPost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "how was your weekend", 200),
		mkPost("p3aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "the alpaca farm was lovely", 300),
		mkPost("p4aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "nice", 400),
		mkPost("p5aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "and the food", 500),
		mkPost("p6aaaaaaaaaaaaaaaaaaaaaaaa", "c2", "alpaca on another channel", 350),
	}
	if err := s.UpsertMany(posts); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	hits, err := s.Search("alpa", nil, 10, 2)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("want 2 hits, got %d", len(hits))
	}
	// Most-recent first: c2 hit at create_at 350 is older than c1 hit at 300?
	// Actually c1 hit is create_at=300, c2 hit is create_at=350. So c2 should
	// come first.
	if hits[0].Match.ChannelId != "c2" {
		t.Errorf("want c2 hit first, got channel %q", hits[0].Match.ChannelId)
	}
	c1Hit := hits[1]
	if c1Hit.Match.Id != "p3aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("c1 match id = %s", c1Hit.Match.Id)
	}
	if len(c1Hit.Before) != 2 {
		t.Errorf("want 2 before, got %d", len(c1Hit.Before))
	} else if c1Hit.Before[0].Id != "p1aaaaaaaaaaaaaaaaaaaaaaaa" || c1Hit.Before[1].Id != "p2aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("before order wrong: %s, %s", c1Hit.Before[0].Id, c1Hit.Before[1].Id)
	}
	if len(c1Hit.After) != 2 {
		t.Errorf("want 2 after, got %d", len(c1Hit.After))
	} else if c1Hit.After[0].Id != "p4aaaaaaaaaaaaaaaaaaaaaaaa" || c1Hit.After[1].Id != "p5aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("after order wrong: %s, %s", c1Hit.After[0].Id, c1Hit.After[1].Id)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	s := tempStore(t)
	if err := s.Upsert(mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "anything", 100)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	hits, err := s.Search("   ", nil, 10, 2)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if hits != nil {
		t.Errorf("want nil for empty query, got %d hits", len(hits))
	}
}

func TestSearchChannelFilter(t *testing.T) {
	s := tempStore(t)
	posts := []*model.Post{
		mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "alpaca farm", 100),
		mkPost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c2", "alpaca trek", 200),
		mkPost("p3aaaaaaaaaaaaaaaaaaaaaaaa", "c3", "alpaca wool", 300),
	}
	if err := s.UpsertMany(posts); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Restrict to c1 + c3.
	hits, err := s.Search("alpaca", []string{"c1", "c3"}, 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("want 2 hits, got %d", len(hits))
	}
	for _, h := range hits {
		if h.Match.ChannelId == "c2" {
			t.Errorf("c2 leaked through scope filter: %s", h.Match.Message)
		}
	}
	// Empty (non-nil) scope -> short-circuit, no hits.
	hits, err = s.Search("alpaca", []string{}, 10, 0)
	if err != nil {
		t.Fatalf("search empty scope: %v", err)
	}
	if hits != nil {
		t.Errorf("empty scope should return nil, got %d hits", len(hits))
	}
}

func TestPostsAround(t *testing.T) {
	s := tempStore(t)
	posts := []*model.Post{
		mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "one", 100),
		mkPost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "two", 200),
		mkPost("p3aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "pivot", 300),
		mkPost("p4aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "four", 400),
		mkPost("p5aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "five", 500),
		mkPost("p6aaaaaaaaaaaaaaaaaaaaaaaa", "c2", "other channel", 350),
	}
	if err := s.UpsertMany(posts); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.PostsAround("c1", "p3aaaaaaaaaaaaaaaaaaaaaaaa", 5, 5)
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 posts (2 before + pivot + 2 after), got %d", len(got))
	}
	ids := []string{got[0].Id, got[1].Id, got[2].Id, got[3].Id, got[4].Id}
	want := []string{
		"p1aaaaaaaaaaaaaaaaaaaaaaaa",
		"p2aaaaaaaaaaaaaaaaaaaaaaaa",
		"p3aaaaaaaaaaaaaaaaaaaaaaaa",
		"p4aaaaaaaaaaaaaaaaaaaaaaaa",
		"p5aaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("pos %d: want %s, got %s", i, want[i], ids[i])
		}
	}
}

func TestPostsAroundLimits(t *testing.T) {
	s := tempStore(t)
	for i := 0; i < 10; i++ {
		p := mkPost("id"+string(rune('a'+i))+"aaaaaaaaaaaaaaaaaaaaaaa", "c1", "msg", int64(100+i))
		if err := s.Upsert(p); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	pivot := "id" + string(rune('a'+5)) + "aaaaaaaaaaaaaaaaaaaaaaa"
	got, err := s.PostsAround("c1", pivot, 2, 2)
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 (2+1+2), got %d", len(got))
	}
}

func TestPostsAroundUnknownPivot(t *testing.T) {
	s := tempStore(t)
	if err := s.Upsert(mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "msg", 100)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.PostsAround("c1", "doesnotexist", 5, 5)
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	if got != nil {
		t.Errorf("want nil for unknown pivot, got %d posts", len(got))
	}
}

func TestSearchEscapesQuotes(t *testing.T) {
	s := tempStore(t)
	if err := s.Upsert(mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", `she said "hi" to me`, 100)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Must not crash the FTS5 parser. Result count is not the point.
	if _, err := s.Search(`"hi"`, nil, 10, 2); err != nil {
		t.Fatalf("search: %v", err)
	}
	if _, err := s.Search(`foo*bar`, nil, 10, 2); err != nil {
		t.Fatalf("search wildcard: %v", err)
	}
}

// ftsCount returns the number of FTS rows matching the given term.
func ftsCount(t *testing.T, s *Store, term string) int {
	t.Helper()
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM posts_fts WHERE posts_fts MATCH ?`, term).Scan(&n)
	if err != nil {
		t.Fatalf("fts count: %v", err)
	}
	return n
}
