package ui

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/mattermost/mattermost/server/public/model"
)

func previewModel() *Model {
	return &Model{filePrev: newFilePreviews("auto")}
}

// mustRender renders a settled entry, failing the test if it is not settled — the
// tests install content directly, so an unsettled entry means the plumbing broke.
func mustRender(t *testing.T, m *Model, e *filePreviewEntry, width int) []string {
	t.Helper()
	lines, settled := m.filePrev.render(e, width)
	if !settled {
		t.Fatal("entry is not settled: markReady did not take")
	}
	return lines
}

// previewLines is filePreviewFileLines with the shape resolved, which is what
// renderAttachments does for real.
func (m *Model) previewLines(p *model.Post, f *model.FileInfo, width int) []string {
	kind, lex := m.filePreviewShape(f)
	return m.filePreviewFileLines(p, f, kind, lex, width)
}

func textFile(name string, size int64) *model.FileInfo {
	return &model.FileInfo{Id: "f1", Name: name, Size: size}
}

// TestFilePreviewKindOf: classification, and above all that a file another
// renderer owns is never stolen. The SVG case is the one that bites — chroma
// happily lexes it as XML, and drawing an icon's markup as source instead of
// drawing the icon would be a regression, not a feature.
func TestFilePreviewKindOf(t *testing.T) {
	text := []string{"app.log", "server.LOG", "patch.diff", "data.json", "conf.yaml",
		"main.go", "query.sql", "Dockerfile", "Makefile", "notes.md", "README.txt"}
	for _, n := range text {
		if k, _ := filePreviewKindOf(textFile(n, 100)); k != filePreviewText {
			t.Errorf("filePreviewKindOf(%q) = %v, want text", n, k)
		}
	}
	for _, n := range []string{"rows.csv", "rows.tsv", "rows.TAB"} {
		if k, _ := filePreviewKindOf(textFile(n, 100)); k != filePreviewTable {
			t.Errorf("filePreviewKindOf(%q) = %v, want table", n, k)
		}
	}
	// Owned by another renderer, or not text at all.
	for _, n := range []string{"icon.svg", "photo.png", "photo.heic", "model.stl",
		"clip.mp4", "doc.pdf", "logs.zip", "sheet.xlsx", "song.mp3", "lib.so"} {
		if k, _ := filePreviewKindOf(textFile(n, 100)); k != filePreviewNone {
			t.Errorf("filePreviewKindOf(%q) = %v, want none", n, k)
		}
	}
	// An untyped upload with no useful name gets nothing rather than a guess.
	if k, _ := filePreviewKindOf(&model.FileInfo{Id: "f", Name: "upload"}); k != filePreviewNone {
		t.Errorf("filePreviewKindOf(untyped) = %v, want none", k)
	}
	// ... unless its MIME type says text.
	if k, _ := filePreviewKindOf(&model.FileInfo{Id: "f", Name: "upload", MimeType: "text/plain"}); k != filePreviewText {
		t.Error("filePreviewKindOf(text/plain, no extension) = none, want text")
	}
}

// TestFilePreviewSizeCap: the file API has no range request, so previewing means
// downloading. A big log must keep its plain chip rather than pull 40MB unasked.
func TestFilePreviewSizeCap(t *testing.T) {
	m := previewModel()
	if !m.drawsFilePreview(textFile("app.log", filePreviewMaxBytes)) {
		t.Error("a file at the cap draws no preview, want one")
	}
	if m.drawsFilePreview(textFile("app.log", filePreviewMaxBytes+1)) {
		t.Error("a file over the cap draws a preview, want none")
	}
}

