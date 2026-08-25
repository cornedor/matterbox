package ui

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattermost/mattermost/server/public/model"
)

// Inline previews of the attachments that are *text*: the head of a log, diff,
// JSON or source file, syntax-highlighted, and a CSV/TSV drawn as a box table.
//
// The point is the same as the image thumbnails next door — see what was posted
// without leaving the transcript — for the files a chat actually carries that no
// image decoder can help with. "Here's the log" is a more common post than
// "here's a 3D model", and until now it rendered as a paperclip and a filename.
//
// Two things make this cheaper than it looks:
//
//   - No graphics. These are plain text lines, so they need no Kitty support, no
//     image ids, no terminal memory and no eviction — they simply become more
//     lines in the post (exactly like inlineThumbLines' placeholder rows do, and
//     for the same reason the rest of the pipeline is none the wiser).
//   - No new dependency. chroma is already linked (glamour pulls it in) and
//     already highlights fenced code blocks; renderTableBox already lays out
//     Markdown pipe tables to fit a pane. This file is mostly wiring.
//
// What it shares with the thumbnails is the *discipline*: fetch lazily and only
// near the viewport, and never let a post change height when the bytes land. See
// filePreviewReservedRows for how the second half is guaranteed.

const (
	// filePreviewMaxBytes caps the size of a file we will fetch to preview
	// unasked. The Mattermost file API has no range request, so previewing means
	// downloading the whole thing — and nobody wants a 200MB log pulled down
	// because it scrolled past. Above the cap the file keeps its plain chip and
	// `o`/`s` still open or save it.
	filePreviewMaxBytes = 2 << 20 // 2 MiB
	// filePreviewLines is how many leading lines of a text file are shown.
	filePreviewLines = 10
	// filePreviewTableRows is how many data rows of a CSV/TSV are shown (the
	// header row is always shown and is not counted here).
	filePreviewTableRows = 8
	// filePreviewCellMax caps a table cell's text. Without it one prose-filled
	// column would wrap every row across several lines, and the block would blow
	// its row budget on three rows of one long-winded CSV.
	filePreviewCellMax = 28
	// filePreviewLineMax caps how much of one line is kept. A minified JSON or a
	// bundled .js is a single line of megabytes: without this the entry would hold
	// all of it for as long as the post is cached, and every render would re-scan
	// it only to draw the first 78 cells. Generously above any pane width, so the
	// truncation the user sees is still the width one.
	filePreviewLineMax = 512
	// filePreviewMaxCols caps how many columns of a tabular file are laid out. A
	// wide export can carry hundreds, which would make every row wider than any
	// terminal and force renderTableBox into its unreadable narrow fallback. The
	// trailer says when columns were dropped.
	filePreviewMaxCols = 12
	// filePreviewTabWidth is what a tab expands to. Fixed rather than elastic:
	// the alternative is measuring tab stops across a block we are about to
	// truncate to the pane width anyway.
	filePreviewTabWidth = 4
	// filePreviewScanBytes is how much of the file is sniffed to decide it is
	// really text. A NUL in the first few KB is the classic binary tell, and it
	// is what stops a mislabelled upload from spraying control codes.
	filePreviewScanBytes = 8 << 10
)

// filePreviewKind is what shape a previewable attachment gets drawn in.
type filePreviewKind uint8

const (
	filePreviewNone  filePreviewKind = iota
	filePreviewText                  // highlighted head-of-file
	filePreviewTable                 // CSV/TSV as a box table
)

// filePreviewReservedRows is how many rows a preview block claims from the moment
// the post first renders, before its bytes have been fetched — and, crucially, the
// most it can ever draw. The block is allowed to come back *shorter* than this
// (a three-line file, a table that had to drop rows to fit a narrow pane) but
// never taller.
//
// That asymmetry is the whole height contract, and it is the same one the image
// thumbnails keep: a wheel scroll anchors on an absolute row offset
// (m.msgFreeOffset), so a post growing when its bytes land would jump the page
// under the cursor. A post that shrinks by a row or two is the compromise a body
// image URL already makes (see reserveThumbCells' nominalBodyImage) — the
// common case for a log, diff or CSV attachment is longer than the cap, so the
// reservation is usually exact.
func filePreviewReservedRows(kind filePreviewKind) int {
	switch kind {
	case filePreviewText:
		return filePreviewLines + 2 // + the box's top and bottom rules
	case filePreviewTable:
		// Four lines of box: top rule, header, header rule, bottom rule.
		return filePreviewTableRows + 4
	}
	return 0
}

