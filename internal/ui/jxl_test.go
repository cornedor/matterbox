package ui

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestLooksLikeJXL: both container forms, and — the point of having a separate
// sniff at all — that neither is something looksLikeVideo would have caught.
func TestLooksLikeJXL(t *testing.T) {
	naked := []byte{0xFF, 0x0A, 0x07, 0x07, 0x00, 0x25}
	boxed := []byte{0x00, 0x00, 0x00, 0x0C, 0x4A, 0x58, 0x4C, 0x20, 0x0D, 0x0A, 0x87, 0x0A, 0x00}
	for _, b := range [][]byte{naked, boxed} {
		if !looksLikeJXL(b) {
			t.Errorf("looksLikeJXL(% x) = false, want true", b[:4])
		}
		if looksLikeVideo(b) {
			t.Errorf("looksLikeVideo(% x) = true — then JXL would need no sniff of its own", b[:4])
		}
	}
	for _, b := range [][]byte{
		{},
		{0xFF},
		{0xFF, 0x0B}, // near-miss on the naked magic
		{0x00, 0x00, 0x00, 0x0C, 0x4A, 0x58, 0x4C, 0x21}, // near-miss on the box type
		[]byte("\x89PNG\r\n\x1a\n"),
	} {
		if looksLikeJXL(b) {
			t.Errorf("looksLikeJXL(% x) = true, want false", b)
		}
	}
}

// TestJXLClaimsFollowTheRuntimeProbe is the invariant that keeps this format
// honest. Every other libav format is settled by the build tag, but libjxl is an
// optional ffmpeg library, so the same tagged binary may or may not have it —
// and claiming a .jxl we cannot decode would reserve thumbnail rows for an image
// that never arrives.
func TestJXLClaimsFollowTheRuntimeProbe(t *testing.T) {
	want := jxlDecodable()
	for _, f := range []*model.FileInfo{
		{Name: "photo.jxl"},
		{Name: "photo.JXL"},
		{Name: "upload", MimeType: "image/jxl"},
		{Name: "upload", MimeType: "image/jpegxl"},
	} {
		if got := isStillImageAttachment(f); got != want {
			t.Errorf("isStillImageAttachment(%q/%q) = %v, want %v (jxlDecodable)", f.Name, f.MimeType, got, want)
		}
		// The animation claim has to track the same probe: without the gate a
		// session that can animate would claim a format it has no decoder for.
		if got := isVideoAttachment(f); got != want {
			t.Errorf("isVideoAttachment(%q/%q) = %v, want %v (jxlDecodable)", f.Name, f.MimeType, got, want)
		}
	}
	if got := previewableMIME("image/jxl"); got != want {
		t.Errorf("previewableMIME(image/jxl) = %v, want %v", got, want)
	}
	if got := isStillImageURL("https://example.com/a.jxl?v=2"); got != want {
		t.Errorf("isStillImageURL(.jxl) = %v, want %v", got, want)
	}
	// Never routed to libav in a build that cannot decode it.
	if got := routesToLibav([]byte{0xFF, 0x0A, 0x07, 0x07}); got != want {
		t.Errorf("routesToLibav(JXL) = %v, want %v", got, want)
	}
}

// TestJXLIsNotTextPreviewed: a .jxl is an image, so the text-preview classifier
// must leave it alone — including in a build that cannot decode it, where drawing
// a binary's bytes as source would be the worst of both.
func TestJXLIsNotTextPreviewed(t *testing.T) {
	if kind, _ := filePreviewKindOf(&model.FileInfo{Name: "photo.jxl", Size: 400}); kind != filePreviewNone {
		t.Errorf("filePreviewKindOf(.jxl) = %v, want none", kind)
	}
}

// TestAnimatedImagesDoNotStreamLikeClips: the streaming player stops on its last
// frame, which is right for a video and wrong for an animated image — a WebP,
// AVIF or JXL stands in for a GIF and has to loop like one. So they must take the
// whole-decode path, which loops on both animation routes.
func TestAnimatedImagesDoNotStreamLikeClips(t *testing.T) {
	// A session that can stream: video build, native animation, preview animation.
	m := &Model{nativeAnim: true, animatePreview: true}
	if !videoBuild {
		// Without libav nothing streams at all, so there is nothing to separate.
		if m.streamsPreviewVideo(previewItem{file: &model.FileInfo{Name: "clip.mp4"}}) {
			t.Error("streamsPreviewVideo = true without the video tag")
		}
		return
	}
	for _, name := range []string{"loop.webp", "loop.avif", "loop.jxl", "LOOP.AVIF"} {
		f := &model.FileInfo{Id: "f", Name: name, Size: 4096}
		if !isAnimatedImageAttachment(f) {
			t.Errorf("isAnimatedImageAttachment(%q) = false", name)
		}
		if m.streamsPreviewVideo(previewItem{file: f, name: name}) {
			t.Errorf("%q would stream as a clip, so it would stop instead of looping", name)
		}
	}
	// A real clip still streams — that is what the path is for.
	for _, name := range []string{"clip.mp4", "clip.webm", "clip.mov"} {
		f := &model.FileInfo{Id: "f", Name: name, Size: 4096}
		if isAnimatedImageAttachment(f) {
			t.Errorf("isAnimatedImageAttachment(%q) = true, want false", name)
		}
		if !m.streamsPreviewVideo(previewItem{file: f, name: name}) {
			t.Errorf("%q no longer streams", name)
		}
	}
	if isAnimatedImageAttachment(nil) {
		t.Error("isAnimatedImageAttachment(nil) = true")
	}
	// By MIME alone, for the uploads that carry no usable filename.
	for _, mime := range []string{"image/webp", "image/avif", "image/jxl"} {
		if !isAnimatedImageAttachment(&model.FileInfo{Name: "upload", MimeType: mime}) {
			t.Errorf("isAnimatedImageAttachment(mime %q) = false", mime)
		}
	}
}