// TestFilePreviewOffDrawsNothing: file_previews: off leaves every such file exactly
// as it was, and never even sights it (so nothing is ever downloaded).
func TestFilePreviewOffDrawsNothing(t *testing.T) {
	m := &Model{filePrev: newFilePreviews("off")}
	f := textFile("app.log", 100)
	p := &model.Post{Id: "p1", Metadata: &model.PostMetadata{Files: []*model.FileInfo{f}}}
	if lines := m.previewLines(p, f, 80); lines != nil {
		t.Errorf("previews are off but got %d lines", len(lines))
	}
	if ids := m.filePrev.pendingIDs(); len(ids) != 0 {
		t.Errorf("previews are off but %d files were queued for fetch", len(ids))
	}
}

// ready installs a parsed preview the way the fetch Cmd's message would, so the
// render half can be tested without a server.
func ready(t *testing.T, m *Model, f *model.FileInfo, body string) {
	t.Helper()
	p := &model.Post{Id: "p1", Metadata: &model.PostMetadata{Files: []*model.FileInfo{f}}}
	if m.previewLines(p, f, 80) == nil {
		t.Fatalf("%q was not sighted", f.Name)
	}
	kind, _ := filePreviewKindOf(f)
	if kind == filePreviewTable {
		rows, more := parseFileTablePreview([]byte(body), filePreviewDelimiter(f))
		m.filePrev.markReady(f.Id, nil, rows, more)
		return
	}
	lines, more := parseFileTextPreview([]byte(body))
	m.filePrev.markReady(f.Id, lines, nil, more)
}

// TestTextPreviewNeverExceedsReservation is the height contract: the block claims
// filePreviewReservedRows before its bytes arrive and must never come back taller,
// or a post would grow under a scroll anchored on an absolute row offset.
func TestTextPreviewNeverExceedsReservation(t *testing.T) {
	budget := filePreviewReservedRows(filePreviewText)
	for _, n := range []int{1, 3, filePreviewLines, filePreviewLines + 1, 500} {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString("line " + strconv.Itoa(i) + "\n")
		}
		m := previewModel()
		f := textFile("app.log", 1000)
		ready(t, m, f, b.String())
		got := mustRender(t, m, m.filePrev.entries[f.Id], 80)
		if len(got) > budget {
			t.Errorf("%d-line file rendered %d rows, over the %d-row reservation", n, len(got), budget)
		}
		if n <= filePreviewLines && len(got) != n+2 {
			t.Errorf("%d-line file rendered %d rows, want %d (the lines plus the box's two rules)", n, len(got), n+2)
		}
		if n > filePreviewLines && len(got) != budget {
			t.Errorf("%d-line file rendered %d rows, want the full %d (head + trailer)", n, len(got), budget)
		}
	}
}

// TestTextPreviewTrailerCountsTheRest: the trailer is the only thing telling you
// the file is longer than the block, so its count has to be right.
func TestTextPreviewTrailerCountsTheRest(t *testing.T) {
	var b strings.Builder
	for i := 0; i < filePreviewLines+7; i++ {
		b.WriteString("x\n")
	}
	m := previewModel()
	f := textFile("app.log", 1000)
	ready(t, m, f, b.String())
	got := mustRender(t, m, m.filePrev.entries[f.Id], 80)
	last := got[len(got)-1]
	if !strings.Contains(last, "7 more lines") {
		t.Errorf("trailer = %q, want it to mention 7 more lines", last)
	}
}

