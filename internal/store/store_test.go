package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	got, err := s.RecentForChannel("c1", 10, false)
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
	got, _ := s.RecentForChannel("c1", 10, false)
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

// TestDelete is a soft delete: the row survives as a stripped tombstone so the
// transcript can show a persistent "message deleted" marker, but the content is
// gone from the live view, from search, and from the edit-history archive.
func TestDelete(t *testing.T) {
	s := tempStore(t)
	p := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "going away", 100)
	if err := s.Upsert(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// An earlier edit so there's an archived revision to purge on delete.
	edit := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "going away soon", 100)
	edit.EditAt = 150
	if err := s.Upsert(edit); err != nil {
		t.Fatalf("upsert edit: %v", err)
	}

	if err := s.Delete(p); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Live view (includeDeleted=false) hides it.
	if got, _ := s.RecentForChannel("c1", 10, false); len(got) != 0 {
		t.Errorf("tombstone leaked into live view: %d post(s)", len(got))
	}
	// Transcript view (includeDeleted=true) keeps a stripped tombstone.
	got, _ := s.RecentForChannel("c1", 10, true)
	if len(got) != 1 {
		t.Fatalf("tombstone missing from transcript view: got %d", len(got))
	}
	if got[0].DeleteAt == 0 {
		t.Error("tombstone should carry a nonzero DeleteAt")
	}
	if got[0].Message != "" {
		t.Errorf("tombstone still holds content: %q", got[0].Message)
	}
	// Search and the revision archive must both stop surfacing the content.
	if n := ftsCount(t, s, "going"); n != 0 {
		t.Errorf("fts not cleaned: %d", n)
	}
	if revs, _ := s.Revisions(p.Id); len(revs) != 0 {
		t.Errorf("delete left %d revision(s) behind", len(revs))
	}
}

// TestDeleteIsTerminal: once a post is soft-deleted, re-upserting its original
// (pre-delete) content must not bring it back to life — the upsert's delete_at
// guard keeps the tombstone.
func TestDeleteIsTerminal(t *testing.T) {
	s := tempStore(t)
	p := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "secret", 100)
	if err := s.Upsert(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Delete(p); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// A stale refetch tries to write the original content back.
	if err := s.Upsert(p); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got, _ := s.RecentForChannel("c1", 10, false); len(got) != 0 {
		t.Errorf("tombstone resurrected into the live view: %d post(s)", len(got))
	}
	got, _ := s.RecentForChannel("c1", 10, true)
	if len(got) != 1 || got[0].Message != "" {
		t.Errorf("tombstone clobbered by re-upsert: %+v", got)
	}
	if n := ftsCount(t, s, "secret"); n != 0 {
		t.Errorf("deleted content back in fts: %d", n)
	}
}

// TestDeleteUncachedSeedsTombstone covers the race that lost tombstones across
// a restart: a post_deleted that arrives before the post itself was ever
// persisted. The event post seeds the tombstone row, and a late upsert of the
// original (live) content can't resurrect it.
func TestDeleteUncachedSeedsTombstone(t *testing.T) {
	s := tempStore(t)
	p := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "ghost", 100)

	// Delete first — the post is not in the store yet.
	if err := s.Delete(p); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := s.RecentForChannel("c1", 10, true)
	if len(got) != 1 {
		t.Fatalf("uncached delete didn't seed a tombstone: got %d", len(got))
	}
	if got[0].Id != p.Id || got[0].DeleteAt == 0 || got[0].Message != "" {
		t.Errorf("bad seeded tombstone: %+v", got[0])
	}

	// A late persist of the original content must not bring it back.
	if err := s.Upsert(p); err != nil {
		t.Fatalf("late upsert: %v", err)
	}
	if live, _ := s.RecentForChannel("c1", 10, false); len(live) != 0 {
		t.Errorf("late upsert resurrected the post: %d", len(live))
	}
	if n := ftsCount(t, s, "ghost"); n != 0 {
		t.Errorf("ghost content searchable: %d", n)
	}
}

