package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/forge"
)

// treeDiff is a change request spread over a few directories, which is what
// makes a tree worth drawing.
func treeDiff() *forge.Diff {
	f := func(p, body string) forge.FileDiff {
		return forge.FileDiff{OldPath: p, NewPath: p, Diff: body}
	}
	return &forge.Diff{
		Refs: forge.DiffRefs{BaseSHA: "b", StartSHA: "s", HeadSHA: "h"},
		Files: []forge.FileDiff{
			f("internal/forge/diff.go", "@@ -1,2 +1,3 @@\n package forge\n-old\n+new\n+more\n"),
			f("internal/forge/gitlab/diff.go", "@@ -1,2 +1,2 @@\n package gitlab\n-a\n+b\n"),
			f("internal/ui/diffview.go", goFileDiff),
			f("README.md", "@@ -1 +1 @@\n-old\n+new\n"),
		},
	}
}

func openTreeDiff(t *testing.T, threads []forge.Thread) (Model, *stubReviewer) {
	t.Helper()
	m, stub := openDiffWith(t, &stubReviewer{diff: treeDiff(), threads: threads})
	m.width, m.height = 120, 30
	m.renderDiffView(26) // sizes both panels, which every move clamps against
	return m, stub
}

func TestBuildDiffTree(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	d := m.diff

	var got []string
	for _, r := range d.tree {
		kind := "file"
		if r.file < 0 {
			kind = "dir"
		}
		got = append(got, strings.Repeat(" ", r.depth*2)+r.label+" ("+kind+")")
	}
	want := []string{
		"internal/ (dir)",
		"  forge/ (dir)",
		"    diff.go (file)",
		"    gitlab/ (dir)",
		"      diff.go (file)",
		"  ui/ (dir)",
		"    diffview.go (file)",
		"README.md (file)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("tree =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Each file's row is findable both ways.
	for fi := range d.diff.Files {
		ti := d.treeOf[fi]
		if d.tree[ti].file != fi {
			t.Errorf("treeOf[%d] = %d, which is file %d", fi, ti, d.tree[ti].file)
		}
		if head := d.fileHead[fi]; d.rows[head].kind != diffRowFile || d.rows[head].file != fi {
			t.Errorf("fileHead[%d] = %d, which is %+v", fi, head, d.rows[head])
		}
	}
	// The counts come off the parsed hunks.
	first := d.tree[d.treeOf[0]]
	if first.adds != 2 || first.dels != 1 {
		t.Errorf("internal/forge/diff.go counts = +%d -%d, want +2 -1", first.adds, first.dels)
	}
}

func TestDiffTreeCountsThreads(t *testing.T) {
	m, _ := openTreeDiff(t, []forge.Thread{{
		ID: "abc", Path: "internal/ui/diffview.go", NewLine: 3,
		Notes: []forge.Note{{Author: "Grace Hopper", Body: "two\nlines"}},
	}})
	d := m.diff
	row := d.tree[d.treeOf[2]]
	if row.threads != 1 {
		t.Errorf("diffview.go threads = %d, want 1 (a two-line note is still one conversation)", row.threads)
	}
	if other := d.tree[d.treeOf[0]]; other.threads != 0 {
		t.Errorf("a file with no conversation reports %d", other.threads)
	}
}

func TestDiffTreeTabSwitchesPanels(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	d := m.diff
	if d.treeFocus {
		t.Fatal("the view opens with the diff focused, not the tree")
	}
	m.handleDiffKey(keyOf("tab"))
	if !d.treeFocus {
		t.Fatal("tab did not move to the tree")
	}
	m.handleDiffKey(keyOf("tab"))
	if d.treeFocus {
		t.Fatal("tab did not move back to the diff")
	}
}

// Moving in the tree moves the diff to that file; moving in the diff moves the
// tree's highlight to the file you scrolled into. One selection, two panels.
func TestDiffTreeSelectionFollowsBothWays(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	d := m.diff

	m.handleDiffKey(keyOf("tab"))
	m.handleDiffKey(keyOf("j"))
	if d.tree[d.treeIdx].file != 1 {
		t.Fatalf("tree landed on file %d, want the second file", d.tree[d.treeIdx].file)
	}
	if r := d.rows[d.cursor]; r.kind != diffRowFile || r.file != 1 {
		t.Errorf("the diff did not follow the tree: cursor on %+v", r)
	}
	// Directory rows are stepped over, never landed on.
	for i := 0; i < 6; i++ {
		m.handleDiffKey(keyOf("j"))
		if d.tree[d.treeIdx].file < 0 {
			t.Fatalf("the highlight stopped on the directory row %q", d.tree[d.treeIdx].label)
		}
	}

	// Back to the diff: scrolling into another file drags the highlight along.
	m.handleDiffKey(keyOf("tab"))
	m.handleDiffKey(keyOf("g")) // top of the diff
	if d.tree[d.treeIdx].file != 0 {
		t.Fatalf("tree highlight on file %d after jumping to the top", d.tree[d.treeIdx].file)
	}
	m.handleDiffKey(keyOf("]"))
	if d.tree[d.treeIdx].file != 1 {
		t.Errorf("tree highlight on file %d after ] in the diff", d.tree[d.treeIdx].file)
	}
}

// ←/→ pan whichever panel has the keys — the reason tab exists.
func TestDiffTreePanIsPerPanel(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	d := m.diff
	m.handleDiffKey(keyOf("l"))
	if d.hscroll != diffHScrollStep || d.treeHScroll != 0 {
		t.Errorf("with the diff focused: diff pan %d, tree pan %d", d.hscroll, d.treeHScroll)
	}
	m.handleDiffKey(keyOf("tab"))
	m.handleDiffKey(keyOf("l"))
	if d.treeHScroll != diffHScrollStep || d.hscroll != diffHScrollStep {
		t.Errorf("with the tree focused: diff pan %d, tree pan %d", d.hscroll, d.treeHScroll)
	}
	m.handleDiffKey(keyOf("h"))
	if d.treeHScroll != 0 {
		t.Errorf("tree pan = %d, want 0", d.treeHScroll)
	}
}

// Enter hands the keys back to the diff, on the file the tree picked.
func TestDiffTreeEnterReturnsToTheDiff(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	d := m.diff
	m.handleDiffKey(keyOf("tab"))
	m.handleDiffKey(keyOf("j"))
	m.handleDiffKey(keyOf("enter"))
	if d.treeFocus {
		t.Error("enter did not return to the diff")
	}
	if d.rows[d.cursor].file != 1 {
		t.Errorf("cursor left on file %d", d.rows[d.cursor].file)
	}
}

// Every row of the frame is the same width — the two columns are stitched per
// row, so a miscounted cell anywhere shows up as a ragged box.
func TestDiffViewFrameRowsAreFlush(t *testing.T) {
	m, _ := openTreeDiff(t, []forge.Thread{{
		ID: "abc", Path: "internal/ui/diffview.go", NewLine: 3,
		Notes: []forge.Note{{Author: "Grace Hopper", Body: "why?"}},
	}})
	lines := strings.Split(m.renderDiffView(26), "\n")
	want := ansi.StringWidth(lines[0])
	for i, ln := range lines {
		if got := ansi.StringWidth(ln); got != want {
			t.Fatalf("row %d is %d cells, want %d: %q", i, got, want, ansi.Strip(ln))
		}
	}
	// And the tree is actually in there.
	if !strings.Contains(ansi.Strip(lines[3]), "internal/") {
		t.Errorf("no tree in the frame: %q", ansi.Strip(lines[3]))
	}
}

// A narrow view drops the tree rather than squeezing both panels, and tab then
// has nowhere to go.
func TestDiffViewNarrowDropsTheTree(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	m.width, m.height = 50, 30
	out := m.renderDiffView(26)
	// The diff's own file headers name the same paths, so the tell is the
	// column seam: a body row has the two frame edges and, with a tree, a
	// divider between them.
	for _, ln := range strings.Split(out, "\n")[2:5] {
		if n := strings.Count(ansi.Strip(ln), "│"); n > 2 {
			t.Errorf("a column divider survived a narrow terminal: %q", ansi.Strip(ln))
		}
	}
	if m.diffTreeShown() {
		t.Fatal("the tree reports itself shown on a narrow view")
	}
	m.handleDiffKey(keyOf("tab"))
	if m.diff.treeFocus {
		t.Error("tab moved to a tree that is not on screen")
	}
}

func TestDiffTreeWidth(t *testing.T) {
	if w := diffTreeWidth(diffPanesMinWidth - 1); w != 0 {
		t.Errorf("narrow view got a %d-cell tree", w)
	}
	if w := diffTreeWidth(80); w < diffTreeMinWidth || w > diffTreeMaxWidth {
		t.Errorf("tree width = %d, outside [%d,%d]", w, diffTreeMinWidth, diffTreeMaxWidth)
	}
	if w := diffTreeWidth(400); w != diffTreeMaxWidth {
		t.Errorf("tree width on a huge terminal = %d, want the cap %d", w, diffTreeMaxWidth)
	}
}