// TestTextPreviewStripsEscapes: an uploaded log can carry raw escape sequences.
// They must not reach the transcript, or a file could repaint the UI.
func TestTextPreviewStripsEscapes(t *testing.T) {
	m := previewModel()
	f := textFile("app.log", 100)
	ready(t, m, f, "before\x1b[2Jafter\nplain\n")
	got := strings.Join(mustRender(t, m, m.filePrev.entries[f.Id], 80), "\n")
	if strings.Contains(got, "\x1b[2J") {
		t.Errorf("the file's own escape sequence survived into %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("stripping the escape ate the text too: %q", got)
	}
}

// TestTextPreviewTruncatesToPane: every line has to fit the pane, or the viewport
// miscounts rows and the block silently grows past its reservation.
func TestTextPreviewTruncatesToPane(t *testing.T) {
	m := previewModel()
	f := textFile("app.log", 100)
	ready(t, m, f, strings.Repeat("wide ", 60)+"\n")
	for _, ln := range mustRender(t, m, m.filePrev.entries[f.Id], 40) {
		if w := lipgloss.Width(ln); w > 40 {
			t.Errorf("line is %d cells wide in a 40-cell pane: %q", w, ln)
		}
	}
}

// TestTablePreviewDrawsABox: a CSV gets the same box table a Markdown pipe table
// does, header included.
func TestTablePreviewDrawsABox(t *testing.T) {
	m := previewModel()
	f := textFile("rows.csv", 200)
	ready(t, m, f, "name,count\nalpha,1\nbeta,2\n")
	got := mustRender(t, m, m.filePrev.entries[f.Id], 60)
	joined := strings.Join(got, "\n")
	for _, want := range []string{fpTopLeft, fpBottomLeft, "name", "count", "alpha", "beta"} {
		if !strings.Contains(joined, want) {
			t.Errorf("table preview is missing %q:\n%s", want, joined)
		}
	}
	// A preview box has rounded corners; the square ones belong to a Markdown
	// pipe table, which is content the message itself carries.
	if strings.Contains(joined, "┌") || strings.Contains(joined, "┘") {
		t.Errorf("table preview kept a square corner:\n%s", joined)
	}
	if len(got) > filePreviewReservedRows(filePreviewTable) {
		t.Errorf("table rendered %d rows, over its %d-row reservation", len(got), filePreviewReservedRows(filePreviewTable))
	}
}

// TestTablePreviewNeverExceedsReservationWhenNarrow: renderTableBox wraps cells
// when columns have to shrink, so a narrow pane is exactly where the height
// contract could break. It must drop rows (or the whole block) instead.
func TestTablePreviewNeverExceedsReservationWhenNarrow(t *testing.T) {
	var b strings.Builder
	b.WriteString("first column,second column,third column\n")
	for i := 0; i < 20; i++ {
		b.WriteString("a fairly wordy cell,another wordy cell,and a third one\n")
	}
	budget := filePreviewReservedRows(filePreviewTable)
	for _, width := range []int{20, 30, 45, 60, 100, 200} {
		m := previewModel()
		f := textFile("rows.csv", 2000)
		ready(t, m, f, b.String())
		got := mustRender(t, m, m.filePrev.entries[f.Id], width)
		if len(got) > budget {
			t.Errorf("width %d: table rendered %d rows, over the %d-row reservation", width, len(got), budget)
		}
		for _, ln := range got {
			if w := lipgloss.Width(ln); w > width {
				t.Errorf("width %d: line is %d cells wide: %q", width, w, ln)
			}
		}
	}
}

// TestTSVUsesTabs: a .tsv is not a CSV with odd contents.
func TestTSVUsesTabs(t *testing.T) {
	m := previewModel()
	f := textFile("rows.tsv", 200)
	ready(t, m, f, "name\tcount\nalpha\t1\nbeta\t2\n")
	joined := strings.Join(mustRender(t, m, m.filePrev.entries[f.Id], 60), "\n")
	if !strings.Contains(joined, "alpha") || !strings.Contains(joined, "beta") {
		t.Errorf("tab-separated rows did not parse:\n%s", joined)
	}
}

// TestNonTabularCSVFallsBackToText: a .csv that is really one column of prose is
// not worth a box, and showing nothing would be worse than showing the text.
func TestNonTabularCSVFallsBackToText(t *testing.T) {
	rows, _ := parseFileTablePreview([]byte("just one line, no more\n"), ',')
	if rows != nil {
		t.Errorf("a single-record CSV parsed as a table: %v", rows)
	}
}

// TestReservationHeldUntilBytesArrive: the post is its final height from its first
// render, which is the whole reason the reservation exists.
func TestReservationHeldUntilBytesArrive(t *testing.T) {
	m := previewModel()
	f := textFile("app.log", 100)
	p := &model.Post{Id: "p1", Metadata: &model.PostMetadata{Files: []*model.FileInfo{f}}}
	held := m.previewLines(p, f, 80)
	if len(held) != filePreviewReservedRows(filePreviewText) {
		t.Fatalf("reserved %d rows, want %d", len(held), filePreviewReservedRows(filePreviewText))
	}
	for i, ln := range held {
		if strings.TrimSpace(ln) != "" {
			t.Errorf("reserved row %d is not blank: %q", i, ln)
		}
	}
	// Collapsed with z: no rows at all, and nothing queued for fetch.
	m.thumbsCollapsed = map[string]bool{"p1": true}
	if lines := m.previewLines(p, f, 80); lines != nil {
		t.Errorf("a collapsed post still drew %d preview rows", len(lines))
	}
}

// TestLooksLikeText: the filename got us here, the bytes decide. A NUL is the
// classic binary tell, and the ratio test lets Latin-1 through.
func TestLooksLikeText(t *testing.T) {
	if !looksLikeText([]byte("hello\nworld\n")) {
		t.Error("plain ASCII rejected")
	}
	if !looksLikeText([]byte("caf\xe9 na\xefve\n")) {
		t.Error("a little Latin-1 rejected")
	}
	if looksLikeText([]byte("PK\x03\x04\x00\x00binary")) {
		t.Error("bytes with a NUL accepted as text")
	}
	if looksLikeText(nil) {
		t.Error("empty accepted as text")
	}
	if looksLikeText([]byte(strings.Repeat("\xff\xfe", 200))) {
		t.Error("a wall of invalid runes accepted as text")
	}
}

// TestFilePreviewChevron: a previewable text file gets the same disclosure chevron
// an image thumbnail does, so z reads as collapsing it.
func TestFilePreviewChevron(t *testing.T) {
	m := previewModel()
	m.inlineImg = newInlineImages("off")
	m.emojiImg = newEmojiImages("auto", true)
	m.emojiImg.setProbeOK()
	m.emojiImg.setColorProfile(true)
	p := &model.Post{Id: "p1", Metadata: &model.PostMetadata{Files: []*model.FileInfo{textFile("app.log", 100)}}}
	out := m.renderAttachments(p, 80)
	if !strings.Contains(out, thumbOpenChevron) {
		t.Errorf("a previewable text attachment carries no chevron: %q", out)
	}
}

// TestLexerMemoCachesMissesToo: the whole reason the memo exists is that a *miss*
// is the expensive lookup (24ms for an unknown fence language, measured). Caching
// only the hits would leave the hot path exactly as slow as it was.
func TestLexerMemoCachesMissesToo(t *testing.T) {
	const bogus = "definitely-not-a-language-xyzzy"
	if l := lexerFor(bogus); l != nil {
		t.Fatalf("lexerFor(%q) = %v, want nil", bogus, l)
	}
	lexerCacheMu.Lock()
	_, cached := lexerCache["l:"+bogus]
	lexerCacheMu.Unlock()
	if !cached {
		t.Error("a nil result was not cached, so every render would pay for the miss again")
	}
}

// TestLexerMemoAgreesWithChroma: a cache that returns the wrong lexer would
// silently mis-highlight everything, so pin it against the registry.
func TestLexerMemoAgreesWithChroma(t *testing.T) {
	for _, name := range []string{"main.go", "app.py", "deploy.log", "Dockerfile", "notes.md"} {
		want, got := lexers.Match(name), lexerForFilename(name)
		if want != got {
			t.Errorf("lexerForFilename(%q) = %v, chroma says %v", name, got, want)
		}
		// Second call comes from the cache and must agree with the first.
		if again := lexerForFilename(name); again != got {
			t.Errorf("lexerForFilename(%q) changed on the second call: %v then %v", name, got, again)
		}
	}
	// Case-insensitive: the same file uploaded as .GO must land on the same entry.
	if lexerForFilename("MAIN.GO") != lexerForFilename("main.go") {
		t.Error("lexerForFilename is case-sensitive, so MAIN.GO and main.go disagree")
	}
}

// TestLexerMemoStopsGrowing: the cap is what keeps a pathological session (a
// thousand distinct fence tags) from holding a map that never shrinks.
func TestLexerMemoStopsGrowing(t *testing.T) {
	lexerCacheMu.Lock()
	saved := lexerCache
	lexerCache = make(map[string]chroma.Lexer, lexerCacheCap)
	for i := 0; i < lexerCacheCap; i++ {
		lexerCache["filler"+strconv.Itoa(i)] = nil
	}
	lexerCacheMu.Unlock()
	defer func() {
		lexerCacheMu.Lock()
		lexerCache = saved
		lexerCacheMu.Unlock()
	}()

	// Still answers correctly with the cache full, it just stops inserting.
	if lexerForFilename("main.go") == nil {
		t.Error("lexerForFilename stopped resolving once the cache was full")
	}
	lexerCacheMu.Lock()
	n := len(lexerCache)
	lexerCacheMu.Unlock()
	if n > lexerCacheCap {
		t.Errorf("cache grew to %d entries, past the %d cap", n, lexerCacheCap)
	}
}

// TestTextPreviewCapsLineLength: a minified JSON or a bundled .js is one line of
// megabytes. Keeping all of it would hold the whole file for as long as the post
// is cached and re-scan it on every render.
func TestTextPreviewCapsLineLength(t *testing.T) {
	lines, _ := parseFileTextPreview([]byte(strings.Repeat("x", 100_000) + "\n"))
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if len([]rune(lines[0])) > filePreviewLineMax {
		t.Errorf("kept %d runes of a single line, cap is %d", len([]rune(lines[0])), filePreviewLineMax)
	}
}

// TestCutRunesKeepsValidUTF8: cutting mid-rune would leave a broken string that
// every width measurement downstream disagrees about.
func TestCutRunesKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("né", 400) // two bytes for é, so a naive byte cut splits it
	for _, n := range []int{1, 2, 3, 17, 512} {
		got := cutRunes(s, n)
		if !utf8.ValidString(got) {
			t.Errorf("cutRunes(n=%d) produced invalid UTF-8", n)
		}
		if r := len([]rune(got)); r > n {
			t.Errorf("cutRunes(n=%d) kept %d runes", n, r)
		}
	}
	if got := cutRunes("short", 99); got != "short" {
		t.Errorf("cutRunes on a short string = %q, want it untouched", got)
	}
}

