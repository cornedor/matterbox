package forge

import "testing"

const sampleDiff = `@@ -1,4 +1,5 @@
 package main

-import "fmt"
+import (
+	"fmt"
+)

 func main() {
@@ -20 +21 @@ func other() {
-	old()
+	new()
`

func TestParseUnifiedDiff(t *testing.T) {
	got := ParseUnifiedDiff(sampleDiff)
	if len(got) == 0 {
		t.Fatal("no lines parsed")
	}
	if got[0].Kind != DiffHunk {
		t.Fatalf("first line = %+v, want a hunk header", got[0])
	}
	// " package main" is line 1 on both sides.
	if l := got[1]; l.Kind != DiffContext || l.Text != "package main" || l.OldLine != 1 || l.NewLine != 1 {
		t.Errorf("context line = %+v", l)
	}
	// The blank context line keeps both numbers.
	if l := got[2]; l.Kind != DiffContext || l.OldLine != 2 || l.NewLine != 2 {
		t.Errorf("blank context line = %+v", l)
	}
	// `-import "fmt"` is old line 3 and has no new number.
	if l := got[3]; l.Kind != DiffDel || l.Text != `import "fmt"` || l.OldLine != 3 || l.NewLine != 0 {
		t.Errorf("deleted line = %+v", l)
	}
	// The three added lines number 3,4,5 on the new side only.
	for i, want := range []int{3, 4, 5} {
		l := got[4+i]
		if l.Kind != DiffAdd || l.NewLine != want || l.OldLine != 0 {
			t.Errorf("added line %d = %+v, want new line %d", i, l, want)
		}
	}
	// A countless hunk header ("@@ -20 +21 @@") still sets both sides.
	var second []DiffLine
	for i, l := range got {
		if l.Kind == DiffHunk && i > 0 {
			second = got[i:]
			break
		}
	}
	if len(second) < 3 {
		t.Fatalf("second hunk missing: %+v", got)
	}
	if second[1].OldLine != 20 || second[1].Kind != DiffDel {
		t.Errorf("second hunk del = %+v, want old line 20", second[1])
	}
	if second[2].NewLine != 21 || second[2].Kind != DiffAdd {
		t.Errorf("second hunk add = %+v, want new line 21", second[2])
	}
}

// A "-" line inside a hunk is a removed line; only the preamble's ---/+++ are
// headers. This is the case a naive prefix check gets wrong.
func TestParseUnifiedDiffSkipsFileHeaders(t *testing.T) {
	d := `diff --git a/a.go b/a.go
index 1234567..89abcde 100644
--- a/a.go
+++ b/a.go
@@ -1,2 +1,2 @@
-removed
+added
`
	got := ParseUnifiedDiff(d)
	if len(got) != 3 {
		t.Fatalf("lines = %d (%+v), want 3 (hunk + del + add)", len(got), got)
	}
	if got[1].Text != "removed" || got[1].Kind != DiffDel {
		t.Errorf("del = %+v", got[1])
	}
	if got[2].Text != "added" || got[2].Kind != DiffAdd {
		t.Errorf("add = %+v", got[2])
	}
}

func TestParseUnifiedDiffNoNewlineMarker(t *testing.T) {
	d := "@@ -1 +1 @@\n-a\n+b\n\\ No newline at end of file\n"
	got := ParseUnifiedDiff(d)
	last := got[len(got)-1]
	if last.Kind != DiffMeta {
		t.Errorf("last = %+v, want meta", last)
	}
	// The marker must not consume a line number.
	if got[2].NewLine != 1 {
		t.Errorf("added line = %+v", got[2])
	}
}

func TestParseUnifiedDiffBinary(t *testing.T) {
	got := ParseUnifiedDiff("Binary files a/x.png and b/x.png differ\n")
	if len(got) != 1 || got[0].Kind != DiffMeta {
		t.Fatalf("got %+v, want one meta line", got)
	}
}

func TestFileDiffPath(t *testing.T) {
	if p := (FileDiff{NewPath: "new.go", OldPath: "old.go"}).Path(); p != "new.go" {
		t.Errorf("path = %q, want the new path", p)
	}
	if p := (FileDiff{OldPath: "gone.go", Deleted: true}).Path(); p != "gone.go" {
		t.Errorf("deleted path = %q", p)
	}
}

func TestStore(t *testing.T) {
	var s Store[*Diff]
	if _, ok := s.Get("g/p", 1); ok {
		t.Error("empty store reported a hit")
	}
	d := &Diff{Truncated: true}
	s.Put("g/p", 1, d)
	// Keyed case-insensitively, like Cache.
	if got, ok := s.Get("G/P", 1); !ok || got != d {
		t.Errorf("get = %v, %v", got, ok)
	}
	s.Invalidate("g/p", 1)
	if _, ok := s.Get("g/p", 1); ok {
		t.Error("invalidated entry still there")
	}
}
