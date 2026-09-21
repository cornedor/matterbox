package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"matterbox/internal/forge"
	"matterbox/internal/forge/gitlab"
)

// stubReviewer is the GitLab provider with its three review calls stubbed out,
// so the view can be driven without a server. Embedding the real client keeps
// link detection, labels and the rest of Provider honest.
type stubReviewer struct {
	*gitlab.Client
	diff       *forge.Diff
	threads    []forge.Thread
	change     *forge.Change
	getCalls   int
	posted     []forge.NewNote
	addErr     error
	resolved   []resolveCall
	resolveErr error
	threadCall int
}

// Get answers from the fixture instead of the embedded client's network call,
// so a test can drive the panel's own fetch path.
func (s *stubReviewer) Get(_ context.Context, repo string, number int, _ string) (*forge.Change, error) {
	s.getCalls++
	if s.change != nil {
		return s.change, nil
	}
	ch := sampleMR()
	ch.Repo, ch.Number = repo, number
	return ch, nil
}

func (s *stubReviewer) Diff(context.Context, string, int) (*forge.Diff, error) {
	return s.diff, nil
}

func (s *stubReviewer) Threads(context.Context, string, int) ([]forge.Thread, error) {
	s.threadCall++
	return s.threads, nil
}

func (s *stubReviewer) AddNote(_ context.Context, _ string, _ int, n forge.NewNote) error {
	s.posted = append(s.posted, n)
	return s.addErr
}

func (s *stubReviewer) ResolveThread(_ context.Context, _ string, _ int, id string, resolved bool) error {
	s.resolved = append(s.resolved, resolveCall{id: id, resolved: resolved})
	for i := range s.threads {
		if s.threads[i].ID == id {
			s.threads[i].Resolved = resolved
		}
	}
	return s.resolveErr
}

// resolveCall records one ResolveThread, so a test can assert what was asked of
// the forge rather than only what the view then showed.
type resolveCall struct {
	id       string
	resolved bool
}

const goFileDiff = `@@ -1,4 +1,5 @@
 package main

-import "fmt"
+import (
+	"fmt"
+)

 func main() {
`

func sampleDiff() *forge.Diff {
	return &forge.Diff{
		Refs: forge.DiffRefs{BaseSHA: "base", StartSHA: "start", HeadSHA: "head"},
		Files: []forge.FileDiff{
			{OldPath: "main.go", NewPath: "main.go", Diff: goFileDiff},
			{OldPath: "", NewPath: "docs/readme.md", New: true, Diff: "@@ -0,0 +1 @@\n+hello\n"},
		},
	}
}

// openDiff opens the review view on a loaded merge request and lands the fetch,
// as the real Cmd would.
func openDiff(t *testing.T, threads []forge.Thread) (Model, *stubReviewer) {
	t.Helper()
	return openDiffWith(t, &stubReviewer{diff: sampleDiff(), threads: threads})
}

// keyOf builds a key press from its name, for the modal handlers that switch on
// msg.String().
func keyOf(s string) tea.KeyPressMsg {
	if len(s) == 1 {
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Text: s}
}

// openDiffStub installs stub as the GitLab provider on m, wired to the same
// instance the link fixtures point at.
func openDiffStub(t *testing.T, m Model, stub *stubReviewer) Model {
	t.Helper()
	stub.Client = gitlab.New(gitlab.Config{BaseURL: "https://git.example.com", Token: "tok"})
	m.forges[forgeGitLab] = stub
	return m
}

// openDiffWith is openDiff with the stub's diff and threads chosen by the caller.
func openDiffWith(t *testing.T, stub *stubReviewer) (Model, *stubReviewer) {
	t.Helper()
	stub.Client = gitlab.New(gitlab.Config{BaseURL: "https://git.example.com", Token: "tok"})
	base := configuredForgeModel(t)
	base.forges[forgeGitLab] = stub
	m := openLoadedChange(t, base, forgeGitLab, mrLink, sampleMR())

	updated, cmd := m.openDiffView()
	m = updated.(Model)
	if m.diff == nil {
		t.Fatalf("diff view did not open: %s", m.status)
	}
	if cmd == nil {
		t.Fatal("no fetch command")
	}
	msg, ok := cmd().(diffLoadedMsg)
	if !ok {
		t.Fatalf("fetch returned %T", cmd())
	}
	final, _ := m.handleDiffLoaded(msg)
	return final.(Model), stub
}

