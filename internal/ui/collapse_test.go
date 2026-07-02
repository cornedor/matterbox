package ui

import (
	"fmt"
	"strings"
	"testing"

	"matterbox/internal/config"

	"github.com/mattermost/mattermost/server/public/model"
)

// gutterLines builds n short, gutter-prefixed body lines, each one visual row
// wide at any realistic test width.
func gutterLines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("  line%02d", i)
	}
	return out
}

// TestCollapseBody exercises the fold helper in isolation: the row budget, the
// footer text, and the cases where it must leave the body untouched.
func TestCollapseBody(t *testing.T) {
	const width = 80

	t.Run("short body is left untouched", func(t *testing.T) {
		body := gutterLines(5)
		got := collapseBody(body, width, 12, 8, "z")
		if len(got) != len(body) {
			t.Fatalf("a body under the threshold must not fold: got %d lines, want %d", len(got), len(body))
		}
	})

	t.Run("long body folds to the preview plus a footer", func(t *testing.T) {
		body := gutterLines(30)
		got := collapseBody(body, width, 12, 8, "z")
		// 8 kept preview rows + 1 footer.
		if len(got) != 9 {
			t.Fatalf("folded body = %d lines, want 9:\n%s", len(got), strings.Join(got, "\n"))
		}
		footer := got[len(got)-1]
		if !strings.Contains(footer, "22 more lines") {
			t.Errorf("footer should report 22 hidden lines, got %q", footer)
		}
		if !strings.Contains(footer, "z to expand") {
			t.Errorf("footer should name the expand key, got %q", footer)
		}
		joined := strings.Join(got, "\n")
		if !strings.Contains(joined, "line00") {
			t.Errorf("preview should keep the first line, got:\n%s", joined)
		}
		if strings.Contains(joined, "line08") {
			t.Errorf("preview should hide line08 onward, got:\n%s", joined)
		}
	})

	t.Run("threshold zero disables folding", func(t *testing.T) {
		body := gutterLines(30)
		got := collapseBody(body, width, 0, 8, "z")
		if len(got) != len(body) {
			t.Fatalf("threshold 0 must disable folding: got %d lines, want %d", len(got), len(body))
		}
	})

	t.Run("a single hidden line reads as singular", func(t *testing.T) {
		body := gutterLines(4)
		got := collapseBody(body, width, 3, 3, "z")
		footer := got[len(got)-1]
		if !strings.Contains(footer, "1 more line ") || strings.Contains(footer, "more lines") {
			t.Errorf("one hidden line should read %q-style singular, got %q", "1 more line", footer)
		}
	})

	t.Run("a single over-tall line cannot be folded", func(t *testing.T) {
		// One line wider than the whole preview budget: there is no earlier
		// boundary to cut at, so the body is returned unchanged.
		body := []string{strings.Repeat("x", width*20)}
		got := collapseBody(body, width, 12, 8, "z")
		if len(got) != 1 {
			t.Fatalf("a single tall line has nothing to hide: got %d lines, want 1", len(got))
		}
	})
}

// collapsePost builds a post whose body is 40 distinguishable lines, comfortably
// over the test threshold.
func collapsePost() *model.Post {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "row number %02d here\n", i)
	}
	return &model.Post{Id: "p1", UserId: "u", Message: strings.TrimRight(b.String(), "\n"), CreateAt: 1000}
}

// collapseModel is a pagingModel with collapsing switched on.
func collapseModel(posts []*model.Post) Model {
	m := pagingModel(posts, 0)
	m.keys = newKeyMap("ctrl")
	m.collapseRows = 12
	m.collapseShow = 8
	m.collapseKeyHint = "z"
	return m
}

// TestNewCollapseThresholds checks how New() derives the two collapse knobs
// from config: an explicit collapse_preview_lines is honoured, an absent one
// falls back to two-thirds of the threshold, and a preview taller than the
// threshold is clamped down to it.
func TestNewCollapseThresholds(t *testing.T) {
	rows, preview := 20, 5
	cases := []struct {
		name             string
		threshold, prev  *int
		wantRows, wantSh int
	}{
		{"explicit preview honoured", &rows, &preview, 20, 5},
		{"absent preview derives two-thirds", &rows, nil, 20, 13},
		{"preview over threshold is clamped", intp(20), intp(50), 20, 20},
		{"preview clamps up to at least one", intp(20), intp(0), 20, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New(nil, &config.Config{
				CollapseLongMessages: c.threshold,
				CollapsePreviewLines: c.prev,
			})
			if m.collapseRows != c.wantRows {
				t.Errorf("collapseRows = %d; want %d", m.collapseRows, c.wantRows)
			}
			if m.collapseShow != c.wantSh {
				t.Errorf("collapseShow = %d; want %d", m.collapseShow, c.wantSh)
			}
		})
	}
}

