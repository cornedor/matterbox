package ui

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// BenchmarkFilePreviewKindOf guards the render path. Classification is asked of
// every attachment on every uncached post render (for the block, the icon and the
// chevron), and it used to cost 2.4ms because chroma's lexers.Match glob-matches
// every lexer it knows. With the memo in highlight.go it is ~140ns. If this
// regresses to microseconds, something has stopped hitting the cache.
func BenchmarkFilePreviewKindOf(b *testing.B) {
	f := &model.FileInfo{Id: "f1", Name: "deploy.log", Size: 500}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = filePreviewKindOf(f)
	}
}

// BenchmarkFilePreviewKindOfImage is the common case: a file another renderer
// owns bails before any lexer lookup at all.
func BenchmarkFilePreviewKindOfImage(b *testing.B) {
	f := &model.FileInfo{Id: "f1", Name: "photo.png", MimeType: "image/png", Size: 500}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = filePreviewKindOf(f)
	}
}

// BenchmarkRenderFilePreviewText measures one uncached (file, width) render — the
// chroma pass. The store's one-slot cache means this runs on a resize, not on a
// keystroke, so microseconds are fine and milliseconds would not be.
func BenchmarkRenderFilePreviewText(b *testing.B) {
	lines, more := parseFileTextPreview([]byte(strings.Repeat("2026-08-25 INFO something happened\n", 40)))
	e := &filePreviewEntry{kind: filePreviewText, lexer: lexerForFilename("x.log"), fetched: true, lines: lines, more: more}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = renderFilePreview(e, 80)
	}
}

// BenchmarkRenderFilePreviewTable measures the same for a box table, at a width
// wide enough that the fit loop does not iterate.
func BenchmarkRenderFilePreviewTable(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("region,orders,revenue\n")
	for i := 0; i < 40; i++ {
		sb.WriteString("Benelux,1420,184300.55\n")
	}
	rows, more := parseFileTablePreview([]byte(sb.String()), ',')
	e := &filePreviewEntry{kind: filePreviewTable, fetched: true, rows: rows, more: more}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = renderFilePreview(e, 80)
	}
}