func TestDiffViewBuildsRows(t *testing.T) {
	m, _ := openDiff(t, nil)
	d := m.diff
	if d.err != nil {
		t.Fatalf("load error: %v", d.err)
	}
	// Two file headers, and the first is main.go.
	var headers []string
	for _, r := range d.rows {
		if r.kind == diffRowFile {
			headers = append(headers, r.text)
		}
	}
	if len(headers) != 2 || headers[0] != "main.go" || !strings.HasPrefix(headers[1], "docs/readme.md") {
		t.Fatalf("file headers = %v", headers)
	}
	if !strings.Contains(headers[1], "(new)") {
		t.Errorf("a new file is not labelled: %q", headers[1])
	}
	// The removed import keeps its old line number and has no new one.
	var del *diffRow
	for i := range d.rows {
		if d.rows[i].kind == diffRowDel {
			del = &d.rows[i]
			break
		}
	}
	if del == nil || del.old != 3 || del.new != 0 {
		t.Fatalf("removed row = %+v", del)
	}
	// The cursor starts on a line a note can hang off.
	if !d.rows[d.cursor].kind.commentable() {
		t.Errorf("cursor starts on %v, which takes no note", d.rows[d.cursor].kind)
	}
}

func TestDiffViewRendersTintedRows(t *testing.T) {
	m, _ := openDiff(t, nil)
	out := m.renderDiffView(30)
	if !strings.Contains(ansi.Strip(out), "package main") {
		t.Fatalf("code missing from the frame:\n%s", ansi.Strip(out))
	}
	if !strings.Contains(ansi.Strip(out), "main.go") {
		t.Error("file header missing")
	}
	// An added row is painted with a background that survives to the end of the
	// row: chroma resets after every token, and a bare reset would drop the tint.
	d := m.diff
	var add int
	for i, r := range d.rows {
		if r.kind == diffRowAdd {
			add = i
			break
		}
	}
	row := d.renderRow(add, 60, false)
	if !strings.Contains(row, "\x1b[48;") && !strings.Contains(row, "\x1b[4") {
		t.Fatalf("added row carries no background: %q", row)
	}
	if i := strings.Index(row, "\x1b[0m"); i >= 0 && i < len(row)-len("\x1b[0m") {
		t.Errorf("a reset in the middle of the row kills its tint: %q", row)
	}
	if w := ansi.StringWidth(row); w != 60 {
		t.Errorf("row width = %d, want 60 so the tint reaches the edge", w)
	}
	if !strings.Contains(ansi.Strip(row), "+") {
		t.Errorf("added row has no + marker: %q", ansi.Strip(row))
	}
}