// TestTablePreviewCapsColumns: a wide export would make every row wider than any
// terminal, and the trailer has to admit the columns were dropped rather than
// showing a slice of the file as if it were all of it.
func TestTablePreviewCapsColumns(t *testing.T) {
	cols := filePreviewMaxCols + 5
	head := make([]string, cols)
	data := make([]string, cols)
	for i := range head {
		head[i] = "col" + strconv.Itoa(i)
		data[i] = "v" + strconv.Itoa(i)
	}
	body := strings.Join(head, ",") + "\n" + strings.Join(data, ",") + "\n" + strings.Join(data, ",") + "\n"

	m := previewModel()
	f := textFile("wide.csv", int64(len(body)))
	ready(t, m, f, body)
	got := mustRender(t, m, m.filePrev.entries[f.Id], 200)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "5 more columns") {
		t.Errorf("the trailer does not admit the dropped columns:\n%s", joined)
	}
	if strings.Contains(joined, "col"+strconv.Itoa(filePreviewMaxCols)) {
		t.Errorf("a column past the cap was laid out:\n%s", joined)
	}
	if len(got) > filePreviewReservedRows(filePreviewTable) {
		t.Errorf("wide table rendered %d rows, over its reservation", len(got))
	}
}

// TestDiffHighlightSurvivesTrailingNewline: chroma's Diff lexer appends a trailing
// newline, which used to trip highlightCode's line-count guard and drop every
// ```diff block (and every .diff/.patch attachment) back to flat single-colour
// text. Additions and removals must come back in different colours.
func TestDiffHighlightSurvivesTrailingNewline(t *testing.T) {
	body := []string{"--- a/x.go", "+++ b/x.go", "+added", "-removed"}
	got := highlightWithLexer(body, lexerForFilename("fix.diff"))
	if len(got) != len(body) {
		t.Fatalf("got %d lines for %d inputs", len(got), len(body))
	}
	add, del := got[2], got[3]
	if add == "+added" || del == "-removed" {
		t.Fatalf("no colour applied: %q / %q", add, del)
	}
	if colorPrefix(add) == colorPrefix(del) {
		t.Errorf("additions and removals share a colour: %q vs %q", add, del)
	}
	// Every fenced-code caller goes through highlightCode, so pin that too.
	if lines := highlightCode(body, "diff"); len(lines) != len(body) || lines[2] == "+added" {
		t.Errorf("highlightCode(diff) fell back: %q", lines)
	}
}

