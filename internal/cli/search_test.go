package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"matterbox/internal/store"
)

func TestSearchUserIDs(t *testing.T) {
	chByID := map[string]*model.Channel{
		"c1": {Id: "c1", Type: model.ChannelTypeOpen},
		"dm": {Id: "dm", Type: model.ChannelTypeDirect, Name: "me__alice"},
	}
	hits := []store.SearchHit{
		{
			Match:  &model.Post{ChannelId: "c1", UserId: "bob"},
			Before: []*model.Post{{UserId: "carol"}},
			After:  []*model.Post{{UserId: "bob"}}, // dup author
		},
		{Match: &model.Post{ChannelId: "dm", UserId: "alice"}},
	}

	// Without context: match authors + the DM partner, no context authors.
	got := searchUserIDs(hits, chByID, "me", false)
	if strings.Join(got, ",") != "bob,alice" {
		t.Errorf("no-context = %v, want [bob alice]", got)
	}

	// With context: carol (a before-author) joins, still de-duped, first-seen.
	got = searchUserIDs(hits, chByID, "me", true)
	if strings.Join(got, ",") != "bob,carol,alice" {
		t.Errorf("with-context = %v, want [bob carol alice]", got)
	}
}

func TestPrintSearchHits(t *testing.T) {
	lbl := labeler{
		meID:     "me",
		teamSlug: map[string]string{"t1": "eng"},
		channels: map[string]*model.Channel{"c1": {Id: "c1", Type: model.ChannelTypeOpen, TeamId: "t1", Name: "general"}},
		names:    map[string]string{"u1": "alice", "u2": "bob"},
	}
	names := lbl.names
	hits := []store.SearchHit{{
		Match:  &model.Post{ChannelId: "c1", UserId: "u1", Message: "the match", CreateAt: ms(t, "09:05")},
		Before: []*model.Post{{ChannelId: "c1", UserId: "u2", Message: "before", CreateAt: ms(t, "09:04")}},
		After:  []*model.Post{{ChannelId: "c1", UserId: "u2", Message: "after", CreateAt: ms(t, "09:06")}},
	}}

	// Default (no context): just the breadcrumb and the match line.
	var buf bytes.Buffer
	printSearchHits(&buf, lbl, names, hits, false)
	out := buf.String()
	if !strings.Contains(out, "eng/general\n") {
		t.Errorf("missing channel breadcrumb:\n%s", out)
	}
	if !strings.Contains(out, "@alice  the match") {
		t.Errorf("missing match line:\n%s", out)
	}
	if strings.Contains(out, "before") || strings.Contains(out, "after") {
		t.Errorf("context leaked without --context:\n%s", out)
	}

	// With context: surrounding posts appear, indented two spaces.
	buf.Reset()
	printSearchHits(&buf, lbl, names, hits, true)
	out = buf.String()
	if !strings.Contains(out, "  [") || !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Errorf("context not rendered/indented:\n%s", out)
	}
	// The match itself stays flush-left (its line begins at column 0).
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "the match") && strings.HasPrefix(line, " ") {
			t.Errorf("match line should be flush-left: %q", line)
		}
	}
}

func TestPrintSearchSummary(t *testing.T) {
	cases := []struct {
		name                string
		shown, total        int
		capped              bool
		offset, limit       int
		wantShown, wantMore string
		wantNoMore          bool
	}{
		{"single", 1, 1, false, 0, 20, "showing 1 of 1 match\n", "", true},
		{"plural", 3, 3, false, 0, 20, "showing 3 of 3 matches\n", "", true},
		{"capped", 20, 500, true, 0, 20, "showing 20 of 500+ matches\n", "page with --offset 20", false},
		{"more", 20, 42, false, 0, 20, "showing 20 of 42 matches\n", "page with --offset 20", false},
		{"more paged", 20, 42, false, 20, 20, "showing 20 of 42 matches\n", "page with --offset 40", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			printSearchSummary(&buf, c.shown, c.total, c.capped, c.offset, c.limit)
			out := buf.String()
			if !strings.Contains(out, c.wantShown) {
				t.Errorf("missing %q in:\n%s", c.wantShown, out)
			}
			hasMore := strings.Contains(out, "more — page")
			if c.wantNoMore && hasMore {
				t.Errorf("unexpected paging hint:\n%s", out)
			}
			if c.wantMore != "" && !strings.Contains(out, c.wantMore) {
				t.Errorf("missing paging hint %q in:\n%s", c.wantMore, out)
			}
		})
	}
}

func TestIndentLines(t *testing.T) {
	if got := indentLines("", "  "); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
	got := indentLines("a\nb\n", "> ")
	if got != "> a\n> b\n" {
		t.Errorf("indentLines = %q", got)
	}
}