func intp(v int) *int { return &v }

// TestRenderPostLinesCollapse: a long message renders folded by default and in
// full once the user expands it, and the cache serves the right variant across
// the toggle (the fingerprint carries the expanded bit).
func TestRenderPostLinesCollapse(t *testing.T) {
	p := collapsePost()
	m := collapseModel([]*model.Post{p})

	collapsed, crows := m.renderPostLines(p, false)
	coll := strings.Join(collapsed, "\n")
	if !strings.Contains(coll, "to expand") {
		t.Fatalf("collapsed render is missing the fold footer:\n%s", coll)
	}
	if !strings.Contains(coll, "row number 00") {
		t.Errorf("collapsed render should keep the head line:\n%s", coll)
	}
	if strings.Contains(coll, "row number 39") {
		t.Errorf("collapsed render should hide the tail line:\n%s", coll)
	}

	m.expandedPosts = map[string]bool{"p1": true}
	full, frows := m.renderPostLines(p, false)
	fl := strings.Join(full, "\n")
	if strings.Contains(fl, "to expand") {
		t.Errorf("expanded render should drop the fold footer:\n%s", fl)
	}
	if !strings.Contains(fl, "row number 39") {
		t.Errorf("expanded render should show the tail line:\n%s", fl)
	}
	if frows <= crows {
		t.Errorf("expanded post (%d rows) should be taller than collapsed (%d rows)", frows, crows)
	}
}

// TestRenderPostLinesCollapseDisabled: with collapseRows == 0 nothing folds,
// however long the message.
func TestRenderPostLinesCollapseDisabled(t *testing.T) {
	p := collapsePost()
	m := collapseModel([]*model.Post{p})
	m.collapseRows = 0

	lines, _ := m.renderPostLines(p, false)
	out := strings.Join(lines, "\n")
	if strings.Contains(out, "to expand") {
		t.Errorf("collapsing disabled but a fold footer appeared:\n%s", out)
	}
	if !strings.Contains(out, "row number 39") {
		t.Errorf("collapsing disabled should render the full body:\n%s", out)
	}
}

// TestCollapseKeepsReactions: only the body folds — reactions still render below
// the fold footer so a folded message doesn't hide its reactions.
func TestCollapseKeepsReactions(t *testing.T) {
	p := collapsePost()
	p.Metadata = &model.PostMetadata{Reactions: []*model.Reaction{{EmojiName: "tada", UserId: "u2", PostId: "p1"}}}
	m := collapseModel([]*model.Post{p})

	lines, _ := m.renderPostLines(p, false)
	footerIdx := -1
	for i, l := range lines {
		if strings.Contains(l, "to expand") {
			footerIdx = i
			break
		}
	}
	if footerIdx < 0 {
		t.Fatalf("expected a fold footer, got:\n%s", strings.Join(lines, "\n"))
	}
	if footerIdx >= len(lines)-1 {
		t.Errorf("reactions were folded away — nothing rendered after the footer:\n%s", strings.Join(lines, "\n"))
	}
}

// TestToggleCollapseKey: pressing the collapse key on the selected long message
// expands it, and pressing again re-folds it.
func TestToggleCollapseKey(t *testing.T) {
	p := collapsePost()
	m := collapseModel([]*model.Post{p})
	m.postIdx = 0
	m.renderMessages()

	out, _ := m.handleMessagesKey(keyPress('z'))
	expanded := out.(Model)
	if !expanded.expandedPosts["p1"] {
		t.Fatalf("z should expand the selected post")
	}
	full, _ := expanded.renderPostLines(p, false)
	if !strings.Contains(strings.Join(full, "\n"), "row number 39") {
		t.Errorf("after expand, the full body should render")
	}

	out2, _ := expanded.handleMessagesKey(keyPress('z'))
	refolded := out2.(Model)
	if refolded.expandedPosts["p1"] {
		t.Fatalf("a second z should re-collapse the post")
	}
	coll, _ := refolded.renderPostLines(p, false)
	if !strings.Contains(strings.Join(coll, "\n"), "to expand") {
		t.Errorf("after re-collapse, the fold footer should return")
	}
}

// TestToggleCollapseDisabled: with collapsing off, the toggle key is an inert
// no-op that just reports the state rather than mutating expandedPosts.
func TestToggleCollapseDisabled(t *testing.T) {
	p := collapsePost()
	m := collapseModel([]*model.Post{p})
	m.collapseRows = 0
	m.postIdx = 0

	out, _ := m.handleMessagesKey(keyPress('z'))
	got := out.(Model)
	if got.expandedPosts["p1"] {
		t.Errorf("collapsing disabled: z should not record an expansion")
	}
}
