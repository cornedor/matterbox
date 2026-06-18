package store

import (
	"context"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestRawQuerySelect(t *testing.T) {
	s := tempStore(t)
	if err := s.UpsertMany([]*model.Post{
		mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "hello world", 100),
		mkPost("p2aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "second message", 200),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	res, err := s.RawQuery(context.Background(),
		"SELECT id, message, create_at FROM posts ORDER BY create_at", 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got := res.Columns; len(got) != 3 || got[0] != "id" || got[1] != "message" || got[2] != "create_at" {
		t.Fatalf("columns = %v", got)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(res.Rows))
	}
	if got := sqlString(res.Rows[0][1]); got != "hello world" {
		t.Errorf("row0 message = %q", got)
	}
	if res.Truncated {
		t.Errorf("unexpected truncation")
	}
}

func TestRawQueryTruncates(t *testing.T) {
	s := tempStore(t)
	var posts []*model.Post
	for i := range 5 {
		posts = append(posts, mkPost(
			string(rune('a'+i))+"1aaaaaaaaaaaaaaaaaaaaaaa", "c1", "m", int64(100+i)))
	}
	if err := s.UpsertMany(posts); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	res, err := s.RawQuery(context.Background(), "SELECT id FROM posts", 3)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Rows) != 3 || !res.Truncated {
		t.Fatalf("want 3 rows + truncated, got %d trunc=%v", len(res.Rows), res.Truncated)
	}
}

func TestRawQueryRejectsWrites(t *testing.T) {
	s := tempStore(t)
	if err := s.UpsertMany([]*model.Post{
		mkPost("p1aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "keep me", 100),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	for _, q := range []string{
		"DELETE FROM posts",
		"delete from posts where 1=1",
		"UPDATE posts SET message = 'x'",
		"DROP TABLE posts",
		"INSERT INTO posts (id) VALUES ('x')",
		// A leading SELECT but a writing CTE — caught by the query_only handle,
		// not the leading-keyword check.
		"WITH t AS (SELECT 1) DELETE FROM posts",
	} {
		if _, err := s.RawQuery(context.Background(), q, 10); err == nil {
			t.Errorf("expected %q to be rejected", q)
		}
	}

	// The post must survive every rejected write.
	res, err := s.RawQuery(context.Background(), "SELECT COUNT(*) AS n FROM posts", 10)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n, _ := sqlInt(res.Rows[0][0]); n != 1 {
		t.Fatalf("post count = %d after rejected writes, want 1", n)
	}
}

// sqlString / sqlInt mirror the UI's cell coercion just enough for assertions.
func sqlString(v any) string {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return ""
	}
}

func sqlInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case float64:
		return int64(t), true
	}
	return 0, false
}

func TestRawQueryReadonlyDoesNotBlockWrites(t *testing.T) {
	// Opening the read-only handle (via a query) must not stop the main handle
	// from continuing to persist posts — they're independent connections.
	s := tempStore(t)
	if _, err := s.RawQuery(context.Background(), "SELECT 1", 1); err != nil {
		t.Fatalf("warm-up query: %v", err)
	}
	if err := s.UpsertMany([]*model.Post{
		mkPost("p9aaaaaaaaaaaaaaaaaaaaaaaa", "c1", "written after RO open", 300),
	}); err != nil {
		t.Fatalf("upsert after RO open: %v", err)
	}
	res, err := s.RawQuery(context.Background(),
		"SELECT message FROM posts WHERE id = 'p9aaaaaaaaaaaaaaaaaaaaaaaa'", 1)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Rows) != 1 || !strings.Contains(sqlString(res.Rows[0][0]), "written after") {
		t.Fatalf("RO handle didn't see the new write: %+v", res.Rows)
	}
}
