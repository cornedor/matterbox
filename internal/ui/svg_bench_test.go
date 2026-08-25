package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// benchAttachFiles is the attachment mix a busy channel actually holds: mostly
// screenshots, the odd document, one drawing. Every one of them pays the SVG
// recognition check on a cache-miss render, so the interesting number is the
// cost across the whole mix rather than for a drawing alone.
func benchAttachFiles() []*model.FileInfo {
	return []*model.FileInfo{
		{Id: "1", Name: "screenshot-2026-08-25.png", Extension: "png", MimeType: "image/png", Size: 240 << 10, Width: 1920, Height: 1080},
		{Id: "2", Name: "notes.pdf", Extension: "pdf", MimeType: "application/pdf", Size: 90 << 10},
		{Id: "3", Name: "capture.jpg", Extension: "jpg", MimeType: "image/jpeg", Size: 512 << 10, Width: 3000, Height: 2000},
		{Id: "4", Name: "architecture.svg", Extension: "svg", MimeType: "image/svg+xml", Size: 24 << 10},
		{Id: "5", Name: "logs.txt", Extension: "txt", MimeType: "text/plain", Size: 4 << 10},
	}
}

// BenchmarkIsSVGAttachment measures the recognition check on its own, across the
// three shapes it has to handle: a correct MIME type, a file the server left
// untyped (extension only), and the common case of something that is not a
// drawing at all and only pays for finding that out.
func BenchmarkIsSVGAttachment(b *testing.B) {
	cases := []struct {
		name string
		f    *model.FileInfo
	}{
		{"mime", &model.FileInfo{Name: "a.svg", Extension: "svg", MimeType: "image/svg+xml"}},
		{"extension_only", &model.FileInfo{Name: "a.svg", Extension: "svg"}},
		{"filename_only", &model.FileInfo{Name: "a.svg"}},
		{"png_miss", &model.FileInfo{Name: "shot.png", Extension: "png", MimeType: "image/png"}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = isSVGAttachment(tc.f)
			}
		})
	}
}

// BenchmarkDrawsFileThumb is the question the transcript asks once per attachment
// on every cache-miss render (and again for the collapse chevron), so it is where
// SVG support taxes posts that carry no drawing at all.
func BenchmarkDrawsFileThumb(b *testing.B) {
	m := &Model{}
	files := benchAttachFiles()
	b.ReportAllocs()
	for b.Loop() {
		for _, f := range files {
			_ = m.drawsFileThumb(f)
		}
	}
}

// BenchmarkFilePreviewable covers the same check from the preview/info side.
func BenchmarkFilePreviewable(b *testing.B) {
	m := &Model{}
	files := benchAttachFiles()
	b.ReportAllocs()
	for b.Loop() {
		for _, f := range files {
			_ = m.filePreviewable(f)
		}
	}
}

// BenchmarkThumbItems measures the enumeration the click target and the collapse
// bookkeeping share — which now also filters out drawings too big to render.
func BenchmarkThumbItems(b *testing.B) {
	m := &Model{}
	p := &model.Post{Id: "p1", Metadata: &model.PostMetadata{Files: benchAttachFiles()}}
	b.ReportAllocs()
	for b.Loop() {
		_ = m.thumbItems(p)
	}
}

// BenchmarkRenderAttachments is the real one: the whole attachment block for a
// post, which is what a cache miss actually pays. Thumbnails are off (no
// inlineImg), so this isolates the recognition and line-building work from any
// image decode.
func BenchmarkRenderAttachments(b *testing.B) {
	m := &Model{}
	p := &model.Post{Id: "p1", Metadata: &model.PostMetadata{Files: benchAttachFiles()}}
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderAttachments(p, 80)
	}
}