// TestDeleteWatermarkAndIdempotent covers the offline-deletion sync contract:
// a Delete carrying the server's delete time stamps the tombstone with it and
// advances MaxUpdateAt past it (so the next PostsSince sweep won't re-report
// it), and a repeated Delete of an already-tombstoned post is a no-op.
func TestDeleteWatermarkAndIdempotent(t *testing.T) {
	s := tempStore(t)
	p := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "hi", 100) // update_at 100
	if err := s.Upsert(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if mx, _ := s.MaxUpdateAt("c1"); mx != 100 {
		t.Fatalf("MaxUpdateAt = %d, want 100", mx)
	}

	// A PostsSince sweep reports it deleted far later (offline deletion).
	del := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "hi", 100)
	del.DeleteAt, del.UpdateAt = 5000, 5000
	if err := s.Delete(del); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := s.RecentForChannel("c1", 10, true)
	if len(got) != 1 || got[0].DeleteAt != 5000 {
		t.Fatalf("tombstone should carry the server delete time 5000: %+v", got)
	}
	if mx, _ := s.MaxUpdateAt("c1"); mx != 5000 {
		t.Errorf("watermark didn't advance past the delete: MaxUpdateAt = %d, want 5000", mx)
	}

	// Re-deleting the same (already tombstoned) post changes nothing.
	if err := s.Delete(del); err != nil {
		t.Fatalf("re-delete: %v", err)
	}
	if got, _ := s.RecentForChannel("c1", 10, true); len(got) != 1 || got[0].DeleteAt != 5000 {
		t.Errorf("re-delete disturbed the tombstone: %+v", got)
	}
}