func TestDiffViewNavigation(t *testing.T) {
	m, _ := openDiff(t, nil)
	m.renderDiffView(30) // sizes the window, which paging needs
	d := m.diff
	start := d.cursor

	m.handleDiffKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if d.cursor != start+1 {
		t.Errorf("j moved to %d, want %d", d.cursor, start+1)
	}
	m.handleDiffKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if d.cursor != start {
		t.Errorf("k moved to %d, want %d", d.cursor, start)
	}
	// ] jumps to the next file header and parks it at the top of the window.
	m.handleDiffKey(tea.KeyPressMsg{Code: ']', Text: "]"})
	if d.rows[d.cursor].kind != diffRowFile || d.rows[d.cursor].file != 1 {
		t.Fatalf("] landed on %+v, want the second file header", d.rows[d.cursor])
	}
	// ←/→ pan.
	m.handleDiffKey(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if d.hscroll != diffHScrollStep {
		t.Errorf("hscroll = %d, want %d", d.hscroll, diffHScrollStep)
	}
	m.handleDiffKey(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if d.hscroll != 0 {
		t.Errorf("hscroll = %d, want 0", d.hscroll)
	}
	// esc closes.
	updated, _ := m.handleDiffKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if updated.(Model).diff != nil {
		t.Error("esc did not close the view")
	}
}

// The note has to carry the line numbers of the row it was written on, with the
// side that does not exist left at zero — that is what the forge validates.
func TestDiffViewNoteOnAddedLine(t *testing.T) {
	m, stub := openDiff(t, nil)
	d := m.diff
	for i, r := range d.rows {
		if r.kind == diffRowAdd {
			d.cursor = i
			break
		}
	}
	added := d.rows[d.cursor]

	updated, _ := m.openDiffNote()
	m = updated.(Model)
	if !m.diffNoteActive() {
		t.Fatal("c did not open the note composer")
	}
	if !strings.Contains(m.diff.note.context, "main.go:") {
		t.Errorf("context line = %q, want the file and line", m.diff.note.context)
	}
	m.diff.note.input.SetValue("nit: group the imports")
	posted, cmd := m.applyDiffNote()
	m = posted.(Model)
	if m.diffNoteActive() {
		t.Error("composer stayed open after posting")
	}
	if cmd == nil {
		t.Fatal("no post command")
	}
	msg, ok := cmd().(diffNotePostedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("post returned %v", cmd())
	}
	if len(stub.posted) != 1 {
		t.Fatalf("posted %d notes", len(stub.posted))
	}
	n := stub.posted[0]
	if n.Body != "nit: group the imports" {
		t.Errorf("body = %q", n.Body)
	}
	if n.NewLine != added.new || n.OldLine != 0 {
		t.Errorf("position = old %d/new %d, want old 0/new %d", n.OldLine, n.NewLine, added.new)
	}
	if n.NewPath != "main.go" || n.Refs.HeadSHA != "head" {
		t.Errorf("anchor = %+v", n)
	}
	if n.ReplyTo != "" {
		t.Errorf("a new conversation carries a reply target: %q", n.ReplyTo)
	}
}

// An existing conversation is drawn under its line, and the comment key replies
// to it instead of starting another one.
func TestDiffViewExistingThreads(t *testing.T) {
	threads := []forge.Thread{{
		ID: "abc", Path: "main.go", NewLine: 3,
		Notes: []forge.Note{{Author: "Grace Hopper", Body: "why the parens?"}},
	}}
	m, stub := openDiff(t, threads)
	d := m.diff

	note := -1
	for i, r := range d.rows {
		if r.kind == diffRowNote {
			note = i
			break
		}
	}
	if note < 0 {
		t.Fatal("the existing conversation was not drawn")
	}
	if !strings.Contains(d.rows[note].text, "Grace Hopper") {
		t.Errorf("note head = %q", d.rows[note].text)
	}
	// It hangs off new line 3 — the first added line.
	if prev := d.rows[note-1]; prev.new != 3 {
		t.Errorf("note attached under new line %d, want 3", prev.new)
	}
	// n jumps to it.
	d.cursor = 0
	m.handleDiffKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if d.cursor != note {
		t.Errorf("n landed on %d, want the note row %d", d.cursor, note)
	}
	updated, _ := m.openDiffNote()
	m = updated.(Model)
	m.diff.note.input.SetValue("readability")
	_, cmd := m.applyDiffNote()
	if cmd == nil {
		t.Fatal("no post command")
	}
	cmd()
	if len(stub.posted) != 1 || stub.posted[0].ReplyTo != "abc" {
		t.Fatalf("posted = %+v, want a reply to abc", stub.posted)
	}
}

// A forge without the review half says so rather than opening a broken view.
func TestDiffViewUnsupportedForge(t *testing.T) {
	base := configuredForgeModel(t)
	m := openLoadedChange(t, base, forgeGitHub, prLink, samplePR())
	updated, cmd := m.openDiffView()
	got := updated.(Model)
	if got.diff != nil {
		t.Fatal("opened a diff view for a forge that has none")
	}
	if cmd != nil {
		t.Error("fetched anyway")
	}
	if !strings.Contains(got.status, "no diff view yet") {
		t.Errorf("status = %q", got.status)
	}
}

// A hunk header, a file header and a note row take no note: the forge has no
// line to anchor one to.
func TestDiffViewNoteNeedsALine(t *testing.T) {
	m, _ := openDiff(t, nil)
	d := m.diff
	for i, r := range d.rows {
		if r.kind == diffRowHunk {
			d.cursor = i
			break
		}
	}
	updated, _ := m.openDiffNote()
	got := updated.(Model)
	if got.diffNoteActive() {
		t.Fatal("opened a composer on a hunk header")
	}
	if !strings.Contains(got.status, "no line here") {
		t.Errorf("status = %q", got.status)
	}
}

func TestKeepBGLeavesBackgroundStanding(t *testing.T) {
	// chroma's shape: a coloured span closed by a full reset.
	in := "\x1b[38;2;1;2;3mfoo\x1b[0m bar"
	out := keepBG(in)
	if strings.Contains(out, "\x1b[0m") {
		t.Errorf("a full reset survived: %q", out)
	}
	if ansi.Strip(out) != "foo bar" {
		t.Errorf("text changed: %q", ansi.Strip(out))
	}
	if keepBG("plain") != "plain" {
		t.Error("unstyled text was rewritten")
	}
}

// A note on a removed line carries the old-side number and no new one — the
// mirror image of the added-line case, and the other half of what the forge
// validates.
func TestDiffViewNoteOnRemovedLine(t *testing.T) {
	m, stub := openDiff(t, nil)
	d := m.diff
	for i, r := range d.rows {
		if r.kind == diffRowDel {
			d.cursor = i
			break
		}
	}
	removed := d.rows[d.cursor]
	updated, _ := m.openDiffNote()
	m = updated.(Model)
	m.diff.note.input.SetValue("why?")
	_, cmd := m.applyDiffNote()
	cmd()
	if len(stub.posted) != 1 {
		t.Fatalf("posted %d notes", len(stub.posted))
	}
	n := stub.posted[0]
	if n.OldLine != removed.old || n.NewLine != 0 {
		t.Errorf("position = old %d/new %d, want old %d/new 0", n.OldLine, n.NewLine, removed.old)
	}
}

// Posting a note reloads the conversations (the diff itself is a cache hit) so
// the new note appears where it landed.
func TestDiffViewReloadsAfterNote(t *testing.T) {
	m, stub := openDiff(t, nil)
	before := stub.threadCall
	updated, cmd := m.handleDiffNotePosted(diffNotePostedMsg{gen: m.diff.gen})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("no reload after a posted note")
	}
	cmd()
	if stub.threadCall != before+1 {
		t.Errorf("threads fetched %d times, want one more than %d", stub.threadCall, before)
	}
	if m.status != "note added" {
		t.Errorf("status = %q", m.status)
	}
}

// A result the user has already closed or refreshed past is dropped.
func TestDiffViewStaleLoadIgnored(t *testing.T) {
	m, _ := openDiff(t, nil)
	rows := len(m.diff.rows)
	updated, _ := m.handleDiffLoaded(diffLoadedMsg{gen: m.diff.gen - 1, diff: &forge.Diff{}})
	if got := len(updated.(Model).diff.rows); got != rows {
		t.Errorf("a stale load replaced the rows: %d → %d", rows, got)
	}
}

// Reloading keeps the cursor on the line it was on, even though the rows around
// it shift when a conversation appears above.
func TestDiffViewReloadKeepsCursorLine(t *testing.T) {
	m, stub := openDiff(t, nil)
	d := m.diff
	for i, r := range d.rows {
		if r.kind == diffRowAdd && r.new == 5 {
			d.cursor = i
			break
		}
	}
	// A conversation lands on an earlier line, pushing every row below it down.
	stub.threads = []forge.Thread{{
		ID: "abc", Path: "main.go", NewLine: 3,
		Notes: []forge.Note{{Author: "Grace Hopper", Body: "one\ntwo"}},
	}}
	cmd := m.reloadDiffThreads()
	msg := cmd().(diffLoadedMsg)
	updated, _ := m.handleDiffLoaded(msg)
	got := updated.(Model).diff
	if r := got.rows[got.cursor]; r.new != 5 || r.kind != diffRowAdd {
		t.Errorf("cursor landed on %+v, want the added line 5", r)
	}
}
