package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/forge"
)

// rowsOnScreen is what the code panel would draw right now.
func rowsOnScreen(d *diffState) []diffRow {
	out := make([]diffRow, 0, len(d.visible))
	for _, i := range d.visible {
		out = append(out, d.rows[i])
	}
	return out
}

func TestDiffFoldHidesAFilesLines(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	d := m.diff
	before := len(d.visible)

	m.handleDiffKey(keyOf("z")) // fold the file the cursor is in (the first)
	if !d.collapsed[0] {
		t.Fatal("z did not fold the file")
	}
	if len(d.visible) >= before {
		t.Errorf("visible rows %d → %d, expected fewer", before, len(d.visible))
	}
	for _, r := range rowsOnScreen(d) {
		if r.file == 0 && r.kind != diffRowFile {
			t.Fatalf("a folded file still shows %+v", r)
		}
	}
	// Its header stays, and the cursor comes up to it.
	if d.rows[d.cursor].kind != diffRowFile || d.rows[d.cursor].file != 0 {
		t.Errorf("cursor left at %+v, want the file header", d.rows[d.cursor])
	}
	// And the header says what it is holding back.
	row := ansi.Strip(d.renderRow(d.cursor, 80, true))
	if !strings.Contains(row, "▸") || !strings.Contains(row, "folded") {
		t.Errorf("folded header = %q", row)
	}

	m.handleDiffKey(keyOf("z"))
	if d.collapsed[0] || len(d.visible) != before {
		t.Errorf("unfolding did not restore the rows: %d, want %d", len(d.visible), before)
	}
}

func TestDiffFoldAll(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	d := m.diff
	m.handleDiffKey(keyOf("Z"))
	if !d.allCollapsed() {
		t.Fatal("Z did not fold everything")
	}
	// Exactly one row per file: the headers.
	if len(d.visible) != len(d.diff.Files) {
		t.Errorf("visible rows = %d, want one per file (%d)", len(d.visible), len(d.diff.Files))
	}
	// Which makes ↑/↓ walk the files, and the tree follow.
	m.handleDiffKey(keyOf("j"))
	if d.rows[d.cursor].file != 1 || d.tree[d.treeIdx].file != 1 {
		t.Errorf("after j: cursor on file %d, tree on file %d", d.rows[d.cursor].file, d.tree[d.treeIdx].file)
	}
	m.handleDiffKey(keyOf("Z"))
	if d.allCollapsed() {
		t.Error("Z did not unfold")
	}
}

// Folding from the tree folds the file the tree is pointing at.
func TestDiffFoldFromTheTree(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	d := m.diff
	m.handleDiffKey(keyOf("tab"))
	m.handleDiffKey(keyOf("j")) // second file
	m.handleDiffKey(keyOf("z"))
	if !d.collapsed[1] {
		t.Fatalf("z in the tree folded %v", d.collapsed)
	}
}

// A conversation inside a folded file is off screen, so n skips past it.
func TestDiffFoldSkipsHiddenThreads(t *testing.T) {
	m, _ := openTreeDiff(t, []forge.Thread{{
		ID: "abc", Path: "internal/forge/diff.go", NewLine: 2,
		Notes: []forge.Note{{Author: "Grace Hopper", Body: "hm"}},
	}})
	d := m.diff
	if !d.moveThread(1) {
		t.Fatal("the conversation is not reachable to begin with")
	}
	d.setPos(0)
	d.toggleCollapse(0)
	if d.moveThread(1) {
		t.Errorf("n landed on %+v inside a folded file", d.rows[d.cursor])
	}
}

// A reload keeps what you folded: you are working through the diff, and the
// files you have dealt with should stay dealt with.
func TestDiffFoldSurvivesAReload(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	d := m.diff
	d.toggleCollapse(1)
	cmd := m.reloadDiffThreads()
	updated, _ := m.handleDiffLoaded(cmd().(diffLoadedMsg))
	if !updated.(Model).diff.collapsed[1] {
		t.Error("the reload unfolded a file")
	}
}

func threadedDiff(t *testing.T, resolved bool) (Model, *stubReviewer) {
	t.Helper()
	return openTreeDiff(t, []forge.Thread{{
		ID: "abc", Path: "internal/ui/diffview.go", NewLine: 3, Resolved: resolved,
		Notes: []forge.Note{{Author: "Grace Hopper", Body: "why the parens?"}},
	}})
}