// colorPrefix is the leading SGR sequence of a styled line, or "" if unstyled.
func colorPrefix(s string) string {
	if !strings.HasPrefix(s, "\x1b[") {
		return ""
	}
	if i := strings.IndexByte(s, 'm'); i > 0 {
		return s[:i+1]
	}
	return ""
}

// TestPreviewBoxFramesTheContent: the box is what says "this is a window onto a
// file, not the message". Rounded corners, a border on every content row, and the
// whole thing exactly the pane width so the right edge lines up.
func TestPreviewBoxFramesTheContent(t *testing.T) {
	m := previewModel()
	f := textFile("app.log", 200)
	ready(t, m, f, "alpha\nbeta\ngamma\n")
	got := mustRender(t, m, m.filePrev.entries[f.Id], 60)
	if len(got) != 5 {
		t.Fatalf("3 lines rendered %d rows, want 5 (3 + two rules)", len(got))
	}
	if !strings.Contains(got[0], fpTopLeft) || !strings.Contains(got[0], fpTopRight) {
		t.Errorf("top rule is not a rounded box: %q", got[0])
	}
	last := got[len(got)-1]
	if !strings.Contains(last, fpBottomLeft) || !strings.Contains(last, fpBottomRight) {
		t.Errorf("bottom rule is not a rounded box: %q", last)
	}
	for i, ln := range got {
		if w := lipgloss.Width(ln); w != 60 {
			t.Errorf("row %d is %d cells wide, want exactly 60 so the borders line up: %q", i, w, ln)
		}
	}
	for i, ln := range got[1 : len(got)-1] {
		if strings.Count(ln, fpVert) != 2 {
			t.Errorf("content row %d has no left+right border: %q", i, ln)
		}
	}
}

