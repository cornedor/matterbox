package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// ms returns the unix-ms timestamp for an HH:MM local time today, so the
// formatted "[15:04]" column is deterministic regardless of the test's
// wall clock.
func ms(t *testing.T, hhmm string) int64 {
	t.Helper()
	parsed, err := time.Parse("15:04", hhmm)
	if err != nil {
		t.Fatalf("parse %q: %v", hhmm, err)
	}
	now := time.Now()
	at := time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, time.Local)
	return at.UnixMilli()
}

func TestOrderedPosts(t *testing.T) {
	pl := &model.PostList{
		// Order is newest-first, as the server returns it.
		Order: []string{"p3", "p2", "sys", "p1", "missing"},
		Posts: map[string]*model.Post{
			"p1":  {Id: "p1", Message: "first", CreateAt: 1},
			"p2":  {Id: "p2", Message: "second", CreateAt: 2},
			"p3":  {Id: "p3", Message: "third", CreateAt: 3},
			"sys": {Id: "sys", Message: "joined", Type: model.PostTypeJoinChannel},
			// "missing" is in Order but absent from Posts.
		},
	}
	got := orderedPosts(pl)
	want := []string{"first", "second", "third"} // chronological, no system/missing
	if len(got) != len(want) {
		t.Fatalf("got %d posts, want %d: %+v", len(got), len(want), got)
	}
	for i, p := range got {
		if p.Message != want[i] {
			t.Errorf("post[%d] = %q, want %q", i, p.Message, want[i])
		}
	}
}

func TestOrderedPostsNil(t *testing.T) {
	if got := orderedPosts(nil); got != nil {
		t.Errorf("orderedPosts(nil) = %v, want nil", got)
	}
}

func TestUniqueUserIDs(t *testing.T) {
	posts := []*model.Post{
		{UserId: "a"}, {UserId: "b"}, {UserId: "a"}, {UserId: ""}, {UserId: "c"},
	}
	got := uniqueUserIDs(posts)
	want := []string{"a", "b", "c"} // de-duped, first-seen order, empties dropped
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("uniqueUserIDs = %v, want %v", got, want)
	}
}

func TestFilterByCreateRange(t *testing.T) {
	posts := []*model.Post{
		{Id: "p1", CreateAt: 100},
		{Id: "p2", CreateAt: 200},
		{Id: "p3", CreateAt: 300},
		{Id: "p4", CreateAt: 400},
	}
	ids := func(ps []*model.Post) string {
		s := ""
		for _, p := range ps {
			s += p.Id
		}
		return s
	}
	cases := []struct {
		name         string
		since, until int64
		want         string
	}{
		{"no bounds", 0, 0, "p1p2p3p4"},
		{"since inclusive", 200, 0, "p2p3p4"},
		{"until exclusive", 0, 300, "p1p2"},
		{"both", 200, 400, "p2p3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ids(filterByCreateRange(posts, c.since, c.until)); got != c.want {
				t.Errorf("filterByCreateRange(%d,%d) = %q, want %q", c.since, c.until, got, c.want)
			}
		})
	}
}

func TestTailN(t *testing.T) {
	posts := []*model.Post{{Id: "a"}, {Id: "b"}, {Id: "c"}}
	if got := tailN(posts, 0); len(got) != 3 {
		t.Errorf("n=0 should not cap: got %d", len(got))
	}
	if got := tailN(posts, 5); len(got) != 3 {
		t.Errorf("n>len should not cap: got %d", len(got))
	}
	got := tailN(posts, 2)
	if len(got) != 2 || got[0].Id != "b" || got[1].Id != "c" {
		t.Errorf("tailN(2) = %v, want [b c]", got)
	}
}

func TestFormatPosts(t *testing.T) {
	posts := []*model.Post{
		{UserId: "u1", Message: "hello", CreateAt: ms(t, "09:05")},
		{UserId: "u2", Message: "line one\nline two", CreateAt: ms(t, "09:06")},
		{UserId: "u3", Message: "who am i", CreateAt: ms(t, "09:07")}, // unknown author
	}
	names := map[string]string{"u1": "alice", "u2": "bob"}

	got := formatPosts(posts, names)
	// Continuation lines indent to the width of "[09:06] @bob  " (14).
	indent := strings.Repeat(" ", len("[09:06] @bob  "))
	want := "" +
		"[09:05] @alice  hello\n" +
		"[09:06] @bob  line one\n" +
		indent + "line two\n" +
		"[09:07] @unknown  who am i\n"
	if got != want {
		t.Errorf("formatPosts mismatch:\n got:\n%q\nwant:\n%q", got, want)
	}
}