// R resolves the conversation the cursor is on — including from the code line
// it hangs off, which is where you are when you have just read it.
func TestDiffResolveFromTheLine(t *testing.T) {
	m, stub := threadedDiff(t, false)
	d := m.diff
	note := -1
	for i, r := range d.rows {
		if r.kind == diffRowNote {
			note = i
			break
		}
	}
	if note < 0 {
		t.Fatal("no conversation in the fixture")
	}
	// Standing on the code line above it.
	d.setPos(d.visPos[note-1])
	if ti := d.threadAtCursor(); ti != 0 {
		t.Fatalf("threadAtCursor from the code line = %d, want 0", ti)
	}
	updated, cmd := m.toggleDiffResolve()
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("R did nothing")
	}
	msg, ok := cmd().(diffResolvedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("resolve returned %v", cmd())
	}
	if len(stub.resolved) != 1 || stub.resolved[0].id != "abc" || !stub.resolved[0].resolved {
		t.Fatalf("forge was asked %+v, want abc→resolved", stub.resolved)
	}
	// The view reloads so the forge's answer, not ours, is what shows.
	done, reload := m.handleDiffResolved(msg)
	if reload == nil {
		t.Error("no reload after resolving")
	}
	if !strings.Contains(done.(Model).status, "resolved") {
		t.Errorf("status = %q", done.(Model).status)
	}
}

// R on an already-resolved conversation reopens it.
func TestDiffResolveTogglesBack(t *testing.T) {
	m, stub := threadedDiff(t, true)
	d := m.diff
	for i, r := range d.rows {
		if r.kind == diffRowNote {
			d.setPos(d.visPos[i])
			break
		}
	}
	_, cmd := m.toggleDiffResolve()
	if cmd == nil {
		t.Fatal("R did nothing")
	}
	cmd()
	if len(stub.resolved) != 1 || stub.resolved[0].resolved {
		t.Fatalf("forge was asked %+v, want a reopen", stub.resolved)
	}
}

func TestDiffResolveNeedsAThread(t *testing.T) {
	m, stub := openTreeDiff(t, nil)
	updated, cmd := m.toggleDiffResolve()
	if cmd != nil || len(stub.resolved) != 0 {
		t.Error("resolved something with no conversation on screen")
	}
	if !strings.Contains(updated.(Model).status, "no inline thread") {
		t.Errorf("status = %q", updated.(Model).status)
	}
}

// The tree counts what is still open, and marks a file whose conversations have
// all been answered.
func TestDiffTreeThreadCountsSplitByState(t *testing.T) {
	m, _ := threadedDiff(t, false)
	if got := m.diff.tree[m.diff.treeOf[2]]; got.threads != 1 || got.resolved != 0 {
		t.Errorf("open thread: %+v", got.diffFileStat)
	}
	m2, _ := threadedDiff(t, true)
	row := m2.diff.tree[m2.diff.treeOf[2]]
	if row.threads != 0 || row.resolved != 1 {
		t.Errorf("resolved thread: %+v", row.diffFileStat)
	}
	if tail := ansi.Strip(treeRowTail(row)); !strings.Contains(tail, "✓") {
		t.Errorf("tree tail = %q, want the all-answered mark", tail)
	}
}

// Fold every file and the header rows are all there is — so a header has to
// carry the cursor like any other row. It didn't, which left the folded view
// with no visible selection at all.
func TestDiffFoldedHeaderShowsTheCursor(t *testing.T) {
	m, _ := openTreeDiff(t, nil)
	d := m.diff
	m.handleDiffKey(keyOf("Z"))

	head := d.cursor
	if d.rows[head].kind != diffRowFile {
		t.Fatalf("with everything folded the cursor sits on %+v", d.rows[head])
	}
	on := d.renderRow(head, 60, true)
	off := d.renderRow(head, 60, false)
	if on == off {
		t.Error("a file header renders the same with and without the cursor")
	}
	// And it is still a header, not just a coloured bar.
	if !strings.Contains(ansi.Strip(on), "internal/forge/diff.go") {
		t.Errorf("cursor row = %q", ansi.Strip(on))
	}

	// Moving down moves the highlight with it: exactly one drawn row is the
	// cursor row at any time.
	m.handleDiffKey(keyOf("j"))
	lit := 0
	for _, i := range d.visible {
		if d.renderRow(i, 60, i == d.cursor) != d.renderRow(i, 60, false) {
			lit++
		}
	}
	if lit != 1 {
		t.Errorf("%d rows render as the cursor, want exactly 1", lit)
	}
}