// --- what counts as previewable text -------------------------------------

// filePreviewTableExt is the extension set drawn as a table rather than as text.
func filePreviewTableExt(ext string) bool {
	switch strings.ToLower(ext) {
	case "csv", "tsv", "tab":
		return true
	}
	return false
}

// filePreviewPlainExt is the extension set that is certainly text but that chroma
// has no lexer for, so lexers.Match would turn it down. Logs are the big one, and
// the whole feature would miss its best case without them.
func filePreviewPlainExt(ext string) bool {
	switch strings.ToLower(ext) {
	case "log", "conf", "cfg", "text", "out", "err", "trace", "properties", "lock", "gitignore", "editorconfig":
		return true
	}
	return false
}

// filePreviewTextMIME reports whether a MIME type alone says "this is text". The
// application/* entries are the handful of textual formats conventionally filed
// outside text/*.
func filePreviewTextMIME(mime string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml", "application/x-yaml", "application/yaml",
		"application/toml", "application/x-sh", "application/javascript", "application/sql":
		return true
	}
	return false
}

// filePreviewKindOf classifies an attachment, returning the shape to draw it in
// and the chroma lexer to highlight it with (nil for none — a table, or text in a
// format chroma doesn't know).
//
// Formats another renderer owns are refused first: an SVG is a drawing, not the
// XML document chroma would happily colour for us, and the same goes for anything
// the image, video and STL paths claim. Order matters here — this is the one place
// that could steal a file from a better renderer.
//
// chroma's own lexer registry does the heavy lifting for the text case. It knows
// several hundred filename patterns (including extensionless ones like Dockerfile
// and Makefile) and — the part that makes it safe to lean on — matches none of the
// binary formats: png, pdf, zip, xlsx, mp3 and friends all come back nil.
func filePreviewKindOf(f *model.FileInfo) (filePreviewKind, chroma.Lexer) {
	if f == nil {
		return filePreviewNone, nil
	}
	// Claimed by a renderer that turns it into pixels. isVideoAttachment is asked
	// unconditionally (not behind videoPlayable) on purpose: a build that cannot
	// play an mp4 should still not draw its bytes as text.
	if isStillImageAttachment(f) || isSVGAttachment(f) || isSTLAttachment(f) || isVideoAttachment(f) {
		return filePreviewNone, nil
	}
	ext := attachmentExt(f)
	mime := attachmentMIME(f)
	if filePreviewTableExt(ext) || mime == "text/csv" || mime == "text/tab-separated-values" {
		return filePreviewTable, nil
	}
	if lex := lexerForFilename(normalizeFilename(f.Name)); lex != nil {
		return filePreviewText, lex
	}
	if filePreviewPlainExt(ext) || filePreviewTextMIME(mime) {
		return filePreviewText, nil
	}
	return filePreviewNone, nil
}