// TestMaxUpdateAt: empty channel is 0; otherwise the largest update_at across
// all rows (deleted included, via the tombstone's stamped delete time).
func TestMaxUpdateAt(t *testing.T) {
	s := tempStore(t)
	if mx, _ := s.MaxUpdateAt("c1"); mx != 0 {
		t.Errorf("empty channel MaxUpdateAt = %d, want 0", mx)
	}
	a := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "a", 100)
	b := mkPost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "b", 300)
	b.UpdateAt = 350
	if err := s.UpsertMany([]*model.Post{a, b}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if mx, _ := s.MaxUpdateAt("c1"); mx != 350 {
		t.Errorf("MaxUpdateAt = %d, want 350", mx)
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
	got, _ := s.RecentForChannel("c1", 3, false)
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

// TestUpsertMetadataOnlyChangePersists guards the upsert's content-change
// WHERE clause (posts.raw_json IS NOT excluded.raw_json): a refetch whose
// message is unchanged but whose serialized post differs — e.g. a reaction or
// file info arrived — must still overwrite the stored row. The cheap-looking
// alternative of comparing only update_at/edit_at would silently drop these.
func TestUpsertMetadataOnlyChangePersists(t *testing.T) {
	s := tempStore(t)
	p := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "stable message", 100)
	if err := s.Upsert(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Same id, same message + timestamps, but new metadata (raw_json differs).
	enriched := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "stable message", 100)
	enriched.AddProp("pinned", true)
	if err := s.Upsert(enriched); err != nil {
		t.Fatalf("re-upsert with metadata: %v", err)
	}
	got, _ := s.RecentForChannel("c1", 10, false)
	if len(got) != 1 {
		t.Fatalf("want 1 post, got %d", len(got))
	}
	if got[0].GetProp("pinned") != true {
		t.Errorf("metadata-only update dropped: prop = %v", got[0].GetProp("pinned"))
	}
	// FTS row for the unchanged message must remain searchable.
	if n := ftsCount(t, s, "stable"); n != 1 {
		t.Errorf("want 1 fts row after metadata update, got %d", n)
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

func TestSearchPorterStemming(t *testing.T) {
	s := tempStore(t)
	now := time.Now().UnixMilli()
	if err := s.Upsert(mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "we connected to the new server", now)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// The Porter stemmer collapses "connection" and "connected" to a shared root
	// ("connect"), so searching one inflection finds the other — which the
	// forward-only prefix matching in ftsQuery ("connection"* never matches
	// "connected") could not do.
	hits, err := s.Search("connection", nil, 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Match.Id != "p1aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf(`want "connection" to match "connected" via porter; got %v`, hitIDs(hits))
	}
}

func TestEnsureFTSTokenizerRebuild(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	// Build a database carrying the pre-porter tokenizer, as an older matterbox
	// would have left it, with one post already stored.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	const oldSchema = `
CREATE TABLE posts (
    id TEXT PRIMARY KEY, channel_id TEXT NOT NULL, user_id TEXT NOT NULL DEFAULT '',
    root_id TEXT NOT NULL DEFAULT '', create_at INTEGER NOT NULL, update_at INTEGER NOT NULL DEFAULT 0,
    edit_at INTEGER NOT NULL DEFAULT 0, delete_at INTEGER NOT NULL DEFAULT 0,
    message TEXT NOT NULL DEFAULT '', raw_json BLOB NOT NULL
);
CREATE VIRTUAL TABLE posts_fts USING fts5(
    message, content='posts', content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 2'
);`
	if _, err := raw.Exec(oldSchema); err != nil {
		t.Fatalf("old schema: %v", err)
	}
	p := mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "we connected to the new server", time.Now().UnixMilli())
	rawJSON, _ := json.Marshal(p)
	if _, err := raw.Exec(
		`INSERT INTO posts (id, channel_id, create_at, update_at, message, raw_json) VALUES (?, ?, ?, ?, ?, ?)`,
		p.Id, p.ChannelId, p.CreateAt, p.UpdateAt, p.Message, rawJSON,
	); err != nil {
		t.Fatalf("seed old post: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// Reopening through the store must migrate the tokenizer and rebuild the
	// index from the content table.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var ddl string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='posts_fts'`).Scan(&ddl); err != nil {
		t.Fatalf("read ddl: %v", err)
	}
	if !strings.Contains(ddl, ftsTokenizer) {
		t.Fatalf("posts_fts not migrated to %q; ddl=%q", ftsTokenizer, ddl)
	}
	// A stemmed query that only works post-rebuild proves the existing row was
	// re-indexed with the porter tokenizer, not just the table redefined.
	hits, err := s.Search("connection", nil, 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Match.Id != p.Id {
		t.Fatalf("want rebuilt index to stem-match the old row; got %v", hitIDs(hits))
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

func TestFtsSpec(t *testing.T) {
	cases := []struct {
		name string
		spec SearchSpec
		want string
	}{
		{"single any_of", SearchSpec{AnyOf: []string{"a"}}, `"a"*`},
		{"any_of group", SearchSpec{AnyOf: []string{"a", "b"}}, `("a"* OR "b"*)`},
		{"all_of and any_of", SearchSpec{AllOf: []string{"cms"}, AnyOf: []string{"x", "y"}}, `"cms"* AND ("x"* OR "y"*)`},
		{"multiword item is a phrase", SearchSpec{AnyOf: []string{"headless cms"}}, `"headless cms"`},
		{"explicit phrase", SearchSpec{Phrases: []string{"content management"}}, `"content management"`},
		{"none_of excludes", SearchSpec{AnyOf: []string{"a"}, NoneOf: []string{"b"}}, `("a"*) NOT "b"*`},
		{"none_of alone is dropped", SearchSpec{NoneOf: []string{"b"}}, ``},
		{"empty", SearchSpec{}, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ftsSpec(tc.spec); got != tc.want {
				t.Errorf("ftsSpec(%+v) = %q; want %q", tc.spec, got, tc.want)
			}
		})
	}
}

func TestSearchSpec(t *testing.T) {
	s := tempStore(t)
	// None of these mention "Acme" — that name lives in the channel
	// title, not the content. Authors and times vary so the metadata filters
	// can be exercised too.
	mk := func(id, ch, msg, author string, at int64) *model.Post {
		return &model.Post{Id: id, ChannelId: ch, UserId: author, Message: msg, CreateAt: at, UpdateAt: at}
	}
	const (
		p1 = "p1aaaaaaaaaaaaaaaaaaaaaaaa"
		p2 = "p2aaaaaaaaaaaaaaaaaaaaaaaa"
		p3 = "p3aaaaaaaaaaaaaaaaaaaaaaaa"
		p4 = "p4aaaaaaaaaaaaaaaaaaaaaaaa"
		p5 = "p5aaaaaaaaaaaaaaaaaaaaaaaa"
	)
	posts := []*model.Post{
		mk(p1, "c1", "migrating to Storyblok as the new headless CMS", "u1", 100),
		mk(p2, "c1", "Storyblok onboarding call next week", "u2", 200),
		mk(p3, "c1", "we picked a new headless CMS", "u1", 300),
		mk(p4, "c1", "quarterly revenue is up", "u2", 400),
		mk(p5, "c2", "Storyblok works great with Next.js", "u1", 500),
	}
	if err := s.UpsertMany(posts); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	set := func(hits []SearchHit) map[string]bool {
		m := map[string]bool{}
		for _, h := range hits {
			m[h.Match.Id] = true
		}
		return m
	}

	t.Run("any_of ORs and ranks by bm25", func(t *testing.T) {
		hits, total, err := s.SearchSpec(SearchSpec{AnyOf: []string{"storyblok", "cms", "new"}}, 10, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := set(hits)
		for _, id := range []string{p1, p2, p3, p5} {
			if !got[id] {
				t.Errorf("want %s in results; got %v", id, hitIDs(hits))
			}
		}
		if got[p4] {
			t.Error("p4 shares no term and should be excluded")
		}
		if total != 4 {
			t.Errorf("total = %d; want 4", total)
		}
		// Ranked by relevance, not recency: a multi-term match (p1/p3) leads,
		// rather than the newest single-term match (p5) that recency would.
		if len(hits) == 0 || (hits[0].Match.Id != p1 && hits[0].Match.Id != p3) {
			t.Errorf("want a multi-term match (p1/p3) first by bm25; got %v", hitIDs(hits))
		}
	})

	t.Run("all_of narrows a broad any_of", func(t *testing.T) {
		hits, total, _ := s.SearchSpec(SearchSpec{AllOf: []string{"cms"}, AnyOf: []string{"storyblok", "new"}}, 10, 0, 0)
		got := set(hits)
		if !got[p1] || !got[p3] || got[p2] || got[p5] || total != 2 {
			t.Errorf("all_of=cms: got %v (total %d); want {p1,p3}", hitIDs(hits), total)
		}
	})

	t.Run("phrase matches exact wording only", func(t *testing.T) {
		hits, total, _ := s.SearchSpec(SearchSpec{Phrases: []string{"headless cms"}}, 10, 0, 0)
		got := set(hits)
		if !got[p1] || !got[p3] || total != 2 {
			t.Errorf(`phrase "headless cms": got %v (total %d); want {p1,p3}`, hitIDs(hits), total)
		}
		if h2, t2, _ := s.SearchSpec(SearchSpec{Phrases: []string{"cms headless"}}, 10, 0, 0); t2 != 0 || len(h2) != 0 {
			t.Errorf("reversed phrase should not match; got %v", hitIDs(h2))
		}
	})

	t.Run("none_of excludes", func(t *testing.T) {
		hits, total, _ := s.SearchSpec(SearchSpec{AnyOf: []string{"storyblok"}, NoneOf: []string{"onboarding"}}, 10, 0, 0)
		got := set(hits)
		if got[p2] || !got[p1] || !got[p5] || total != 2 {
			t.Errorf("none_of=onboarding: got %v (total %d); want {p1,p5}", hitIDs(hits), total)
		}
	})

	t.Run("channel scope", func(t *testing.T) {
		hits, total, _ := s.SearchSpec(SearchSpec{AnyOf: []string{"storyblok"}, ChannelIDs: []string{"c1"}}, 10, 0, 0)
		got := set(hits)
		if !got[p1] || !got[p2] || got[p5] || total != 2 {
			t.Errorf("channel c1: got %v (total %d); want {p1,p2}", hitIDs(hits), total)
		}
	})

	t.Run("author filter", func(t *testing.T) {
		hits, total, _ := s.SearchSpec(SearchSpec{AnyOf: []string{"storyblok"}, AuthorIDs: []string{"u2"}}, 10, 0, 0)
		got := set(hits)
		if !got[p2] || got[p1] || got[p5] || total != 1 {
			t.Errorf("author u2: got %v (total %d); want {p2}", hitIDs(hits), total)
		}
	})

	t.Run("date bounds [after, before)", func(t *testing.T) {
		hits, total, _ := s.SearchSpec(SearchSpec{AnyOf: []string{"storyblok"}, After: 150, Before: 450}, 10, 0, 0)
		got := set(hits)
		if !got[p2] || got[p1] || got[p5] || total != 1 {
			t.Errorf("date [150,450): got %v (total %d); want {p2}", hitIDs(hits), total)
		}
	})

	t.Run("total counts beyond the page limit", func(t *testing.T) {
		hits, total, _ := s.SearchSpec(SearchSpec{AnyOf: []string{"storyblok", "cms", "new"}}, 2, 0, 0)
		if len(hits) != 2 || total != 4 {
			t.Errorf("limit 2: len(hits)=%d total=%d; want 2 and 4", len(hits), total)
		}
	})

	t.Run("empty spec yields nothing", func(t *testing.T) {
		hits, total, err := s.SearchSpec(SearchSpec{}, 10, 0, 0)
		if err != nil || len(hits) != 0 || total != 0 {
			t.Errorf("empty spec: hits=%d total=%d err=%v; want 0,0,nil", len(hits), total, err)
		}
	})

	t.Run("scope resolved to no channels yields nothing", func(t *testing.T) {
		hits, total, err := s.SearchSpec(SearchSpec{AnyOf: []string{"storyblok"}, ChannelIDs: []string{}}, 10, 0, 0)
		if err != nil || len(hits) != 0 || total != 0 {
			t.Errorf("empty channel scope: hits=%d total=%d err=%v; want 0,0,nil", len(hits), total, err)
		}
	})

	t.Run("offset pages through results without overlap", func(t *testing.T) {
		spec := SearchSpec{AnyOf: []string{"storyblok", "cms", "new"}} // 4 matches
		page1, total, _ := s.SearchSpec(spec, 2, 0, 0)
		page2, _, _ := s.SearchSpec(spec, 2, 2, 0)
		if len(page1) != 2 || len(page2) != 2 {
			t.Fatalf("want 2+2 across pages; got %d and %d", len(page1), len(page2))
		}
		seen := map[string]bool{}
		for _, h := range append(page1, page2...) {
			if seen[h.Match.Id] {
				t.Errorf("id %s appeared on both pages", h.Match.Id)
			}
			seen[h.Match.Id] = true
		}
		for _, id := range []string{p1, p2, p3, p5} {
			if !seen[id] {
				t.Errorf("paging missed %s; saw %v", id, seen)
			}
		}
		if empty, total2, _ := s.SearchSpec(spec, 2, 4, 0); len(empty) != 0 || total2 != total {
			t.Errorf("offset past end: hits=%d total=%d; want 0 and %d", len(empty), total2, total)
		}
	})
}

func TestRankByRelevanceAndAge(t *testing.T) {
	now := time.Now().UnixMilli()
	const day = int64(86_400_000)
	halfLife := 90 * 24 * time.Hour
	// Pools are passed in bm25 order (best first), as searchFTS produces them.
	mk := func(id string, ageDays int64) scoredPost {
		return scoredPost{post: &model.Post{Id: id, CreateAt: now - ageDays*day}}
	}

	t.Run("close relevance favours recency", func(t *testing.T) {
		// Adjacent relevance ranks, large age gap: the fresh one wins.
		got := rankByRelevanceAndAge([]scoredPost{mk("old", 300), mk("new", 1)}, now, halfLife)
		if len(got) != 2 || got[0].Id != "new" {
			t.Errorf("want newer first; got %v", postIDs(got))
		}
	})

	t.Run("strong relevance survives a small age gap", func(t *testing.T) {
		// Top bm25 hit only slightly older than a fresh 2nd-ranked one: within a
		// half-life the decay is mild, so the markedly stronger match stays on top.
		got := rankByRelevanceAndAge([]scoredPost{mk("relevant", 20), mk("fresh", 1)}, now, halfLife)
		if got[0].Id != "relevant" {
			t.Errorf("want the stronger match first; got %v", postIDs(got))
		}
	})

	t.Run("stale top match sinks below recent ones", func(t *testing.T) {
		// Most relevant but many half-lives old: it should fall behind newer,
		// only-slightly-weaker matches.
		got := rankByRelevanceAndAge([]scoredPost{
			mk("ancient", 365), // most relevant, oldest
			mk("recentA", 3),
			mk("recentB", 5),
		}, now, halfLife)
		if got[len(got)-1].Id != "ancient" {
			t.Errorf("the stale top-bm25 hit should sink; got %v", postIDs(got))
		}
	})

	t.Run("non-positive half-life disables decay (pure relevance)", func(t *testing.T) {
		// Input is relevance-sorted, so a disabled decay preserves that order.
		got := rankByRelevanceAndAge([]scoredPost{mk("a", 300), mk("b", 1), mk("c", 50)}, now, 0)
		if got[0].Id != "a" || got[1].Id != "b" || got[2].Id != "c" {
			t.Errorf("want input order preserved; got %v", postIDs(got))
		}
	})

	t.Run("single match returned as-is", func(t *testing.T) {
		got := rankByRelevanceAndAge([]scoredPost{mk("only", 50)}, now, halfLife)
		if len(got) != 1 || got[0].Id != "only" {
			t.Errorf("want [only]; got %v", postIDs(got))
		}
	})
}

func TestAuthoredBetween(t *testing.T) {
	s := tempStore(t)
	mk := func(id, ch, author, msg string, at int64) *model.Post {
		return &model.Post{Id: id, ChannelId: ch, UserId: author, Message: msg, CreateAt: at, UpdateAt: at}
	}
	const (
		a1 = "a1aaaaaaaaaaaaaaaaaaaaaaaa"
		a2 = "a2aaaaaaaaaaaaaaaaaaaaaaaa"
		a3 = "a3aaaaaaaaaaaaaaaaaaaaaaaa"
		a4 = "a4aaaaaaaaaaaaaaaaaaaaaaaa"
		b1 = "b1aaaaaaaaaaaaaaaaaaaaaaaa"
		sy = "syaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	sysPost := mk(sy, "c1", "me", "me joined", 250)
	sysPost.Type = model.PostTypeJoinChannel
	posts := []*model.Post{
		mk(a1, "c1", "me", "first", 100),
		mk(a2, "c2", "me", "second", 200),
		sysPost, // system post by me — must be skipped
		mk(a3, "c1", "me", "third", 300),
		mk(a4, "c2", "me", "fourth", 400),
		mk(b1, "c1", "other", "not mine", 250),
	}
	if err := s.UpsertMany(posts); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	ids := func(ps []*model.Post) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = p.Id
		}
		return out
	}
	eq := func(t *testing.T, got []*model.Post, want ...string) {
		t.Helper()
		g := ids(got)
		if len(g) != len(want) {
			t.Fatalf("got %v, want %v", g, want)
		}
		for i := range want {
			if g[i] != want[i] {
				t.Fatalf("got %v, want %v", g, want)
			}
		}
	}

	t.Run("author scope, no bounds, oldest first, system skipped", func(t *testing.T) {
		got, err := s.AuthoredBetween("me", 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, got, a1, a2, a3, a4)
	})

	t.Run("since is inclusive, until exclusive", func(t *testing.T) {
		got, err := s.AuthoredBetween("me", 200, 400, 0)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, got, a2, a3) // 200 included, 400 excluded
	})

	t.Run("limit keeps the most recent, still oldest-first", func(t *testing.T) {
		got, err := s.AuthoredBetween("me", 0, 0, 2)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, got, a3, a4) // newest two (300,400) re-sorted ascending
	})

	t.Run("unknown author yields nothing", func(t *testing.T) {
		got, err := s.AuthoredBetween("nobody", 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("want none, got %v", ids(got))
		}
	})

	t.Run("empty author yields nothing", func(t *testing.T) {
		got, err := s.AuthoredBetween("", 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("want nil, got %v", ids(got))
		}
	})
}

func postIDs(posts []*model.Post) []string {
	ids := make([]string, len(posts))
	for i, p := range posts {
		ids[i] = p.Id
	}
	return ids
}

func hitIDs(hits []SearchHit) []string {
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.Match.Id)
	}
	return ids
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