// TestPreviewCaptionLivesInTheBottomBorder: the "… N more lines" note is set into
// the closing rule rather than taking a row of its own, so it reads as part of the
// frame and costs nothing.
func TestPreviewCaptionLivesInTheBottomBorder(t *testing.T) {
	var b strings.Builder
	for i := 0; i < filePreviewLines+3; i++ {
		b.WriteString("x\n")
	}
	m := previewModel()
	f := textFile("app.log", 500)
	ready(t, m, f, b.String())
	got := mustRender(t, m, m.filePrev.entries[f.Id], 60)

	last := got[len(got)-1]
	if !strings.Contains(last, "3 more lines") {
		t.Errorf("the caption is not in the bottom rule: %q", last)
	}
	if !strings.Contains(last, fpBottomLeft) || !strings.Contains(last, fpBottomRight) {
		t.Errorf("the caption replaced the rule instead of sitting in it: %q", last)
	}
	if lipgloss.Width(last) != 60 {
		t.Errorf("the caption rule is %d cells wide, want 60", lipgloss.Width(last))
	}
	// And it is the *only* place the count appears — no duplicate trailer row.
	if n := strings.Count(strings.Join(got, "\n"), "3 more lines"); n != 1 {
		t.Errorf("the caption appears %d times, want 1", n)
	}
}