// looksLikeText reports whether bytes are plausibly text: no NUL in the scanned
// prefix, and decodable as UTF-8 without a pile of invalid runes. The same
// belt-and-braces sniff svgimg.Looks and stl.Looks do — the filename got us here,
// the bytes decide whether we actually draw.
func looksLikeText(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	head := b
	if len(head) > filePreviewScanBytes {
		head = head[:filePreviewScanBytes]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	// Latin-1 and friends are still worth showing, so this is a ratio test rather
	// than a hard utf8.Valid: reject only when invalid bytes are common enough
	// that the file is probably not text at all. The threshold is deliberately
	// loose (under ~30%) because a short accented line legitimately has a high
	// proportion of them — "café naïve" is 2 bad bytes in 12.
	bad := 0
	for i := 0; i < len(head); {
		r, size := utf8.DecodeRune(head[i:])
		if r == utf8.RuneError && size == 1 {
			bad++
		}
		i += size
	}
	return bad*10 <= len(head)*3
}

// --- the lazy store ------------------------------------------------------

// filePreviewEntry is one attachment's preview: its shape, its parsed content once
// fetched, and a one-slot cache of the lines that content rendered to at a given
// pane width (the render is re-run on resize, which is already debounced).
type filePreviewEntry struct {
	file  *model.FileInfo
	kind  filePreviewKind
	lexer chroma.Lexer

	fetched bool
	failed  bool
	// text: the leading lines, tabs already expanded. table: the header row
	// followed by the data rows.
	lines []string
	rows  [][]string
	// more is how many lines/rows were left out, for the trailer.
	more int

	cacheWidth int
	cacheLines []string
}

// filePreviews holds every sighted attachment preview. Guarded by a mutex because
// the fetch/parse runs on a background Cmd goroutine while View reads.
type filePreviews struct {
	mu      sync.Mutex
	on      bool
	entries map[string]*filePreviewEntry
	pending map[string]struct{}
}

// newFilePreviews builds the store. mode is the file_previews config value:
// anything but "auto" leaves it inert, and every method below short-circuits.
func newFilePreviews(mode string) *filePreviews {
	return &filePreviews{on: mode == "auto", entries: map[string]*filePreviewEntry{}, pending: map[string]struct{}{}}
}

func (fp *filePreviews) enabled() bool { return fp != nil && fp.on }

// sight registers an attachment the renderer has just drawn and returns its entry,
// queueing a fetch the first time. Mirrors inlineImages.sight: the render pass is
// what discovers work, and fetchPendingFilePreviews later decides which of it is
// close enough to the viewport to be worth doing.
func (fp *filePreviews) sight(f *model.FileInfo, kind filePreviewKind, lex chroma.Lexer) *filePreviewEntry {
	if !fp.enabled() || f == nil || f.Id == "" {
		return nil
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	e := fp.entries[f.Id]
	if e == nil {
		e = &filePreviewEntry{file: f, kind: kind, lexer: lex}
		fp.entries[f.Id] = e
		fp.pending[f.Id] = struct{}{}
	}
	return e
}

// pendingIDs lists the attachments sighted but not yet fetched.
func (fp *filePreviews) pendingIDs() []string {
	if !fp.enabled() {
		return nil
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.pending) == 0 {
		return nil
	}
	out := make([]string, 0, len(fp.pending))
	for id := range fp.pending {
		out = append(out, id)
	}
	return out
}

// takePending removes and returns the pending files whose ids are in want, so the
// caller can fetch them. Anything left pending is picked up by a later scan.
func (fp *filePreviews) takePending(want map[string]struct{}) []*model.FileInfo {
	if !fp.enabled() || len(want) == 0 {
		return nil
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	var out []*model.FileInfo
	for id := range want {
		if _, ok := fp.pending[id]; !ok {
			continue
		}
		e := fp.entries[id]
		if e == nil {
			delete(fp.pending, id)
			continue
		}
		delete(fp.pending, id)
		out = append(out, e.file)
	}
	return out
}

// markReady installs a parsed preview.
func (fp *filePreviews) markReady(id string, lines []string, rows [][]string, more int) {
	if !fp.enabled() {
		return
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	e := fp.entries[id]
	if e == nil {
		return
	}
	e.fetched, e.lines, e.rows, e.more = true, lines, rows, more
	e.cacheWidth, e.cacheLines = 0, nil
}

// setKind corrects an entry's shape. Used when a .csv turns out not to be tabular
// at all and is shown as text instead — the decision needs the bytes, which only
// the fetch has.
func (fp *filePreviews) setKind(id string, kind filePreviewKind) {
	if !fp.enabled() {
		return
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if e := fp.entries[id]; e != nil {
		e.kind = kind
	}
}

// markFailed records that an attachment can't be previewed, so it is never asked
// for again — an undecodable file would otherwise be re-fetched forever.
func (fp *filePreviews) markFailed(ids ...string) {
	if !fp.enabled() {
		return
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	for _, id := range ids {
		if e := fp.entries[id]; e != nil {
			e.fetched, e.failed = true, true
			e.cacheWidth, e.cacheLines = 0, nil
		}
	}
}

// markRetry forgets a transient failure (a network blip) so a later sighting
// tries again. Mirrors inlineImages' retry handling.
func (fp *filePreviews) markRetry(ids ...string) {
	if !fp.enabled() {
		return
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	for _, id := range ids {
		delete(fp.entries, id)
		delete(fp.pending, id)
	}
}

// render returns the entry's lines at the given width, using its one-slot cache,
// and whether the entry is settled — fetched or given up on. Both answers come
// from inside the lock: the caller needs "is there anything yet?" to decide
// between drawing the block and holding the reservation, and the fetch goroutine
// writes exactly those fields, so reading them outside would be a data race that
// only shows up when the render legitimately produces no lines (a pane too narrow,
// an empty file).
//
// Called from the render path, so the chroma pass runs once per (file, width)
// rather than once per keystroke — and the post line cache above it absorbs the
// rest.
func (fp *filePreviews) render(e *filePreviewEntry, width int) (lines []string, settled bool) {
	if e == nil {
		return nil, true
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if e.failed {
		return nil, true
	}
	if !e.fetched {
		return nil, false // caller draws the reservation instead
	}
	if e.cacheWidth == width && e.cacheLines != nil {
		return e.cacheLines, true
	}
	out := renderFilePreview(e, width)
	e.cacheWidth, e.cacheLines = width, out
	return out, true
}

// --- parsing (runs on the fetch goroutine) --------------------------------

// filePreviewDelimiter is the field separator for a tabular attachment: tab for
// .tsv/.tab, comma otherwise. Read off the filename because a CSV's MIME type is
// text/csv either way.
func filePreviewDelimiter(f *model.FileInfo) rune {
	switch strings.ToLower(attachmentExt(f)) {
	case "tsv", "tab":
		return '\t'
	}
	if attachmentMIME(f) == "text/tab-separated-values" {
		return '\t'
	}
	return ','
}

// parseFileTextPreview splits raw into the leading display lines, returning how
// many lines were left over. Tabs are expanded here rather than at render time so
// the width truncation downstream measures what it will actually draw.
func parseFileTextPreview(raw []byte) (lines []string, more int) {
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	all := strings.Split(s, "\n")
	// A trailing newline yields a final empty element that is not a line.
	if n := len(all); n > 0 && all[n-1] == "" {
		all = all[:n-1]
	}
	keep := all
	if len(keep) > filePreviewLines {
		keep, more = keep[:filePreviewLines], len(all)-filePreviewLines
	}
	lines = make([]string, len(keep))
	for i, ln := range keep {
		lines[i] = sanitizeFileLine(cutRunes(ln, filePreviewLineMax))
	}
	return lines, more
}

// cutRunes truncates s to at most n runes, cutting on a rune boundary so the
// result is still valid UTF-8. No ellipsis: the width truncation downstream adds
// one, and this cap is far above any pane width, so a user only ever sees that.
func cutRunes(s string, n int) string {
	if len(s) <= n { // len is a cheap upper bound on the rune count
		return s
	}
	i, count := 0, 0
	for i < len(s) {
		if count == n {
			return s[:i]
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		count++
	}
	return s
}

// sanitizeFileLine makes one line of an uploaded file safe and measurable to
// draw: tabs expanded (see expandTabs — measured width has to equal painted
// width or the viewport miscounts rows), and every other control character
// dropped. That second half matters more than it looks: a log or a diff can
// carry raw escape sequences, and passing those through to the transcript would
// let an uploaded file repaint the UI.
func sanitizeFileLine(s string) string {
	if !hasControl(s) {
		return s
	}
	return expandTabs(stripControl(s), filePreviewTabWidth)
}

// hasControl reports whether s carries a C0 control character or DEL. Tab counts:
// it is legal but still has to be expanded.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// stripControl drops every C0 control character except tab, plus DEL.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// parseFileTablePreview reads raw as delimiter-separated records, returning the
// header row plus up to filePreviewTableRows data rows, and how many rows were
// left over. Ragged and sloppily quoted files are read as-is (FieldsPerRecord -1,
// LazyQuotes) rather than rejected: this is a preview, and a strict parser would
// refuse exactly the hand-made CSVs people paste into chat.
func parseFileTablePreview(raw []byte, comma rune) (rows [][]string, more int) {
	r := csv.NewReader(bytes.NewReader(raw))
	r.Comma = comma
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = false
	total := 0
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		total++
		if len(rows) <= filePreviewTableRows {
			for i, c := range rec {
				rec[i] = sanitizeFileLine(strings.TrimSpace(c))
			}
			rows = append(rows, rec)
		}
	}
	if total > len(rows) {
		more = total - len(rows)
	}
	if len(rows) < 2 {
		// A header with no data (or nothing at all) is not worth a box; let the
		// text path have it instead.
		return nil, 0
	}
	return rows, more
}

// --- rendering -----------------------------------------------------------

var filePreviewBorderStyle = lipgloss.NewStyle().Foreground(dimColor)

// Preview boxes are drawn with rounded corners, which is what tells them apart
// from the square-cornered box a Markdown pipe table renders as: one is a window
// onto a file, the other is content the message itself contains.
const (
	fpTopLeft     = "╭"
	fpTopRight    = "╮"
	fpBottomLeft  = "╰"
	fpBottomRight = "╯"
	fpHoriz       = "─"
	fpVert        = "│"
)

// filePreviewMinInner is the narrowest content column worth boxing. Below it the
// preview is dropped entirely rather than drawn as a box with nothing legible in
// it — which the height contract allows, since shorter is always fine.
const filePreviewMinInner = 8

// boxFilePreview wraps already-styled content lines in a rounded box, gutter
// indented, exactly width cells wide. captions (may be empty) are candidate
// wordings for a note drawn *into* the bottom rule — see filePreviewBottomRule —
// so saying what was left out costs no extra row and reads as part of the frame
// rather than as more content.
//
// Content is padded to the inner width rather than left ragged: the right-hand
// border has to land in the same column on every row, and the lines arrive with
// ANSI colour in them, so the padding is measured by display width and not by
// len().
func boxFilePreview(content []string, captions []string, width int) []string {
	inner := width - 4 // "│ " and " │"
	if inner < filePreviewMinInner {
		return nil
	}
	out := make([]string, 0, len(content)+2)
	out = append(out, "  "+filePreviewBorderStyle.Render(fpTopLeft+strings.Repeat(fpHoriz, width-2)+fpTopRight))
	for _, ln := range content {
		pad := inner - lipgloss.Width(ln)
		if pad < 0 {
			pad = 0
		}
		out = append(out, "  "+filePreviewBorderStyle.Render(fpVert)+" "+ln+strings.Repeat(" ", pad)+" "+filePreviewBorderStyle.Render(fpVert))
	}
	out = append(out, "  "+filePreviewBorderStyle.Render(filePreviewBottomRule(width, captions...)))
	return out
}

// filePreviewBottomRule is the box's closing line with a caption set into it:
// "╰─ … 12 more lines ─────╯".
//
// captions are candidate wordings from longest to shortest, and the first one
// that fits the rule is used. That is what keeps the caption inside the border on
// a box too narrow for the full phrasing — a two-column CSV can be nine cells
// wide, where "… 4 more rows" has nowhere to go but "+4". Dropping the note
// instead would show a slice of a file as if it were all of it, and moving it to
// a line below the box would cost a row and stop reading as part of the frame.
func filePreviewBottomRule(width int, captions ...string) string {
	bare := fpBottomLeft + strings.Repeat(fpHoriz, width-2) + fpBottomRight
	// "╰─ " + caption + " " + at least one ─ + "╯" is 5 cells of frame.
	room := width - 6
	for _, c := range captions {
		if c == "" || lipgloss.Width(c) > room {
			continue
		}
		fill := width - 5 - lipgloss.Width(c)
		if fill < 1 {
			continue
		}
		return fpBottomLeft + fpHoriz + " " + c + " " + strings.Repeat(fpHoriz, fill) + fpBottomRight
	}
	return bare
}

// renderFilePreview draws a fetched preview to fit width, gutter-indented like
// every other body line. Never taller than filePreviewReservedRows for its kind —
// see there for why that is a contract and not a nicety.
func renderFilePreview(e *filePreviewEntry, width int) []string {
	box := width - 2 // the two-cell body gutter
	if box < 12 {
		return nil // too narrow to say anything useful
	}
	switch e.kind {
	case filePreviewText:
		return renderTextPreview(e, box)
	case filePreviewTable:
		return renderTablePreview(e, width)
	}
	return nil
}

// renderTextPreview highlights the head of a text file. Truncation happens before
// highlighting, on the plain text, so a cut can never land inside an escape
// sequence — and so chroma colours exactly the characters we draw.
func renderTextPreview(e *filePreviewEntry, box int) []string {
	if len(e.lines) == 0 {
		return nil
	}
	inner := box - 4 // the box's borders and their one-space padding
	if inner < filePreviewMinInner {
		return nil
	}
	plain := make([]string, len(e.lines))
	for i, ln := range e.lines {
		plain[i] = ansi.Truncate(ln, inner, "…")
	}
	var captions []string
	if e.more > 0 {
		captions = []string{filePreviewMoreLabel(e.more, "line"), filePreviewShortLabel(e.more)}
	}
	return boxFilePreview(highlightWithLexer(plain, e.lexer), captions, box)
}

// renderTablePreview draws a tabular file as the same box table a Markdown pipe
// table gets, reusing renderTableBox so the two look identical and shrink to a
// narrow pane identically.
//
// The loop is the height contract: renderTableBox wraps a cell across extra lines
// when a column has to shrink, so a narrow pane can make the box taller than its
// reservation. Rather than guess, drop a data row and ask again — bounded by the
// row count, and in the common (wide enough) case it never iterates at all. If
// even a two-row table won't fit, the preview is dropped entirely, which the
// contract allows: shorter is fine, taller is not.
func renderTablePreview(e *filePreviewEntry, width int) []string {
	if len(e.rows) < 2 {
		return nil
	}
	budget := filePreviewReservedRows(filePreviewTable)
	for n := len(e.rows); n >= 2; n-- {
		rows := e.rows[:n]
		t := &mdTable{aligns: filePreviewAligns(rows), rows: filePreviewCells(rows)}
		lines := renderTableBox(t, width)
		if len(lines) == 0 {
			return nil
		}
		lines = roundTablePreviewBox(lines, filePreviewTableTrailer(e, n))

		if len(lines) <= budget {
			return lines
		}
	}
	return nil
}

// filePreviewColumns is how many columns a tabular preview lays out: the widest
// record, capped at filePreviewMaxCols. Also reports how many were dropped, so the
// trailer can say so rather than quietly showing a slice of the file as if it were
// all of it.
func filePreviewColumns(rows [][]string) (n, dropped int) {
	for _, r := range rows {
		if len(r) > n {
			n = len(r)
		}
	}
	if n > filePreviewMaxCols {
		return filePreviewMaxCols, n - filePreviewMaxCols
	}
	return n, 0
}

// filePreviewAligns gives every column the left alignment a CSV has no way to
// declare. alignLeft is the zero value, so this is just the column count.
func filePreviewAligns(rows [][]string) []tableAlign {
	n, _ := filePreviewColumns(rows)
	return make([]tableAlign, n)
}

// filePreviewCells truncates each cell to filePreviewCellMax and pads every record
// out to the column count, since renderTableBox indexes by column.
func filePreviewCells(rows [][]string) [][]string {
	n, _ := filePreviewColumns(rows)
	out := make([][]string, len(rows))
	for i, r := range rows {
		row := make([]string, n)
		for c := 0; c < n; c++ {
			if c < len(r) {
				row[c] = ansi.Truncate(r[c], filePreviewCellMax, "…")
			}
		}
		out[i] = row
	}
	return out
}

// roundTablePreviewBox restyles renderTableBox's output as a preview box: rounded
// corners, and the caption set into the bottom rule the way boxFilePreview does
// it. Done as a post-process on the two rule lines rather than by teaching
// renderTableBox a second look, because that renderer is shared with Markdown
// pipe tables and those should keep the square corners they have always had.
//
// The rule's width is measured off the box renderTableBox actually drew, not off
// the pane: a table narrower than the pane (the common case — columns take their
// natural widths when they fit) would otherwise get a caption rule running well
// past its own right edge.
func roundTablePreviewBox(lines []string, captions []string) []string {
	if len(lines) < 2 {
		return lines
	}
	// tablePlainFallback (a pane too narrow for a box at all) has no rules to
	// restyle, so leave it alone — a caption on nothing would be misleading.
	if !strings.Contains(lines[0], "┌") {
		return lines
	}
	boxWidth := lipgloss.Width(lines[0]) - 2 // minus the two-cell body gutter
	lines[0] = strings.NewReplacer("┌", fpTopLeft, "┐", fpTopRight).Replace(lines[0])
	last := len(lines) - 1
	if len(captions) == 0 {
		lines[last] = strings.NewReplacer("└", fpBottomLeft, "┘", fpBottomRight).Replace(lines[last])
		return lines
	}
	lines[last] = "  " + filePreviewBorderStyle.Render(filePreviewBottomRule(boxWidth, captions...))
	return lines
}

// filePreviewTableTrailer describes what the box left out: the rows beyond the
// cap plus any this render had to drop to fit, and the columns past
// filePreviewMaxCols. Returns candidate wordings longest-first for the bottom
// rule, or nil when the box shows the whole file.
func filePreviewTableTrailer(e *filePreviewEntry, shown int) []string {
	rows := e.more + (len(e.rows) - shown)
	_, cols := filePreviewColumns(e.rows)
	switch {
	case rows > 0 && cols > 0:
		return []string{
			filePreviewMoreLabel(rows, "row") + ", " + strconv.Itoa(cols) + " more columns",
			filePreviewMoreLabel(rows, "row"),
			filePreviewShortLabel(rows),
		}
	case rows > 0:
		return []string{filePreviewMoreLabel(rows, "row"), filePreviewShortLabel(rows)}
	case cols > 0:
		return []string{filePreviewMoreLabel(cols, "column"), "+" + strconv.Itoa(cols) + "c"}
	}
	return nil
}

// filePreviewMoreLabel is the full wording of what a preview left out.
func filePreviewMoreLabel(n int, unit string) string {
	if n == 1 {
		return "… 1 more " + unit
	}
	return "… " + strconv.Itoa(n) + " more " + unit + "s"
}

// filePreviewShortLabel is the last-resort wording for a box too narrow to hold a
// sentence: just the count.
func filePreviewShortLabel(n int) string { return "+" + strconv.Itoa(n) }

// --- the Model side ------------------------------------------------------

// filePreviewsActive reports whether text/table previews are on. Unlike the image
// thumbnails there is no terminal capability to check — these are ordinary text
// lines, so any terminal can draw them.
func (m *Model) filePreviewsActive() bool { return m.filePrev.enabled() }

// filePreviewShape is the classification renderAttachments computes once per
// attachment and then hands to each of the three things that need it — the block
// itself, the file's icon, and the collapse chevron. Returns filePreviewNone when
// previews are off or the file is too big to fetch, so those three can all just
// ask "is there a shape here?" and cannot disagree about the answer.
//
// Computed once because it is not free: filePreviewKindOf ends in a chroma lexer
// lookup, and asking three times per attachment per uncached render is three times
// the price of asking once. (It is memoised — see lexerForFilename — but ~140ns
// times three times every attachment on screen is still worth not paying.)
func (m *Model) filePreviewShape(f *model.FileInfo) (filePreviewKind, chroma.Lexer) {
	if !m.filePreviewsActive() || f == nil || f.Size > filePreviewMaxBytes {
		return filePreviewNone, nil
	}
	return filePreviewKindOf(f)
}

// drawsFilePreview reports whether an attachment contributes a text/table preview
// to its post — the same role drawsFileThumb plays for images. For callers that
// need the boolean and not the shape.
func (m *Model) drawsFilePreview(f *model.FileInfo) bool {
	kind, _ := m.filePreviewShape(f)
	return kind != filePreviewNone
}

// filePreviewFileLines draws an attachment's text/table preview (used by
// renderAttachments, above the file's own filename line): the rendered block once
// its bytes have arrived, or the reserved blank rows while they are still coming.
// Nothing at all while the post's previews are collapsed — and nothing means
// nothing: the file is never sighted, so it is never fetched.
//
// kind/lexer come from filePreviewShape, so the caller pays for the classification
// once and the icon and chevron next to it reuse the same answer.
func (m *Model) filePreviewFileLines(p *model.Post, f *model.FileInfo, kind filePreviewKind, lex chroma.Lexer, paneWidth int) []string {
	if kind == filePreviewNone || m.thumbsHidden(p) {
		return nil
	}
	e := m.filePrev.sight(f, kind, lex)
	if e == nil {
		return nil
	}
	lines, settled := m.filePrev.render(e, paneWidth)
	if len(lines) > 0 {
		return lines
	}
	if settled {
		return nil // undecodable, or decoded to nothing: no rows, no reservation
	}
	// Hold the space the block will fill, so the post is its final height from its
	// very first render (see filePreviewReservedRows).
	rows := filePreviewReservedRows(kind)
	out := make([]string, rows)
	for i := range out {
		out[i] = ""
	}
	return out
}

// postHasFilePreview reports whether post p draws a text/table preview for any of
// its attachments — the question z asks to decide whether it is collapsing media
// or folding a body. Mirrors postThumbKeys' role for images.
func (m *Model) postHasFilePreview(p *model.Post) bool {
	if p == nil || p.Metadata == nil || !m.filePreviewsActive() {
		return false
	}
	for _, f := range p.Metadata.Files {
		if kind, _ := m.filePreviewShape(f); kind != filePreviewNone {
			return true
		}
	}
	return false
}

// filePreviewsFetchedMsg is the result of a background preview batch: parsed
// content per file id, plus the ids that are undecodable (never asked for again)
// and the ids that hit a transient error (forgotten, so a later sighting retries).
type filePreviewsFetchedMsg struct {
	ready  map[string]filePreviewContent
	failed []string
	retry  []string
}

// filePreviewContent is one parsed preview crossing back from the fetch goroutine.
type filePreviewContent struct {
	lines []string
	rows  [][]string
	more  int
}

// fetchPendingFilePreviews downloads and parses the sighted previews near enough
// to the viewport to be worth having, in the background. Run from Update after
// each event; nil when there is nothing to do.
//
// The viewport margin is not an optimisation here so much as the difference
// between a feature and a menace: renderMessages renders every post in the render
// window (up to 400), so *every* text attachment in it gets sighted, and fetching
// them all would mean downloading a few hundred files to show the two on screen.
// It reuses the thumbnails' own reach test, so both features agree on what "near"
// means and a collapsed post is excluded from both for free.
func (m *Model) fetchPendingFilePreviews() tea.Cmd {
	if !m.filePreviewsActive() {
		return nil
	}
	want := m.thumbKeysNearViewport(m.filePrev.pendingIDs())
	files := m.filePrev.takePending(want)
	if len(files) == 0 {
		return nil
	}
	snap := m // value copy: the Cmd runs on another goroutine
	return func() tea.Msg {
		return snap.loadFilePreviews(files)
	}
}

// loadFilePreviews fetches and parses a batch of previews. Runs on a background
// goroutine, so all the work — download, sniff, split, highlight-free parse — is
// off the UI thread; only the finished lines cross back.
func (m Model) loadFilePreviews(files []*model.FileInfo) tea.Msg {
	msg := filePreviewsFetchedMsg{ready: make(map[string]filePreviewContent, len(files))}
	for _, f := range files {
		if f == nil {
			continue
		}
		path, _ := m.cachedFilePath(f)
		raw, err := m.readOrDownloadFile(path, f)
		if err != nil {
			msg.retry = append(msg.retry, f.Id)
			continue
		}
		if !looksLikeText(raw) {
			msg.failed = append(msg.failed, f.Id)
			continue
		}
		kind, _ := filePreviewKindOf(f)
		if kind == filePreviewTable {
			rows, more := parseFileTablePreview(raw, filePreviewDelimiter(f))
			if len(rows) >= 2 {
				msg.ready[f.Id] = filePreviewContent{rows: rows, more: more}
				continue
			}
			// Not really tabular (one column, or a single line): fall through and
			// show it as text rather than nothing.
		}
		lines, more := parseFileTextPreview(raw)
		if len(lines) == 0 {
			msg.failed = append(msg.failed, f.Id)
			continue
		}
		msg.ready[f.Id] = filePreviewContent{lines: lines, more: more}
	}
	return msg
}

// handleFilePreviewsFetched installs a finished batch and drops the cached lines
// of every post that owns one, so the next render picks the block up — the post
// fingerprint doesn't track preview readiness, exactly as it doesn't track
// thumbnail readiness (see invalidatePostsForThumbs).
func (m Model) handleFilePreviewsFetched(msg filePreviewsFetchedMsg) (Model, tea.Cmd) {
	touched := make(map[string]struct{}, len(msg.ready)+len(msg.failed))
	for id, c := range msg.ready {
		// A table that parsed as text arrives with lines and no rows; markReady
		// takes whichever is set, and the entry's kind is corrected here so the
		// renderer picks the matching path.
		if len(c.rows) == 0 {
			m.filePrev.setKind(id, filePreviewText)
		}
		m.filePrev.markReady(id, c.lines, c.rows, c.more)
		touched[id] = struct{}{}
	}
	m.filePrev.markFailed(msg.failed...)
	for _, id := range msg.failed {
		touched[id] = struct{}{}
	}
	m.filePrev.markRetry(msg.retry...)
	if len(touched) > 0 {
		m.invalidatePostsForThumbs(touched)
	}
	return m, nil
}