// TestTableCaptionRuleMatchesTheTableWidth: a table narrower than the pane must
// not get a caption rule running past its own right edge.
func TestTableCaptionRuleMatchesTheTableWidth(t *testing.T) {
	var b strings.Builder
	b.WriteString("region,orders\n")
	for i := 0; i < filePreviewTableRows+4; i++ {
		b.WriteString("Benelux,1420\n")
	}
	m := previewModel()
	f := textFile("rows.csv", 200)
	ready(t, m, f, b.String())
	got := mustRender(t, m, m.filePrev.entries[f.Id], 100)
	top, bottom := got[0], got[len(got)-1]
	if !strings.Contains(bottom, "more row") {
		t.Fatalf("expected a caption in the bottom rule, got %q", bottom)
	}
	if lipgloss.Width(top) != lipgloss.Width(bottom) {
		t.Errorf("top rule is %d cells, caption rule is %d — the box is lopsided:\n%s\n%s",
			lipgloss.Width(top), lipgloss.Width(bottom), top, bottom)
	}
	if lipgloss.Width(bottom) >= 100 {
		t.Errorf("the caption rule stretched to the pane width (%d) instead of the table's: %q",
			lipgloss.Width(bottom), bottom)
	}
}

// TestNarrowPaneDropsThePreview: below the width where a box says anything useful,
// nothing is drawn — which the height contract allows, since shorter is fine.
func TestNarrowPaneDropsThePreview(t *testing.T) {
	m := previewModel()
	f := textFile("app.log", 100)
	ready(t, m, f, "alpha\nbeta\n")
	if got := mustRender(t, m, m.filePrev.entries[f.Id], 10); got != nil {
		t.Errorf("a 10-cell pane drew %d rows: %q", len(got), got)
	}
}

// TestNarrowBoxKeepsAShortCaption: a two-column CSV can be nine cells wide, where
// "… 4 more rows" has nowhere to go. It must degrade to the bare count rather than
// drop the note — showing a slice of a file as if it were all of it is the one
// outcome worth avoiding.
func TestNarrowBoxKeepsAShortCaption(t *testing.T) {
	var b strings.Builder
	b.WriteString("a,b\n")
	for i := 0; i < filePreviewTableRows+4; i++ {
		b.WriteString("1,2\n")
	}
	m := previewModel()
	f := textFile("rows.csv", 200)
	ready(t, m, f, b.String())
	got := mustRender(t, m, m.filePrev.entries[f.Id], 100)
	last := got[len(got)-1]
	if !strings.Contains(last, "+4") {
		t.Errorf("a narrow table lost its caption entirely: %q", last)
	}
	if !strings.Contains(last, fpBottomLeft) || !strings.Contains(last, fpBottomRight) {
		t.Errorf("the short caption is not inside the border: %q", last)
	}
	if lipgloss.Width(last) != lipgloss.Width(got[0]) {
		t.Errorf("the short caption changed the box width: %q vs %q", got[0], last)
	}
}

// TestBottomRulePicksTheLongestThatFits pins the degradation order directly.
func TestBottomRulePicksTheLongestThatFits(t *testing.T) {
	long, short := "… 4 more rows", "+4"
	if got := filePreviewBottomRule(40, long, short); !strings.Contains(got, long) {
		t.Errorf("a wide rule did not take the full wording: %q", got)
	}
	if got := filePreviewBottomRule(10, long, short); !strings.Contains(got, short) {
		t.Errorf("a narrow rule did not fall back to the short wording: %q", got)
	}
	// Nothing fits: a bare rule, never a widened box.
	got := filePreviewBottomRule(6, long, short)
	if strings.ContainsAny(got, "4") {
		t.Errorf("a rule with no room still drew a caption: %q", got)
	}
	if lipgloss.Width(got) != 6 {
		t.Errorf("bare rule is %d cells, want 6", lipgloss.Width(got))
	}
	// Every width must produce exactly that width, caption or not.
	for w := 7; w <= 60; w++ {
		if n := lipgloss.Width(filePreviewBottomRule(w, long, short)); n != w {
			t.Errorf("width %d produced a %d-cell rule", w, n)
		}
	}
}

// previewCollapseModel builds a Model with one selected post carrying f, image
// thumbnails off, so z's only reason to treat the post as media is the preview.
func previewCollapseModel(f *model.FileInfo) *Model {
	p := &model.Post{Id: "p1", Message: "here", Metadata: &model.PostMetadata{Files: []*model.FileInfo{f}}}
	return &Model{
		filePrev:     newFilePreviews("auto"),
		inlineImg:    newInlineImages("off"),
		posts:        []*model.Post{p},
		postIdx:      0,
		collapseRows: 0, // body collapsing off: the preview is the only foldable thing
	}
}

// TestZCollapsesAPreview: z has to fold a boxed log away. Before this, z asked
// only about image thumbnails, so a post whose only block was a preview either
// toggled its body or reported that collapsing was disabled — and the preview
// could never be hidden at all.
func TestZCollapsesAPreview(t *testing.T) {
	f := textFile("app.log", 400)
	m := previewCollapseModel(f)
	p := m.posts[0]

	if !m.postHasFilePreview(p) {
		t.Fatal("postHasFilePreview = false, test premise is wrong")
	}
	if lines := m.previewLines(p, f, 80); len(lines) == 0 {
		t.Fatal("no preview rows before collapsing")
	}

	next, _ := m.toggleCollapse(focusMessages)
	nm, ok := next.(Model)
	if !ok {
		t.Fatal("toggleCollapse did not return a Model")
	}
	if nm.status != "" {
		t.Errorf("z reported %q instead of collapsing the preview", nm.status)
	}
	if !nm.thumbsCollapsed["p1"] {
		t.Fatal("z did not collapse the post")
	}
	if lines := nm.previewLines(p, f, 80); lines != nil {
		t.Errorf("the preview still drew %d rows after z", len(lines))
	}

	// And z again brings it back.
	next2, _ := nm.toggleCollapse(focusMessages)
	nm2 := next2.(Model)
	if nm2.thumbsCollapsed["p1"] {
		t.Error("the second z did not expand the post again")
	}
	if lines := nm2.previewLines(p, f, 80); len(lines) == 0 {
		t.Error("the preview did not come back after the second z")
	}
}

// TestZStillReportsDisabledWithoutMedia: the guard has to keep working for a post
// that has nothing to fold — widening it to previews must not make z silently
// succeed on a plain message with body collapsing off.
func TestZStillReportsDisabledWithoutMedia(t *testing.T) {
	m := previewCollapseModel(&model.FileInfo{Id: "f1", Name: "notes.pdf", Size: 400})
	next, _ := m.toggleCollapse(focusMessages)
	nm := next.(Model)
	if nm.status != "message collapsing is disabled" {
		t.Errorf("status = %q, want the disabled notice", nm.status)
	}
	if nm.thumbsCollapsed["p1"] {
		t.Error("a post with nothing to fold was marked collapsed")
	}
}

// TestCollapsedPreviewIsNeverFetched: collapsing is not merely visual — a
// collapsed post owns no preview keys, so its file is never downloaded.
func TestCollapsedPreviewIsNeverFetched(t *testing.T) {
	f := textFile("app.log", 400)
	m := previewCollapseModel(f)
	m.thumbsCollapsed = map[string]bool{"p1": true}
	if lines := m.previewLines(m.posts[0], f, 80); lines != nil {
		t.Fatalf("a collapsed post drew %d rows", len(lines))
	}
	if ids := m.filePrev.pendingIDs(); len(ids) != 0 {
		t.Errorf("a collapsed post queued %d files for download", len(ids))
	}
}
